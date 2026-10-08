// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { boardPerson } from './models-board-fixtures'
import { mockModelsSettings, openModelsSettings } from './models-settings-page'
import { controlStability } from './control-stability'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { ACCOUNTS, NOW, capacityWorld } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import type { AccountUsagePolicy } from '../src/lib/accountUsage'

const controls = (page: import('@playwright/test').Page) => ({ usage: page.locator('[data-usage-group]'), careful: page.locator('[data-usage="careful"]'), balanced: page.locator('[data-usage="balanced"]'), maxout: page.locator('[data-usage="maxout"]'), thinking: page.locator('[data-thinking-group]'), kind: page.locator('[data-next-kind]'), project: page.locator('[data-next-project]'), why: page.locator('[data-next-why]') })

test('Usage saves for the captured person, Undo restores inheritance, and a failed save keeps the current value', async ({ page }) => {
  const state = await mockModelsSettings(page); await openModelsSettings(page)
  const guard = await controlStability(page, controls(page))
  for (const posture of ['careful', 'maxout', 'balanced']) await guard.check(async () => { await page.locator(`[data-usage="${posture}"]`).click(); await expect(page.locator(`[data-usage="${posture}"]`)).toHaveAttribute('aria-pressed', 'true') })
  expect(state.writes.map(write => write.body.usage)).toEqual(['careful', 'maxout', 'balanced'])
  expect(state.writes.every(write => write.person === boardPerson)).toBe(true)
  await guard.check(async () => { await page.getByRole('button', { name: 'Undo', exact: true }).last().click(); await expect(page.locator('[data-usage="maxout"]')).toHaveAttribute('aria-pressed', 'true') })
  state.setFail(403)
  await guard.check(async () => { await page.locator('[data-usage="careful"]').click(); await expect(page.locator('.feedback [role="alert"]')).toBeVisible(); await expect(page.locator('[data-usage="maxout"]')).toHaveAttribute('aria-pressed', 'true') }); guard.done()
  await expect(page.getByRole('link', { name: 'Accounts and computers', exact: true })).toHaveAttribute('href', '/settings/accounts')
  await expect(page.getByText('Arrives with AEON-864', { exact: false })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Boost today/ })).toHaveCount(0)
})

for (const width of [390, 1024, 1280, 1440]) for (const theme of ['light', 'dark']) test(`Usage controls stay still at ${width} ${theme} with German copy`, async ({ page }, testInfo) => {
  await page.setViewportSize({ width, height: 1050 })
  await mockModelsSettings(page, { german: true }); await openModelsSettings(page, '?lang=de')
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
  const guard = await controlStability(page, controls(page))
  for (const posture of ['careful', 'maxout', 'balanced']) await guard.check(async () => { await page.locator(`[data-usage="${posture}"]`).click(); await expect(page.locator(`[data-usage="${posture}"]`)).toHaveAttribute('aria-pressed', 'true') }); guard.done()
  await page.screenshot({ path: testInfo.outputPath(`models-${width}-${theme}-de.png`), fullPage: true })
})

test('An agent sees the Usage values without person controls', async ({ page }) => {
  await mockModelsSettings(page); await openModelsSettings(page, '?agent=1')
  await expect(page.locator('[data-usage-group] button')).toHaveCount(0)
  await expect(page.locator('[data-usage-group]')).toContainText('Balanced')
})

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
  const writes: Record<string, unknown>[] = []
  await page.route('**/api/agent-accounts/overview*', route => route.fulfill({ json: { accounts: [{ account_id: policy.account_id, usage_policy: policy }], has_more: false } }))
  await page.route(/\/api\/agent-accounts\/[^/]+\/(posture|floor)$/, route => {
    const body = route.request().postDataJSON(); writes.push(body)
    if (body.floor_percent !== undefined) policy = { ...policy, floor_percent: body.floor_percent, own_floor_percent: body.floor_percent, revision: policy.revision + 1 }
    else policy = { ...policy, posture: body.posture || 'balanced', source: body.posture ? 'account' : 'person', revision: policy.revision + 1 }
    return route.fulfill({ json: policy })
  })
  await page.goto('/settings/accounts?lang=de')
  await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
  await page.locator(`.list-row[data-accounts~="${ACCOUNTS.main}"]`).click()
  const usage = page.locator('[data-account-usage]').first()
  await expect(usage.locator('[data-account-posture="balanced"]')).toHaveAttribute('aria-pressed', 'true')
  const guard = await controlStability(page, { options: usage.locator('.usage-options'), careful: usage.locator('[data-account-posture="careful"]'), maxout: usage.locator('[data-account-posture="maxout"]'), inherit: usage.getByRole('button', { name: 'Meiner Modelleinstellung folgen' }), floor: usage.getByRole('spinbutton'), save: usage.getByRole('button', { name: 'Untergrenze speichern' }) })
  for (const posture of ['careful', 'maxout']) await guard.check(async () => { await usage.locator(`[data-account-posture="${posture}"]`).click(); await expect(usage.locator(`[data-account-posture="${posture}"]`)).toHaveAttribute('aria-pressed', 'true') })
  await guard.check(async () => { await usage.getByRole('spinbutton').fill('25'); await usage.getByRole('button', { name: 'Untergrenze speichern' }).click(); await expect(usage.locator('.policy-note')).toContainText('25 %') }); guard.done()
  expect(writes).toEqual([{ posture: 'careful', revision: 1, binding_revision: 2 }, { posture: 'maxout', revision: 2, binding_revision: 2 }, { floor_percent: 25, revision: 3, binding_revision: 2 }])
  await page.screenshot({ path: testInfo.outputPath(`accounts-${width}-${theme}-de.png`), fullPage: true })
})
