// SPDX-License-Identifier: AGPL-3.0-only
import type { SessionView } from '../../stores/agents'

export interface ActivityEntry { note: string; at: string }
export interface ActivitySession { activity_note?: string | null; activity_history?: ActivityEntry[] }
export const activityOf = (view: SessionView) => view.session as typeof view.session & ActivitySession

export function currentStep(view: SessionView, workers = 0) {
  const note = activityOf(view).activity_note?.trim()
  if (note) return note
  if (view.status.group === 'stopped') return 'Finished this session'
  if (view.status.group === 'idle') return view.status.state === 'stale' ? 'Waiting for a heartbeat' : 'Waiting for work'
  if (view.status.state === 'throttled') return 'Paused by pacing or allowance'
  if (view.status.state === 'problem') return 'Check the session details'
  if (view.status.state === 'waiting') return 'Waiting for attention'
  if (view.session.phase === 'starting') return 'Starting up'
  if (view.session.phase === 'stopping') return 'Stopping this session'
  if (view.session.role === 'coordinator' && workers) return `Coordinating ${workers} ${workers === 1 ? 'worker' : 'workers'}`
  if (view.session.work_shape === 'scout') return 'Investigating the ticket'
  if (view.session.work_shape === 'ship') return 'Building a change'
  return 'Working on this project'
}
