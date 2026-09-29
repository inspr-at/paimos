// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Browser, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents, type AgentWorld } from './agents-fixtures'

const shots = '/private/tmp/claude-501/-Users-markus-Code-aeon/a4527da9-f872-45f5-a2f2-48dde0ce2ce5/scratchpad/shots/aeon-218'
const sessionId = '5e000000-0000-4000-8000-000000000001'

const world: AgentWorld = {
  me: me.id,
  projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}

function rating(mine: { score: number; tags: string[]; comment: string } | null, votes = 2, average: string | null = '4.50') {
  return {
    session_id: sessionId,
    ticket_node_id: 'n-1',
    harness: 'codex',
    model: 'gpt-5.4',
    account_label: null,
    mine: mine ? { ...mine, updated_at: '2026-09-27T10:00:00Z' } : null,
    votes,
    average,
    signals: { review_rounds: 1, ci_failures: 0, reverts: 2 },
  }
}

const agentWork = {
  node_id: 'n-epic', kind: 'epic', currency: 'USD', usage_available: true, includes_descendants: true,
  scope_truncated: false, list_truncated: false,
  sessions: [{
    id: sessionId, ticket_node_id: 'n-1', ticket_key: 'PHAROS-11', ticket_title: 'Connect Hetzner Cloud',
    harness: 'codex', label: 'Harbor worker', model: 'gpt-5.4', model_state: 'known', effort: 'high', effort_state: 'known',
    phase: 'stopped', started_at: '2026-09-27T10:00:00Z', ended_at: '2026-09-27T10:02:30Z', duration_seconds: 150, duration_state: 'known',
    usage_reported: true,
    models: [{
      model: 'gpt-5.4', input_tokens: '1200', output_tokens: '80', cached_input_tokens: '15',
      tokens_state: 'known', cached_state: 'known', estimated_cost_usd: '1.250000000000', cost_state: 'estimated',
      provisional: false, price_version: '3', billing_mode: 'subscription', subscription_label: 'Codex Pro',
    }],
    models_truncated: false, input_tokens: '1200', output_tokens: '80', cached_input_tokens: '15',
    tokens_state: 'known', cached_state: 'known', estimated_cost_usd: '1.250000000000', cost_state: 'estimated',
    unknown_token_models: 0, unknown_cost_models: 0,
  }],
  totals: {
    session_count: 1, input_tokens: '1200', output_tokens: '80', cached_input_tokens: '15',
    tokens_state: 'known', cached_state: 'known', estimated_cost_usd: '1.250000000000', cost_state: 'estimated',
    currency: 'USD', duration_seconds: 150, duration_state: 'known',
    unknown_token_sessions: 0, unknown_cost_sessions: 0, unknown_token_models: 0, unknown_cost_models: 0,
  },
}

async function openTicket(page: Page, theme: 'light' | 'dark' = 'light') {
  const saved: unknown[] = []
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  await mockWork(page, data)
  await page.route('**/api/nodes/n-epic/agent-work', route => route.fulfill({ json: agentWork }))
  await page.route('**/api/nodes/n-epic/delivery-ratings', route => route.fulfill({ json: { node_id: 'n-epic', sessions: [rating(null)] } }))
  await page.route('**/api/harness-sessions/*/delivery-rating', async route => {
    const body = route.request().postDataJSON()
    saved.push(body)
    await route.fulfill({ json: rating({ score: body.score, tags: body.tags, comment: body.comment }, 3, '4.33') })
  })
  await page.goto('/p/PHAROS/PHAROS-10')
  const work = page.getByRole('region', { name: 'Agent work' })
  await expect(work).toBeVisible()
  return { work, saved }
}

test('a person rates the delivery on the ticket and the signals stay beside it', async ({ page }) => {
  const errors = watchErrors(page)
  const { work, saved } = await openTicket(page)
  const box = work.locator('[data-delivery-rating]')
  await expect(box).toContainText('4.5 from 2')
  await expect(box).toContainText('1 review round · 2 reverts')
  await expect(box).not.toContainText('CI')
  await box.getByRole('radio', { name: '4 out of 5' }).click()
  await box.getByRole('button', { name: 'Rework' }).click()
  await box.getByRole('textbox', { name: 'Comment' }).fill('Needs another pass')
  await box.getByRole('button', { name: 'Save' }).click()
  await expect.poll(() => saved.length).toBe(1)
  expect(saved[0]).toEqual({ score: 4, tags: ['rework'], comment: 'Needs another pass' })
  await expect(box.getByRole('radio', { name: '4 out of 5' })).toHaveAttribute('aria-checked', 'true')
  await expect(box.getByRole('button', { name: 'Rework' })).toHaveAttribute('aria-pressed', 'true')
  await expect(box.getByRole('button', { name: 'Save' })).toHaveCount(0)
  await expect(box).toContainText('4.33 from 3')
  expect(errors).toEqual([])
})

test('ticket rating fits light and dark', async ({ browser }) => {
  mkdirSync(shots, { recursive: true })
  await shot(browser, 1600, 'light', async page => {
    const { work } = await openTicket(page, 'light')
    const box = work.locator('[data-delivery-rating]')
    await box.getByRole('radio', { name: '4 out of 5' }).click()
    await box.getByRole('button', { name: 'Quality' }).click()
    await expect(box.getByRole('button', { name: 'Save' })).toBeVisible()
    await expect(box.evaluate(el => el.scrollWidth <= el.clientWidth + 1)).resolves.toBe(true)
    await box.screenshot({ path: `${shots}/ticket-light-1600.png` })
  })
  await shot(browser, 390, 'dark', async page => {
    const { work } = await openTicket(page, 'dark')
    const box = work.locator('[data-delivery-rating]')
    await box.getByRole('radio', { name: '4 out of 5' }).click()
    await box.getByRole('textbox', { name: 'Comment' }).fill('Needs another pass')
    await expect(box.evaluate(el => el.scrollWidth <= el.clientWidth + 1)).resolves.toBe(true)
    await expect(page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)).resolves.toBe(true)
    await box.screenshot({ path: `${shots}/ticket-dark-390.png` })
  })
})

test('the session overview shows the same rating', async ({ browser }) => {
  mkdirSync(shots, { recursive: true })
  await shot(browser, 1600, 'light', async page => {
    const data = fixtures()
    data.preferences.theme = { choice: 'light' }
    await mockWork(page, data)
    await mockAgents(page, agentData(world))
    await page.route('**/api/harness-sessions/*/delivery-rating', route => route.fulfill({
      json: rating({ score: 4, tags: ['rework'], comment: 'Needs another pass' }, 3, '4.33'),
    }))
    await page.goto(`/agents/${sessionId}`)
    const details = page.getByRole('complementary', { name: 'Session details' })
    const box = details.locator('[data-delivery-rating]')
    await expect(box.getByRole('radio', { name: '4 out of 5' })).toHaveAttribute('aria-checked', 'true')
    await expect(box).toContainText('1 review round · 2 reverts')
    await expect(details.getByRole('heading', { name: 'Details' })).toBeVisible()
    await box.screenshot({ path: `${shots}/session-light-1600.png` })
  })
})

test('usage shows the rating by harness', async ({ browser }) => {
  mkdirSync(shots, { recursive: true })
  const dashboard = {
    from: '2026-08-29T00:00:00Z', to: '2026-09-28T00:00:00Z', generated_at: '2026-09-27T10:00:00Z',
    attribution: 'lifetime_for_sessions_started_in_range', trend_basis: 'session_started_utc_day', list_price_currency: 'USD',
    truncated: false,
    totals: blank('All visible sessions', 2, '1.250000000000'),
    by_project: [blank('Pharos', 2, '1.250000000000')],
    by_model: [blank('gpt-4.1', 2, '1.250000000000')],
    by_harness: [blank('codex', 2, '1.250000000000'), blank('claude', 1, '0.250000000000')],
    by_subscription: [blank('Codex Pro', 2, '1.250000000000', { billing_mode: 'subscription' })],
    trend: [],
    tickets: [],
    tickets_cost_unknown: 0,
    allowance: { state: 'none', windows: [] },
    ratings: {
      votes: 2, average: '4.50',
      by_model: [{ label: 'gpt-4.1', votes: 2, average: '4.50' }],
      by_harness: [{ label: 'codex', votes: 2, average: '4.50' }],
    },
  }
  await shot(browser, 1600, 'light', async page => {
    const data = fixtures()
    data.preferences.theme = { choice: 'light' }
    await mockWork(page, data)
    await page.route('**/api/usage/dashboard**', route => route.fulfill({ json: dashboard }))
    await page.goto('/agents/usage')
    await expect(page.getByRole('heading', { name: 'Usage', level: 1 })).toBeVisible()
    const summary = page.locator('.summary-card')
    await expect(summary.getByText('Rating', { exact: true })).toBeVisible()
    await expect(summary).toContainText('4.5')
    await expect(summary).toContainText('2 votes')
    await page.getByRole('button', { name: 'Harness' }).click()
    const table = page.getByRole('table', { name: 'By harness' })
    await expect(table.getByRole('columnheader', { name: 'Rating' })).toBeVisible()
    await expect(table.getByRole('row', { name: /Codex/ })).toContainText('4.5')
    await expect(table.getByRole('row', { name: /Claude/ }).locator('td').last()).toHaveText('')
    await page.locator('.breakdown').screenshot({ path: `${shots}/usage-light-1600.png` })
  })
  await shot(browser, 390, 'dark', async page => {
    const data = fixtures()
    data.preferences.theme = { choice: 'dark' }
    await mockWork(page, data)
    await page.route('**/api/usage/dashboard**', route => route.fulfill({ json: dashboard }))
    await page.goto('/agents/usage')
    await page.getByRole('button', { name: 'Harness' }).click()
    await expect(page.getByRole('table', { name: 'By harness' })).toBeVisible()
    await expect(page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)).resolves.toBe(true)
    await page.locator('.breakdown').screenshot({ path: `${shots}/usage-dark-390.png` })
  })
})

function blank(label: string, sessions: number, cost: string, extra: Record<string, unknown> = {}) {
  return {
    label, key: label, sessions, usage_rows: sessions, unreported_sessions: 0,
    input_tokens: '100', input_known_rows: 1, input_unknown_rows: 0,
    output_tokens: '20', output_known_rows: 1, output_unknown_rows: 0,
    cached_input_tokens: null, cached_input_known_rows: 0, cached_input_unknown_rows: 1,
    tokens_state: 'partial', estimated_cost_usd: cost, cost_known_rows: 1, cost_unknown_rows: 0,
    cost_state: 'partial', provisional_rows: 0, provisional_sessions: 0, ...extra,
  }
}

async function shot(browser: Browser, width: number, theme: 'light' | 'dark', run: (page: Page) => Promise<void>) {
  const context = await browser.newContext({
    viewport: { width, height: width === 390 ? 844 : 1000 },
    colorScheme: theme,
    reducedMotion: 'reduce',
  })
  const page = await context.newPage()
  try {
    await run(page)
  } finally {
    await context.close()
  }
}
