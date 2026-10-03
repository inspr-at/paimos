// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api.ts'
import type { ReleaseChange } from './releases.ts'

export interface PendingChanges {
  live_version: string; base_commit: string; head_commit: string; checked_at: string
  source: 'github-main'; status: 'available' | 'partial' | 'unavailable'
  total: number | null; known_total: number; changes: ReleaseChange[]
  next_cursor: string | null; unavailable: string[]
}

export async function getPendingChanges(cursor?: string, signal?: AbortSignal): Promise<PendingChanges> {
  const query = new URLSearchParams({ limit: '50' })
  if (cursor) query.set('cursor', cursor)
  const response = await api(`/releases/pending?${query}`, { signal }, 8_000)
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    throw new Error(typeof body.error === 'string' ? body.error : `Waiting changes could not be loaded (${response.status}).`)
  }
  const body = await response.json() as PendingChanges
  if (!body || typeof body !== 'object') throw new Error('The server sent an unknown waiting changes format.')
  const count = Number.isSafeInteger(body.known_total) && body.known_total >= 0 && body.known_total <= 250
  const exact = body.status === 'available' ? body.total === body.known_total : body.total === null
  if (!['available', 'partial', 'unavailable'].includes(body.status) || body.source !== 'github-main' || !count || !exact || typeof body.live_version !== 'string' || !Array.isArray(body.changes) || body.changes.length > 100 || body.changes.length > body.known_total || !Array.isArray(body.unavailable) || (body.next_cursor !== null && typeof body.next_cursor !== 'string')) throw new Error('The server sent an unknown waiting changes format.')
  return body
}

export function pendingLine(data: PendingChanges | null, loading: boolean, error: string) {
  if (loading && !data) return 'Checking changes waiting for the next release'
  if (error || !data || data.status === 'unavailable') return 'Waiting changes unavailable'
  const count = data.total ?? data.known_total
  return `${data.total === null ? 'At least ' : ''}${count} ${count === 1 ? 'change' : 'changes'} waiting for the next release`
}
