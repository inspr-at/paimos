// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api.ts'

// AEON-876 wire types. Ticket area identities are assigned by the server.
export interface WorkKind {
  id: string; slug: string; label: string; hint: string; position: number
  examples: string[]; labels: string[]; ticket_count: number
  project_id?: string | null; system?: 'other' | 'security' | 'review' | null; archived_at?: string | null
}
export interface KindWords { label: string; hint: string; examples: string[]; labels: string[] }
export interface SituationLimits { small_hours: number; fix_rounds: number; revision: number; set_by: string | null; set_at: string | null }
interface KindPage { items: WorkKind[]; next_cursor: string | null }
const MAX_PAGES = 16, MAX_BYTES = 1024 * 1024
async function request<T>(path: string, method: string, body: unknown, signal: AbortSignal): Promise<T> {
  const response = await api(path, {
    method, signal: AbortSignal.any([signal, AbortSignal.timeout(30_000)]),
    ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  })
  const reader = response.body?.getReader()
  if (!reader) throw new Error('Empty response')
  const chunks: Uint8Array[] = []
  let size = 0
  try {
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      size += value.byteLength
      if (size > MAX_BYTES) throw new Error('Response is too large')
      chunks.push(value)
    }
  } catch (error) { await reader.cancel().catch(() => {}); throw error }
  finally { reader.releaseLock() }
  const bytes = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length }
  const data = JSON.parse(new TextDecoder().decode(bytes))
  if (!response.ok) throw new APIError(response.status, data.message ?? data.error ?? `Request failed (${response.status})`, data)
  return data as T
}
export async function listWorkKinds(signal: AbortSignal): Promise<{ items: WorkKind[]; truncated: boolean }> {
  const items: WorkKind[] = [], cursors = new Set<string>()
  let cursor: string | null = null
  for (let page = 0; page < MAX_PAGES; page++) {
    const query = new URLSearchParams({ include_archived: 'true', limit: '100', ...(cursor ? { cursor } : {}) })
    const result: KindPage = await request(`/work-kinds?${query}`, 'GET', undefined, signal)
    if (!Array.isArray(result.items) || result.items.length > 100) throw new Error('Invalid kinds page')
    items.push(...result.items)
    if (!result.next_cursor) return { items, truncated: false }
    if (cursors.has(result.next_cursor)) throw new Error('Invalid kinds cursor')
    cursors.add(result.next_cursor); cursor = result.next_cursor
  }
  return { items, truncated: true }
}
export const createKind = (words: KindWords, position: number, signal: AbortSignal) => request<WorkKind>('/work-kinds', 'POST', { ...words, position }, signal)
export const updateKind = (id: string, words: KindWords, signal: AbortSignal) => request<WorkKind>(`/work-kinds/${encodeURIComponent(id)}`, 'PATCH', words, signal)
export const archiveKind = (id: string, signal: AbortSignal) => request<WorkKind>(`/work-kinds/${encodeURIComponent(id)}`, 'DELETE', undefined, signal)
export const restoreKind = (id: string, signal: AbortSignal) => request<WorkKind>(`/work-kinds/${encodeURIComponent(id)}/restore`, 'POST', undefined, signal)
export const orderKinds = (slugs: string[], signal: AbortSignal) => request<KindPage>('/work-kinds/order', 'PUT', { slugs }, signal)
export const getSituationLimits = (signal: AbortSignal) => request<SituationLimits>('/model-preferences/situations', 'GET', undefined, signal)
export const putSituationLimits = (limits: Pick<SituationLimits, 'small_hours' | 'fix_rounds' | 'revision'>, signal: AbortSignal) => request<SituationLimits>('/model-preferences/situations', 'PUT', limits, signal)
export const kindWords = (kind: WorkKind): KindWords => ({ label: kind.label, hint: kind.hint, examples: [...kind.examples], labels: [...kind.labels] })
export const activeKinds = (items: WorkKind[]) => items.filter(kind => !kind.archived_at && kind.system !== 'review').sort((a, b) => Number(a.system === 'other') - Number(b.system === 'other') || a.position - b.position || a.id.localeCompare(b.id))
export function movedKinds(items: WorkKind[], id: string, direction: -1 | 1): string[] | null {
  const visible = activeKinds(items), index = visible.findIndex(kind => kind.id === id), target = index + direction
  if (index < 0 || target < 0 || target >= visible.length || visible[index]!.system === 'other' || visible[target]!.system === 'other') return null
  const next = [...visible]
  ;[next[index], next[target]] = [next[target]!, next[index]!]
  // The legacy system review row is still required by the order endpoint, but
  // reviews are situations, not kind columns. Preserve it in the payload.
  return [...next.map(kind => kind.slug), ...items.filter(kind => !kind.archived_at && kind.system === 'review').map(kind => kind.slug)]
}
export function parseKindWords(draft: { label: string; hint: string; examples: string; labels: string }): KindWords | null {
  const words = { label: draft.label.trim(), hint: draft.hint.trim(), examples: draft.examples.split('\n').map(value => value.trim()).filter(Boolean), labels: draft.labels.split(',').map(value => value.trim()).filter(Boolean) }
  const length = (value: string) => [...value].length
  if (!words.label || length(words.label) > 40 || !words.hint || length(words.hint) > 120 || words.examples.length > 3 || words.examples.some(value => length(value) > 120) || words.labels.length > 32 || words.labels.some(value => length(value) > 48) || new Set(words.labels).size !== words.labels.length) return null
  return words
}
export const validLimit = (value: number, max: number) => Number.isInteger(value) && value >= 1 && value <= max
