// SPDX-License-Identifier: AGPL-3.0-only
// How a view's small menu opens (AEON-1453): right-click on desktop, a long press on
// touch, and Shift+F10 or the context-menu key on a focused segment. A completed hold
// consumes its own click, so holding a segment never also switches to it. No hover hint.
import { onScopeDispose } from 'vue'

export const HOLD_MS = 500
const SLOP = 8

export function isMenuKey(event: KeyboardEvent) {
  return event.key === 'ContextMenu' || (event.key === 'F10' && event.shiftKey && !event.altKey && !event.ctrlKey && !event.metaKey)
}

export function useViewMenuTrigger(open: (id: string, anchor: HTMLElement) => void, schedule: { set: typeof setTimeout; clear: typeof clearTimeout } = { set: setTimeout, clear: clearTimeout }) {
  let press: { pointerId: number; x: number; y: number; timer: ReturnType<typeof setTimeout> } | undefined
  let held: HTMLElement | undefined
  function cancel() { if (press) schedule.clear(press.timer); press = undefined }
  function pointerdown(event: PointerEvent, id: string) {
    cancel()
    held = undefined
    if ((event.pointerType !== 'touch' && event.pointerType !== 'pen') || event.isPrimary === false || event.button !== 0) return
    const element = event.currentTarget as HTMLElement
    const started = { pointerId: event.pointerId, x: event.clientX, y: event.clientY, timer: undefined as unknown as ReturnType<typeof setTimeout> }
    started.timer = schedule.set(() => {
      if (press !== started) return
      press = undefined
      if (!element.isConnected) return
      held = element
      open(id, element)
    }, HOLD_MS)
    press = started
  }
  function pointermove(event: PointerEvent) {
    if (press?.pointerId === event.pointerId && Math.hypot(event.clientX - press.x, event.clientY - press.y) > SLOP) cancel()
  }
  function pointerend(event: PointerEvent) { if (press?.pointerId === event.pointerId) cancel() }
  // True when this click is the compatibility click of a completed hold.
  function heldClick(event: MouseEvent) {
    const was = held
    held = undefined
    if (!was || event.detail === 0 || !event.target || !was.contains(event.target as Node)) return false
    event.preventDefault()
    return true
  }
  function contextmenu(event: MouseEvent, id: string) {
    event.preventDefault()
    // Touch browsers also send contextmenu for the hold that already opened the menu.
    if (held && held === event.currentTarget) return
    open(id, event.currentTarget as HTMLElement)
  }
  function keydown(event: KeyboardEvent, id: string, anchor = event.currentTarget as HTMLElement) {
    if (!isMenuKey(event)) return false
    event.preventDefault()
    event.stopPropagation()
    open(id, anchor)
    return true
  }
  onScopeDispose(cancel)
  return { pointerdown, pointermove, pointerend, heldClick, contextmenu, keydown }
}
