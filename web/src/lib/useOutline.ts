// SPDX-License-Identifier: AGPL-3.0-only
import { computed, reactive, ref, watch, type Ref } from 'vue'
import { getNode, listNodes, type ListItem } from './api'
import { childMap, epicStats, flattenOutline, missingAncestors, type EpicStats, type OutlineEntry } from './outline'
import { compareRows, effectiveSort, hasFilters, type ListFilters } from './ticketList'
import { rowStore } from './rowStore'
import { asListItem, kinds } from './useTicket'
import { serializeSort, statusMeta } from './work'

interface Block { ids: string[]; cursor: string | null; loading: boolean; error: string }
type ListApi = { rows: Ref<ListItem[]>; loading: Ref<boolean>; names: Map<string, string> }

// Expansion is remembered per project for the session (memory, not device storage).
const expandedByProject = new Map<string, Set<string>>()
const LEVEL = 200

// The project Outline. Without search or filters and with closed work shown, levels
// load lazily as they open. Otherwise the whole match set is known (it is the list's
// query), so the tree is those rows plus the ancestors that place them.
export function useOutline(projectId: Ref<string | null>, filters: Ref<ListFilters>, active: Ref<boolean>, list: ListApi) {
  const matchMode = computed(() => hasFilters(filters.value) || !filters.value.showClosed)
  const autoExpand = computed(() => hasFilters(filters.value))
  const sortKeys = computed(() => effectiveSort({ ...filters.value, group: 'none' }))
  const sortParam = computed(() => serializeSort(sortKeys.value))
  const compare = computed(() => compareRows(sortKeys.value))

  const expanded = ref(new Set<string>())
  const autoCollapsed = ref(new Set<string>())
  const noEpicCollapsed = ref(false)
  const createUnder = ref<string | null>(null)
  const stats = reactive(new Map<string, EpicStats | 'loading'>())

  // Lazy mode state.
  const lazyNodes = reactive(new Map<string, ListItem>())
  const blocks = reactive(new Map<string, Block>())
  const epicBlock = ref<Block>({ ids: [], cursor: null, loading: false, error: '' })
  const looseBlock = ref<Block>({ ids: [], cursor: null, loading: false, error: '' })
  // Match mode: ancestors fetched to place matching rows.
  const ancestors = reactive(new Map<string, ListItem>())
  const resolving = ref(false)
  let generation = 0
  let ancestorRun = 0

  watch(projectId, id => {
    expanded.value = id ? (expandedByProject.get(id) ?? new Set()) : new Set()
    if (id && !expandedByProject.has(id)) expandedByProject.set(id, expanded.value)
    stats.clear(); ancestors.clear()
  }, { immediate: true })
  watch(() => JSON.stringify({ ...filters.value, sort: null, group: null, cols: null, view: null }), () => { autoCollapsed.value = new Set() })
  function persist() { if (projectId.value) expandedByProject.set(projectId.value, expanded.value) }

  // ---------- Match mode ----------
  const matchIds = computed(() => new Set(list.rows.value.map(row => row.id)))
  const matchNodes = computed(() => {
    const map = new Map<string, ListItem>()
    for (const row of list.rows.value) map.set(row.id, row)
    for (const [id, row] of ancestors) if (!map.has(id)) map.set(id, row)
    return map
  })
  const matchChildren = computed(() => childMap(matchNodes.value.values(), compare.value))
  async function resolveAncestors() {
    const root = projectId.value
    if (!root || !active.value || !matchMode.value) return
    const request = ++ancestorRun
    resolving.value = true
    try {
      const all = await kinds()
      for (let round = 0; round < 8; round++) {
        const missing = missingAncestors(matchNodes.value, root)
        if (!missing.length || request !== ancestorRun) break
        const sent = rowStore.mark()
        const fetched = await Promise.all(missing.map(id => getNode(id).catch(() => null)))
        for (const node of fetched) {
          if (!node) continue
          const kind = all.find(k => k.id === node.kind_id)
          if (!kind) continue
          const item = asListItem(node, kind, null, null)
          const assignee = typeof node.fields.assignee === 'string' ? node.fields.assignee : null
          if (assignee) item.assignee = { id: assignee, name: list.names.get(assignee) ?? 'Someone' }
          // The row store's display object (the panel edits and moves that
          // one); one the store knows deleted is not placed.
          const row = rowStore.adopt(item, sent, { full: false })
          if (row) ancestors.set(node.id, row)
        }
        // Parents that cannot be read would loop forever; place their children at the top.
        for (const id of missing) if (!ancestors.has(id)) ancestors.set(id, { id, key: '', title: '', parent_id: root, kind_slug: 'missing' } as unknown as ListItem)
      }
    } finally { if (request === ancestorRun) resolving.value = false }
  }
  watch([() => list.rows.value.length, () => list.loading.value, matchMode, active, projectId], () => {
    if (active.value && matchMode.value && !list.loading.value) void resolveAncestors()
  })

  // ---------- Lazy mode ----------
  async function loadBlock(target: Ref<Block> | Block, params: Record<string, unknown>, more = false) {
    const block = 'value' in target ? target.value : target
    const request = generation
    block.loading = true; block.error = ''
    try {
      const sent = rowStore.mark()
      const page = await listNodes({ ...params, sort: sortParam.value, limit: LEVEL, cursor: more ? block.cursor ?? undefined : undefined } as never)
      if (request !== generation) return
      // Rows are the row store's display objects (AEON-326): the panel and the
      // list show the same object, and a copy older than a deletion stays out.
      const ids: string[] = []
      for (const item of page.items) {
        const row = rowStore.adopt(item, sent, { show: true })
        if (!row) continue
        lazyNodes.set(item.id, row)
        ids.push(item.id)
      }
      block.ids = more ? [...block.ids, ...ids.filter(id => !block.ids.includes(id))] : ids
      block.cursor = page.next_cursor
    } catch (e) {
      if (request === generation) block.error = e instanceof Error ? e.message : 'Could not load'
    } finally { if (request === generation) block.loading = false }
  }
  async function loadChildren(id: string, more = false) {
    if (!blocks.has(id)) blocks.set(id, { ids: [], cursor: null, loading: false, error: '' })
    const block = blocks.get(id)!
    if (block.loading || (!more && block.ids.length)) return
    await loadBlock(block, { parent_id: id, kind: ['ticket', 'task', 'epic'] }, more)
  }
  async function loadRoots() {
    const root = projectId.value
    if (!root || !active.value || matchMode.value) return
    generation++
    lazyNodes.clear(); blocks.clear()
    epicBlock.value = { ids: [], cursor: null, loading: true, error: '' }
    looseBlock.value = { ids: [], cursor: null, loading: true, error: '' }
    await Promise.all([
      loadBlock(epicBlock, { parent_id: root, kind: ['epic'] }),
      loadBlock(looseBlock, { parent_id: root, kind: ['ticket', 'task'] }),
    ])
    while (epicBlock.value.cursor && !epicBlock.value.error) await loadBlock(epicBlock, { parent_id: root, kind: ['epic'] }, true)
    // Reopen what was expanded before, level by level.
    let frontier = [...expanded.value].filter(id => lazyNodes.has(id))
    for (let depth = 0; depth < 6 && frontier.length; depth++) {
      await Promise.all(frontier.map(id => loadChildren(id)))
      frontier = [...expanded.value].filter(id => lazyNodes.has(id) && !blocks.has(id))
    }
  }
  watch([active, matchMode, projectId, sortParam], () => { generation++; if (active.value && !matchMode.value) void loadRoots() }, { immediate: true })
  function loadMoreRoot() { if (looseBlock.value.cursor && !looseBlock.value.loading) void loadBlock(looseBlock, { parent_id: projectId.value, kind: ['ticket', 'task'] }, true) }

  // ---------- Epic progress ----------
  const statsQueue: string[] = []
  let statsRunning = 0
  function pumpStats() {
    while (statsRunning < 4 && statsQueue.length) {
      const id = statsQueue.shift()!
      statsRunning++
      listNodes({ within: id, kind: ['ticket', 'task'], facets: ['state'], limit: 1 })
        .then(page => stats.set(id, epicStats(page.facets?.state ?? {}, s => statusMeta(s).closed, s => ['done', 'delivered', 'accepted'].includes(statusMeta(s).key), s => statusMeta(s).key === 'cancelled')))
        .catch(() => stats.delete(id))
        .finally(() => { statsRunning--; pumpStats() })
    }
  }
  function ensureStats(id: string, force = false) {
    if (!force && stats.has(id)) return
    stats.set(id, 'loading')
    if (!statsQueue.includes(id)) statsQueue.push(id)
    pumpStats()
  }

  // ---------- Tree ----------
  const node = (id: string) => matchMode.value ? matchNodes.value.get(id) : lazyNodes.get(id)
  const roots = computed(() => {
    if (!matchMode.value) return { epics: epicBlock.value.ids, loose: looseBlock.value.ids }
    // Rows whose parent could not be read are placed at the top instead of vanishing.
    const top: ListItem[] = []
    for (const row of matchChildren.value.get(projectId.value ?? '') ?? []) {
      if (row.kind_slug === 'missing') top.push(...(matchChildren.value.get(row.id) ?? []))
      else top.push(row)
    }
    top.sort(compare.value)
    return { epics: top.filter(row => row.kind_slug === 'epic').map(row => row.id), loose: top.filter(row => row.kind_slug !== 'epic' && row.kind_slug !== 'missing').map(row => row.id) }
  })
  function isExpanded(id: string) { return autoExpand.value ? !autoCollapsed.value.has(id) : expanded.value.has(id) }
  function hasChildren(id: string) {
    if (matchMode.value) return (matchChildren.value.get(id)?.length ?? 0) > 0
    return (lazyNodes.get(id)?.children_count ?? 0) > 0 || (blocks.get(id)?.ids.length ?? 0) > 0
  }
  const entries = computed<OutlineEntry[]>(() => flattenOutline({
    rootId: projectId.value ?? '',
    epics: roots.value.epics,
    loose: roots.value.loose,
    looseHasMore: !matchMode.value && !!looseBlock.value.cursor,
    looseLoading: looseBlock.value.loading,
    node,
    children: id => {
      if (matchMode.value) return { ids: (matchChildren.value.get(id) ?? []).map(row => row.id), hasMore: false, loading: false }
      const block = blocks.get(id)
      return block ? { ids: block.ids, hasMore: !!block.cursor, loading: block.loading } : null
    },
    hasChildren,
    expanded: isExpanded,
    dimmed: id => matchMode.value && !matchIds.value.has(id),
    stats: id => { const value = stats.get(id); return value && value !== 'loading' ? value : null },
    noEpicCollapsed: noEpicCollapsed.value,
    createUnder: createUnder.value,
  }))
  watch(() => roots.value.epics, ids => { if (active.value) for (const id of ids) ensureStats(id) }, { immediate: true })
  const rows = computed(() => entries.value.flatMap(entry => entry.type === 'row' ? [entry.row] : []))
  // The first page shows once both blocks have their first rows, so epics never
  // arrive above tickets that are already on screen.
  const loading = computed(() => matchMode.value ? (list.loading.value || (resolving.value && !entries.value.length)) : (epicBlock.value.loading && !epicBlock.value.ids.length) || (looseBlock.value.loading && !looseBlock.value.ids.length))
  const error = computed(() => matchMode.value ? '' : epicBlock.value.error || looseBlock.value.error)

  // ---------- Actions ----------
  function setExpanded(id: string, open: boolean) {
    if (autoExpand.value) {
      const next = new Set(autoCollapsed.value)
      if (open) next.delete(id); else next.add(id)
      autoCollapsed.value = next
    } else {
      const next = new Set(expanded.value)
      if (open) next.add(id); else next.delete(id)
      expanded.value = next; persist()
    }
    if (open && !matchMode.value) void loadChildren(id)
  }
  function toggle(id: string) { setExpanded(id, !isExpanded(id)) }
  async function expandAll() {
    if (matchMode.value) {
      if (autoExpand.value) autoCollapsed.value = new Set()
      else { expanded.value = new Set([...matchChildren.value.keys()].filter(id => matchNodes.value.has(id))); persist() }
      noEpicCollapsed.value = false
      return
    }
    noEpicCollapsed.value = false
    let frontier = [...epicBlock.value.ids, ...looseBlock.value.ids].filter(id => hasChildren(id))
    for (let depth = 0; depth < 6 && frontier.length; depth++) {
      const next = new Set(expanded.value); for (const id of frontier) next.add(id); expanded.value = next; persist()
      await Promise.all(frontier.map(id => loadChildren(id)))
      frontier = frontier.flatMap(id => blocks.get(id)?.ids ?? []).filter(id => hasChildren(id) && !expanded.value.has(id))
    }
  }
  function collapseAll() {
    if (autoExpand.value) autoCollapsed.value = new Set([...matchChildren.value.keys()])
    else { expanded.value = new Set(); persist() }
  }
  function parentOf(id: string) { return node(id)?.parent_id ?? null }
  function epicOf(id: string): string | null {
    let current = node(id)
    for (let i = 0; current && i < 8; i++) {
      if (current.kind_slug === 'epic') return current.id
      current = current.parent_id ? node(current.parent_id) : undefined
    }
    return null
  }
  function refreshStatsFor(id: string) { const epic = epicOf(id); if (epic) ensureStats(epic, true) }
  // Lazy mode keeps its own rows; created, moved and deleted work is placed at once.
  // Rows are the row store's display objects, and a parent's children count
  // follows through the store once per child (the panel may have counted it).
  function insert(item: ListItem) {
    if (matchMode.value) { refreshStatsFor(item.id); return }
    const row = rowStore.row(item.id) ?? rowStore.adopt(item, undefined, { full: false })
    if (!row) return
    lazyNodes.set(item.id, row)
    const parent = row.parent_id ?? ''
    const block = parent === projectId.value ? (row.kind_slug === 'epic' ? epicBlock.value : looseBlock.value) : blocks.get(parent)
    if (block && !block.ids.includes(item.id)) block.ids = [item.id, ...block.ids]
    if (lazyNodes.has(parent)) rowStore.child(parent, item.id, true)
    refreshStatsFor(item.id)
  }
  function detach(item: ListItem, fromParent: string | null) {
    const parent = fromParent ?? ''
    for (const block of [epicBlock.value, looseBlock.value, blocks.get(parent)]) if (block) block.ids = block.ids.filter(id => id !== item.id)
    if (lazyNodes.has(parent)) rowStore.child(parent, item.id, false)
  }
  function relocate(item: ListItem, fromParent: string | null, fromEpic: string | null) {
    if (fromEpic) ensureStats(fromEpic, true)
    if (matchMode.value) { refreshStatsFor(item.id); return }
    detach(item, fromParent)
    insert(item)
  }
  function remove(item: ListItem) {
    const epic = epicOf(item.id)
    if (!matchMode.value) { detach(item, item.parent_id); lazyNodes.delete(item.id) }
    if (epic && epic !== item.id) ensureStats(epic, true)
  }
  function findByKey(key: string) {
    const wanted = key.toLowerCase()
    for (const map of [lazyNodes, ancestors]) for (const item of map.values()) if (item.key.toLowerCase() === wanted) return item
    return undefined
  }
  // Open the chain above a row so it can be seen (the parent must already be known).
  function reveal(id: string) {
    const chain: string[] = []
    let parent = parentOf(id)
    for (let i = 0; parent && parent !== projectId.value && i < 8; i++) { chain.push(parent); parent = parentOf(parent) }
    for (const ancestor of chain.reverse()) if (!isExpanded(ancestor)) setExpanded(ancestor, true)
  }
  function startCreateUnder(id: string | null) {
    createUnder.value = id
    if (id && !isExpanded(id)) setExpanded(id, true)
  }

  return {
    matchMode, autoExpand, entries, rows, loading, error, node, isExpanded, hasChildren, noEpicCollapsed, createUnder,
    toggle, setExpanded, expandAll, collapseAll, loadChildren, loadMoreRoot, parentOf, epicOf, insert, relocate, remove, findByKey, reveal, startCreateUnder,
    refreshStatsFor, hasMoreRoot: computed(() => !matchMode.value && !!looseBlock.value.cursor), loadingMoreRoot: computed(() => looseBlock.value.loading && !!looseBlock.value.ids.length),
    reload: () => { generation++; if (matchMode.value) void resolveAncestors(); else void loadRoots() },
  }
}
