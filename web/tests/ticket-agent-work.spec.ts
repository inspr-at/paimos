// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'

const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })

const agentWork = {
  node_id: 'n-epic',
  kind: 'epic',
  currency: 'USD',
  usage_available: true,
  includes_descendants: true,
  scope_truncated: false,
  list_truncated: false,
  sessions: [
    {
      id: '5e000000-0000-4000-8000-000000000001',
      ticket_node_id: 'n-1',
      ticket_key: 'PHAROS-11',
      ticket_title: 'Connect Hetzner Cloud for managed provisioning',
      harness: 'codex',
      label: 'Harbor worker',
      model: 'gpt-5.4',
      model_state: 'known',
      effort: 'high',
      effort_state: 'known',
      phase: 'stopped',
      started_at: '2026-09-27T10:00:00Z',
      ended_at: '2026-09-27T10:02:30Z',
      duration_seconds: 150,
      duration_state: 'known',
      usage_reported: true,
      models: [
        {
          model: 'gpt-5.4',
          input_tokens: '1200',
          output_tokens: '80',
          cached_input_tokens: '15',
          tokens_state: 'known',
          cached_state: 'known',
          estimated_cost_usd: '1.500000000000',
          cost_state: 'estimated',
          provisional: true,
          price_version: '3',
          billing_mode: 'subscription',
          subscription_label: 'Reported team',
        },
        {
          model: 'gpt-5.4-mini',
          input_tokens: '10',
          output_tokens: '2',
          cached_input_tokens: '0',
          tokens_state: 'known',
          cached_state: 'known',
          estimated_cost_usd: '0.250000000000',
          cost_state: 'estimated',
          provisional: false,
          price_version: '3',
          billing_mode: 'api',
          subscription_label: null,
        },
      ],
      models_truncated: false,
      input_tokens: '1210',
      output_tokens: '82',
      cached_input_tokens: '15',
      tokens_state: 'known',
      cached_state: 'known',
      estimated_cost_usd: '1.750000000000',
      cost_state: 'provisional',
      unknown_token_models: 0,
      unknown_cost_models: 0,
    },
    {
      id: '5e000000-0000-4000-8000-000000000002',
      ticket_node_id: 'n-epic',
      ticket_key: 'PHAROS-10',
      ticket_title: 'Guarded multi-cloud provisioning',
      harness: 'cursor',
      label: null,
      model: null,
      model_state: 'missing',
      effort: null,
      effort_state: 'missing',
      phase: 'working',
      started_at: '2026-09-27T11:00:00Z',
      ended_at: null,
      duration_seconds: 45,
      duration_state: 'ongoing',
      usage_reported: false,
      models: [],
      models_truncated: false,
      input_tokens: null,
      output_tokens: null,
      cached_input_tokens: null,
      tokens_state: 'unknown',
      cached_state: 'unknown',
      estimated_cost_usd: null,
      cost_state: 'unknown',
      unknown_token_models: 0,
      unknown_cost_models: 0,
    },
  ],
  totals: {
    session_count: 2,
    input_tokens: '1210',
    output_tokens: '82',
    cached_input_tokens: '15',
    tokens_state: 'partial',
    cached_state: 'partial',
    estimated_cost_usd: '1.750000000000',
    cost_state: 'partial',
    currency: 'USD',
    duration_seconds: 195,
    duration_state: 'partial',
    unknown_token_sessions: 1,
    unknown_cost_sessions: 1,
    unknown_token_models: 0,
    unknown_cost_models: 0,
  },
}

async function openEpic(page: Page) {
  await mockWork(page, fixtures())
  await page.route('**/api/nodes/n-epic/agent-work', route => route.fulfill({ json: agentWork }))
  await page.goto('/p/PHAROS/PHAROS-10')
  const work = panel(page).getByRole('region', { name: 'Agent work' })
  await expect(work).toBeVisible()
  return work
}

test('an epic shows sessions with reported figures and leaves missing ones unknown', async ({ page }) => {
  const work = await openEpic(page)
  await expect(work).toContainText('Totals include this epic and the tickets and tasks under it.')
  await expect(work).toContainText('Totals add only reported figures.')
  await expect(work).toContainText('2 sessions')
  await expect(work).toContainText('1,210 in')
  await expect(work).toContainText('$1.75, incomplete')
  await expect(work).toContainText('at least 3m 15s')
  await expect(work.getByRole('link', { name: 'Harbor worker. Open the session' })).toHaveAttribute('href', '/agents/5e000000-0000-4000-8000-000000000001')
  const reported = work.locator('li').filter({ hasText: 'Harbor worker' }).first()
  await expect(reported).toContainText('Codex · 2 models · high · 2m 30s')
  await expect(reported).toContainText('1,210 in · 82 out · 15 cached')
  await expect(reported).toContainText('$1.75 provisional')
  await expect(reported).toContainText('gpt-5.4')
  await expect(reported).toContainText('1,200 in · 80 out · 15 cached')
  await expect(reported).toContainText('$1.50 provisional')
  await expect(reported).toContainText('subscription, reported')
  await expect(reported).toContainText('gpt-5.4-mini')
  await expect(reported).toContainText('$0.25 estimated')
  await expect(reported).toContainText('api, reported')
  await expect(reported).toContainText('PHAROS-11')
  await expect(reported).not.toContainText('5m')
  const missing = work.locator('li').filter({ hasText: 'Cursor' })
  await expect(missing).toContainText('Model unknown')
  await expect(missing).toContainText('Effort unknown')
  await expect(missing).toContainText('45s so far')
  await expect(missing).toContainText('Tokens unknown')
  await expect(missing).toContainText('Cost unknown')
  await expect(missing).not.toContainText('$0')
  await expect(work.getByRole('link')).toHaveCount(2)
})

test('a ticket with no sessions says so without inventing a cost', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-11')
  const work = panel(page).getByRole('region', { name: 'Agent work' })
  await expect(work).toContainText('No agent sessions are recorded for this ticket.')
  await expect(work.locator('li')).toHaveCount(0)
  await expect(work).not.toContainText('$')
})

test('a 128-character usage model keeps its figures in the ticket panel', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const longModel = 'm'.repeat(128)
  const report = structuredClone(agentWork)
  report.sessions = [report.sessions[0]]
  report.sessions[0].models = [{ ...report.sessions[0].models[0], model: longModel }]
  await mockWork(page, fixtures())
  await page.route('**/api/nodes/n-epic/agent-work', route => route.fulfill({ json: report }))
  await page.goto('/p/PHAROS/PHAROS-10')
  const work = panel(page).getByRole('region', { name: 'Agent work' })
  await expect(work).toContainText(longModel)
  await expect(work).toContainText('1,200 in')
  await expect(work).toContainText('$1.50 provisional')
  expect(await work.evaluate(element => element.scrollWidth <= element.clientWidth)).toBe(true)
})

test('empty capped scope still warns that deeper sessions may exist', async ({ page }) => {
  const report = structuredClone(agentWork)
  report.sessions = []
  report.scope_truncated = true
  await mockWork(page, fixtures())
  await page.route('**/api/nodes/n-epic/agent-work', route => route.fulfill({ json: report }))
  await page.goto('/p/PHAROS/PHAROS-10')
  const work = panel(page).getByRole('region', { name: 'Agent work' })
  await expect(work).toContainText('No agent sessions are recorded in the included items.')
  await expect(work).toContainText('Some items under this epic were left out of the query.')
  await expect(work.locator('li')).toHaveCount(0)
})

for (const colorScheme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`agent work ${colorScheme} ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    const work = await openEpic(page)
    await work.scrollIntoViewIfNeeded()
    await expect(work.getByText('Harbor worker')).toBeVisible()
    await page.screenshot({ path: `../.agent-shots/ticket-agent-work-${colorScheme}-${width}.png`, fullPage: true })
  })
}
