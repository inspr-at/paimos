// SPDX-License-Identifier: AGPL-3.0-only
import { effectScope, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { useKnowledge, filtersFromQuery } from '../src/lib/useKnowledge'
import { listKnowledge, type KnowledgePage, type KnowledgeItem } from '../src/lib/knowledge'
import { releaseScope, type ReleaseScope } from '../src/lib/releaseScope'
import { filtersFromQuery as ticketFilters, filtersFromView, filtersToQuery } from '../src/lib/ticketList'
import type { SavedView } from '../src/lib/api'
vi.mock('../src/lib/knowledge', async original => ({ ...await original<object>(), listKnowledge: vi.fn() }))
afterEach(() => vi.resetAllMocks())
const page = (id: string, cursor = ''): KnowledgePage => ({ items: [{ id, type: 'runbook', status: 'active', updated_at: '2026-10-03T12:00:00Z', title: id, slug: id, key: id } as KnowledgeItem], total: 250, next_cursor: cursor, truncated: false, counts: { type: { runbook: 250 }, status: { active: 250 } } })
const flush = async () => { await nextTick(); await nextTick() }
it('an inactive refresh invalidates identical-filter Knowledge without fetching until reactivation', async () => {
  vi.mocked(listKnowledge).mockResolvedValueOnce(page('deleted-note')).mockResolvedValueOnce({ ...page('unused'), items: [], total: 0, counts: { type: {}, status: {} } })
  const scoped = effectScope(), active = ref(true), filters = ref(filtersFromQuery({}))
  const state = scoped.run(() => useKnowledge(ref('project'), filters, active, ref<ReleaseScope>({ kind: 'backlog' }), ref('person')))!
  await flush(); expect(state.items.value.map(item => item.id)).toEqual(['deleted-note'])
  active.value = false; await flush()
  await state.load()
  expect(listKnowledge).toHaveBeenCalledTimes(1)
  expect(state.items.value.map(item => item.id)).toEqual(['deleted-note'])
  active.value = true; await flush()
  expect(listKnowledge).toHaveBeenCalledTimes(2)
  expect(state.items.value).toEqual([]); expect(state.total.value).toBe(0)
  expect(state.typeCounts.value).toEqual({}); expect(state.loaded.value).toBe(true)
  scoped.stop()
})
it('restores Knowledge after a Tickets round trip with different per-tab filters', async () => {
  vi.mocked(listKnowledge).mockResolvedValue(page('retained-note'))
  const scoped = effectScope(), active = ref(true), filters = ref(filtersFromQuery({ type: 'runbook' }))
  const state = scoped.run(() => useKnowledge(ref('project'), filters, active, ref<ReleaseScope>({ kind: 'backlog' }), ref('person')))!
  await flush()
  expect(state.items.value.map(item => item.id)).toEqual(['retained-note'])
  active.value = false; filters.value = filtersFromQuery({}); await flush()
  expect(state.items.value).toEqual([])
  filters.value = filtersFromQuery({ type: 'runbook' }); active.value = true; await flush()
  expect(listKnowledge).toHaveBeenCalledTimes(2)
  expect(state.items.value.map(item => item.id)).toEqual(['retained-note'])
  expect(state.loaded.value).toBe(true); expect(state.loading.value).toBe(false); expect(state.error.value).toBe('')
  scoped.stop()
})
it('scoped reads intersect own filters/counts on 200-row pages and load more explicitly', async () => {
  vi.mocked(listKnowledge).mockResolvedValueOnce(page('first', 'next')).mockResolvedValueOnce(page('second'))
  const scoped = effectScope(), project = ref<string | null>('project'), filters = ref(filtersFromQuery({ status: 'all', type: 'runbook' })), active = ref(true), scope = ref<ReleaseScope>({ kind: 'backlog' })
  const state = scoped.run(() => useKnowledge(project, filters, active, scope, ref('tenant:person')))!
  await flush(); expect(listKnowledge).toHaveBeenCalledOnce(); expect(listKnowledge).toHaveBeenLastCalledWith(expect.objectContaining({ ships_in: 'none', limit: 200, type: ['runbook'], status: [] }), expect.any(AbortSignal))
  expect(state.total.value).toBe(250); expect(state.cursor.value).toBe('next')
  await state.loadMore(); expect(state.items.value.map(it => it.id)).toEqual(['first', 'second']); expect(state.cursor.value).toBe(''); scoped.stop()
})
it('drops noncooperative late scope/person responses and resets the previous population', async () => {
  let old!: (page: KnowledgePage) => void
  vi.mocked(listKnowledge).mockImplementationOnce(() => new Promise(resolve => { old = resolve })).mockResolvedValueOnce(page('new'))
  const scoped = effectScope(), person = ref('old'), scope = ref<ReleaseScope>({ kind: 'backlog' })
  const state = scoped.run(() => useKnowledge(ref('project'), ref(filtersFromQuery({})), ref(true), scope, person))!
  person.value = 'new'; await flush(); old(page('old')); await flush()
  expect(state.items.value.map(it => it.id)).toEqual(['new']); expect(state.error.value).toBe(''); scoped.stop()
})
it('repairs ambiguous/excluded saved scopes visibly without issuing all-work requests', async () => {
  const scoped = effectScope()
  const state = scoped.run(() => useKnowledge(ref('project'), ref(filtersFromQuery({})), ref(true), ref(releaseScope(['none','!abc'])), ref('person')))!
  await flush(); expect(listKnowledge).not.toHaveBeenCalled(); expect(state.error.value).toContain('Choose a release scope'); expect(state.items.value).toEqual([]); scoped.stop()
})
it('saved single UUID/none scopes preserve unrelated ticket filters without a second membership facet', () => {
  for (const ships_in of ['none', '11111111-1111-4111-8111-111111111111']) {
    const view = { filters: { ships_in, assignee: 'mira', status: 'open' }, sort: { field: 'position', direction: 'asc' }, sort_keys: [], group_by: 'none', columns: [] } as unknown as SavedView
    const migrated = filtersFromView(view), query = filtersToQuery(migrated)
    expect(releaseScope(migrated.ships_in).kind).toBe(ships_in === 'none' ? 'backlog' : 'release')
    expect(ticketFilters(query).assignee).toEqual(['mira']); expect(query.ships_in).toBe(ships_in)
  }
})

it('bounds explicitly loaded Knowledge context to 2,000 rows and retains honest total/cursor', async () => {
  let number=0
  vi.mocked(listKnowledge).mockImplementation(async()=>{
    number++
    return {...page('unused',`cursor-${number}`),total:5000,items:Array.from({length:200},(_,i)=>({...page('unused').items[0]!,id:`${number}-${i}`}))}
  })
  const scoped=effectScope(),state=scoped.run(()=>useKnowledge(ref('project'),ref(filtersFromQuery({})),ref(true),ref<ReleaseScope>({kind:'backlog'}),ref('person')))!
  await flush();for(let n=1;n<10;n++) await state.loadMore()
  expect(state.items.value).toHaveLength(2000);expect(state.renderLimited.value).toBe(true);expect(state.total.value).toBe(5000)
  await state.loadMore();expect(listKnowledge).toHaveBeenCalledTimes(10);expect(state.error.value).toContain('2,000');scoped.stop()
})
