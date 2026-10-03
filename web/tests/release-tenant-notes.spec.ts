// SPDX-License-Identifier: AGPL-3.0-only
// AEON-372: product notes survive a tenant without an AEON project/snapshots.
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync } from 'node:fs'
import { dirname, resolve, join } from 'node:path'
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases } from './releases-fixtures'
import type { ReleaseHistory } from '../src/lib/releases'

let history: ReleaseHistory
test.beforeAll(({}, info) => {
  const output = info.outputPath('history.json')
  mkdirSync(dirname(output), { recursive: true })
  const args = ['test', '-p', '2', './internal/releasehistory', '-run', '^TestProductNotesBrowserFixture$', '-count=1']
  const options = { cwd: resolve('..'), env: { ...process.env, GOMAXPROCS: '2', AEON_RELEASE_FIXTURE_OUT: output }, timeout: 120_000 }
  try { execFileSync('go', args, options) }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error
    execFileSync('nix', ['develop', '-c', 'go', ...args], options)
  }
  history = JSON.parse(readFileSync(output, 'utf8'))
})

const current = '260923143005.0.0'
const paths = { header: '/', direct: '/releases', all: '/?releases=all', version: `/?releases=${current}` }
for (const tenant of ['pma', 'ppm']) for (const width of [1600, 390]) for (const [entry, path] of Object.entries(paths)) {
  test(`${tenant} ${width} ${entry}: frozen notes and both switches survive reload`, async ({ page }, info) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: 'light' })
    const data = fixtures()
    if (tenant === 'pma') {
      data.projects = data.projects.filter(p => p.id !== 'p-aeon')
      data.nodes = data.nodes.filter(n => n.project !== 'p-aeon')
    }
    await mockWork(page, data)
    const response = structuredClone(history)
    // PPM-shaped responses have a tenant capture. The real database overlay's
    // precedence/isolation is exercised in TestProductNotesFallbackIsTenantScopedAndFrozen.
    if (tenant === 'ppm') for (const release of response.releases) {
      if (release.notes?.public_items) {
        release.notes.items = release.notes.public_items.map(item => ({ ...item, id: '44444444-4444-4444-8444-444444444444' }))
        release.notes.source = 'database-snapshot'
        delete release.notes.public_items
      }
    }
    await mockReleases(page, response as unknown as Record<string, unknown>)
    await page.goto(path)
    if (entry === 'header') {
      await page.getByRole('button', { name: 'App and workspace', exact: true }).click()
      await page.getByRole('menuitem', { name: /^Release history/ }).click()
    }
    const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
    const detail = sheet.locator('article.detail')
    const radio = (name: string) => sheet.getByRole('radio', { name, exact: true })
    // The header and all-history entry intentionally open an unselected list
    // at every width; direct pages preselect on desktop only.
    if (entry === 'header' || entry === 'all' || width === 390 && entry !== 'version') await sheet.getByRole('row').first().click()
    await expect(detail.getByRole('article', { name: 'Clear release notes', exact: true })).toBeVisible()
    await expect(detail.getByRole('region', { name: /^Fixes/ })).toContainText('Reliable release switches')
    await expect(detail).toContainText('Read what changed in every workspace.')
    await radio('DE').click()
    await expect(radio('DE')).toHaveAttribute('aria-checked', 'true')
    await expect(page).toHaveURL(/release_lang=de/)
    await expect(detail).toContainText('Lies die Änderungen in jedem Arbeitsbereich.')
    await radio('Details').click()
    await expect(radio('Details')).toHaveAttribute('aria-checked', 'true')
    await expect(page).toHaveURL(/release_view=details/)
    await expect(detail.locator('.benefit')).toHaveCount(0)
    await expect(detail.getByRole('link', { name: /^Commit .* on GitHub$/ }).first()).toBeVisible()
    await page.reload()
    await expect(radio('DE')).toHaveAttribute('aria-checked', 'true')
    await expect(radio('Details')).toHaveAttribute('aria-checked', 'true')
    await expect(detail).toContainText('Klare Release Notizen')
    await radio('DE').focus()
    await page.keyboard.press('ArrowLeft')
    await expect(radio('EN')).toHaveAttribute('aria-checked', 'true')
    await expect(radio('EN')).toBeFocused()
    await expect(detail).toContainText('Clear release notes')
    await radio('Details').focus()
    await page.keyboard.press('ArrowLeft')
    await expect(radio('Highlights')).toHaveAttribute('aria-checked', 'true')
    await expect(radio('Highlights')).toBeFocused()
    await expect(detail).toContainText('Read what changed in every workspace.')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: info.outputPath('highlights.png') })
    // A historical capture with no German stays useful and labels its fallback.
    await page.goto('/releases/260923134631.0.0?release_lang=de')
    await expect(detail.getByRole('article', { name: /^Earlier release notes/ })).toBeVisible()
    await expect(detail.locator('.lang-badge').first()).toHaveText('EN')
  })
}

for (const width of [1600, 390]) for (const colorScheme of ['light', 'dark'] as const) {
  test(`portable notes visual ${width} ${colorScheme}`, async ({ page }) => {
    const shots = process.env.AEON_372_SHOTS
    test.skip(!shots, 'set AEON_372_SHOTS for visual inspection')
    mkdirSync(shots!, { recursive: true })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    const data = fixtures()
    data.projects = data.projects.filter(p => p.id !== 'p-aeon')
    data.nodes = data.nodes.filter(n => n.project !== 'p-aeon')
    await mockWork(page, data)
    await mockReleases(page, history as unknown as Record<string, unknown>)
    await page.goto(`/?releases=${current}&release_lang=en`)
    const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
    await expect(sheet.getByRole('article', { name: 'Clear release notes', exact: true })).toBeVisible()
    await page.screenshot({ path: join(shots!, `highlights-${width}-${colorScheme}.png`) })
    await sheet.getByRole('radio', { name: 'DE', exact: true }).click()
    await sheet.getByRole('radio', { name: 'Details', exact: true }).click()
    await expect(sheet.locator('article.detail .benefit')).toHaveCount(0)
    await page.screenshot({ path: join(shots!, `details-${width}-${colorScheme}.png`) })
  })
}
