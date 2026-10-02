// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, reactive, ref, watch, type Ref } from 'vue'
import { getNode, listNodes, type ListItem, type ListPage, type ListQuery } from './api'
import { childMap, epicStats, flattenOutline, missingAncestors, type EpicStats, type OutlineEntry } from './outline'
import { apiParams, compareRows, effectiveSort, hasFilters, type ListFilters } from './ticketList'
import { rowStore } from './rowStore'
import { asListItem, kinds } from './useTicket'
import { serializeSort, statusMeta } from './work'
import type { LiveNodeStore } from './liveNodes'
import { placeKey, useLiveList, type ListRead, type LiveListOptions, type LiveReadPurpose } from './useLiveList'

interface Block { ids: string[]; cursor: string | null; loading: boolean; error: string; paged?: Set<string> }
type ListApi = { rows: Ref<ListItem[]>; loading: Ref<boolean>; names: Map<string, string>; reads?: Ref<ListRead | null>; load?: () => unknown }
type LiveOptions = Partial<Pick<LiveListOptions, 'me' | 'quiet' | 'holds' | 'blockers' | 'around' | 'applied' | 'env'>> & { store?: LiveNodeStore; fetchList?: (query: ListQuery) => Promise<ListPage> }

// Expansion is remembered per project for the session (memory, not device storage).
const expandedByProject = new Map<string, Set<string>>()
const LEVEL = 200

// The project Outline. Without search or filters and with closed work shown, levels
// load lazily as they open. Otherwise the whole match set is known (it is the list's
// query), so the tree is those rows plus the ancestors that place them.
export function useOutline(projectId: Ref<string | null>, filters: Ref<ListFilters>, active: Ref<boolean>, list: ListApi, options: LiveOptions = {}) {
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
  const lazyRead = ref<ListRead | null>(null)
  const loadedOnce = ref(false)
  // Filter membership learned by live queries is committed with their rows.
  const matched = new Map<string, boolean>()
  // Immutable sort values keep a field patch (including updated_at) in its
  // place, including when Show applies an unrelated structural update.
  const order = reactive(new Map<string, ListItem>())

  watch(projectId, id => {
    expanded.value = id ? (expandedByProject.get(id) ?? new Set()) : new Set()
    if (id && !expandedByProject.has(id)) expandedByProject.set(id, expanded.value)
    stats.clear(); ancestors.clear(); matched.clear(); order.clear(); ancestorRun++
  }, { immediate: true })
  watch(() => JSON.stringify({ ...filters.value, sort: null, group: null, cols: null, view: null }), () => {
    autoCollapsed.value = new Set(); generation++; ancestorRun++; matched.clear()
  })
  function persist() { if (projectId.value) expandedByProject.set(projectId.value, expanded.value) }

  // ---------- Match mode ----------
  const matchIds = computed(() => new Set(list.rows.value.map(row => row.id)))
  const matchNodes = computed(() => {
    const map = new Map<string, ListItem>()
    for (const row of list.rows.value) map.set(row.id, row)
    for (const [id, row] of ancestors) if (!map.has(id)) map.set(id, row)
    return map
  })
  const placedNodes = computed(() => new Map([...matchNodes.value].map(([id, row]) => [id, live.layout(row)])))
  const comparePlaced = (a: ListItem, b: ListItem) => compare.value(order.get(a.id) ?? a, order.get(b.id) ?? b)
  const matchChildren = computed(() => childMap(placedNodes.value.values(), comparePlaced))
  function rememberOrder(items: Iterable<ListItem>, replace = false) {
    if (replace) order.clear()
    for (const row of items) if (!order.has(row.id)) order.set(row.id, rowStore.shown(row.id) ?? { ...row })
  }
  watch(() => list.rows.value, items => rememberOrder(items), { flush: 'sync', immediate: true })
  watch(() => list.reads?.value, read => { if (read?.kind === 'load') rememberOrder(matchNodes.value.values(), true) })
  watch(sortParam, () => rememberOrder(matchNodes.value.values(), true))
  async function resolveAncestors() {
    const root = projectId.value
    if (!root || !active.value || !matchMode.value) return
    const request = ++ancestorRun
    resolving.value = true
    try {
      const all = await kinds()
      for (let round = 0; round < 8; round++) {
        const missing = missingAncestors(placedNodes.value, root)
        if (!missing.length || request !== ancestorRun) break
        const sent = rowStore.mark()
        const fetched = await Promise.all(missing.map(id => getNode(id).catch(() => null)))
        if (request !== ancestorRun || !active.value || !matchMode.value) return
        if (rowStore.gapSince(sent)) { round--; continue }
        for (const node of fetched) {
          if (!node) continue
          const kind = all.find(k => k.id === node.kind_id)
          if (!kind) continue
          const item = asListItem(node, kind, null, null)
          const assignee = typeof node.fields.assignee === 'string' ? node.fields.assignee : null
          if (assignee) item.assignee = { id: assignee, name: list.names.get(assignee) ?? 'Someone' }
          // The row store's display object (the panel edits and moves that
          // one); one the store knows deleted is not placed.
          const row = rowStore.adopt(item, sent, { full: false, show: true })
          if (row) { rememberOrder([row]); ancestors.set(node.id, row) }
        }
        // Parents that cannot be read would loop forever; place their children at the top.
        for (const id of missing) if (!ancestors.has(id)) ancestors.set(id, { id, key: '', title: '', parent_id: root, kind_slug: 'missing' } as unknown as ListItem)
      }
      // Keep only the chains needed by the current match set. Old parents
      // must not survive a move as unrelated, dimmed roots.
      const needed = new Set<string>()
      for (const item of list.rows.value) {
        let parent = live.layout(item).parent_id
        for (let depth = 0; parent && parent !== root && depth < 8 && !needed.has(parent); depth++) {
          needed.add(parent)
          const row = ancestors.get(parent) ?? matchNodes.value.get(parent)
          parent = row ? live.layout(row).parent_id : null
        }
      }
      for (const id of ancestors.keys()) if (!needed.has(id)) ancestors.delete(id)
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
      // Keep the same level and cursor when a gap makes an in-flight page
      // untrustworthy. It may contain a deletion the stream never delivered.
      const query = { ...params, sort: sortParam.value, limit: LEVEL, cursor: more ? block.cursor ?? undefined : undefined }
      let sent, page
      do {
        sent = rowStore.mark()
        page = await listNodes(query as never)
        if (request !== generation) return
      } while (rowStore.gapSince(sent))
      // Rows are the row store's display objects (AEON-326): the panel and the
      // list show the same object, and a copy older than a deletion stays out.
      const ids: string[] = []
      for (const item of page.items) {
        const row = rowStore.adopt(item, sent, { show: true })
        if (!row || live.layout(row).parent_id !== params.parent_id) continue
        rememberOrder([row])
        lazyNodes.set(item.id, row)
        ids.push(item.id)
      }
      // Show or a local move may already have placed rows while this first
      // page was in flight. Its older snapshot cannot erase that placement.
      const retained = block.ids.filter(id => {
        const row = lazyNodes.get(id)
        return row && live.layout(row).parent_id === params.parent_id
      })
      // Merge new page rows by the requested sort keys. Keep both the server's
      // page order (its collation may differ) and existing held placements.
      const known = new Set(retained), merged: string[] = []
      // Earlier pages precede this one even when a browser would collate their
      // keys differently. Only rows placed locally beyond that edge need merging.
      const edge = more ? retained.reduce((last, id, index) => block.paged?.has(id) ? index : last, -1) : -1
      let at = 0
      for (const id of ids) {
        if (known.has(id)) continue
        while (at < retained.length && (at <= edge || comparePlaced(lazyNodes.get(retained[at]!)!, lazyNodes.get(id)!) <= 0)) merged.push(retained[at++]!)
        merged.push(id); known.add(id)
      }
      block.ids = [...merged, ...retained.slice(at)]
      block.paged = new Set([...(more ? block.paged ?? [] : []), ...ids])
      block.cursor = page.next_cursor
      loadedOnce.value = true
      lazyRead.value = { kind: 'more', sent, behind: page.items.filter(item => rowStore.newer(item.id, item.updated_at)).map(item => item.id) }
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
    const request = generation
    const sent = rowStore.mark()
    lazyNodes.clear(); blocks.clear(); order.clear()
    epicBlock.value = { ids: [], cursor: null, loading: true, error: '' }
    looseBlock.value = { ids: [], cursor: null, loading: true, error: '' }
    await Promise.all([
      loadBlock(epicBlock, { parent_id: root, kind: ['epic'] }),
      loadBlock(looseBlock, { parent_id: root, kind: ['ticket', 'task'] }),
    ])
    if (request !== generation) return
    while (request === generation && epicBlock.value.cursor && !epicBlock.value.error) await loadBlock(epicBlock, { parent_id: root, kind: ['epic'] }, true)
    if (request !== generation) return
    // Reopen what was expanded before, level by level.
    let frontier = [...expanded.value].filter(id => lazyNodes.has(id))
    for (let depth = 0; depth < 6 && frontier.length; depth++) {
      await Promise.all(frontier.map(id => loadChildren(id)))
      if (request !== generation) return
      frontier = [...expanded.value].filter(id => lazyNodes.has(id) && !blocks.has(id))
    }
    lazyRead.value = { kind: 'load', sent, behind: [...lazyNodes.values()].filter(row => rowStore.newer(row.id, row.updated_at)).map(row => row.id) }
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
    top.sort(comparePlaced)
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
  const rows = computed(() => entries.value.flatMap(entry => entry.type === 'row' ? [entry.row] : []))
  // The first page shows once both blocks have their first rows, so epics never
  // arrive above tickets that are already on screen.
  const loading = computed(() => matchMode.value ? (list.loading.value || (resolving.value && !entries.value.length)) : (epicBlock.value.loading && !epicBlock.value.ids.length) || (looseBlock.value.loading && !looseBlock.value.ids.length))
  const error = computed(() => matchMode.value ? '' : epicBlock.value.error || looseBlock.value.error)

  // ---------- Live rows, stable tree placement ----------
  // The list's causal batching, labels, editor pin and Show policy also own
  // the Outline. Lazy block ids are placement only; every value is a store row.
  const liveRows = computed<ListItem[]>({
    get: () => [...(matchMode.value ? matchNodes.value : lazyNodes).values()].filter(row => row.kind_slug !== 'missing'),
    set(items) {
      // Release only structural sort changes. A field-only patch must keep
      // its original sort values even when Show applies other rows' updates.
      for (const row of items) {
        const placed = live.layout(row), before = order.get(row.id)
        if (before && (before.parent_id !== placed.parent_id || placeKey(before, { ...filters.value, group: 'none' }) !== placeKey(placed, { ...filters.value, group: 'none' }))) order.delete(row.id)
      }
      rememberOrder(items)
      if (matchMode.value) {
        const wasMatch = new Set(list.rows.value.map(row => row.id))
        list.rows.value = items.filter(row => matched.get(row.id) ?? wasMatch.has(row.id))
        ancestors.clear()
        for (const row of items) if (!list.rows.value.some(item => item.id === row.id)) ancestors.set(row.id, row)
      } else {
        lazyNodes.clear()
        for (const row of items) lazyNodes.set(row.id, row)
        const byParent = childMap(items.map(row => live.layout(row)), comparePlaced)
        const roots = byParent.get(projectId.value ?? '') ?? []
        epicBlock.value.ids = roots.filter(row => row.kind_slug === 'epic').map(row => row.id)
        looseBlock.value.ids = roots.filter(row => row.kind_slug !== 'epic').map(row => row.id)
        for (const [id, block] of blocks) block.ids = (byParent.get(id) ?? []).map(row => row.id)
      }
    },
  })
  async function fetchLive(query: ListQuery, purpose: LiveReadPurpose) {
    const run = generation, sent = rowStore.mark()
    const page = await (options.fetchList ?? listNodes)(query)
    if (!matchMode.value || run !== generation || purpose === 'presence') return page
    // Ancestors place filtered matches even when they do not match themselves.
    // Read them afresh on relevant events; no project-lifetime cache of values.
    const ids = query.ids ?? []
    const context = ids.filter(id => ancestors.has(id) && !list.rows.value.some(row => row.id === id) && !page.items.some(row => row.id === id))
    const extra = context.length ? await listNodes({ within: projectId.value!, kind: ['epic', 'ticket', 'task'], ids: context, limit: LEVEL }) : null
    if (run === generation && !rowStore.gapSince(sent)) {
      for (const id of ids) if (!rowStore.touchedSince(id, sent)) matched.set(id, page.items.some(row => row.id === id))
      for (const item of page.items) if (!rowStore.touchedSince(item.id, sent)) matched.set(item.id, true)
    }
    return { ...page, items: [...page.items, ...(extra?.items ?? [])] }
  }
  function applied() {
    if (matchMode.value) {
      // A context ancestor that now matches stays in the same place and
      // becomes an ordinary match once its field update is accepted.
      const promoted = [...ancestors.values()].filter(row => matched.get(row.id) && !live.pending.kind(row.id) && !list.rows.value.some(item => item.id === row.id))
      if (promoted.length) {
        const next = [...list.rows.value]
        const compareMatches = compareRows(effectiveSort(filters.value))
        for (const row of promoted) {
          const at = next.findIndex(item => compareMatches(row, item) < 0)
          next.splice(at < 0 ? next.length : at, 0, row)
          ancestors.delete(row.id)
        }
        list.rows.value = next
      }
      void resolveAncestors()
    }
    void refreshParentCounts()
    for (const id of roots.value.epics) ensureStats(id, true)
    options.applied?.()
  }
  async function refreshParentCounts() {
    const run = generation
    const parents = liveRows.value.flatMap(row => row.children_count > 0 ? [row.id, row.parent_id] : [row.parent_id])
    const ids = [...new Set([...parents, ...blocks.keys()])].filter((id): id is string => !!id && id !== projectId.value && !!node(id))
    for (let at = 0; at < ids.length; at += LEVEL) {
      const sent = rowStore.mark()
      try {
        const page = await listNodes({ within: projectId.value!, kind: ['epic', 'ticket', 'task'], ids: ids.slice(at, at + LEVEL), limit: LEVEL })
        if (run !== generation || rowStore.gapSince(sent)) return
        for (const item of page.items) rowStore.adopt(item, sent)
      } catch { /* The next event or resync refreshes projections again. */ }
    }
  }
  const live = useLiveList({
    projectId, filters, rows: liveRows, loading,
    reads: computed(() => matchMode.value ? list.reads?.value ?? null : lazyRead.value),
    loadedOnce: computed(() => matchMode.value ? !list.loading.value : loadedOnce.value),
    active, more: () => false, store: options.store, fetchList: fetchLive,
    me: options.me ?? (() => null), quiet: options.quiet, holds: options.holds,
    blockers: options.blockers ?? (() => ({ selected: 0, editing: false, menuOpen: false, dialogOpen: false, dragging: false })),
    around: options.around, env: options.env, applied,
    released: () => { liveRows.value = liveRows.value; applied() },
    placement: row => JSON.stringify([row.parent_id, placeKey(row, { ...filters.value, group: 'none' })]),
    resyncQueries: () => matchMode.value
      ? [liveQuery()]
      : [
        { parent_id: projectId.value!, kind: ['epic'], limit: LEVEL },
        { parent_id: projectId.value!, kind: ['ticket', 'task'], limit: LEVEL },
        ...[...blocks.keys()].map(id => ({ parent_id: id, kind: ['epic', 'ticket', 'task'], limit: LEVEL })),
      ],
    reload: () => { reload(); if (matchMode.value) void list.load?.() },
  })
  watch(() => roots.value.epics, ids => { if (active.value) for (const id of ids) ensureStats(id) }, { immediate: true })
  function liveQuery() { return apiParams(projectId.value!, filters.value, { limit: LEVEL }) }
  // Placement changes only when Show releases the held layout (or after an
  // explicit local move); resolve the new chain then, never underneath a row.
  watch(() => [...placedNodes.value.values()].map(row => `${row.id}:${row.parent_id}`).join(','), () => {
    if (active.value && matchMode.value && !list.loading.value) void resolveAncestors()
  })
  onScopeDispose(() => { generation++; ancestorRun++ })

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
    order.delete(item.id); rememberOrder([row])
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
    if (matchMode.value) { rememberOrder([item], false); refreshStatsFor(item.id); void resolveAncestors(); return }
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
  function reload() {
    generation++; ancestorRun++; matched.clear(); order.clear()
    if (matchMode.value) { ancestors.clear(); void resolveAncestors() }
    else void loadRoots()
  }

  return {
    matchMode, autoExpand, entries, rows, loading, error, node, isExpanded, hasChildren, noEpicCollapsed, createUnder,
    toggle, setExpanded, expandAll, collapseAll, loadChildren, loadMoreRoot, parentOf, epicOf, insert, relocate, remove, findByKey, reveal, startCreateUnder,
    refreshStatsFor, hasMoreRoot: computed(() => !matchMode.value && !!looseBlock.value.cursor), loadingMoreRoot: computed(() => looseBlock.value.loading && !!looseBlock.value.ids.length),
    reload, live, loadedRows: liveRows,
  }
}
