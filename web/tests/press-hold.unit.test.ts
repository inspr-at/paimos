// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { HOLD_DELAYS, holdDelay, startHold, type HoldClock } from '../src/lib/pressHold'

// A manual clock: each pending timer runs only when the test advances it.
function manualClock() {
  let now = 0, seq = 0
  const timers = new Map<number, { at: number; run: () => void }>()
  const waits: number[] = []
  const clock: HoldClock = {
    set: (run, ms) => { waits.push(ms); timers.set(++seq, { at: now + ms, run }); return seq },
    clear: handle => { timers.delete(handle as number) },
  }
  function advance(ms: number) {
    const until = now + ms
    for (;;) {
      const next = [...timers.entries()].sort((a, b) => a[1].at - b[1].at)[0]
      if (!next || next[1].at > until) break
      timers.delete(next[0]); now = next[1].at; next[1].run()
    }
    now = until
  }
  return { clock, advance, waits, pending: () => timers.size }
}

describe('press and hold on − and +', () => {
  it('steps once at once, waits 450 ms, then speeds up by about 0.8 to a 90 ms floor', () => {
    const { clock, advance, waits } = manualClock()
    let steps = 0
    startHold(() => { steps++ }, () => true, () => {}, clock)
    expect(steps).toBe(1)
    advance(449); expect(steps).toBe(1)
    advance(1); expect(steps).toBe(2)
    advance(360 + 290 + 230 + 185 + 150 + 120 + 100 + 90 + 90 + 90)
    expect(steps).toBe(12)
    expect(waits.slice(0, 12)).toEqual([450, 360, 290, 230, 185, 150, 120, 100, 90, 90, 90, 90])
    expect(HOLD_DELAYS.every((ms, i) => i === 0 || ms <= HOLD_DELAYS[i - 1]! && ms >= HOLD_DELAYS[i - 1]! * .75)).toBe(true)
    expect(holdDelay(99)).toBe(90)
  })

  it('a smart stop ends the hold before the step that would cross, and needs a fresh press', () => {
    const { clock, advance, pending } = manualClock()
    let value = 27, stops = 0
    // 30 → ∞ is a boundary: repeating pauses at 30.
    startHold(() => { value++ }, () => value + 1 <= 30, () => { stops++ }, clock)
    advance(10_000)
    expect(value).toBe(30)
    expect(stops).toBe(1)
    expect(pending()).toBe(0)
    // A fresh press crosses: its first step happens at once, whatever canRepeat says.
    startHold(() => { value++ }, () => false, () => { stops++ }, clock)
    expect(value).toBe(31)
    advance(450)
    expect(value).toBe(31)
    expect(stops).toBe(2)
  })

  it('release ends the hold at once, runs onStop exactly once and leaves no timer behind', () => {
    const { clock, advance, pending } = manualClock()
    let steps = 0, stops = 0
    const stop = startHold(() => { steps++ }, () => true, () => { stops++ }, clock)
    advance(450 + 360)
    expect(steps).toBe(3)
    stop(); stop()
    expect(stops).toBe(1)
    expect(pending()).toBe(0)
    advance(5_000)
    expect(steps).toBe(3)
  })

  it('a stop requested from inside a step schedules nothing more', () => {
    const { clock, advance, pending } = manualClock()
    let steps = 0
    const stop: { fn?: () => void } = {}
    stop.fn = startHold(() => { steps++; if (steps === 2) stop.fn?.() }, () => true, () => {}, clock)
    advance(450)
    expect(steps).toBe(2)
    expect(pending()).toBe(0)
  })
})
