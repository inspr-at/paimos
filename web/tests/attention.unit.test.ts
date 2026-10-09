// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it, vi } from 'vitest'
import { h, reactive, ref } from 'vue'
import { attentionFolds, attentionGrouping, orderAttentionGroups, orderAttentionRows, attentionIdentity, refreshAttentionRelease, mergeAttentionRows, collectAttentionGroups, finishAttentionGroups, foldAttentionGroups, ATTENTION_GROUP_CAP, ATTENTION_GROUP_PAGE_CAP, type AttentionGroup, type AttentionItem, type AttentionIdentity, type AttentionPage, type AttentionResult, type AttentionFilters } from '../src/lib/attention'
import * as attention from '../src/lib/attention'
import * as apiClient from '../src/lib/api.ts'
import * as statusAutopilot from '../src/lib/statusAutopilot'
import * as identityScope from '../src/lib/identityScope'
import { mountView, settle, textOf } from './webcore-view-harness'
import { displayLanguage } from '../src/lib/displayLanguage'

const identity = (event_id: number, extra: Partial<AttentionIdentity> = {}): AttentionIdentity => ({ event_id, node_id: `node-${event_id}`, revision: '2026-10-05T08:00:00Z', release_id: 'release', release_revision: 7, release_project_revision: 4, ...extra })
const receipt: AttentionResult = { event_id: 1, ok: true, release_id: 'release', previous_release_revision: 7, release_revision: 8, previous_release_project_revision: 4, release_project_revision: 5 }
it('refreshes matching sibling identities and sends both resource revisions on the next action', () => {
 const rows = [identity(1), identity(2), identity(3, { release_id: 'other' }), identity(4, { release_revision: 6 }), identity(5, { release_project_revision: 3 })]
 refreshAttentionRelease(rows, receipt)
 expect(rows.map(row => [row.release_revision, row.release_project_revision])).toEqual([[8,5], [8,5], [7,4], [6,4], [7,3]])
 expect(attentionIdentity(rows[1]!)).toMatchObject({ release_revision: 8, release_project_revision: 5 })
 refreshAttentionRelease(rows, { ...receipt, previous_release_revision: 8, release_revision: 9, previous_release_project_revision: 5, release_project_revision: 6 })
 expect(rows[1]).toMatchObject({ release_revision: 9, release_project_revision: 6 })
})
it('ignores failed or incomplete release receipts', () => {
 for (const result of [{ ...receipt, ok: false }, { ...receipt, release_revision: undefined }, { ...receipt, previous_release_project_revision: undefined }]) {
  const row = identity(1); refreshAttentionRelease([row], result)
  expect(row).toEqual(identity(1))
 }
})

it('orders every group by open flags and every row by kind then newest without mutating the visit', () => {
 const group = (id: string, total: number, key = id): AttentionGroup => ({ id, key, total, counts: {}, applicable: total, editable: total, can_manage: false })
 const groups = [group('last', 2), group('beta', 8), group('alpha', 8)]
 expect(orderAttentionGroups(groups, 'project').map(group => group.id)).toEqual(['alpha', 'beta', 'last'])
 expect(groups.map(group => group.id)).toEqual(['last', 'beta', 'alpha'])
 expect(orderAttentionGroups(['missed', 'triage', 'proposed', 'cancel', 'blocked'].map(id => group(id, 1)), 'kind').map(group => group.id)).toEqual(['proposed', 'triage', 'cancel', 'blocked', 'missed'])
 const item = (event_id: number, kind: AttentionItem['kind'], at: string) => ({ event_id, kind, at } as AttentionItem)
 expect(orderAttentionRows([item(3, 'missed', '2026-10-07'), item(1, 'triage', '2026-10-05'), item(2, 'triage', '2026-10-07')]).map(row => row.event_id)).toEqual([2, 1, 3])
})
it('uses saved per-group folds before big-list defaults and opens search matches without changing saved folds', () => {
 const groups = ['a', 'b', 'c'].map(id => ({ id } as AttentionGroup))
 expect(attentionGrouping(undefined)).toBe('project')
 expect(attentionGrouping('kind')).toBe('kind')
 expect(attentionGrouping('none')).toBe('none')
 expect([...attentionFolds(groups, 50)]).toEqual([])
 expect([...attentionFolds(groups, 51)]).toEqual(['b', 'c'])
 const saved = { a: true, b: false }
 expect([...attentionFolds(groups, 51, saved)]).toEqual(['a', 'c'])
 expect([...attentionFolds(groups, 51, saved, true)]).toEqual([])
 expect(saved).toEqual({ a: true, b: false })
})

const attentionItem = (event_id: number, kind: AttentionItem['kind'], at: string, project_id = `p-${event_id}`): AttentionItem => ({
 event_id, node_id: `node-${event_id}`, revision: '2026-10-05T08:00:00Z', key: `AEON-${event_id}`, title: `Ticket ${event_id}`,
 project_id, kind, from: 'new', to: 'backlog', reason: 'because', at, editable: true, applicable: true,
})
const attentionPage = (items: AttentionItem[], next_cursor: string | null, counts: AttentionPage['counts'] = {}): AttentionPage => ({
 items, total: items.length, counts, next_cursor, facets: { projects: [], assignees: [] }, facets_truncated: false,
})
const noFilters: AttentionFilters = { kind: '', project_id: '', assignee: '', q: '' }

it('keeps server order across a live cursor and sorts the loaded window only when the cursor ends', () => {
 const first = [attentionItem(1, 'missed', '2026-10-07', 'p'), attentionItem(2, 'triage', '2026-10-01', 'p')]
 const page = attentionPage(first, 'cursor')
 const open = mergeAttentionRows([], page)
 expect(open.map(row => row.event_id)).toEqual([1, 2])
 expect(page.items.map(row => row.event_id)).toEqual([1, 2])
 const done = mergeAttentionRows(open, attentionPage([
  attentionItem(3, 'proposed', '2026-10-06', 'p'), attentionItem(4, 'missed', '2026-10-02', 'p'),
 ], null))
 expect(done.map(row => row.event_id)).toEqual([3, 2, 1, 4])
 expect(mergeAttentionRows([], attentionPage(first, null)).map(row => row.event_id)).toEqual([2, 1])
})

it('builds capped groups from the list cursor and stops at the page cap', async () => {
 const many = Array.from({ length: ATTENTION_GROUP_CAP + 1 }, (_, index) => attentionItem(index + 1, 'triage', '2026-10-07', `p-${index}`))
 const folded = foldAttentionGroups(many, 'project')
 expect(folded.groups).toHaveLength(ATTENTION_GROUP_CAP)
 expect(folded.truncated).toBe(true)
 expect(many).toHaveLength(ATTENTION_GROUP_CAP + 1)

 let calls = 0
 const walked = await collectAttentionGroups('project', async () => {
  calls += 1
  return attentionPage([attentionItem(calls, 'triage', '2026-10-07', `p-${calls}`)], 'next')
 })
 expect(calls).toBe(ATTENTION_GROUP_PAGE_CAP)
 expect(walked.pages).toBe(ATTENTION_GROUP_PAGE_CAP)
 expect(walked.truncated).toBe(true)
 expect(walked.groups).toHaveLength(ATTENTION_GROUP_PAGE_CAP)

 const cursors: Array<string | undefined> = []
 let once = 0
 const capped = await collectAttentionGroups('project', async after => {
  cursors.push(after)
  once += 1
  return attentionPage([1, 2, 3].map(id => attentionItem(id, 'triage', '2026-10-07', `p-${id}`)), 'next')
 }, { cap: 2, pageCap: 4 })
 expect(once).toBe(1)
 expect(cursors).toEqual([undefined])
 expect(capped.groups.map(group => group.id)).toEqual(['p-1', 'p-2'])
 expect(capped.truncated).toBe(true)

 const followed: Array<string | undefined> = []
 await collectAttentionGroups('kind', async after => {
  followed.push(after)
  const id = followed.length
  return attentionPage([attentionItem(id, id === 1 ? 'triage' : 'cancel', '2026-10-07')], id === 1 ? 'page-2' : null)
 }, { pageCap: 2 })
 expect(followed).toEqual([undefined, 'page-2'])
})

it('fills kind totals from the list counts and project names from facets', () => {
 const scanned = foldAttentionGroups([attentionItem(1, 'triage', '2026-10-07', 'p-aeon')], 'kind')
 const counts = attentionPage([attentionItem(1, 'triage', '2026-10-07', 'p-aeon')], null, { triage: 4, cancel: 2 })
 const finished = finishAttentionGroups({ ...scanned, truncated: true }, counts, 'kind', noFilters)
 expect(finished.groups.map(group => [group.id, group.total])).toEqual([['triage', 4], ['cancel', 2]])
 expect(finished.groups.find(group => group.id === 'cancel')?.editable).toBe(1)
 const filtered = finishAttentionGroups(scanned, counts, 'kind', { ...noFilters, kind: 'triage' })
 expect(filtered.groups.map(group => group.id)).toEqual(['triage'])
 const named = finishAttentionGroups(foldAttentionGroups([attentionItem(1, 'triage', '2026-10-07', 'p-aeon')], 'project'), {
  ...attentionPage([], null), facets: { projects: [{ id: 'p-aeon', label: 'AEON Aeon' }], assignees: [] },
 }, 'project', noFilters)
 expect(named.groups[0]).toMatchObject({ id: 'p-aeon', key: 'AEON', title: 'Aeon' })
})

const fallbackItem = (event_id: number): AttentionItem => ({
 event_id, node_id: `node-${event_id}`, revision: '2026-10-05T08:00:00Z', key: `AEON-${event_id}`, title: `Ticket ${event_id}`,
 project_id: 'p-old', kind: 'triage', from: 'new', to: 'backlog', reason: 'because', at: '2026-10-07T08:00:00Z', editable: true, applicable: true,
})
const fallbackPage = (items: AttentionItem[], next_cursor: string | null): AttentionPage => ({
 items, total: 17, counts: { triage: 17 }, next_cursor, facets_truncated: false,
 facets: { projects: [{ id: 'p-old', label: 'OLD Old Project' }], assignees: [{ id: 'old-person', label: 'Previous assignee' }] },
})
const emptyFallbackPage: AttentionPage = { items: [], total: 0, counts: {}, next_cursor: null, facets: { projects: [], assignees: [] }, facets_truncated: false }

// The groups route is missing, so the view walks the list. The held page resolves
// and queues the visit change before that walk's caller resumes: a bare await
// then writes the old groups, counts and facets into the new visit.
async function fallbackVisit(change: 'stay' | 'identity' | 'filter') {
 const session = reactive({ identity: { tenant: { id: 't1' }, principal: { id: 'ada' } } })
 const route = reactive({ query: {} as Record<string, string>, fullPath: '/tickets' })
 const calls: { owner: string; q: string; project: string; cursor: string }[] = []
 const trace: string[] = []
 let scans = 0, changed = false
 let release!: (page: AttentionPage) => void
 const continued = new Promise<AttentionPage>(resolve => { release = resolve })
 function applyChange() {
  if (changed || change === 'stay') return
  changed = true
  trace.push('switch')
  if (change === 'identity') session.identity = { tenant: { id: 't1' }, principal: { id: 'grace' } }
  else route.query = { q: 'later' }
 }
 vi.stubGlobal('window', { addEventListener() {}, removeEventListener() {}, innerHeight: 800 })
 vi.stubGlobal('document', { documentElement: { lang: 'en' }, getElementById: () => null, querySelector: () => null })
 vi.stubGlobal('navigator', { platform: 'MacIntel', userAgent: 'test' })
 const view = mountView('../src/views/NeedsAttentionView.vue', {
  'vue-router': { useRoute: () => route, useRouter: () => ({ replace() {}, push() {} }) },
  '../lib/statusAutopilot': statusAutopilot,
  '../lib/attention': { ...attention, listAttentionGroups: async () => null, collectAttentionGroups: async (...args: Parameters<typeof collectAttentionGroups>) => {
   const result = await collectAttentionGroups(...args)
   scans += 1; trace.push('scanned'); return result
  }, listAttention: (filters: AttentionFilters, _signal: AbortSignal, cursor?: string) => {
   const owner = `${session.identity.tenant.id}/${session.identity.principal.id}`
   calls.push({ owner, q: filters.q, project: filters.project_id, cursor: cursor ?? '' })
   if (cursor) return continued.then(page => { trace.push('resolved'); if (change !== 'stay') queueMicrotask(applyChange); return page })
   if (!filters.project_id && owner === 't1/ada' && filters.q === '' && filters.kind === '') return Promise.resolve(fallbackPage([fallbackItem(1)], 'more'))
   if (!filters.project_id) return new Promise<AttentionPage>(() => {})
   return Promise.resolve(emptyFallbackPage)
  } },
  '../lib/identityScope': identityScope,
  '../lib/displayLanguage': { displayLanguage },
  '../lib/preferences': { usePreference: () => ({ value: ref(null), ready: Promise.resolve(), save() {} }) },
  '../lib/ticketList': { filtersFromQuery: () => ({}) },
  '../lib/toast': { toast: () => 1, dismiss() {}, toastBottomClearance: ref(0) },
  '../lib/work': { absoluteTime: (value: string) => value, relativeTime: () => '1d', statusMeta: (state: string) => ({ label: state }) },
  '../stores/session': { useSession: () => session },
  '../stores/projects': { useProjects: () => ({ load() {}, byId: () => null }) },
  '../directives/clipTip': { vClipTip: { mounted() {}, updated() {} } },
  '../components/work/TicketTable.vue': { __esModule: true, default: { props: ['groups'], setup(props: { groups?: { label?: string }[] }) { return () => h('div', (props.groups ?? []).map(group => group.label ?? '').join('|')) } } },
  '../components/work/ListToolbar.vue': { __esModule: true, default: { props: ['attention'], setup(props: { attention?: { facets?: { projects?: { label: string }[]; assignees?: { label: string }[] } } }, ctx: { expose: (bindings: object) => void }) { ctx.expose({ closeOverlays() {} }); return () => h('div', [...(props.attention?.facets?.projects ?? []), ...(props.attention?.facets?.assignees ?? [])].map(facet => facet.label).join('|')) } } },
 })
 try {
  await settle()
  expect(calls.some(call => call.cursor === 'more')).toBe(true)
  release(fallbackPage([fallbackItem(2)], null))
  await settle(); await settle()
  return { text: textOf(view.root), calls, trace, scans }
 } finally { view.app.unmount(); vi.unstubAllGlobals() }
}

it('a finished fallback group scan shows the scanned project, count and facets', async () => {
 const visit = await fallbackVisit('stay')
 expect(visit.scans).toBe(1)
 expect(visit.trace).toEqual(['resolved', 'scanned'])
 expect(visit.text).toContain('17')
 expect(visit.text).toContain('Old Project')
 expect(visit.text).toContain('Previous assignee')
 expect(visit.text).toContain('All projects')
 expect(visit.text).toContain('Needs attention collects what the autopilot flagged for a person.')
 expect(visit.text).not.toContain('Alle Projekte')
 expect(visit.text).not.toContain('Braucht Aufmerksamkeit')
})

it('a resolved fallback group scan cannot restore groups, counts or facets after an identity change', async () => {
 const visit = await fallbackVisit('identity')
 expect(visit.scans).toBe(1)
 expect(visit.trace.indexOf('switch')).toBeGreaterThanOrEqual(0)
 expect(visit.trace.indexOf('switch')).toBeLessThan(visit.trace.indexOf('scanned'))
 expect(visit.calls.some(call => call.owner === 't1/grace' && call.cursor === '')).toBe(true)
 expect(visit.text).not.toContain('17')
 expect(visit.text).not.toContain('Old Project')
 expect(visit.text).not.toContain('Previous assignee')
 expect(visit.text).toContain('All projects')
 expect(visit.text).not.toContain('Alle Projekte')
 expect(visit.text).not.toContain('Braucht Aufmerksamkeit')
})

it('a resolved fallback group scan cannot restore groups, counts or facets after a filter change', async () => {
 const visit = await fallbackVisit('filter')
 expect(visit.scans).toBe(1)
 expect(visit.trace.indexOf('switch')).toBeGreaterThanOrEqual(0)
 expect(visit.trace.indexOf('switch')).toBeLessThan(visit.trace.indexOf('scanned'))
 expect(visit.calls.some(call => call.q === 'later' && call.cursor === '')).toBe(true)
 expect(visit.text).not.toContain('17')
 expect(visit.text).not.toContain('Old Project')
 expect(visit.text).not.toContain('Previous assignee')
 expect(visit.text).toContain('All projects')
 expect(visit.text).not.toContain('Alle Projekte')
 expect(visit.text).not.toContain('Braucht Aufmerksamkeit')
})

type ResolutionEvent = { id: number; actor_principal_id: string; node_id: string; type: string; before?: { updated_at?: string }; after?: { updated_at?: string }; undo_of?: number }
// Production history is oldest-first and stops at 200. Without the resolution-type
// filter the resolution event is past that page; the snapshot spells the same
// instant with a numeric offset.
function resolutionPage(path: string, events: ResolutionEvent[]) {
 const url = new URL(path, 'http://aeon.local')
 const after = Number(url.searchParams.get('after'))
 const node = url.searchParams.get('node_id')
 const requested = (url.searchParams.get('type') ?? '').split(',').filter(Boolean)
 const typed = ['status_autopilot.attention_apply', 'status_autopilot.attention_dismiss', 'status_autopilot.attention_undone'].every(type => requested.includes(type))
 const noise: ResolutionEvent[] = typed ? [] : Array.from({ length: 201 }, (_, index) => ({
  id: after + 1 + index, actor_principal_id: 'person', node_id: node ?? '', type: 'node.updated',
  before: { updated_at: '2026-10-01T00:00:00+00:00' }, after: { updated_at: '2026-10-01T00:00:01+00:00' },
 }))
 const pool = [...noise, ...events].filter(event => event.id > after && event.node_id === node && (requested.length === 0 || requested.includes(event.type))).sort((a, b) => a.id - b.id)
 const items = pool.slice(0, 200)
 return { items, next_after: pool.length > 200 ? items.at(-1)!.id : null }
}
async function withHistory<T>(events: ResolutionEvent[], run: (calls: string[]) => Promise<T>) {
 const calls: string[] = []
 const spy = vi.spyOn(apiClient, 'api').mockImplementation(async (path: string) => {
  calls.push(path)
  return new Response(JSON.stringify(resolutionPage(path, events)), { status: 200 })
 })
 try { return await run(calls) } finally { spy.mockRestore() }
}
const resolutionTypes = 'status_autopilot.attention_apply,status_autopilot.attention_dismiss,status_autopilot.attention_undone'

it('binds an offset snapshot on a resolution-type page when ticket history is longer than 200 events', async () => {
 const revision = '2026-10-04T08:00:00.000001Z'
 const events: ResolutionEvent[] = [
  { id: 507, actor_principal_id: 'person', node_id: 'node-7', type: 'status_autopilot.attention_apply', before: { updated_at: '2026-10-04T08:00:00.000001+00:00' }, after: { updated_at: '2026-10-07T21:00:00.000004+00:00' } },
  { id: 508, actor_principal_id: 'person', node_id: 'node-7', type: 'status_autopilot.attention_apply', before: { updated_at: '2026-10-04T10:00:00.000002+02:00' }, after: { updated_at: '2026-10-07T21:00:00.000005+00:00' } },
  { id: 509, actor_principal_id: 'other', node_id: 'node-7', type: 'status_autopilot.attention_apply', before: { updated_at: '2026-10-04T08:00:00.000001+00:00' }, after: { updated_at: '2026-10-07T21:00:00.000006+00:00' } },
 ]
 const bound = await withHistory(events, async calls => {
  const result = await attention.attentionResolution({ event_id: 7, node_id: 'node-7', revision }, 'apply', 'person', new AbortController().signal)
  expect(decodeURIComponent(calls[0] ?? '')).toContain(`type=${resolutionTypes}`)
  expect(calls[0]).toContain('after=7')
  expect(calls[0]).toContain('limit=200')
  return result
 })
 expect(bound).toEqual({ event_id: 7, ok: true, resolution_event_id: 507, revision: '2026-10-07T21:00:00.000004+00:00' })
})

it('returns no row identity when the resolution page has no event for this revision', async () => {
 const events: ResolutionEvent[] = [
  { id: 507, actor_principal_id: 'person', node_id: 'node-7', type: 'status_autopilot.attention_dismiss', before: { updated_at: '2026-10-04T08:00:00.000002+00:00' }, after: { updated_at: '2026-10-07T21:00:00.000004+00:00' } },
 ]
 const missed = await withHistory(events, () => attention.attentionResolution({ event_id: 7, node_id: 'node-7', revision: '2026-10-04T08:00:00.000001Z' }, 'dismiss', 'person', new AbortController().signal))
 expect(missed).toBeNull()
})

it('binds undo from undo_of on the resolution-type page', async () => {
 const events: ResolutionEvent[] = [
  { id: 80, actor_principal_id: 'person', node_id: 'node-7', type: 'status_autopilot.attention_undone', before: { updated_at: '2026-10-07T23:00:00+02:00' }, after: { updated_at: '2026-10-04T08:00:00+00:00' }, undo_of: 42 },
  { id: 81, actor_principal_id: 'person', node_id: 'node-7', type: 'status_autopilot.attention_undone', before: { updated_at: '2026-10-04T08:00:00Z' }, after: { updated_at: '1999-01-01T00:00:00Z' }, undo_of: 41 },
 ]
 const undone = await withHistory(events, async calls => {
  const result = await attention.attentionResolution({ event_id: 7, node_id: 'node-7', revision: '2026-10-04T08:00:00Z', resolution_event_id: 42 }, 'undo', 'person', new AbortController().signal)
  expect(decodeURIComponent(calls[0] ?? '')).toContain('after=42')
  return result
 })
 expect(undone).toEqual({ event_id: 7, ok: true, revision: '2026-10-04T08:00:00+00:00' })
})
