// SPDX-License-Identifier: AGPL-3.0-only
// The Time column (AEON-1211, design AEON-1196): how long a live session has run,
// how long ago an ended one ended, and since when a paused one rests. The table says
// it in full ("42 min", "12 min ago", "since 09:58"); compact rows say it short at the
// end of line 1 ("42m", "12m ago", "09:58"). The avatar's tooltip says what it means.
import { clockTime } from '../../lib/agentPause'
import { duration } from '../../lib/agentState'
import { relativeTime } from '../../lib/work'
import type { HarnessSession } from '../../lib/agents'

type TimeSession = Pick<HarnessSession, 'created_at' | 'stopped_at' | 'heartbeat_at' | 'pause'>
export interface TimeView { session: TimeSession; status: { state: string } }

const at = (iso: string | null | undefined) => { const t = Date.parse(iso ?? ''); return Number.isFinite(t) ? t : NaN }

/** "40 s", "42 min", "3 h 12 min", "2 d 3 h": a duration in words for the table and tooltips. */
export function spoken(ms: number) {
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return `${s} s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} min`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${h} h ${m % 60} min` : `${h} h`
  const d = Math.floor(h / 24)
  return h % 24 ? `${d} d ${h % 24} h` : `${d} d`
}

type Kind = 'running' | 'ended' | 'paused'
function kindOf(view: TimeView): { kind: Kind; since: number } {
  const s = view.session
  if (view.status.state === 'paused') {
    const since = at(s.pause?.paused_at ?? s.stopped_at)
    if (Number.isFinite(since)) return { kind: 'paused', since }
  }
  const stopped = at(s.stopped_at)
  if (Number.isFinite(stopped)) return { kind: 'ended', since: stopped }
  return { kind: 'running', since: at(s.created_at) }
}

/** The table's Time cell. */
export function timeText(view: TimeView, now: number) {
  const { kind, since } = kindOf(view)
  if (!Number.isFinite(since)) return '—'
  if (kind === 'paused') return `since ${clockTime(since)}`
  if (kind === 'ended') return relativeTime(new Date(since).toISOString(), { now, long: true })
  return spoken(now - since)
}
/** The compact row's time, at the end of line 1. */
export function timeShort(view: TimeView, now: number) {
  const { kind, since } = kindOf(view)
  if (!Number.isFinite(since)) return ''
  if (kind === 'paused') return clockTime(since)
  if (kind === 'ended') return relativeTime(new Date(since).toISOString(), { now })
  return duration(now - since)
}
/** What the time means, for the avatar's tooltip and the Time cell's. */
export function timeTip(view: TimeView, now: number) {
  const s = view.session
  const { kind, since } = kindOf(view)
  if (!Number.isFinite(since)) return ''
  if (kind === 'paused') return `Paused since ${clockTime(since)}`
  if (kind === 'ended') {
    const ran = Number.isFinite(at(s.created_at)) ? ` · ran ${spoken(since - at(s.created_at))}` : ''
    return `${view.status.state === 'done' ? 'Done' : 'Ended'} ${relativeTime(new Date(since).toISOString(), { now, long: true })}${ran}`
  }
  const beat = at(s.heartbeat_at)
  if (view.status.state === 'unresponsive') return `${Number.isFinite(beat) ? `No heartbeat for ${spoken(now - beat)}` : 'No heartbeat yet'} · running ${spoken(now - since)}`
  return `Running ${spoken(now - since)}`
}
/** The Time sort: the duration the column shows, so ascending reads top down. */
export function timeValue(view: TimeView, now: number) {
  const { since } = kindOf(view)
  return Number.isFinite(since) ? Math.max(0, now - since) : Number.POSITIVE_INFINITY
}
