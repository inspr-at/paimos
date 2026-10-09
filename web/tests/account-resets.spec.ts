// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { controlStability } from './control-stability'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, NOW, capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import type { AccountUsagePolicy } from '../src/lib/accountUsage'
import type { AccountResetState, ResetPolicy } from '../src/lib/accountResets'

// AEON-1037 (AEON-1030 draft 6): the account posture is gone (Pace on the dial replaces it); the
// floor and the vendor-reported Resets card are the per-account overrides, and neither moves a control.
test.use({ timezoneId: 'Europe/Vienna' })
const credits = { count: 2, expires_at: ['2026-10-04T08:00:00Z', '2026-11-06T09:00:00Z'], source: 'vendor' as const }
const plan = { planned_at: '2026-10-03T16:00:00Z', raised_pace_points: 18, raised_pace_until: '2026-10-03T22:00:00Z' }
for (const width of [400, 1024, 1440]) for (const theme of ['light', 'dark']) test(`Account floor and resets switch stay still at ${width} ${theme}`, async ({ page }, testInfo) => {
  await page.setViewportSize({ width, height: 1050 }); await page.clock.setSystemTime(NOW)
  await mockWork(page, fixtures(), { admin: true })
  const world = capacityWorld(), data = agentData({ me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} })
  data.accounts = world.accounts as unknown as typeof data.accounts
  await mockAgents(page, data, { capacity: world })
  await page.route('**/api/me/permissions*', route => {
    const permissions = mockEffectivePermissions('admin')
    permissions.workspace.permissions.push('account.read', 'account.manage', 'model_prefs.manage')
    return route.fulfill({ json: permissions })
  })
  let policy: AccountUsagePolicy = { account_id: ACCOUNTS.main, posture: 'balanced', source: 'person', floor_percent: 10, own_floor_percent: 10, revision: 1, binding_revision: 2, can_set_posture: true, can_set_floor: true }
  let resetPolicy: ResetPolicy = 'suggest', fail = false
  const writes: Record<string, unknown>[] = []
  await page.route('**/api/agent-accounts/overview*', route => route.fulfill({ json: { accounts: [{ account_id: policy.account_id, harness: 'codex', usage_policy: policy, resets: credits, reset_policy: resetPolicy, reset_plan: resetPolicy === 'auto_before_expiry' ? plan : null, reset_revision: policy.revision, binding_revision: policy.binding_revision }], has_more: false } }))
  await page.route(/\/api\/agent-accounts\/[^/]+\/(floor|reset-policy|posture)$/, route => {
    const body = route.request().postDataJSON(); writes.push(body)
    if (fail || route.request().url().endsWith('/posture')) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
    policy = { ...policy, revision: policy.revision + 1 }
    if (body.floor_percent !== undefined) { policy = { ...policy, floor_percent: body.floor_percent, own_floor_percent: body.floor_percent }; return route.fulfill({ json: policy }) }
    resetPolicy = body.reset_policy
    const state: AccountResetState = { account_id: policy.account_id, reset_policy: resetPolicy, revision: policy.revision, binding_revision: policy.binding_revision, resets: credits, reset_plan: resetPolicy === 'auto_before_expiry' ? plan : null, undo_supported: false }
    return route.fulfill({ json: state })
  })
  await page.goto('/settings/accounts?lang=de')
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
  await page.locator(`.list-row[data-accounts~="${ACCOUNTS.main}"]`).click()
  const usage = page.locator('[data-account-usage]').first(), card = usage.locator('[data-account-resets]'), toggle = card.getByRole('switch', { name: 'Don’t let resets expire' })
  await expect(usage.locator('[data-account-posture]')).toHaveCount(0)
  await expect(usage.getByRole('button', { name: 'Follow my Models setting' })).toHaveCount(0)
  await expect(card.locator('[data-reset-summary]')).toHaveText('2 resets · 1 expires Sun, 1 on 6 Nov')
  await expect(toggle).not.toBeChecked(); await expect(card.locator('[data-reset-plan]')).toHaveText('Off: PAIMOS only suggests.')
  // ?lang=de does not translate the app (AEON-998).
  const guard = await controlStability(page, { floor: usage.getByRole('spinbutton'), save: usage.getByRole('button', { name: /Save floor/ }), resets: card.locator('.rs-sw'), summary: card.locator('[data-reset-summary]'), accountSwitch: page.locator('.use-sec [role="switch"]').first() })
  await guard.check(async () => { await toggle.click(); await expect(toggle).toBeChecked(); await expect(card.locator('[data-reset-plan]')).toHaveText('Planned: Sat ~18:00, then +18 percentage points a day until Sun.') })
  if (width === 1440) await page.screenshot({ path: testInfo.outputPath(`resets-on-${width}-${theme}.png`), fullPage: true })
  await guard.check(async () => { await toggle.click(); await expect(toggle).not.toBeChecked(); await expect(card.locator('[data-reset-plan]')).toHaveText('Off: PAIMOS only suggests.') })
  await guard.check(async () => {
    const input = usage.getByRole('spinbutton'), save = usage.getByRole('button', { name: /Save floor/ })
    await input.fill('25'); await input.press('Enter'); expect(writes).toHaveLength(2)
    await input.press('Escape'); await expect(save).toBeFocused()
    await input.focus(); await input.press(await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)) ? 'Meta+Enter' : 'Control+Enter')
    await expect(usage.locator('.policy-note')).toContainText('Floor: 25%.')
  })
  fail = true
  await guard.check(async () => { await toggle.click(); await expect(usage.locator('[role="alert"]')).toBeVisible(); await expect(toggle).not.toBeChecked() })
  fail = false
  await guard.check(async () => { await toggle.click(); await expect(toggle).toBeChecked(); await expect(usage.locator('[role="alert"]')).toHaveCount(0) }); guard.done()
  expect(writes).toEqual([{ reset_policy: 'auto_before_expiry', revision: 1, binding_revision: 2 }, { reset_policy: 'suggest', revision: 2, binding_revision: 2 }, { floor_percent: 25, revision: 3, binding_revision: 2 }, { reset_policy: 'auto_before_expiry', revision: 4, binding_revision: 2 }, { reset_policy: 'auto_before_expiry', revision: 4, binding_revision: 2 }])
  await page.screenshot({ path: testInfo.outputPath(`accounts-resets-${width}-${theme}.png`), fullPage: true })
})
