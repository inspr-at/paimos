// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1494: what Goals needs before In flight retires. Risks: hours left that
// round a little work to nothing or hide work without an estimate; "today" read
// in the wrong time zone; a note without its author and time; a stream drop the
// footer never admits, or a goal change that waits for the next minute's read;
// a goal percent that is not its items' rollup, or a hand-set one without the pen.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, effectScope, nextTick, ref, type EffectScope } from 'vue'
import { renderToString } from '@vue/server-renderer'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { Identity } from '../src/lib/api'
import type { Goal, GoalList, GoalOutcome } from '../src/lib/goals'

const mocks = vi.hoisted(() => {
  const media = { matches: false, addEventListener() {}, removeEventListener() {} }
  vi.stubGlobal('window', { history: { state: {} }, matchMedia: () => media, addEventListener() {}, removeEventListener() {}, localStorage: { getItem: () => null, setItem() {}, removeItem() {} }, sessionStorage: { getItem: () => null, setItem() {}, removeItem() {} } })
  vi.stubGlobal('navigator', { platform: 'MacIntel', userAgent: 'test', onLine: true })
  return { listGoals: vi.fn() }
})
vi.mock('../src/lib/goals', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/goals')>()
  return { ...actual, listGoals: mocks.listGoals, goalSeries: vi.fn(async () => ({ range: '24h', expansion: 'full', points: [], read_through: 1, generation: 1 })), goalCreatedAt: vi.fn(async () => 1_000) }
})
vi.mock('../src/lib/api', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/api')>()
  return { ...actual, api: vi.fn(async () => new Response(JSON.stringify({ value: null }), { status: 200, headers: { 'content-type': 'application/json' } })) }
})
vi.mock('../src/components/projects/LiveAgents.vue', () => ({ default: { name: 'LiveAgents', props: ['agents', 'project', 'variant'], render: () => null } }))
vi.mock('../src/lib/usePolledData', async importOriginal => {
  const actual = await importOriginal<typeof import('../src/lib/usePolledData')>()
  return { ...actual, usePoller: (run: () => Promise<unknown>) => ({ start(immediate = false) { if (immediate) void run() }, stop() {}, tick() {} }) }
})

import { agoText, doneToday, goalPercent, hoursLeftLine, hoursLeftText, noteByline } from '../src/lib/goals'
import { openGoalsLive, type GoalsLiveSource, type GoalsLiveState } from '../src/lib/goalsLive'
import { useSession } from '../src/stores/session'
import { useGoalsPage } from '../src/components/goals/goalsPage'
import GoalItem from '../src/components/goals/GoalItem.vue'
import GoalRow from '../src/components/goals/GoalRow.vue'
import GoalFreshness from '../src/components/goals/GoalFreshness.vue'
import GoalProgress from '../src/components/goals/GoalProgress.vue'
import GoalStatusNote from '../src/components/goals/GoalStatusNote.vue'
import GoalsView from '../src/components/goals/GoalsView.vue'
import type { GoalsPage } from '../src/components/goals/goalsPage'

const HOUR = 3_600_000
// Sun 18 Oct 2026, 00:30 in Vienna (22:30 UTC the day before).
const NOW = Date.parse('2026-10-17T22:30:00Z')
const VIENNA = 'Europe/Vienna'
const clock = (ms: number) => new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(new Date(ms))
const person = (id: string): Identity => ({ principal: { id, name: id, kind: 'person' }, tenant: { id: 'tenant', name: 'Workspace' } })
const outcome = (extra: Partial<GoalOutcome> = {}): GoalOutcome => ({
  id: 'o1', ref: 'o1', label: 'Agent reviews become authenticated review runs', note: '', position: 1, keys: ['AEON-1442'],
  state: 'moving', session_ids: [], eta: { source: 'none', partial: false }, passed: false,
  coverage: { estimated_leaves: 1, total_leaves: 1 }, weight: 1, handed_over: false, ...extra,
})
function goal(id: string, extra: Partial<Goal> = {}): Goal {
  return {
    id, key: id.toUpperCase(), title: id, kind: 'goal', project_id: null, workspace: true, expansion: 'full',
    counts: { outcomes: 1, tickets: 1 }, items: [], steps: [], next_step: null, projects: [], waits: { count: 0, question_ids: [] },
    progress: { pct: 10, computed_pct: 10, source: 'computed', basis: 'count', total: 1, done: 0, items_without_estimate: 0, items_partially_estimated: 0 },
    states: { moving: 1, queued: 0, paused: 0, done: 0 }, done: false, read_through: 1, generation: 1, ...extra,
  }
}
const listed = (items: Goal[], extra: Partial<GoalList> = {}): GoalList => ({
  items, totals: { moving: 1, queued: 0, paused: 0, done: 0 }, totals_partial: false, quiet_projects: { count: 0, keys: [] }, truncated: false, read_through: 1, generation: 1, ...extra,
})
// Server-rendered markup without Vue's fragment comments.
const bare = (markup: string) => markup.replace(/<!--[^>]*-->/g, '')
async function html(component: unknown, props: Record<string, unknown>) {
  const pinia = createPinia()
  setActivePinia(pinia)
  useSession().identity = person('person-a')
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { render: () => null } }] })
  const app = createSSRApp(component as never, props)
  app.use(pinia)
  app.use(router)
  return renderToString(app)
}

describe('the words for hours left, today and notes', () => {
  it('hours left rounds to whole hours, never to nothing, and admits work without an estimate', () => {
    const progress = goal('g').progress!
    expect(hoursLeftText({ ...progress, remaining_hours: 39.6 }, false)).toBe('~40 h left')
    expect(hoursLeftText({ ...progress, remaining_hours: 0.3 }, false)).toBe('~1 h left')
    expect(hoursLeftText({ ...progress, remaining_hours: 0, remaining_unestimated: 4 }, false)).toBe('')
    expect(hoursLeftText({ ...progress, remaining_hours: 12 }, true)).toBe('')
    expect(hoursLeftText(undefined, false)).toBe('')
    expect(hoursLeftLine({ ...progress, remaining_hours: 40 }, false)).toEqual({ text: 'About 40 agent hours of work left', muted: ' (estimates)' })
    expect(hoursLeftLine({ ...progress, remaining_hours: 1, remaining_unestimated: 3 }, false)).toEqual({ text: 'About 1 agent hour of work left', muted: ' (estimates; 3 open tickets have none)' })
    expect(hoursLeftLine({ ...progress, remaining_hours: 0 }, false)).toBeNull()
  })

  it('"today" is the viewer’s day: a merge at 23:10 UTC is today in Vienna, yesterday in UTC', () => {
    const items = [
      outcome({ state: 'done', merged_at: '2026-10-17T23:10:00Z' }), // 01:10 Vienna, after NOW: not yet
      outcome({ state: 'done', merged_at: '2026-10-17T22:10:00Z' }), // 00:10 Vienna today
      outcome({ state: 'done', merged_at: '2026-10-17T21:50:00Z' }), // 23:50 Vienna yesterday
      outcome({ state: 'done' }),
    ]
    expect(doneToday(items, NOW, VIENNA)).toBe(1)
    expect(doneToday(items, NOW, 'UTC')).toBe(2)
  })

  it('the note byline names its author and time, and the status note its age', () => {
    expect(noteByline({ note_by_name: 'AEON-LEAD', note_at: '2026-10-17T16:52:00Z' }, Date.parse('2026-10-17T19:00:00Z'), VIENNA)).toBe('· AEON-LEAD 18:52')
    expect(noteByline({ note_by_name: '', note_at: '2026-10-17T16:52:00Z' }, Date.parse('2026-10-17T19:00:00Z'), VIENNA)).toBe('· 18:52')
    expect(noteByline({}, NOW)).toBe('')
    expect(agoText(new Date(NOW - 2 * HOUR).toISOString(), NOW)).toBe('2 h ago')
    expect(agoText(new Date(NOW - 5 * 60_000).toISOString(), NOW)).toBe('5 min ago')
    expect(agoText(new Date(NOW - 20_000).toISOString(), NOW)).toBe('just now')
    expect(agoText(new Date(NOW - 72 * HOUR).toISOString(), NOW)).toBe('3 d ago')
  })
})

class FakeSource implements GoalsLiveSource {
  readyState = 1
  onerror: ((event: Event) => unknown) | null = null
  closed = false
  private listeners = new Map<string, ((event: MessageEvent) => void)[]>()
  constructor(readonly url: string) {}
  addEventListener(type: string, listener: (event: MessageEvent) => void) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]) }
  close() { this.closed = true }
  emit(type: string, data: unknown, id = '') { for (const listener of this.listeners.get(type) ?? []) listener(new MessageEvent(type, { data: JSON.stringify(data), lastEventId: id })) }
  fail(readyState: number) { this.readyState = readyState; this.onerror?.(new Event('error')) }
}

describe('the live line', () => {
  beforeEach(() => { vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'] }); vi.setSystemTime(NOW) })
  afterEach(() => { vi.useRealTimers() })

  it('says Live once ready, re-reads on a goal change, says Reconnecting on a drop and re-reads after a gap only', () => {
    const sources: FakeSource[] = [], states: GoalsLiveState[] = []
    const changed = vi.fn()
    const live = openGoalsLive({ open: url => { const s = new FakeSource(url); sources.push(s); return s }, changed, state: s => states.push(s), retryMs: [1_000] })
    expect(sources[0]!.url).toBe('/api/events/stream?after=latest')
    expect(states).toEqual(['connecting'])
    sources[0]!.emit('stream.ready', { after: 5, resumed: false })
    expect(states.at(-1)).toBe('live')
    expect(changed).not.toHaveBeenCalled() // the view's own first read covers the start
    sources[0]!.emit('goal.status_note_changed', {}, '7')
    sources[0]!.emit('goal.items_changed', {}, '8')
    expect(changed).toHaveBeenCalledTimes(2)

    // The browser gave up: Reconnecting, then a new stream after the last goal event.
    sources[0]!.fail(2)
    expect(sources[0]!.closed).toBe(true)
    expect(states.at(-1)).toBe('reconnecting')
    vi.advanceTimersByTime(1_000)
    expect(sources[1]!.url).toBe('/api/events/stream?after=8')
    sources[1]!.emit('stream.ready', { after: 12, resumed: false })
    expect(states.at(-1)).toBe('live')
    expect(changed).toHaveBeenCalledTimes(3) // the server could not resume: read again

    // The browser retries on its own; an exact resume needs no read.
    sources[1]!.fail(0)
    expect(states.at(-1)).toBe('reconnecting')
    sources[1]!.emit('stream.ready', { after: 12, resumed: true })
    expect(states.at(-1)).toBe('live')
    expect(changed).toHaveBeenCalledTimes(3)

    // A stale stream's late events are ignored; closing ends the line.
    sources[0]!.emit('goal.updated', {}, '99')
    expect(changed).toHaveBeenCalledTimes(3)
    live.close()
    expect(sources[1]!.closed).toBe(true)
    expect(states.at(-1)).toBe('off')
  })
})

describe('the page reads again when goals change', () => {
  const scopes: EffectScope[] = []
  const streams: FakeSource[] = []
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    vi.stubGlobal('EventSource', class extends FakeSource { constructor(url: string) { super(url); streams.push(this) } })
    mocks.listGoals.mockReset().mockResolvedValue(listed([goal('g1')], { status_note: { text: 'Nine items merged.', by: 'p-lead', by_name: 'LEAD', set_at: new Date(NOW - 2 * HOUR).toISOString() }, eta_last_set_at: '2026-10-17T16:41:00Z' }))
  })
  afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); streams.length = 0; vi.unstubAllGlobals(); vi.useRealTimers() })

  it('a goal event reads the list at once (one read per burst), and a drop shows Reconnecting', async () => {
    useSession().identity = person('person-a')
    const scope = effectScope()
    scopes.push(scope)
    const page = scope.run(() => useGoalsPage({ term: ref(''), active: () => true }))! as GoalsPage
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.listGoals).toHaveBeenCalledTimes(1)
    expect(page.statusNote.value?.by_name).toBe('LEAD')
    expect(page.etaLastSetAt.value).toBe(Date.parse('2026-10-17T16:41:00Z'))
    expect(page.updatedAt.value).not.toBeNull()
    const stream = streams.at(-1)!
    stream.emit('stream.ready', { after: 3, resumed: false })
    expect(page.live.value).toBe('live')
    stream.emit('goal.eta_changed', {}, '4')
    stream.emit('goal.items_changed', {}, '5')
    await vi.advanceTimersByTimeAsync(400)
    expect(mocks.listGoals).toHaveBeenCalledTimes(2)
    stream.fail(0)
    expect(page.live.value).toBe('reconnecting')
    scope.stop()
    await nextTick()
    expect(stream.closed).toBe(true)
  })

  it('a busy stream never starves the list: changes during a read cause one more read after it, not an abort', async () => {
    useSession().identity = person('person-a')
    const scope = effectScope()
    scopes.push(scope)
    scope.run(() => useGoalsPage({ term: ref(''), active: () => true }))
    await vi.advanceTimersByTimeAsync(0)
    const stream = streams.at(-1)!
    stream.emit('stream.ready', { after: 3, resumed: false })
    let finish!: (value: GoalList) => void
    const signals: (AbortSignal | undefined)[] = []
    mocks.listGoals.mockImplementation((_query: unknown, signal?: AbortSignal) => { signals.push(signal); return new Promise<GoalList>(resolve => { finish = resolve }) })
    stream.emit('goal.items_changed', {}, '4')
    await vi.advanceTimersByTimeAsync(400)
    expect(mocks.listGoals).toHaveBeenCalledTimes(2)
    for (let id = 5; id < 9; id++) { stream.emit('goal.eta_changed', {}, String(id)); await vi.advanceTimersByTimeAsync(400) }
    expect(mocks.listGoals).toHaveBeenCalledTimes(2) // still the one read in flight
    expect(signals[0]?.aborted).toBe(false)
    finish(listed([goal('g1')]))
    await vi.advanceTimersByTimeAsync(0)
    expect(mocks.listGoals).toHaveBeenCalledTimes(3) // exactly one follow-up for everything that arrived meanwhile
    finish(listed([goal('g1')]))
    await vi.advanceTimersByTimeAsync(1_000)
    expect(mocks.listGoals).toHaveBeenCalledTimes(3)
  })
})

describe('what the rows and the footer show', () => {
  const row = (open: boolean) => ({
    isOpen: () => open, filter: ref('all'), searchOpens: () => false, folds: new Map(), charts: new Set(),
    range: ref('24h'), seriesFor: () => undefined, toggle() {}, toggleChart() {}, setRange() {}, toggleFold() {},
  })

  it('an item shows its agent note with a speech icon, its author and time; without a note no note line', async () => {
    const at = Date.now() - 10 * 60_000
    const noted = await html(GoalItem, { item: outcome({ note: 'Hand-check of 5 review runs, then the gate goes live', note_by: 'p1', note_by_name: 'AEON-LEAD', note_at: new Date(at).toISOString() }), goalId: 'g', agents: [], project: { title: 'Release', routeKey: 'AEON' }, now: Date.now() })
    expect(noted).toContain('i-live is-note')
    expect(noted).toContain('Hand-check of 5 review runs, then the gate goes live')
    expect(noted).toContain(`· AEON-LEAD ${clock(at)}`)
    expect(noted).toContain('M3.2 2.8h9.6') // the speech icon
    const plain = await html(GoalItem, { item: outcome(), goalId: 'g', agents: [], project: { title: 'Release', routeKey: 'AEON' }, now: Date.now() })
    expect(plain).not.toContain('is-note')
  })

  it('a goal row keeps a reserved hours-left line, and its Done fold counts today’s merges', async () => {
    const now = Date.now()
    const items = [
      outcome({ id: 'd1', state: 'done', merged_at: new Date(now - 60_000).toISOString() }),
      outcome({ id: 'd2', state: 'done', merged_at: new Date(now - 120_000).toISOString() }),
      outcome({ id: 'd3', state: 'done', merged_at: new Date(now - 72 * HOUR).toISOString() }),
      outcome({ id: 'm1', state: 'moving' }),
    ]
    const g = goal('g-r136', { items, eta: { source: 'agent', text: 'Sat 17 Oct', partial: false }, progress: { ...goal('x').progress!, remaining_hours: 39.6, remaining_unestimated: 0 }, states: { moving: 1, queued: 0, paused: 0, done: 3 } })
    const rendered = await html(GoalRow, { goal: g, page: row(true), now, agents: new Map(), term: '', menuOpen: false })
    expect(rendered).toContain('~40 h left')
    expect(rendered).toContain('about 40 h left')
    expect(bare(rendered)).toMatch(/<b[^>]*>3<\/b> done<span class="today"[^>]*>· 2 today<\/span>/)
    const unknown = await html(GoalRow, { goal: goal('g2', { eta: { source: 'agent', text: '~21:00', partial: false } }), page: row(false), now, agents: new Map(), term: '', menuOpen: false })
    // The line is there while unknown, empty and hidden from screen readers, so nothing moves when it arrives.
    expect(unknown).toMatch(/<span class="left"[^>]*aria-hidden="true"[^>]*><\/span>/)
    expect(unknown).not.toContain(' today')
  })

  it('the footer says Live with the last ETA time, or Reconnecting with the last update, in one slot', async () => {
    const now = Date.now(), set = now - 30 * 60_000, update = now - 4 * 60_000
    const on = await html(GoalFreshness, { live: 'live', etaSetAt: set, updatedAt: update, now })
    expect(on).toContain('data-live="1"')
    expect(bare(on)).toMatch(new RegExp(`<b[^>]*>Live</b> · ETAs last set ${clock(set)}`))
    expect(on).toMatch(/class="f-off" aria-hidden="true"/)
    expect(on).not.toContain('aria-hidden="false"')
    const off = await html(GoalFreshness, { live: 'reconnecting', etaSetAt: set, updatedAt: update, now })
    expect(off).toContain('data-live="0"')
    expect(bare(off)).toMatch(new RegExp(`<b[^>]*>Reconnecting</b> · last update ${clock(update)}`))
    expect(off).toMatch(/class="f-live" aria-hidden="true"/)
  })

  it('a goal’s percent comes from its items unless set by hand, and a hand-set percent always shows the pen and says so', async () => {
    // Tick's early read: "0 of 1 done" beside 31 % and no pen. A computed goal shows its items' rollup,
    // even if a stale percent rides along; only a hand-set percent shows its own value, with the pen.
    const computed = { pct: 31, computed_pct: 0, source: 'computed', basis: 'count', total: 1, done: 0, items_without_estimate: 0, items_partially_estimated: 0 } as const
    const hand = { ...computed, source: 'override', override_note: 'the baseline counts double' } as const
    expect(goalPercent(computed)).toEqual({ pct: 0, byHand: false })
    expect(goalPercent(hand)).toEqual({ pct: 31, byHand: true, tip: 'Progress set by hand · the baseline counts double' })
    expect(goalPercent({ ...hand, override_note: undefined }).tip).toBe('Progress set by hand')
    const pen = /<span class="pct"[^>]*><svg|<button type="button" class="pct"[^>]*><svg/
    // Both forms: the bar (closed) and the chart that swaps in for it (open).
    for (const open of [false, true]) {
      const plain = bare(await html(GoalProgress, { goal: goal('g1', { progress: computed }), open, range: '24h', now: NOW }))
      expect(plain).toMatch(/class="pct"[^>]*>0%</)
      expect(plain).not.toMatch(pen)
      expect(plain).not.toContain('set by hand')
      const set = bare(await html(GoalProgress, { goal: goal('g1', { progress: hand }), open, range: '24h', now: NOW }))
      expect(set).toMatch(pen)
      expect(set).toMatch(/class="pct" data-tip="Progress set by hand · the baseline counts double"[^>]*>|data-tip="Progress set by hand · the baseline counts double"[^>]*class="pct"/)
      expect(set).toMatch(/<\/svg>31%</)
      if (!open) {
        expect(plain).toContain('0 of 1 done')
        expect(set).toContain('aria-label="g1, 31 percent done, set by hand. Show progress over time"')
      } else expect(set).toContain('aria-label="31 percent, set by hand. Hide progress over time"')
    }
  })

  it('the status note is one line with its author and age, without a fold chevron, and renders nothing without a note', async () => {
    const note = { text: 'Since this morning: 9 items merged.', by: 'p-lead', by_name: 'LEAD', set_at: new Date(Date.now() - 2 * HOUR).toISOString() }
    const shown = await html(GoalStatusNote, { note, now: Date.now() })
    expect(shown).toContain('Since this morning: 9 items merged.')
    expect(shown).toContain('<span class="n-by"')
    expect(shown).toContain('LEAD</span> · 2 h ago')
    expect(shown).toContain('data-full="0"')
    // One line at every width: the separate fold chevron and its remembered fold are gone.
    expect(shown).not.toContain('n-toggle')
    expect(shown).not.toContain('data-folded')

    const view = (statusNote: typeof note | null) => ({
      counts: ref({ all: 1, moving: 1, queued: 0, paused: 0, done: 0 }), totalsPartial: ref(false), filter: ref('all'), hideDone: ref(true), allOpen: ref(false),
      setHideDone() {}, toggleAll() {}, goals: ref([goal('g1')]), list: ref(listed([goal('g1')])), loaded: ref(true), error: ref(''), reading: ref(false),
      loadingMore: ref(false), hiddenByGroups: ref(false), sort: ref('eta'), dir: ref('asc'), range: ref('24h'), setSort() {}, setRange() {}, loadMore() {}, read() {}, want() {},
      charts: new Set(), folds: new Map(), isOpen: () => false, searchOpens: () => false, seriesFor: () => undefined, toggle() {}, toggleChart() {}, toggleFold() {},
      statusNote: ref(statusNote), live: ref('live'), etaLastSetAt: ref(null), updatedAt: ref(null),
    }) as unknown as GoalsPage
    const withNote = await html(GoalsView, { page: view(note), term: '', now: Date.now(), hiddenGroups: false })
    expect(withNote.indexOf('class="ws-note"')).toBeGreaterThan(-1)
    expect(withNote.indexOf('class="ws-note"')).toBeLessThan(withNote.indexOf('aria-label="Waiting on you"'))
    expect(withNote).toContain('class="fresh"')
    const without = await html(GoalsView, { page: view(null), term: '', now: Date.now(), hiddenGroups: false })
    expect(without).not.toContain('ws-note')
  })
})
