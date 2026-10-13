// SPDX-License-Identifier: AGPL-3.0-only
// Live updates without moving the reader (AEON-1279). WebKit has no scroll anchoring, so a row
// inserted, removed or resized above the screen moves everything on it, and a phone shows that as
// a scroll running away under the finger. Two parts:
// - touchScroll: whether a finger is on the screen or the scroll it started still runs (momentum).
//   It starts with the touch and ends at scrollend, or after a short quiet spell where the browser
//   has no scrollend (Safari). Lists hold their live changes while it runs.
// - readerAnchor: remember the first rows on screen before an update and, after it, scroll by
//   however far they moved, so they stay where the reader saw them. At the very top nothing is
//   out of view: new rows show where they belong.
import { onBeforeUpdate, onScopeDispose, onUpdated, ref, type Ref } from 'vue'

/** How long a scroll may be quiet before it counts as settled where scrollend never comes. */
export const SETTLE_MS = 180

export interface ScrollTimers { set: (run: () => void, ms: number) => unknown; clear: (handle: unknown) => void }
// Called as functions, never as methods: a browser's setTimeout refuses another `this`.
const browserTimers: ScrollTimers = { set: (run, ms) => setTimeout(run, ms), clear: handle => clearTimeout(handle as ReturnType<typeof setTimeout>) }

export interface TouchScroll { active: Ref<boolean>; stop: () => void }

/** Touch activity on `target` (the window by default): a finger down, or the scroll it started still moving. */
export function touchScroll(target: Pick<EventTarget, 'addEventListener' | 'removeEventListener'> | undefined = typeof window === 'undefined' ? undefined : window, timers: ScrollTimers = browserTimers): TouchScroll {
  const active = ref(false)
  let fingers = 0, quiet: unknown
  const cancel = () => { if (quiet !== undefined) timers.clear(quiet); quiet = undefined }
  const settle = () => { cancel(); quiet = timers.set(() => { quiet = undefined; if (!fingers) active.value = false }, SETTLE_MS) }
  const down = (event: Event) => { fingers = (event as TouchEvent).touches?.length ?? 1; cancel(); active.value = true }
  const up = (event: Event) => { fingers = (event as TouchEvent).touches?.length ?? 0; if (!fingers && active.value) settle() }
  // Momentum keeps scrolling after the finger lifts: every scroll restarts the quiet spell.
  const scrolled = () => { if (active.value && !fingers) settle() }
  const ended = () => { if (active.value && !fingers) { cancel(); active.value = false } }
  const options = { capture: true, passive: true } as const
  const listeners: [string, (event: Event) => void][] = [['touchstart', down], ['touchend', up], ['touchcancel', up], ['scroll', scrolled], ['scrollend', ended]]
  for (const [type, run] of listeners) target?.addEventListener(type, run, options)
  const stop = () => { cancel(); for (const [type, run] of listeners) target?.removeEventListener(type, run, options) }
  return { active, stop }
}

/** touchScroll for a component: stops with its scope. */
export function useTouchScroll(target?: Pick<EventTarget, 'addEventListener' | 'removeEventListener'>, timers?: ScrollTimers): Ref<boolean> {
  const touch = touchScroll(target, timers)
  onScopeDispose(touch.stop)
  return touch.active
}

export interface ReaderAnchorOptions {
  /** The element that scrolls. */
  scroller: () => HTMLElement | null | undefined
  /** The rows that may anchor, in document order. Never a sticky one: it stays put while the rest moves. */
  rows: () => Iterable<HTMLElement> | ArrayLike<HTMLElement> | null | undefined
  /** Where the visible part starts below sticky heads; never above the scroller's top. */
  top?: () => number
}
export interface ReaderAnchor {
  /** Before the update: remember the first rows on screen and where they are. A second capture
   *  before the restore keeps the first: a component can update twice in one flush. */
  capture: () => void
  /** After the update: scroll so the first remembered row still there is where it was. */
  restore: () => void
}

// A few rows, so the reader stays put even when the first one leaves.
const CANDIDATES = 4

export function readerAnchor(options: ReaderAnchorOptions): ReaderAnchor {
  let marks: { el: HTMLElement; top: number }[] = []
  return {
    capture() {
      if (marks.length) return
      const scroller = options.scroller()
      if (!scroller || scroller.scrollTop <= 0) return
      const edge = Math.max(scroller.getBoundingClientRect().top, options.top?.() ?? -Infinity)
      for (const el of Array.from(options.rows() ?? [])) {
        const box = el.getBoundingClientRect()
        if (box.height <= 0 || box.bottom <= edge) continue
        marks.push({ el, top: box.top })
        if (marks.length === CANDIDATES) break
      }
      // A mark never outlives its flush: an update that never restores cannot leave a stale one.
      if (marks.length) queueMicrotask(() => { marks = [] })
    },
    restore() {
      const scroller = options.scroller(), mark = marks.find(m => m.el.isConnected)
      marks = []
      if (!scroller || !mark) return
      const moved = mark.el.getBoundingClientRect().top - mark.top
      if (Math.abs(moved) >= 0.5) scroller.scrollTop += moved
    },
  }
}

/** readerAnchor around every update of the calling component. */
export function useReaderAnchor(options: ReaderAnchorOptions): ReaderAnchor {
  const anchor = readerAnchor(options)
  onBeforeUpdate(anchor.capture)
  onUpdated(anchor.restore)
  return anchor
}
