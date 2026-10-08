// SPDX-License-Identifier: AGPL-3.0-only
// Screenshot matrix for the model board. Scheduling weight lives outside the
// twelve-shard prior gate; behavior cases stay in models-board-controls.spec.ts.
import { expect, test } from '@playwright/test'
import { boardPin } from './models-board-fixtures'
import { controlStability } from './control-stability'
import { card, headerControls, mockBoard, open } from './models-board-page'

for (const width of [390, 1024, 1280, 1440]) for (const theme of ['light', 'dark']) for (const lang of (width >= 1280 ? ['en', 'de'] : ['de'])) {
  test(`board and full-screen evidence ${width} ${theme} ${lang} text`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 1100 }); await page.emulateMedia({ colorScheme: theme as 'light' | 'dark' })
    await page.addInitScript(mode => { document.addEventListener('DOMContentLoaded', () => { document.documentElement.dataset.theme = mode }) }, theme)
    await mockBoard(page, { german: lang === 'de' }); await open(page, `?lang=${lang}`)
    const guard = await controlStability(page, { ...headerControls(page), pin: card(page, boardPin.line, 'frontend'), board: page.locator('[data-board-scroll]') })
    await guard.check(async () => { await card(page, 'anthropic:sonnet').press('Enter'); await page.keyboard.press('Escape') }); guard.done()
    await expect(page.locator('[data-line]').first()).toHaveCSS('height', '48px')
    await page.locator('[data-board-show]').focus(); await page.mouse.move(0, 0)
    await expect(page.getByRole('tooltip')).toHaveCount(0)
    await page.screenshot({ path: testInfo.outputPath(`board-${width}-${theme}-${lang}.png`), fullPage: false })
    await page.locator('[data-models-fullscreen]').click(); await expect(page.locator('.fullboard [data-board-ready="true"]')).toBeVisible()
    expect(await page.locator('.bc-head').first().evaluate(element => element.getBoundingClientRect().height)).toBe(120)
    await page.screenshot({ path: testInfo.outputPath(`full-board-${width}-${theme}-${lang}.png`), fullPage: false })
  })
}
