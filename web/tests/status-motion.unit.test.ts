// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1362: the status one-offs follow design r4b §7.2 (marks only, compositor-cheap properties, ≤ 600 ms), replay
// cleanly, are skipped with reduced motion, and every status has one bar fill.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

type Animated = { id: string; cancel: ReturnType<typeof vi.fn> }
function icon(status: string, parts: string[]) {
  const started: { part: string; keyframes: Keyframe[]; options: KeyframeAnimationOptions }[] = []
  const running: Animated[] = []
  const element = {
    classList: { contains: (name: string) => ['status-icon', `st-${status}`].includes(name) },
    getAnimations: () => running.slice(),
    querySelector: (part: string) => parts.includes(part) ? {
      animate: (keyframes: Keyframe[], options: KeyframeAnimationOptions) => {
        started.push({ part, keyframes, options })
        const animation = { id: options.id!, cancel: vi.fn() }
        running.push(animation)
        return animation
      },
    } : null,
  }
  return { element: element as unknown as Element, started, running }
}
const reduce = (matches: boolean) => vi.stubGlobal('matchMedia', (query: string) => ({ matches: matches && query === '(prefers-reduced-motion: reduce)' }))
beforeEach(() => vi.resetModules())
afterEach(() => vi.unstubAllGlobals())

it('every status but In progress has a one-off on its marks, with the design timings and cheap properties only', async () => {
  const { ONE_OFFS, ONE_OFF_EASE } = await import('../src/lib/statusMotion')
  const timings = Object.fromEntries(Object.entries(ONE_OFFS).map(([status, steps]) => [status, steps!.map(step => [step.part, step.delay, step.duration])]))
  expect(timings).toEqual({
    done: [['.tick', 0, 480]], delivered: [['.tick', 0, 480]], accepted: [['.tick', 0, 300], ['.tick2', 200, 320]], cancelled: [['.slash', 0, 460]],
    qa: [['.pulse', 0, 560]], new: [['.dot', 0, 520]], open: [['.glint', 0, 520]], backlog: [['.dots', 0, 520]], blocked: [['.dashes', 0, 420]], archived: [['.sq', 0, 420]],
  })
  expect(ONE_OFFS.progress).toBeUndefined()
  expect(ONE_OFF_EASE).toBe('cubic-bezier(.22, .8, .3, 1)')
  for (const steps of Object.values(ONE_OFFS)) for (const step of steps!) {
    expect(step.delay + step.duration).toBeLessThanOrEqual(600)
    // Rings, discs and ticks' backgrounds never animate: only the marks, the dots/dashes turning in place and the settling square.
    expect(['.ring', '.disc']).not.toContain(step.part)
    for (const frame of step.keyframes) for (const property of Object.keys(frame)) expect(['offset', 'opacity', 'strokeDashoffset', 'transform']).toContain(property)
    const first = step.keyframes[0], last = step.keyframes.at(-1)!
    // Draws start from nothing and end whole; turns and pulses end on the static icon.
    if ('strokeDashoffset' in first && step.part !== '.glint') { expect(first.strokeDashoffset).toBe(1); expect(last.strokeDashoffset).toBe(0) }
    if ('transform' in last) expect(['rotate(0deg)', 'scale(1)', 'scale(1.5)']).toContain(last.transform)
  }
})

it('a one-off starts every part once, eased and without a lasting fill, and a replay cancels the running one', async () => {
  reduce(false)
  const { playOneOff, ONE_OFF_ID } = await import('../src/lib/statusMotion')
  const accepted = icon('accepted', ['.disc', '.tick', '.tick2'])
  expect(playOneOff(accepted.element)).toBe(2)
  expect(accepted.started.map(({ part, options }) => [part, options.delay, options.duration, options.fill, options.id, options.easing])).toEqual([
    ['.tick', 0, 300, 'backwards', ONE_OFF_ID, 'cubic-bezier(.22, .8, .3, 1)'], ['.tick2', 200, 320, 'backwards', ONE_OFF_ID, 'cubic-bezier(.22, .8, .3, 1)'],
  ])
  const first = accepted.running.slice()
  expect(playOneOff(accepted.element)).toBe(2)
  for (const animation of first) expect(animation.cancel).toHaveBeenCalledTimes(1)
  expect(accepted.started).toHaveLength(4)
  const progress = icon('progress', [])
  expect(playOneOff(progress.element)).toBe(0)
})

it('reduced motion skips every one-off', async () => {
  reduce(true)
  const { playOneOff } = await import('../src/lib/statusMotion')
  const done = icon('done', ['.tick'])
  expect(playOneOff(done.element)).toBe(0)
  expect(done.started).toEqual([])
})

it('every status and spelling has one bar fill read from its icon', async () => {
  const { statusFill } = await import('../src/lib/statusMotion')
  expect(['new', 'backlog', 'open', 'blocked', 'in-progress', 'in_progress', 'active', 'qa', 'done', 'delivered', 'accepted', 'cancelled', 'canceled', 'archived', 'someday'].map(statusFill)).toEqual([
    'f-ring fc-new', 'f-dotted fc-backlog', 'f-ring fc-open', 'f-dashed fc-blocked', 'f-diag fc-progress', 'f-diag fc-progress', 'f-diag fc-progress', 'f-halo fc-qa',
    'f-tint fc-done', 'f-solid fc-delivered', 'f-deep fc-accepted', 'f-cancel fc-cancelled', 'f-cancel fc-cancelled', 'f-square fc-archived', 'f-ring fc-other',
  ])
})
