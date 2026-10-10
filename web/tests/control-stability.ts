// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Locator, type Page } from '@playwright/test'
// Pattern A: measure controls in their scroll container, allowing content below
// to grow. Pattern B callers can include the frame to also freeze its height.
export async function controlStability(page: Page, controls: Record<string, Locator>) {
  let interactions = 0
  const measure = async () => {
    const samples: Record<string, { x: number; y: number; width: number; height: number }> = {}
    for (const [name, locator] of Object.entries(controls)) {
      await expect(locator, name).toBeVisible()
      samples[name] = await locator.evaluate(el => {
        const rect = el.getBoundingClientRect()
        let x = rect.x + window.scrollX, y = rect.y + window.scrollY
        for (let parent = el.parentElement; parent; parent = parent.parentElement) { x += parent.scrollLeft; y += parent.scrollTop }
        return { x, y, width: rect.width, height: rect.height }
      })
      expect(samples[name].width, name).toBeGreaterThan(0); expect(samples[name].height, name).toBeGreaterThan(0)
    }
    return samples
  }
  const before = await measure()
  return {
    async check(interaction: () => Promise<unknown>) {
      await interaction(); interactions++
      const after = await measure()
      for (const name of Object.keys(before)) for (const dimension of ['x', 'y', 'width', 'height'] as const) {
        expect(Math.abs(after[name][dimension] - before[name][dimension]), `${name}.${dimension}`).toBeLessThanOrEqual(.5)
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 'horizontal overflow').toBeLessThanOrEqual(1)
    },
    done() { expect(interactions, 'guard must measure an interaction').toBeGreaterThan(0) },
  }
}
