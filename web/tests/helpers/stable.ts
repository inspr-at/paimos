// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Locator } from '@playwright/test'

export interface StableInteraction { name: string; run: () => Promise<unknown> }
export interface StableOptions {
  controls: Record<string, Locator>
  interactions: StableInteraction[]
  scrollAreas?: Record<string, Locator>
  tolerance?: number
  ready?: () => Promise<unknown>
}

// Scroll-container coordinates distinguish intentional scrolling from layout
// movement. Fixed overlays do not inherit the page's scrolling coordinates.
async function box(locator: Locator, name = 'control') {
  await expect(locator).toHaveCount(1)
  await expect(locator).toBeVisible()
  const sample = await locator.evaluate(async element => {
    await document.fonts.ready
    await Promise.all(element.getAnimations({ subtree: true })
      .filter(animation => animation.effect?.getTiming().iterations !== Infinity)
      .map(animation => animation.finished.catch(() => {})))
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
    const rect = element.getBoundingClientRect()
    let x = rect.x, y = rect.y
    for (let current: Element | null = element; current; current = current.parentElement) {
      if (current !== element) { x += current.scrollLeft; y += current.scrollTop }
      if (getComputedStyle(current).position === 'fixed') break
    }
    return { x, y, width: rect.width, height: rect.height }
  })
  expect(sample.width, `sampled control width must be positive (${name})`).toBeGreaterThan(0)
  expect(sample.height, `sampled control height must be positive (${name})`).toBeGreaterThan(0)
  return sample
}

/** Compare named controls against the initial layout after every interaction.
 * Callbacks await actual state changes, without fixed timeout sleeps. Include
 * selector groups, actions and the clicked row. Measure frame height only for a
 * phone sheet or an already scrolling body; top-anchored short frames may grow
 * downward. Locators may match the next series item; pass scrolling bodies to
 * catch horizontal overflow as well. Use ready for view-specific layout work
 * that finishes after rows appear; it runs before every set of samples.
 */
export async function expectStableControls({ controls, interactions, scrollAreas = {}, tolerance = 0.5, ready }: StableOptions) {
  expect(Object.keys(controls).length, 'name at least one control').toBeGreaterThan(0)
  expect(interactions.length, 'provide at least one interaction').toBeGreaterThan(0)
  const overflow = async (step: string) => {
    for (const [name, area] of Object.entries(scrollAreas)) {
      const extra = await area.evaluate(el => el.scrollWidth - el.clientWidth)
      expect(extra, `${step}: ${name} horizontal overflow`).toBeLessThanOrEqual(1)
    }
  }
  // Settle the named controls together. Serial double-frame waits multiplied
  // Hours' 12 layouts into 300 frame waits and exhausted its 30-second budget.
  // Every control still waits for fonts/animations and keeps the same checks.
  const measure = async () => {
    await ready?.()
    return new Map(await Promise.all(Object.entries(controls).map(async ([name, locator]) =>
      [name, await box(locator, name)] as const)))
  }
  const baseline = await measure()
  await overflow('initial')
  for (const interaction of interactions) {
    await interaction.run()
    const samples = await measure()
    for (const [name, current] of samples) {
      const before = baseline.get(name)!
      for (const axis of ['x', 'y', 'width', 'height'] as const) {
        expect(Math.abs(current[axis] - before[axis]), `${interaction.name}: ${name}.${axis} (${before[axis]} → ${current[axis]})`).toBeLessThanOrEqual(tolerance)
      }
    }
    await overflow(interaction.name)
  }
}
