// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { accessWorld, mockAccess, DEPLOYER, REGISTRY } from './access-fixtures'

const LONG_SCOPE = `imports.${'workspace_metadata_'.repeat(6)}read`
const LONG_ROLE = `Deployment${'Automation'.repeat(10)}`
const agent = (page: Page) => page.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: 'pharos-deployer' })

async function open(page: Page, firstKey: boolean) {
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, fixtures())
  const world = accessWorld()
  world.roles.find(r => r.key === 'viewer')!.name = LONG_ROLE
  if (firstKey) world.keys = world.keys.filter(k => k.principal_id !== DEPLOYER)
  else world.keys.find(k => k.id === 'k2')!.scopes.push(LONG_SCOPE)
  await mockAccess(page, world)
  // Long ids exercise wrapping independently of the length of the reason.
  await page.route('**/api/authz/permissions', route => route.fulfill({ json: [...REGISTRY,
    { ...REGISTRY[0], key: LONG_SCOPE, group: 'Imports' },
    { ...REGISTRY[0], key: 'imports.manage', group: 'Imports' },
  ] }))
  await page.goto('/settings/access/agents')
  await agent(page).getByRole('button', { name: /active keys?/ }).click()
  return world
}

async function fits(dialog: Locator, scopeRows = false) {
  await expect(dialog).toBeVisible()
  await dialog.page().evaluate(() => document.fonts.ready)
  const issues = await dialog.evaluate((el, checkRows) => {
    const failures: string[] = []
    const body = el.querySelector('.sheet-body')!
    for (const [name, item] of [['dialog', el], ['scroll area', body]] as const) {
      if (item.scrollWidth > item.clientWidth + 1) failures.push(`${name} overflows by ${item.scrollWidth - item.clientWidth}px`)
    }
    const edge = el.getBoundingClientRect()
    if (edge.left < -1 || edge.right > innerWidth + 1) failures.push('dialog exceeds viewport')
    if (!checkRows) return failures
    const rows = [...el.querySelectorAll('.scope-row')]
    if (!rows.length) failures.push('scope rows missing')
    for (const row of rows) {
      const box = row.getBoundingClientRect()
      const label = row.textContent?.trim()
      if (row.scrollWidth > row.clientWidth + 1) failures.push(`row overflows: ${label}`)
      for (const span of row.querySelectorAll('.scope-text > span')) {
        const range = document.createRange()
        range.selectNodeContents(span)
        // Text fragment bounds catch overlap even when a clipping container
        // would hide its scrollbar, and cover rows below the vertical fold.
        for (const rect of range.getClientRects()) {
          if (rect.left < box.left - 1 || rect.right > box.right + 1) failures.push(`text leaves its column: ${span.textContent}`)
        }
      }
    }
    for (let i = 0; i < rows.length; i++) for (let j = i + 1; j < rows.length; j++) {
      const a = rows[i]!.getBoundingClientRect(), b = rows[j]!.getBoundingClientRect()
      if (Math.min(a.right, b.right) - Math.max(a.left, b.left) > 1 && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 1) failures.push('scope rows overlap')
    }
    return failures
  }, scopeRows)
  expect(issues).toEqual([])
}

async function tooltips(dialog: Locator) {
  const texts = dialog.locator('.scope-text > span:not(:first-child)')
  expect(await texts.count()).toBeGreaterThan(0)
  for (const text of await texts.all()) await expect(text).toHaveAttribute('title', (await text.textContent())!)
}

for (const width of [360, 768, 1280]) for (const theme of ['light', 'dark'] as const) {
  test(`new, edit and rotate scopes stay in their columns at ${width}px in ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ colorScheme: theme })
    const errors = watchErrors(page)
    const world = await open(page, false)
    await agent(page).getByRole('button', { name: 'New key', exact: true }).click()
    const fresh = page.getByRole('dialog', { name: 'New key for pharos-deployer', exact: true })
    await expect(fresh.getByRole('checkbox', { name: /nodes\.write/ })).toBeDisabled()
    await expect(fresh.locator('.scope-reason').filter({ hasText: LONG_ROLE }).first()).toBeVisible()
    await fits(fresh, true)
    await tooltips(fresh)
    await fresh.locator('.scope-group').filter({ hasText: 'Imports' }).scrollIntoViewIfNeeded()
    await page.screenshot({ path: testInfo.outputPath(`new-key-${width}-${theme}.png`) })
    await fresh.getByRole('button', { name: 'Cancel', exact: true }).click()

    await agent(page).getByRole('button', { name: /^Edit scopes/ }).click()
    const edit = page.getByRole('dialog', { name: 'Edit scopes for pharos-deployer', exact: true })
    await edit.getByRole('button', { name: 'Show unavailable scopes', exact: true }).click()
    await expect(edit.getByRole('checkbox', { name: /nodes\.write/ })).toBeDisabled()
    await expect(edit).toContainText(LONG_ROLE)
    await fits(edit, true)
    await tooltips(edit)
    await edit.getByRole('button', { name: 'Cancel', exact: true }).click()

    // Restore the long stored scope pruned by the standard fixture's edit GET.
    world.keys.find(k => k.id === 'k2')!.scopes.push(LONG_SCOPE)
    await page.reload()
    await agent(page).getByRole('button', { name: /active keys?/ }).click()
    await agent(page).locator('tbody tr').filter({ hasText: 'aeon_ph4r_' }).getByRole('button', { name: /^Rotate key/ }).click()
    const rotate = page.getByRole('dialog', { name: 'Rotate key for pharos-deployer', exact: true })
    await expect(rotate.locator('.rotation-scopes')).toContainText(LONG_SCOPE)
    await expect(rotate.locator('.rotation-scopes li').filter({ hasText: LONG_SCOPE })).toHaveAttribute('title', LONG_SCOPE)
    await fits(rotate)
    // Check the rendered long id, rather than only the dialog's clipped bounds.
    expect(await rotate.locator('.rotation-scopes').evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true)
    expect(errors).toEqual([])
    expect(world.calls.some(c => c.method === 'POST' && c.path === '/api/agent-keys')).toBe(false)
  })

  test(`first key and key ready fit at ${width}px in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ colorScheme: theme })
    await open(page, true)
    await agent(page).getByRole('button', { name: 'Create first key', exact: true }).click()
    const first = page.getByRole('dialog', { name: 'Create first key for pharos-deployer', exact: true })
    await fits(first, true)
    await tooltips(first)
    await first.getByRole('checkbox', { name: /nodes\.read/ }).check()
    await first.getByRole('button', { name: 'Create first key', exact: true }).click()
    const ready = page.getByRole('dialog', { name: 'Key ready', exact: true })
    await expect(ready.getByLabel('New agent key')).toBeVisible()
    await fits(ready)
  })
}
