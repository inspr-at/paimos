// SPDX-License-Identifier: AGPL-3.0-only
// AEON-291: the one client copy of the server's managed-control eligibility
// (internal/harness/managed_controls.go managedControl + forceAvailable). The
// row menu and the session panel both ask here, so neither offers a control the
// server would refuse with 409.
import type { HarnessSession } from './agents.ts'

export const MANAGED_OWNERSHIP_WINDOW_MS = 45_000
export type ManagedKind = 'steer' | 'interrupt' | 'stop' | 'rename' | 'model' | 'effort'
export interface ManagedAccess {
  now: number
  // A person with harness.control in the session's project.
  allowed: boolean
  // The bound run's status: the server requires it running on the owning daemon.
  runStatus?: string | null
}

// Sessions the ownership-aware route serves at all; others use /controls/*.
export const managedControlSession = (s: HarnessSession) => s.management_mode === 'managed' && s.advertised_capabilities.includes('managed_control_v1')

export function ownershipFresh(s: HarnessSession, now: number) {
  const observed = Date.parse(s.process_observed_at ?? '')
  return !!s.process_ownership && Number.isFinite(observed) && now - observed >= 0 && now - observed <= MANAGED_OWNERSHIP_WINDOW_MS
}

// Why no managed control can be sent right now, or '' when the session takes
// them. Order: permission, ended, adapter, run, ownership.
export function managedControlUnavailable(s: HarnessSession, access: ManagedAccess): string {
  if (!access.allowed) return 'You need permission to control this session.'
  if (s.phase === 'stopped' || s.stopped_at || s.archived_at) return 'This session has stopped.'
  if (s.harness !== 'claude' || !s.run_id || !s.advertised_capabilities.includes('stop') || !managedControlSession(s)) return 'This session does not take controls from here.'
  if (access.runStatus !== 'running') return 'Its run is not running.'
  if (!ownershipFresh(s, access.now)) return 'Waiting for the owning daemon to confirm this process.'
  return ''
}

// Exactly what POST /managed-controls accepts for this kind (quota aside).
export const managedControlAllowed = (s: HarnessSession, kind: ManagedKind, access: ManagedAccess) =>
  !managedControlUnavailable(s, access) && s.advertised_capabilities.includes(kind)
