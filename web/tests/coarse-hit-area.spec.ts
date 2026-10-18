// SPDX-License-Identifier: AGPL-3.0-only
// AEON-723: dropping .journey-chip left .ekey::before::before. That selector is
// invalid, and one invalid selector drops the whole comma-separated ::before
// rule, so narrow viewports and coarse pointers lose the 44px hit area on
// every control in the group. The control's own box must stay its real size.
import { readFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'

// Changed-area selection follows this static import. The spec reads the sheet
// itself; calling the import would ask the Node runner to load CSS.
export function hitAreaStylesheetModule() {
  return import('../src/styles/base.css')
}
void hitAreaStylesheetModule

const css = readFileSync(new URL('../src/styles/base.css', import.meta.url), 'utf8')
if (css.toLowerCase().includes('</style')) throw new Error('base.css cannot be inlined into the regression document')

const controls = [
  ['btn', 'button', 'btn'],
  ['link', 'a', 'title-link'],
  ['chip', 'a', 'ticket-chip'],
  ['row', 'a', 'row-main'],
  ['ekey', 'a', 'ekey'],
] as const

function documentWithSheet() {
  const items = controls.map(([id, tag, cls]) =>
    `<${tag} class="${cls}" id="${id}" ${tag === 'a' ? 'href="#reach"' : 'type="button"'} style="display:inline-block;width:28px;height:26px;padding:0;margin:0">${id}</${tag}>`).join('')
  return `<!doctype html><style>${css}</style><main style="display:grid;grid-template-columns:repeat(3,28px);gap:64px;padding:80px">${items}</main>`
}

async function reach(page: Page, id: string) {
  return page.locator(`#${id}`).evaluate(el => {
    const rect = el.getBoundingClientRect()
    const pseudo = getComputedStyle(el, '::before')
    const x = rect.x + rect.width / 2
    const y = rect.y + rect.height / 2
    const own = (dx: number, dy: number) => {
      const hit = document.elementFromPoint(x + dx, y + dy)
      return !!hit && (hit === el || el.contains(hit))
    }
    return {
      content: pseudo.content,
      width: Number.parseFloat(pseudo.width),
      height: Number.parseFloat(pseudo.height),
      visualWidth: rect.width,
      visualHeight: rect.height,
      centre: own(0, 0),
      // Chromium excludes the right and bottom edges. Stay one fraction inside.
      edges: [own(-22, 0), own(21.99, 0), own(0, -22), own(0, 21.99)],
    }
  })
}

for (const device of [
  { width: 390, coarse: false, extended: true },
  { width: 1024, coarse: true, extended: true },
  { width: 1440, coarse: false, extended: false },
]) {
  test.describe(`${device.width}px ${device.coarse ? 'coarse' : 'fine'} pointer`, () => {
    test.use({ viewport: { width: device.width, height: 900 }, hasTouch: device.coarse })
    test(`shared controls ${device.extended ? 'extend to a 44px hit area' : 'keep only their own box'}`, async ({ page }) => {
      await page.setContent(documentWithSheet())
      expect(await page.evaluate(() => matchMedia('(max-width: 720px), (pointer: coarse)').matches)).toBe(device.extended)
      for (const [id] of controls) {
        const box = await reach(page, id)
        expect(box.centre, `${id} centre stays on the control`).toBe(true)
        expect(box.visualWidth, `${id} visual width stays the authored box`).toBe(28)
        expect(box.visualHeight, `${id} visual height stays the authored box`).toBe(26)
        if (device.extended) {
          expect(box.content, `${id} hit-area rule applies`).toBe('""')
          expect(box.width, `${id} hit area is at least 44px wide`).toBeGreaterThanOrEqual(44)
          expect(box.height, `${id} hit area is at least 44px tall`).toBeGreaterThanOrEqual(44)
          expect(box.edges, `${id} owns all four edges of the 44px reach`).toEqual([true, true, true, true])
        } else {
          expect(box.content, `${id} has no hit-area extender`).toBe('none')
          expect(box.edges, `${id} does not claim points outside its box`).toEqual([false, false, false, false])
        }
      }
    })
  })
}
