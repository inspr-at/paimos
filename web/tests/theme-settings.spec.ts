// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fixtures, mockWork, me, watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import type { ActiveTheme, ThemeRecord } from '../src/lib/themes'
const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value))
const record = (id: string, name: string, scope: ThemeRecord['scope']): ThemeRecord => ({ id, name, scope, tenant_id: 't1', owner_principal_id: scope === 'personal' ? me.id : null, revision: 3, created_at: '', updated_at: '', values: { primary: { light: '#0e6f6c', dark: null }, secondary: { light: '#d69b31', dark: '#e2b45a' }, recurring_marker: { source: 'secondary', custom: null }, agents: { avatar: 'robot-5', ring: 'still', hover: true, size: 90, palette: 'deutan' } } })
async function setup(page: Page, options: { admin?: boolean; fail?: number; defaultLater?: boolean } = {}) {
  await mockWork(page, fixtures(), { admin: options.admin })
  const data = { items: [record('default', 'Porcelain', 'default'), record('contrast', 'High contrast', 'workspace'), record('copper', 'Copper', 'personal')], selected: 'copper', revision: 17, writes: [] as { method: string; path: string; body: Record<string, unknown> | null }[], fail: options.fail ?? 0, copies: 0 }
  const active = (): ActiveTheme => ({ theme: data.items.find(item => item.id === data.selected)!, default_theme_id: 'default', selected_theme_id: data.selected === 'default' ? null : data.selected, revision: data.revision, fallback_notice: null })
  await page.route('**/api/**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    if (!(path.startsWith('/api/themes') || path === '/api/me/theme')) return route.fallback()
    const body = request.postData() ? request.postDataJSON() : null
    if (method !== 'GET') {
      data.writes.push({ method, path, body })
      if (data.fail) return route.fulfill({ status: data.fail, json: { error: 'Failed write' } })
    }
    if (path === '/api/me/theme') {
      if (method === 'PUT') { expect(body.revision).toBe(data.revision); data.selected = body.theme_id ?? 'default'; data.revision++ }
      return route.fulfill({ json: clone(active()) })
    }
    if (path === '/api/themes') return route.fulfill({ json: { items: clone(options.defaultLater && !url.searchParams.has('after') ? data.items.filter(item => item.id !== 'default') : data.items), next_cursor: options.defaultLater && !url.searchParams.has('after') ? 'copper' : null } })
    const id = path.split('/')[3]!, theme = data.items.find(item => item.id === id)
    if (!theme) return route.fulfill({ status: 404, json: { error: 'Not found' } })
    if (method === 'POST') {
      expect(body.revision).toBe(theme.revision)
      const copy = { ...clone(theme), id: `copy-${++data.copies}`, name: body.name, scope: 'personal' as const, owner_principal_id: me.id, revision: 1 }
      data.items.push(copy); return route.fulfill({ status: 201, json: clone(copy) })
    }
    if (method === 'PATCH') {
      expect(body.revision).toBe(theme.revision); Object.assign(theme, body, { revision: theme.revision + 1 })
    }
    if (method === 'DELETE') {
      expect(Number(url.searchParams.get('revision'))).toBe(theme.revision)
      data.items = data.items.filter(item => item.id !== id)
      if (data.selected === id) data.selected = 'default'
      return route.fulfill({ status: 204 })
    }
    return route.fulfill({ json: clone(theme) })
  })
  return data
}
const card = (page: Page) => page.locator('#colours')
const bar = (page: Page) => page.getByRole('region', { name: 'Unsaved theme changes' })
async function colour(page: Page, label: string, hex: string, screenshot?: string) {
  await page.getByRole('button', { name: label, exact: true }).click()
  if (screenshot) await page.screenshot({ path: screenshot })
  await page.getByLabel('Hex colour', { exact: true }).fill(hex)
  await page.getByRole('button', { name: /^Done/ }).click()
}
for (const width of [390, 1024, 1440]) for (const mode of ['light', 'dark'] as const) {
  test(`Theme settings ${width} ${mode}: preview, Suggest, marker and overlay stay still`, async ({ page }) => {
    await page.setViewportSize({ width, height: 950 })
    const errors = watchErrors(page)
    await setup(page, { admin: true }); await page.goto('/settings/theme')
    await expect(card(page)).toBeVisible()
    await page.evaluate(mode => { document.documentElement.dataset.theme = mode }, mode)
    await mkdir('test-results/aeon-642', { recursive: true })
    const primary = page.getByRole('button', { name: 'Primary accent, light', exact: true })
    await primary.scrollIntoViewIfNeeded()
    await expectStableControls({ controls: { primary, derivedPrimary: page.getByRole('button', { name: 'Use derived primary dark' }), derivedSecondary: page.getByRole('button', { name: 'Use derived secondary dark' }), secondary: page.getByRole('button', { name: 'Secondary accent, light', exact: true }), markerGroup: page.getByRole('radiogroup', { name: 'Recurring marker colour' }), markerPrimary: page.getByRole('radio', { name: 'Primary', exact: true }), markerSecondary: page.getByRole('radio', { name: 'Secondary', exact: true }), markerNeutral: page.getByRole('radio', { name: 'Neutral grey', exact: true }), markerCustom: page.getByRole('radio', { name: 'Custom', exact: true }) }, scrollAreas: { page: page.locator('.settings-page') }, interactions: [
      { name: 'edit opens overlay without moving controls', run: async () => { await colour(page, 'Primary accent, light', '#ffffff', `test-results/aeon-642/picker-${width}-${mode}.png`); await expect(bar(page)).toBeVisible() } },
      { name: 'set primary dark by hand', run: async () => { await colour(page, 'Primary accent, dark', '#123456'); await expect(page.getByRole('button', { name: 'Use derived primary dark' })).toBeEnabled() } },
      { name: 'reset primary dark to derived', run: async () => { await page.getByRole('button', { name: 'Use derived primary dark' }).click(); await expect(page.getByRole('button', { name: 'Use derived primary dark' })).toBeDisabled() } },
      { name: 'reset secondary dark to derived', run: async () => { await page.getByRole('button', { name: 'Use derived secondary dark' }).click(); await expect(page.getByRole('button', { name: 'Use derived secondary dark' })).toBeDisabled() } },
      { name: 'set secondary dark by hand', run: async () => { await colour(page, 'Secondary accent, dark', '#123456'); await expect(page.getByRole('button', { name: 'Use derived secondary dark' })).toBeEnabled() } },
      { name: 'Suggest only failing light', run: async () => { await page.getByRole('button', { name: 'Suggest readable primary light' }).click(); await expect(page.getByTestId('primary-light-contrast')).not.toContainText('below') } },
      { name: 'marker Primary', run: async () => { await page.getByRole('radio', { name: 'Primary', exact: true }).click() } },
      { name: 'marker Secondary', run: async () => { await page.getByRole('radio', { name: 'Secondary', exact: true }).click() } },
      { name: 'marker Neutral grey', run: async () => { await page.getByRole('radio', { name: 'Neutral grey', exact: true }).click() } },
      { name: 'marker Custom opens picker', run: async () => { await page.getByRole('radio', { name: 'Custom', exact: true }).click(); await page.getByLabel('Hex colour', { exact: true }).fill('#bf3d6d'); await page.getByRole('button', { name: /^Done/ }).click() } },
      { name: 'discard restores values without shifting content', run: async () => { await bar(page).getByRole('button', { name: 'Discard', exact: true }).click(); await expect(bar(page)).toHaveCount(0) } },
    ] })
    // Long names cannot widen the list or push action controls.
    await page.getByRole('button', { name: 'Rename Copper', exact: true }).click()
    await page.getByRole('textbox', { name: 'Theme name' }).fill('Kurz')
    await expect(bar(page)).toBeVisible()
    await expectStableControls({ controls: { primary, rename: page.getByRole('button', { name: 'Rename Copper', exact: true }), name: page.getByRole('textbox', { name: 'Theme name' }), save: bar(page).getByRole('button', { name: /^Save/ }), discard: bar(page).getByRole('button', { name: 'Discard', exact: true }) }, scrollAreas: { page: page.locator('.settings-page') }, interactions: [
      { name: 'long German name keeps Colours and save actions still', run: async () => { await page.getByRole('textbox', { name: 'Theme name' }).fill('Arbeitsbereich für wiederkehrende Aufgaben und persönliche Farbgestaltung') } },
    ] })
    await page.getByRole('textbox', { name: 'Theme name' }).press('Escape')
    await expect(bar(page)).toBeVisible()
    await expect(page.locator('html')).toHaveAttribute('data-theme', mode)
    await mkdir('test-results/aeon-642', { recursive: true })
    await page.screenshot({ path: `test-results/aeon-642/theme-${width}-${mode}.png`, fullPage: true })
    await card(page).scrollIntoViewIfNeeded()
    await page.screenshot({ path: `test-results/aeon-642/colours-${width}-${mode}.png` })
    await page.getByRole('region', { name: 'dark colour preview' }).scrollIntoViewIfNeeded()
    await page.screenshot({ path: `test-results/aeon-642/preview-${width}-${mode}.png` })
    expect(errors).toEqual([])
  })
}
test('save preserves Agents, manual dark and derived reset; invalid hex never writes', async ({ page }) => {
  const data = await setup(page); await page.goto('/settings/theme')
  await colour(page, 'Primary accent, dark', '#123456')
  await expect(card(page)).toContainText('Dark set by hand')
  await page.getByRole('button', { name: 'Use derived primary dark' }).click()
  await expect(card(page)).toContainText('Dark derived from light')
  await colour(page, 'Primary accent, light', '#3a5fc4')
  await page.getByRole('button', { name: 'Primary accent, light', exact: true }).click()
  await page.getByLabel('Hex colour', { exact: true }).fill('#bad')
  await expect(page.getByLabel('Hex colour', { exact: true })).toHaveAttribute('aria-invalid', 'true')
  await page.getByRole('button', { name: /^Done/ }).click()
  await bar(page).getByRole('button', { name: /^Save/ }).click()
  await expect(bar(page)).toHaveCount(0); await expect(page.locator('.theme-status')).toContainText('Saved.')
  expect(data.items[2]!.values.primary).toEqual({ light: '#3a5fc4', dark: null })
  expect(data.items[2]!.values.agents).toEqual(record('copper', 'Copper', 'personal').values.agents)
  expect(data.writes.filter(write => write.method === 'PATCH')).toHaveLength(1)
})
test('Suggest touches only a failing mode and warnings never block Save', async ({ page }) => {
  const data = await setup(page); await page.goto('/settings/theme')
  await page.getByRole('button', { name: 'Suggest readable secondary light', exact: true }).click()
  await bar(page).getByRole('button', { name: /^Save/ }).click(); await expect(bar(page)).toHaveCount(0)
  expect(data.items[2]!.values.secondary.dark).toBe('#e2b45a')
  await colour(page, 'Primary accent, light', '#ffffff')
  await expect(page.getByTestId('primary-light-contrast')).toContainText('below 4.5:1')
  await expect(bar(page).getByRole('button', { name: /^Save/ })).toBeEnabled()
  await bar(page).getByRole('button', { name: /^Save/ }).click(); await expect(bar(page)).toHaveCount(0)
  expect(data.items[2]!.values.primary.light).toBe('#ffffff')
})
test('workspace themes are read-only to members; duplicate and delete use captured revisions', async ({ page }) => {
  const data = await setup(page); await page.goto('/settings/theme')
  await page.getByRole('button', { name: 'Use Porcelain', exact: true }).click()
  await expect(card(page)).toContainText('Read-only workspace theme')
  await expect(card(page)).not.toContainText('only you see it')
  await expect(page.getByRole('button', { name: 'Primary accent, light', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Rename Porcelain' })).toHaveCount(0)
  await expect(page.locator('[data-theme-id="default"] .scope')).toHaveText('Workspace default · read-only')
  await page.getByRole('button', { name: 'Use High contrast', exact: true }).click()
  await expect(card(page)).toContainText('Read-only workspace theme')
  await expect(page.getByRole('button', { name: 'Primary accent, light', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Delete Porcelain' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Rename High contrast' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Duplicate High contrast', exact: true }).click()
  await expect(card(page)).toContainText('Your theme')
  await page.getByRole('button', { name: 'Delete High contrast copy', exact: true }).click()
  await expect(page.getByRole('group', { name: 'Delete theme confirmation' })).toContainText('High contrast copy')
  await page.getByRole('button', { name: 'Keep theme', exact: true }).click()
  expect(data.items).toHaveLength(4)
  await page.getByRole('button', { name: 'Delete High contrast copy', exact: true }).click()
  await page.getByRole('button', { name: 'Delete theme', exact: true }).click()
  await expect(page.locator('.theme-status')).toContainText('Deleted.')
  await expect(card(page).locator('.permission-note')).toContainText('Porcelain'); expect(data.items).toHaveLength(3)
})
test('selection after a deletion conflict restores editable Colours and Save', async ({ page }) => {
  const data = await setup(page, { fail: 409 }); await page.goto('/settings/theme')
  await page.getByRole('button', { name: 'Delete Copper', exact: true }).click()
  await page.getByRole('button', { name: 'Delete theme', exact: true }).click()
  await expect(page.locator('.theme-status')).toContainText('changed elsewhere')
  await expect(page.getByRole('button', { name: 'Primary accent, light', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: 'Keep theme', exact: true }).click()
  data.fail = 0
  await page.getByRole('button', { name: 'Use High contrast', exact: true }).click()
  await page.getByRole('button', { name: 'Use Copper', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Primary accent, light', exact: true })).toBeEnabled()
  await expect(page.locator('.theme-status')).not.toContainText('changed elsewhere')
  await colour(page, 'Primary accent, light', '#3a5fc4')
  await bar(page).getByRole('button', { name: /^Save/ }).click()
  await expect(bar(page)).toHaveCount(0)
  await expect(page.locator('.theme-status')).toContainText('Saved.')
  expect(data.items.find(item => item.id === 'copper')!.values.primary.light).toBe('#3a5fc4')
})
test('New theme duplicates the default even on a later list page; managers may edit it', async ({ page }) => {
  const data = await setup(page, { admin: true, defaultLater: true }); await page.goto('/settings/theme')
  await expect(page.getByRole('button', { name: 'Load more themes' })).toBeVisible()
  await page.getByRole('button', { name: 'New theme', exact: true }).click()
  await expect(card(page)).toContainText('Porcelain copy')
  expect(data.writes.find(write => write.method === 'POST')!.path).toBe('/api/themes/default/duplicate')
  await page.getByRole('button', { name: 'Use Porcelain', exact: true }).click()
  await expect(card(page)).toContainText('you manage it')
  await expect(card(page)).not.toContainText('only you see it')
  await expect(page.locator('[data-theme-id="default"] .scope')).toHaveText('Workspace default · you manage it')
  await expect(page.getByRole('button', { name: 'Primary accent, light', exact: true })).toBeEnabled()
})
for (const failure of [500, 409]) test(`failed Save (${failure}) retains edits and does not report success`, async ({ page }) => {
  const data = await setup(page, { fail: failure }); await page.goto('/settings/theme')
  await colour(page, 'Primary accent, light', '#3a5fc4')
  await bar(page).getByRole('button', { name: /^Save/ }).click()
  await expect(page.locator('.theme-status')).toContainText(failure === 409 ? 'changed elsewhere' : 'failed')
  await expect(bar(page)).toBeVisible(); expect(data.items[2]!.values.primary.light).toBe('#0e6f6c')
  if (failure === 409) await expect(bar(page).getByRole('button', { name: /^Save/ })).toBeDisabled()
  await bar(page).getByRole('button', { name: 'Discard', exact: true }).click(); await expect(bar(page)).toHaveCount(0)
})
test('phone picker has a pinned action bar, stable controls and two-step Escape', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 }); await setup(page); await page.goto('/settings/theme')
  const trigger = page.getByRole('button', { name: 'Primary accent, light', exact: true }); await trigger.click()
  const dialog = page.getByRole('dialog', { name: 'Primary accent · light' })
  await expectStableControls({ controls: { frame: dialog, done: dialog.getByRole('button', { name: /^Done/ }), presets: dialog.locator('.presets'), clickedPreset: dialog.getByRole('button', { name: 'Use #3a5fc4' }), hex: dialog.getByLabel('Hex colour', { exact: true }) }, scrollAreas: { body: dialog.locator('.picker-body') }, interactions: [
    { name: 'preset', run: async () => { await dialog.getByRole('button', { name: 'Use #3a5fc4' }).click() } },
    { name: 'invalid hex feedback', run: async () => { await dialog.getByLabel('Hex colour', { exact: true }).fill('bad'); await expect(dialog.getByLabel('Hex colour', { exact: true })).toHaveAttribute('aria-invalid', 'true') } },
  ] })
  await page.keyboard.press('Escape'); await expect(dialog).toBeVisible()
  await page.keyboard.press('Escape'); await expect(dialog).toHaveCount(0); await expect(trigger).toBeFocused()
})
test('platform submit shortcut closes the picker and saves an in-place name', async ({ page }) => {
  const data = await setup(page); await page.goto('/settings/theme')
  const mod = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? 'Meta' : 'Control')
  await page.getByRole('button', { name: 'Primary accent, light', exact: true }).click()
  const hex = page.getByLabel('Hex colour', { exact: true }); await hex.fill('#3a5fc4')
  await hex.press(`${mod}+Enter`); await expect(page.getByRole('dialog')).toHaveCount(0)
  await page.getByRole('button', { name: 'Rename Copper', exact: true }).click()
  const name = page.getByRole('textbox', { name: 'Theme name' }); await name.fill('Blue copper')
  await name.press(`${mod}+a`); await name.press(`${mod}+Enter`)
  await expect(bar(page)).toHaveCount(0); expect(data.items[2]!.name).toBe('Blue copper')
})
