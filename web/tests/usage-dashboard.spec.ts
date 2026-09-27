// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { fixtures, mockWork } from './work-fixtures'
import type { UsageDashboard, UsageGroup } from '../src/lib/usageFormat.ts'

const shots = resolve(process.cwd(), '../.agent-shots')

function group(label: string, sessions: number, cost: string | null, extra: Partial<UsageGroup> = {}): UsageGroup {
  const known = cost ? 1 : 0
  return {
    label, key: label, sessions, usage_rows: sessions, unreported_sessions: cost ? 0 : sessions,
    input_tokens: known ? '1000' : null, input_known_rows: known, input_unknown_rows: known ? 0 : 1,
    output_tokens: known ? '400' : null, output_known_rows: known, output_unknown_rows: known ? 0 : 1,
    cached_input_tokens: null, cached_input_known_rows: 0, cached_input_unknown_rows: Math.max(sessions, 1),
    tokens_state: known ? 'partial' : 'unknown', estimated_cost_usd: cost, cost_known_rows: known, cost_unknown_rows: known ? 0 : Math.max(sessions, 1),
    cost_state: known ? 'partial' : 'unknown', provisional_rows: 0, provisional_sessions: 0, ...extra,
  }
}

const dashboard: UsageDashboard = {
  from: '2026-08-29T00:00:00Z', to: '2026-09-28T00:00:00Z', generated_at: '2026-09-27T10:00:00Z',
  attribution: 'lifetime_for_sessions_started_in_range', trend_basis: 'session_started_utc_day', list_price_currency: 'USD',
  truncated: false,
  totals: {
    label: 'All visible sessions', sessions: 6, usage_rows: 7, unreported_sessions: 1,
    input_tokens: '1013', input_known_rows: 4, input_unknown_rows: 2,
    output_tokens: '408', output_known_rows: 4, output_unknown_rows: 2,
    cached_input_tokens: '53', cached_input_known_rows: 3, cached_input_unknown_rows: 3,
    tokens_state: 'partial', estimated_cost_usd: '528.000000000000', cost_known_rows: 5, cost_unknown_rows: 2,
    cost_state: 'partial', provisional_rows: 1, provisional_sessions: 1,
  },
  by_project: [group('Pharos', 4, '521.000000000000', { key: 'PHAROS', id: 'p-pharos' }), group('Unreported', 1, null, { key: '' })],
  by_model: [group('gpt-4.1', 4, '521.000000000000'), group('Unreported', 1, null)],
  by_subscription: [group('Codex Pro', 3, '521.000000000000', { billing_mode: 'subscription' }), group('Unreported', 1, null, { billing_mode: 'unreported' })],
  trend: [
    { day: '2026-09-10', group: group('', 4, '521.000000000000') },
    { day: '2026-09-11', group: group('', 1, null) },
  ],
  tickets: [{
    ...group('Known ticket', 2, '520.000000000000', { key: 'PHAROS-11', id: 'n-1', cost_state: 'partial' }),
    project_id: 'p-pharos', project_key: 'PHAROS',
  }],
  tickets_cost_unknown: 1,
  allowance: {
    state: 'visible',
    windows: [{
      account_id: 'a-1', label: 'Codex Pro', harness: 'codex', account_state: 'available', window_id: 'w-1', unit: 'tokens',
      allowance: 1000, used: 100, reserved: 50, pace_model: 'unrestricted', burst_ratio: '0.1000',
      starts_at: '2026-09-27T09:00:00Z', ends_at: '2026-09-27T11:00:00Z', provisional: true, pace_cap: 1000, headroom: 850, hard_remaining: 850,
    }],
  },
}

async function install(page: Page, theme: 'light' | 'dark') {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  await mockWork(page, data)
  const calls: string[] = []
  await page.route('**/api/usage/dashboard**', async route => {
    calls.push(route.request().url())
    await route.fulfill({ json: dashboard })
  })
  return calls
}

test('usage shows lifetime list estimate, unknown, and allowance without turning gaps into zero', async ({ page }) => {
  const calls = await install(page, 'light')
  await page.goto('/agents/usage')
  await expect(page.getByRole('heading', { name: 'Usage', level: 1 })).toBeVisible()
  await expect(page.locator('.summary')).toContainText('Lifetime usage for sessions that started')
  await expect(page.locator('.summary')).toContainText('not spend consumed')
  await expect(page.locator('.summary')).toContainText('not verified coverage')
  const summary = page.locator('.summary-card')
  await expect(summary).toContainText('6 sessions started')
  await expect(summary).toContainText('1,013 from 4 of 6')
  await expect(summary).toContainText('528.00 USD')
  await expect(summary).toContainText('1 provisional')
  await expect(summary).toContainText('2 unknown cost')
  await expect(summary).not.toContainText('0.00')
  await expect(page.getByRole('heading', { name: 'Sessions started', exact: true })).toBeVisible()
  await expect(page.getByText('Not spend during that day.')).toBeVisible()
  await expect(page.getByRole('row', { name: /11 Sep/ })).toContainText('Unknown')
  await expect(page.getByRole('region', { name: 'By subscription' })).toContainText('Reported subscription')
  await expect(page.getByRole('link', { name: /PHAROS-11/ })).toHaveAttribute('href', '/p/PHAROS/PHAROS-11')
  await expect(page.getByRole('region', { name: 'Allowance' })).toContainText('Codex Pro')
  await expect(page.getByRole('region', { name: 'Allowance' })).toContainText('Provisional')
  await page.getByRole('button', { name: '7 days' }).click()
  await expect.poll(() => calls.at(-1) ?? '').toContain('from=2026-')
  await expect(page).toHaveURL(/days=7/)
  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'Pharos' })
  await expect.poll(() => calls.at(-1) ?? '').toContain('project=p-pharos')
})

function isolated(cost: string, sessions: number, from: string, label: string): UsageDashboard {
  const row = group(label, sessions, cost)
  return {
    ...dashboard,
    from, to: '2026-09-28T00:00:00Z',
    totals: { ...row, label: 'All visible sessions' },
    by_project: [row],
    by_model: [group('gpt-4.1', sessions, cost)],
    by_subscription: [group('Codex Pro', sessions, cost, { billing_mode: 'subscription' })],
    trend: [{ day: from.slice(0, 10), group: group('', sessions, cost) }],
    tickets: [],
    tickets_cost_unknown: 0,
    allowance: { state: 'none', windows: [] },
  }
}

test('an older usage response cannot replace the selection that followed it', async ({ page }) => {
  const pageErrors: string[] = []
  page.on('pageerror', error => pageErrors.push(error.message))
  await page.addInitScript(() => {
    const original = window.fetch.bind(window)
    const held: { url: string; signal: AbortSignal | undefined; finish: (value: { status: number; body: unknown }) => void }[] = []
    ;(window as unknown as { __usageHeld: typeof held }).__usageHeld = held
    window.fetch = (input, init) => {
      const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
      if (!url.includes('/api/usage/dashboard')) return original(input, init)
      return new Promise(resolve => {
        held.push({
          url,
          signal: init?.signal ?? undefined,
          finish: value => resolve(new Response(JSON.stringify(value.body), { status: value.status, headers: { 'Content-Type': 'application/json' } })),
        })
      })
    }
  })
  const data = fixtures()
  data.preferences.theme = { choice: 'light' }
  await mockWork(page, data)
  const heldCount = () => page.evaluate(() => (window as unknown as { __usageHeld: unknown[] }).__usageHeld.length)
  const aborted = (index: number) => page.evaluate(index => (window as unknown as { __usageHeld: { signal?: AbortSignal }[] }).__usageHeld[index]?.signal?.aborted ?? false, index)
  const finish = (index: number, status: number, body: unknown) => page.evaluate(({ index, status, body }) => {
    ;(window as unknown as { __usageHeld: { finish: (value: { status: number; body: unknown }) => void }[] }).__usageHeld[index].finish({ status, body })
  }, { index, status, body })
  const kept = isolated('77.000000000000', 3, '2026-09-21T00:00:00Z', 'Kept range')
  const stale = isolated('999.000000000000', 9, '2026-08-29T00:00:00Z', 'Stale project')
  const status = page.locator('.state-line')
  const summary = page.locator('.summary-card')

  await page.goto('/agents/usage?days=7')
  await expect.poll(heldCount).toBe(1)
  await finish(0, 200, kept)
  await expect(summary).toContainText('3 sessions started')
  await expect(summary).toContainText('77.00 USD')
  await expect(page.locator('.range')).toContainText('21 Sept 2026')
  await expect(page.locator('.usage-page')).toHaveAttribute('aria-busy', 'false')

  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'Pharos' })
  await expect.poll(heldCount).toBe(2)
  await expect.poll(() => aborted(0)).toBe(true)
  await expect(summary).toHaveCount(0)
  await expect(page.getByText('77.00 USD')).toHaveCount(0)
  await expect(page.getByText('21 Sept 2026')).toHaveCount(0)
  await expect(status).toHaveText('Loading usage')
  await expect(page.locator('.usage-page')).toHaveAttribute('aria-busy', 'true')
  await expect(page.getByRole('button', { name: '7 days' })).toHaveAttribute('aria-pressed', 'true')

  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'All visible projects' })
  await expect.poll(heldCount).toBe(3)
  await expect.poll(() => aborted(1)).toBe(true)
  await expect(summary).toContainText('77.00 USD')
  await expect(summary).toContainText('3 sessions started')
  await expect(page.locator('.range')).toContainText('21 Sept 2026')
  await expect(status).toHaveText('Updating usage')

  await finish(1, 200, stale)
  await expect(page.getByText('999.00 USD')).toHaveCount(0)
  await expect(page.getByText('Stale project')).toHaveCount(0)
  await expect(page.getByText('9 sessions started')).toHaveCount(0)
  await expect(page.getByText(/29 Aug/)).toHaveCount(0)
  await expect(summary).toContainText('77.00 USD')
  await expect(status).toHaveText('Updating usage')
  await expect(page.locator('.usage-page')).toHaveAttribute('aria-busy', 'true')

  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'Pharos' })
  await expect.poll(heldCount).toBe(4)
  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'All visible projects' })
  await expect.poll(heldCount).toBe(5)
  await expect.poll(() => aborted(3)).toBe(true)
  await expect(summary).toContainText('77.00 USD')
  await expect(status).toHaveText('Updating usage')
  await finish(3, 503, { error: 'The old project range failed' })
  await expect(page.getByText('The old project range failed')).toHaveCount(0)
  await expect(summary).toContainText('77.00 USD')
  await expect(summary).toContainText('3 sessions started')
  await expect(status).toHaveText('Updating usage')
  await expect(page.getByRole('alert')).toHaveCount(0)

  await finish(4, 503, { error: 'The selected range could not be loaded' })
  await expect(page.getByRole('alert')).toContainText('The selected range could not be loaded')
  await expect(summary).toContainText('77.00 USD')
  await expect(summary).toContainText('3 sessions started')
  await expect(page.getByText('999.00 USD')).toHaveCount(0)
  await expect(page.getByText('The old project range failed')).toHaveCount(0)
  await expect(status).toHaveCount(0)
  await expect(page.locator('.usage-page')).toHaveAttribute('aria-busy', 'false')

  await page.getByRole('combobox', { name: 'Project', exact: true }).selectOption({ label: 'Pharos' })
  await expect.poll(heldCount).toBe(6)
  await page.getByRole('region', { name: 'Usage' }).getByRole('link', { name: 'Agents' }).click()
  await expect(page).toHaveURL(/\/agents$/)
  await expect.poll(() => aborted(5)).toBe(true)
  await finish(5, 200, stale)
  await expect(page.getByText('999.00 USD')).toHaveCount(0)
  await expect(page.getByText('Stale project')).toHaveCount(0)
  expect(pageErrors).toEqual([])
})

test('usage screenshots at 1600 and 390, light and dark', async ({ browser }) => {
  test.setTimeout(120_000)
  mkdirSync(shots, { recursive: true })
  for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
    const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, colorScheme: theme, reducedMotion: 'reduce', deviceScaleFactor: 1 })
    const page = await context.newPage()
    await install(page, theme)
    await page.goto('/agents/usage')
    await expect(page.getByRole('heading', { name: 'Usage', level: 1 })).toBeVisible()
    await expect(page.locator('.summary-card')).toContainText('528.00 USD')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await page.evaluate(() => {
      const shell = document.querySelector('.app-shell')
      const main = document.querySelector('main')
      for (const node of [document.documentElement, document.body, shell, main]) {
        if (!(node instanceof HTMLElement)) continue
        node.style.height = 'auto'
        node.style.overflow = 'visible'
      }
      if (shell instanceof HTMLElement) shell.style.display = 'block'
    })
    await page.screenshot({ path: resolve(shots, `usage-${width}-${theme}.png`), fullPage: true })
    await context.close()
  }
})
