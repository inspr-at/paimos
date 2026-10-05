// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import { useReleasePlanning } from '../src/lib/useReleasePlanning'
import { planningListItem, planningQuery, type PlanningAnswer, type PlanningContext, type PlanningOverview, type PlanningRead, type PlanningSource, type ItemPage, type PlanningRelease } from '../src/lib/deliveryPlanning'
import { filtersFromQuery } from '../src/lib/ticketList'
import type { PlacementReceipt } from '../src/lib/deliveryChanges'
const context: PlanningContext = { project: 'project', person: 'person', scope: 'all', query: 'view=planning&hide_closed=true' }
const counts = (shown = 1, hidden = 0) => ({ matched_count: shown + hidden, shown_count: shown, hidden_count: hidden, hidden_finished: hidden, hidden_exit: 0, incomplete: false })
const release = (id: string, shown = 1): PlanningRelease => ({ project_id: 'project', release_id: id, title: id, state: 'planned', visibility: 'internal', rank: id, revision: 1, matches: counts(shown), rollup: { units: shown, completed: 0, open_hours: shown }, build_summary: { budget_outlook: 'unknown' } })
const overview = (n = 2): PlanningOverview => ({ active: Array.from({ length: n }, (_, i) => release(String(i))), released: { items: [], next_cursor: 'history-2' }, backlog: { ranked: 0, tail: 0 }, backlog_matches: { ranked: counts(0), tail: counts(0) }, abandoned: 0, counts_incomplete: false })
const page = (n: number, prefix = '', cursor = ''): ItemPage => ({ items: Array.from({ length: n }, (_, i) => ({ item_id: `${prefix}${i}`, project_id: 'project', key: `KEY-${i}`, title: 'Work', kind: 'ticket', state: 'backlog', rank: String(i), created_at: '', node_revision: '', revision: 1, estimated_hours: 1, expedite: false, due_on: null })), next_cursor: cursor, count: n, incomplete: false, matches: counts(n) })
function harness(canInsert?: (source: PlanningSource) => boolean) {
  const requests: { source: PlanningSource; context: PlanningContext; cursor: string; limit: number; signal: AbortSignal; resolve: (answer: PlanningAnswer) => void; reject: (error: Error) => void }[] = []
  const read: PlanningRead = (context, source, cursor, limit, signal) => new Promise((resolve, reject) => { requests.push({ source, context, cursor, limit, signal, resolve, reject }) })
  return { requests, state: useReleasePlanning({ read, canInsert }) }
}
async function settle() { await nextTick(); await nextTick() }

describe('release planning query', () => {
  it('intersects work filters and Hide without a lifecycle or scope selector', () => {
    const query = new URLSearchParams(planningQuery('project', filtersFromQuery({ q: 'beyond page one', status: 'done,!cancelled', assignee: 'mira,!lin', type: 'ticket,!epic', ships_in: 'none', closed: '1' }), ['done', 'cancelled']))
    expect(query.get('view')).toBe('planning'); expect(query.get('hide_closed')).toBe('false'); expect(query.get('q')).toBe('beyond page one')
    expect(query.getAll('work_state')).toContain('done'); expect(query.getAll('work_state')).toContain('!cancelled')
    expect(query.getAll('assignee')).toEqual(['mira', '!lin']); expect(query.getAll('kind')).toEqual(['ticket', '!epic']) // Main preserves explicit exclusions for canonical Work aliases.
    expect(query.has('state')).toBe(false); expect(query.has('ships_in')).toBe(false); expect(query.getAll('hide_state')).toEqual(['done', 'cancelled'])
  })
  it('bounds UTF-8 search before work', () => { expect(() => planningQuery('project', filtersFromQuery({ q: 'ä'.repeat(101) }))).toThrow('200 UTF-8') })
  it('bounds filter arrays before normalization', () => {
    expect(() => planningQuery('project', { ...filtersFromQuery({}), assignee: Array.from({ length: 101 }, (_, i) => `person-${i}`) })).toThrow('Too many filter values')
  })
})
describe('bounded release reads', () => {
  it('retires occupied-row capacity after a placement receipt for an unrendered member', async () => {
    const h = harness(); h.state.reset(context)
    const initial = overview(2)
    initial.active[0]!.occupied_rows = 950; initial.active[1]!.occupied_rows = 999
    h.requests[0]!.resolve(initial); await settle()
    h.state.committed({ kind: 'placement', result: { items: [{ item_id: 'unrendered', project_id: 'project', release_id: '1', revision: 2, expedite: false, due_on: null }], release_revisions: { '0': 2, '1': 2 }, undo_event_id: 42 } })
    expect(h.state.overview.value?.active.map(row => row.occupied_rows)).toEqual([undefined, undefined])
    expect(h.state.overview.value?.active.map(row => row.revision)).toEqual([2, 2])
    expect(h.requests).toHaveLength(1); h.state.dispose()
  })
  it('retires source and potential successor capacity after lifecycle rollover', async () => {
    const h = harness(); h.state.reset(context)
    const initial = overview(2)
    initial.active[0]!.occupied_rows = 950; initial.active[1]!.occupied_rows = 999
    h.requests[0]!.resolve(initial); await settle()
    h.state.committed({ kind: 'lifecycle', result: { ...initial.active[0]!, state: 'frozen', version: '1.0.0', revision: 2 } })
    expect(h.state.overview.value?.active.map(row => row.occupied_rows)).toEqual([undefined, undefined])
    expect(h.state.overview.value?.active.every(row => row.rollup_stale)).toBe(true)
    expect(h.requests).toHaveLength(1); h.state.dispose()
  })
  it('applies recovered completed work to rollups without inserting hidden/unsearched rows or exposing foreign reads', async () => {
    const h = harness(); h.state.reset(context); h.requests[0]!.resolve(overview(2)); await settle()
    h.state.expand('0'); h.requests[1]!.resolve({ ...page(0), matches:counts(0) }); await settle()
    const recovered = { ...page(1,'recovered').items[0]!, release_id:'1', state:'done' }
    h.state.committed({ kind:'placement', recovered:[recovered], undoable:false, result:{ items:[{...recovered,release_id:'0',revision:2}], release_revisions:{'0':2}, undo_event_id:9 } })
    expect(h.state.overview.value?.active[0]?.rollup).toEqual({units:2,completed:1,open_hours:1})
    expect(h.state.overview.value?.active[1]?.rollup).toEqual({units:0,completed:0,open_hours:1})
    expect(h.state.work['release:0']?.items).toEqual([]); expect(h.state.overview.value?.counts_incomplete).toBe(true)
    expect(h.requests).toHaveLength(2); h.state.dispose()
  })
  it('moves its own closed row to Released while leaving foreign containers for explicit Refresh', async () => {
    const h = harness(); h.state.reset(context); const initial=overview(2); initial.active[0]!.rollup={units:3,completed:2,open_hours:1}; h.requests[0]!.resolve(initial); await settle()
    h.state.committed({kind:'lifecycle',result:{...initial.active[0]!,state:'released',revision:2}})
    expect(h.state.overview.value?.active.map(row=>row.release_id)).toEqual(['1'])
    expect(h.state.overview.value?.released.items[0]?.rollup_stale).toBe(true)
    expect(h.state.overview.value?.active[0]?.rollup).toEqual({units:1,completed:0,open_hours:1})
    expect(h.state.overview.value?.counts_incomplete).toBe(true); expect(h.requests).toHaveLength(1); h.state.dispose()
  })
  it('serialized Backlog receipts clear release identity and Undo restores rollups without reads', async () => {
    const h = harness(); h.state.reset(context); h.requests[0]!.resolve(overview(1)); await settle()
    h.state.expand('0'); h.state.expand('backlog')
    const original = { ...page(1, 'owned').items[0]!, release_id: '0', rank: 'V' }
    h.requests[1]!.resolve({ ...page(1), items: [original] }); await settle()
    const moved: PlacementReceipt = JSON.parse('{"items":[{"item_id":"owned0","project_id":"project","rank":"W","revision":2,"expedite":false,"due_on":null}],"undo_event_id":42}')
    h.state.committed({ kind: 'placement', result: moved })
    expect(h.state.work['backlog:ranked']?.items[0]?.release_id).toBeUndefined()
    expect(h.state.overview.value?.backlog.ranked).toBe(1)
    expect(h.state.overview.value?.active[0]?.rollup).toEqual({ units: 0, completed: 0, open_hours: 0 })
    const restored: PlacementReceipt = JSON.parse('{"items":[{"item_id":"owned0","project_id":"project","release_id":"0","rank":"V","revision":3,"expedite":false,"due_on":null}],"undo_event_id":null}')
    h.state.committed({ kind: 'placement', result: restored })
    expect(h.state.work['release:0']?.items[0]).toMatchObject({ release_id: '0', rank: 'V', revision: 3 })
    expect(h.state.work['backlog:ranked']?.items).toEqual([])
    expect(h.state.overview.value?.active[0]?.rollup).toEqual({ units: 1, completed: 0, open_hours: 1 })
    expect(h.state.overview.value?.backlog.ranked).toBe(0)
    expect(h.state.overview.value?.backlog_matches?.ranked).toEqual(counts(0))
    expect(h.requests).toHaveLength(2); h.state.dispose()
  })
  it('serialized unranked Undo clears the moved rank and restores the Backlog tail counts', async () => {
    const h = harness(); h.state.reset(context)
    const answer = overview(1); answer.active[0] = release('0', 0); answer.backlog.tail = 1; answer.backlog_matches!.tail = counts(1)
    h.requests[0]!.resolve(answer); await settle(); h.state.expand('0'); h.state.expand('backlog')
    const original = { ...page(1, 'tail').items[0]!, rank: undefined }
    h.requests[1]!.resolve({ ...page(1), items: [original] }); await settle()
    const moved: PlacementReceipt = JSON.parse('{"items":[{"item_id":"tail0","project_id":"project","release_id":"0","rank":"V","revision":2,"expedite":false,"due_on":null}],"undo_event_id":42}')
    h.state.committed({ kind: 'placement', result: moved })
    const restored: PlacementReceipt = JSON.parse('{"items":[{"item_id":"tail0","project_id":"project","revision":3,"expedite":false,"due_on":null}],"undo_event_id":null}')
    h.state.committed({ kind: 'placement', result: restored })
    expect(h.state.work['backlog:tail']?.items[0]?.rank).toBeUndefined()
    expect(h.state.work['backlog:tail']?.items[0]?.release_id).toBeUndefined()
    expect(h.state.overview.value?.backlog).toEqual({ ranked: 0, tail: 1 })
    expect(h.state.overview.value?.backlog_matches?.tail).toEqual(counts(1))
    expect(h.state.overview.value?.active[0]?.rollup.units).toBe(0)
    expect(h.requests).toHaveLength(2); h.state.dispose()
  })
  it('makes one overview request with no member fan-out, then lazily opens multiple rows', async () => {
    const h = harness(); h.state.reset(context); expect(h.requests.map(r => r.source)).toEqual(['overview'])
    h.requests[0]!.resolve(overview()); await settle(); expect(h.requests).toHaveLength(1)
    h.state.expand('0'); h.state.expand('1'); expect(h.requests.slice(1).map(r => r.source)).toEqual(['release:0', 'release:1'])
    h.requests[1]!.resolve(page(1)); h.requests[2]!.resolve(page(1)); await settle()
    expect([...h.state.expanded]).toEqual(['0', '1']); expect(h.state.rendered.value).toBe(2); h.state.dispose()
  })
  it('restores unloaded expansion IDs without fetching their members before their release page', async () => {
    const h = harness(); h.state.reset(context); h.requests[0]!.resolve(overview()); await settle()
    h.state.expand('unloaded'); expect(h.requests).toHaveLength(1)
    h.state.more('released'); h.requests[1]!.resolve({ items: [release('unloaded')] }); await settle()
    expect(h.requests.map(r => r.source)).toEqual(['overview', 'released', 'release:unloaded']); h.state.dispose()
  })
  it('reserves no more than four requests and 2,000 rendered work rows for Expand all', async () => {
    const h = harness(); h.state.reset(context); h.requests[0]!.resolve(overview(50)); await settle(); h.state.expandAll()
    expect(h.requests).toHaveLength(5); expect(h.state.inFlight.value).toBe(4)
    for (let first = 1; first <= 9; first += 4) {
      for (const request of h.requests.slice(first, first + 4)) request.resolve(page(request.limit, request.source))
      await settle(); expect(h.state.inFlight.value).toBeLessThanOrEqual(4)
    }
    for (const request of h.requests.slice(9)) request.resolve(page(request.limit, request.source)); await settle()
    expect(h.state.rendered.value).toBe(2000); expect(h.requests).toHaveLength(11); expect(h.state.budgetBlocked.value).toBe(true); expect(h.state.queued.value).toBe(40)
    h.state.collapse('0'); await settle(); expect(h.requests).toHaveLength(12); h.state.dispose()
  })
  it.each(['release:0', 'backlog:ranked', 'backlog:tail'] as const)('reopens %s after continuation with fresh cursor history', async source => {
    const h = harness(); h.state.reset(context)
    const answer = overview(); answer.backlog_matches = { ranked: counts(3), tail: counts(3) }
    h.requests[0]!.resolve(answer); await settle()
    const id = source.startsWith('backlog:') ? 'backlog' : '0'
    h.state.expand(id)
    const first = h.requests.find(r => r.source === source)!
    first.resolve(page(1, 'first', 'page-2')); await settle()
    h.state.moreWork(source); h.requests.at(-1)!.resolve(page(1, 'second', 'page-3')); await settle()
    expect(h.state.work[source]?.items).toHaveLength(2)
    h.state.collapse(id); h.state.expand(id)
    h.requests.filter(r => r.source === source).at(-1)!.resolve(page(1, 'reopened', 'page-2')); await settle()
    expect(h.state.work[source]?.error).toBe('')
    expect(h.state.work[source]?.items.map(i => i.item_id)).toEqual(['reopened0'])
    expect(h.state.work[source]?.cursor).toBe('page-2'); h.state.dispose()
  })
  it('expands loaded Upcoming and Released rows through the same request and render bounds', async () => {
    const h = harness(); h.state.reset(context)
    const answer = overview(1); answer.released.items = Array.from({ length: 50 }, (_, i) => ({ ...release(`history-${i}`), state: 'released' as const }))
    h.requests[0]!.resolve(answer); await settle(); h.state.expandAll()
    expect([...h.state.expanded]).toEqual(['0', ...answer.released.items.map(r => r.release_id), 'backlog'])
    expect(h.state.inFlight.value).toBe(4)
    for (let first = 1; first <= 9; first += 4) {
      for (const request of h.requests.slice(first, first + 4)) request.resolve(page(request.limit, request.source))
      await settle(); expect(h.state.inFlight.value).toBeLessThanOrEqual(4)
    }
    for (const request of h.requests.slice(9)) request.resolve(page(request.limit, request.source)); await settle()
    expect(h.state.rendered.value).toBe(2000); expect(h.requests).toHaveLength(11)
    expect(h.requests.slice(2).every(r => r.source.startsWith('release:history-'))).toBe(true)
    expect(h.state.budgetBlocked.value).toBe(true); expect(h.state.queued.value).toBe(41); h.state.dispose()
  })
  it('preserves populated ticket context and reported progress/ETA in the reused row', () => {
    const item = { ...page(1).items[0]!, parent: { id: 'epic', key: 'EP-1', title: 'Epic context', kind_slug: 'epic' }, epic: { id: 'epic', key: 'EP-1', title: 'Epic context' }, assignee: { id: 'person', name: 'Mira', has_avatar: true }, eta: { progress_pct: 42, eta_ready_at: '2026-10-03T14:00:00Z', finished: false } }
    expect(planningListItem(item)).toMatchObject({ parent_id: 'epic', parent: item.parent, epic: item.epic, assignee: item.assignee, eta: item.eta })
  })
  it('expands unloaded member hits and hidden-only counts, leaves name-only hits closed', async () => {
    const h = harness(); h.state.reset({ ...context, query: `${context.query}&q=needle` })
    const answer = overview(0); answer.active = [release('member', 2), release('hidden', 0), release('name', 0)]
    answer.active[1]!.matches = counts(0, 230); answer.released.items = [release('historical', 1)]
    h.requests[0]!.resolve(answer); await settle(); expect([...h.state.expanded]).toEqual(['member', 'hidden', 'historical'])
    expect(h.requests.map(r => r.source)).toEqual(['overview', 'release:member', 'release:historical'])
    expect(h.state.work['release:hidden']?.matches?.hidden_count).toBe(230); h.state.dispose()
  })
  it('preserves whole-query counts and cursor/query identity through child/history continuation', async () => {
    const h = harness(); h.state.reset(context); h.requests[0]!.resolve(overview()); await settle(); h.state.expand('0')
    const answer = page(200, 'first', 'next-200'); answer.matches = counts(450, 230)
    h.requests[1]!.resolve(answer); await settle(); h.state.moreWork('release:0'); expect(h.requests[2]).toMatchObject({ cursor: 'next-200', context, limit: 200 })
    h.requests[2]!.resolve({ ...page(10, 'later'), matches: answer.matches }); await settle()
    expect(h.state.work['release:0']?.items).toHaveLength(210); expect(h.state.work['release:0']?.matches?.hidden_count).toBe(230)
    h.state.more('released'); h.state.more('released'); expect(h.requests.filter(r => r.source === 'released')).toHaveLength(1)
    expect(h.requests.at(-1)).toMatchObject({ cursor: 'history-2', limit: 50 }); h.state.dispose()
  })
  it('discards collapse/reopen and filter/person races even when the transport ignores abort', async () => {
    const h = harness(); h.state.reset(context); h.requests[0]!.resolve(overview()); await settle(); h.state.expand('0')
    h.state.collapse('0'); expect(h.requests[1]!.signal.aborted).toBe(true); h.state.expand('0')
    h.requests[1]!.resolve(page(1, 'old')); await settle(); expect(h.state.rendered.value).toBe(0)
    h.state.reset({ ...context, query: `${context.query}&q=new` }); expect(h.requests[2]!.signal.aborted).toBe(true)
    h.requests[2]!.resolve(page(1, 'stale')); await settle(); expect(h.state.rendered.value).toBe(0)
    h.state.reset({ ...context, person: 'other' }); expect(h.state.overview.value).toBe(null); expect(h.state.expanded.size).toBe(0)
    h.requests[3]!.resolve(overview()); await settle(); expect(h.state.overview.value).toBe(null); h.state.dispose()
  })
  it('keeps an own committed move when a pre-commit continuation ignores abort', async () => {
    const h = harness(); h.state.reset(context); h.requests[0]!.resolve(overview()); await settle()
    h.state.expand('0'); h.state.expand('1')
    const original = { ...page(1, 'owned').items[0]!, release_id: '0' }
    h.requests[1]!.resolve({ ...page(0), items: [original], next_cursor: 'older-page' })
    h.requests[2]!.resolve(page(0)); await settle()
    h.state.moreWork('release:0'); const late = h.requests.at(-1)!
    h.state.committed({ kind: 'placement', result: { items: [{ ...original, release_id: '1', revision: 2 }], release_revision: 2, release_revisions: { '0': 2, '1': 2 }, undo_event_id: 42 } })
    expect(late.signal.aborted).toBe(true)
    late.resolve({ ...page(0), items: [original] }); await settle()
    expect(h.state.work['release:0']?.items).toHaveLength(0)
    expect(h.state.work['release:1']?.items.map(i => i.item_id)).toEqual([original.item_id])
    expect(h.state.overview.value?.active.map(r => r.revision)).toEqual([2, 2])
    expect(h.requests.filter(r => r.source === 'overview')).toHaveLength(1)
    h.state.dispose()
  })
  it('Undo restores an own row moved into a collapsed release without reading foreign changes', async () => {
    const h = harness(); h.state.reset(context); h.requests[0]!.resolve(overview()); await settle(); h.state.expand('0')
    const original = { ...page(1, 'owned').items[0]!, release_id: '0' }
    h.requests[1]!.resolve({ ...page(0), items: [original] }); await settle()
    h.state.committed({ kind: 'placement', result: { items: [{ ...original, release_id: '1', revision: 2 }], undo_event_id: 42 } })
    expect(h.state.work['release:0']?.items).toHaveLength(0); expect(h.state.work['release:1']).toBeUndefined()
    h.state.committed({ kind: 'placement', result: { items: [{ ...original, revision: 3 }], undo_event_id: null } })
    expect(h.state.work['release:0']?.items[0]?.revision).toBe(3)
    expect(h.state.overview.value?.active.map(r => r.rollup.units)).toEqual([1, 1])
    h.state.committed({ kind: 'lifecycle' })
    expect(h.requests).toHaveLength(2); h.state.dispose()
  })
  it('queues passive insertion until active controls leave or a fixed action applies it', async () => {
    let safe = true
    const h = harness(() => safe); h.state.reset(context); h.requests[0]!.resolve(overview()); await settle(); h.state.expand('0'); safe = false
    h.requests[1]!.resolve(page(200)); await settle(); expect(h.state.rendered.value).toBe(0); expect(h.state.pending.value).toBe(1)
    h.state.flush(); expect(h.state.rendered.value).toBe(0); safe = true; h.state.flush(); expect(h.state.rendered.value).toBe(200)
    h.state.expand('1'); safe = false; h.requests[2]!.resolve(page(1)); await settle(); h.state.flush(true); expect(h.state.rendered.value).toBe(201); h.state.dispose()
  })
  it('reports repeated cursors and stale refresh failures honestly', async () => {
    const h = harness(); h.state.reset(context); h.requests[0]!.resolve(overview()); await settle(); h.state.expand('0')
    h.requests[1]!.resolve(page(1, '', 'next')); await settle(); h.state.moreWork('release:0'); h.requests[2]!.resolve(page(1, 'bad', 'next')); await settle()
    expect(h.state.work['release:0']?.error).toMatch(/repeated/); expect(h.state.rendered.value).toBe(1)
    h.state.reset({ ...context, scope: 'none' }); h.requests[3]!.reject(new Error('Read refused')); await settle()
    expect(h.state.error.value).toBe('Read refused'); expect(h.state.stale.value).toBe(true); h.state.dispose()
  })
})
