// SPDX-License-Identifier: AGPL-3.0-only
import type { AgentState } from '../../lib/agentSignals'
import type { SessionStatus } from '../../lib/agentState'

// The Agents head counts sessions by state, and every count is a filter on
// Sessions (AEON-780). Working, waiting and problems always show; the rest
// show while they exist, after them, so a count that appears moves nothing.
export type HeadFilter = 'working' | 'waiting' | 'problem' | 'throttled' | 'pausing' | 'paused'
export interface Countable {
  name?: string
  status: Pick<SessionStatus, 'group' | 'state' | 'label' | 'reasons'>
  session: { phase: string; stopped_at?: string | null; heartbeat_at?: string | null; created_at: string }
}
export interface HeadCount { filter: HeadFilter; n: number; label: string; mark: AgentState; tip: string }

const ALWAYS: HeadFilter[] = ['working', 'waiting', 'problem']
const WHILE_ANY: HeadFilter[] = ['throttled', 'pausing', 'paused']
export const FILTER_LABEL: Record<HeadFilter, string> = { working: 'Working', waiting: 'Waiting', problem: 'Problem', throttled: 'Throttled', pausing: 'Pausing', paused: 'Paused' }
const SHOW: Record<HeadFilter, string> = {
  working: 'Show working sessions', waiting: 'Show sessions waiting on a tool or a reply', problem: 'Show sessions with a problem or lost contact',
  throttled: 'Show throttled sessions', pausing: 'Show agents handing over', paused: 'Show paused sessions',
}
const NONE: Record<HeadFilter, string> = {
  working: 'No session is working', waiting: 'No session is waiting', problem: 'No session has a problem',
  throttled: 'No session is throttled', pausing: 'No agent is handing over', paused: 'No session is paused',
}

const live = (view: Countable) => !view.session.stopped_at && view.session.phase !== 'stopped'
// One rule for the head count and the Sessions filter, so the two never disagree.
export function matchesFilter(view: Countable, filter: HeadFilter): boolean {
  if (filter === 'paused') return view.status.state === 'paused'
  if (!live(view)) return false
  const group = view.status.group
  if (filter === 'waiting') return group === 'needs'
  if (filter === 'problem') return group === 'problem' || group === 'unresponsive'
  return group === filter
}

// How long it has been in this state, as people say it: "12 min", "3 h".
export function since(view: Countable, now: number) {
  const minutes = Math.max(1, Math.round((now - Date.parse(view.session.heartbeat_at ?? view.session.created_at)) / 60_000))
  return minutes < 60 ? `${minutes} min` : minutes < 48 * 60 ? `${Math.round(minutes / 60)} h` : `${Math.round(minutes / 1440)} d`
}
const reason = (view: Countable) => (view.status.reasons?.[0]?.detail || view.status.label).replace(/\.$/, '')

export function headCounts(views: Countable[], now: number): HeadCount[] {
  const count = (filter: HeadFilter) => views.filter(view => matchesFilter(view, filter))
  return [...ALWAYS, ...WHILE_ANY].flatMap(filter => {
    const hits = count(filter)
    if (!hits.length && WHILE_ANY.includes(filter)) return []
    const n = hits.length
    const label = filter === 'problem' && n !== 1 ? 'problems' : filter
    // A single problem keeps what the live line said about it: who, what, since when.
    const one = filter === 'problem' && n === 1 ? hits[0]! : null
    const tip = !n ? NONE[filter] : one ? `Show ${one.name ? `${one.name}: ` : ''}${reason(one)} · ${since(one, now)}` : SHOW[filter]
    return [{ filter, n, label, mark: filter, tip }]
  })
}
