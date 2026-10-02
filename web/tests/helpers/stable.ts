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
