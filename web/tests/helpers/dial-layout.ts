// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Locator } from '@playwright/test'

/** Keep the density check tied to the content rather than the retired row size.
 * AEON-1036 (approved draft 6) fixes the rows at 88 px, and 200 px on a phone (the approved draft's 186 px clips the 44 px selector and stepper), so the
 * controls line up in one column and never move; the usage lines and the pace state
 * live in the left column.
 * Measure an invisible, naturally sized copy in the same query container so
 * wrapping is allowed, but an arbitrarily padded fixed-height row still fails.
 */
export async function expectDialRowsFitContent(dial: Locator) {
  const layout = await dial.evaluate(async element => {
    await document.fonts.ready
    const rows = element.querySelector<HTMLElement>('.rows')!
    const source = [...rows.children] as HTMLElement[]
    const style = getComputedStyle(element)
    const narrow = element.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight) <= 760
    const copy = rows.cloneNode(true) as HTMLElement
    Object.assign(copy.style, { position: 'absolute', top: '0', left: '0', visibility: 'hidden', pointerEvents: 'none', width: `${rows.getBoundingClientRect().width}px`, margin: '0' })
    copy.setAttribute('aria-hidden', 'true')
    for (const row of copy.children) Object.assign((row as HTMLElement).style, { height: 'auto', minHeight: '0' })
    rows.parentElement!.append(copy)
    try {
      return source.map((row, index) => {
        const rect = row.getBoundingClientRect()
        const natural = copy.children[index]!.getBoundingClientRect().height
        const revision10 = !!row.querySelector('.value-slot')
        const minimum = revision10 ? (narrow ? 200 : 88) : (narrow ? 126 : 60)
        const content = [...row.children].map(child => {
          const box = child.getBoundingClientRect()
          return { width: box.width, height: box.height, top: box.top - rect.top, bottom: box.bottom - rect.top, left: box.left - rect.left, right: box.right - rect.left }
        })
        return { key: row.dataset.key, height: rect.height, width: rect.width, natural, minimum, content }
      })
    } finally { copy.remove() }
  })
  expect(layout.length, 'dial has harness rows').toBeGreaterThan(0)
  for (const row of layout) {
    expect(row.natural, `${row.key}: natural content is measurable`).toBeGreaterThan(0)
    expect(row.height, `${row.key}: row fits its content and approved minimum`).toBeLessThanOrEqual(Math.max(row.minimum, row.natural) + 0.5)
    for (const content of row.content) {
      expect(content.width, `${row.key}: content has width`).toBeGreaterThan(0)
      expect(content.height, `${row.key}: content has height`).toBeGreaterThan(0)
      expect(content.top, `${row.key}: content stays inside row top`).toBeGreaterThanOrEqual(-0.5)
      expect(content.bottom, `${row.key}: content stays inside row bottom`).toBeLessThanOrEqual(row.height + 0.5)
      expect(content.left, `${row.key}: content stays inside row left`).toBeGreaterThanOrEqual(-0.5)
      expect(content.right, `${row.key}: content stays inside row right`).toBeLessThanOrEqual(row.width + 0.5)
    }
  }
}
