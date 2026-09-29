// SPDX-License-Identifier: AGPL-3.0-only
import type { HarnessSession } from '../../lib/agents'

export const movableWorker = (s: HarnessSession) => s.can_reparent === true && s.role === 'worker' && !s.stopped_at && !s.archived_at && s.phase !== 'stopped'
export function moveTarget(worker: HarnessSession, lead: HarnessSession, sessions: HarnessSession[]) {
  if (!movableWorker(worker) || lead.can_reparent !== true || lead.role !== 'coordinator' || lead.stopped_at || lead.archived_at || lead.phase === 'stopped' || worker.id === lead.id || worker.project_id !== lead.project_id || worker.parent_harness_session_id === lead.id) return false
  const seen = new Set<string>()
  let parent: HarnessSession | undefined = lead
  while (parent) {
    if (parent.id === worker.id || seen.has(parent.id)) return false
    seen.add(parent.id)
    parent = sessions.find(s => s.id === parent?.parent_harness_session_id)
  }
  return true
}
