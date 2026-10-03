// SPDX-License-Identifier: AGPL-3.0-only
export interface PlacementKind {
  id: string; slug: string; label: string; project_id?: string | null
  system?: string | null; position: number; archived_at?: string | null
}
export interface PlacementKindPage { items: PlacementKind[]; next_cursor: string | null }

export function ticketKinds(kinds: PlacementKind[], project: string): PlacementKind[] {
  return kinds.filter(kind => !kind.archived_at && !['review', 'other'].includes(kind.slug) && (!kind.project_id || kind.project_id === project))
    .sort((a, b) => a.position - b.position || a.label.localeCompare(b.label))
}
export function placementSuggested(fields: Record<string, unknown>, field: 'area' | 'complexity'): boolean {
  return fields[field + '_source'] === 'suggested' || fields[field + '_source'] === 'agent' && fields[field + '_confirmed'] !== true
}
// Dropping the stored provenance explicitly confirms an unchanged suggestion.
export function placementPatch(field: 'area' | 'complexity', value: string): Record<string, unknown> {
  return { [field]: value || null, [field + '_source']: undefined, [field + '_by']: undefined, [field + '_at']: undefined, [field + '_confirmed']: undefined }
}
export async function loadTicketKinds(fetchPage: (cursor?: string) => Promise<PlacementKindPage>): Promise<PlacementKind[]> {
  const result: PlacementKind[] = [], cursors = new Set<string>()
  let cursor: string | undefined
  for (let page = 0; page < 16; page++) {
    const next = await fetchPage(cursor)
    if (next.items.length > 100 || result.length + next.items.length > 1000) throw new Error('Too many work kinds to display.')
    result.push(...next.items)
    if (!next.next_cursor) return result
    if (cursors.has(next.next_cursor)) throw new Error('Work kinds could not be loaded completely.')
    cursors.add(next.next_cursor); cursor = next.next_cursor
  }
  throw new Error('Work kinds could not be loaded completely.')
}
