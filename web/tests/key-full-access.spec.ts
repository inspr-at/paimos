// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Locator, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { accessWorld, mockAccess, DEPLOYER, REGISTRY } from './access-fixtures'

const row = (page: Page) => page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'pharos-deployer' })
async function open(page: Page, role: 'admin' | 'member' = 'admin', custom = false) {
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, fixtures())
  const world = accessWorld()
  if (custom) {
    world.roles.push({ ...world.roles.find(r => r.key === 'viewer')!, id: 'role-private', key: `agent_${DEPLOYER.replace(/-/g, '')}`, name: 'Agent private', builtin: false })
    world.agents.find(a => a.principal_id === DEPLOYER)!.workspace_role = 'role-private'
  } else world.agents.find(a => a.principal_id === DEPLOYER)!.workspace_role = `role-${role}`
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  await row(page).getByRole('button', { name: /active key/ }).click()
  return world
}
async function edit(page: Page) {
  await row(page).getByRole('button', { name: /^Edit scopes/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Edit scopes for pharos-deployer' })
  await expect(sheet.getByRole('radio', { name: 'Keep current' })).toBeChecked()
  return sheet
}
async function create(page: Page) {
  await row(page).getByRole('button', { name: 'New key', exact: true }).click()
  return page.getByRole('dialog', { name: 'New key for pharos-deployer' })
}
const checked = (sheet: Locator) => sheet.getByRole('checkbox', { checked: true })
const patches = (world: ReturnType<typeof accessWorld>) => world.calls.filter(c => c.method === 'PATCH' && c.path === '/api/agent-keys/k2/scopes')

for (const role of ['admin', 'member'] as const) for (const mode of ['new', 'edit'] as const) {
  test(`${mode} Full access follows ${role}, with group All/None bounded to visible scopes`, async ({ page }) => {
    const world = await open(page, role)
    const sheet = await (mode === 'new' ? create(page) : edit(page))
    const expected = REGISTRY.filter(p => p.agent_grantable && world.roles.find(r => r.key === role)!.permissions.includes(p.key)).map(p => p.key)
    await expect(sheet.getByRole('button', { name: 'Apply Full access' })).toBeVisible()
    await sheet.getByRole('button', { name: 'Apply Full access' }).click()
    await expect(checked(sheet)).toHaveCount(expected.length)
    for (const key of expected) await expect(sheet.getByRole('checkbox', { name: new RegExp(key.replace('.', '\\.' )) })).toBeChecked()
    await expect(sheet).toContainText('Person-only: managing members, roles, keys and settings')
    await expect(sheet).toContainText('approval decisions; rule publishing; conversation watching')
    await expect(sheet).toContainText('reading keys')
    await expect(sheet).toContainText('harness force-stop and recovery; ownership transfer; the customer portal')
    const work = sheet.getByRole('group', { name: 'Work', exact: true })
    const workKeys = REGISTRY.filter(p => p.group === 'Work' && expected.includes(p.key)).map(p => p.key)
    await work.getByRole('button', { name: 'None', exact: true }).click()
    await expect(checked(sheet)).toHaveCount(expected.length - workKeys.length)
    await work.getByRole('button', { name: 'All', exact: true }).click()
    await expect(checked(sheet)).toHaveCount(expected.length)
    // Search limits group actions; hidden selections in the same group survive.
    await sheet.getByRole('searchbox', { name: 'Find a scope' }).fill('nodes.read')
    await work.getByRole('button', { name: 'None', exact: true }).click()
    await sheet.getByRole('searchbox', { name: 'Find a scope' }).fill('')
    await expect(sheet.getByRole('checkbox', { name: /nodes\.read/ })).not.toBeChecked()
    await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).toBeChecked()
    await sheet.getByRole('button', { name: 'Apply Full access' }).click()
    await expect(sheet.getByRole('region', { name: 'Confirm role changes' })).toHaveCount(0)
    const count = world.keys.length
    if (mode === 'edit') {
      await sheet.getByRole('button', { name: 'Save scopes', exact: true }).click()
      await expect(sheet).toHaveCount(0)
      expect([...world.keys.find(k => k.id === 'k2')!.scopes].sort()).toEqual([...expected].sort())
      expect(world.keys).toHaveLength(count)
      expect(world.keys.find(k => k.id === 'k2')).toMatchObject({ prefix: 'ph4r', revoked_at: null, expires_at: '2026-12-31T00:00:00Z' })
      expect(world.calls.filter(c => c.method === 'POST' && c.path === '/api/agent-keys')).toHaveLength(0)
      expect(patches(world)[0]!.body).not.toHaveProperty('expires_at')
    } else {
      await sheet.getByRole('button', { name: 'Create key', exact: true }).click()
      await expect(page.getByRole('dialog', { name: 'Key ready' })).toBeVisible()
      expect([...world.keys[0]!.scopes].sort()).toEqual([...expected].sort())
    }
  })
}

test('bulk presets never extend a dedicated role; explicit checkbox extension still confirms', async ({ page }) => {
  const world = await open(page, 'admin', true)
  const sheet = await edit(page)
  await sheet.getByRole('button', { name: 'Apply Full access' }).click()
  await expect(checked(sheet)).toHaveCount(4) // viewer: work, knowledge, quotes, member list
  await sheet.getByRole('group', { name: 'Work', exact: true }).getByRole('button', { name: 'All', exact: true }).click()
  await expect(sheet.getByRole('checkbox', { name: /nodes\.write/ })).not.toBeChecked()
  await expect(sheet.getByRole('region', { name: 'Confirm role changes' })).toHaveCount(0)
  const saveBefore = await sheet.getByRole('button', { name: 'Save scopes', exact: true }).boundingBox()
  await sheet.getByRole('checkbox', { name: /nodes\.write/ }).check()
  await expect(sheet.getByRole('region', { name: 'Confirm role changes' })).toBeVisible()
  expect(await sheet.getByRole('button', { name: 'Add to role and save scopes' }).boundingBox()).toEqual(saveBefore)
  await sheet.getByRole('button', { name: 'Add to role and save scopes' }).click()
  await expect(sheet).toHaveCount(0)
  expect(world.roles.find(r => r.id === 'role-private')!.permissions).toContain('nodes.write')
  const next = await create(page)
  await next.getByRole('button', { name: 'Apply Full access' }).click()
  await expect(checked(next)).toHaveCount(5)
  await expect(next.getByRole('checkbox', { name: /nodes\.delete/ })).not.toBeChecked()
})

test('expiry edits including Never keep the existing key; Keep current and Cancel write nothing', async ({ page }) => {
  const world = await open(page)
  const count = world.keys.length
  let sheet = await edit(page)
  await sheet.getByRole('radio', { name: '30 days', exact: true }).click()
  await sheet.getByRole('radio', { name: 'Keep current' }).click()
  await expect(sheet.getByRole('button', { name: 'Save scopes', exact: true })).toBeDisabled()
  await sheet.getByRole('radio', { name: 'Never', exact: true }).click()
  await sheet.getByRole('button', { name: 'Cancel', exact: true }).click()
  expect(patches(world)).toHaveLength(0)
  for (const days of [30, 90, 365, 0]) {
    sheet = await edit(page)
    await sheet.getByRole('radio', { name: days ? `${days} days` : 'Never', exact: true }).click()
    await sheet.getByRole('button', { name: 'Save scopes', exact: true }).click()
    await expect(sheet).toHaveCount(0)
    const expiry = days ? new Date(Date.parse('2026-09-23T12:00:00Z') + days * 86_400_000).toISOString() : null
    expect(patches(world).at(-1)!.body).toEqual({ add: [], remove: [], expires_at: expiry })
    expect(world.keys.find(k => k.id === 'k2')).toMatchObject({ expires_at: expiry, scopes: ['nodes.read'], prefix: 'ph4r', revoked_at: null })
  }
  expect(world.keys).toHaveLength(count)
  expect(world.calls.filter(c => c.method === 'POST' && c.path === '/api/agent-keys')).toHaveLength(0)
  sheet = await edit(page)
  await sheet.getByRole('radio', { name: 'Never', exact: true }).click()
  await expect(sheet.getByRole('button', { name: 'Save scopes', exact: true })).toBeDisabled()
})

test('expiry failure retains draft; modifier Enter saves and Escape leaves a field before closing', async ({ page }) => {
  const world = await open(page)
  const sheet = await edit(page)
  await sheet.getByRole('radio', { name: 'Never', exact: true }).click()
  await page.route('**/api/agent-keys/k2/scopes', route => route.request().method() === 'PATCH' ? route.fulfill({ status: 403, json: { error: 'forbidden' } }) : route.fallback())
  await sheet.getByRole('button', { name: 'Save scopes', exact: true }).click()
  await expect(sheet.getByRole('alert')).toContainText('Access changed')
  await expect(sheet.getByRole('radio', { name: 'Never', exact: true })).toBeChecked()
  await expect(sheet).toBeVisible()
  expect(world.keys.find(k => k.id === 'k2')!.expires_at).toBe('2026-12-31T00:00:00Z')
  await sheet.getByRole('button', { name: 'Reload scopes' }).click()
  await expect(sheet.getByRole('radio', { name: 'Keep current' })).toBeChecked()
  await sheet.getByRole('searchbox', { name: 'Find a scope' }).focus()
  await page.keyboard.press('Escape')
  await expect(sheet).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(sheet).toHaveCount(0)
  await page.unroute('**/api/agent-keys/k2/scopes')
  const next = await edit(page)
  await next.getByRole('radio', { name: 'Never', exact: true }).click()
  await next.getByRole('searchbox', { name: 'Find a scope' }).focus()
  const mac = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform))
  await page.keyboard.press(mac ? 'Meta+Enter' : 'Control+Enter')
  await expect(next).toHaveCount(0)
  expect(world.keys.find(k => k.id === 'k2')!.expires_at).toBeNull()
})

for (const width of [390, 1600]) for (const theme of ['light', 'dark'] as const) {
  test(`key sheet controls stay anchored at ${width}px ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await page.emulateMedia({ colorScheme: theme })
    const errors = watchErrors(page)
    await open(page)
    for (const mode of ['new', 'edit'] as const) {
      const sheet = await (mode === 'new' ? create(page) : edit(page))
      const actions = sheet.locator('.sheet-foot')
      const before = await actions.boundingBox()
      const close = sheet.getByRole('button', { name: /^Close / })
      const closeBefore = await close.boundingBox()
      await sheet.getByRole('button', { name: 'Apply Full access' }).click()
      await sheet.locator('summary').filter({ hasText: 'Full access' }).click()
      await sheet.getByRole('searchbox', { name: 'Find a scope' }).fill('no matching scopes')
      await sheet.getByRole('radio', { name: 'Never', exact: true }).click()
      expect(await actions.boundingBox()).toEqual(before)
      expect(await close.boundingBox()).toEqual(closeBefore)
      expect(await sheet.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
      const result = await new AxeBuilder({ page }).include('[role="dialog"]').withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze()
      expect(result.violations.map(v => v.id)).toEqual([])
      await sheet.getByRole('searchbox', { name: 'Find a scope' }).fill('')
      await sheet.locator('summary').filter({ hasText: 'Full access' }).click()
      if (process.env.KEY_FULL_ACCESS_SHOTS) {
        await mkdir(process.env.KEY_FULL_ACCESS_SHOTS, { recursive: true })
        await page.screenshot({ path: `${process.env.KEY_FULL_ACCESS_SHOTS}/${mode}-${width}-${theme}.png` })
      }
      await sheet.getByRole('button', { name: 'Cancel', exact: true }).click()
    }
    expect(errors).toEqual([])
  })
}

test('New agent Full access requires an explicit role choice and passes the preset to its first key', async ({ page }) => {
  const world = await open(page)
  await page.getByRole('button', { name: 'New agent', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'New agent', exact: true })
  await sheet.getByLabel('Name', { exact: true }).fill('full-agent')
  await sheet.getByLabel('Purpose preset').selectOption('full-access')
  await expect(sheet.getByLabel('Role', { exact: false })).toHaveValue('')
  await expect(sheet).toContainText('Admin allows the full agent-grantable set')
  await sheet.getByLabel('Role', { exact: false }).selectOption('role-admin')
  await sheet.getByRole('button', { name: 'Create agent', exact: true }).click()
  const key = page.getByRole('dialog', { name: 'Create first key for full-agent' })
  await expect(key.getByRole('button', { name: 'Apply Full access' })).toBeVisible()
  await key.getByRole('button', { name: 'Apply Full access' }).click()
  expect(world.calls.find(c => c.method === 'POST' && c.path === '/api/members/agents')!.body).toMatchObject({ workspace_role_id: 'role-admin' })
})
