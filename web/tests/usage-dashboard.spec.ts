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
