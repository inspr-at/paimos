// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork } from './work-fixtures'
import { accessWorld, mockAccess, DEPLOYER } from './access-fixtures'
import { TICKET_WORKER_SCOPES } from '../src/lib/access'

async function open(page: Page, configure?: (world: ReturnType<typeof accessWorld>) => void) {
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, fixtures())
  const world = accessWorld()
  configure?.(world)
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  await expect(page.locator('.agents-tab')).toBeVisible()
  return world
}
const deployer = (page: Page) => page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'pharos-deployer' })
async function inspect(page: Page, name: string) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  expect(await page.getByRole('dialog').evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  const scan = await new AxeBuilder({ page }).include('[role="dialog"]').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
  expect(scan.violations.map(v => v.id)).toEqual([])
  if (process.env.KEY_DIALOG_SHOTS) {
    await mkdir(process.env.KEY_DIALOG_SHOTS, { recursive: true })
    await page.screenshot({ path: `${process.env.KEY_DIALOG_SHOTS}/${name}.png` })
  }
}

for (const width of [390, 1600]) for (const theme of ['light', 'dark'] as const) {
  test(`project Ticket worker flow, explanations and searchable scopes at ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const world = await open(page)
    await page.getByRole('button', { name: 'New agent', exact: true }).click()
    const form = page.getByRole('dialog', { name: 'New agent', exact: true })
    await expect(form.getByLabel('Role', { exact: false })).toHaveValue('role-viewer')
    await form.getByLabel('Name', { exact: true }).fill('ticket-helper')
    await form.getByLabel('Purpose preset').selectOption('ticket-worker')
    await expect(form.getByLabel('Project access', { exact: true })).toHaveValue('projects')
    await form.getByRole('checkbox', { name: 'Pharos', exact: true }).check()
    await expect(form.getByLabel('Role', { exact: false })).toHaveValue('role-member')
    await expect(form).toContainText('Member covers Ticket worker')
    await expect(form.getByLabel('Description')).toHaveValue('Ticket worker for PHAROS')
    await expect(form.locator('#agent-role option[value="role-member"]')).toContainText('Can ')
    await inspect(page, `agent-${width}-${theme}`)
    await form.getByRole('button', { name: 'Create agent', exact: true }).click()
    const key = page.getByRole('dialog', { name: 'Create first key for ticket-helper' })
    await expect(key.locator('details').filter({ hasText: 'Ticket worker' })).toHaveAttribute('open', '')
    expect(world.keys.filter(k => k.name === 'ticket-helper')).toHaveLength(0)
    await expect(key.getByRole('checkbox', { checked: true })).toHaveCount(0)
    await expect(key.locator('details').filter({ hasText: 'Ticket worker' })).toContainText('comments.read')
    await key.getByRole('button', { name: 'Apply Ticket worker' }).click()
    await expect(key.getByRole('checkbox', { checked: true })).toHaveCount(5)
    for (const term of ['nodes.write', 'EDIT WORK', 'Search', 'Read ticket comments']) {
      await key.getByRole('searchbox', { name: 'Find a scope' }).fill(term)
      await expect(key.locator('.scope-row')).toHaveCount(1)
    }
    await expect(key.locator('.scope-row')).toContainText('comments.read')
    await expect(key.locator('.scope-row')).toContainText('Read ticket comments')
    await expect(key.locator('.scope-row')).toContainText('Low')
    await key.getByRole('searchbox', { name: 'Find a scope' }).fill('nodes.write')
    await expect(key.locator('.scope-row')).toContainText('Medium')
    await inspect(page, `key-${width}-${theme}`)
    await key.getByRole('button', { name: 'Create first key', exact: true }).click()
    await expect(page.getByRole('dialog', { name: 'Key ready', exact: true })).toBeVisible()
    expect(world.keys.find(k => k.name === 'ticket-helper')!.scopes).toEqual(TICKET_WORKER_SCOPES)
    const created = world.agents.find(a => a.name === 'ticket-helper')!
    expect(created.workspace_role).toBeNull()
    expect(created.description).toBe('Ticket worker for PHAROS')
    expect(world.bindings.filter(b => b.principal_id === created.principal_id)).toEqual([{ principal_id: created.principal_id, project_id: 'p-pharos', role_id: 'role-member' }])
  })
}

test('edited description and role survive project changes; empty description needs confirmation', async ({ page }) => {
  const world = await open(page)
  await page.getByRole('button', { name: 'New agent', exact: true }).click()
  const form = page.getByRole('dialog', { name: 'New agent', exact: true })
  await form.getByLabel('Name', { exact: true }).fill('described-helper')
  await form.getByLabel('Purpose preset').selectOption('ticket-worker')
  await form.getByRole('checkbox', { name: 'Pharos', exact: true }).check()
  await form.getByLabel('Description').fill('Only triage release reports')
  await form.locator('#agent-role').selectOption('role-viewer')
  await form.getByRole('checkbox', { name: 'Aeon', exact: true }).check()
  await expect(form.getByLabel('Description')).toHaveValue('Only triage release reports')
  await expect(form.locator('#agent-role')).toHaveValue('role-viewer')
  await form.getByLabel('Description').fill('')
  await form.getByRole('button', { name: 'Create agent', exact: true }).click()
  await expect(form).toContainText('Add a description or confirm creating without one')
  expect(world.calls.filter(c => c.method === 'POST' && c.path === '/api/members/agents')).toHaveLength(0)
  await form.getByRole('checkbox', { name: 'Create without a description' }).check()
  await form.getByRole('button', { name: 'Create agent', exact: true }).click()
  await expect(page.getByRole('dialog', { name: 'Create first key for described-helper' })).toBeVisible()
  expect(world.agents.find(a => a.name === 'described-helper')!.description).toBe('')
})

test('no grantable covering role leaves the choice explicit; Admin is never selected', async ({ page }) => {
  await open(page, world => {
    const member = world.roles.find(r => r.key === 'member')!
    world.roles = world.roles.filter(r => r.key !== 'member' && r.key !== 'delivery-lead')
    world.roles.push({ ...member, id: 'ungivable', key: 'ungivable', name: 'Ungivable', builtin: false, permissions: [...TICKET_WORKER_SCOPES, 'outside.authority'] })
  })
  await page.getByRole('button', { name: 'New agent', exact: true }).click()
  const form = page.getByRole('dialog', { name: 'New agent', exact: true })
  await form.getByLabel('Purpose preset').selectOption('ticket-worker')
  await expect(form.locator('#agent-role')).toHaveValue('')
  await expect(form).toContainText('No non-admin role you may grant covers all of Ticket worker')
  await expect(form.locator('#agent-role option[value="ungivable"]')).toBeDisabled()
})

test('new key preset clips to both the creator and agent role, then reacts to lost permission', async ({ page }) => {
  const world = await open(page, world => {
    const member = world.roles.find(r => r.key === 'member')!
    world.agents.find(a => a.principal_id === DEPLOYER)!.workspace_role = member.id
    world.roles.push({ ...member, id: 'manager', key: 'key-manager', name: 'Key manager', builtin: false, permissions: ['keys.manage', 'members.read', 'nodes.read', 'nodes.write', 'comments.read'] })
    world.people[0]!.workspace_role = 'manager'
  })
  await deployer(page).getByRole('button', { name: /active key/ }).click()
  await deployer(page).getByRole('button', { name: 'New key' }).click()
  const key = page.getByRole('dialog', { name: 'New key for pharos-deployer' })
  await key.locator('summary').filter({ hasText: 'Ticket worker' }).click()
  await expect(key.locator('details').filter({ hasText: 'Ticket worker' })).toContainText('3/5 available')
  await key.getByRole('button', { name: 'Apply Ticket worker' }).click()
  await expect(key.getByRole('checkbox', { name: /comments\.write/ })).toBeDisabled()
  world.roles.find(r => r.id === 'manager')!.permissions = ['keys.manage', 'members.read', 'nodes.read', 'comments.read']
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(key.getByRole('checkbox', { name: /nodes\.write/ })).toBeDisabled()
  await expect(key.getByRole('checkbox', { name: /nodes\.write/ })).not.toBeChecked()
  await key.getByRole('button', { name: 'Create key', exact: true }).click()
  await expect(page.getByRole('dialog', { name: 'Key ready' })).toBeVisible()
  expect(world.keys[0]!.scopes).toEqual(['nodes.read', 'comments.read'])
})

test('scope editor preset never extends a custom role; rotation search never changes scopes', async ({ page }) => {
  const world = await open(page, world => {
    const viewer = world.roles.find(r => r.key === 'viewer')!
    world.roles.push({ ...viewer, id: 'role-agent', key: 'agent-worker', name: 'Agent worker', builtin: false })
    world.agents.find(a => a.principal_id === DEPLOYER)!.workspace_role = 'role-agent'
  })
  await deployer(page).getByRole('button', { name: /active key/ }).click()
  await deployer(page).getByRole('button', { name: /^Edit scopes/ }).click()
  const editor = page.getByRole('dialog', { name: 'Edit scopes for pharos-deployer' })
  await editor.locator('summary').filter({ hasText: 'Ticket worker' }).click()
  await expect(editor.locator('details').filter({ hasText: 'Ticket worker' })).toContainText('1/5 available')
  await editor.getByRole('button', { name: 'Apply Ticket worker' }).click()
  await expect(editor.getByRole('checkbox', { name: /nodes\.write/ })).not.toBeChecked()
  await expect(editor.getByRole('region', { name: 'Confirm role changes' })).toHaveCount(0)
  await editor.getByRole('searchbox', { name: 'Find a scope' }).fill('nodes.read')
  await expect(editor.locator('.scope-row')).toContainText('See work')
  await expect(editor.locator('.scope-row')).toContainText('See projects, tickets and their history')
  await expect(editor.locator('.scope-row')).toContainText('Low')
  await editor.getByRole('button', { name: 'Cancel', exact: true }).click()
  expect(world.calls.filter(c => c.method === 'PATCH')).toHaveLength(0)
  await deployer(page).getByRole('button', { name: /^Rotate key/ }).click()
  const rotation = page.getByRole('dialog', { name: 'Rotate key for pharos-deployer' })
  await expect(rotation.locator('.rotation-scopes')).toContainText('See projects, tickets and their history')
  await rotation.getByRole('searchbox', { name: 'Find a scope' }).fill('no matching scope')
  await expect(rotation).toContainText('No scope matches')
  await expect(rotation.getByRole('button', { name: 'Apply Ticket worker' })).toHaveCount(0)
  await rotation.getByRole('button', { name: 'Rotate key', exact: true }).click()
  await expect(page.getByRole('dialog', { name: 'Key ready' })).toBeVisible()
  expect(world.keys[0]!.scopes).toEqual(['nodes.read'])
  expect(world.calls.filter(c => c.method === 'POST' && c.path === '/api/agent-keys').at(-1)!.body).toEqual({ rotate_key_id: 'k2', expires_at: '2026-12-22T12:00:00.000Z' })
})
