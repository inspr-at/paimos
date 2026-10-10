// SPDX-License-Identifier: AGPL-3.0-only
// AEON-398: real historic export -> packnotes -> Build -> non-PPM HTTP fixture.
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases } from './releases-fixtures'

let history: Record<string, unknown>
test.beforeAll(({}, info) => {
  const output = info.outputPath('historic-history.json')
  mkdirSync(dirname(output), { recursive: true })
  const args = ['test', '-p', '2', './internal/releasehistory', '-run', '^TestHistoricNotesNonPPMTenant$', '-count=1']
  const options = { cwd: resolve('..'), env: { ...process.env, GOMAXPROCS: '2', AEON_HISTORIC_FIXTURE_OUT: output }, timeout: 120_000 }
  try { execFileSync('go', args, options) }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
    execFileSync('nix', ['develop', '-c', 'go', ...args], options)
  }
  history = JSON.parse(readFileSync(output, 'utf8'))
})

for (const width of [1600, 390]) test(`historic notes in a non-PPM tenant: filters and views at ${width}`, async ({ page }) => {
  await page.setViewportSize({ width, height: 1000 })
  const data = fixtures()
  // Exercise all five historic rows, including the two reservations hidden by default.
  data.preferences['developer-ui'] = { show_reserved_versions: true }
  data.projects = data.projects.filter(p => p.id !== 'p-aeon')
  data.nodes = data.nodes.filter(n => n.project !== 'p-aeon')
  await mockWork(page, data)
  await mockReleases(page, history)
  await page.goto('/releases/260923160000.0.0')
  const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
  const detail = sheet.locator('article.detail')
  await expect(detail.getByRole('region', { name: 'Features, 1' })).toContainText('Historic feature')
  await expect(detail.getByRole('region', { name: 'Fixes, 1' })).toContainText('Historic fix')
  await expect(detail.getByRole('region', { name: 'Other, 1' })).toContainText('Maintenance without benefit text')
  await expect(detail).toContainText('Existing benefit.')
  await expect(detail).toContainText('Existing fix benefit.')
  await expect(detail).toContainText('Notes written after release')
  await detail.getByRole('article', { name: 'Historic feature', exact: true }).locator('summary').click()
  await expect(detail.getByText('Historic feature implementation', { exact: true })).toBeVisible()
  await sheet.getByRole('radio', { name: 'Details', exact: true }).click()
  await expect(page).toHaveURL(/release_view=details/)
  await expect(detail.locator('.benefit')).toHaveCount(0)
  await expect(detail.getByRole('link', { name: /^Commit .* on GitHub$/ }).first()).toBeVisible()
  await page.reload()
  await expect(sheet.getByRole('radio', { name: 'Details', exact: true })).toHaveAttribute('aria-checked', 'true')
  await sheet.getByRole('radio', { name: 'Highlights', exact: true }).click()
  await expect(detail).toContainText('Existing benefit.')
  if (width === 390) await sheet.getByRole('button', { name: 'All releases' }).click()
  const list = sheet.getByRole('grid', { name: 'Releases, newest first' })
  await expect(list.getByRole('row')).toHaveCount(5)
  await sheet.getByRole('button', { name: 'Features', exact: true }).click()
  await expect(list.getByRole('row')).toHaveCount(2)
  const featureIDs = await list.getByRole('row').evaluateAll(rows => rows.map(row => row.id))
  expect(featureIDs).toEqual(['release-260923160000-0-0', 'release-260923134631-0-0'])
  await sheet.getByRole('button', { name: 'Features', exact: true }).click()
  await expect(list.getByRole('row')).toHaveCount(5)
  await sheet.getByRole('button', { name: 'Fixes', exact: true }).click()
  await expect(list.getByRole('row')).toHaveCount(2)
  const fixIDs = await list.getByRole('row').evaluateAll(rows => rows.map(row => row.id))
  expect(fixIDs).toEqual(['release-260923160000-0-0', 'release-260923143005-0-0'])
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})
