// SPDX-License-Identifier: AGPL-3.0-only
// AEON-291: a session menu offers only what works for that session. No disabled
// items with excuses; a session outside Aeon gets one quiet line instead.
import { HEARTBEAT_STALE_MS } from '../../lib/agentState.ts'
import type { HarnessSession } from '../../lib/agents'
import type { SessionView } from '../../stores/agents'

export type SessionAction = 'interrupt' | 'stop' | 'settings' | 'ticket' | 'copy' | 'remove'
export interface SessionMenu { control: SessionAction[]; other: SessionAction[]; remove: boolean; note: string }
export interface MenuAccess { canControl: boolean; canRemove: boolean; pending?: { state: string } | null; product: string; now: number }

// Mirrors the server: managed controls need a Claude process whose ownership
// the daemon confirmed within 45 seconds (ManagedSessionControls, forceAvailable).
export const OWNERSHIP_WINDOW_MS = 45_000
export const managedControl = (s: HarnessSession) => s.advertised_capabilities.includes('managed_control_v1')
export function ownershipFresh(s: HarnessSession, now: number) {
  const observed = Date.parse(s.process_observed_at ?? '')
  return s.harness === 'claude' && !!s.process_ownership && Number.isFinite(observed) && now - observed >= 0 && now - observed <= OWNERSHIP_WINDOW_MS
}

const SETTINGS = ['rename', 'model', 'effort']
export const isLive = (s: HarnessSession) => s.phase !== 'stopped' && !s.stopped_at && !s.archived_at

// A heartbeat inside the "awaiting" threshold is evidence the process still runs.
export function heartbeatFresh(s: HarnessSession, now: number) {
  const at = Date.parse(s.heartbeat_at ?? '')
  return Number.isFinite(at) && now - at < HEARTBEAT_STALE_MS
}

// No heartbeat, Lost contact or stopped: the record is all that is left to
// tidy, so one click removes it (with undo). Live sessions keep a confirm.
export const quickRemoval = (view: SessionView) => !view.session.archived_at && (view.status.state === 'unresponsive' || !isLive(view.session))

// Said only where it is true: a fresh heartbeat means the process keeps running.
export function removalConsequence(s: HarnessSession, now: number) {
  return isLive(s) && heartbeatFresh(s, now) ? 'Its process keeps running; only the record leaves Agents.' : ''
}

export function sessionMenu(view: SessionView, access: MenuAccess): SessionMenu {
  const s = view.session
  const live = isLive(s)
  const control: SessionAction[] = []
  if (live && s.management_mode === 'managed' && access.canControl) {
    const busy = !!access.pending && access.pending.state !== 'completed'
    const reachable = !managedControl(s) || ownershipFresh(s, access.now)
    if (!busy && reachable && s.advertised_capabilities.includes('interrupt')) control.push('interrupt')
    if (!busy && reachable && s.advertised_capabilities.includes('stop')) control.push('stop')
    if (managedControl(s) && SETTINGS.some(kind => s.advertised_capabilities.includes(kind))) control.push('settings')
  }
  const other: SessionAction[] = []
  if (view.ticket) other.push('ticket')
  other.push('copy')
  const note = live && s.management_mode === 'unmanaged' ? `Runs outside ${access.product} — stop it in its terminal` : ''
  return { control, other, remove: access.canRemove && !s.archived_at, note }
}
