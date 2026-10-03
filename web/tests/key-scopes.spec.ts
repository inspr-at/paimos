// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { accessWorld, mockAccess, DEPLOYER } from './access-fixtures'
import { expectStableControls } from './helpers/stable'

const agent = (page: Page) => page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'pharos-deployer' })
const sheet = (page: Page) => page.getByRole('dialog', { name: 'Edit scopes for pharos-deployer' })
async function open(page: Page, role: 'owner' | 'member' = 'owner', configure?: (world: ReturnType<typeof accessWorld>) => void) {
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, fixtures())
  const world = accessWorld({ role })
  configure?.(world)
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  if (role === 'owner') await agent(page).getByRole('button', { name: /active key/ }).click()
  return world
}
function customAgentRole(world: ReturnType<typeof accessWorld>, shared = false) {
  const viewer = world.roles.find(r => r.key === 'viewer')!
  world.roles.push({ ...viewer, id: 'role-agent', key: 'agent-workstation', name: 'Agent workstation-agents', builtin: false, permissions: [...viewer.permissions] })
  world.agents.find(a => a.principal_id === DEPLOYER)!.workspace_role = 'role-agent'
  if (shared) world.agents[0]!.workspace_role = 'role-agent'
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

test('owner confirms role and key together, with shared-role impact', async ({ page }) => {
  const world = await open(page, 'owner', world => customAgentRole(world, true))
  await edit(page)
  const row = sheet(page).getByRole('checkbox', { name: /nodes\.write/ })
  await expect(row).toBeEnabled()
  await expect(sheet(page)).toContainText("Not in this agent's role (Agent workstation-agents)")
  await expect(sheet(page)).not.toContainText('Outside current permissions')
  await row.check()
  await expect(sheet(page).getByRole('region', { name: 'Confirm role changes' })).toContainText("Also add to the agent's role?")
  await expect(sheet(page)).toContainText('shared by 2 people and agents')
  expect(world.calls.filter(c => c.method === 'PATCH')).toHaveLength(0)
  const count = world.events.length
  await sheet(page).getByRole('button', { name: 'Add to role and save scopes' }).click()
  await expect(sheet(page)).toHaveCount(0)
  expect(world.calls.filter(c => c.method === 'PATCH').map(c => c.body)).toEqual([{ add: ['nodes.write'], remove: [], role_extension: { role_id: 'role-agent', add: ['nodes.write'] } }])
  expect(world.events).toHaveLength(count + 1)
  expect(world.roles.find(r => r.id === 'role-agent')!.permissions).toContain('nodes.write')
  expect(world.keys.find(k => k.id === 'k2')!.scopes).toEqual(['nodes.read', 'nodes.write'])
  expect(world.keys.find(k => k.id === 'k1')!.scopes).toEqual([])
  expect(world.events.at(-1)?.after).toMatchObject({ scopes: ['nodes.read', 'nodes.write'], role: { id: 'role-agent', permissions: expect.arrayContaining(['nodes.write']) } })
})

test('cancel or untick a role extension writes nothing', async ({ page }) => {
  const world = await open(page, 'owner', world => customAgentRole(world))
  await edit(page)
  await sheet(page).getByRole('checkbox', { name: /nodes\.write/ }).check()
  await sheet(page).getByRole('checkbox', { name: /nodes\.write/ }).uncheck()
  await expect(sheet(page).getByRole('region', { name: 'Confirm role changes' })).toHaveCount(0)
  await expect(sheet(page).getByRole('button', { name: 'Save scopes' })).toBeDisabled()
  await sheet(page).getByRole('checkbox', { name: /nodes\.write/ }).check()
  await sheet(page).getByRole('button', { name: 'Cancel', exact: true }).click()
  expect(world.calls.filter(c => c.method === 'PATCH')).toHaveLength(0)
  expect(world.roles.find(r => r.id === 'role-agent')!.permissions).not.toContain('nodes.write')
})

test('non-owner key manager sees the agent role limit and cannot extend it', async ({ page }) => {
  const world = await open(page, 'owner', world => {
    customAgentRole(world)
    world.roles.push({ ...world.roles.find(r => r.key === 'viewer')!, id: 'role-manager', key: 'key-manager', name: 'Key manager', builtin: false, permissions: ['keys.manage', 'nodes.read', 'nodes.write', 'knowledge.read', 'members.read'] })
    world.people[0]!.workspace_role = 'role-manager'
  })
  await edit(page)
  await sheet(page).getByRole('button', { name: 'Show unavailable scopes' }).click()
  await expect(sheet(page).getByRole('checkbox', { name: /nodes\.write/ })).toBeDisabled()
  await expect(sheet(page)).toContainText("Not in this agent's role (Agent workstation-agents)")
  await expect(sheet(page)).not.toContainText('Outside current permissions')
  await expect(sheet(page).getByRole('button', { name: 'Add to role and save scopes' })).toHaveCount(0)
  await sheet(page).getByRole('checkbox', { name: /knowledge\.read/ }).check()
  await sheet(page).getByRole('button', { name: 'Save scopes' }).click()
  await expect(sheet(page)).toHaveCount(0)
  expect(world.roles.find(r => r.id === 'role-agent')!.permissions).not.toContain('nodes.write')
})

test('unknown stored scopes are cleaned when the sheet loads', async ({ page }) => {
  const world = await open(page, 'owner', world => { world.keys.find(k => k.id === 'k2')!.scopes.push('retired.scope') })
  const count = world.events.length
  await edit(page)
  await expect(sheet(page).getByRole('checkbox', { name: /retired\.scope/ })).toHaveCount(0)
  expect(world.keys.find(k => k.id === 'k2')!.scopes).toEqual(['nodes.read'])
  expect(world.events).toHaveLength(count + 1)
  expect(world.events.at(-1)?.after).toMatchObject({ pruned_scopes: ['retired.scope'] })
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

const scopeLayouts = [{ width: 1600 }, { width: 1440 }, { width: 390 }, { width: 390, wideFont: true }]
for (const theme of ['light', 'dark'] as const) for (const { width, wideFont } of scopeLayouts) {
  test(`scopes fit ${width}px in ${theme} with keyboard access${wideFont ? ' (wide system font)' : ''}`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const errors = watchErrors(page)
    await open(page, 'owner', world => customAgentRole(world, true))
    const dir = process.env.KEY_SCOPES_SHOTS ?? testInfo.outputDir
    mkdirSync(dir, { recursive: true })
    await agent(page).getByRole('button', { name: /^Edit scopes/ }).scrollIntoViewIfNeeded()
    await page.screenshot({ path: join(dir, `keys-${width}-${theme}.png`), fullPage: true })
    await edit(page)
    const dialog = sheet(page)
    // OS font metrics differ; a wider font must not expand the action on role confirmation.
    if (wideFont) await page.addStyleTag({ content: '.actions .btn { font-family: monospace; }' })
    const scope = dialog.getByRole('checkbox', { name: /nodes\.write/ })
    await expectStableControls({
      controls: {
        frame: dialog,
        cancel: dialog.getByRole('button', { name: 'Cancel', exact: true }),
        save: dialog.locator('.actions .primary'),
        actions: dialog.locator('.actions'),
        lifetime: dialog.getByRole('radiogroup'),
        presets: dialog.locator('.preset-actions'),
        scope,
        row: dialog.locator('.scope-row').filter({ has: page.getByRole('checkbox', { name: /nodes\.write/ }) }),
      },
      scrollAreas: { sheet: dialog, body: dialog.locator('.sheet-body') },
      interactions: [{ name: 'confirm role extension', run: async () => {
        await scope.check()
        await expect(dialog.getByRole('region', { name: 'Confirm role changes' })).toBeVisible()
      } }],
    })
    // Actions stay at the top; the long role explanation is in the scrolling body.
    await sheet(page).getByRole('region', { name: 'Confirm role changes' }).scrollIntoViewIfNeeded()
    const confirmation = await sheet(page).getByRole('region', { name: 'Confirm role changes' }).boundingBox()
    expect(confirmation!.y + confirmation!.height).toBeLessThanOrEqual(width === 390 ? 844 : 1000)
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
