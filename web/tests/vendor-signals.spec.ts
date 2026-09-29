// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

test.use({ locale: 'en-GB', timezoneId: 'UTC' })

const now = Date.parse('2026-09-29T12:00:00Z')
async function setup(page: Page, theme: 'light' | 'dark', grants = { manage: true }) {
  await page.clock.install({ time: now })
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: true })
  const data = agentData({ now, me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: { 'p-aeon': { key: 'AEON', title: 'Aeon' } } })
  const first = data.sessions[0]!
  Object.assign(first, { display_label: 'Vendor signal fixture', phase: 'stopped', activity: 'throttled', stopped_at: new Date(now).toISOString(), stop_reason: 'vendor_limit', run_status: 'failed', has_problem: false, needs_attention: false, vendor_limited: true, limit_window: '5h', limit_resets_at: '2026-09-29T14:10:00Z' })
  await mockAgents(page, data)
  await page.route('**/api/agent-pairing/computers', route => route.fulfill({ json: { computers: [] } }))
  await page.route('**/api/me/permissions*', route => {
    const p = mockEffectivePermissions('admin', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    p.workspace.permissions = [...p.workspace.permissions.filter(permission => permission !== 'account.manage'), 'account.read']
    if (grants.manage) p.workspace.permissions.push('account.manage')
    return route.fulfill({ json: p })
  })
  const changes: boolean[] = []
  const account = data.accounts.find(a => a.harness === 'claude')!
  Object.assign(account, { statusline_opt_in: 'own' })
  await page.route('**/api/agent-accounts/*/statusline', async route => {
    expect(route.request().method()).toBe('PUT')
    const { enabled } = route.request().postDataJSON()
    changes.push(enabled)
    Object.assign(account, { statusline_enabled: enabled })
    await route.fulfill({ json: { enabled } })
  })
  return { first, changes, account }
}

test('a computer approver loses the statusline toggle when account.manage is revoked', async ({ page }) => {
  const grants = { manage: true }
  const { changes, account } = await setup(page, 'light', grants)
  await page.goto('/settings/accounts')
  const toggle = page.getByRole('switch', { name: /Show .* in your Claude status line/ })
  await expect(toggle).toBeVisible()
  expect(account.statusline_opt_in).toBe('own')
  grants.manage = false
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(toggle).toHaveCount(0)
  expect(changes).toEqual([])
  await page.reload()
  await expect(page.getByRole('heading', { name: 'Accounts', exact: true })).toBeVisible()
  await expect(toggle).toHaveCount(0)
})

for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`vendor throttling and explicit statusline consent ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 960 })
    const { first, changes } = await setup(page, theme)
    await page.goto('/agents')
    const row = page.locator(`[data-row="s:${first.id}"]`)
    await expect(row).toBeVisible()
    await expect(row).toContainText(/Throttled/)
    await expect(row).toContainText(/Throttled · 5-hour limit until 14:10/)
    await row.scrollIntoViewIfNeeded()
    const stateBox = await row.locator('.c-state').boundingBox()
    const textBox = await row.locator('.state-word').boundingBox()
    expect(textBox!.x + textBox!.width).toBeLessThanOrEqual(stateBox!.x + stateBox!.width + 1)
    await expect(row).not.toContainText('Problem')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    const dir = process.env.AEON354_SHOTS
    if (dir) { mkdirSync(dir, { recursive: true }); await page.screenshot({ path: join(dir, `throttled-${width}-${theme}.png`), fullPage: true }) }
    await page.goto('/settings/accounts')
    const toggle = page.getByRole('switch', { name: /Show .* in your Claude status line/ })
    await expect(toggle).toBeVisible()
    await expect(toggle).toHaveAttribute('aria-checked', 'false')
    expect(changes).toEqual([])
    await toggle.focus(); await page.keyboard.press('Space')
    await expect(toggle).toHaveAttribute('aria-checked', 'true')
    expect(changes).toEqual([true])
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    if (dir) await page.screenshot({ path: join(dir, `statusline-${width}-${theme}.png`), fullPage: true })
    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-checked', 'false')
    expect(changes).toEqual([true, false])
  })
}
