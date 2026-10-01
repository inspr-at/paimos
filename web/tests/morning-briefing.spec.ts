// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { setupUsage, NOW } from './usage-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { watchErrors } from './work-fixtures'

const START = new Date(NOW - 24 * 3600_000).toISOString()
const AT = new Date(NOW - 3600_000).toISOString()
const uuid = '45400000-0000-4000-8000-000000000001'
const outcome = (id: string, kind: string, payload: Record<string, unknown>) => ({ id, kind, ticket_node_id: 'n-a1', ticket_key: 'AEON-1', project_id: 'p-aeon', session_id: null, rules_version: null, release_title: null, payload, recorded_at: AT })
test.use({ timezoneId: 'Europe/Vienna' })
async function setup(page: Page, options: { failure?: boolean; noCost?: boolean; dark?: boolean; empty?: boolean; slow?: boolean; permissionFailure?: 'workspace' | 'project' } = {}) {
  await setupUsage(page, { variant: 'reported', theme: options.dark ? 'dark' : 'light' })
  const writes: Record<string, unknown>[] = []
  let preference: Record<string, unknown> = { time: '08:00', last_visit: START }
  await page.route('**/api/preferences/morning-briefing', async route => {
    if (route.request().method() === 'PUT') { preference = route.request().postDataJSON().value; writes.push(preference) }
    await route.fulfill({ json: { key: 'morning-briefing', value: preference } })
  })
  const reads: string[] = []
  await page.route('**/api/outcomes?**', async route => {
    reads.push(route.request().url())
    if (options.slow) await new Promise(resolve => setTimeout(resolve, 400))
    if (options.failure) return route.fulfill({ status: 500, json: { error: 'unavailable' } })
    const cursor = new URL(route.request().url()).searchParams.get('cursor')
    await route.fulfill({ json: { outcomes: options.empty ? [] : cursor ? [outcome('out-review', 'review_verdict', { verdict: 'changes', summary: 'Tenant isolation needs a fix' })] : [outcome('out-done', 'ticket_done', { to_state: 'delivered' }), outcome('out-release', 'released', { version: '260929110000.0.0' })], next_cursor: options.empty || cursor ? null : 'page-2' } })
  })
  await page.route('**/api/nodes?**', route => {
    const ids = new URL(route.request().url()).searchParams.get('ids')?.split(',') ?? []
    if (!ids.includes('work-1')) return route.fallback()
    const rows = [
      { id: 'work-1', key: 'WO-454', kind_slug: 'work_order', parent: { id: 'n-a1', key: 'AEON-1', title: 'Aeon foundation', kind_slug: 'ticket' } },
      { id: 'n-a1', key: 'AEON-1', kind_slug: 'ticket', parent: null },
    ].filter(n => ids.includes(n.id)).map(n => ({ ...n, project: { id: 'p-aeon', key: 'PRJ-35', title: 'Aeon' }, fields: {}, title: n.key, state: 'done', created_at: AT, updated_at: AT }))
    return route.fulfill({ json: { items: rows, next_cursor: null } })
  })
  await page.route('**/api/events?**', route => {
    const query = new URL(route.request().url()).searchParams
    const run = query.get('type')?.includes('run.telemetry')
    return route.fulfill({ json: { items: options.empty || !run ? [] : [{ id: 454, node_id: 'work-1', type: 'run.telemetry', before: null, after: { report: { kind: 'finished' }, run: { status: 'completed', outcome_detail: 'merged' } }, at: AT }], next_after: null, next_cursor: null, ...(query.get('briefing') === 'true' ? { window: { from: START, to: new Date(NOW).toISOString(), first: false, capped: false } } : {}) } })
  })
  await page.route('**/api/approvals?**', route => route.fulfill({ json: options.empty ? [] : [{ id: uuid, agent_principal_id: uuid, scope: 'nodes.write', resource_kind: 'node', resource_id: 'n-a1', rationale: 'Check the delivery evidence', proposed_at: AT, expires_at: new Date(NOW + 3600_000).toISOString(), decision: null, risk: 'low' }] }))
  await page.route('**/api/projects/*/messages?**', route => route.fulfill({ json: { items: [], next_after: 0 } }))
  await page.route('**/api/journey/next-actions?**', route => route.fulfill({ json: { items: [] } }))
  let permissionFailure = options.permissionFailure
  if (options.noCost || permissionFailure) {
    await page.route('**/api/me/permissions*', route => {
      const projectId = new URL(route.request().url()).searchParams.get('project_id') ?? undefined
      if (permissionFailure === 'workspace' && !projectId || permissionFailure === 'project' && projectId) return route.fulfill({ status: 500, json: { error: 'unavailable' } })
      const result = mockEffectivePermissions('admin', projectId)
      if (options.noCost) {
        result.workspace.permissions = result.workspace.permissions.filter(p => p !== 'harness.read')
        if (result.project) result.project.permissions = result.project.permissions.filter(p => p !== 'harness.read')
      }
      return route.fulfill({ json: result })
    })
    // Even an overbroad mocked source may never paint a merge or money without cost access.
  }
  return { writes, reads, preference: () => preference, recoverPermissions: () => { permissionFailure = undefined } }
}

test('person briefing cites existing facts, includes the second log page and advances only on success', async ({ page }) => {
  const errors = watchErrors(page), data = await setup(page)
  await page.goto('/briefing')
  await expect(page.getByRole('heading', { name: 'Morning briefing', level: 1 })).toBeVisible()
  await expect(page.getByRole('link', { name: 'AEON-1 · Marked delivered' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'AEON-1 · Merge reported' })).toBeVisible()
  await expect(page.getByText('Tenant isolation needs a fix', { exact: true })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Recommended next step' }).getByRole('link').first()).toHaveAttribute('href', `/agents?needs=a%3A${uuid}`)
  await expect(page.getByRole('link', { name: 'Source event' })).toHaveAttribute('href', '/api/events?node_id=work-1&after=453&limit=1')
  await expect(page.getByRole('link', { name: 'Source outcome' }).first()).toHaveAttribute('href', '/api/outcomes?ticket_node_id=n-a1&outcome_id=out-done&limit=1')
  await expect(page.getByText('Lifetime usage of sessions started in this window.', { exact: false })).toBeVisible()
  await expect.poll(() => data.writes.length).toBe(1)
  const end = new URL(data.reads[0]!).searchParams.get('to')
  expect(data.writes[0]).toEqual({ time: '08:00', last_visit: end })
  expect(new URL(data.reads[0]!).searchParams.get('from')).toBe(START)
  expect(data.reads[1]).toContain('cursor=page-2')
  await page.getByLabel('Daily reminder at').fill('09:30')
  await page.getByLabel('Daily reminder at').blur()
  await expect.poll(() => data.preference().time).toBe('09:30')
  expect(data.preference().last_visit).toBe(end)
  expect(errors).toEqual([])
})

test('source failure is visible and keeps the person’s earlier cutoff', async ({ page }) => {
  const data = await setup(page, { failure: true })
  await page.goto('/briefing')
  await expect(page.getByText('Outcome history could not be loaded. Retry to include it.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Refresh' })).toBeEnabled()
  expect(data.writes).toEqual([])
  expect(data.preference().last_visit).toBe(START)
})

for (const permissionFailure of ['workspace', 'project'] as const) test(`${permissionFailure} permission read failure keeps the cutoff and can be retried`, async ({ page }) => {
  const data = await setup(page, { permissionFailure })
  await page.goto('/briefing')
  if (permissionFailure === 'workspace') await expect(page.getByText('Permissions could not be loaded. Try again before reading the briefing.')).toBeVisible()
  else await expect(page.getByLabel('Briefing coverage')).toContainText('Access in')
  expect(data.writes).toEqual([])
  expect(data.preference().last_visit).toBe(START)
  data.recoverPermissions()
  await page.getByRole('button', { name: permissionFailure === 'workspace' ? 'Try again' : 'Refresh', exact: true }).click()
  await expect(page.getByRole('link', { name: 'AEON-1 · Marked delivered' })).toBeVisible()
  await expect.poll(() => data.writes.length).toBe(1)
})

test('without harness.read no usage request, money or merged run evidence is shown', async ({ page }) => {
  const data = await setup(page, { noCost: true })
  const usageCalls: string[] = [], eventCalls: string[] = []
  page.on('request', request => { if (request.url().includes('/usage/dashboard')) usageCalls.push(request.url()); if (request.url().includes('/api/events?')) eventCalls.push(request.url()) })
  await page.goto('/briefing')
  await expect(page.getByRole('link', { name: 'AEON-1 · Marked delivered' })).toBeVisible()
  await expect(page.locator('.briefing-page')).not.toContainText('$')
  await expect(page.getByRole('link', { name: 'AEON-1 · Merge reported' })).toHaveCount(0)
  expect(usageCalls).toEqual([])
  expect(new URL(eventCalls[0]!).searchParams.get('type')).toBe('node.updated')
  await expect.poll(() => data.writes.length).toBe(1)
})

test('empty complete window is quiet and offers no invented next step', async ({ page }) => {
  await setup(page, { empty: true })
  await page.goto('/briefing')
  await expect(page.getByText('No completion or delivery recorded in this window.')).toBeVisible()
  await expect(page.getByText('No person action in the sources that answered.')).toBeVisible()
  await expect(page.getByRole('region', { name: 'Recommended next step' })).toHaveCount(0)
})

test('mobile dark briefing fits, has no colored edge accents and keeps all sections usable', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page, { dark: true })
  await page.goto('/briefing')
  await expect(page.getByRole('heading', { name: 'What it cost' })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
  const edges = await page.locator('.briefing-page *').evaluateAll(els => els.filter(el => { const style = getComputedStyle(el); return ['Left', 'Top'].some(side => parseFloat(style[`border${side}Width` as 'borderLeftWidth']) >= 3 && style[`border${side}Style` as 'borderLeftStyle'] !== 'none') }).length)
  expect(edges).toBe(0)
  await page.screenshot({ path: '../.agent-shots/morning-briefing-mobile-dark.png', fullPage: true })
  expect(errors).toEqual([])
})

test('Projects links to the daily briefing and reading it clears the due reminder', async ({ page }) => {
  await setup(page)
  await page.goto('/')
  const reminder = page.getByRole('link', { name: 'Your morning briefing is ready What finished · what needs you · usage' })
  await expect(reminder).toBeVisible()
  await reminder.click()
  await expect(page.getByRole('link', { name: 'AEON-1 · Marked delivered' })).toBeVisible()
  await page.goto('/')
  await expect(page.getByRole('link', { name: 'Morning briefing', exact: true })).toBeVisible()
  await expect(page.getByText('Your morning briefing is ready')).toHaveCount(0)
})


test('a briefing action focuses its existing approval card when Agents was already loaded', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await page.goto('/briefing')
  const next = () => page.getByRole('region', { name: 'Recommended next step' }).getByRole('link').first()
  await next().click()
  const request = page.locator(`[data-row="a:${uuid}"]`)
  await expect(request).toBeFocused()
  // A client-side return preserves the Agents store. Mounting the same link again still focuses it.
  await page.goBack()
  await expect(next()).toBeVisible()
  await next().click()
  await expect(request).toBeFocused()
  expect(errors).toEqual([])
})

test('leaving during a slow source read never advances the visit marker', async ({ page }) => {
  const data = await setup(page, { slow: true })
  await page.goto('/briefing')
  await expect(page.getByText('Reading your briefing sources…')).toBeVisible()
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible()
  await page.waitForTimeout(500)
  expect(data.writes).toEqual([])
  expect(data.preference().last_visit).toBe(START)
})
