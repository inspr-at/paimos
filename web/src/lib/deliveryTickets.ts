// SPDX-License-Identifier: AGPL-3.0-only
// Delivery › Tickets (design r4b §3.4; AEON-1359): what the Simple section, its tiles, the Expert tiles and the
// Created → live chart show, worked out from the server's status timing so it is tested without a browser. Nothing is
// extrapolated: a step without samples is "No data yet", a missing window before is "no data", never 0.
import type { DeliveryLanguage } from './delivery'
import { bucketOf, durationTop, num, type BucketUnit, type ChartBucket, type ChartModel, type Delta, type Part, type TileModel, type WindowDays } from './deliveryNumbers'
import { deliveryText, fill } from './deliveryNumbersText'
import { simpleText } from './deliverySimpleText'
import { stepOf, type StatusTimingStep, type StatusTimingStepKey, type TicketStatusTiming } from './statusTiming'
import { durationParts, fmtDurationSeconds as fmtDuration, shortDay, statusFill } from './workFigures'

export type Final = 'delivered' | 'done'
export const TOTAL_STEP: Record<Final, StatusTimingStepKey> = { delivered: 'created_to_live', done: 'created_to_done' }
export const STEPS: Record<Final, StatusTimingStepKey[]> = {
  delivered: ['new_to_picked_up', 'in_progress', 'qa_to_merged', 'done_to_live', 'live_to_accepted'],
  done: ['new_to_picked_up', 'in_progress', 'qa_to_merged'],
}
// "Typical time per step" in the hero, filled like the statuses each step is spent in (spec §5.1).
const TYPICAL_STEPS: Record<Final, StatusTimingStepKey[]> = { delivered: ['new_to_picked_up', 'in_progress', 'qa_to_merged', 'done_to_live'], done: ['new_to_picked_up', 'in_progress', 'qa_to_merged'] }
const STEP_FILL: Partial<Record<StatusTimingStepKey, string>> = { new_to_picked_up: statusFill('new'), in_progress: statusFill('in_progress'), qa_to_merged: statusFill('qa'), done_to_live: statusFill('done'), live_to_accepted: statusFill('delivered') }

export interface Trend { cls: 'better' | 'worse' | 'same' | 'none'; icon: 'arrow-up' | 'arrow-down' | 'minus'; text: string; prev: string; delta: string; word: string }
/** The change against the window before: steady within 10 %; lower is better for every ticket step. */
export function trendOf(step: StatusTimingStep | null, days: number, lang: DeliveryLanguage): Trend {
  const text = simpleText(lang), words = deliveryText(lang)
  const fmt = (seconds: number) => fmtDuration(seconds, lang)
  if (!step || step.p50 == null) return { cls: 'none', icon: 'minus', text: text.tNoData, prev: '', delta: '', word: '' }
  if (!step.prev_n || step.prev_p50 == null) return { cls: 'none', icon: 'minus', text: fill(text.tNone, { w: days }), prev: '', delta: '', word: '' }
  const change = step.prev_p50 === 0 ? (step.p50 === 0 ? 0 : 1) : (step.p50 - step.prev_p50) / step.prev_p50
  if (Math.abs(change) < 0.1) return { cls: 'same', icon: 'minus', text: fill(text.tSame, { w: days }), prev: fmt(step.prev_p50), delta: text.tickets.within, word: words.same }
  const better = change < 0
  return {
    cls: better ? 'better' : 'worse', icon: change < 0 ? 'arrow-down' : 'arrow-up', text: fill(better ? text.tBetter : text.tWorse, { w: days }),
    prev: fmt(step.prev_p50), delta: `${change < 0 ? '−' : '+'}${fmt(Math.abs(step.p50 - step.prev_p50))}`, word: better ? words.better : words.worse,
  }
}

/** A sentence with one emphasised value, split around its placeholder ("Slow: 1 in 10 over **5.9d**"). */
export interface Emphasis { before: string; value: string; after: string }
export function emphasis(template: string, key: string, value: string): Emphasis {
  const at = template.indexOf(`{${key}}`)
  return at < 0 ? { before: template, value: '', after: '' } : { before: template.slice(0, at), value, after: template.slice(at + key.length + 2) }
}
export interface StepTile {
  key: StatusTimingStepKey; name: string; value: [string, string] | null; slow: Emphasis | null; count: Emphasis; rework: boolean
  trend: Trend; say: string; learn: { body: string; facts: string[] }
}
export interface TicketsModel {
  final: Final; head: string; count: string
  hero: { key: StatusTimingStepKey; name: string; value: [string, string] | null; slow: Emphasis | null; trend: Trend; say: string; learn: { body: string; facts: string[] } }
  typical: { key: StatusTimingStepKey; name: string; fill: string; value: string; seconds: number | null }[]
  heroFoot: string; foot: string; partial: boolean
  steps: StepTile[]
  chart: ChartModel
  expert: TicketTileModel[]
}
/** Expert tiles reuse ExpertTile; their key is a ticket step rather than an Arion metric. */
export type TicketTileModel = Omit<TileModel, 'def'> & { def: { key: string } }

const plural = (n: number, [one, many]: readonly string[]) => fill(n === 1 ? one : many, { n })

/** The model for one answer. `timing` null (loading or failed) keeps every slot with empty values. */
export function ticketsModel(timing: TicketStatusTiming | null, days: WindowDays, lang: DeliveryLanguage, finalHint: Final = 'delivered'): TicketsModel {
  const text = simpleText(lang).tickets
  const final: Final = timing?.final_status ?? finalHint
  const fmt = (seconds: number) => fmtDuration(seconds, lang)
  const totalKey = TOTAL_STEP[final], total = stepOf(timing, totalKey)
  const since = timing?.coverage.since ? shortDay(timing.coverage.since, lang) : ''
  const partial = !!timing?.coverage.partial
  const foot = partial && since ? `${text.foot} · ${fill(text.history, { d: since })}` : text.foot
  const n = total?.n ?? 0, rework = timing?.rework ?? 0
  const learn = (key: StatusTimingStepKey) => {
    const step = stepOf(timing, key)
    return {
      body: text.learn[key],
      facts: [
        fill(text.learnFacts, { p50: step?.p50 != null ? fmt(step.p50) : '–', p90: step?.p90 != null ? fmt(step.p90) : '–', n: num(step?.n ?? 0, lang), w: days }),
        since ? fill(text.learnSource, { d: since }) : text.learnSourceAll,
      ],
    }
  }
  const slowOf = (step: StatusTimingStep | null) => step?.p90 != null ? emphasis(text.slow, 'v', fmt(step.p90)) : null

  const typical = TYPICAL_STEPS[final].map(key => {
    const step = stepOf(timing, key)
    return { key, name: text.steps[key], fill: STEP_FILL[key]!, value: step?.p50 != null ? fmt(step.p50) : '—', seconds: step?.p50 ?? null }
  })
  let say = text.heroNone[final]
  if (total?.p50 != null && total.p90 != null) {
    const longest = typical.filter(step => step.seconds != null).sort((a, b) => b.seconds! - a.seconds!).slice(0, 2).map(step => step.name)
    say = fill(text.heroSay[final], { p50: fmt(total.p50), p90: fmt(total.p90) })
      + (longest.length === 2 ? fill(text.longest, { a: longest[0], b: longest[1] }) : longest.length === 1 ? fill(text.longestOne, { a: longest[0] }) : '')
  }
  const heroTrend = trendOf(total, days, lang)
  const steps: StepTile[] = STEPS[final].map(key => {
    const step = stepOf(timing, key), trend = trendOf(step, days, lang)
    return {
      key, name: text.steps[key], value: step?.p50 != null ? durationParts(step.p50, lang) : null, slow: slowOf(step),
      count: key === 'qa_to_merged' ? emphasis(text.rework, 'n', num(rework, lang)) : emphasis(text.counts[key], 'n', num(step?.n ?? 0, lang)), rework: key === 'qa_to_merged',
      trend, say: step?.n && step.p50 != null ? fill(text.say[key as keyof typeof text.say], { v: fmt(step.p50) }) : text.sayNone, learn: learn(key),
    }
  })

  const countText = plural(n, text.count[final])
  const expert: TicketTileModel[] = [totalKey, ...STEPS[final]].map(key => {
    const step = stepOf(timing, key), trend = trendOf(step, days, lang)
    const delta: Delta = trend.delta ? { cls: trend.cls, icon: trend.icon, text: trend.delta, word: trend.word } : { cls: 'none', icon: 'minus', text: trend.text, word: '' }
    const value = step?.p50 != null ? durationParts(step.p50, lang) : null
    const lineA: Part[] = step?.p50 != null ? [{ text: 'p50 ' }, { text: fmt(step.p50), strong: true }, { text: ' · p90 ' }, { text: step.p90 != null ? fmt(step.p90) : '–', strong: true }] : [{ text: 'p50 · p90 –' }]
    const counted = fill(text.counts[key], { n: num(step?.n ?? 0, lang) })
    return {
      def: { key: `tickets-${key}` }, label: text.steps[key], definition: text.learn[key], reason: null, target: text.noTarget,
      source: since ? fill(text.source, { d: since }) : text.sourceAll, status: step?.n ? (partial ? 'partial' : 'ok') : 'no_data', partial, foot: text.eventLog,
      value: value ? { main: value[0], unit: value[1] } : null, lineA: { parts: lineA, mono: true },
      lineB: key === 'qa_to_merged' ? `${counted} · ${fill(text.reworkShort, { n: num(rework, lang) })}` : counted, delta,
    }
  })

  return {
    final, head: text.head[final], count: countText,
    hero: { key: totalKey, name: text.hero[final], value: total?.p50 != null ? durationParts(total.p50, lang) : null, slow: slowOf(total), trend: heroTrend, say, learn: learn(totalKey) },
    typical, partial, foot,
    heroFoot: `${text.foot} · ${countText} · ${fill(text.rework, { n: num(rework, lang) })}${partial && since ? ` · ${fill(text.history, { d: since })}` : ''}`,
    steps, chart: ticketChart(timing, days, lang, final), expert,
  }
}

// ---------- The Created → live chart: p50 line and p50–p90 band per bucket, bridged across days without a release ----------
const DAY = 86_400
export function ticketChart(timing: TicketStatusTiming | null, days: WindowDays, lang: DeliveryLanguage, final: Final): ChartModel {
  const text = simpleText(lang).tickets, words = deliveryText(lang)
  const unit: BucketUnit = bucketOf(days)
  const raw = timing?.buckets ?? []
  const fmt = (seconds: number) => fmtDuration(seconds, lang)
  const count = (n: number) => plural(n, text.count[final])
  const buckets: ChartBucket[] = raw.map((bucket, index) => {
    const last = index === raw.length - 1, back = raw.length - 1 - index, from = bucket.from.slice(0, 10)
    const status = bucket.state === 'no_data' ? 'no_data' : bucket.state === 'partial' ? 'partial' : bucket.n > 0 ? 'ok' : 'empty'
    if (unit === 'day') {
      const label = last ? words.today : shortDay(from, lang)
      return { from, to: bucket.to.slice(0, 10), unit, status, label, long: label, show: days === 7 || back % 7 === 0 }
    }
    if (unit === 'week') return { from, to: bucket.to.slice(0, 10), unit, status, label: last ? words.thisWk : shortDay(from, lang), long: last ? words.thisWeek : `${words.weekOf} ${shortDay(from, lang)}`, show: back % (days === 90 ? 4 : 8) === 0 }
    const month = new Intl.DateTimeFormat(lang === 'de' ? 'de-AT' : 'en-GB', { month: 'short', timeZone: 'UTC' }).format(new Date(`${from}T00:00:00Z`))
    return { from, to: bucket.to.slice(0, 10), unit, status, label: month, long: last ? `${month} (${words.soFar})` : month, show: back % 3 === 0 }
  })
  const p50 = raw.map(bucket => bucket.n > 0 && bucket.p50 != null ? bucket.p50 / DAY : null)
  const p90 = raw.map(bucket => bucket.n > 0 && bucket.p90 != null ? bucket.p90 / DAY : null)
  const present = (list: (number | null)[]) => list.filter((value): value is number => value != null)
  const { top, clippedTo } = durationTop(present(p50), present(p90), null)
  const readouts = raw.map((bucket, index) => {
    const label = buckets[index].long
    if (bucket.state === 'no_data') return fill(text.readoutNoData, { l: label })
    if (!bucket.n || bucket.p50 == null) return `${label} · ${text.readoutNone[final][unit]}`
    return fill(text.readout, { l: label, p50: fmt(bucket.p50), p90: bucket.p90 != null ? fmt(bucket.p90) : '–', c: count(bucket.n) })
  })
  const shown = present(p50).length
  return {
    key: 'tickets', kind: 'duration', label: text.steps[TOTAL_STEP[final]], unit: text.unit, buckets,
    panels: [{ top, share: 1, caption: '', lines: [{ values: p50, tone: 'teal', connect: true, bridge: true }], band: p50.map((value, k) => value != null && p90[k] != null ? [value, p90[k]!] : null), fade: clippedTo != null, targets: [] }],
    readouts, foot: fill(text.chartFoot, { c: count(stepOf(timing, TOTAL_STEP[final])?.n ?? 0) }), legend: null, nights: [],
    sparse: buckets.length <= 13 || shown < buckets.length * 0.6,
  }
}
