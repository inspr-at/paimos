// SPDX-License-Identifier: AGPL-3.0-only
// AEON-679: retain import permission, preview and history assertions from the
// agent-rules capture matrix (AEON-263). Skipped
// unless RULES_SHOTS=<dir>; each state is captured at 1600 and 390, light and dark.
//   RULES_SHOTS=/tmp/shots npx playwright test -c playwright.ui.config.ts tests/agent-rules-shots.spec.ts --workers=1
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockRulesScale, scaleImportFile, type ScaleOptions } from './rules-scale-fixtures'

const OUT = process.env.RULES_SHOTS ?? ''
const ONLY = process.env.RULES_SHOTS_FILTER ?? ''
test.skip(!OUT, 'Screenshots run only with RULES_SHOTS=<dir>.')

interface Shot { name: string; options?: ScaleOptions; act?: (page: Page) => Promise<void> }
const shots: Shot[] = [
  { name: 'import-checking', options: { state: 'empty', holdProjectPermissions: true }, act: async page => {
    await page.getByRole('button', { name: 'Import rules' }).click()
    await page.locator('#draft-import-file').setInputFiles(scaleImportFile())
    await expect(page.getByText('Checking permissions…')).toBeVisible()
  } },
  { name: 'import-review', options: { state: 'empty' }, act: async page => {
    await page.getByRole('button', { name: 'Import rules' }).click()
    await page.locator('#draft-import-file').setInputFiles(scaleImportFile())
    await expect(page.getByRole('button', { name: /Import 14 sets as drafts/ })).toBeVisible()
    await page.getByText('Secrets', { exact: true }).click()
  } },
  { name: 'preview', options: { state: 'live', tldr: true }, act: async page => {
    await page.getByRole('button', { name: 'Preview' }).click()
    await expect(page.getByRole('table', { name: 'Explanation and exact rule' })).toBeVisible()
  } },
  { name: 'history', options: { state: 'drafts' }, act: async page => {
    await page.getByRole('button', { name: 'Actions for Secrets' }).click()
    await page.getByRole('menuitem', { name: 'Version history' }).click()
    await expect(page.getByText('Adopted INSPR doctrine 0.14.').first()).toBeVisible()
  } },
]

for (const shot of shots.filter(item => !ONLY || new RegExp(ONLY).test(item.name))) {
  for (const theme of ['light', 'dark'] as const) {
    for (const width of [1600, 390]) {
      test(`${shot.name} ${width} ${theme}`, async ({ page }) => {
        mkdirSync(OUT, { recursive: true })
        await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
        await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
        await mockWork(page, fixtures())
        await mockSettings(page, settingsData())
        await mockRulesScale(page, shot.options)
        await page.goto('/settings/agent-rules')
        await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
        await expect(page.getByRole('heading', { name: 'Agent rules' })).toBeVisible()
        await expect(page.getByRole('status', { name: 'Loading rules' })).toHaveCount(0)
        if (shot.act) await shot.act(page)
        await page.waitForTimeout(250)
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)
        expect(overflow, 'horizontal scroll').toBe(false)
        await page.screenshot({ path: `${OUT}/rules__${shot.name}__${width}__${theme}.png` })
      })
    }
  }
}
