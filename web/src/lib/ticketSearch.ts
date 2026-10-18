// SPDX-License-Identifier: AGPL-3.0-only
// The command palette's ticket search, shared with the relation picker so both
// find the same tickets the same way: a key ("PHAROS-29", "PHAROS-") is a key
// lookup; words go to the list's title match and the hybrid search together.
import { listNodes, searchNodes, type ListItem, type WorkNode } from './api'
import { keyQuery } from './palette'
import { kinds } from './useTicket'
import { WORK_KINDS } from './workVocabulary'
export { WORK_KINDS } from './workVocabulary'

export async function searchWork(q: string, options: { within?: string; signal: AbortSignal }): Promise<{ listed: ListItem[]; hits: WorkNode[] }> {
  const key = keyQuery(q)
  const [page, found] = await Promise.all([
    listNodes({ q, kind: WORK_KINDS, within: options.within, sort: key ? 'key' : '-updated_at', limit: 8 }, { signal: options.signal }),
    key ? Promise.resolve({ items: [] }) : searchNodes(q, { limit: 12 }, { signal: options.signal }).catch(() => ({ items: [] })),
  ])
  return { listed: page.items, hits: found.items.map(hit => hit.node) }
}

// Kind id to slug for the work kinds, which search hits carry only by id.
export async function workKindMap(): Promise<Map<string, string>> {
  return new Map((await kinds()).filter(kind => WORK_KINDS.includes(kind.slug)).map(kind => [kind.id, kind.slug]))
}
