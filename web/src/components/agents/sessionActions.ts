// SPDX-License-Identifier: AGPL-3.0-only
// AEON-291: a session menu offers only what works for that session. No disabled
// items with excuses; a session outside Aeon gets one quiet line instead.
import { HEARTBEAT_STALE_MS } from '../../lib/agentState.ts'
import { managedControlAllowed, managedControlSession } from '../../lib/managedControl.ts'
import type { HarnessSession } from '../../lib/agents'
import type { SessionView } from '../../stores/agents'

export type SessionAction = 'interrupt' | 'stop' | 'settings' | 'ticket' | 'copy' | 'remove'
export interface SessionMenu { control: SessionAction[]; other: SessionAction[]; remove: boolean; note: string }
export interface MenuAccess { canControl: boolean; canRemove: boolean; pending?: { state: string } | null; product: string; now: number }

const SETTINGS = ['rename', 'model', 'effort'] as const
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
  // managed_control_v1 eligibility is shared with the session panel (lib/managedControl).
  if (live && s.management_mode === 'managed' && access.canControl) {
    const busy = !!access.pending && access.pending.state !== 'completed'
    if (managedControlSession(s)) {
      const managed = { now: access.now, allowed: access.canControl, runStatus: s.run_status !== undefined ? s.run_status : view.run?.status }
      if (!busy && managedControlAllowed(s, 'interrupt', managed)) control.push('interrupt')
      if (!busy && managedControlAllowed(s, 'stop', managed)) control.push('stop')
      if (SETTINGS.some(kind => managedControlAllowed(s, kind, managed))) control.push('settings')
    } else {
      if (!busy && s.advertised_capabilities.includes('interrupt')) control.push('interrupt')
      if (!busy && s.advertised_capabilities.includes('stop')) control.push('stop')
    }
  }
  const other: SessionAction[] = []
  if (view.ticket) other.push('ticket')
  other.push('copy')
  const note = live && s.management_mode === 'unmanaged' ? `Runs outside ${access.product} — stop it in its terminal` : ''
  return { control, other, remove: access.canRemove && !s.archived_at, note }
}
