// SPDX-License-Identifier: AGPL-3.0-only
// Everything the lifeline strip on a ticket says (design r4b §3.3, §5.2, §6.2; AEON-1359), worked out from the
// event-log lifeline and the project's typical step times, so it is tested without a browser.
import type { DeliveryLanguage } from './delivery'
import type { StatusTimingActor, TicketLifeline } from './statusTiming'
import { statusMeta } from './work'
import { clock, dayKey, fmtDurationSeconds as fmtDuration, lifeOf, markersOf, slowness, STEP_OF_STATUS, statusFill, whenFull, type Life, type LifeSegment, type Marker, type Typicals } from './workFigures'
import { put, lifelineText as workFiguresText, type LifelineText as WorkFiguresText } from './workFiguresText'

export interface TipLine { text: string; tone: 'title' | 'muted' | 'plain' | 'attn' }
export interface LineItem { label: string; value: string | null; soFar: boolean; usual: { text: string; attn: boolean } | null }
export interface SegmentView { index: number; status: string; seconds: number; fill: string; now: boolean; tip: TipLine[] }
export interface MarkerView { kind: Marker['kind']; index: number; icon: string; text: string }
export interface LifelineView {
  life: Life
  caption: { label: string; value: string; after: string }
  items: LineItem[]
  partly: boolean
  segments: SegmentView[]
  markers: MarkerView[]
  markerModels: Marker[]
}

export function statusLabel(status: string, text: WorkFiguresText): string {
  return text.status?.[status] ?? statusMeta(status).label
}

export function lifelineView(lifeline: TicketLifeline, typicals: Typicals | null, lang: DeliveryLanguage = 'en'): LifelineView {
  const text = workFiguresText(lang), life = lifeOf(lifeline)
  const fmt = (seconds: number) => fmtDuration(seconds, lang)
  const label = (status: string) => statusLabel(status, text)
  const segLabel = (segment: LifeSegment) => segment.again ? put(text.again, { s: label(segment.status) }) : label(segment.status)
  const who = (actor: StatusTimingActor) => `${actor.name}${text.kinds[actor.kind] ?? ''}`

  const last = life.visits[life.visits.length - 1]
  const caption = life.liveSeconds != null ? { label: text.live, value: fmt(life.liveSeconds), after: '' }
    : life.open ? { label: '', value: fmt(life.lifeSeconds), after: text.soFar }
      : { label: '', value: fmt(life.lifeSeconds), after: put(text.fromCreated, { s: label(last?.status ?? '') }) }

  const items: LineItem[] = life.segments.map(segment => {
    const slow = slowness(segment, typicals)
    return { label: segLabel(segment), value: fmt(segment.seconds), soFar: segment.now, usual: slow ? { text: put(text.usually, { v: fmt(slow.p50) }), attn: slow.attn } : null }
  })
  if (life.end) items.push({ label: label(life.end.status), value: null, soFar: false, usual: null })

  const segments: SegmentView[] = life.segments.map(segment => {
    const tip: TipLine[] = [{ text: `${segLabel(segment)} · ${fmt(segment.seconds)}${segment.now ? ` ${text.soFar}` : ''}`, tone: 'title' }]
    const end = segment.now ? null : segment.end
    const until = segment.now || end == null ? text.now : dayKey(segment.start) === dayKey(end) ? clock(end) : whenFull(end, lang)
    tip.push({ text: put(text.range, { a: whenFull(segment.start, lang), b: until }), tone: 'muted' })
    if (segment.now) tip.push({ text: put(text.still, { s: text.stillWords[segment.status] ?? label(segment.status).toLowerCase() }), tone: 'plain' })
    else if (segment.endedBy && segment.next) {
      const release = segment.next === 'delivered' && segment.nextRelease ? put(text.withRelease, { r: segment.nextRelease }) : ''
      const dayAfter = segment.status === 'delivered' && segment.next === 'accepted' && fmt(segment.seconds) === fmtDuration(86_400, lang) ? text.dayAfterLive : ''
      tip.push({ text: `${put(text.moved, { who: who(segment.endedBy), s: label(segment.next) })}${release}${dayAfter}`, tone: 'plain' })
    }
    if (segment.assumed) tip.push({ text: text.assumed, tone: 'muted' })
    const step = STEP_OF_STATUS[segment.status], typical = step ? typicals?.[step] : undefined
    if (segment.status === 'blocked') tip.push({ text: text.blocked, tone: 'muted' })
    else if (step && typical?.p50 != null) {
      const slow = slowness(segment, typicals), values = { p50: fmt(typical.p50), p90: typical.p90 != null ? fmt(typical.p90) : '', step: text.steps[step] }
      const tail = typical.p90 != null
      if (slow?.attn) tip.push({ text: put(text.attn, values), tone: 'attn' })
      else if (slow) tip.push({ text: put(tail ? text.slower : text.slowerNoTail, values), tone: 'plain' })
      else tip.push({ text: put(tail ? text.usual : text.usualNoTail, values), tone: 'muted' })
    }
    return { index: segment.index, status: segment.status, seconds: segment.seconds, fill: statusFill(segment.status), now: segment.now, tip }
  })

  const markerModels = markersOf(life)
  const byText = (actor: StatusTimingActor | null) => actor ? put(text.by, { who: actor.name }) : text.created
  const markers: MarkerView[] = markerModels.map(marker => {
    const when = whenFull(marker.at, lang)
    let words: string
    if (marker.kind === 'now') words = `${put(text.nowSince, { s: label(marker.status ?? ''), when })}${marker.by ? ` · ${byText(marker.by)}` : ''}`
    else if (marker.kind === 'final' && !marker.gate) words = `${label(marker.status ?? '')} · ${when} · ${byText(marker.by)} · ${text.current}`
    else {
      const gate = marker.gate!
      words = `${put(gate.again ? text.enteredAgain : text.entered, { g: text.gates[gate.group.id] })} · ${when} · ${byText(marker.by)}${marker.kind === 'final' ? ` · ${text.current}` : ''}`
    }
    return { kind: marker.kind, index: marker.index, icon: marker.icon, text: words }
  })

  return { life, caption, items, partly: !lifeline.complete, segments, markers, markerModels }
}

/** The step typicals for the lifeline, from the project's Delivery › Tickets answer at 90 days and no filters. */
export function typicalsOf(steps: readonly { step: string; p50: number | null; p90: number | null }[] | null | undefined): Typicals | null {
  if (!steps) return null
  const out: Typicals = {}
  for (const step of steps) out[step.step as keyof Typicals] = { p50: step.p50, p90: step.p90 }
  return out
}
