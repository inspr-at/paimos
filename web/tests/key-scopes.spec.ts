// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { accessWorld, mockAccess } from './access-fixtures'

const agent = (page: Page) => page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'pharos-deployer' })
const sheet = (page: Page) => page.getByRole('dialog', { name: 'Edit scopes for pharos-deployer' })
async function open(page: Page, role: 'owner' | 'member' = 'owner') {
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, fixtures())
  const world = accessWorld({ role })
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  if (role === 'owner') await agent(page).getByRole('button', { name: /active key/ }).click()
  return world
}
async function edit(page: Page) {
  await agent(page).getByRole('button', { name: /^Edit scopes/ }).click()
  await expect(sheet(page).getByRole('checkbox', { name: /nodes\.read/ })).toBeChecked()
}

test('edits scopes on the same key and records the change', async ({ page }) => {
  const world = await open(page)
  const count = world.keys.length
  await edit(page)
  await sheet(page).getByRole('button', { name: 'Show unavailable scopes' }).click()
  await expect(sheet(page).getByRole('checkbox', { name: /nodes\.write/ })).toBeDisabled()
  await sheet(page).getByRole('button', { name: 'Hide unavailable scopes' }).click()
  await expect(sheet(page).getByRole('checkbox', { name: /keys\.manage/ })).toHaveCount(0)
  await expect(sheet(page).getByRole('button', { name: 'Save scopes' })).toBeDisabled()
  await sheet(page).getByRole('checkbox', { name: /knowledge\.read/ }).check()
  await sheet(page).getByRole('checkbox', { name: /nodes\.read/ }).uncheck()
  await expect(sheet(page)).toContainText('1 added · 1 removed')
  await sheet(page).getByRole('button', { name: 'Save scopes' }).click()
  await expect(sheet(page)).toHaveCount(0)
  expect(world.calls.filter(c => c.method === 'PATCH' && c.path.endsWith('/scopes')).map(c => c.body)).toEqual([{ add: ['knowledge.read'], remove: ['nodes.read'] }])
  expect(world.keys.find(k => k.id === 'k2')?.scopes).toEqual(['knowledge.read'])
  expect(world.keys).toHaveLength(count)
  expect(world.events.at(-1)?.type).toBe('agent_key.scopes_changed')
  expect(world.calls.filter(c => c.method === 'POST' && c.path === '/api/agent-keys')).toHaveLength(0)
  await expect(agent(page).getByText('knowledge.read', { exact: true })).toBeVisible()
})

test('cancel, empty scope set, and expired keys', async ({ page }) => {
  const world = await open(page)
  await edit(page)
  await sheet(page).getByRole('checkbox', { name: /nodes\.read/ }).uncheck()
  await expect(sheet(page)).toContainText('This key will have no access.')
  await sheet(page).getByRole('button', { name: 'Cancel', exact: true }).click()
  expect(world.calls.filter(c => c.method === 'PATCH')).toHaveLength(0)
  await edit(page)
  await sheet(page).getByRole('checkbox', { name: /nodes\.read/ }).uncheck()
  await sheet(page).getByRole('button', { name: 'Save scopes' }).click()
  await expect(sheet(page)).toHaveCount(0)
  expect(world.keys.find(k => k.id === 'k2')?.scopes).toEqual([])
  world.keys.find(k => k.id === 'k2')!.expires_at = '2020-01-01T00:00:00Z'
  await page.reload()
  await agent(page).getByRole('button', { name: /active key/ }).click()
  await expect(agent(page).getByRole('button', { name: /^Edit scopes/ })).toHaveCount(0)
})

test('server permission rejection keeps the draft and permits reloading', async ({ page }) => {
  await open(page)
  await edit(page)
  await sheet(page).getByRole('checkbox', { name: /knowledge\.read/ }).check()
  await page.route('**/api/agent-keys/k2/scopes', route => route.request().method() === 'PATCH' ? route.fulfill({ status: 403, json: { error: 'forbidden' } }) : route.fallback())
  await sheet(page).getByRole('button', { name: 'Save scopes' }).click()
  await expect(sheet(page).getByRole('alert')).toContainText('Access changed')
  await expect(sheet(page).getByRole('checkbox', { name: /knowledge\.read/ })).toBeChecked()
  await sheet(page).getByRole('button', { name: 'Reload scopes' }).click()
  await expect(sheet(page).getByRole('checkbox', { name: /knowledge\.read/ })).not.toBeChecked()
})

test('people without keys.manage have no scope edit action', async ({ page }) => {
  await open(page, 'member')
  await expect(page.getByRole('button', { name: /^Edit scopes/ })).toHaveCount(0)
})

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`scopes fit ${width}px in ${theme} with keyboard access`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const errors = watchErrors(page)
    await open(page)
    const dir = process.env.KEY_SCOPES_SHOTS ?? testInfo.outputDir
    mkdirSync(dir, { recursive: true })
    await agent(page).getByRole('button', { name: /^Edit scopes/ }).scrollIntoViewIfNeeded()
    await page.screenshot({ path: join(dir, `keys-${width}-${theme}.png`), fullPage: true })
    await edit(page)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(await sheet(page).evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    const result = await new AxeBuilder({ page }).include('[role="dialog"]').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
    expect(result.violations.map(v => v.id)).toEqual([])
    await page.screenshot({ path: join(dir, `scopes-${width}-${theme}.png`) })
    await sheet(page).getByRole('checkbox', { name: /knowledge\.read/ }).focus()
    await page.keyboard.press('Space')
    await expect(sheet(page).getByRole('checkbox', { name: /knowledge\.read/ })).toBeChecked()
    await page.keyboard.press('Escape')
    await expect(sheet(page)).toHaveCount(0)
    expect(errors).toEqual([])
  })
}
