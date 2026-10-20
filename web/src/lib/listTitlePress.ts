// SPDX-License-Identifier: AGPL-3.0-only
// A ctrl/cmd press on a list title opens one tab even when the row moves
// before the browser's click lands (AEON-1327, AEON-1460). The native default
// is dropped in that case. Shift, alt and middle click stay with the caller.

export const LIST_TITLE_PRESS_SLOP = 8

export interface ListTitlePress {
  id: number
  x: number
  y: number
  href: string
  released: boolean
  armed: boolean
  opened: boolean
}

interface PressPoint { button: number; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean; defaultPrevented: boolean; pointerId: number; clientX: number; clientY: number }

export function modifiedTitlePress(event: { button: number; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean }): boolean {
  return event.button === 0 && (event.metaKey || event.ctrlKey) && !event.shiftKey && !event.altKey
}

export function appTitleHref(href: string | null | undefined): href is string {
  return !!href && href.startsWith('/') && !href.startsWith('//')
}

export function beginTitlePress(event: PressPoint, href: string | null | undefined): ListTitlePress | null {
  if (event.defaultPrevented || !modifiedTitlePress(event) || !appTitleHref(href)) return null
  return { id: event.pointerId, x: event.clientX, y: event.clientY, href, released: false, armed: false, opened: false }
}

/** The href the press showed when the pointer stayed, or null when it dragged away or was not a modifier press. Does not open: the caller opens once, and a click that also lands sees `opened`. */
export function releaseTitlePress(press: ListTitlePress | null, event: PressPoint): string | null {
  if (!press || press.released || press.id !== event.pointerId) return null
  press.released = true
  if (event.defaultPrevented || !modifiedTitlePress(event) || press.opened) return null
  if (Math.hypot(event.clientX - press.x, event.clientY - press.y) > LIST_TITLE_PRESS_SLOP) return null
  press.armed = true
  return press.href
}

/** The href to open the first time. A press released by a drag stays closed. */
export function openTitlePress(press: ListTitlePress | null): string | null {
  if (!press || press.opened || (press.released && !press.armed)) return null
  press.opened = true
  return press.href
}

/** A modifier click that belongs to a press cancels the native default. It returns the href only when the press has not opened yet. A keyboard activation (detail 0, no press) leaves the browser's own tab. */
export function claimTitleClick(press: ListTitlePress | null, event: { button: number; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean; defaultPrevented: boolean; detail: number; preventDefault: () => void }): string | null {
  if (!modifiedTitlePress(event)) return null
  const started = event.detail > 0 ? press : null
  if (!started || event.defaultPrevented) return null
  event.preventDefault()
  return openTitlePress(started)
}
