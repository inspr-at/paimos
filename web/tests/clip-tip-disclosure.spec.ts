// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { watchErrors } from './work-fixtures'

import { expectStableControls } from './helpers/stable'
import { longName } from './clip-tip-fixtures'
import { fixShots, noOverflow, keyboardTip, scrollPage, setupHarness } from './clip-tip-playwright-fixtures'

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test.describe(`tooltip fix: ${width}px ${theme}`, () => {
    test.use({ viewport: { width, height: 400 }, colorScheme: theme })

    test('keyboard disclosure survives pointer movement until Escape or blur', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      const name = page.locator('.standalone')
      const change = page.getByRole('button', { name: 'Change name' })
      await keyboardTip(page, name, longName)
      await expect(name).toBeFocused()
      await expectStableControls({ controls: { name, change }, interactions: [
        { name: 'pointer leaves the keyboard source', run: async () => {
          await name.hover()
          await page.mouse.move(1, 1)
          await expect(name).toBeFocused()
          await expect(page.locator('.tooltip')).toHaveText(longName)
        } },
        { name: 'pointer crosses another tooltip source', run: async () => {
          await change.evaluate(el => el.setAttribute('data-tip', 'Andere Aktion'))
          await change.hover()
          await expect(name).toBeFocused()
          await expect(page.locator('.tooltip')).toHaveText(longName)
        } },
        { name: 'ordinary key keeps the focused disclosure', run: async () => {
          await name.press('a')
          await expect(page.locator('.tooltip')).toHaveText(longName)
        } },
        { name: 'Escape dismisses without a pointer reopening it', run: async () => {
          await name.press('Escape')
          await expect(page.locator('.tooltip')).toHaveCount(0)
          await expect(name).toBeFocused()
          await page.evaluate(() => document.body.appendChild(document.createElement('span')))
          await expect(page.locator('.tooltip')).toHaveCount(0)
        } },
        { name: 'focus leaves the source', run: async () => {
          await keyboardTip(page, name, longName)
          await change.focus()
          await expect(page.locator('.tooltip')).toHaveText('Andere Aktion')
          await change.evaluate(el => el.removeAttribute('data-tip'))
          await expect(page.locator('.tooltip')).toHaveCount(0)
        } },
      ] })
      await noOverflow(page)
      await keyboardTip(page, name, longName)
      await page.mouse.move(1, 1)
      await expect(page.locator('.tooltip')).toHaveText(longName)
      await page.screenshot({ path: `${fixShots}/keyboard-${width}-${theme}.png` })
      expect(errors).toEqual([])
    })

    test('focus disclosure follows real scrolling; pointer disclosure dismisses', async ({ page }) => {
      const errors = watchErrors(page)
      await setupHarness(page, theme)
      const name = page.locator('.standalone')
      const change = page.getByRole('button', { name: 'Change name' })
      await keyboardTip(page, name, longName)
      const before = (await name.boundingBox())!
      await expectStableControls({ controls: { name, change }, interactions: [
        { name: 'scroll the focused name', run: async () => {
          await scrollPage(page, 50)
          await expect(name).toBeFocused()
          await expect(page.locator('.tooltip')).toHaveText(longName)
          const tipBox = (await page.locator('.tooltip').boundingBox())!
          const nameBox = (await name.boundingBox())!
          expect(nameBox.y).toBeCloseTo(before.y - 50, 1)
          expect(tipBox.y + tipBox.height).toBeCloseTo(nameBox.y - 8, 0)
        } },
        { name: 'scroll the focused name offscreen', run: async () => {
          await scrollPage(page, 250)
          await expect(name).toBeFocused()
          await expect(page.locator('.tooltip')).toHaveText(longName)
          expect((await page.locator('.tooltip').boundingBox())!.y).toBeGreaterThanOrEqual(8)
        } },
      ] })
      await page.screenshot({ path: `${fixShots}/scroll-${width}-${theme}.png` })
      await page.evaluate(() => (document.activeElement as HTMLElement)?.blur())
      await scrollPage(page, 0)
      await name.hover()
      await expect(page.locator('.tooltip')).toHaveText(longName)
      await scrollPage(page, 50)
      await expect(page.locator('.tooltip')).toHaveCount(0)
      expect(errors).toEqual([])
    })

    for (const side of ['default', 'end'] as const) {
      test(`long ${side} disclosure is viewport bounded and scrolls inside`, async ({ page }) => {
        const errors = watchErrors(page)
        await setupHarness(page, theme)
        const name = page.locator('.standalone')
        const longText = Array.from({ length: 20 }, (_, index) => `${index + 1}. ${longName}`).join('\n')
        await name.evaluate((el, options) => {
          el.textContent = options.text
          if (options.side === 'end') el.setAttribute('data-tip-side', 'end')
        }, { text: longText, side })
        await expect(name).toHaveAttribute('data-tip', longText)
        await keyboardTip(page, name, longText)
        const tip = page.locator('.tooltip')
        const checkBounds = async () => {
          const box = (await tip.boundingBox())!
          expect(box.y).toBeGreaterThanOrEqual(8)
          expect(box.y + box.height).toBeLessThanOrEqual(392)
          expect(box.x).toBeGreaterThanOrEqual(8)
          expect(box.x + box.width).toBeLessThanOrEqual(width - 8)
          expect(await tip.evaluate(el => el.scrollHeight - el.clientHeight)).toBeGreaterThan(0)
          expect(await tip.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1)
        }
        await checkBounds()
        await expectStableControls({ controls: { name, change: page.getByRole('button', { name: 'Change name' }) }, interactions: [
          { name: 'read overflowing disclosure with the wheel', run: async () => {
            await tip.hover()
            await page.mouse.wheel(0, 200)
            await expect.poll(() => tip.evaluate(el => el.scrollTop)).toBeGreaterThan(0)
            await expect(name).toBeFocused()
            await expect(tip).toHaveText(longText)
            expect(await page.evaluate(() => scrollY)).toBe(0)
            await checkBounds()
          } },
          { name: 'move source towards viewport top', run: async () => {
            await scrollPage(page, 160)
            await expect(tip).toHaveText(longText)
            await checkBounds()
          } },
        ] })
        await noOverflow(page)
        await page.screenshot({ path: `${fixShots}/long-${side}-${width}-${theme}.png` })
        expect(errors).toEqual([])
      })
    }
  })
}
