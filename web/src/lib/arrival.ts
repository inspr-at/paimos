// SPDX-License-Identifier: AGPL-3.0-only
// A deep link scrolls its target to the top once it is on the page. Data that
// arrives after that scroll can grow the content above it and push the target
// off-screen, so the arrival holds the target in place for a short while: when
// the layout moves it, it scrolls there again. The hold is bounded and lets go
// at once when the person, or other code, scrolls or acts (AEON-1365).

/** Where the target sits in its scroll box, in that box's content coordinates. */
export interface ArrivalSpot {
  box: unknown
  /** The box's scroll position now. */
  scroll: number
  /** The target's top in the box's content: layout only, scrolling never changes it. */
  offset: number
  /** The scroll position that puts the target at the top, within what the box can scroll. */
  goal: number
}

/** The page as the hold sees it; arrivalPage() is the browser, tests pass their own. */
export interface ArrivalPage {
  /** null once the target has left the page. */
  measure(): ArrivalSpot | null
  /** Brings the target to the top of its scroll box. */
  scroll(): void
  /** Calls back on layout changes and on scrolling; returns the way to stop. */
  watch(changed: () => void): () => void
  /** Calls back when the person acts: wheel, touch, pointer or key. */
  person(took: () => void): () => void
  later(run: () => void, ms: number): () => void
  now(): number
}

// Long enough for a slow first read of the page's data, short enough that a late
// change elsewhere never pulls the page back; the ring of an arrival shows 1.8 s.
export const ARRIVAL_HOLD_MS = 3000
// A scroll that leaves the target this much further from its spot is not ours.
const SLACK = 4
// A smooth scroll moves within a frame or two. One that has not moved this long
// after it started, with somewhere to go, was cancelled by someone else's scroll.
const STALL_MS = 400
const moved = (a: number, b: number) => Math.abs(a - b) > 0.5

/** Scrolls to the target and holds it there; returns the way to let go early. */
export function arrive(page: ArrivalPage, holdMs = ARRIVAL_HOLD_MS): () => void {
  let last: ArrivalSpot | null = null, placed = { scroll: 0, at: 0 }, done = false
  const undo: (() => void)[] = []
  const stop = () => {
    if (done) return
    done = true
    for (const step of undo.splice(0)) step()
  }
  const place = () => {
    page.scroll(); last = page.measure()
    placed = { scroll: last?.scroll ?? 0, at: page.now() }
  }
  const check = () => {
    if (done) return
    const spot = page.measure()
    if (!spot || !last) return stop()
    // The layout moved the target, or changed how far its box can scroll: back to it,
    // unless our scroll there never started because someone else's scroll stopped it.
    if (spot.box !== last.box || moved(spot.offset, last.offset) || moved(spot.goal, last.goal)) {
      const stalled = last.scroll === placed.scroll && Math.abs(last.goal - last.scroll) > SLACK && page.now() - placed.at > STALL_MS
      return stalled ? stop() : place()
    }
    // Our own scroll only ever brings the target closer. A scroll that takes it away
    // is the person's or other code's: from then on the page is theirs.
    if (Math.abs(spot.goal - spot.scroll) > Math.abs(last.goal - last.scroll) + SLACK) return stop()
    last = spot
  }
  place()
  undo.push(page.later(stop, holdMs), page.person(stop), page.watch(check))
  return stop
}

const PERSON = ['wheel', 'touchstart', 'pointerdown', 'keydown'] as const

function scrollBox(target: HTMLElement): HTMLElement {
  const root = (document.scrollingElement ?? document.documentElement) as HTMLElement
  for (let element = target.parentElement; element && element !== root && element !== document.body; element = element.parentElement) {
    const overflow = getComputedStyle(element).overflowY
    if ((overflow === 'auto' || overflow === 'scroll' || overflow === 'overlay') && element.scrollHeight > element.clientHeight) return element
  }
  return root
}

/** The browser side of arrive(): the target's scroll box, resize and scroll events, the person's input. */
export function arrivalPage(target: HTMLElement, behavior: ScrollBehavior): ArrivalPage {
  return {
    measure() {
      if (!target.isConnected) return null
      const box = scrollBox(target), root = box === document.scrollingElement || box === document.documentElement
      const top = root ? 0 : box.getBoundingClientRect().top + box.clientTop
      const offset = target.getBoundingClientRect().top - top + box.scrollTop
      const margin = (parseFloat(getComputedStyle(target).scrollMarginTop) || 0) + (parseFloat(getComputedStyle(box).scrollPaddingTop) || 0)
      const goal = Math.min(Math.max(offset - margin, 0), Math.max(box.scrollHeight - box.clientHeight, 0))
      return { box, scroll: box.scrollTop, offset, goal }
    },
    scroll: () => target.scrollIntoView({ block: 'start', behavior }),
    watch(changed) {
      // The target moves when anything before it in the flow changes size: its
      // ancestors and their earlier siblings.
      const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(() => changed()) : null
      for (let element: Element | null = target; element && element !== document.documentElement; element = element.parentElement) {
        for (let item: Element | null = element; item; item = item.previousElementSibling) observer?.observe(item)
      }
      window.addEventListener('scroll', changed, { capture: true, passive: true })
      return () => { observer?.disconnect(); window.removeEventListener('scroll', changed, { capture: true }) }
    },
    person(took) {
      for (const type of PERSON) window.addEventListener(type, took, { capture: true, passive: true })
      return () => { for (const type of PERSON) window.removeEventListener(type, took, { capture: true }) }
    },
    later(run, ms) {
      const timer = setTimeout(run, ms)
      return () => clearTimeout(timer)
    },
    now: () => performance.now(),
  }
}
