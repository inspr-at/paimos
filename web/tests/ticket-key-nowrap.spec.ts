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
      let priorityRows = 0
      for (const row of await rows(page).all()) {
        const keyFit = await wholeKey(row.locator('.c-key .key'), 'tr')
        expect(keyFit, `${colorScheme} ${keyFit.why}`).toMatchObject({ lines: 1, clipped: false, inside: true })
        const fit = await row.evaluate(tr => {
          const right = (selector: string) => tr.querySelector(selector)?.getBoundingClientRect().right ?? 0
          const label = tr.querySelector<HTMLElement>('.status-btn > span:not(.status-icon)')!
          const icon = tr.querySelector<SVGElement>('.c-prio svg.prio')
          const date = tr.querySelector<HTMLElement>('.c-updated time, .c-eta time, .c-eta .when')
          let priority: { gap: number; intersect: boolean } | null = null
          if (icon && date) {
            const a = icon.getBoundingClientRect(), b = date.getBoundingClientRect()
            if (a.width >= 1 && b.width >= 1) {
              priority = {
                gap: b.left - a.right,
                intersect: a.right > b.left + .5 && a.left < b.right - .5 && a.bottom > b.top + .5 && a.top < b.bottom - .5,
              }
            }
          }
          return {
            title: right('.title-link'), status: right('.status-btn'), statusCell: right('.c-status'), date: date?.getBoundingClientRect().right ?? 0, card: tr.getBoundingClientRect().right - parseFloat(getComputedStyle(tr).paddingRight),
            label: label.textContent?.trim() ?? '',
            labelShown: label.getClientRects().length > 0 && label.getBoundingClientRect().width >= 1,
            labelWhole: label.scrollWidth <= label.clientWidth + 1,
            name: tr.querySelector('.status-btn')!.getAttribute('aria-label') ?? '',
            dateWhole: !!date && date.scrollWidth <= date.clientWidth + 1,
            priority,
            why: `${tr.querySelector('.c-key .key')!.textContent}: ${[...tr.children].map(td => `${td.className.split(' ')[0]} ${Math.round(td.getBoundingClientRect().width)}`).join(', ')}; columns ${getComputedStyle(tr).gridTemplateColumns}`,
          }
        })
        expect(fit.title, `the title gives way inside the card; ${fit.why}`).toBeLessThanOrEqual(fit.card + .5)
        expect(fit.status, `the status stays inside the card; ${fit.why}`).toBeLessThanOrEqual(fit.card + .5)
        // On a phone card the status chip owns its whole slot. A hidden cue
        // that keeps room beside it squeezes the label on wider fonts.
        if (width <= 720) expect(fit.statusCell - fit.status, `nothing hidden keeps room beside the status; ${fit.why}`).toBeLessThanOrEqual(.5)
        expect(fit.date, `the date stays inside the card; ${fit.why}`).toBeLessThanOrEqual(fit.card + .5)
        // From 360px the status label reads whole.
        if (width >= 360) expect({ shown: fit.labelShown, whole: fit.labelWhole }, `the status label reads whole; ${fit.why}`).toEqual({ shown: true, whole: true })
        // At 320 the label gives way whole before the row can grow: the chip
        // keeps its icon and the button still names the status. The date has
        // not had to give way yet.
        if (width === 320) {
          expect(fit.label, fit.why).not.toBe('')
          expect(fit.labelShown, `the status label goes icon-only at 320; ${fit.why}`).toBe(false)
          expect(fit.name, fit.why).toContain(`Status: ${fit.label}`)
        }
        if (width <= 720) expect(fit.dateWhole, `the date reads whole; ${fit.why}`).toBe(true)
        if ((width === 320 || width === 360) && fit.priority) {
          priorityRows++
          expect(fit.priority.intersect, `${colorScheme} the priority icon stays clear of the date; ${fit.why}`).toBe(false)
          expect(fit.priority.gap, `${colorScheme} the priority icon keeps at least 6px from the date; ${fit.why}`).toBeGreaterThanOrEqual(6)
        }
      }
      if (width === 320 || width === 360) expect(priorityRows, `${colorScheme}: both priority rows keep a slot beside the date`).toBeGreaterThanOrEqual(2)
      // Nothing runs past the viewport, and the card keeps the page inset and its right border.
      const edge = await grid(page).evaluate(el => {
        const scroller = document.scrollingElement!
        return { scrollWidth: scroller.scrollWidth, clientWidth: scroller.clientWidth, card: el.closest('.table-card')!.getBoundingClientRect().right }
      })
      expect(edge.scrollWidth, `${colorScheme} the page never scrolls sideways`).toBeLessThanOrEqual(edge.clientWidth)
      if (width === 320 || width === 360) expect(edge.card, `${colorScheme} the card ends 12px inside the viewport`).toBeLessThanOrEqual(edge.clientWidth - 12 + .5)
      await page.screenshot({ path: testInfo.outputPath(`keys-list-${width}-${colorScheme}.png`) })
    }

    if (width === 320) {
      // Squeezed further, the date is next to give way. The key and the
      // icon-only status chip stay whole, and nothing leaves the card.
      await grid(page).evaluate(el => { (el.closest('.table-card') as HTMLElement).style.width = '228px' })
      let datesGivingWay = 0
      for (const row of await rows(page).all()) {
        const keyFit = await wholeKey(row.locator('.c-key .key'), 'tr')
        expect(keyFit, `squeezed ${keyFit.why}`).toMatchObject({ lines: 1, clipped: false, inside: true })
        const fit = await row.evaluate(tr => {
          const chip = tr.querySelector<HTMLElement>('.status-btn')!, icon = chip.querySelector('.status-icon')!.getBoundingClientRect(), box = chip.getBoundingClientRect()
          const date = tr.querySelector<HTMLElement>('.c-updated time')!
          return {
            card: tr.getBoundingClientRect().right - parseFloat(getComputedStyle(tr).paddingRight),
            date: date.getBoundingClientRect().right,
            dateWhole: date.scrollWidth <= date.clientWidth + 1,
            chipWhole: icon.left >= box.left - .5 && icon.right <= box.right + .5,
            why: `${tr.querySelector('.c-key .key')!.textContent}: columns ${getComputedStyle(tr).gridTemplateColumns}`,
          }
        })
        expect(fit.chipWhole, `squeezed: the status icon stays whole; ${fit.why}`).toBe(true)
        expect(fit.date, `squeezed: the date stays inside the card; ${fit.why}`).toBeLessThanOrEqual(fit.card + .5)
        if (!fit.dateWhole) datesGivingWay++
      }
      expect(datesGivingWay, 'squeezed: the long key\'s date gives way').toBeGreaterThanOrEqual(1)
      await grid(page).evaluate(el => { (el.closest('.table-card') as HTMLElement).style.width = '' })
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
