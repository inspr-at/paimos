// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api.ts'

export interface DeskItem {
  id: string; kind: 'question' | 'approval' | 'action_request' | 'doctrine'; project_id?: string
  revision: number; title: string; created_at: string; expires_at?: string; held: boolean; href: string; source: string
}
export interface DeskProjection {
  items: DeskItem[]; counts: { open: number; held: number; chores: number }
  has_more: boolean; next_cursor?: string; as_of: string
}

export async function readDeskProjection(limit = 100, cursor?: string, signal?: AbortSignal): Promise<DeskProjection> {
  const params = new URLSearchParams({ limit: String(limit), ...(cursor ? { cursor } : {}) })
  const response = await api(`/decision-desk/projection?${params}`, { signal })
  if (!response.ok) throw new APIError(response.status, `Decision Desk could not be read (${response.status}).`)
  const page: DeskProjection = await response.json()
  if (!Array.isArray(page.items) || page.items.length > 100 || !page.counts ||
    ![page.counts.open, page.counts.held, page.counts.chores].every(n => Number.isSafeInteger(n) && n >= 0) || typeof page.has_more !== 'boolean') {
    throw new Error('Decision Desk returned an invalid projection. Try again.')
  }
  return page
}

// Preserve the server order. Bounded coverage and a changing queue are explicit;
// a count never comes from this client-side list or from a page's item count.
export async function loadDeskProjection(signal?: AbortSignal, read = readDeskProjection): Promise<DeskProjection & { truncated: boolean }> {
  let page = await read(100, undefined, signal)
  const first = page
  const items: DeskItem[] = [], ids = new Set<string>(), cursors = new Set<string>()
  for (let n = 0; n < 10; n++) {
    for (const item of page.items) {
      const key = `${item.kind}:${item.id}`
      if (!ids.has(key)) { ids.add(key); items.push(item) }
    }
    if (!page.has_more) return { ...first, items, has_more: false, truncated: items.length !== first.counts.open }
    if (!page.next_cursor || cursors.has(page.next_cursor)) throw new Error('Decision Desk pagination did not advance. Try again.')
    if (n === 9) break
    cursors.add(page.next_cursor)
    page = await read(100, page.next_cursor, signal)
  }
  return { ...first, items, has_more: true, truncated: true }
}
