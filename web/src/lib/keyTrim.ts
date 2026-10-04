// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'

export interface KeyTrimProposal {
  id: string; key_id: string; key_name: string; owner_id: string; owner_name: string
  previous_scopes: string[]; snapshot_digest: string; candidate_scopes: string[]; candidate_digest: string
  evidence: { summary: string; observed_at: string; risks: { scope: string; evidence: string; risk_if_dropped: string }[] }
  usage: { scope: string; last_used_at: string | null }[]
  created_by: string; created_at: string; expires_at: string; state: 'pending' | 'applied' | 'declined' | 'restored' | 'expired'
  revision: number; applied_at: string | null; restore_until: string | null; blocked_reason?: string
}
interface TrimPage { items: KeyTrimProposal[]; has_more: boolean; next_cursor?: string }
export type KeyTrimCursors = { pending?: string; decided?: string }
async function trimRequest<T>(path: string, input?: unknown): Promise<T> {
  const response = await api(path, input === undefined ? undefined : { method: 'POST', body: JSON.stringify(input) })
  const maxBytes = 8 * 1024 * 1024
  if (Number(response.headers.get('Content-Length')) > maxBytes) { await response.body?.cancel(); throw new Error('The key trim response is too large.') }
  if (!response.body) throw new Error('The key trim response is missing.')
  const reader = response.body.getReader(), decoder = new TextDecoder()
  let bytes = 0, text = ''
  try {
    for (;;) {
      const chunk = await reader.read(); if (chunk.done) break
      bytes += chunk.value.byteLength
      if (bytes > maxBytes) { await reader.cancel(); throw new Error('The key trim response is too large.') }
      text += decoder.decode(chunk.value, { stream: true })
    }
    text += decoder.decode()
  } finally { reader.releaseLock() }
  const result = JSON.parse(text)
  if (!response.ok) throw new APIError(response.status, result.error || 'The key trim action failed.', result)
  return result
}
export async function readKeyTrims(cursors: KeyTrimCursors = {}): Promise<{ items: KeyTrimProposal[]; warnings: string[]; next: KeyTrimCursors }> {
  const items: KeyTrimProposal[] = [], warnings: string[] = []
  const next: KeyTrimCursors = {}
  // One bounded window per state. Explicit continuation can reach any record
  // without accumulating an unbounded history or re-reading earlier pages.
  for (const state of ['pending', 'decided'] as const) {
    const cursor = cursors[state]
    const page = await trimRequest<TrimPage>(`/key-trim-proposals?state=${state}&limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`)
    if (!Array.isArray(page.items) || page.items.length > 100 || typeof page.has_more !== 'boolean') throw new Error('The key trim page is invalid.')
    items.push(...page.items)
    if (page.has_more) {
      if (!page.next_cursor || page.next_cursor.length > 128 || page.next_cursor === cursor || !page.items.length || page.items.at(-1)?.id !== page.next_cursor) throw new Error('The key trim continuation is invalid.')
      next[state] = page.next_cursor
      warnings.push(`More ${state} key trim proposals are available. Use Next key trims in that view.`)
    }
  }
  return { items, warnings, next }
}
export async function decideKeyTrim(proposal: KeyTrimProposal, decision: 'approve' | 'decline' | 'restore', requestId: string): Promise<KeyTrimProposal> {
  const restore = decision === 'restore'
  const result = await trimRequest<KeyTrimProposal>(`/key-trim-proposals/${encodeURIComponent(proposal.id)}/${restore ? 'restore' : 'decision'}`, {
    request_id: requestId, expected_digest: restore ? proposal.candidate_digest : proposal.snapshot_digest, ...(!restore && { decision }),
  })
  if (result.id !== proposal.id || result.key_id !== proposal.key_id || result.revision !== proposal.revision + 1 || result.state !== (restore ? 'restored' : decision === 'approve' ? 'applied' : 'declined')) throw new Error('The key trim result could not be confirmed. Refresh the desk.')
  return result
}
