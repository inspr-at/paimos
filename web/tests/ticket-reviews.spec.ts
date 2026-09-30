// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

const base = 'a'.repeat(40), head = 'b'.repeat(40)
const review = {
  ticket_node_id: 'n-1', work_order_id: 'review-order', request_id: 'review-request', run_id: 'review-run',
  repository: 'example/review-fixture', base_sha: base, head_sha: head, author_run_id: null, author_family: 'openai',
  reviewer_profile_id: 'review-profile', reviewer_family: 'anthropic', reviewer_model: 'review-model', reviewer_effort: 'xhigh', reviewer_profile_version: 'pinned-version', effective_model: 'review-model', pull_request: null,
  status: 'completed', gate_open: false, gate_reason: 'The reviewer requested changes.', github_status: 'unconfigured',
  result: { verdict: 'changes', reason: '', findings: [{ severity: 'high', file: 'internal/crossreview/module.go', line: 42, message: 'Recheck the tenant boundary before recording the result.' }] },
  ladder: [{ profile: { family: 'openai' }, skip_reasons: ['author family cannot review itself'], selected: false }, { profile: { family: 'xai' }, skip_reasons: ['no qualified account available'], selected: false }],
  cost_micros: 125000, duration_seconds: 95, created_at: '2026-09-30T12:00:00Z',
}
async function setup(page: Page, writable = true) {
  await mockWork(page, fixtures())
  await page.route('**/api/me/permissions*', route => {
    const project = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
    const grants = mockEffectivePermissions('member', project)
    grants.workspace.permissions.push('work_orders.read', ...(writable ? ['run.create'] : []))
    if (grants.project) grants.project.permissions.push('work_orders.read', ...(writable ? ['run.create'] : []))
    return route.fulfill({ json: grants })
  })
  await page.route('**/api/nodes/*/reviews', route => route.fulfill({ json: [review] }))
}
const section = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' }).getByRole('region', { name: 'Cross-family review' })

test('findings and honest fallback reasons fit desk and phone in both themes', async ({ page }) => {
  await setup(page)
  const output = process.env.REVIEW_SHOTS
  if (output) await mkdir(output, { recursive: true })
  for (const width of [1600, 390]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme as 'light' | 'dark' })
    await page.goto('/p/PHAROS/PHAROS-11')
    const region = section(page)
    await expect(region).toContainText('Changes needed')
    await expect(region).toContainText('internal/crossreview/module.go:42')
    await region.getByText('Review route', { exact: true }).click()
    await expect(region).toContainText('author family cannot review itself')
    await expect(region).toContainText('no qualified account available')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)).toBe(true)
    await region.scrollIntoViewIfNeeded()
    if (output) await region.screenshot({ path: `${output}/review-${width}-${theme}.png` })
  }
})

test('request is tied to exact commits; an uncertain retry retains its request id', async ({ page }) => {
  await setup(page)
  const bodies: Record<string, unknown>[] = []
  await page.route('**/api/nodes/*/reviews', async route => {
    if (route.request().method() === 'GET') return route.fulfill({ json: [] })
    bodies.push(route.request().postDataJSON())
    if (bodies.length === 1) return route.fulfill({ status: 503, json: { error: 'Temporarily unavailable' } })
    return route.fulfill({ status: 201, json: { ...review, status: 'queued', result: { verdict: '', reason: '', findings: [] }, gate_reason: 'Awaiting independent review.' } })
  })
  await page.goto('/p/PHAROS/PHAROS-11')
  const region = section(page)
  await region.getByText('Review a commit range', { exact: true }).click()
  await region.getByLabel('Repository', { exact: true }).fill('example/review-fixture')
  await region.getByLabel('Base commit').fill(base)
  await region.getByLabel('Head commit').fill(head)
  await region.getByLabel('Author family').selectOption('openai')
  await region.getByRole('button', { name: 'Request review', exact: true }).click()
  await expect(region.getByRole('alert')).toBeVisible()
  await region.getByRole('button', { name: 'Request review', exact: true }).click()
  await expect(region).toContainText('Awaiting independent review.')
  expect(bodies).toHaveLength(2)
  expect(bodies[0]).toEqual(bodies[1])
  expect(bodies[1]).toMatchObject({ repository: review.repository, base_sha: base, head_sha: head, author_family: 'openai' })
})

test('readers can inspect a verdict without a request action', async ({ page }) => {
  await setup(page, false)
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(section(page)).toContainText('Changes needed')
  await expect(section(page).getByText('Review a commit range', { exact: true })).toHaveCount(0)
})

test('epics do not offer a review action that the server cannot accept', async ({ page }) => {
  await setup(page)
  let reads = 0
  await page.route('**/api/nodes/*/reviews', route => { reads++; return route.fulfill({ json: [] }) })
  await page.goto('/p/PHAROS/PHAROS-10')
  await expect(page.getByRole('complementary', { name: 'Ticket details' }).getByRole('heading', { name: 'Guarded multi-cloud provisioning' })).toBeVisible()
  await expect(section(page)).toHaveCount(0)
  expect(reads).toBe(0)
})
