// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { controlStability } from './control-stability'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, NOW, capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import type { AccountUsagePolicy } from '../src/lib/accountUsage'

// The Models page no longer carries a Usage switch (AEON-1011, parked for Accounts and computers); the account's own posture and floor stay here.
for (const width of [390, 1024, 1280, 1440]) for (const theme of ['light', 'dark']) test(`Account owner posture and admin floor stay still at ${width} ${theme}`, async ({ page }, testInfo) => {
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
  let fail = false
  const writes: Record<string, unknown>[] = []
  await page.route('**/api/agent-accounts/overview*', route => route.fulfill({ json: { accounts: [{ account_id: policy.account_id, usage_policy: policy }], has_more: false } }))
  await page.route(/\/api\/agent-accounts\/[^/]+\/(posture|floor)$/, route => {
    const body = route.request().postDataJSON(); writes.push(body)
    if (fail) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
    if (body.floor_percent !== undefined) policy = { ...policy, floor_percent: body.floor_percent, own_floor_percent: body.floor_percent, revision: policy.revision + 1 }
    else policy = { ...policy, posture: body.posture || 'balanced', source: body.posture ? 'account' : 'person', revision: policy.revision + 1 }
    return route.fulfill({ json: policy })
  })
  await page.goto('/settings/accounts?lang=de')
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
  await page.locator(`.list-row[data-accounts~="${ACCOUNTS.main}"]`).click()
  const usage = page.locator('[data-account-usage]').first()
  await expect(usage.locator('[data-account-posture="balanced"]')).toHaveAttribute('aria-pressed', 'true')
  // ?lang=de does not translate the app (AEON-998).
  const guard = await controlStability(page, { options: usage.locator('.usage-options'), careful: usage.locator('[data-account-posture="careful"]'), maxout: usage.locator('[data-account-posture="maxout"]'), inherit: usage.getByRole('button', { name: 'Follow my Models setting' }), floor: usage.getByRole('spinbutton'), save: usage.getByRole('button', { name: 'Save floor' }), accountSwitch: page.locator('.use-sec [role="switch"]').first() })
  for (const posture of ['careful', 'maxout']) await guard.check(async () => { await usage.locator(`[data-account-posture="${posture}"]`).click(); await expect(usage.locator(`[data-account-posture="${posture}"]`)).toHaveAttribute('aria-pressed', 'true') })
  await guard.check(async () => {
    const input = usage.getByRole('spinbutton'), save = usage.getByRole('button', { name: /Save floor/ })
    await input.fill('25'); await input.press('Enter'); expect(writes).toHaveLength(2)
    await input.press('Escape'); await expect(save).toBeFocused()
    await input.focus(); await input.press(await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)) ? 'Meta+Enter' : 'Control+Enter')
    await expect(usage.locator('.policy-note')).toContainText('Floor: 25%.')
  })
  fail = true
  await guard.check(async () => { await usage.locator('[data-account-posture="careful"]').click(); await expect(usage.locator('[role="alert"]')).toBeVisible(); await expect(usage.locator('[data-account-posture="maxout"]')).toHaveAttribute('aria-pressed', 'true') })
  fail = false
  await guard.check(async () => { await usage.locator('[data-account-posture="maxout"]').click(); await expect(usage.locator('[role="alert"]')).toHaveCount(0) }); guard.done()
  expect(writes).toEqual([{ posture: 'careful', revision: 1, binding_revision: 2 }, { posture: 'maxout', revision: 2, binding_revision: 2 }, { floor_percent: 25, revision: 3, binding_revision: 2 }, { posture: 'careful', revision: 4, binding_revision: 2 }, { posture: 'maxout', revision: 4, binding_revision: 2 }])
  await page.screenshot({ path: testInfo.outputPath(`accounts-${width}-${theme}-de.png`), fullPage: true })
})
