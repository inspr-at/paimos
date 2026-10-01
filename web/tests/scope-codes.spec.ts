// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { accessWorld, mockAccess, DEPLOYER } from './access-fixtures'
import { encodeScopeCode } from '../src/lib/scopeCode'

const agent = (page: Page) => page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'pharos-deployer' })
async function open(page: Page, configure?: (world: ReturnType<typeof accessWorld>) => void) {
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, fixtures())
  const world = accessWorld()
  configure?.(world)
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  await agent(page).getByRole('button', { name: /active key/ }).click()
  return world
}

test('New key replaces selection, checks the checksum and writes only after confirmation', async ({ page }) => {
  const world = await open(page)
  await agent(page).getByRole('button', { name: 'New key' }).click()
  const sheet = page.getByRole('dialog', { name: 'New key for pharos-deployer' })
  const input = sheet.getByLabel('Scope code', { exact: true })
  await input.fill(encodeScopeCode(['nodes.read', 'knowledge.read']))
  await expect(sheet.getByRole('checkbox', { name: /nodes\.read/ })).toBeChecked()
  await expect(sheet.getByRole('checkbox', { name: /knowledge\.read/ })).toBeChecked()
  await input.fill(encodeScopeCode(['knowledge.read']))
  await expect(sheet.getByRole('checkbox', { name: /nodes\.read/ })).not.toBeChecked()
  await input.fill(encodeScopeCode(['nodes.read']).slice(0, -1))
  await expect(sheet.getByRole('alert')).toContainText('Your selection was kept')
  await expect(sheet.getByRole('checkbox', { name: /knowledge\.read/ })).toBeChecked()
  expect(world.calls.filter(c => c.method === 'POST')).toHaveLength(0)
  await sheet.getByRole('button', { name: 'Create key', exact: true }).click()
  await expect(page.getByRole('dialog', { name: 'Key ready' })).toBeVisible()
  expect(world.keys[0]!.scopes).toEqual(['knowledge.read'])
})

test('pasted scopes beyond role, creator or registry stay unticked with reasons', async ({ page }) => {
  const world = await open(page, world => {
    world.roles.push({ ...world.roles.find(r => r.key === 'viewer')!, id: 'role-manager', key: 'key-manager', builtin: false, permissions: ['keys.manage', 'members.read', 'nodes.read', 'nodes.write'] })
    world.people[0]!.workspace_role = 'role-manager'
  })
  await agent(page).getByRole('button', { name: 'New key' }).click()
  const sheet = page.getByRole('dialog', { name: 'New key for pharos-deployer' })
  await sheet.getByLabel('Scope code', { exact: true }).fill(encodeScopeCode(['nodes.read', 'nodes.write', 'knowledge.read', 'keys.manage', 'unknown.scope']))
  await expect(sheet.getByRole('checkbox', { name: /nodes\.read/ })).toBeChecked()
  await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).not.toBeChecked()
  await expect(sheet.getByRole('checkbox', { name: /knowledge\.read/ })).not.toBeChecked()
  await expect(sheet).toContainText('beyond pharos-deployer’s role')
  await expect(sheet).toContainText('you do not hold this')
  await expect(sheet).toContainText('Unavailable to agent keys')
  await expect(sheet).toContainText('Unknown in this workspace')
  expect(world.calls.filter(c => c.method === 'POST')).toHaveLength(0)
})

test('Change scopes never turns a pasted proposal into a role extension', async ({ page }) => {
  const world = await open(page, world => {
    const viewer = world.roles.find(r => r.key === 'viewer')!
    world.roles.push({ ...viewer, id: 'role-agent', key: 'agent-custom', name: 'Agent custom', builtin: false, permissions: [...viewer.permissions] })
    world.agents.find(a => a.principal_id === DEPLOYER)!.workspace_role = 'role-agent'
  })
  await agent(page).getByRole('button', { name: /^Edit scopes/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Edit scopes for pharos-deployer' })
  await sheet.getByRole('button', { name: 'Show unavailable scopes', exact: true }).click()
  await sheet.getByLabel('Scope code', { exact: true }).fill(encodeScopeCode(['knowledge.read', 'nodes.write']))
  await expect(sheet.getByRole('checkbox', { name: /knowledge\.read/ })).toBeChecked()
  await expect(sheet.getByRole('checkbox', { name: /nodes\.read/ })).not.toBeChecked()
  await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).not.toBeChecked()
  await expect(sheet).toContainText("Not in this agent's role (Agent custom)")
  await expect(sheet.getByRole('region', { name: 'Confirm role changes' })).toHaveCount(0)
  expect(world.calls.filter(c => c.method === 'PATCH')).toHaveLength(0)
  await sheet.getByRole('button', { name: 'Save scopes', exact: true }).click()
  await expect(sheet).toHaveCount(0)
  expect(world.calls.filter(c => c.method === 'PATCH').at(-1)!.body).toEqual({ add: ['knowledge.read'], remove: ['nodes.read'] })
  expect(world.roles.find(r => r.id === 'role-agent')!.permissions).not.toContain('nodes.write')
})

test('Rotate uses a proposed set only when the operator confirms', async ({ page }) => {
  const world = await open(page)
  const row = agent(page).locator('tbody tr').filter({ hasText: 'aeon_ph4r_' })
  await row.getByRole('button', { name: /^Rotate key/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Rotate key for pharos-deployer' })
  await sheet.getByRole('searchbox', { name: 'Find a scope' }).fill('unknown.scope')
  await expect(sheet.locator('.rotation-scopes')).toContainText('No scope matches')
  expect(world.keys.find(k => k.id === 'k2')!.scopes).toEqual(['nodes.read'])
  await sheet.getByLabel('Scope code', { exact: true }).fill(encodeScopeCode(['knowledge.read', 'nodes.write']))
  await expect(sheet.getByRole('checkbox', { name: /knowledge\.read/ })).toBeChecked()
  await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).not.toBeChecked()
  await expect(sheet).toContainText('Original scopes')
  expect(world.calls.filter(c => c.method === 'POST')).toHaveLength(0)
  expect(world.keys.find(k => k.id === 'k2')!.revoked_at).toBeNull()
  await sheet.getByRole('button', { name: 'Rotate key', exact: true }).click()
  await expect(page.getByRole('dialog', { name: 'Key ready' })).toBeVisible()
  expect(world.calls.filter(c => c.method === 'POST').at(-1)!.body).toMatchObject({ rotate_key_id: 'k2', rotation_scopes: ['knowledge.read'] })
  expect(world.keys.find(k => k.id === 'k2')!.revoked_at).not.toBeNull()
  expect(world.keys[0]!.scopes).toEqual(['knowledge.read'])
})

test('Rotate caps a generated private role and cannot restore its project-only grant', async ({ page }) => {
  const world = await open(page, world => {
    const viewer = world.roles.find(r => r.key === 'viewer')!
    const privateRole = { ...viewer, id: 'role-private', key: `agent_${DEPLOYER.replace(/-/g, '')}`, name: 'Agent deployer', builtin: false, permissions: ['nodes.read'] }
    world.roles.push(privateRole)
    const target = world.agents.find(a => a.principal_id === DEPLOYER)!
    target.workspace_role = privateRole.id
    world.bindings.push({ principal_id: DEPLOYER, project_id: 'p-pharos', role_id: 'role-member' })
    world.keys.find(k => k.id === 'k2')!.scopes = ['nodes.write']
  })
  const row = agent(page).locator('tbody tr').filter({ hasText: 'aeon_ph4r_' })
  await row.getByRole('button', { name: /^Rotate key/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Rotate key for pharos-deployer' })
  await expect(sheet.getByRole('alert')).toContainText('The original scopes exceed')
  await expect(sheet.getByRole('button', { name: 'Rotate key', exact: true })).toBeDisabled()
  await sheet.getByLabel('Scope code', { exact: true }).fill(encodeScopeCode(['nodes.read', 'nodes.write']))
  await expect(sheet.getByRole('checkbox', { name: /nodes\.read/ })).toBeChecked()
  await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).not.toBeChecked()
  await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).toBeDisabled()
  expect(world.calls.filter(c => c.method === 'POST')).toHaveLength(0)
  await sheet.getByRole('button', { name: 'Rotate key', exact: true }).click()
  await expect(page.getByRole('dialog', { name: 'Key ready' })).toBeVisible()
  expect(world.calls.filter(c => c.method === 'POST').at(-1)!.body).toMatchObject({ rotate_key_id: 'k2', rotation_scopes: ['nodes.read'] })
  expect(world.roles.find(r => r.id === 'role-private')!.permissions).toEqual(['nodes.read'])
})

test('an agent without live roles cannot select scopes by code', async ({ page }) => {
  const world = await open(page, world => {
    const target = world.agents.find(a => a.principal_id === DEPLOYER)!
    target.workspace_role = null
    world.bindings = world.bindings.filter(b => b.principal_id !== DEPLOYER)
  })
  await agent(page).getByRole('button', { name: 'New key' }).click()
  const sheet = page.getByRole('dialog', { name: 'New key for pharos-deployer' })
  await sheet.getByLabel('Scope code', { exact: true }).fill(encodeScopeCode(['nodes.read', 'nodes.write']))
  await expect(sheet.getByRole('checkbox', { name: /nodes\.read/ })).not.toBeChecked()
  await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).not.toBeChecked()
  await expect(sheet).toContainText('beyond pharos-deployer’s role')
  expect(world.calls.filter(c => c.method === 'POST')).toHaveLength(0)
})

for (const theme of ['light', 'dark'] as const) for (const width of [1440, 390]) test(`scope proposals fit ${width}px in ${theme}`, async ({ page }, testInfo) => {
  const errors = watchErrors(page)
  await page.emulateMedia({ colorScheme: theme })
  await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
  await open(page)
  await agent(page).getByRole('button', { name: 'New key' }).click()
  const sheet = page.getByRole('dialog', { name: 'New key for pharos-deployer' })
  const input = sheet.getByLabel('Scope code', { exact: true })
  await input.fill(encodeScopeCode(['nodes.read', 'nodes.write', 'unknown.scope']))
  await input.focus()
  await expect(input).toBeFocused()
  expect(await sheet.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  const result = await new AxeBuilder({ page }).include('[role="dialog"]').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(result.violations.map(v => v.id)).toEqual([])
  await page.screenshot({ path: testInfo.outputPath(`scope-code-${width}-${theme}.png`) })
  expect(errors).toEqual([])
})
