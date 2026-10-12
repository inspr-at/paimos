// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1211 (design AEON-1196 §3.2): calm enter and leave. Risk: a refresh adds, removes or moves a
// row under the pointer; held changes never apply, or apply before the pointer has been away 1.5 s;
// an ended session vanishes at once instead of keeping its place for 10 minutes; the time column
// says the wrong thing. Timers are injected, never slept on.
import { describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'
import { LINGER_MS, RELEASE_MS, frameOf, heldChanges, lingers, movesAt, reordered, settle, useSessionHold, type Structure } from '../src/components/agents/sessionHold'
import { timeShort, timeText, timeTip, timeValue } from '../src/components/agents/sessionTime'
import { projectGroups } from '../src/components/agents/sessionGroups'
import type { ProjectLead } from '../src/lib/lead'

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
    set: (fn: () => void, ms: number) => { queue.set(++serial, { at: now + ms, fn }); return serial },
    clear: (id: unknown) => { queue.delete(id as number) },
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

  it('calls the platform timers as plain functions: a browser refuses setTimeout with another this', async () => {
    const receivers: unknown[] = []
    vi.stubGlobal('setTimeout', function (this: unknown) { receivers.push(this); return 1 })
    vi.stubGlobal('clearTimeout', function (this: unknown) { receivers.push(this) })
    try {
      const live = ref<Row[]>([{ id: 'a', group: 'working' }]), pointing = ref(true)
      const scope = effectScope()
      scope.run(() => useSessionHold(() => live.value, structure, () => pointing.value))
      await nextTick()
      pointing.value = false; await nextTick()
      pointing.value = true; await nextTick()
      scope.stop()
      expect(receivers.length, 'the release was scheduled and cancelled').toBeGreaterThanOrEqual(2)
      expect(receivers.every(receiver => receiver === undefined || receiver === globalThis)).toBe(true)
    } finally { vi.unstubAllGlobals() }
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

describe('a filter match is part of a row\'s place', () => {
  // Risk (gate on 49f7335a): the hold keeps a row's group and parent but the list judges its filter
  // match on the newest model names, so a rename in the registry adds or removes a row under the pointer.
  function setup() {
    const names = ref<Record<string, string>>({ a: 'Opus', b: 'Sol', c: 'Fable' })
    const query = ref('opus')
    const filtered: Structure<Row> = { ...structure, match: row => names.value[row.id]!.toLowerCase().includes(query.value) }
    const live = ref<Row[]>([{ id: 'a', group: 'working' }, { id: 'b', group: 'working' }, { id: 'c', group: 'idle' }])
    const pointing = ref(false), timers = manualTimers()
    const hold = effectScope().run(() => useSessionHold(() => live.value, filtered, () => pointing.value, timers))!
    const matched = () => ['a', 'b', 'c'].map(id => hold.matched(id))
    return { names, query, pointing, timers, hold, matched }
  }

  it('keeps the match each row froze with through a rename, until the release', async () => {
    const { names, pointing, timers, hold, matched } = setup()
    expect(matched(), 'nothing holds: the list judges the newest names').toEqual([undefined, undefined, undefined])
    pointing.value = true; await nextTick()
    // The registry renames two models while the person points: a stops matching, c starts.
    names.value = { a: 'Lyra', b: 'Sol', c: 'Opus' }
    expect(matched()).toEqual([true, false, false])
    expect(ids(hold.views.value)).toEqual(['a', 'b', 'c'])
    expect(hold.pending.value, 'both new matches wait').toBe(2)
    pointing.value = false; await nextTick()
    timers.advance(RELEASE_MS)
    expect(matched()).toEqual([undefined, undefined, undefined])
    expect(hold.pending.value).toBe(0)
  })

  it('freezes the newest matches on Show now, the person\'s own search, and lets a followed row through', async () => {
    const { names, query, pointing, hold, matched } = setup()
    pointing.value = true; await nextTick()
    names.value = { a: 'Lyra', b: 'Sol', c: 'Opus' }
    hold.showNow()
    expect(matched()).toEqual([false, false, true])
    expect(hold.pending.value).toBe(0)
    // The list calls Show now when the person changes the search (SessionList.vue).
    query.value = 'sol'
    expect(matched(), 'the frame keeps what it froze until Show now').toEqual([false, false, true])
    expect(hold.pending.value).toBe(2)
    hold.showNow()
    expect(matched()).toEqual([false, true, false])
    hold.follow(['b'])
    expect(hold.matched('b'), 'the person\'s own change follows the newest copy').toBeUndefined()
  })
})

describe('project headings share the held session structure', () => {
  // Risk (fix6): independent lead/queue reads insert a heading above a held session,
  // although no session id changed and the old pending count stayed zero.
  function setup() {
    const live = ref<Row[]>([{ id: 'a', group: 'working', label: 'old activity' }])
    const pointing = ref(false), timers = manualTimers(), scope = effectScope()
    const lead = ref<ProjectLead>({ project_id: 'p-pharos', state: 'working', reason: '', revision: 1, generation: 1, session_id: 'a', process_active: true })
    const queued = ref(0)
    const layout = {
      read: (views: readonly Row[]) => projectGroups({
        roots: views.map(row => ({ view: { session: { id: row.id, project_id: 'p-pharos', role: 'coordinator' }, projectKey: 'PHAROS', projectTitle: 'Pharos' } })),
        projects: [{ id: 'p-aeon', key: 'AEON', title: 'Aeon' }, { id: 'p-pharos', key: 'PHAROS', title: 'Pharos' }],
        lead: id => id === 'p-pharos' ? { ...lead.value } : { ...lead.value, project_id: id, state: 'none', session_id: null, process_active: false },
        queued: id => id === 'p-aeon' ? queued.value : 0,
        lines: true,
      }).groups,
      headings: (groups: ReturnType<typeof projectGroups>['groups']) => groups.map(g => ({ id: g.project.id, key: g.line?.kind ?? 'session' })),
    }
    const hold = scope.run(() => useSessionHold(() => live.value, structure, () => pointing.value, timers, layout))!
    return { live, pointing, timers, scope, lead, queued, hold }
  }

  it('keeps a queued-work heading out until Show now, then holds its departure until release', async () => {
    const { live, pointing, timers, scope, queued, hold } = setup()
    try {
      pointing.value = true; await nextTick()
      queued.value = 1
      live.value = [{ ...live.value[0]!, label: 'new activity' }]
      expect(hold.pending.value, 'the new no-lead heading is a waiting change').toBe(1)
      expect(hold.layout.value!.map(g => g.project.id)).toEqual(['p-pharos'])
      expect(ids(hold.views.value)).toEqual(['a'])
      expect(hold.views.value[0]!.label, 'row evidence stays live').toBe('new activity')
      hold.showNow()
      expect(hold.layout.value!.map(g => g.project.id)).toEqual(['p-aeon', 'p-pharos'])
      expect(hold.pending.value).toBe(0)
      queued.value = 0
      expect(hold.pending.value).toBe(1)
      pointing.value = false; await nextTick()
      timers.advance(RELEASE_MS - 1)
      expect(hold.layout.value!.map(g => g.project.id)).toEqual(['p-aeon', 'p-pharos'])
      timers.advance(1)
      expect(hold.layout.value!.map(g => g.project.id)).toEqual(['p-pharos'])
      expect(hold.pending.value).toBe(0)
    } finally { scope.stop() }
  })

  it('keeps a handover heading out through a cancelled release, then applies it on release', async () => {
    const { pointing, timers, scope, lead, hold } = setup()
    try {
      pointing.value = true; await nextTick()
      lead.value = { ...lead.value, session_id: 'next-generation', state: 'starting', revision: 2, generation: 2 }
      expect(hold.pending.value, 'the pending lead heading is a waiting change').toBe(1)
      expect(hold.layout.value![0]!.line).toBeNull()
      expect(ids(hold.views.value)).toEqual(['a'])
      pointing.value = false; await nextTick()
      timers.advance(RELEASE_MS - 1)
      pointing.value = true; await nextTick()
      timers.advance(RELEASE_MS)
      expect(hold.layout.value![0]!.line).toBeNull()
      expect(hold.pending.value).toBe(1)
      pointing.value = false; await nextTick()
      timers.advance(RELEASE_MS)
      expect(hold.layout.value![0]!.line?.kind).toBe('pending')
      expect(hold.pending.value).toBe(0)
    } finally { scope.stop() }
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
