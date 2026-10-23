// SPDX-License-Identifier: AGPL-3.0-only
// Coordinator: PLAYWRIGHT_PORT=5827 KX1_SCREENSHOT_DIR=<scratchpad> npm test -- tests/key-expiry.spec.ts
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { DEPLOYER, accessWorld, mockAccess } from './access-fixtures'

const NOW = '2026-09-23T12:00:00Z'
const day = 86_400_000
const expiry = (days: number) => new Date(Date.parse(NOW) + days * day).toISOString()
const agent = (page: Page) => page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'pharos-deployer' })
const row = (page: Page, prefix: string) => agent(page).locator('tbody tr').filter({ hasText: `aeon_${prefix}_` })
const posts = (world: ReturnType<typeof accessWorld>) => world.calls.filter(c => c.method === 'POST' && c.path === '/api/agent-keys')

async function open(page: Page, variants = false) {
  await page.clock.setFixedTime(new Date(NOW))
  await mockWork(page, fixtures())
  const world = accessWorld()
  world.keys.find(k => k.id === 'k2')!.expires_at = expiry(10)
  if (variants) {
    const base = world.keys.find(k => k.id === 'k2')!
    world.keys.push(
      { ...base, id: 'never', prefix: 'never', expires_at: null },
      { ...base, id: 'expired', prefix: 'expired', expires_at: expiry(-1) },
      { ...base, id: 'later', prefix: 'later', expires_at: expiry(90) },
    )
  }
  await mockAccess(page, world)
  await page.goto('/settings/access/agents')
  await agent(page).getByRole('button', { name: /active key/ }).click()
  await expect(agent(page).locator('.keys-table')).toBeVisible()
  return world
}

test('expiry and status stay separate from actions; row controls and SVGs align', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  await open(page, true)
  await expect(agent(page).getByRole('columnheader', { name: 'Expires', exact: true })).toBeVisible()
  await expect(row(page, 'ph4r').locator('[data-label=Expires]')).toContainText('In 10 days')
  await expect(row(page, 'ph4r').locator('.soon')).toBeVisible()
  await expect(row(page, 'ph4r')).toContainText('Rotate before expiry')
  await expect(row(page, 'never').locator('[data-label=Expires]')).toHaveText('Never')
  await expect(row(page, 'later').locator('[data-label=Expires]')).toHaveText('In 90 days')
  await expect(row(page, 'later').locator('.soon')).toHaveCount(0)
  await expect(row(page, 'expired').locator('[data-label=Status]')).toHaveText('Expired')
  await expect(row(page, 'expired').getByRole('button', { name: /^Rotate key/ })).toBeVisible()
  await expect(row(page, 'ph0l').locator('[data-label=Status]')).toHaveText('Revoked')
  await expect(row(page, 'ph0l').getByRole('button')).toHaveCount(0)
  await expect(agent(page).locator('[data-label=Status] button')).toHaveCount(0)
  const centers = await agent(page).evaluate(el => {
    const center = (selector: string) => { const r = el.querySelector(selector)!.getBoundingClientRect(); return r.y + r.height / 2 }
    return [center('.role-btn'), center('.keys-btn')]
  })
  expect(Math.abs(centers[0]! - centers[1]!)).toBeLessThan(1)
  // Compare the first text line, not the two-line key cell's overall height.
  const textTops = await row(page, 'ph4r').evaluate(el => [...el.querySelectorAll('td')].map(cell => {
    const walker = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT)
    let node: Node | null
    while ((node = walker.nextNode())) {
      if (!node.textContent?.trim()) continue
      const range = document.createRange(); range.selectNodeContents(node)
      return range.getBoundingClientRect().top
    }
    return 0
  }))
  expect(Math.max(...textTops) - Math.min(...textTops)).toBeLessThan(4)
  const offsets = await agent(page).locator('.actions button, .role-btn, .keys-btn').evaluateAll(buttons => buttons.flatMap(button => {
    const b = button.getBoundingClientRect()
    return [...button.querySelectorAll('svg')].map(svg => { const s = svg.getBoundingClientRect(); return Math.abs(s.y + s.height / 2 - b.y - b.height / 2) })
  }))
  expect(offsets.every(n => n < 1)).toBe(true)
  expect(errors).toEqual([])
})

for (const days of [30, 90, 365, 0]) {
  test(`new key maps ${days || 'never'} expiry to the API`, async ({ page }) => {
    const world = await open(page)
    await agent(page).getByRole('button', { name: 'New key' }).click()
    const sheet = page.getByRole('dialog', { name: 'New key for pharos-deployer' })
    await expect(sheet.getByRole('radio', { name: '90 days', exact: true })).toHaveAttribute('aria-checked', 'true')
    await expect(sheet).toContainText('Keys do not rotate automatically')
    if (days !== 90) await sheet.getByRole('radio', { name: days ? `${days} days` : 'Never', exact: true }).click()
    await sheet.getByRole('checkbox', { name: /nodes\.read/ }).check()
    await sheet.getByRole('button', { name: 'Create key' }).click()
    await expect(page.getByRole('dialog', { name: 'Key ready' })).toBeVisible()
    const body = posts(world)[0]!.body as { expires_at?: string; principal_id: string }
    expect(body.principal_id).toBe(DEPLOYER)
    expect(body.expires_at ?? null).toBe(days ? expiry(days) : null)
  })
}

test('lifetime radios work with arrow keys and keep one tab stop', async ({ page }) => {
  await open(page)
  await agent(page).getByRole('button', { name: 'New key' }).click()
  const radio = page.getByRole('radio', { name: '90 days', exact: true })
  await radio.focus()
  await page.keyboard.press('ArrowRight')
  await expect(page.getByRole('radio', { name: '365 days' })).toBeFocused()
  await expect(page.getByRole('radio', { name: '365 days' })).toHaveAttribute('aria-checked', 'true')
  await page.keyboard.press('End')
  await expect(page.getByRole('radio', { name: 'Never', exact: true })).toBeFocused()
  await expect(page.locator('.lifetimes [tabindex="0"]')).toHaveCount(1)
})

test('rotation waits for confirmation, keeps scopes, and shows the replacement only once', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const world = await open(page)
  await row(page, 'ph4r').getByRole('button', { name: /^Rotate key/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Rotate key for pharos-deployer' })
  await expect(sheet).toContainText('revoke the old key immediately when you confirm')
  await expect(sheet.locator('.rotation-scopes')).toContainText('nodes.read')
  await expect(sheet.getByRole('checkbox')).toHaveCount(0)
  expect(posts(world)).toHaveLength(0)
  await sheet.getByRole('button', { name: 'Cancel' }).click()
  expect(world.keys.find(k => k.id === 'k2')!.revoked_at).toBeNull()
  await row(page, 'ph4r').getByRole('button', { name: /^Rotate key/ }).click()
  world.slow = 500
  await sheet.getByRole('button', { name: 'Rotate key', exact: true }).click()
  await expect(sheet.getByRole('button', { name: 'Cancel' })).toBeDisabled()
  await page.keyboard.press('Escape')
  await expect(sheet).toBeVisible()
  const ready = page.getByRole('dialog', { name: 'Key ready' })
  await expect(ready).toContainText('The old key is now revoked')
  expect(posts(world)).toHaveLength(1)
  expect(posts(world)[0]!.body).toEqual({ rotate_key_id: 'k2', expires_at: expiry(90) })
  expect(world.keys[0]!.scopes).toEqual(['nodes.read'])
  const secret = await ready.getByLabel('New agent key').inputValue()
  await ready.getByRole('button', { name: 'Copy key', exact: true }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(secret)
  await ready.getByRole('button', { name: 'Done' }).click()
  await expect(page.getByLabel('New agent key')).toHaveCount(0)
  await expect(row(page, 'ph4r').locator('.state')).toHaveText('Revoked')
  expect(world.calls.filter(c => c.method === 'DELETE')).toHaveLength(0)
  await page.reload()
  await agent(page).getByRole('button', { name: /active key/ }).click()
  await expect(page.getByLabel('New agent key')).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText(secret)
})

test('a failed rotation preserves the sheet and reports the server error', async ({ page }) => {
  const world = await open(page)
  await row(page, 'ph4r').getByRole('button', { name: /^Rotate key/ }).click()
  await page.route('**/api/agent-keys', async route => {
    if (route.request().method() === 'POST') return route.fulfill({ status: 409, json: { error: 'key already revoked or rotated' } })
    await route.fallback()
  })
  const sheet = page.getByRole('dialog', { name: 'Rotate key for pharos-deployer' })
  await sheet.getByRole('button', { name: 'Rotate key', exact: true }).click()
  await expect(sheet.getByRole('alert')).toContainText('key already revoked or rotated')
  await expect(page.getByLabel('New agent key')).toHaveCount(0)
  expect(world.keys.find(k => k.id === 'k2')!.revoked_at).toBeNull()
})

test('rotation cannot silently drop scopes when the actor loses a grant', async ({ page }) => {
  const world = await open(page)
  await row(page, 'ph4r').getByRole('button', { name: /^Rotate key/ }).click()
  world.roles.find(r => r.id === 'role-owner')!.permissions = world.roles.find(r => r.id === 'role-owner')!.permissions.filter(k => k !== 'nodes.read')
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  const sheet = page.getByRole('dialog', { name: 'Rotate key for pharos-deployer' })
  await expect(sheet.getByRole('button', { name: 'Rotate key', exact: true })).toBeDisabled()
  await expect(sheet.getByRole('alert')).toContainText('Rotation keeps those scopes')
  expect(posts(world)).toHaveLength(0)
})

for (const theme of ['light'] as const) {
  test(`axe and coordinator screenshots: keys and New key sheet in ${theme}`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width: 1440, height: 1000 })
    await open(page, true)
    const dir = process.env.KX1_SCREENSHOT_DIR ?? testInfo.outputDir
    mkdirSync(dir, { recursive: true })
    const scan = async () => {
      const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      expect(result.violations.map(v => ({ id: v.id, targets: v.nodes.map(n => n.target) }))).toEqual([])
    }
    await scan()
    await page.screenshot({ path: join(dir, `kx1-keys-${theme}.png`), fullPage: true })
    await agent(page).getByRole('button', { name: 'New key' }).click()
    await expect(page.getByRole('dialog', { name: 'New key for pharos-deployer' })).toBeVisible()
    await scan()
    await page.screenshot({ path: join(dir, `kx1-new-key-${theme}.png`) })
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    await row(page, 'ph4r').getByRole('button', { name: /^Rotate key/ }).click()
    await scan()
  })

  test(`key cards and expiry sheet fit a phone in ${theme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width: 390, height: 844 })
    await open(page, true)
    await expect(row(page, 'ph4r').locator('[data-label=Expires]')).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await agent(page).getByRole('button', { name: 'New key' }).click()
    await expect(page.getByRole('radio', { name: 'Never', exact: true })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  })
}
