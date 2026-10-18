// SPDX-License-Identifier: AGPL-3.0-only
// Press and hold on − and + (AEON-781): like a remote's volume button. One step
// at once, then repeats that start readable and speed up to a floor that still
// lets the number be read. The caller decides where a hold must pause.

/** Wait before each repeat: 450 ms until the first one, then about ×0.8 per step down to 90 ms. */
export const HOLD_DELAYS = [450, 360, 290, 230, 185, 150, 120, 100, 90] as const
export const holdDelay = (repeat: number) => HOLD_DELAYS[Math.min(Math.max(0, repeat), HOLD_DELAYS.length - 1)]!

export interface HoldClock { set(run: () => void, ms: number): unknown; clear(handle: unknown): void }
export const browserClock: HoldClock = {
  set: (run, ms) => setTimeout(run, ms),
  clear: handle => clearTimeout(handle as ReturnType<typeof setTimeout>),
}

/**
 * Steps once now, then keeps stepping while held. Before every repeat,
 * canRepeat() may end the hold (a ladder end, or a boundary that needs a fresh
 * press). Returns stop(), which is safe to call more than once; onStop runs once.
 */
export function startHold(step: () => void, canRepeat: () => boolean, onStop: () => void, clock: HoldClock = browserClock): () => void {
  let live = true, handle: unknown
  const stop = () => {
    if (!live) return
    live = false; clock.clear(handle); onStop()
  }
  const wait = (repeat: number) => {
    handle = clock.set(() => {
      if (!live) return
      if (!canRepeat()) return stop()
      step()
      if (live) wait(repeat + 1)
    }, holdDelay(repeat))
  }
  step()
  if (live) wait(0)
  return stop
}
