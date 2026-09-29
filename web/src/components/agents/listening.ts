// SPDX-License-Identifier: AGPL-3.0-only
// AEON-280: is a live session actually listening for messages? It is when it
// pulled its inbox (hook, drain, long poll, stream or ack) within the window.
// Pure, so it is unit tested without Vue.
import type { HarnessSession } from '../../lib/agents.ts'

// Matches the heartbeat staleness window: a pull older than this means messages
// wait, and past their deadline they fail back to the sender.
export const LISTENING_WINDOW_MS = 3 * 60_000

const VIA: Record<string, string> = { hook: 'a turn hook', drain: 'managed delivery', long_poll: 'a long poll', stream: 'a live stream', ack: 'an acknowledgement' }

export interface ListeningState { listening: boolean; label: string; detail: string; tip: string }

function ago(ms: number) {
  const minutes = Math.floor(ms / 60_000)
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  return hours < 24 ? `${hours}h ago` : `${Math.floor(hours / 24)}d ago`
}

// Null for ended sessions: nothing is expected to listen.
export function listeningState(session: Pick<HarnessSession, 'phase' | 'stopped_at' | 'archived_at' | 'inbox_seen_at' | 'inbox_seen_via'>, now: number): ListeningState | null {
  if (session.phase === 'stopped' || session.stopped_at || session.archived_at) return null
  const seen = session.inbox_seen_at ? Date.parse(session.inbox_seen_at) : Number.NaN
  if (!Number.isFinite(seen)) return { listening: false, label: 'Not listening', detail: 'never pulled', tip: 'Has not pulled its inbox yet. Messages wait, then fail back to you at their deadline.' }
  const age = Math.max(0, now - seen)
  const via = session.inbox_seen_via ? VIA[session.inbox_seen_via] : undefined
  const through = via ? ` through ${via}` : ''
  if (age < LISTENING_WINDOW_MS) return { listening: true, label: 'Listening', detail: '', tip: `Pulled its inbox ${ago(age)}${through}.` }
  return { listening: false, label: 'Not listening', detail: `last pulled ${ago(age)}`, tip: `Last pulled its inbox ${ago(age)}${through}. Messages wait until it pulls again.` }
}
