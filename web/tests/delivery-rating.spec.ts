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

function rating(mine: { score: number | null; tags: string[]; comment: string } | null, votes = 0, average: string | null = null) {
  return {
    session_id: sessionId,
    ticket_node_id: 'n-1',
    harness: 'codex',
    model: 'gpt-5.4',
    account_label: null,
    mine: mine ? { ...mine, updated_at: '2026-09-29T10:00:00Z' } : null,
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
  const saved: { method: string; body?: unknown }[] = []
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  await mockWork(page, data)
  await page.route('**/api/nodes/n-epic/agent-work', route => route.fulfill({ json: agentWork }))
  await page.route('**/api/nodes/n-epic/delivery-ratings', route => route.fulfill({ json: { node_id: 'n-epic', sessions: [rating(null)] } }))
  await page.route('**/api/harness-sessions/*/delivery-rating', async route => {
    const method = route.request().method()
    if (method === 'DELETE') {
      saved.push({ method })
      await route.fulfill({ json: rating(null) })
      return
    }
    const body = route.request().postDataJSON()
    saved.push({ method, body })
    await route.fulfill({ json: rating({ score: body.score ?? null, tags: body.tags ?? [], comment: body.comment }, 1, body.score ? '4.00' : null) })
  })
  await page.goto('/p/PHAROS/PHAROS-10')
  const work = page.getByRole('region', { name: 'Agent work' })
  await expect(work).toBeVisible()
  return { work, saved }
}

test('a working session on the ticket shows no mark and a stopped one does', async ({ page }) => {
  const workingId = '5e000000-0000-4000-8000-000000000002'
  const report = structuredClone(agentWork)
  report.sessions.push({
    ...agentWork.sessions[0],
    id: workingId,
    label: 'Still working',
    phase: 'working',
    ended_at: null,
    duration_state: 'ongoing',
  })
  report.totals.session_count = 2
  await mockWork(page, fixtures())
  await page.route('**/api/nodes/n-epic/agent-work', route => route.fulfill({ json: report }))
  await page.route('**/api/nodes/n-epic/delivery-ratings', route => route.fulfill({
    json: { node_id: 'n-epic', sessions: [rating(null), { ...rating(null), session_id: workingId }] },
  }))
  await page.goto('/p/PHAROS/PHAROS-10')
  const work = page.getByRole('region', { name: 'Agent work' })
  await expect(work).toBeVisible()
  const stopped = work.locator('li').filter({ hasText: 'Harbor worker' }).first()
  const working = work.locator('li').filter({ hasText: 'Still working' }).first()
  await expect(stopped.getByRole('button', { name: 'Needs rework' })).toBeVisible()
  await expect(working.getByRole('button', { name: 'Needs rework' })).toHaveCount(0)
  await expect(working.locator('[data-delivery-rating]')).toHaveCount(0)
})

test('a person marks rework with a reason and can undo it', async ({ page }) => {
  const errors = watchErrors(page)
  const { work, saved } = await openTicket(page)
  const box = work.locator('[data-delivery-rating]')
  await expect(box.getByRole('button', { name: 'Needs rework' })).toBeVisible()
  await expect(box.getByRole('radio')).toHaveCount(0)
  await expect(box).toContainText('1 review round · 2 reverts')
  await expect(box).not.toContainText('CI')
  await box.getByRole('button', { name: 'Needs rework' }).click()
  await expect(box.getByRole('textbox', { name: 'Reason' })).toBeFocused()
  await expect(box.getByText('optional', { exact: true })).toBeVisible()
  await expect(box.getByRole('radio', { name: '4 out of 5' })).toHaveCount(0)
  await box.getByRole('button', { name: 'Cancel' }).click()
  await expect(box.getByRole('button', { name: 'Needs rework' })).toBeFocused()
  await box.getByRole('button', { name: 'Needs rework' }).click()
  await expect(box.getByRole('textbox', { name: 'Reason' })).toBeFocused()
  await box.getByRole('button', { name: 'Rework', exact: true }).click()
  await box.getByRole('textbox', { name: 'Reason' }).fill('Needs another pass')
  await box.locator('summary').click()
  await box.getByRole('radio', { name: '4 out of 5' }).click()
  await box.getByRole('button', { name: 'Mark for rework' }).click()
  await expect.poll(() => saved.length).toBe(1)
  expect(saved[0]).toEqual({ method: 'PUT', body: { score: 4, tags: ['rework'], comment: 'Needs another pass' } })
  await expect(box).toContainText('Marked for rework')
  await expect(box).toContainText('Needs another pass')
  await expect(box.getByRole('radio')).toHaveCount(0)
  await expect(box.getByRole('button', { name: 'Edit' })).toBeFocused()
  await box.getByRole('button', { name: 'Edit' }).click()
  await expect(box.getByRole('textbox', { name: 'Reason' })).toBeFocused()
  await box.getByRole('button', { name: 'Cancel' }).click()
  await expect(box.getByRole('button', { name: 'Edit' })).toBeFocused()
  await box.getByRole('button', { name: 'Edit' }).click()
  await box.getByRole('textbox', { name: 'Reason' }).fill('Still needs another pass')
  await box.getByRole('button', { name: 'Update' }).click()
  await expect.poll(() => saved.length).toBe(2)
  expect(saved[1]?.body).toMatchObject({ comment: 'Still needs another pass', score: 4, tags: ['rework'] })
  await expect(box.getByRole('button', { name: 'Edit' })).toBeFocused()
  await box.getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => saved.some(item => item.method === 'DELETE')).toBe(true)
  await expect(box.getByRole('button', { name: 'Needs rework' })).toBeFocused()
  await expect(box).not.toContainText('Marked for rework')
  expect(errors).toEqual([])
})

test('ticket rework fits 390 and 1600 in light and dark', async ({ browser }) => {
  mkdirSync(shots, { recursive: true })
  for (const width of [1600, 390]) {
    for (const theme of ['light', 'dark'] as const) {
      await shot(browser, width, theme, async page => {
        const { work } = await openTicket(page, theme)
        const box = work.locator('[data-delivery-rating]')
        await expect(box.getByRole('button', { name: 'Needs rework' })).toBeVisible()
        await box.screenshot({ path: `${shots}/ticket-idle-${theme}-${width}.png` })
        await box.getByRole('button', { name: 'Needs rework' }).click()
        await box.getByRole('textbox', { name: 'Reason' }).fill('Needs another pass')
        await box.locator('summary').click()
        await expect(box.evaluate(el => el.scrollWidth <= el.clientWidth + 1)).resolves.toBe(true)
        await expect(page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)).resolves.toBe(true)
        await box.screenshot({ path: `${shots}/ticket-form-${theme}-${width}.png` })
        await box.getByRole('button', { name: 'Mark for rework' }).click()
        await expect(box).toContainText('Marked for rework')
        await expect(box.evaluate(el => el.scrollWidth <= el.clientWidth + 1)).resolves.toBe(true)
        await box.screenshot({ path: `${shots}/ticket-marked-${theme}-${width}.png` })
      })
    }
  }
})

test('the session overview shows the same rework mark', async ({ browser }) => {
  mkdirSync(shots, { recursive: true })
  for (const width of [1600, 390]) {
    for (const theme of ['light', 'dark'] as const) {
      await shot(browser, width, theme, async page => {
        const data = fixtures()
        data.preferences.theme = { choice: theme }
        await mockWork(page, data)
        await mockAgents(page, agentData(world))
        await page.route('**/api/harness-sessions/*/delivery-rating', route => route.fulfill({
          json: rating({ score: null, tags: ['rework'], comment: 'Needs another pass' }, 1, null),
        }))
        const stoppedId = '5e000000-0000-4000-8000-000000000007'
        await page.goto(`/agents/${sessionId}`)
        const details = page.getByRole('complementary', { name: 'Session details' })
        await expect(details.getByRole('heading', { name: 'Details' })).toBeVisible()
        await expect(details.locator('[data-delivery-rating]')).toHaveCount(0)
        await page.goto(`/agents/${stoppedId}`)
        const box = details.locator('[data-delivery-rating]')
        await expect(box).toContainText('Marked for rework')
        await expect(box).toContainText('Needs another pass')
        await expect(box.getByRole('button', { name: 'Undo' })).toBeVisible()
        await expect(box.getByRole('radio')).toHaveCount(0)
        await expect(box).toContainText('1 review round · 2 reverts')
        await expect(details.getByRole('heading', { name: 'Details' })).toBeVisible()
        await expect(page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)).resolves.toBe(true)
        await box.screenshot({ path: `${shots}/session-${theme}-${width}.png` })
      })
    }
  }
})

test('usage shows the rework rate by harness', async ({ browser }) => {
  mkdirSync(shots, { recursive: true })
  const group = (label: string, exceptions: number, deliveries: number) => ({
    label, votes: exceptions, average: null, exceptions, deliveries, rework_rate: `${exceptions}/${deliveries}`,
  })
  const dashboard = {
    from: '2026-08-29T00:00:00Z', to: '2026-09-28T00:00:00Z', generated_at: '2026-09-27T10:00:00Z',
    attribution: 'lifetime_for_sessions_started_in_range', trend_basis: 'session_started_utc_day', list_price_currency: 'USD',
    truncated: false,
    totals: blank('All visible sessions', 3, '1.250000000000'),
    by_project: [blank('Pharos', 3, '1.250000000000')],
    by_model: [blank('gpt-4.1', 2, '1.250000000000')],
    by_harness: [blank('codex', 2, '1.250000000000'), blank('claude', 1, '0.250000000000')],
    by_subscription: [blank('Codex Pro', 2, '1.250000000000', { billing_mode: 'subscription' })],
    trend: [],
    tickets: [],
    tickets_cost_unknown: 0,
    allowance: { state: 'none', windows: [] },
    ratings: {
      votes: 2, average: null, exceptions: 1, deliveries: 3, rework_rate: '1/3',
      by_model: [group('gpt-4.1', 1, 2)],
      by_harness: [group('codex', 1, 2)],
    },
  }
  for (const width of [1600, 390]) {
    for (const theme of ['light', 'dark'] as const) {
      await shot(browser, width, theme, async page => {
        const data = fixtures()
        data.preferences.theme = { choice: theme }
        await mockWork(page, data)
        await page.route('**/api/usage/dashboard**', route => route.fulfill({ json: dashboard }))
        await page.goto('/agents/usage')
        await expect(page.getByRole('heading', { name: 'Usage', level: 1 })).toBeVisible()
        const summary = page.locator('.summary-card')
        await expect(summary.getByText('Rework', { exact: true })).toBeVisible()
        await expect(summary).toContainText('33%')
        await expect(summary).toContainText('1 of 3')
        await expect(summary).not.toContainText('4.5')
        await page.getByRole('button', { name: 'Harness' }).click()
        const table = page.getByRole('table', { name: 'By harness' })
        await expect(table.getByRole('columnheader', { name: 'Rework' })).toBeVisible()
        await expect(table.getByRole('row', { name: /Codex/ })).toContainText('50%')
        await expect(table.getByRole('row', { name: /Claude/ }).locator('td').last()).toHaveText('')
        await expect(page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)).resolves.toBe(true)
        await page.locator('.breakdown').screenshot({ path: `${shots}/usage-${theme}-${width}.png` })
      })
    }
  }
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
