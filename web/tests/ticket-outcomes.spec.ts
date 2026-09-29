// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'

const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const at = Date.parse('2026-09-23T12:00:00Z')

const outcomes = {
  outcomes: [
    {
      id: 'o-1', kind: 'review_verdict', ticket_node_id: 'n-1', ticket_key: 'PHAROS-11', project_id: 'p-pharos',
      session_id: null, rules_version: null, release_node_id: null, release_key: null, release_title: null,
      source: 'recorded', actor_principal_id: 'a-1', recorded_at: new Date(at - 2 * 60 * 60_000).toISOString(), idempotency_key: 'review-1',
      payload: { verdict: 'pass', reviewer_model: 'codex', route: 'backend', round: 2, findings: 1, summary: 'One naming mismatch in the release note, otherwise the benefit text is ready to ship with the candidate.' },
    },
    {
      id: 'o-2', kind: 'ci_result', ticket_node_id: 'n-1', ticket_key: 'PHAROS-11', project_id: 'p-pharos',
      session_id: null, rules_version: null, release_node_id: null, release_key: null, release_title: null,
      source: 'recorded', actor_principal_id: 'a-1', recorded_at: new Date(at - 4 * 60 * 60_000).toISOString(), idempotency_key: 'ci-1',
      payload: { result: 'fail', name: 'web' },
    },
    {
      id: 'o-3', kind: 'released', ticket_node_id: 'n-1', ticket_key: 'PHAROS-11', project_id: 'p-pharos',
      session_id: null, rules_version: null, release_node_id: 'rel-1', release_key: 'PHAROS-90', release_title: 'September release',
      source: 'automatic', actor_principal_id: 'a-1', recorded_at: new Date(at - 5 * 60 * 60_000).toISOString(), idempotency_key: 'rel-1',
      payload: { version: '260929120000.0.0' },
    },
  ],
}

test('a ticket with no outcomes leaves the section out', async ({ page }) => {
  await mockWork(page, fixtures())
  const read = page.waitForResponse(response => new URL(response.url()).pathname === '/api/outcomes')
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(panel(page).getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  await read
  await expect(panel(page).getByRole('region', { name: 'Outcomes' })).toHaveCount(0)
})

test('outcomes stay inside the ticket panel at phone and desk widths', async ({ page }) => {
  await page.clock.setSystemTime(at)
  await mockWork(page, fixtures())
  await page.route('**/api/outcomes*', route => route.fulfill({ json: outcomes }))
  for (const width of [1600, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto('/p/PHAROS/PHAROS-11')
    const section = panel(page).getByRole('region', { name: 'Outcomes' })
    await expect(section).toBeVisible()
    await expect(section).toContainText('Review passed')
    await expect(section).toContainText('CI failed')
    await expect(section).toContainText('Released in September release')
    await expect(section).toContainText('260929120000.0.0')
    await expect(section).not.toContainText('rel-1')
    await expect(section.locator('.detail').first()).toHaveAttribute('title', /naming mismatch/)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)).toBe(true)
  }
})
