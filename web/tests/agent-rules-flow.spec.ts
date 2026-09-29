// SPDX-License-Identifier: AGPL-3.0-only
// The simplified agent rules flow at realistic scale (AEON-263): one next action,
// import that waits for permissions instead of denying, and one approval to publish.
import { expect, test, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { AEON_PROJECT, SCALE_COUNTS, mockRulesScale, scaleImportFile, type ScaleOptions } from './rules-scale-fixtures'

async function open(page: Page, options: ScaleOptions = {}) {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData())
  const mock = await mockRulesScale(page, options)
  await page.goto('/settings/agent-rules')
  await expect(page.getByRole('heading', { name: 'Agent rules' })).toBeVisible()
  await expect(page.getByRole('status', { name: 'Loading rules' })).toHaveCount(0)
  return mock
}
const writes = (calls: { method: string; path: string }[]) => calls.filter(call => call.method !== 'GET' && call.path.startsWith('/api/rules'))
const noHorizontalScroll = (page: Page) => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)

test('the fixture is realistic: 14 sets, 51 rules, 30 locked', () => {
  expect(SCALE_COUNTS).toEqual({ sets: 14, rules: 51, locked: 30 })
})

test('an empty workspace shows one line and one action', async ({ page }) => {
  const errors = watchErrors(page)
  await open(page, { state: 'empty' })
  await expect(page.getByText('No rules yet.')).toBeVisible()
  const main = page.getByRole('region', { name: 'Agent rules' })
  await expect(main.locator('.btn.primary')).toHaveCount(1)
  await expect(main.getByRole('button', { name: 'Import rules' })).toBeVisible()
  await expect(main.getByRole('button', { name: /Review and publish/ })).toHaveCount(0)
  await expect(main.getByRole('combobox')).toHaveCount(0)
  expect(errors).toEqual([])
})

test('import waits for every scope permission, shows checking, then reviews without a false denial', async ({ page }) => {
  const errors = watchErrors(page)
  const mock = await open(page, { state: 'empty', holdProjectPermissions: true })
  await page.getByRole('button', { name: 'Import rules' }).click()
  const dialog = page.getByRole('dialog', { name: 'Import rules' })
  await dialog.locator('#draft-import-file').setInputFiles(scaleImportFile())
  await expect(dialog.getByText('Checking permissions…')).toBeVisible()
  await expect(dialog.getByText('inspr-rules-2026-09-28.json')).toBeVisible()
  await expect(dialog.getByRole('alert')).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: 'Import as drafts' })).toBeDisabled()
  expect(mock.calls.some(call => call.path === `/api/me/permissions?project_id=${AEON_PROJECT}`)).toBe(true)
  mock.releasePermissions()
  await expect(dialog.getByText('Checking permissions…')).toHaveCount(0)
  await expect(dialog.getByRole('alert')).toHaveCount(0)
  await expect(dialog.getByText('14 sets · 51 rules')).toBeVisible()
  await expect(dialog.getByRole('region', { name: 'Project · Aeon' })).toBeVisible()
  // The chosen file stays named: no native "no file chosen" beside a selected file.
  await expect(dialog.getByText(/No file chosen|Keine ausgewählt/)).toHaveCount(0)
  await expect(dialog.getByText(AEON_PROJECT)).toHaveCount(0)
  expect(writes(mock.calls)).toHaveLength(0)
  const confirm = dialog.getByRole('button', { name: 'Import 14 sets as drafts' })
  await expect(confirm).toBeInViewport()
  const box = await confirm.boundingBox()
  expect(box?.height ?? 0).toBeGreaterThanOrEqual(28)
  await confirm.click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByText('Imported 14 sets as drafts.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Review and publish (14 sets)' })).toBeVisible()
  await expect(page.getByText('51 rules in 14 sets · 0 live · 14 waiting to publish')).toBeVisible()
  // The page follows the project the file targets.
  await expect(page.getByRole('combobox', { name: 'Project' })).toHaveValue(AEON_PROJECT)
  expect(mock.calls.some(call => call.path.includes('/publish'))).toBe(false)
  expect(errors).toEqual([])
})

test('the page opens on the project the rules target, not the first project listed', async ({ page }) => {
  await open(page, { state: 'drafts' })
  await expect(page.getByRole('combobox', { name: 'Project' })).toHaveValue(AEON_PROJECT)
  await expect(page.getByRole('heading', { name: 'Package scope' })).toBeVisible()
})

test('review and publish is one confirmation for every waiting set', async ({ page }) => {
  const errors = watchErrors(page)
  const mock = await open(page, { state: 'drafts' })
  await expect(page.getByText('51 rules in 14 sets · 2 live · 12 waiting to publish')).toBeVisible()
  await page.getByRole('button', { name: 'Review and publish (12 sets)' }).click()
  const dialog = page.getByRole('dialog', { name: 'Review and publish' })
  await expect(dialog.getByRole('list', { name: 'Sets to publish' }).locator('> li')).toHaveCount(12)
  await expect(dialog.getByText('1 added')).toBeVisible()
  await expect(dialog.getByText(/of 12,000 bytes/)).toBeVisible()
  // The note sits above the footer, fully in view without scrolling the list.
  await expect(dialog.getByRole('textbox', { name: /Note/ })).toBeInViewport({ ratio: 1 })
  await dialog.getByRole('textbox', { name: /Note/ }).fill('Adopt INSPR doctrine 0.14 for Aeon.')
  await dialog.getByRole('button', { name: 'Publish 12 sets' }).click()
  await expect(dialog).toHaveCount(0)
  await expect(page.getByText(/Published 12 sets/)).toBeVisible()
  const posts = writes(mock.calls)
  expect(posts).toHaveLength(1)
  expect(posts[0]!.path).toBe('/api/rules/publish')
  const body = posts[0]!.body as { items: { set_id: string; expected_revision: number; version: string }[]; note: string }
  expect(body.items).toHaveLength(12)
  expect(body.items.every(item => item.version === 'auto' && item.expected_revision === 2)).toBe(true)
  expect(body.note).toBe('Adopt INSPR doctrine 0.14 for Aeon.')
  await expect(page.getByText('51 rules in 14 sets · all live')).toBeVisible()
  await expect(page.getByRole('button', { name: /Review and publish/ })).toHaveCount(0)
  expect(errors).toEqual([])
})

test('a refused batch says truthfully that nothing was published', async ({ page }) => {
  await open(page, { state: 'drafts', batchFailure: { status: 422, code: 'rules_budget_exceeded', error: 'too big', actual_bytes: 12345, max_bytes: 12000 } })
  await page.getByRole('button', { name: 'Review and publish (12 sets)' }).click()
  const dialog = page.getByRole('dialog', { name: 'Review and publish' })
  await dialog.getByRole('button', { name: 'Publish 12 sets' }).click()
  await expect(dialog.getByRole('alert')).toContainText('Nothing was published.')
  await expect(dialog.getByRole('alert')).toContainText('12,345 bytes')
  await expect(page.getByText('12 waiting to publish')).toBeVisible()
})

test('one set can still be published from its menu, through the same approval', async ({ page }) => {
  const mock = await open(page, { state: 'drafts' })
  await page.getByRole('button', { name: 'Actions for Fleet' }).click()
  await page.getByRole('menuitem', { name: 'Publish this set' }).click()
  const dialog = page.getByRole('dialog', { name: 'Publish “Fleet”' })
  await dialog.getByRole('button', { name: 'Publish 1 set' }).click()
  await expect(dialog).toHaveCount(0)
  const body = writes(mock.calls)[0]?.body as { items: { set_id: string }[] }
  expect(body.items).toHaveLength(1)
  await expect(page.getByText('3 live · 11 waiting to publish')).toBeVisible()
})

test('an agent reads the rules but never sees a publish action', async ({ page }) => {
  await open(page, { state: 'drafts', kind: 'agent' })
  await expect(page.getByRole('heading', { name: 'Secrets' })).toBeVisible()
  await expect(page.getByRole('button', { name: /Review and publish/ })).toHaveCount(0)
  await expect(page.getByText('Agents read rules; a person publishes them.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Import' })).toHaveAttribute('aria-disabled', 'true')
  await page.getByRole('button', { name: 'Actions for Secrets' }).click()
  await expect(page.getByRole('menuitem', { name: /Edit rules/ })).toHaveAttribute('aria-disabled', 'true')
  await expect(page.getByRole('menuitem', { name: /Publish this set/ })).toHaveAttribute('aria-disabled', 'true')
})

test('a member without publish cannot publish or edit company rules', async ({ page }) => {
  await open(page, { state: 'drafts', publish: false })
  await expect(page.getByRole('button', { name: /Review and publish/ })).toHaveCount(0)
  await expect(page.getByText(/needs the workspace permission to publish/)).toBeVisible()
  await page.getByRole('button', { name: 'Actions for Git' }).click()
  const edit = page.getByRole('menuitem', { name: /Edit rules/ })
  await expect(edit).toHaveAttribute('aria-disabled', 'true')
  await expect(edit).toContainText('Editing company rules needs the workspace permission to publish rules.')
})

test('phone width has no horizontal scroll, with rules, the editor and the dialogs open', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open(page, { state: 'drafts' })
  expect(await noHorizontalScroll(page)).toBe(true)
  await page.getByRole('button', { name: /^Secrets/ }).click()
  await page.getByRole('button', { name: /Show details for Never run a command/ }).click()
  expect(await noHorizontalScroll(page)).toBe(true)
  await page.getByRole('button', { name: 'Actions for Git' }).click()
  await page.getByRole('menuitem', { name: 'Edit rules' }).click()
  await expect(page.getByRole('textbox', { name: 'Set name' })).toBeVisible()
  expect(await noHorizontalScroll(page)).toBe(true)
  await page.getByRole('button', { name: 'Cancel' }).click()
  await page.getByRole('button', { name: 'Review and publish (12 sets)' }).click()
  const dialog = page.getByRole('dialog', { name: 'Review and publish' })
  await expect(dialog.getByRole('button', { name: 'Publish 12 sets' })).toBeInViewport()
  await expect(dialog.getByRole('textbox', { name: /Note/ })).toBeInViewport({ ratio: 1 })
  expect(await noHorizontalScroll(page)).toBe(true)
  const width = await dialog.evaluate(el => el.getBoundingClientRect().width)
  expect(width).toBeLessThanOrEqual(390)
})

test('axe: agent rules with sets open and the review dialog', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await open(page, { state: 'drafts' })
  await page.getByRole('button', { name: /^Secrets/ }).click()
  const summary = async () => {
    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
    return results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 6).map(n => `  ${n.target.join(' ')} — ${n.html.slice(0, 180)}`).join('\n')}`)
  }
  let found = await summary()
  expect(found, found.join('\n')).toEqual([])
  await page.getByRole('button', { name: 'Review and publish (12 sets)' }).click()
  await expect(page.getByRole('dialog', { name: 'Review and publish' })).toBeVisible()
  found = await summary()
  expect(found, found.join('\n')).toEqual([])
})
