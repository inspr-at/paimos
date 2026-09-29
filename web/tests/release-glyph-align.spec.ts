// SPDX-License-Identifier: AGPL-3.0-only
// AEON-371: a kind glyph is centred on its title's first-line cap, at desktop
// and phone widths, including when that title wraps.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases } from './releases-fixtures'
import { historicHistory } from './releases-historic-fixtures'

const LONG = 'Dead sessions tidy up with a much longer release title that wraps onto a second and third line while describing how session controls and worker state now remain consistent across all views and devices'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })

async function open(page: Page) {
  await mockWork(page, fixtures())
  await mockReleases(page, historicHistory())
  await page.goto('/releases/260929113854.0.0')
  await expect(sheet(page).locator('article.detail .pill-title').first()).toBeVisible()
  await page.evaluate(() => document.fonts.ready)
}

// Glyph-box centre against the cap centre of H on the first line. The marker
// sits on that line's baseline; canvas font metrics give the cap.
async function samples(page: Page, wrap: boolean) {
  return page.evaluate(({ long, wrap }) => {
    const titles = [...document.querySelectorAll<HTMLElement>('.detail .features .pill-title, .detail .fixes .pill-title, .detail .other > ul > li > .subject')]
    const seen = new Set<string>()
    const out: { kind: string; delta: number; gap: number; wrapped: boolean; svgH: number; lineShift: number; transform: string; svgOpacity: number }[] = []
    for (const el of titles) {
      const group = el.closest('.group')
      const kind = group?.classList.contains('features') ? 'features' : group?.classList.contains('fixes') ? 'fixes' : 'other'
      if (seen.has(kind)) continue
      seen.add(kind)
      const glyph = el.querySelector<HTMLElement>('.change-glyph')!
      if (wrap) {
        for (const node of [...el.childNodes]) if (node !== glyph) node.remove()
        el.append(document.createTextNode(long))
      }
      const marker = document.createElement('span')
      marker.style.cssText = 'display:inline-block;vertical-align:baseline;width:0;height:0'
      glyph.after(marker)
      const style = getComputedStyle(el)
      const ctx = document.createElement('canvas').getContext('2d')!
      ctx.font = style.font
      const measured = ctx.measureText('H')
      const base = marker.getBoundingClientRect().top
      const capCenter = base - (measured.actualBoundingBoxAscent - measured.actualBoundingBoxDescent) / 2
      const svg = el.querySelector('svg')!.getBoundingClientRect()
      const text = [...el.childNodes].find(node => node.nodeType === Node.TEXT_NODE && node.textContent?.trim())
      let lineShift = 0
      if (text) {
        const range = document.createRange()
        range.selectNodeContents(text)
        const rects = [...range.getClientRects()].filter(rect => rect.width > 0)
        if (rects.length > 1) lineShift = rects[1]!.left - rects[0]!.left
      }
      const lineHeight = parseFloat(style.lineHeight)
      out.push({
        kind,
        delta: svg.top + svg.height / 2 - capCenter,
        gap: marker.getBoundingClientRect().left - svg.right,
        wrapped: el.getBoundingClientRect().height > lineHeight + 1,
        svgH: svg.height,
        lineShift,
        transform: `${getComputedStyle(glyph).transform} ${getComputedStyle(el.querySelector('svg')!).transform}`,
        svgOpacity: Number(getComputedStyle(el.querySelector('svg')!).opacity),
      })
      marker.remove()
    }
    return out
  }, { long: LONG, wrap })
}

for (const width of [1600, 390]) {
  test(`kind glyphs sit on the first line's cap centre at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
    await open(page)
    const detail = sheet(page).locator('article.detail')
    for (const wrap of [false, true]) {
      const rows = await samples(page, wrap)
      expect(rows.map(row => row.kind).sort()).toEqual(['features', 'fixes', 'other'])
      for (const row of rows) {
        expect(row.delta, `${width} ${row.kind} wrapped=${wrap}`).toBeGreaterThanOrEqual(-0.5)
        expect(row.delta, `${width} ${row.kind} wrapped=${wrap} delta ${row.delta}`).toBeLessThanOrEqual(0.5)
        expect(row.gap, `${width} ${row.kind} gap`).toBeGreaterThanOrEqual(7.5)
        expect(row.gap, `${width} ${row.kind} gap ${row.gap}`).toBeLessThanOrEqual(8.5)
        expect(row.svgH, `${width} ${row.kind} icon size`).toBeGreaterThan(12.5)
        expect(row.svgH, `${width} ${row.kind} icon size`).toBeLessThan(13.5)
        expect(row.transform, `${width} ${row.kind}`).toBe('none none')
        expect(row.svgOpacity, `${width} ${row.kind} svg opacity`).toBeCloseTo(0.7)
        if (wrap) {
          expect(row.wrapped, `${width} ${row.kind} should wrap`).toBe(true)
          expect(Math.abs(row.lineShift), `${width} ${row.kind} wrapped lines`).toBeLessThanOrEqual(0.5)
        }
      }
    }
    const overflow = await detail.evaluate(el => {
      const dialog = el.closest('dialog')
      return !!dialog && dialog.scrollWidth > dialog.clientWidth + 1
    })
    expect(overflow).toBe(false)
  })
}
