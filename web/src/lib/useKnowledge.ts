// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { countBy, filterItems, groupItems, isKnowledgeType, listKnowledge, type KnowledgeItem, type KnowledgeStatus, type KnowledgeType, type SortBy } from './knowledge'
import { scopeParameter, scopeReadIdentity, type ReleaseScope } from './releaseScope'

export type StatusView = 'current' | 'proposed' | 'archived' | 'all'
export const STATUS_VIEWS: readonly { value: StatusView; label: string; statuses: KnowledgeStatus[] }[] = [
  { value: 'current', label: 'Current', statuses: ['active', 'proposed'] },
  { value: 'proposed', label: 'Proposed', statuses: ['proposed'] },
  { value: 'archived', label: 'Archived', statuses: ['archived'] },
  { value: 'all', label: 'All', statuses: [] },
]
export interface KnowledgeFilters { q: string; type: KnowledgeType | ''; status: StatusView; sort: SortBy | '' }
const text = (value: unknown) => typeof value === 'string' ? value : ''
export function filtersFromQuery(query: Record<string, unknown>): KnowledgeFilters {
  const status = text(query.status), sort = text(query.sort)
  return { q: text(query.q), type: isKnowledgeType(query.type) ? query.type : '',
    status: STATUS_VIEWS.some(view => view.value === status) ? status as StatusView : 'current',
    sort: ['updated', 'title', 'slug', 'relevance'].includes(sort) ? sort as SortBy : '' }
}
export function filtersToQuery(filters: KnowledgeFilters): Record<string, string> {
  const out: Record<string, string> = {}
  if (filters.q.trim()) out.q = filters.q.trim()
  if (filters.type) out.type = filters.type
  if (filters.status !== 'current') out.status = filters.status
  if (filters.sort) out.sort = filters.sort
  return out
}

// Scoped context is filtered/counts on the server before bounded keyset pages.
// The old unscoped API still supplies its compatible, explicitly truncated list.
export function useKnowledge(projectId: Ref<string | null>, filters: Ref<KnowledgeFilters>, active: Ref<boolean>, scope?: Ref<ReleaseScope>, person?: Ref<string>) {
  const items = ref<KnowledgeItem[]>([]), loaded = ref(false), loading = ref(false), error = ref(''), truncated = ref(false)
  const searching = ref(false), searched = ref(''), cursor = ref('')
  const serverTotal = ref(0), serverCounts = ref<{ type: Partial<Record<KnowledgeType, number>>; status: Partial<Record<KnowledgeStatus, number>> }>({ type: {}, status: {} })
  const scoped = computed(() => !!scope && scope.value.kind !== 'all')
  const identity = computed(() => JSON.stringify([scopeReadIdentity(projectId.value ?? '', person?.value ?? '', scope?.value ?? { kind: 'all' }), filters.value]))
  const statuses = computed(() => STATUS_VIEWS.find(view => view.value === filters.value.status)?.statuses ?? [])
  const types = computed<KnowledgeType[]>(() => filters.value.type ? [filters.value.type] : [])
  let generation = 0, controller: AbortController | null = null, timer: ReturnType<typeof setTimeout> | undefined
  let loadedFor = ''
  async function read(more = false) {
    const id = projectId.value, captured = identity.value
    if (!id || !active.value || (more && (!cursor.value || loading.value))) return
    if (more && items.value.length >= 2000) { error.value = '2,000 entries loaded. Narrow the search to reach more context.'; return }
    clearTimeout(timer); controller?.abort(); controller = new AbortController()
    const signal = controller.signal, request = ++generation, next = more ? cursor.value : ''
    loading.value = true; searching.value = !!filters.value.q.trim(); error.value = ''
    try {
      if (scope?.value.kind === 'repair') throw new Error('Choose a release scope. This saved view contains multiple or excluded release values.')
      const page = await listKnowledge({ project_id: id, ships_in: scope ? scopeParameter(scope.value) : undefined,
        q: filters.value.q, limit: scoped.value ? 200 : 1000, cursor: next || undefined,
        ...(scoped.value ? { type: types.value, status: statuses.value, sort: filters.value.sort || (filters.value.q.trim() ? 'relevance' : 'updated') } : {}) }, signal)
      if (signal.aborted || request !== generation || captured !== identity.value || !active.value) return
      if (!Array.isArray(page.items) || page.items.length > (scoped.value ? 200 : 1000)) throw new Error('Knowledge page exceeds the requested bound')
      if (next && page.next_cursor === next) throw new Error('The Knowledge cursor did not advance')
      const seen = new Set(more ? items.value.map(it => it.id) : [])
      items.value = [...(more ? items.value : []), ...page.items.filter(it => !seen.has(it.id))].slice(0, 2000)
      cursor.value = page.next_cursor ?? ''; truncated.value = page.truncated || !!page.counts_incomplete
      serverTotal.value = page.total; serverCounts.value = page.counts
      loaded.value = true; loadedFor = captured; searched.value = filters.value.q.trim()
    } catch (e) {
      if (!signal.aborted && request === generation && captured === identity.value) error.value = e instanceof Error ? e.message : 'Knowledge could not be loaded.'
    } finally {
      if (request === generation && captured === identity.value) { loading.value = false; searching.value = false }
    }
  }
  const load = () => read(false)
  const loadMore = () => read(true)
  const renderLimited = computed(() => !!cursor.value && items.value.length >= 2000)
  watch([identity, active], ([key, on], before) => {
    clearTimeout(timer); generation++; controller?.abort(); loading.value = false; searching.value = false
    if (key !== before?.[0]) { items.value = []; loaded.value = false; cursor.value = ''; truncated.value = false; serverTotal.value = 0; serverCounts.value = { type: {}, status: {} }; error.value = '' }
    if (on && projectId.value && key !== loadedFor) {
      if (filters.value.q.trim()) { searching.value = true; timer = setTimeout(() => void load(), 180) }
      else void load()
    }
  }, { immediate: true })
  const sortBy = computed<SortBy>(() => filters.value.sort || (filters.value.q.trim() ? 'relevance' : 'updated'))
  const visible = computed(() => filterItems(items.value, types.value, statuses.value))
  const groups = computed(() => groupItems(visible.value, sortBy.value))
  const sequence = computed(() => groups.value.flatMap(group => group.items))
  const typeCounts = computed(() => scoped.value ? serverCounts.value.type : countBy(filterItems(items.value, [], statuses.value)).type)
  const statusCounts = computed(() => scoped.value ? serverCounts.value.status : countBy(filterItems(items.value, types.value, [])).status)
  const total = computed(() => scoped.value ? serverTotal.value : filterItems(items.value, [], statuses.value).length)
  const slugs = (type: KnowledgeType) => items.value.filter(item => item.type === type).map(item => item.slug)
  function upsert(item: KnowledgeItem) {
    if (scoped.value) { void load(); return }
    const at = items.value.findIndex(existing => existing.id === item.id)
    items.value = at === -1 ? [{ ...item }, ...items.value] : items.value.map(existing => existing.id === item.id ? { ...item } : existing)
  }
  function remove(id: string) { items.value = items.value.filter(item => item.id !== id); if (scoped.value) void load() }
  function stop() { generation++; clearTimeout(timer); controller?.abort() }
  onScopeDispose(stop)
  return { renderLimited, items, loaded, loading, error, truncated, searching, searched, cursor, visible, groups, sequence, typeCounts, statusCounts, total, sortBy, load, loadMore, upsert, remove, slugs, stop }
}
export type KnowledgeState = ReturnType<typeof useKnowledge>
