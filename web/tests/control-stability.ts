// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Locator, type Page } from '@playwright/test'

// Document position plus nested scroller offsets. window.scrollX/Y already
// includes document.scrollingElement, so that element's own scroll must not
// be added again. A pure window scroll then stays put; a nested scroller
// (the app's main column) still counts once.
export function contentBox(
  rect: { x: number; y: number; width: number; height: number },
  windowScroll: { x: number; y: number },
  ancestors: { scrollLeft: number; scrollTop: number; root: boolean }[],
) {
  let x = rect.x + windowScroll.x, y = rect.y + windowScroll.y
  for (const parent of ancestors) {
    if (parent.root) continue
    x += parent.scrollLeft
    y += parent.scrollTop
  }
  return { x, y, width: rect.width, height: rect.height }
}

// Pattern A: measure controls in their scroll container, allowing content below
// to grow. Pattern B callers can include the frame to also freeze its height.
// `grows` names controls that grow away from the actions by design (AEON-1438:
// the chat field grows upward with its text): their left, right and bottom
// edges must stay; their top and height may follow the text.
export async function controlStability(page: Page, controls: Record<string, Locator>, options: { grows?: string[] } = {}) {
  let interactions = 0
  const measure = async () => {
    const samples: Record<string, { x: number; y: number; width: number; height: number }> = {}
    for (const [name, locator] of Object.entries(controls)) {
      await expect(locator, name).toBeVisible()
      const sample = await locator.evaluate(el => {
        const rect = el.getBoundingClientRect(), root = document.scrollingElement
        const ancestors = []
        for (let parent = el.parentElement; parent; parent = parent.parentElement) {
          ancestors.push({ scrollLeft: parent.scrollLeft, scrollTop: parent.scrollTop, root: parent === root })
        }
        return { rect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height }, windowScroll: { x: window.scrollX, y: window.scrollY }, ancestors }
      })
      samples[name] = contentBox(sample.rect, sample.windowScroll, sample.ancestors)
      expect(samples[name].width, name).toBeGreaterThan(0); expect(samples[name].height, name).toBeGreaterThan(0)
    }
    return samples
  }
  const before = await measure()
  return {
    async check(interaction: () => Promise<unknown>) {
      await interaction(); interactions++
      const after = await measure()
      for (const name of Object.keys(before)) {
        if (options.grows?.includes(name)) {
          expect(Math.abs(after[name].x - before[name].x), `${name}.x`).toBeLessThanOrEqual(.5)
          expect(Math.abs(after[name].width - before[name].width), `${name}.width`).toBeLessThanOrEqual(.5)
          expect(Math.abs(after[name].y + after[name].height - before[name].y - before[name].height), `${name}.bottom`).toBeLessThanOrEqual(.5)
          continue
        }
        for (const dimension of ['x', 'y', 'width', 'height'] as const) {
          expect(Math.abs(after[name][dimension] - before[name][dimension]), `${name}.${dimension}`).toBeLessThanOrEqual(.5)
        }
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 'horizontal overflow').toBeLessThanOrEqual(1)
    },
    done() { expect(interactions, 'guard must measure an interaction').toBeGreaterThan(0) },
  }
}
