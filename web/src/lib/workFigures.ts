// SPDX-License-Identifier: AGPL-3.0-only
// Shared words and numbers of "Work at a glance" (design r4b, AEON-1356/1357; this part AEON-1359): one duration
// format everywhere, the ticket steps, the status icons' bar form, and the lifeline model (segments, gate markers and
// their layout). Pure, so tests/work-figures.unit.test.ts runs it without a browser.
import type { DeliveryLanguage } from './delivery'
import { canonicalStatus, HEADER_STATUS_GROUPS, type HeaderStatusGroup } from './projectStatusCounts'
import type { StatusTimingActor, StatusTimingStepKey, StatusTimingVisit, TicketLifeline } from './statusTiming'
import { statusMeta, type StatusKey } from './work'

// ---------- Durations: 40m · 2.4h · 5h · 14h · 1.5d · 2d · 12d (minimum 1m) ----------
const one = (value: number, lang: DeliveryLanguage) => {
  const rounded = Math.round(value * 10) / 10
  const text = Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1)
  return lang === 'de' ? text.replace('.', ',') : text
}
/** Seconds in the one duration format of the design (spec §1). */
export function fmtDuration(seconds: number | null | undefined, lang: DeliveryLanguage = 'en'): string {
  if (seconds == null || !Number.isFinite(seconds)) return '—'
  const minutes = seconds / 60
  if (minutes < 59.5) return `${Math.max(1, Math.round(minutes))}m`
  const hours = minutes / 60
  if (hours < 9.95) return `${one(hours, lang)}h`
  if (Math.round(hours) < 24) return `${Math.round(hours)}h`
  const days = hours / 24
  const unit = lang === 'de' ? 'T' : 'd'
  return days < 9.95 ? `${one(days, lang)}${unit}` : `${Math.round(days)}${unit}`
}
/** A duration as [number, unit], for the big tile values ("1.5" + "d"). */
export function durationParts(seconds: number, lang: DeliveryLanguage = 'en'): [string, string] {
  const text = fmtDuration(seconds, lang), match = text.match(/^([\d.,]+)(\D+)$/)
  return match ? [match[1], match[2]] : [text, '']
}

// ---------- Dates as the app writes them: "Wed 14 Oct 03:48" (never "Sept"), the person's own time zone ----------
const MONTHS: Record<DeliveryLanguage, string[]> = {
  en: ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'],
  de: ['Jän', 'Feb', 'März', 'Apr', 'Mai', 'Juni', 'Juli', 'Aug', 'Sep', 'Okt', 'Nov', 'Dez'],
}
const WEEKDAYS: Record<DeliveryLanguage, string[]> = {
  en: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'],
  de: ['So', 'Mo', 'Di', 'Mi', 'Do', 'Fr', 'Sa'],
}
const pad = (n: number) => String(n).padStart(2, '0')
export const clock = (ms: number) => { const d = new Date(ms); return `${pad(d.getHours())}:${pad(d.getMinutes())}` }
export const dayKey = (ms: number) => { const d = new Date(ms); return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}` }
export function dayMonth(ms: number, lang: DeliveryLanguage = 'en'): string {
  const d = new Date(ms)
  return lang === 'de' ? `${d.getDate()}. ${MONTHS.de[d.getMonth()]}` : `${d.getDate()} ${MONTHS.en[d.getMonth()]}`
}
export function whenFull(ms: number, lang: DeliveryLanguage = 'en'): string {
  return `${WEEKDAYS[lang][new Date(ms).getDay()]} ${dayMonth(ms, lang)} ${clock(ms)}`
}

// ---------- Ticket steps (spec §1, §2) ----------
/** The step whose typical time a status visit is compared with; Blocked and the closed statuses have none. */
export const STEP_OF_STATUS: Readonly<Partial<Record<string, StatusTimingStepKey>>> = {
  new: 'new_to_picked_up', open: 'new_to_picked_up', backlog: 'new_to_picked_up',
  in_progress: 'in_progress', qa: 'qa_to_merged', done: 'done_to_live', delivered: 'live_to_accepted',
}
export interface StepTypical { p50: number | null; p90: number | null }
export type Typicals = Partial<Record<StatusTimingStepKey, StepTypical>>

// ---------- The status icons' bar form (spec §5.1) ----------
// Ring → outline, in progress → 45° stripes, filled → solid, dotted and dashed rings → dotted and dashed outlines,
// ⊘ → grey tint with one slash, □ → square outline. The classes live in styles/status-fills.css.
export const STATUS_FILL: Readonly<Record<StatusKey, string>> = {
  new: 'f-ring', open: 'f-ring', backlog: 'f-dotted', blocked: 'f-dashed', progress: 'f-diag', qa: 'f-halo',
  done: 'f-tint', delivered: 'f-solid', accepted: 'f-deep', cancelled: 'f-cancel', archived: 'f-square', other: 'f-ring',
}
export function statusFill(state: string): string {
  const { key } = statusMeta(state)
  return `${STATUS_FILL[key]} fc-${key}`
}

// ---------- The lifeline (spec §2 Lifeline, §3.3, §5.2, §6) ----------
const TERMINAL = new Set(['done', 'delivered', 'accepted', 'cancelled', 'archived'])
export interface LifeVisit {
  index: number; status: string; start: number; end: number | null; seconds: number
  movedBy: StatusTimingActor | null; endedBy: StatusTimingActor | null; next: string | null; nextRelease: string | null
  again: boolean; ongoing: boolean; assumed: boolean
}
export interface LifeSegment extends LifeVisit { now: boolean }
export interface Life {
  visits: LifeVisit[]; segments: LifeSegment[]; end: LifeVisit | null; open: boolean
  created: number; generatedAt: number; lifeSeconds: number; liveSeconds: number | null
}

export function lifeOf(lifeline: TicketLifeline): Life {
  const raw: StatusTimingVisit[] = lifeline.visits ?? []
  const generatedAt = Date.parse(lifeline.generated_at)
  const visits: LifeVisit[] = raw.map((visit, index) => ({
    index, status: canonicalStatus(visit.status), start: Date.parse(visit.from), end: visit.to ? Date.parse(visit.to) : null,
    seconds: Math.max(0, visit.seconds), movedBy: visit.moved_by, endedBy: visit.ended_by,
    next: visit.next_status ? canonicalStatus(visit.next_status) : null, nextRelease: raw[index + 1]?.release ?? null,
    again: visit.again, ongoing: visit.ongoing, assumed: visit.source === 'inferred',
  }))
  const last = visits[visits.length - 1]
  const open = !!last?.ongoing
  const lastIsEnd = !!last && !open && TERMINAL.has(last.status)
  const kept = lastIsEnd ? visits.slice(0, -1) : visits
  const segments = kept.map((visit, index) => ({ ...visit, now: open && index === kept.length - 1 }))
  const created = visits[0]?.start ?? generatedAt
  const delivered = visits.find(visit => visit.status === 'delivered')
  return {
    visits, segments, end: lastIsEnd ? last : null, open, created, generatedAt,
    lifeSeconds: Math.max(0, ((open ? generatedAt : last?.start ?? created) - created) / 1000),
    liveSeconds: delivered ? Math.max(0, (delivered.start - created) / 1000) : null,
  }
}

export interface Slowness { p50: number; p90: number | null; attn: boolean }
/** Slower than usual: more than 1.5 × the step's p50 and at least 10 minutes over it; attention above p90. */
export function slowness(segment: Pick<LifeVisit, 'status' | 'seconds'>, typicals: Typicals | null): Slowness | null {
  const step = STEP_OF_STATUS[segment.status]
  const typical = step ? typicals?.[step] : undefined
  if (!typical || typical.p50 == null) return null
  if (segment.seconds <= Math.max(typical.p50 * 1.5, typical.p50 + 600)) return null
  return { p50: typical.p50, p90: typical.p90, attn: typical.p90 != null && segment.seconds > typical.p90 }
}

// Gates are the header's four status groups; a marker sits where the ticket entered one (again after a backward move).
export type GateId = HeaderStatusGroup['id']
export const gateOf = (status: string): HeaderStatusGroup | undefined => HEADER_STATUS_GROUPS.find(group => (group.states as readonly string[]).includes(status))
export interface Gate { group: HeaderStatusGroup; index: number; at: number; by: StatusTimingActor | null; again: boolean }
export function gatesOf(visits: readonly LifeVisit[]): Gate[] {
  const out: Gate[] = [], seen = new Set<string>()
  let previous: string | null = null
  visits.forEach((visit, index) => {
    const group = gateOf(visit.status)
    if (!group) return
    if (group.id !== previous) { out.push({ group, index, at: visit.start, by: index ? visit.movedBy : null, again: seen.has(group.id) }); seen.add(group.id) }
    previous = group.id
  })
  return out
}

export type MarkerKind = 'gate' | 'final' | 'now'
export interface Marker { kind: MarkerKind; index: number; at: number; by: StatusTimingActor | null; icon: string; gate: Gate | null; status: string | null }
/** The gates plus one marker at the right end: the final status (finished) or the current one (open, a "now" notch).
 * When the final status is itself a gate, the two are one marker with the final status's icon. */
export function markersOf(life: Life): Marker[] {
  const markers: Marker[] = gatesOf(life.visits).map(gate => ({ kind: 'gate', index: gate.index, at: gate.at, by: gate.by, icon: gate.group.icon, gate, status: null }))
  const lastIndex = life.visits.length - 1
  if (lastIndex < 0) return markers
  const last = life.visits[lastIndex], lastGate = markers[markers.length - 1]
  if (!life.open && TERMINAL.has(last.status) && lastGate && lastGate.index === lastIndex) Object.assign(lastGate, { kind: 'final', status: last.status, icon: last.status })
  else markers.push({ kind: life.open ? 'now' : 'final', index: lastIndex, at: last.start, by: lastIndex ? last.movedBy : null, icon: last.status, gate: null, status: last.status })
  return markers
}

/** Segment widths in proportion to time, each at least `min` px, with `gap` px between them, filling `width`. */
export function stripWidths(durations: readonly number[], width: number, min = 6, gap = 2): number[] {
  const n = durations.length, available = Math.max(0, width - gap * Math.max(0, n - 1))
  const fixed = new Array<boolean>(n).fill(false)
  let widths: number[] = []
  for (let round = 0; round <= n; round++) {
    const free = durations.reduce((sum, d, i) => fixed[i] ? sum : sum + Math.max(0, d), 0), loose = fixed.filter(x => !x).length
    const room = available - (n - loose) * min
    widths = durations.map((d, i) => fixed[i] ? min : free > 0 ? Math.max(0, d) / free * room : room / Math.max(1, loose))
    let changed = false
    widths.forEach((w, i) => { if (!fixed[i] && w < min) { fixed[i] = true; changed = true } })
    if (!changed) break
  }
  return widths
}

export const MARKER_SIZE = 15
export const MARKER_STEP = 17
export interface MarkerPlace { notch: number; left: number; displaced: boolean }
/** Each marker's 1 px notch sits on a whole pixel at its true crossing, and its icon is centred on the notch (x + .5).
 * Markers never overlap: later gates yield rightwards, and earlier ones leftwards to the final marker pinned at the
 * right end; only the markers a cluster moved are off their notch (spec §6.1, §6.2). */
export function placeMarkers(markers: readonly Marker[], widths: readonly number[], stripWidth: number): MarkerPlace[] {
  const lefts: number[] = []
  let x = 0
  for (const w of widths) { lefts.push(x); x += w + 2 }
  const notch = markers.map(marker => Math.max(0, Math.min(stripWidth - 1,
    marker.kind !== 'gate' ? stripWidth - 1 : marker.index === 0 ? 0 : Math.round((lefts[marker.index] ?? stripWidth) - 1))))
  const centre = notch.map(value => value + 0.5), n = markers.length
  for (let k = 1; k < n - 1; k++) if (centre[k] < centre[k - 1] + MARKER_STEP) centre[k] = centre[k - 1] + MARKER_STEP
  for (let k = n - 2; k >= 0; k--) if (centre[k] > centre[k + 1] - MARKER_STEP) centre[k] = centre[k + 1] - MARKER_STEP
  return markers.map((_, k) => ({ notch: notch[k], left: centre[k] - MARKER_SIZE / 2, displaced: Math.abs(centre[k] - notch[k] - 0.5) > 0.01 }))
}
