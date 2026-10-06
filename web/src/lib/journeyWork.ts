// SPDX-License-Identifier: AGPL-3.0-only
import type { ListItem } from './api.ts'
import { isWorkItem, isWorkLeaf, isWorkParent } from './workVocabulary.ts'

// Legacy journeys count tickets; their tasks remain subdivisions. Canonical
// journeys count leaves at every depth, including migrated tasks.
export function isJourneyLeaf(row: Pick<ListItem, 'kind_slug' | 'is_leaf'>): boolean {
  return row.kind_slug === 'work' ? isWorkLeaf(row) : row.kind_slug === 'ticket'
}

// Canonical work is grouped by nesting. Legacy list responses keep their epic
// projection until upgraded. Only work leaves count toward a parent's work.
export function workParentId(row: ListItem): string | null {
  return row.kind_slug === 'work'
    ? row.parent?.kind_slug === 'work' ? row.parent.id : null
    : row.epic?.id ?? (row.parent?.kind_slug === 'epic' ? row.parent.id : null)
}

export function parentLeafGroups(rows: ListItem[]): Map<string, ListItem[]> {
  const byId = new Map(rows.map(row => [row.id, row]))
  const groups = new Map(rows.filter(row => isWorkItem(row) && isWorkParent(row)).map(row => [row.id, [] as ListItem[]]))
  for (const leaf of rows.filter(isJourneyLeaf)) {
    let id = workParentId(leaf)
    const seen = new Set<string>()
    // Supplied rows bound the traversal; the seen set terminates cycles without
    // silently dropping leaves from valid deep ancestors.
    while (id && !seen.has(id)) {
      seen.add(id)
      groups.get(id)?.push(leaf)
      const parent = byId.get(id)
      id = parent ? workParentId(parent) : null
    }
  }
  return groups
}
