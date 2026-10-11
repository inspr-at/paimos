// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1211 (design AEON-1196 §3.2): calm enter and leave. Risk: a refresh adds, removes or moves a
// row under the pointer; held changes never apply, or apply before the pointer has been away 1.5 s;
// an ended session vanishes at once instead of keeping its place for 10 minutes; the time column
// says the wrong thing. Timers are injected, never slept on.
import { describe, expect, it } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'
import { LINGER_MS, RELEASE_MS, frameOf, heldChanges, lingers, movesAt, reordered, settle, useSessionHold, type Structure } from '../src/components/agents/sessionHold'
import { timeShort, timeText, timeTip, timeValue } from '../src/components/agents/sessionTime'

type Row = { id: string; group: string; parent?: string; label?: string }
const structure: Structure<Row> = {
  id: row => row.id,
  key: row => `${row.group}|${row.parent ?? ''}`,
  // Tree order: roots in list order, each followed by its children.
  order: rows => rows.filter(r => !r.parent).flatMap(root => [root.id, ...rows.filter(r => r.parent === root.id).map(r => r.id)]),
}
const ids = (rows: readonly Row[]) => rows.map(r => r.id)

// A clock the test moves by hand.
function manualTimers() {
  let now = 0, serial = 0
  const queue = new Map<number, { at: number; fn: () => void }>()
  return {
    set: ((fn: () => void, ms: number) => { queue.set(++serial, { at: now + ms, fn }); return serial }) as unknown as typeof setTimeout,
    clear: ((id: number) => { queue.delete(id) }) as unknown as typeof clearTimeout,
    advance(ms: number) {
      now += ms
      for (const [id, task] of [...queue].sort((a, b) => a[1].at - b[1].at)) if (task.at <= now) { queue.delete(id); task.fn() }
    },
  }
}

describe('the frame', () => {
  const frame = frameOf([{ id: 'a', group: 'working' }, { id: 'b', group: 'working' }, { id: 'c', group: 'idle' }], structure)

  it('keeps members in place, a departed one as last seen, and counts every waiting change once', () => {
    const live: Row[] = [{ id: 'a', group: 'working', label: 'new step' }, { id: 'c', group: 'needs' }, { id: 'd', group: 'working' }]
    const settled = settle(frame, live, structure)
    // a: newest copy (same place); b: left, still shown as last seen; c: moved, held in its old group; d: waits.
    expect(settled).toEqual([{ id: 'a', group: 'working', label: 'new step' }, { id: 'b', group: 'working' }, { id: 'c', group: 'idle' }])
    expect([...heldChanges(frame, live, structure)].sort()).toEqual(['b', 'c', 'd'])
  })

  it('counts a new order as the fewest rows that moved', () => {
    const rank = new Map(['a', 'b', 'c', 'd', 'e'].map((id, i) => [id, i]))
    expect([...reordered(['a', 'b', 'c', 'd', 'e'], rank)]).toEqual([])
    expect([...reordered(['e', 'a', 'b', 'c', 'd'], rank)]).toEqual(['e'])
    expect([...reordered(['b', 'a', 'd', 'c', 'e'], rank)].length).toBe(2)
  })

  it('lets the person\'s own change through while the rest waits', () => {
    const live: Row[] = [{ id: 'a', group: 'working' }, { id: 'c', group: 'needs' }]
    const follows = new Set(['b'])
    expect(ids(settle(frame, live, structure, follows))).toEqual(['a', 'c'])
    expect([...heldChanges(frame, live, structure, follows)]).toEqual(['c'])
  })
})

describe('holding while the person points', () => {
  function setup() {
    const live = ref<Row[]>([{ id: 'a', group: 'working' }, { id: 'b', group: 'working' }])
    const pointing = ref(false)
    const timers = manualTimers()
    const scope = effectScope()
    const hold = scope.run(() => useSessionHold(() => live.value, structure, () => pointing.value, timers))!
    return { live, pointing, timers, hold, scope }
  }

  it('follows the list while nobody points, and holds structure while someone does', async () => {
    const { live, pointing, hold } = setup()
    live.value = [...live.value, { id: 'c', group: 'working' }]
    expect(ids(hold.views.value)).toEqual(['a', 'b', 'c'])
    pointing.value = true; await nextTick()
    live.value = [{ id: 'a', group: 'working', label: 'tests pass' }, { id: 'c', group: 'working' }, { id: 'd', group: 'working' }]
    // b left and d arrived: both wait; a's new line shows at once.
    expect(ids(hold.views.value)).toEqual(['a', 'b', 'c'])
    expect(hold.views.value[0]!.label).toBe('tests pass')
    expect(hold.pending.value).toBe(2)
    expect(hold.rank.value?.get('c')).toBe(2)
  })

  it('applies 1.5 s after the pointer leaves, and not if it comes back first', async () => {
    const { live, pointing, timers, hold } = setup()
    pointing.value = true; await nextTick()
    live.value = [...live.value, { id: 'c', group: 'working' }]
    pointing.value = false; await nextTick()
    timers.advance(RELEASE_MS - 1)
    expect(hold.pending.value).toBe(1)
    pointing.value = true; await nextTick()
    timers.advance(RELEASE_MS)
    expect(hold.pending.value, 'the pointer came back: still held').toBe(1)
    pointing.value = false; await nextTick()
    timers.advance(RELEASE_MS)
    expect(hold.pending.value).toBe(0)
    expect(ids(hold.views.value)).toEqual(['a', 'b', 'c'])
    expect(hold.rank.value).toBeNull()
  })

  it('Show now applies at once and holds what comes after', async () => {
    const { live, pointing, hold } = setup()
    pointing.value = true; await nextTick()
    live.value = [{ id: 'a', group: 'working' }]
    expect(hold.pending.value).toBe(1)
    hold.showNow()
    expect(ids(hold.views.value)).toEqual(['a'])
    expect(hold.pending.value).toBe(0)
    live.value = [{ id: 'a', group: 'working' }, { id: 'e', group: 'working' }]
    expect(ids(hold.views.value)).toEqual(['a'])
    expect(hold.pending.value).toBe(1)
  })

  it('applies the person\'s own removal at once', async () => {
    const { live, pointing, hold } = setup()
    pointing.value = true; await nextTick()
    hold.follow(['b'])
    live.value = [{ id: 'a', group: 'working' }]
    expect(ids(hold.views.value)).toEqual(['a'])
    expect(hold.pending.value).toBe(0)
  })

  it('cancels its timer when its scope ends', async () => {
    const { live, pointing, timers, hold, scope } = setup()
    pointing.value = true; await nextTick()
    live.value = [{ id: 'a', group: 'working' }]
    pointing.value = false; await nextTick()
    scope.stop()
    timers.advance(RELEASE_MS)
    expect(hold.rank.value, 'no release ran after the list unmounted').not.toBeNull()
  })
})

describe('ended sessions keep their place for 10 minutes', () => {
  const now = Date.parse('2026-10-11T10:00:00Z')
  const endedAgo = (ms: number) => ({ stopped_at: new Date(now - ms).toISOString(), archived_at: null })
  it('lingers under 10 minutes, never once removed', () => {
    expect(lingers(endedAgo(0), now)).toBe(true)
    expect(lingers(endedAgo(LINGER_MS - 1), now)).toBe(true)
    expect(lingers(endedAgo(LINGER_MS), now)).toBe(false)
    expect(lingers({ ...endedAgo(1000), archived_at: new Date(now).toISOString() }, now)).toBe(false)
    expect(lingers({ stopped_at: null }, now)).toBe(false)
    expect(movesAt(endedAgo(60_000))).toBe(now - 60_000 + LINGER_MS)
  })
})

describe('the Time column', () => {
  const now = Date.parse('2026-10-11T10:00:00Z')
  const at = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
  const view = (state: string, session: Record<string, unknown>) => ({ status: { state }, session: { created_at: at(72), stopped_at: null, heartbeat_at: at(0.1), ...session } }) as Parameters<typeof timeText>[0]
  it('says running time, time since the end, and since when it rests', () => {
    const running = view('working', {})
    expect([timeText(running, now), timeShort(running, now), timeTip(running, now)]).toEqual(['1 h 12 min', '1h 12m', 'Running 1 h 12 min'])
    const done = view('done', { stopped_at: at(12) })
    expect([timeText(done, now), timeShort(done, now), timeTip(done, now)]).toEqual(['12 min ago', '12m ago', 'Done 12 min ago · ran 1 h'])
    const ended = view('stopped', { stopped_at: at(3) })
    expect(timeTip(ended, now)).toBe('Ended 3 min ago · ran 1 h 9 min')
    const paused = view('paused', { stopped_at: at(30), pause: { paused_at: at(31) } })
    expect(timeText(paused, now)).toMatch(/^since \d{1,2}:\d{2}/)
    expect(timeTip(paused, now)).toMatch(/^Paused since /)
    const silent = view('unresponsive', { heartbeat_at: at(0.67) })
    expect(timeTip(silent, now)).toBe('No heartbeat for 40 s · running 1 h 12 min')
    // The Time sort orders by what the column shows.
    expect(timeValue(done, now)).toBe(12 * 60_000)
    expect(timeValue(running, now)).toBe(72 * 60_000)
  })
})
