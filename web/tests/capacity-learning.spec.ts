// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, NOW, TZ, capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { usageDashboard } from './usage-data'
import type { AccountCapacity } from '../src/lib/capacity'

test.use({ locale: 'en-GB', timezoneId: TZ })
async function setup(page: Page, theme: 'light' | 'dark', manage = true) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now: NOW, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: { 'p-aeon': { key: 'AEON', title: 'Aeon' } } })
  data.approvals = []; data.messages = []
  const capacity = capacityWorld()
  data.accounts = capacity.accounts as unknown as typeof data.accounts
  const base = capacity.handle
  capacity.handle = (path, method, body) => {
    const out = base(path, method, body)
    if (path === '/api/agent-accounts/capacity' && out?.json) for (const c of out.json as AccountCapacity[]) {
      const learning = { windows: [], tokens: 24000, cost_micros: 0, runs: 3, limit_hits: 0 }
      Object.assign(c, { learning })
      if (c.account_id === ACCOUNTS.main) Object.assign(learning, {
        windows: [{ window_kind: 'weekly', bucket: '', run_count: 12, run_percent: 8, hold_percent: 9, plus_minus: 3, auto_reserve_percent: 24, work_days: 5 }],
        presence_until: new Date(NOW + 20 * 60_000).toISOString(), suggested_hours: { start: 9, end: 19, days: 5 },
        correction: { points: 18, at: new Date(NOW - 2 * 60_000).toISOString() },
      })
      if (c.account_id === ACCOUNTS.studio) Object.assign(learning, { sleeps_at_night: true, away_suggested: true })
      if (c.account_id === ACCOUNTS.grok) {
        Object.assign(learning, { limit_hits: 2 })
        Object.assign(c.windows[0].reading, { source: 'estimate', used_percent: 50, plus_minus: 20, evidence: { kind: 'limit_hits', samples: 2 } })
        c.windows[0].remaining_percent = 50
      }
      if (c.account_id === ACCOUNTS.cursor) c.windows = []
    }
    return out
  }
  await mockAgents(page, data, { capacity })
  await page.route('**/api/me/permissions*', route => {
    const permissions = mockEffectivePermissions('admin')
    permissions.workspace.permissions = [...permissions.workspace.permissions.filter(p => p !== 'account.manage'), 'account.read', ...(manage ? ['account.manage'] : [])]
    return route.fulfill({ json: permissions })
  })
  await page.route('**/api/usage/dashboard**', route => route.fulfill({ json: usageDashboard('unreported', 30) }))
  return capacity
}
for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`learned evidence ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const capacity = await setup(page, theme)
    await page.goto('/agents')
    const card = page.getByRole('region', { name: 'Accounts', exact: true })
    await expect(card).toBeVisible()
    if (!process.env.LEARNED_BASELINE) {
      await expect(card.getByText('Estimated · ±20% · 2 limit hits')).toBeVisible()
      const cursor = page.locator(`[data-account="${ACCOUNTS.cursor}"]`)
      await expect(cursor.locator('[role="meter"]')).toHaveCount(0)
      await expect(cursor).toContainText('24k tokens')
      const main = page.locator(`[data-account="${ACCOUNTS.main}"]`)
      await main.getByText(/What .+ learned/, { exact: true }).click()
      await expect(main).toContainText('~8% a run')
      await expect(main).toContainText('12 runs')
      await expect(main).toContainText('Estimate was 18% too optimistic')
      expect(capacity.puts).toEqual([])
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
    if (process.env.LEARNED_SHOTS) {
      mkdirSync(process.env.LEARNED_SHOTS, { recursive: true })
      if (width === 390) await page.setViewportSize({ width, height: 3400 })
      await card.screenshot({ path: join(process.env.LEARNED_SHOTS, `agents-${width}-${theme}.png`) })
    }
    if (!process.env.LEARNED_BASELINE) {
      const main = page.locator(`[data-account="${ACCOUNTS.main}"]`)
      await main.getByRole('button', { name: 'Use these hours' }).click()
      await expect.poll(() => capacity.puts.length).toBe(1)
      expect(capacity.puts[0]).toMatchObject({ scope: 'account', account_id: ACCOUNTS.main, schedule: { week: [{ on: true, start: 9, end: 19 }, ...Array.from({ length: 4 }, () => ({ on: true, start: 9, end: 19 })), { on: false, start: 8, end: 22 }, { on: false, start: 8, end: 22 }] } })
      await page.setViewportSize({ width, height: width === 390 ? 1600 : 1000 })
      await page.goto('/settings/accounts')
      await expect(page.getByText(/What .+ learned/, { exact: true }).first()).toBeVisible()
      await page.getByText(/What .+ learned/, { exact: true }).first().click()
      if (process.env.LEARNED_SHOTS) await page.screenshot({ path: join(process.env.LEARNED_SHOTS, `settings-${width}-${theme}.png`), fullPage: true })
      await page.goto('/agents/usage')
      const band = page.getByRole('region', { name: 'Capacity', exact: true })
      await expect(band.getByText('Estimated · ±20% · 2 limit hits')).toBeVisible()
      await expect(band.locator('[data-pool="cursor"] .gauge')).toHaveCount(0)
      await expect(band.locator('[data-pool="cursor"]')).toContainText('24k tokens')
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
      if (process.env.LEARNED_SHOTS) await band.screenshot({ path: join(process.env.LEARNED_SHOTS, `usage-${width}-${theme}.png`) })
    }
  })
}

test('learning suggestions cannot write without account.manage', async ({ page }) => {
  test.skip(!!process.env.LEARNED_BASELINE)
  const capacity = await setup(page, 'light', false)
  await page.goto('/agents')
  await page.locator(`[data-account="${ACCOUNTS.main}"]`).getByText(/What .+ learned/, { exact: true }).click()
  await expect(page.getByRole('button', { name: 'Use these hours' })).toHaveCount(0)
  expect(capacity.puts).toEqual([])
})
