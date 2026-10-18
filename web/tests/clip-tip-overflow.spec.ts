// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page, type Locator } from '@playwright/test'
import { watchErrors } from './work-fixtures'

import { expectStableControls } from './helpers/stable'
import { longName } from './clip-tip-fixtures'
import { overflowShots, overflowText, noOverflow, keyboardTip, setupHarness } from './clip-tip-playwright-fixtures'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test.describe(`overflow fix4: ${width}px ${theme}`, () => {
    test.use({ viewport: { width, height: 400 }, colorScheme: theme, hasTouch: true })

    async function picker(page: Page) {
      await setupHarness(page, theme)
      await page.getByRole('button', { name: 'option', exact: true }).click()
      const panel = page.getByRole('dialog', { name: 'Assignee of PHAROS-11' })
      const row = panel.locator('.menu-item').first()
      const label = row.locator('.label')
      await label.evaluate((el, text) => { el.textContent = text }, overflowText)
      await expect(label).toHaveAttribute('data-tip', overflowText)
      await keyboardTip(page, row, overflowText)
      const tip = page.locator('.tooltip')
      await expect(tip).toHaveClass(/scrollable/)
      return { panel, row, tip }
    }

    async function finalLine(page: Page, tip: Locator, shot: string) {
      // Every navigation key must scroll this region, without moving the page
      // or the picker's active row. Browser animation is observed, never slept.
      await page.keyboard.press('ArrowDown')
      await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
      await page.keyboard.press('PageDown')
      await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBeGreaterThan(100)
      await page.keyboard.press('End')
      await expect.poll(() => tip.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThanOrEqual(1)
      await expect(tip).toContainText(`20. ${longName}`)
      await expect(tip).toHaveAttribute('role', 'region')
      await expect(tip).not.toHaveAttribute('aria-hidden', 'true')
      await expect(tip).toBeFocused()
      const box = (await tip.boundingBox())!
      expect(box.x).toBeGreaterThanOrEqual(8)
      expect(box.x + box.width).toBeLessThanOrEqual(width - 8)
      expect(box.y).toBeGreaterThanOrEqual(8)
      expect(box.y + box.height).toBeLessThanOrEqual(392)
      await page.screenshot({ path: `${overflowShots}/${shot}-text-${width}-${theme}.png` })
      await page.keyboard.press('PageUp')
      await expect.poll(() => tip.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeGreaterThan(0)
      const beforeUp = await tip.evaluate(el => el.scrollTop)
      await page.keyboard.press('ArrowUp')
      await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBeLessThan(beforeUp)
      await page.keyboard.press('Home')
      await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBe(0)
    }

    test('keyboard reaches the final line and Escape restores the standalone source', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      const name = page.locator('.standalone')
      await name.evaluate((el, text) => { el.textContent = text }, overflowText)
      await keyboardTip(page, name, overflowText)
      const tip = page.locator('.tooltip')
      const pageTop = await page.evaluate(() => scrollY)
      await expectStableControls({ controls: { name, change: page.getByRole('button', { name: 'Change name' }) }, interactions: [
        { name: 'Tab into full text', run: async () => { await page.keyboard.press('Tab'); await expect(tip).toBeFocused() } },
        { name: 'keyboard reads the final line', run: async () => { await finalLine(page, tip, 'standalone'); expect(await page.evaluate(() => scrollY)).toBe(pageTop) } },
        { name: 'Escape returns focus without reopening', run: async () => { await page.keyboard.press('Escape'); await expect(tip).toHaveCount(0); await expect(name).toBeFocused() } },
      ] })
      await noOverflow(page)
      await page.screenshot({ path: `${overflowShots}/standalone-${width}-${theme}.png` })
      expect(errors).toEqual([])
    })

    test('Tab reads picker overflow and Escape returns to the same option before closing', async ({ page }) => {
      const errors = watchErrors(page)
      const { panel, row, tip } = await picker(page)
      const short = panel.locator('.menu-item').last()
      const pageTop = await page.evaluate(() => scrollY)
      await expectStableControls({ controls: { row, short, group: panel.locator('.menu'), title: panel.locator('.menu-title') }, interactions: [
        { name: 'Tab into picker full text', run: async () => { await page.keyboard.press('Tab'); await expect(tip).toBeFocused(); await expect(panel).toBeVisible() } },
        { name: 'scroll without picker navigation', run: async () => { await finalLine(page, tip, 'picker-keyboard'); await expect(short).not.toBeFocused(); expect(await page.evaluate(() => scrollY)).toBe(pageTop) } },
        { name: 'Shift Tab returns to source', run: async () => { await page.keyboard.press('Shift+Tab'); await expect(row).toBeFocused(); await expect(tip).toBeVisible() } },
        { name: 'Escape dismisses only the full text', run: async () => {
          await page.keyboard.press('Tab'); await expect(tip).toBeFocused()
          await page.keyboard.press('Escape'); await expect(tip).toHaveCount(0); await expect(row).toBeFocused(); await expect(panel).toBeVisible()
        } },
      ], scrollAreas: { picker: panel } })
      await noOverflow(page)
      await page.screenshot({ path: `${overflowShots}/picker-keyboard-${width}-${theme}.png` })
      await page.keyboard.press('Escape')
      await expect(panel).toHaveCount(0)
      expect(errors).toEqual([])
    })

    for (const input of ['scrollbar', 'touch'] as const) {
      test(`${input} scrolling keeps the originating picker open`, async ({ page }) => {
        const errors = watchErrors(page)
        const { panel, row, tip } = await picker(page)
        await expectStableControls({ controls: { row, short: panel.locator('.menu-item').last(), group: panel.locator('.menu'), title: panel.locator('.menu-title') }, interactions: [
          { name: `${input} interacts with overflowing text`, run: async () => {
            const box = (await tip.boundingBox())!
            if (input === 'scrollbar') {
              // Press the scrollbar gutter/track itself, then wheel inside the
              // region. Both pointerdown and scrolling must belong to the panel.
              await page.mouse.click(box.x + box.width - 2, box.y + box.height / 2)
              await expect(panel).toBeVisible()
              await tip.hover(); await page.mouse.wheel(0, 200)
            } else {
              const cdp = await page.context().newCDPSession(page)
              try {
                const x = box.x + box.width / 2, y = box.y + box.height - 40
                await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] })
                await expect(panel).toBeVisible()
                for (const distance of [30, 60, 90, 120]) await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y: y - distance }] })
                await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
              } finally { await cdp.detach() }
            }
            await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
            await expect(panel).toBeVisible(); await expect(tip).toHaveText(overflowText)
            await expect(page.getByRole('status')).toBeEmpty()
          } },
        ], scrollAreas: { picker: panel } })
        await noOverflow(page)
        await page.screenshot({ path: `${overflowShots}/picker-${input}-${width}-${theme}.png` })
        expect(errors).toEqual([])
      })
    }
  })
}
