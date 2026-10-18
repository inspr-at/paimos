// SPDX-License-Identifier: AGPL-3.0-only
// Delivery › Numbers › Building it (AEON-1493): the server's four numbers (first-try rate, rounds per item,
// idle between rounds, lead time) turned into the shipped SimpleTile, ExpertTile and TrendChart models and the
// worst-five rows. Every figure, change and verdict comes from the server; this file only formats them, so a
// missing figure is "no data yet", never 0.
import { api, APIError } from './api.ts'
import type { DeliveryLanguage } from './delivery'
import { bucketOf, durationTop, niceCeil, num, pct, type BucketStatus, type BucketUnit, type ChartBucket, type ChartModel, type Delta, type Part, type WindowDays } from './deliveryNumbers'
import { deliveryText, fill } from './deliveryNumbersText'
import { ratioGap, type SimpleTileModel, type SparkModel, type Trend, type Verdict, type VerdictLevel } from './deliverySimple'
import { simpleText } from './deliverySimpleText'
import { buildingText, type BuildingKey, type RoundOutcome } from './deliveryBuildingText'
import type { TicketTileModel } from './deliveryTickets'
import type { TimingFilters } from './statusTiming'
import { shortDay } from './workFigures'

export type { BuildingKey, RoundOutcome } from './deliveryBuildingText'

// ---------- Contract (api/areas/projects-delivery-building.yaml) ----------
export interface BuildingMeasure { n: number; items: number; count: number; value: number | null; p50: number | null; p90: number | null; mean: number | null }
export interface BuildingBucket extends BuildingMeasure { from: string; to: string; state: 'observed' | 'partial' | 'no_data' }
export interface BuildingWorst { node_id: string; key: string; title: string; value: number; rounds: RoundOutcome[]; more_rounds: number }
export interface BuildingMetric {
  key: BuildingKey; unit: 'percent' | 'rounds' | 'minutes'; better: 'higher' | 'lower'
  target: { value: number; direction: 'min' | 'max'; source: 'delivery_settings' } | null
  current: BuildingMeasure; previous: BuildingMeasure
  change: { value: number | null; trend: 'better' | 'worse' | 'same' | 'none' }
  verdict: { level: VerdictLevel; ratio: number | null }
  buckets: BuildingBucket[]; worst: BuildingWorst[]
}
export interface DeliveryBuilding {
  project_id: string; generated_at: string; window: number; since: string | null
  coverage: { items: number; without_rounds: number; partial: boolean }
  metrics: BuildingMetric[]
}
export const BUILDING_KEYS: readonly BuildingKey[] = ['first_try', 'rounds', 'idle', 'lead']

export async function readDeliveryBuilding(projectId: string, window: number, filters: TimingFilters, signal?: AbortSignal): Promise<DeliveryBuilding> {
  const query = new URLSearchParams({ window: String(window), type: filters.type, priority: filters.priority, worked_by: filters.worked_by })
  const response = await api(`/projects/${encodeURIComponent(projectId)}/delivery/building?${query}`, { signal })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' && data.error ? data.error : `Request failed (${response.status})`)
  }
  const body = await response.json() as DeliveryBuilding
  if (!body || body.project_id !== projectId || body.window !== window || !Array.isArray(body.metrics) || BUILDING_KEYS.some(key => !body.metrics.some(metric => metric.key === key)))
    throw new APIError(200, 'Invalid building response')
  return body
}

// ---------- Numbers in words ----------
const NB = ' '
/** Minutes as the design says them: "22 min", "1 h 40 min", "31 h". */
export function hm(minutes: number, lang: DeliveryLanguage): string {
  const m = Math.round(minutes)
  if (m < 60) return `${num(Math.max(0, m), lang)}${NB}min`
  const h = Math.floor(m / 60), r = m % 60
  return r ? `${num(h, lang)}${NB}h${NB}${r}${NB}min` : `${num(h, lang)}${NB}h`
}
function minuteParts(minutes: number, lang: DeliveryLanguage): { text: string; unit: string }[] {
  const m = Math.round(minutes)
  if (m < 60) return [{ text: num(Math.max(0, m), lang), unit: 'min' }]
  const h = Math.floor(m / 60), r = m % 60
  return r ? [{ text: num(h, lang), unit: 'h' }, { text: String(r), unit: 'min' }] : [{ text: num(h, lang), unit: 'h' }]
}
const one = (n: number) => n === 1
const word = (n: number, [singular, plural]: readonly [string, string]) => one(n) ? singular : plural
/** A template with '|' between plain and bold runs: plain, bold, plain, bold … */
function runs(template: string, values: Record<string, string | number>): Part[] {
  return template.split('|').map((piece, index) => ({ text: fill(piece, values), strong: index % 2 === 1 || undefined })).filter(part => part.text)
}

/** The figure in its own words: share, rounds with one decimal, minutes as "1 h 40 min". */
function figure(key: BuildingKey, value: number, lang: DeliveryLanguage): string {
  if (key === 'first_try') return pct(value, lang)
  if (key === 'rounds') return num(value, lang, value % 1 ? 1 : 0)
  return hm(value, lang)
}
function targetFigure(key: BuildingKey, value: number, lang: DeliveryLanguage): string {
  if (key === 'first_try') return pct(value, lang)
  if (key === 'rounds') return num(value, lang, value % 1 ? 1 : 0)
  return hm(value, lang)
}

export interface BuildingTileView {
  key: BuildingKey
  simple: SimpleTileModel
  expert: TicketTileModel
  chart: ChartModel
  /** One p50/p90 line for the stacked phone tiles of the Expert level. */
  pp: Part[]
  worst: { rows: { nodeId: string; key: string; title: string; value: string; rounds: RoundOutcome[]; more: string; said: string }[]; title: string; sub: string }
}
export interface BuildingView {
  /** The head's count ("2 of 3 on target"), the no-data sentence or the error, in the same place. */
  head: { kind: 'loading' | 'ready' | 'nodata' | 'error'; text: string }
  tiles: BuildingTileView[]
  /** Simple tiles with a target and a value: the summary card's off-target line takes them. */
  summary: SimpleTileModel[]
}

const SPARK_STATUS: Record<BuildingBucket['state'], BucketStatus> = { observed: 'ok', partial: 'partial', no_data: 'no_data' }

function bucketsOf(raw: BuildingBucket[], days: WindowDays, lang: DeliveryLanguage): ChartBucket[] {
  const words = deliveryText(lang), unit: BucketUnit = bucketOf(days)
  return raw.map((bucket, index) => {
    const last = index === raw.length - 1, back = raw.length - 1 - index, from = bucket.from.slice(0, 10), to = bucket.to.slice(0, 10)
    const status: BucketStatus = bucket.state === 'no_data' ? 'no_data' : bucket.state === 'partial' ? 'partial' : bucket.n > 0 ? 'ok' : 'empty'
    if (unit === 'day') {
      const weekday = new Intl.DateTimeFormat(lang === 'de' ? 'de-AT' : 'en-GB', { weekday: 'short', timeZone: 'UTC' }).format(new Date(`${from}T00:00:00Z`))
      const label = last ? words.today : days === 7 ? `${weekday} ${Number(from.slice(8, 10))}` : shortDay(from, lang)
      return { from, to, unit, status, label, long: last ? words.today : `${weekday} ${shortDay(from, lang)}`, show: days === 7 || back % 7 === 0 }
    }
    if (unit === 'week') return { from, to, unit, status, label: last ? words.thisWk : shortDay(from, lang), long: last ? words.thisWeek : `${words.weekOf} ${shortDay(from, lang)}`, show: back % (days === 90 ? 4 : 8) === 0 }
    const month = new Intl.DateTimeFormat(lang === 'de' ? 'de-AT' : 'en-GB', { month: 'short', timeZone: 'UTC' }).format(new Date(`${from}T00:00:00Z`))
    return { from, to, unit, status, label: month, long: last ? `${month} (${words.soFar})` : month, show: back % 3 === 0 }
  })
}

/** Placeholders keep every tile and chart in its slot while loading, without data or after a failed read. */
function emptyMetric(key: BuildingKey): BuildingMetric {
  const none: BuildingMeasure = { n: 0, items: 0, count: 0, value: null, p50: null, p90: null, mean: null }
  return { key, unit: key === 'first_try' ? 'percent' : key === 'rounds' ? 'rounds' : 'minutes', better: key === 'first_try' ? 'higher' : 'lower', target: null,
    current: none, previous: none, change: { value: null, trend: 'none' }, verdict: { level: 'none', ratio: null }, buckets: [], worst: [] }
}

function chartOf(metric: BuildingMetric, target: BuildingMetric['target'], buckets: ChartBucket[], days: WindowDays, lang: DeliveryLanguage): ChartModel {
  const text = buildingText(lang), words = text.metrics[metric.key], key = metric.key
  const raw = metric.buckets, hours = key === 'lead'
  const scale = (value: number | null) => value == null ? null : hours ? value / 60 : value
  const line = raw.map(bucket => bucket.n > 0 ? scale(key === 'first_try' ? bucket.value : bucket.p50) : null)
  const high = raw.map(bucket => bucket.n > 0 && key !== 'first_try' ? scale(bucket.p90) : null)
  const present = (list: (number | null)[]) => list.filter((value): value is number => value != null)
  let top = 100, fade = false
  if (key === 'rounds') top = Math.max(4, niceCeil(Math.max(4, ...present(high), target?.value ?? 0) * 1.1))
  else if (key !== 'first_try') {
    const tail = durationTop(present(line), present(high), target ? scale(target.value) : null)
    top = tail.top
    fade = tail.clippedTo != null
  }
  const items = (n: number) => word(n, text.items), waits = (n: number) => word(n, text.waits)
  const readouts = raw.map((bucket, index) => {
    const l = buckets[index]?.long ?? ''
    if (bucket.state === 'no_data') return `${l} · ${text.noDataYet}`
    if (!bucket.n) return `${l} · ${text.nothing}`
    const v = (value: number | null) => value == null ? '–' : figure(key, value, lang)
    if (key === 'first_try') return fill(text.readout.first, { l, v: v(bucket.value), k: num(bucket.count, lang), n: num(bucket.n, lang), items: items(bucket.n) })
    if (key === 'rounds') return fill(text.readout.rounds, { l, p50: v(bucket.p50), mean: v(bucket.mean), n: num(bucket.n, lang), items: items(bucket.n) })
    if (key === 'idle') return fill(text.readout.idle, { l, p50: v(bucket.p50), p90: v(bucket.p90), n: num(bucket.n, lang), waits: waits(bucket.n) })
    return fill(text.readout.lead, { l, p50: v(bucket.p50), p90: v(bucket.p90), n: num(bucket.n, lang), items: items(bucket.n) })
  })
  const label = text.chartTarget[key]
  return {
    key: `building-${key}`, kind: key === 'first_try' ? 'share' : key === 'rounds' ? 'runs' : 'duration', label: text.per[bucketOf(days)], unit: words.chartUnit, buckets,
    panels: [{ top, share: 1, caption: '', lines: [{ values: line, tone: 'teal', connect: true }], fade,
      band: key === 'first_try' ? null : line.map((low, index) => low != null && high[index] != null ? [low, high[index]!] : null),
      targets: target && label ? [{ value: scale(target.value)!, label: fill(label, { t: targetFigure(key, target.value, lang) }) }] : [] }],
    readouts, foot: text.src, legend: null, nights: [], sparse: buckets.length <= 13,
  }
}

function trendOf(metric: BuildingMetric, mode: 'loading' | 'error' | 'ready', days: WindowDays, lang: DeliveryLanguage): { simple: Trend; expert: Delta } {
  const words = simpleText(lang), page = deliveryText(lang), text = buildingText(lang), change = metric.change
  if (mode !== 'ready' || metric.current.value == null) return { simple: { cls: 'none', icon: 'minus', text: words.tNoData }, expert: { cls: 'none', icon: 'minus', text: words.tNoData, word: '' } }
  if (change.trend === 'none' || change.value == null) {
    const none = fill(words.tNone, { w: days })
    return { simple: { cls: 'none', icon: 'minus', text: none }, expert: { cls: 'none', icon: 'minus', text: none, word: '' } }
  }
  const magnitude = Math.abs(change.value)
  const amount = metric.key === 'first_try' ? `${num(magnitude, lang)}${NB}${text.pts}` : metric.key === 'rounds' ? num(magnitude, lang, 1) : hm(magnitude, lang)
  const sign = change.value > 0 ? '+' : change.value < 0 ? '−' : '±'
  const icon = change.value < 0 ? 'arrow-down' : change.value > 0 ? 'arrow-up' : 'arrow'
  const cls = change.trend
  const sentence = { better: words.tBetter, worse: words.tWorse, same: words.tSame }[cls]
  return {
    simple: { cls, icon, text: `${fill(sentence, { w: days })} (${sign}${amount})` },
    expert: { cls, icon, text: `${sign}${amount}`, word: page[cls] },
  }
}

function verdictOf(metric: BuildingMetric, target: BuildingMetric['target'], mode: 'loading' | 'error' | 'ready', lang: DeliveryLanguage): Verdict {
  const words = simpleText(lang), text = buildingText(lang), value = metric.current.value
  if (mode === 'error') return { level: 'none', word: words.notLoaded, gap: '', ratio: null }
  if (value == null) return { level: 'none', word: words.nodataV, gap: '', ratio: null }
  if (!target) return { level: 'none', word: text.noTarget, gap: text.watched, sep: ': ', ratio: null }
  const level = metric.verdict.level, ratio = metric.verdict.ratio
  let gap = ''
  if (level !== 'on' && level !== 'none') {
    if (metric.key === 'first_try') gap = `${pct(value, lang)} vs. ${pct(target.value, lang)} ${words.wanted}`
    else if (ratio != null) gap = ratioGap(ratio, words, lang)
  }
  return { level, word: words[level === 'none' ? 'none' : level], gap, ratio }
}

/** The view of one answer. `data` null (loading or failed) keeps every slot with empty values. */
export function buildingView(data: DeliveryBuilding | null, days: WindowDays, mode: 'loading' | 'error' | 'ready', lang: DeliveryLanguage): BuildingView {
  const text = buildingText(lang), words = simpleText(lang), page = deliveryText(lang)
  const ready = mode === 'ready' && !!data
  const byKey = new Map((ready ? data!.metrics : []).map(metric => [metric.key, metric] as const))
  const tiles = BUILDING_KEYS.map(key => {
    const metric = byKey.get(key) ?? emptyMetric(key), target = metric.target, w = text.metrics[key], c = metric.current
    const has = ready && c.value != null
    const t = target ? targetFigure(key, target.value, lang) : ''
    const values = {
      k: num(c.count, lang), n: num(c.n, lang), m: num(c.n - c.count, lang), c: num(c.count, lang), i: num(c.items, lang), t,
      p50: c.p50 == null ? '–' : figure(key, c.p50, lang), p90: c.p90 == null ? '–' : figure(key, c.p90, lang), mean: c.mean == null ? '–' : num(c.mean, lang, 1),
      v: c.value == null ? '–' : key === 'rounds' ? num(Math.ceil(c.value), lang) : key === 'first_try' ? pct(c.value, lang) : hm(c.value, lang),
    }
    // As designed: this window's sentence once there are figures, else what is wanted. Short enough for the two
    // lines a phone reserves, so no state moves the tiles below (AEON-541).
    const wants = fill(w.wants, values)
    const sentence = key === 'rounds' && c.value != null && c.value <= 1 ? w.sayOne! : key === 'first_try' && one(c.n) ? w.sayOne! : w.say
    const say = has ? fill(sentence, values) : wants
    const trend = trendOf(metric, mode, days, lang)
    const raw = metric.buckets
    const buckets = bucketsOf(raw, days, lang)
    const spark: SparkModel = {
      kind: 'line', percent: key === 'first_try', mode: ready ? 'ready' : mode, unit: bucketOf(days),
      statuses: raw.map(bucket => bucket.state === 'observed' && bucket.n === 0 ? 'empty' : SPARK_STATUS[bucket.state]),
      values: raw.map(bucket => ready && bucket.n > 0 ? (key === 'first_try' ? bucket.value : bucket.p50 == null ? null : key === 'lead' ? bucket.p50 / 60 : bucket.p50) : null),
      target: target ? { value: key === 'lead' ? target.value / 60 : target.value, up: target.direction === 'min', label: fill(text.sparkTarget, { t }) } : null,
      nights: [],
    }
    const value = has ? key === 'first_try' ? [{ text: num(c.value!, lang), unit: '%' }] : key === 'rounds' ? [{ text: num(c.value!, lang, c.value! % 1 ? 1 : 0), unit: w.unit }] : minuteParts(c.value!, lang) : null
    const simple: SimpleTileModel = {
      key: `building-${key}`, name: w.name, up: metric.better === 'higher', hasTarget: !!target, value,
      empty: mode === 'error' ? words.notLoaded : page.noData,
      verdict: verdictOf(metric, target, mode, lang), trend: trend.simple, say, partial: false, foot: text.foot, spark,
      learn: { body: fill(w.learn, values), expert: `${w.name} · ${text.src}`, target: target ? fill(w.target, values) : w.target,
        facts: [`${words.expertName}: ${w.name} · ${text.src}`, target ? fill(w.target, values) : w.target] },
    }
    const lineA = has ? runs(w.lineA, values) : [{ text: 'p50 · p90 –' }]
    const expert: TicketTileModel = {
      def: { key: `building-${key}` }, label: w.name, definition: fill(w.learn, values), reason: null,
      target: target ? fill(w.target, values) : w.target, source: text.src,
      status: ready && c.n > 0 ? 'ok' : 'no_data', partial: false, foot: text.src,
      // A duration in hours and minutes keeps both units small, as the Simple tile shows it.
      value: has ? { main: value![0].text, unit: value![0].unit, ...(value![1] ? { more: { main: value![1].text, unit: value![1].unit } } : {}) } : null,
      lineA: { parts: lineA, mono: key !== 'first_try' }, lineB: ready ? fill(one(c.items) && w.lineBOne ? w.lineBOne : w.lineB, values) : '',
      delta: trend.expert,
    }
    const worst = (ready ? metric.worst : []).map(row => {
      const shown = key === 'first_try' || key === 'rounds' ? (row.value === 1 && w.worstOne ? w.worstOne : fill(w.worst, { v: num(row.value, lang) })) : fill(w.worst, { v: hm(row.value, lang) })
      const marks = row.rounds.map(outcome => text.kinds[outcome]).join(', ')
      return { nodeId: row.node_id, key: row.key, title: row.title, value: shown, rounds: row.rounds, more: row.more_rounds ? fill(text.moreRounds, { n: row.more_rounds }) : '',
        said: `${row.key} ${row.title}, ${shown}. ${text.roundsWord}: ${marks}${row.more_rounds ? `, ${fill(text.moreRounds, { n: row.more_rounds })}` : ''}` }
    })
    return {
      key, simple, expert, chart: chartOf(metric, target, buckets, days, lang),
      pp: has ? runs(w.pp, values) : [{ text: ready ? 'p50 · p90 –' : ' ' }],
      worst: { rows: worst, title: text.wpTitle, sub: fill(text.wpSub, { n: w.name, w: days }) },
    }
  })
  const withTarget = tiles.filter(tile => tile.simple.hasTarget)
  let head: BuildingView['head']
  if (mode === 'loading') head = { kind: 'loading', text: ' ' }
  else if (mode === 'error' || !data) head = { kind: 'error', text: text.err }
  else if (!data.coverage.items) head = { kind: 'nodata', text: text.noData }
  else head = { kind: 'ready', text: fill(text.onTarget, { k: withTarget.filter(tile => tile.simple.verdict.level === 'on').length, t: withTarget.length }) }
  return { head, tiles, summary: head.kind === 'ready' ? tiles.map(tile => tile.simple) : [] }
}

