// SPDX-License-Identifier: AGPL-3.0-only
import type { SessionView } from '../../stores/agents'
import type { CurrentAgentActivity } from '../../lib/agentRows'
import { STATE_LABEL } from '../../lib/agentSignals.ts'

export interface ActivityEntry { note: string; at: string }
export interface ActivitySession { activity_note?: string | null; activity_history?: ActivityEntry[] }
export const activityOf = (view: SessionView) => view.session as typeof view.session & ActivitySession

export function currentStep(view: SessionView, workers = 0) {
  if (view.status.reasons?.length) return view.status.reasons[0].detail
  const note = view.session.agent_activity_mode === undefined ? activityOf(view).activity_note?.trim() : currentActivity(view, Date.now())
  if (note) return note
  if (view.status.state === 'done') return 'All work reported'
  if (view.status.group === 'stopped') return view.status.label === STATE_LABEL.stopped ? 'Session ended' : view.status.label
  if (view.status.group === 'idle') return view.status.state === 'stale' ? 'Waiting for a heartbeat' : 'Waiting for work'
  if (view.status.state === 'throttled') return view.session.vendor_limited ? view.status.label : 'Paused by pacing or allowance'
  if (view.status.state === 'problem') return 'The service reported a problem without a visible reason.'
  if (view.status.state === 'waiting') return 'Waiting for attention'
  if (view.session.phase === 'starting') return 'Starting up'
  if (view.session.phase === 'stopping') return 'Stopping this session'
  if (view.session.role === 'coordinator' && workers) return `Coordinating ${workers} ${workers === 1 ? 'worker' : 'workers'}`
  if (view.session.work_shape === 'scout') return 'Investigating the ticket'
  if (view.session.work_shape === 'ship') return 'Building a change'
  return 'Working on this project'
}

export function currentActivity(view: SessionView, now: number): string {
  const s = view.session
  if (s.agent_activity_mode === 'off' || s.stopped_at || s.archived_at) return ''
  const a = s.current_activity
  if (!a || !Number.isFinite(Date.parse(a.at))) return ''
  if (a.source === 'agent' && (s.agent_activity_mode === 'tool_activity' || now - Date.parse(a.at) >= 600_000)) return ''
  return a.text
}

export function workerActivity(workers: SessionView[], now: number): string {
  const counts = new Map<string, number>()
  for (const view of workers) {
    const text = currentActivity(view, now)
    if (!text) continue
    const group = /^Running .*tests$/.test(text) ? 'testing' : text === 'Waiting for CI' ? 'waiting for CI' : text.startsWith('Editing ') ? 'editing' : text.toLowerCase()
    counts.set(group, (counts.get(group) ?? 0) + 1)
  }
  return [...counts].map(([text, count]) => `${count} ${count === 1 ? 'worker' : 'workers'} ${text}`).join(' · ')
}

export function activityDurations(history: readonly CurrentAgentActivity[], now: number, stopped?: string | null) {
  const end = stopped ? Math.min(now, Date.parse(stopped)) : now
  return history.map((item, index) => ({ ...item, duration: `${Math.max(1, Math.round(((index ? Date.parse(history[index - 1]!.at) : end) - Date.parse(item.at)) / 60_000))} min` }))
}
