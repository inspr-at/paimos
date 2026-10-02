// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Locator } from '@playwright/test'

// Compare layout coordinates rather than screenshots: text/theme changes are
// allowed, but controls and the sections above an expanding note must stay put.
export async function stableBoxes(controls: Record<string, Locator>) {
  const boxes = Object.fromEntries(await Promise.all(Object.entries(controls).map(async ([name, control]) => {
    const box = await control.boundingBox()
    expect(box, `${name} exists`).not.toBeNull()
    return [name, box!]
  })))
  return async () => {
    for (const [name, control] of Object.entries(controls)) {
      const box = await control.boundingBox()
      expect(box, `${name} exists after switching`).not.toBeNull()
      for (const dimension of ['x', 'y', 'width', 'height'] as const) {
        expect(Math.abs(box![dimension] - boxes[name]![dimension]), `${name}.${dimension} moved`).toBeLessThanOrEqual(1)
      }
    }
  }
}

export interface StableInteraction { name: string; run: () => Promise<unknown> }
export interface StableOptions {
  controls: Record<string, Locator>
  interactions: StableInteraction[]
  scrollAreas?: Record<string, Locator>
}

// Scroll-container coordinates distinguish intentional scrolling from layout
// movement. Fixed overlays do not inherit the page's scrolling coordinates.
async function box(locator: Locator) {
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
  expect(sample.width, 'sampled control width must be positive').toBeGreaterThan(0)
  expect(sample.height, 'sampled control height must be positive').toBeGreaterThan(0)
  return sample
}

/** Compare named controls against the initial layout after every interaction.
 * Callbacks await actual state changes, without fixed timeout sleeps. Include
 * selector groups, actions and the clicked row. Measure frame height only for a
 * phone sheet or an already scrolling body; top-anchored short frames may grow
 * downward. Locators may match the next series item; pass scrolling bodies to
 * catch horizontal overflow as well.
 */
export async function expectStableControls({ controls, interactions, scrollAreas = {} }: StableOptions) {
  expect(Object.keys(controls).length, 'name at least one control').toBeGreaterThan(0)
  expect(interactions.length, 'provide at least one interaction').toBeGreaterThan(0)
  const overflow = async (step: string) => {
    for (const [name, area] of Object.entries(scrollAreas)) {
      const extra = await area.evaluate(el => el.scrollWidth - el.clientWidth)
      expect(extra, `${step}: ${name} horizontal overflow`).toBeLessThanOrEqual(1)
    }
  }
  const baseline = new Map<string, Awaited<ReturnType<typeof box>>>()
  for (const [name, locator] of Object.entries(controls)) baseline.set(name, await box(locator))
  await overflow('initial')
  for (const interaction of interactions) {
    await interaction.run()
    for (const [name, locator] of Object.entries(controls)) {
      const current = await box(locator), before = baseline.get(name)!
      for (const axis of ['x', 'y', 'width', 'height'] as const) {
        expect(Math.abs(current[axis] - before[axis]), `${interaction.name}: ${name}.${axis} (${before[axis]} → ${current[axis]})`).toBeLessThanOrEqual(0.5)
      }
    }
    await overflow(interaction.name)
  }
}
