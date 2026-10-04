// SPDX-License-Identifier: AGPL-3.0-only
// The Outline as a flat list of table entries: epics first, then a "No epic" group,
// children indented under their parents with guide lines. Pure, for unit tests.
import type { ListItem } from './api.ts'

export interface EpicStats { done: number; scope: number; total: number; open: number }
export interface TreeMeta {
  depth: number
  // guides[i]: whether the ancestor at depth i + 1 continues below (a vertical line in column i).
  guides: boolean[]
  last: boolean
  hasChildren: boolean
  expanded: boolean
  loading: boolean
  dimmed: boolean
  stats: EpicStats | null
  parentId: string
}
export type OutlineEntry =
  | { type: 'row'; key: string; row: ListItem; tree: TreeMeta }
  | { type: 'group'; key: string; label: string; count: number; collapsed: boolean }
  | { type: 'skeleton'; key: string; depth: number; guides: boolean[]; last: boolean }
  | { type: 'more'; key: string; depth: number; guides: boolean[]; parentId: string; loading: boolean }
  | { type: 'create'; key: string; depth: number; guides: boolean[]; epic: { id: string; key: string; title: string } }

export interface OutlineSource {
  rootId: string
  epics: string[]
  loose: string[]
  looseHasMore: boolean
  looseLoading: boolean
  node: (id: string) => ListItem | undefined
  children: (id: string) => { ids: string[]; hasMore: boolean; loading: boolean } | null
  hasChildren: (id: string) => boolean
  expanded: (id: string) => boolean
  dimmed: (id: string) => boolean
  stats: (id: string) => EpicStats | null
  noEpicCollapsed: boolean
  createUnder: string | null
}

export function flattenOutline(source: OutlineSource): OutlineEntry[] {
  const out: OutlineEntry[] = []
  const walk = (ids: string[], depth: number, guides: boolean[], parentId: string, trailing: boolean) => {
    ids.forEach((id, index) => {
      const row = source.node(id)
      if (!row) return
      const last = index === ids.length - 1 && !trailing
      const creatingHere = source.createUnder === id
      const hasChildren = source.hasChildren(id) || creatingHere
      const expanded = hasChildren && (source.expanded(id) || creatingHere)
      const block = expanded ? source.children(id) : null
      const loading = expanded && (!block || (block.loading && !block.ids.length))
      out.push({ type: 'row', key: id, row, tree: { depth, guides, last, hasChildren, expanded, loading, dimmed: source.dimmed(id), stats: (row.is_leaf === false || row.kind_slug === 'epic') ? source.stats(id) : null, parentId } })
      if (!expanded) return
      const childGuides = depth === 0 ? [] : [...guides, !last]
      if (creatingHere) out.push({ type: 'create', key: `create-${id}`, depth: depth + 1, guides: childGuides, epic: { id, key: row.key, title: row.title } })
      if (loading) {
        const count = Math.max(1, Math.min(row.children_count || 2, 3))
        for (let i = 0; i < count; i++) out.push({ type: 'skeleton', key: `skeleton-${id}-${i}`, depth: depth + 1, guides: childGuides, last: i === count - 1 })
        return
      }
      walk(block?.ids ?? [], depth + 1, childGuides, id, !!block?.hasMore)
      if (block?.hasMore) out.push({ type: 'more', key: `more-${id}`, depth: depth + 1, guides: childGuides, parentId: id, loading: block.loading })
    })
  }
  walk(source.epics, 0, [], source.rootId, false)
  if (source.loose.length || source.looseHasMore) {
    out.push({ type: 'group', key: 'no-epic', label: 'No epic', count: source.loose.length, collapsed: source.noEpicCollapsed })
    if (!source.noEpicCollapsed) {
      walk(source.loose, 0, [], source.rootId, false)
      if (source.looseHasMore) out.push({ type: 'more', key: 'more-root', depth: 0, guides: [], parentId: source.rootId, loading: source.looseLoading })
    }
  }
  return out
}

// Match-set mode: rows plus the ancestors that place them, grouped by parent and ordered.
export function childMap(nodes: Iterable<ListItem>, compare: (a: ListItem, b: ListItem) => number): Map<string, ListItem[]> {
  const map = new Map<string, ListItem[]>()
  for (const node of nodes) {
    const parent = node.parent_id ?? ''
    if (!map.has(parent)) map.set(parent, [])
    map.get(parent)!.push(node)
  }
  for (const list of map.values()) list.sort(compare)
  return map
}

// Parent ids that a set of rows needs but does not contain (below the project).
export function missingAncestors(nodes: Map<string, ListItem>, rootId: string): string[] {
  const missing = new Set<string>()
  for (const node of nodes.values()) {
    const parent = node.parent_id
    if (parent && parent !== rootId && !nodes.has(parent)) missing.add(parent)
  }
  return [...missing]
}

export function epicStats(counts: Record<string, number>, isClosed: (state: string) => boolean, isDone: (state: string) => boolean, isCancelled: (state: string) => boolean): EpicStats {
  let done = 0, total = 0, cancelled = 0, open = 0
  for (const [state, count] of Object.entries(counts)) {
    total += count
    if (isDone(state)) done += count
    else if (isCancelled(state)) cancelled += count
    if (!isClosed(state)) open += count
  }
  return { done, scope: total - cancelled, total, open }
}
