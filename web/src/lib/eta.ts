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
  // The server's completion evidence: no session is open on the ticket and the last
  // worker reported 100% and recorded a clean exit. The only thing that reads as Done.
  finished?: { at?: string | null; by?: string | null } | null
}

export interface TicketEta {
  has_working_session?: boolean
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
  finished?: boolean
  finished_at?: string | null
  finished_by?: string | null
}

// One compact reading: the headline estimate (ready first, else live), its other
// form for hover, the percent, and a tooltip that names both forms, who and when.
export interface EtaView {
  kind: EtaKind | null
  text: string | null
  hover: string | null
  progress: string | null
  pct: number | null
  overdue: boolean
  stale: boolean
  // The server holds positive completion evidence: a calm Done, no time (AEON-437).
  done: boolean
  tip: string
}

export function etaFromTicket(eta: TicketEta | null | undefined): EtaInput | null {
  if (!eta) return null
  const ready = eta.eta_ready_at ? { at: eta.eta_ready_at, reported_at: eta.ready_reported_at, by: eta.ready_by, stale: eta.ready_stale, kind: 'Ready' as const } : null
  const live = eta.eta_live_at ? { at: eta.eta_live_at, reported_at: eta.live_reported_at, by: eta.live_by, stale: eta.live_stale, kind: 'Live' as const } : null
  const progress = typeof eta.progress_pct === 'number' ? eta.progress_pct : null
  const finished = eta.finished ? { at: eta.finished_at, by: eta.finished_by } : null
  if (!ready && !live && progress == null && !finished) return null
  return { ready, live, progress, stale: !!(eta.eta_stale || eta.ready_stale || eta.live_stale), finished }
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

function span(minutes: number): string {
  return minutes < 90 ? `${minutes} min` : minutes < 36 * 60 ? `${Math.round(minutes / 60)} h` : `${Math.round(minutes / 1440)} d`
}

function relative(at: string, now: number): string {
  const delta = Date.parse(at) - now
  const minutes = Math.round(Math.abs(delta) / 60_000)
  if (delta >= 0) return minutes < 1 ? '<1 min' : `~${span(minutes)}`
  return minutes < 1 ? 'due now' : `overdue ${span(minutes)}`
}

const dayOf = (ms: number, timeZone: string) => new Intl.DateTimeFormat('en-CA', { timeZone }).format(new Date(ms))

// A clock time names the weekday once it is not today, so "15:40" is never ambiguous.
function clock(at: string, now: number, timeZone: string): string {
  const ms = Date.parse(at)
  const time = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hourCycle: 'h23', timeZone }).format(new Date(ms))
  if (dayOf(ms, timeZone) === dayOf(now, timeZone)) return time
  const far = Math.abs(ms - now) > 6 * 86_400_000
  const day = new Intl.DateTimeFormat('en-GB', far ? { day: 'numeric', month: 'short', timeZone } : { weekday: 'short', timeZone }).format(new Date(ms))
  return `${day} ${time}`
}

const valid = (at: string | null | undefined): at is string => !!at && !Number.isNaN(Date.parse(at))

function sideTip(side: EtaSide, now: number, timeZone: string): string[] {
  const when = clock(side.at, now, timeZone)
  const rel = relative(side.at, now)
  const past = Date.parse(side.at) < now
  const head = past ? `${side.kind} was due at ${when}, ${rel}` : `${side.kind} at ${when}, in ${rel}`
  const who = side.by?.trim()
  if (!valid(side.reported_at)) return [head, ...(who ? [`Estimated by ${who}`] : [])]
  const from = clock(side.reported_at, now, timeZone)
  const age = Math.max(0, Math.round((now - Date.parse(side.reported_at)) / 60_000))
  const by = who ? ` by ${who}` : ''
  // A stale report leads with its age: that is the news, the time is a leftover.
  return side.stale ? [`Estimate from ${from}${by} is ${span(age)} old`, head] : [head, `Estimated${by} at ${from}`]
}

// At 100% there is nothing left to estimate: a due time that passed is not late, and
// a report that aged is not stale. Only the server's completion evidence reads Done;
// 100% with a session still on the ticket is just a full percent.
function doneTip(finished: NonNullable<EtaInput['finished']>, now: number, timeZone: string): string {
  const who = finished.by?.trim()
  return `All work reported${who ? ` by ${who}` : ''}${valid(finished.at) ? ` at ${clock(finished.at, now, timeZone)}` : ''}`
}

// formatEta returns null when nothing was reported. `timeZone` is explicit so tests
// can pin a clock; the screen passes the viewer's zone.
export function formatEta(input: EtaInput | null | undefined, mode: EtaMode, now: number, timeZone = 'UTC'): EtaView | null {
  if (!input) return null
  const sides = [input.ready, input.live].filter((side): side is EtaSide => !!side && valid(side.at))
  const pct = typeof input.progress === 'number' ? Math.max(0, Math.min(100, Math.round(input.progress))) : null
  if (!sides.length && pct == null && !input.finished) return null
  const complete = pct === 100 || !!input.finished
  const main = complete ? null : sides[0] ?? null
  const stale = !complete && (!!input.stale || sides.some(side => side.stale))
  const tip = input.finished ? [doneTip(input.finished, now, timeZone)] : complete ? ['100% done'] : [
    ...sides.flatMap(side => sideTip(side, now, timeZone)),
    ...(pct != null ? [`${pct}% done`] : []),
  ]
  if (stale && !sides.some(side => side.stale && valid(side.reported_at))) tip.push('Estimate not refreshed in time')
  return {
    kind: main?.kind ?? null,
    text: main ? (mode === 'clock' ? clock(main.at, now, timeZone) : relative(main.at, now)) : null,
    hover: main && mode === 'both' ? clock(main.at, now, timeZone) : null,
    progress: pct != null ? `${pct}%` : null,
    pct,
    overdue: !!main && Date.parse(main.at) < now,
    stale,
    done: !!input.finished,
    tip: tip.join('\n'),
  }
}

// The report time a stale progress reading can name. A stale ready estimate
// leads; a stale live estimate is the fallback; a flag without a side still
// uses whichever timestamp was stored.
export function progressReportedAt(eta: TicketEta | null | undefined): string | null {
  if (!eta) return null
  if (eta.ready_stale && eta.ready_reported_at) return eta.ready_reported_at
  if (eta.live_stale && eta.live_reported_at) return eta.live_reported_at
  if (eta.eta_stale || eta.ready_stale || eta.live_stale) return eta.ready_reported_at || eta.live_reported_at || null
  return null
}

// What a screen reader says for the compact progress cell, including when the
// ETA column is hidden. The clock matches the estimate tooltip.
export function progressAccessibleName(pct: number, stale: boolean, reportedAt: string | null | undefined, now: number, timeZone = 'UTC'): string {
  const done = `${pct}% done`
  if (!stale) return done
  if (!valid(reportedAt)) return `${done}, estimate stale`
  return `${done}, estimate stale since ${clock(reportedAt, now, timeZone)}`
}
