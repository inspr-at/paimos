// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Locator } from '@playwright/test'

// AEON-730: the shared dialog size scale (tokens.css --dialog-s/m/l).
export const DIALOG_SCALE = { s: 440, m: 560, l: 840 } as const
export type DialogSize = keyof typeof DIALOG_SCALE

export interface DialogSample {
  frame: { x: number; y: number; width: number; height: number }
  buttons: { label: string; width: number; natural: number; height: number }[]
}

/** Measure a dialog frame and its action buttons. A button's natural width is
 * its content's extent (icons, label, keycaps, gaps) plus padding and border:
 * what it needs for its label, not what the row could give it. */
export async function sampleDialog(frame: Locator, actions: Locator): Promise<DialogSample> {
  await expect(frame).toBeVisible()
  return frame.evaluate(async (element, buttonsRoot) => {
    await document.fonts.ready
    await Promise.all(element.getAnimations({ subtree: true }).map(animation => animation.finished.catch(() => {})))
    const rect = element.getBoundingClientRect()
    const buttons = [...(buttonsRoot as Element).querySelectorAll<HTMLElement>('.btn, button.desk-btn')]
      .filter(button => button.offsetParent !== null)
      .map(button => {
        const box = button.getBoundingClientRect()
        const style = getComputedStyle(button)
        const children = [...button.childNodes]
        let left = Infinity, right = -Infinity
        for (const child of children) {
          const range = document.createRange()
          range.selectNode(child)
          for (const part of range.getClientRects()) {
            if (!part.width) continue
            left = Math.min(left, part.left); right = Math.max(right, part.right)
          }
        }
        const content = Number.isFinite(left) ? right - left : 0
        const chrome = ['paddingLeft', 'paddingRight', 'borderLeftWidth', 'borderRightWidth'].reduce((sum, key) => sum + Number.parseFloat(style[key as 'paddingLeft']), 0)
        return { label: button.textContent?.trim() ?? '', width: box.width, natural: content + chrome, height: box.height }
      })
    return { frame: { x: rect.x, y: rect.y, width: rect.width, height: rect.height }, buttons }
  }, await actions.elementHandle())
}

/** Desktop: the frame stays within its scale step and inside the window; no
 * action button is wider than its label needs (a small minimum is allowed). */
export async function expectCompactDialog(frame: Locator, actions: Locator, size: DialogSize, viewport: { width: number; height: number }) {
  const sample = await sampleDialog(frame, actions)
  expect.soft(sample.frame.width, `dialog width within the ${size.toUpperCase()} step`).toBeLessThanOrEqual(DIALOG_SCALE[size] + 0.5)
  expect.soft(sample.frame.x, 'dialog starts inside the window').toBeGreaterThanOrEqual(-0.5)
  expect.soft(sample.frame.x + sample.frame.width, 'dialog ends inside the window').toBeLessThanOrEqual(viewport.width + 0.5)
  expect.soft(sample.buttons.length, 'dialog has measurable actions').toBeGreaterThan(0)
  for (const button of sample.buttons) {
    expect.soft(button.width, `“${button.label}” is no wider than its label needs`).toBeLessThanOrEqual(Math.max(button.natural, 88) + 1)
    expect.soft(button.height, `“${button.label}” is a compact desktop button`).toBeLessThanOrEqual(36.5)
  }
  return sample
}

/** Phone: a full-height sheet with actions pinned to the bottom edge and touch
 * targets of at least 44px. */
export async function expectPhoneSheet(frame: Locator, actions: Locator, viewport: { width: number; height: number }) {
  const sample = await sampleDialog(frame, actions)
  expect.soft(sample.frame.width, 'phone sheet spans the window').toBeGreaterThanOrEqual(viewport.width - 16.5)
  expect.soft(sample.frame.x + sample.frame.width).toBeLessThanOrEqual(viewport.width + 0.5)
  const bar = await actions.boundingBox()
  expect.soft(bar, 'phone action bar is visible').not.toBeNull()
  if (bar) expect.soft(bar.y + bar.height, 'phone action bar is pinned to the bottom').toBeGreaterThanOrEqual(sample.frame.y + sample.frame.height - 48)
  for (const button of sample.buttons) expect.soft(button.height, `“${button.label}” is a touch target`).toBeGreaterThanOrEqual(43.5)
  return sample
}
