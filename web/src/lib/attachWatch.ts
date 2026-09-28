// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api.ts'

export interface AttachStatus { request_id: string; owner_id: string; state: 'active' | 'detached' | 'unreachable'; lease_until: string | null }
export interface AttachReview {
  request_id: string; request_digest: string; state: 'pending' | 'approved' | 'active' | 'detached' | 'unreachable'; expires_at: string
  snapshot: {
    computer_id: string; project_id: string; ticket_id: string; host: string; harness: string; transcript: string; file_id: string
    process: { pid: number; uid: number; started: string; executable: string; cwd: string }
  }
}
export async function attachAction(path: string, body?: object, signal?: AbortSignal): Promise<AttachReview> {
  const res = await api(`/agent-pairing/attach${path}`, { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body ?? {}) })
  if (!res.ok) throw new Error(res.status === 429 ? 'Too many attempts. Try again in ten minutes.' : res.status === 403 ? 'Only the paired computer’s owner can approve this watch.' : res.status === 410 ? 'This request ended. Start a new attach in the terminal.' : 'Attach unavailable. Check the code and try again.')
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
