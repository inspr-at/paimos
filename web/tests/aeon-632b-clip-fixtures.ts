// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { expectStableControls } from './helpers/stable'

export const NAME = 'Verlässliche Zusammenarbeit für österreichische Entwicklungsprojekte mit vollständiger Dokumentation und Freigaben'
export const GROUP = 'Österreichische Projekte und gemeinsame Entwicklungsfreigabe'
export const SLUG = 'verlaessliche-zusammenarbeit-und-vollstaendige-dokumentation-mit-gemeinsamen-freigaben'
export const shots = 'test-results/aeon-632b-clip'

export async function setup(page: Page, theme: string) {
  await page.emulateMedia({ colorScheme: theme as 'light' | 'dark', reducedMotion: 'reduce' })
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.projects[0]!.title = NAME
  data.projects[1]!.title = NAME + ' · zweite Arbeitsgruppe'
  data.preferences['project-groups'] = { groups: [{ id: 'g:long', name: GROUP }, { id: 'g:short', name: 'Kurz' }], place: { 'p-aeon': 'g:long' } }
  await mockWork(page, data)
  await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
  await mockSettings(page, settingsData())
  return data
}

export async function capture(page: Page, view: string, width: number, theme: string) {
  for (const dismiss of await page.locator('.toast-close').all()) await dismiss.click()
  await page.mouse.move(0, 0)
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
  await page.evaluate(() => document.fonts.ready)
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth), `${view}: page overflow`).toBeLessThanOrEqual(1)
  expect(await page.locator('#main').evaluate(el => el.scrollWidth - el.clientWidth), `${view}: content overflow`).toBeLessThanOrEqual(1)
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: `${shots}/${view}-${width}-${theme}.png`, fullPage: true })
}

export async function disclosure(page: Page, name: Locator, controls: Record<string, Locator> = {}, options: { tab?: boolean; twoLines?: boolean } = {}) {
  await expect(name).toBeVisible()
  const full = await name.getAttribute('data-tip') ?? (await name.textContent())?.trim()
  expect(full).toBeTruthy()
  const clipped = await name.evaluate(el => el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1)
  if (page.viewportSize()!.width <= 720 && options.twoLines !== false) {
    const lines = await name.evaluate(el => { const style = getComputedStyle(el); return { height: el.clientHeight, line: parseFloat(style.lineHeight), padding: parseFloat(style.paddingTop) + parseFloat(style.paddingBottom), clamp: style.webkitLineClamp } })
    expect(lines.clamp).toBe('2')
    expect(lines.height).toBeLessThanOrEqual(lines.line * 2 + lines.padding + 1)
  }
  await name.scrollIntoViewIfNeeded()
  await expectStableControls({
    controls: { name, ...controls },
    interactions: [
      { name: 'keyboard focus reveals name', run: async () => {
        if (options.tab !== false) await page.keyboard.press('Tab')
        await name.evaluate(el => ((el.hasAttribute('tabindex') ? el : el.closest('a, button, summary, [role="option"]') ?? el) as HTMLElement).focus())
        if (clipped) await expect(page.locator('.tooltip')).toHaveText(full!)
      } },
      { name: 'pointer reveals name', run: async () => {
        await name.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
        await page.mouse.move(0, 0)
        await name.hover()
        if (clipped) await expect(page.locator('.tooltip')).toHaveText(full!)
      } },
    ],
    scrollAreas: { content: page.locator('#main') },
  })
  await name.evaluate(() => (document.activeElement as HTMLElement | null)?.blur())
  await page.mouse.move(0, 0)
}

