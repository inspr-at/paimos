// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api.ts'

export type WatchConsentMode = 'aeon' | 'local_auth'
export type LocalAuthCapability = 'available' | 'unsupported' | 'unsigned' | 'no_gui' | 'policy' | 'unreported'
export interface LocalAuthComputer { computer_id: string; name: string; capability: LocalAuthCapability; pairing_upgraded?: boolean }
export const metadataOnlyAttach = (snapshot: { mode?: string }) => snapshot.mode === 'lease'
export interface AttachStatus { mode?: 'lease'; process_state?: 'confirmed_exited'; request_id: string; owner_id: string; state: 'active' | 'detached' | 'unreachable'; lease_until: string | null }
export interface AttachReview {
  consent_mode?: WatchConsentMode; consent_digest?: string
  request_id: string; request_digest: string; state: 'pending' | 'approved' | 'active' | 'detached' | 'unreachable' | 'confirmed_exited'; expires_at: string
  snapshot: {
    mode?: 'lease'; platform?: 'darwin' | 'linux'; computer_id: string; project_id: string; ticket_id: string; host: string; harness: string; transcript: string; file_id: string
    process: { pid: number; uid: number; started: string; executable: string; cwd: string }
  }
}
// What the person sees for a request that has not become a session yet. A client
// clock past the server's expiry counts as expired right away; the next poll agrees.
export type AttachOutcome = 'waiting' | 'approved' | 'expired' | 'cancelled'
export function attachOutcome(review: Pick<AttachReview, 'state' | 'expires_at'>, now: number): AttachOutcome | null {
  const past = Date.parse(review.expires_at) <= now
  switch (review.state) {
    case 'pending': return past ? 'expired' : 'waiting'
    case 'approved': return past ? 'expired' : 'approved'
    case 'unreachable': return 'expired'
    case 'detached': return 'cancelled'
    default: return null
  }
}
export async function listPendingAttach(signal?: AbortSignal): Promise<AttachReview[]> {
  const res = await api('/agent-pairing/attach/pending', { signal })
  if (!res.ok) throw new Error('Attach requests unavailable.')
  const body = await res.json() as { requests?: AttachReview[] }
  return Array.isArray(body.requests) ? body.requests : []
}
export { attachCodeFromHash, formatAttachCode } from './attachLink.ts'

export async function attachAction(path: string, body?: object, signal?: AbortSignal): Promise<AttachReview> {
  const res = await api(`/agent-pairing/attach${path}`, { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body ?? {}) })
  if (!res.ok) throw new Error(res.status === 429 ? 'Too many attempts. Try again in ten minutes.' : res.status === 403 ? 'Only the paired computer’s owner can approve this watch.' : res.status === 409 ? 'The consent setting or request changed. Close this review and open it again.' : res.status === 410 ? 'This request ended. Start a new attach in the terminal.' : 'Attach unavailable. Check the code and try again.')
  return res.json() as Promise<AttachReview>
}
// Defense in depth for a malicious or stale relay. Payloads remain plain text;
// no Markdown, HTML, terminal interpretation, links, or command actions.
export function watchText(raw: string): string | null {
  if (raw.length > 100_000) return null
  try {
    const value: unknown = JSON.parse(raw)
    if (typeof value !== 'string' || new TextEncoder().encode(value).length > 16_384 || /[\p{Cc}\p{Cf}]/u.test(value.replace(/[\n\t]/g, ''))) return null
    return value
  } catch { return null }
}
export const appendWatchText = (previous: string, next: string) => (previous + next).slice(-65_536)
