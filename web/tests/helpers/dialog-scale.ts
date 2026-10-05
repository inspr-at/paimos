// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Locator } from '@playwright/test'
import { expectStableControls, type StableInteraction } from './stable'

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
export async function expectCompactDialog(frame: Locator, actions: Locator, size: DialogSize, viewport: { width: number; height: number }, hasActions = true) {
  const sample = await sampleDialog(frame, actions)
  expect.soft(sample.frame.width, `dialog width within the ${size.toUpperCase()} step`).toBeLessThanOrEqual(DIALOG_SCALE[size] + 0.5)
  expect.soft(sample.frame.x, 'dialog starts inside the window').toBeGreaterThanOrEqual(-0.5)
  expect.soft(sample.frame.x + sample.frame.width, 'dialog ends inside the window').toBeLessThanOrEqual(viewport.width + 0.5)
  if (hasActions) expect.soft(sample.buttons.length, 'dialog has measurable actions').toBeGreaterThan(0)
  for (const button of sample.buttons) {
    expect.soft(button.width, `“${button.label}” is no wider than its label needs`).toBeLessThanOrEqual(Math.max(button.natural, 88) + 1)
    expect.soft(button.height, `“${button.label}” is a compact desktop button`).toBeLessThanOrEqual(36.5)
  }
  return sample
}

type Rect = { x: number; y: number; width: number; height: number }
export interface PhoneSheetGeometry {
  frame: Rect
  /** The action bar; null when it is not rendered. */
  bar: Rect | null
  /** The action bar locator resolved to the frame itself (or an ancestor). */
  barIsFrame: boolean
  buttons: { label: string; width: number; height: number; bottom: number }[]
}

/** The sheet's bottom padding (safe area excluded) may sit below its action bar. */
export const PHONE_BAR_INSET = 24

/** Everything wrong with a phone sheet: it covers the window, its action bar is
 * a real footer inside it, ending at its bottom edge and on screen, with at
 * least one visible touch target. Empty when the sheet is right. */
export function phoneSheetFindings({ frame, bar, barIsFrame, buttons }: PhoneSheetGeometry, viewport: { width: number; height: number }): string[] {
  const found: string[] = []
  if (frame.x > 0.5 || frame.x + frame.width < viewport.width - 0.5) found.push(`sheet spans ${frame.x}–${frame.x + frame.width}, not the window width ${viewport.width}`)
  if (frame.y > 0.5 || frame.y + frame.height < viewport.height - 0.5) found.push(`sheet covers ${frame.y}–${frame.y + frame.height}, not the window height ${viewport.height}`)
  if (barIsFrame) found.push('the action bar is the sheet itself; pass its footer')
  if (!bar || bar.width <= 0 || bar.height <= 0) found.push('the action bar has no size')
  else {
    const bottom = bar.y + bar.height, frameBottom = frame.y + frame.height
    if (bar.y < frame.y - 0.5 || bottom > frameBottom + 0.5) found.push('the action bar lies outside the sheet')
    if (frameBottom - bottom > PHONE_BAR_INSET + 0.5) found.push(`the action bar ends ${frameBottom - bottom}px above the sheet's bottom edge`)
    if (bottom > viewport.height + 0.5) found.push(`the action bar ends below the window (${bottom} > ${viewport.height})`)
  }
  if (!buttons.length) found.push('the action bar has no visible actions')
  for (const button of buttons) {
    if (button.width <= 0) found.push(`“${button.label}” has no width`)
    if (button.height < 43.5) found.push(`“${button.label}” is ${button.height}px, not a 44px touch target`)
    if (button.bottom > viewport.height + 0.5) found.push(`“${button.label}” ends below the window`)
  }
  return found
}

export interface PhoneSheetOptions {
  /** The sheet's scrolling body: scrolled to its end and back, and checked for horizontal overflow. */
  body: Locator
  /** Content changes (typing, filtering, copy feedback) the sheet and its actions must ride out. */
  changes?: StableInteraction[]
}

/** Phone (AEON-541 pattern B): a full-height sheet whose action bar is pinned
 * to the bottom edge. The sheet, the bar and its first action keep their exact
 * place while the content changes and the body scrolls. */
export async function expectPhoneSheet(frame: Locator, actions: Locator, viewport: { width: number; height: number }, { body, changes = [] }: PhoneSheetOptions) {
  const sample = await sampleDialog(frame, actions)
  await expect(actions).toHaveCount(1)
  const geometry: PhoneSheetGeometry = await actions.evaluate((bar, sheet) => {
    const rect = (element: Element) => { const r = element.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } }
    const buttons = [...bar.querySelectorAll<HTMLElement>('.btn, button.desk-btn')].filter(button => button.offsetParent !== null)
      .map(button => { const r = button.getBoundingClientRect(); return { label: button.textContent?.trim() ?? '', width: r.width, height: r.height, bottom: r.bottom } })
    return { frame: rect(sheet as Element), bar: bar.getClientRects().length ? rect(bar) : null, barIsFrame: bar.contains(sheet as Node), buttons }
  }, await frame.elementHandle())
  expect.soft(phoneSheetFindings(geometry, viewport), 'phone sheet').toEqual([])
  await expectStableControls({
    controls: { sheet: frame, 'action bar': actions, 'first action': actions.locator('.btn, button.desk-btn').first() },
    interactions: [
      ...changes,
      { name: 'scroll the body to its end', run: () => body.evaluate(element => { element.scrollTop = element.scrollHeight }) },
      { name: 'scroll the body back', run: () => body.evaluate(element => { element.scrollTop = 0 }) },
    ],
    scrollAreas: { body },
  })
  return sample
}
