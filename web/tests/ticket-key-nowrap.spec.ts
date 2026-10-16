// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

// AEON-1379: a ticket key reads as one piece. The phone card list, the wide
// rows and a parent's child rows never break a key at its hyphen ("PHAROS- /
// 11"); the title beside it truncates or wraps instead.
const grid = (page: Page) => page.getByRole('grid', { name: 'Tickets' })
const rows = (page: Page) => grid(page).locator('tr.ticket-row:not(.ghost):not(.fit-row)')

// One line box for the key text, nothing clipped, and the key inside its frame.
async function wholeKey(key: Locator, frame: string) {
  return key.evaluate((el, selector) => {
    const range = document.createRange()
    range.selectNodeContents(el)
    const lines = new Set([...range.getClientRects()].filter(rect => rect.width > 0).map(rect => Math.round(rect.top))).size
    const box = el.getBoundingClientRect(), outer = el.closest(selector)!.getBoundingClientRect()
    const frameStyle = getComputedStyle(el.closest(selector)!)
    const why = `${el.textContent}: ${el.scrollWidth}/${el.clientWidth}px in ${Math.round(el.parentElement!.getBoundingClientRect().width)}px; columns ${frameStyle.gridTemplateColumns || frameStyle.display}`
    return { why, lines, clipped: el.scrollWidth > el.clientWidth + 1, inside: box.left >= outer.left - .5 && box.right <= outer.right + .5 }
  }, frame)
}

const SCHEMES = ['light', 'dark'] as const

for (const width of [320, 360, 390, 1440]) {
  test(`ticket keys stay on one line in the list and child rows at ${width}px in light and dark`, async ({ page }, testInfo) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: 900 })
    const data = fixtures()
    // A five-digit number on a six-letter project, with a title long enough to give way.
    Object.assign(data.nodes.find(node => node.id === 'n-2')!, { key: 'PHAROS-14530', title: 'Add an Oracle Cloud connector that provisions guarded tenancies across every configured region' })
    await mockWork(page, data)
    await page.goto('/p/PHAROS')
    await expect(rows(page)).toHaveCount(5)
    await expect(grid(page).locator('.c-key .key', { hasText: /^PHAROS-14530$/ })).toBeVisible()
    for (const colorScheme of SCHEMES) {
      await page.emulateMedia({ colorScheme })
      await page.evaluate(() => document.fonts.ready)
      for (const row of await rows(page).all()) {
        const keyFit = await wholeKey(row.locator('.c-key .key'), 'tr')
        expect(keyFit, `${colorScheme} ${keyFit.why}`).toMatchObject({ lines: 1, clipped: false, inside: true })
        const fit = await row.evaluate(tr => {
          const right = (selector: string) => tr.querySelector(selector)?.getBoundingClientRect().right ?? 0
          const label = tr.querySelector<HTMLElement>('.status-btn > span')!
          return {
            title: right('.title-link'), status: right('.status-btn'), card: tr.getBoundingClientRect().right - parseFloat(getComputedStyle(tr).paddingRight),
            labelWhole: label.scrollWidth <= label.clientWidth + 1,
            why: `${tr.querySelector('.c-key .key')!.textContent}: ${[...tr.children].map(td => `${td.className.split(' ')[0]} ${Math.round(td.getBoundingClientRect().width)}`).join(', ')}; columns ${getComputedStyle(tr).gridTemplateColumns}`,
          }
        })
        expect(fit.title, `the title gives way inside the card; ${fit.why}`).toBeLessThanOrEqual(fit.card + .5)
        expect(fit.status, `the status stays inside the card; ${fit.why}`).toBeLessThanOrEqual(fit.card + .5)
        // Only the narrowest phones shorten the status label; from 360px it reads whole.
        if (width >= 360) expect(fit.labelWhole, `the status label reads whole; ${fit.why}`).toBe(true)
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
      await page.screenshot({ path: testInfo.outputPath(`keys-list-${width}-${colorScheme}.png`) })
    }

    await rows(page).filter({ hasText: 'PHAROS-10' }).locator('.title-link').click()
    const children = page.getByRole('complementary', { name: 'Ticket details' }).locator('.child-row')
    await expect(children).toHaveCount(2)
    await expect(children.locator('.key', { hasText: /^PHAROS-14530$/ })).toBeVisible()
    for (const colorScheme of SCHEMES) {
      await page.emulateMedia({ colorScheme })
      for (const child of await children.all()) {
        const keyFit = await wholeKey(child.locator('.key'), '.child-row')
        expect(keyFit, `${colorScheme} ${keyFit.why}`).toMatchObject({ lines: 1, clipped: false, inside: true })
        const key = (await child.locator('.key').boundingBox())!, title = (await child.locator('.child-title').boundingBox())!
        expect(key.x + key.width, 'the key never runs under the title').toBeLessThanOrEqual(title.x + .5)
      }
      await page.screenshot({ path: testInfo.outputPath(`keys-children-${width}-${colorScheme}.png`) })
    }
    expect(errors).toEqual([])
  })
}
