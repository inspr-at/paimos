// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { capacityWorld, NOW } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { controlStability } from './control-stability'

async function setup(page: Page, owns = true) {
  await page.clock.setSystemTime(NOW)
  const work = fixtures(); work.preferences['agents.working'] = { total: 5, limits: {} }
  work.preferences['ui.agents.sections'] = { dial: false }
  await mockWork(page, work, { admin: true })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  await mockAgents(page, data, { capacity: capacityWorld() })
  await page.route('**/api/me/permissions*', route => {
    const permissions = mockEffectivePermissions('admin'); permissions.workspace.permissions.push('account.read', 'account.manage')
    return route.fulfill({ json: permissions })
  })
  await page.route('**/api/agents/plan', route => route.fulfill({ json: { total: 5, limits: {}, principal_id: me.id, running: {}, running_total: 0, source: 'plan', updated_at: null } }))
  let policies = ['owned-a', 'owned-b'].map(account_id => ({ account_id, posture: 'balanced', source: 'person', floor_percent: 20, own_floor_percent: 20, revision: 3, binding_revision: 2, can_set_posture: owns, can_set_floor: false, boost_percent: 0, boost_until: null as string | null }))
  const writes: unknown[] = []
  await page.route('**/api/agent-accounts/overview*', route => route.fulfill({ json: { accounts: policies.map(usage_policy => ({ account_id: usage_policy.account_id, usage_policy })), has_more: false } }))
  await page.route('**/api/agent-accounts/boost', route => {
    const body = route.request().postDataJSON(); writes.push(body)
    policies = policies.map(p => ({ ...p, revision: p.revision + 1, boost_percent: body.boost_percent, boost_until: body.boost_percent ? new Date(NOW + 6 * 60 * 60_000).toISOString() : null }))
    return route.fulfill({ json: { accounts: policies } })
  })
  return { writes }
}

test('Boost today preserves the header controls through every option in both themes and languages', async ({ page }, info) => {
  // Two languages, four widths and both themes, each with a full header measurement, exceed the 30s default on CI.
  test.setTimeout(90_000)
  const errors = watchErrors(page), { writes } = await setup(page)
  await page.goto('/agents')
  const dial = page.getByRole('region', { name: 'Agents at once' }), boost = dial.locator('[data-boost-today]')
  await expect(boost).toBeVisible()
  for (const language of ['en', 'de']) {
    await page.goto(`/agents?lang=${language}`); await expect(boost).toBeVisible()
    for (const width of [1440, 1280, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
      await page.setViewportSize({ width, height: 1100 }); await page.emulateMedia({ colorScheme: theme })
      const buttons = Object.fromEntries([0, 10, 20, 30].map(percent => [`boost${percent}`, boost.locator(`[data-boost="${percent}"]`)]))
      const guard = await controlStability(page, { dial: dial.locator('.f-dial'), fold: dial.locator('.fs-tog'), plus: dial.getByRole('button', { name: 'One agent more at once' }), minus: dial.getByRole('button', { name: 'One agent fewer at once' }), selectors: boost.locator('.boost-options'), ...buttons })
      for (const percent of [10, 20, 30, 0]) await guard.check(async () => {
        await buttons[`boost${percent}`]!.click()
        await expect(buttons[`boost${percent}`]!).toHaveAttribute('aria-pressed', 'true')
        await expect(buttons[`boost${percent}`]!).toBeEnabled()
      })
      guard.done()
      for (const button of Object.values(buttons)) { const box = await button.boundingBox(); expect(box!.width).toBeGreaterThanOrEqual(44); expect(box!.height).toBeGreaterThanOrEqual(44) }
      await buttons.boost20!.click(); await expect(buttons.boost20!).toHaveAttribute('aria-pressed', 'true')
      await dial.screenshot({ path: info.outputPath(`${width}-${theme}-${language}.png`), animations: 'disabled' })
    }
  }
  expect(writes.length).toBeGreaterThan(0)
  expect(writes[0]).toMatchObject({ boost_percent: 10, accounts: [{ account_id: 'owned-a', revision: 3, binding_revision: 2 }, { account_id: 'owned-b', revision: 3, binding_revision: 2 }] })
  expect(errors).toEqual([])
})

test('a failed boost preserves the confirmed choice and non-owners have no buttons', async ({ page }) => {
  await setup(page)
  await page.route('**/api/agent-accounts/boost', route => route.fulfill({ status: 409, json: { error: 'binding changed' } }))
  await page.goto('/agents')
  const boost = page.locator('[data-boost-today]')
  await expect(boost).toBeVisible()
  const guard = await controlStability(page, { dial: page.locator('.f-dial'), selectors: boost.locator('.boost-options'), clicked: boost.locator('[data-boost="30"]') })
  await guard.check(async () => { await boost.locator('[data-boost="30"]').click(); await expect(boost).toContainText('Could not confirm') }); guard.done()
  await expect(boost.locator('[data-boost="0"]')).toHaveAttribute('aria-pressed', 'true')
  await expect(boost.getByRole('button', { name: 'Reload', exact: true })).toBeVisible()
  await setup(page, false); await page.goto('/agents'); await expect(page.locator('[data-boost]')).toHaveCount(0)
})
