// SPDX-License-Identifier: AGPL-3.0-only
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases } from './releases-fixtures'

let history: Record<string, unknown>
test.beforeAll(({}, info) => {
  const output = info.outputPath('own-history.json')
  mkdirSync(dirname(output), { recursive: true })
  const args = ['test', '-p', '2', './internal/releasehistory', '-run', '^TestReservedNotesNonPPMTenant$', '-count=1']
  const options = { cwd: resolve('..'), env: { ...process.env, GOMAXPROCS: '2', AEON_RESERVED_FIXTURE_OUT: output }, timeout: 120_000 }
  try { execFileSync('go', args, options) }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
    execFileSync('nix', ['develop', '-c', 'go', ...args], options)
  }
  history = JSON.parse(readFileSync(output, 'utf8'))
})

for (const width of [1600, 390]) for (const theme of ['light', 'dark'] as const) test(`own release notes reach another tenant at ${width} ${theme}`, async ({ page }) => {
  await page.emulateMedia({ colorScheme: theme })
  await page.setViewportSize({ width, height: 1000 })
  const data = fixtures()
  data.projects = data.projects.filter(p => p.id !== 'p-aeon')
  data.nodes = data.nodes.filter(n => n.project !== 'p-aeon')
  await mockWork(page, data)
  await mockReleases(page, history)
  await page.goto('/releases/260930160000.0.0')
  const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
  const detail = sheet.locator('article.detail')
  await expect(detail.getByRole('region', { name: 'Features, 1' })).toContainText('Own release feature')
  await expect(detail.getByRole('region', { name: 'Fixes, 1' })).toContainText('Own release fix')
  await expect(detail).toContainText('This release carries its features.')
  await expect(detail).not.toContainText('Internal changes only.')
  await expect(detail).not.toContainText('HIDDEN')
  await expect(detail).not.toContainText('Notes written after release')
  await sheet.getByRole('radio', { name: 'DE', exact: true }).click()
  await expect(detail).toContainText('Dieses Release enthält seine Funktionen.')
  await expect(detail).toContainText('Dieses Release enthält seine Fehlerbehebungen.')
  await sheet.getByRole('radio', { name: 'Details', exact: true }).click()
  await expect(detail.locator('.benefit')).toHaveCount(0)
  await expect(detail.getByText('Note source: embedded-product-notes')).toBeVisible()
  await expect(detail.getByRole('list', { name: 'Geprüfte Korrekturen' })).toContainText('AEON-2: Reviewed repair wording.')
  await sheet.getByRole('radio', { name: 'Highlights', exact: true }).click()
  await sheet.getByRole('radio', { name: 'EN', exact: true }).click()
  await expect(detail).toContainText('This release carries its fixes.')
  if (width === 390) await sheet.getByRole('button', { name: 'All releases' }).click()
  const list = sheet.getByRole('listbox', { name: 'Releases, newest first' })
  await sheet.getByRole('button', { name: 'Features', exact: true }).click()
  await expect(list.locator('#release-260930160000-0-0')).toBeVisible()
  await sheet.getByRole('button', { name: 'Features', exact: true }).click()
  await sheet.getByRole('button', { name: 'Fixes', exact: true }).click()
  await expect(list.locator('#release-260930160000-0-0')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})
