// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockStartAgent } from './start-agent-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

async function setup(page: Page, theme: 'light' | 'dark' = 'light', manage = true) {
  const work = fixtures(); work.preferences.theme = { choice: theme }
  await mockWork(page, work, { admin: manage, readOnly: !manage })
  const data = agentData({ me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' } })
  const account = data.accounts.find(a => a.harness === 'pi')!
  Object.assign(account, { label: 'OpenRouter on this Mac', provider: 'openrouter', model: 'stealth/space-bunny-alpha', model_status: 'known', model_data_note: true, openrouter_credits: { observed_at: new Date().toISOString(), usage: 2.5, limit: 10, remaining: 7.5 } })
  await mockAgents(page, data)
  await page.route('**/api/me/permissions*', route => { const p = mockEffectivePermissions(manage ? 'admin' : 'viewer'); p.workspace.permissions.push('account.read'); if (manage) p.workspace.permissions.push('account.manage'); return route.fulfill({ json: p }) })
  const writes: unknown[] = []
  await page.route(`**/api/agent-accounts/${account.id}/model`, async route => {
    expect(route.request().method()).toBe('PUT')
    const body = route.request().postDataJSON(); writes.push(body)
    expect(Object.keys(body)).toEqual(['model'])
    Object.assign(account, { model: body.model, model_status: 'unknown', model_data_note: false })
    await route.fulfill({ json: account })
  })
  await page.goto('/settings/accounts')
  const row = page.locator('.account').filter({ hasText: 'OpenRouter on this Mac' })
  await row.getByRole('button', { name: 'Details for OpenRouter on this Mac' }).click()
  await expect(row.getByLabel('Model OpenRouter')).toBeVisible()
  return { row, writes }
}
for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`pi model settings ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const { row, writes } = await setup(page, theme)
    await expect(row).toContainText("Stealth and free models on OpenRouter may log prompts and outputs")
    await expect(row).toContainText('$2.50 used')
    await expect(page.locator('input[type="password"]')).toHaveCount(0)
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const input = row.getByLabel('Model OpenRouter')
    await input.fill('invalid slug')
    await expect(row.getByRole('button', { name: 'Save model' })).toBeDisabled()
    await input.fill('vendor/model:free')
    await expect(row.getByRole('button', { name: 'Save model' })).toBeEnabled()
    const dir = process.env.AEON374_SHOTS
    if (dir) { mkdirSync(dir, { recursive: true }); await row.scrollIntoViewIfNeeded(); await page.screenshot({ path: join(dir, `settings-${width}-${theme}.png`), fullPage: true }) }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await row.getByRole('button', { name: 'Save model' }).click()
    await expect(row.getByRole('status')).toContainText('Unknown slug')
    expect(writes).toEqual([{ model: 'vendor/model:free' }])
    await expect(row.getByRole('button', { name: 'Save model' })).toHaveCount(0)
    if (dir) await page.screenshot({ path: join(dir, `saved-${width}-${theme}.png`), fullPage: true })
  })
}
test('account reader sees pi model without editing', async ({ page }) => {
  const { row, writes } = await setup(page, 'light', false)
  await expect(row.getByLabel('Model OpenRouter')).toHaveAttribute('readonly', '')
  await expect(row.getByRole('button', { name: 'Save model' })).toHaveCount(0)
  expect(writes).toEqual([])
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) test(`pi Start pins the Settings model ${width} ${theme}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  const mock = await mockStartAgent(page, { catalog: 'pi-openrouter', theme })
  await page.goto('/agents')
  await expect(page.locator('.pi-model-caption')).toContainText('stealth/space-bunny-alpha')
  const dir = process.env.AEON374_SHOTS
  if (dir) await page.screenshot({ path: join(dir, `agents-${width}-${theme}.png`), fullPage: true })
  await page.locator('button.start-agent').click()
  const dialog = page.getByRole('dialog', { name: 'Start agent', exact: true })
  await dialog.getByRole('searchbox').fill('PHAROS-11')
  await dialog.getByRole('button', { name: /PHAROS-11 Connect Hetzner/ }).click()
  await expect(dialog.getByLabel('Model', { exact: true })).toHaveAttribute('readonly', '')
  await expect(dialog.getByLabel('Model', { exact: true })).toHaveValue('openrouter/stealth/space-bunny-alpha')
  await expect(dialog.getByLabel('Thinking', { exact: true }).locator('option:checked')).toHaveText('Off')
  await expect(dialog).toContainText('Model is set in Settings, under Accounts.')
  if (dir) await page.screenshot({ path: join(dir, `start-${width}-${theme}.png`), fullPage: true })
  await dialog.getByRole('button', { name: 'Queue run', exact: true }).click()
  await expect(dialog.getByRole('heading', { name: 'Queued', exact: true })).toBeVisible()
  expect(mock.calls.find(c => c.path.endsWith('/runs') && c.method === 'POST')?.body?.model_profile_id).toBe(mock.profile.id)
})
