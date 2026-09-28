// SPDX-License-Identifier: AGPL-3.0-only
// Screenshots of the agent rules flow at realistic scale (AEON-263). Skipped
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

interface Shot { name: string; options?: ScaleOptions; act?: (page: Page) => Promise<void>; full?: boolean }
const shots: Shot[] = [
  { name: 'empty', options: { state: 'empty' } },
  { name: 'drafts', options: { state: 'drafts' }, full: true },
  { name: 'expanded', options: { state: 'drafts' }, full: true, act: async page => {
    await page.getByRole('button', { name: /^Secrets/ }).click()
    await page.getByRole('button', { name: /^Cross-repo authoring/ }).click()
    await page.getByRole('button', { name: /^Package scope/ }).click()
    await page.getByRole('button', { name: /Show details for Never run a command/ }).click()
  } },
  { name: 'editing', options: { state: 'drafts' }, act: async page => {
    await page.getByRole('button', { name: 'Actions for Git' }).click()
    await page.getByRole('menuitem', { name: 'Edit rules' }).click()
    await page.getByRole('article', { name: 'Git' }).scrollIntoViewIfNeeded().catch(() => {})
    await page.locator('.set.editing').scrollIntoViewIfNeeded()
  } },
  { name: 'import-choose', options: { state: 'empty' }, act: async page => {
    await page.getByRole('button', { name: 'Import rules' }).click()
  } },
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
  { name: 'publish', options: { state: 'drafts' }, act: async page => {
    await page.getByRole('button', { name: /Review and publish/ }).click()
    await page.getByRole('dialog').getByText('Cross-repo authoring').click()
  } },
  { name: 'preview', options: { state: 'live' }, act: async page => {
    await page.getByRole('button', { name: 'Preview' }).click()
    await page.getByRole('button', { name: 'Preview for…' }).click()
  } },
  { name: 'history', options: { state: 'drafts' }, act: async page => {
    await page.getByRole('button', { name: 'Actions for Secrets' }).click()
    await page.getByRole('menuitem', { name: 'Version history' }).click()
    await expect(page.getByText('Adopted INSPR doctrine 0.14.').first()).toBeVisible()
  } },
]

// The settings page scrolls inside its own container: grow the viewport to the
// content so a "full" shot holds the whole page.
async function growToContent(page: Page, base: number) {
  let height = base
  for (let pass = 0; pass < 4; pass++) {
    const hidden = await page.evaluate(() => {
      let most = 0
      for (const el of [document.documentElement, ...document.querySelectorAll('body *')]) {
        if (el !== document.documentElement && !/(auto|scroll)/.test(getComputedStyle(el).overflowY)) continue
        if (el.clientHeight === 0) continue
        el.scrollTop = 0
        most = Math.max(most, el.scrollHeight - el.clientHeight)
      }
      return most
    })
    if (hidden < 4 || height >= 6000) break
    height = Math.min(6000, height + hidden)
    await page.setViewportSize({ width: page.viewportSize()!.width, height })
    await page.waitForTimeout(200)
  }
}

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
        if (shot.full) await growToContent(page, width === 390 ? 844 : 1000)
        await page.screenshot({ path: `${OUT}/rules__${shot.name}__${width}__${theme}.png` })
      })
    }
  }
}
