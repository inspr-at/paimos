// SPDX-License-Identifier: AGPL-3.0-only
// Ready and live estimates. Unknown stays empty; a past instant is overdue, which
// is separate from a report that has gone stale.

export type EtaMode = 'relative' | 'clock' | 'both'
export type EtaKind = 'Ready' | 'Live'

export interface EtaSide {
  at: string
  reported_at?: string | null
  by?: string | null
  stale?: boolean
  kind: EtaKind
}

export interface EtaInput {
  ready?: EtaSide | null
  live?: EtaSide | null
  progress?: number | null
  stale?: boolean
}

export interface TicketEta {
  eta_ready_at?: string | null
  eta_live_at?: string | null
  progress_pct?: number | null
  ready_reported_at?: string | null
  live_reported_at?: string | null
  ready_by?: string | null
  live_by?: string | null
  ready_stale?: boolean
  live_stale?: boolean
  eta_stale?: boolean
}

export interface EtaView {
  ready: string | null
  live: string | null
  readyHover: string | null
  liveHover: string | null
  progress: string | null
  tip: string
  stale: boolean
}

export function etaFromTicket(eta: TicketEta | null | undefined): EtaInput | null {
  if (!eta) return null
  const ready = eta.eta_ready_at ? { at: eta.eta_ready_at, reported_at: eta.ready_reported_at, by: eta.ready_by, stale: eta.ready_stale, kind: 'Ready' as const } : null
  const live = eta.eta_live_at ? { at: eta.eta_live_at, reported_at: eta.live_reported_at, by: eta.live_by, stale: eta.live_stale, kind: 'Live' as const } : null
  const progress = typeof eta.progress_pct === 'number' ? eta.progress_pct : null
  if (!ready && !live && progress == null) return null
  return { ready, live, progress, stale: !!(eta.eta_stale || eta.ready_stale || eta.live_stale) }
}

export function etaFromSession(session: {
  eta_ready_at?: string | null
  eta_live_at?: string | null
  progress_pct?: number | null
  eta_reported_at?: string | null
  eta_stale?: boolean
  agent?: { name: string } | null
}): EtaInput | null {
  return etaFromTicket({
    eta_ready_at: session.eta_ready_at, eta_live_at: session.eta_live_at, progress_pct: session.progress_pct,
    ready_reported_at: session.eta_reported_at, live_reported_at: session.eta_reported_at,
    ready_by: session.agent?.name, live_by: session.agent?.name,
    ready_stale: session.eta_stale, live_stale: session.eta_stale,
  })
}

function relative(at: string, now: number): string {
  const delta = Date.parse(at) - now
  const ahead = delta >= 0
  const minutes = Math.round(Math.abs(delta) / 60_000)
  const body = minutes < 1 ? (ahead ? '<1 min' : 'overdue')
    : minutes < 90 ? `${minutes} min`
    : minutes < 36 * 60 ? `${Math.round(minutes / 60)} h`
    : `${Math.round(minutes / 1440)} d`
  if (!ahead) return minutes < 1 ? 'overdue' : `${body} overdue`
  return minutes < 1 ? '<1 min' : `~${body}`
}

function clock(at: string, timeZone: string): string {
  return new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone }).format(new Date(at))
}

function whenLabel(at: string | null | undefined, timeZone: string): string {
  if (!at || Number.isNaN(Date.parse(at))) return ''
  return new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone }).format(new Date(at))
}

function sideTip(side: EtaSide, timeZone: string): string {
  const who = side.by?.trim()
  const when = whenLabel(side.reported_at || side.at, timeZone)
  return [side.kind, who, when].filter(Boolean).join(' · ')
}

// formatEta returns null when nothing was reported. `timeZone` is explicit so tests
// can pin a clock; the screen passes the viewer's zone.
export function formatEta(input: EtaInput | null | undefined, mode: EtaMode, now: number, timeZone = 'UTC'): EtaView | null {
  if (!input) return null
  const sides = [input.ready, input.live].filter((side): side is EtaSide => !!side?.at && !Number.isNaN(Date.parse(side.at)))
  const progress = typeof input.progress === 'number' ? `${input.progress}%` : null
  if (!sides.length && !progress) return null
  const text = (side: EtaSide) => mode === 'clock' ? clock(side.at, timeZone) : relative(side.at, now)
  const hover = (side: EtaSide) => mode === 'both' ? clock(side.at, timeZone) : null
  return {
    ready: input.ready ? text(input.ready) : null,
    live: input.live ? text(input.live) : null,
    readyHover: input.ready ? hover(input.ready) : null,
    liveHover: input.live ? hover(input.live) : null,
    progress,
    tip: [...sides.map(side => sideTip(side, timeZone)), progress ? `${progress} done` : ''].filter(Boolean).join('\n'),
    stale: !!input.stale || sides.some(side => side.stale),
  }
}
