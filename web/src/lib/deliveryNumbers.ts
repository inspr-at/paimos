// SPDX-License-Identifier: AGPL-3.0-only
// The Delivery page's numbers (AEON-994): the AEON-993/1001 metrics contract,
// the per-person page preferences, and everything the tiles and charts show,
// worked out here so it is tested without a browser. Nothing is extrapolated:
// a bucket without samples is "no data yet" before the source covers it and
// "nothing recorded" after; a missing window before is "no comparison", never 0.
import { api, APIError } from './api.ts'
import type { DeliveryLanguage } from './delivery'
import { deliveryText, fill, type DeliveryText, type MetricTextKey } from './deliveryNumbersText'

// ---------- Contract (api/openapi.yaml DeliveryMetrics) ----------
export type MetricKey = MetricTextKey | 'flaked_failures' | 'review_changes_share'
export type SampleStatus = 'ok' | 'partial' | 'no_data'
export type BucketUnit = 'day' | 'week' | 'month'
export interface MetricCoverage { window_days: number; bucket: BucketUnit; buckets_total: number; buckets_covered: number; covered_days: number; from: string | null; full: boolean }
interface Measured { status: SampleStatus; n: number; value: number | null; p50: number | null; p90: number | null }
export interface MetricPrevious extends Measured { coverage: MetricCoverage }
export interface MetricWindow extends Measured { days: number; coverage: MetricCoverage; previous: MetricPrevious | null }
export interface MetricPoint extends Measured { date: string }
export interface MetricBucket extends Measured { days: number; from: string; to: string; bucket: 'week' | 'month' }
export interface MetricTarget { value: number; direction: 'max' | 'min'; note: string; source: string }
export interface MetricRelease { key: string; release: string | null; tag: string | null; outcome: 'live' | 'failed'; queued_at: string; live_at: string | null; healthy_at: string | null }
export interface Metric {
  number: number; key: MetricKey; label: string; unit: 'minutes' | 'percent' | 'runs'; source: string; definition: string
  status: SampleStatus; reason: string | null; latest: { value: number; at: string } | null
  windows: MetricWindow[]; daily: MetricPoint[]; series: MetricBucket[]; target: MetricTarget | null; releases?: MetricRelease[]
}
export interface MetricSource {
  repository: string; ci_workflow: string; nightly_workflow: string; app_connected: boolean
  backfill: 'not_started' | 'running' | 'done'; backfill_since: string | null; backfill_done_at: string | null; covered_since: string | null
}
export interface DeliveryMetrics { project_id: string; generated_at: string; source: MetricSource | null; metrics: Metric[] }

export async function readDeliveryMetrics(projectId: string, signal?: AbortSignal): Promise<DeliveryMetrics> {
  const response = await api(`/projects/${encodeURIComponent(projectId)}/delivery/metrics`, { signal })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' && data.error ? data.error : `Request failed (${response.status})`)
  }
  const body = await response.json()
  if (!body || !Array.isArray(body.metrics)) throw new APIError(response.status, 'Invalid delivery metrics response')
  return body as DeliveryMetrics
}

// ---------- Per-person page preferences (GET/PUT /api/preferences/delivery:numbers) ----------
export const WINDOWS = [7, 30, 90, 180, 365] as const
export type WindowDays = typeof WINDOWS[number]
export type Level = 'simple' | 'expert'
export interface DeliveryPrefs { level: Level; window: WindowDays }
export const DELIVERY_PREFS_KEY = 'delivery:numbers'
export const DEFAULT_PREFS: DeliveryPrefs = { level: 'simple', window: 7 }
export function readPrefs(value: unknown): DeliveryPrefs {
  const saved = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  return {
    level: saved.level === 'expert' || saved.level === 'simple' ? saved.level : DEFAULT_PREFS.level,
    window: WINDOWS.includes(saved.window as WindowDays) ? saved.window as WindowDays : DEFAULT_PREFS.window,
  }
}
export const bucketOf = (days: WindowDays): BucketUnit => days <= 30 ? 'day' : days <= 180 ? 'week' : 'month'

// ---------- Numbers in words ----------
const NB = ' '
const locale = (lang: DeliveryLanguage) => lang === 'de' ? 'de-AT' : 'en-GB'
export function num(value: number, lang: DeliveryLanguage, digits = 0): string {
  return value.toLocaleString(locale(lang), { minimumFractionDigits: digits, maximumFractionDigits: digits })
}
export const pct = (value: number, lang: DeliveryLanguage) => lang === 'de' ? `${num(value, lang)}${NB}%` : `${num(value, lang)}%`
/** Minutes as [number, unit]: hours from three hours on, one decimal below ten minutes. */
export function durationParts(minutes: number, lang: DeliveryLanguage): [string, string] {
  if (minutes >= 180) return [num(minutes / 60, lang, 1), 'h']
  return [num(minutes, lang, minutes < 10 && minutes % 1 ? 1 : 0), 'min']
}
export const duration = (minutes: number, lang: DeliveryLanguage) => durationParts(minutes, lang).join(NB)
const dateOf = (iso: string) => new Date(`${iso.slice(0, 10)}T00:00:00Z`)
const fmtDate = (iso: string, lang: DeliveryLanguage, options: Intl.DateTimeFormatOptions) => dateOf(iso).toLocaleDateString(locale(lang), { timeZone: 'UTC', ...options })
export const shortDate = (iso: string, lang: DeliveryLanguage) => fmtDate(iso, lang, { day: 'numeric', month: 'short' })
export const clockTime = (iso: string, lang: DeliveryLanguage) => new Date(iso).toLocaleTimeString(locale(lang), { hour: '2-digit', minute: '2-digit', hour12: false })

// ---------- The ten tiles (numbers 3 and 7 carry two metrics) ----------
export type TileKind = 'duration' | 'runs' | 'share' | 'review' | 'merge' | 'release' | 'nightly'
export type SourceKey = 'gh' | 'review' | 'merge' | 'release' | 'nightly'
export interface TileDef { key: MetricTextKey; also?: MetricKey; kind: TileKind; src: SourceKey }
export const TILES: readonly TileDef[] = [
  { key: 'pr_ci_wall', kind: 'duration', src: 'gh' },
  { key: 'queue_run_wall', kind: 'duration', src: 'gh' },
  { key: 'first_attempt_green', also: 'flaked_failures', kind: 'share', src: 'gh' },
  { key: 'time_to_first_green', kind: 'duration', src: 'gh' },
  { key: 'pr_open_to_merged', kind: 'duration', src: 'gh' },
  { key: 'queue_runs_per_pr', kind: 'runs', src: 'gh' },
  { key: 'review_time', also: 'review_changes_share', kind: 'review', src: 'review' },
  { key: 'merge_rounds_model_share', kind: 'merge', src: 'merge' },
  { key: 'release_queue_to_live', kind: 'release', src: 'release' },
  { key: 'nightly_green', kind: 'nightly', src: 'nightly' },
]
export const windowOf = (metric: Metric | undefined, days: number) => metric?.windows.find(window => window.days === days) ?? null

export function sourceLabel(def: TileDef, source: MetricSource | null, text: DeliveryText): string {
  if (def.src !== 'nightly') return text.sources[def.src]
  const workflow = source?.nightly_workflow.split('/').pop()?.replace(/\.ya?ml$/, '') || 'nightly-full'
  return fill(text.sources.nightly, { wf: workflow })
}
export function coverText(coverage: MetricCoverage | null | undefined, text: DeliveryText): string {
  if (!coverage || coverage.full) return ''
  return fill(text.covers, { k: coverage.buckets_covered, n: coverage.buckets_total, u: text.units[coverage.bucket] })
}

export interface Delta { cls: 'better' | 'worse' | 'same' | 'none'; icon: 'arrow-up' | 'arrow-down' | 'arrow' | 'minus'; text: string; word: string }
/** The change against the window just before: steady within 10 %, or 3 points for shares. */
export function deltaOf(def: TileDef, window: MetricWindow | null, direction: 'max' | 'min', ctx: { days: number; text: DeliveryText; lang: DeliveryLanguage; historyLoading: boolean }): Delta {
  const { text, lang, days } = ctx
  const before = window?.previous ?? null
  if (!window || window.value == null || !before || before.value == null) {
    return { cls: 'none', icon: 'minus', text: ctx.historyLoading && !before?.coverage.full ? text.tLoading : fill(text.tNone, { w: days }), word: '' }
  }
  const share = def.kind === 'share' || def.kind === 'merge' || def.kind === 'nightly'
  const change = window.value - before.value
  const steady = share ? Math.abs(change) < 3 : before.value === 0 ? change === 0 : Math.abs(change) / Math.abs(before.value) < 0.1
  const good = direction === 'max' ? change < 0 : change > 0
  const cls = steady ? 'same' : good ? 'better' : 'worse'
  const magnitude = Math.abs(change)
  const amount = share ? `${num(magnitude, lang)}${NB}${text.pts}` : def.kind === 'runs' ? num(magnitude, lang, 1) : duration(magnitude, lang)
  const sign = change > 0 ? '+' : change < 0 ? '−' : '±'
  return { cls, icon: change < 0 ? 'arrow-down' : change > 0 ? 'arrow-up' : 'arrow', text: `${sign}${amount}`, word: text[cls] }
}

export interface Part { text: string; strong?: boolean }
export interface TileModel {
  def: TileDef; label: string; definition: string; reason: string | null; target: string; source: string
  status: SampleStatus; value: { main: string; unit: string } | null
  lineA: { parts: Part[]; mono: boolean; squares?: ('ok' | 'bad' | 'none')[] }; lineB: string
  delta: Delta; partial: boolean; foot: string
}
const plural = (n: number, one: string, many: string) => n === 1 ? one : many

/** Nights of the daily series, oldest first: green, red, or no run (covered, nothing ran) / no data yet. */
export function nightOf(point: Measured & { date: string }, coveredFrom: string | null): 'ok' | 'bad' | 'none' | 'nodata' {
  if (point.n > 0 && point.value != null) return point.value >= 100 ? 'ok' : 'bad'
  return coveredFrom && point.date >= coveredFrom ? 'none' : 'nodata'
}
/** Same verdict on consecutive nights back from the latest one; tonight may not have run yet, any other night without a run ends it. */
export function nightStreak(nights: ('ok' | 'bad' | 'none' | 'nodata')[]): { verdict: 'ok' | 'bad'; count: number } | null {
  let index = nights.length - 1
  if (index >= 0 && nights[index] !== 'ok' && nights[index] !== 'bad') index--
  if (index < 0 || (nights[index] !== 'ok' && nights[index] !== 'bad')) return null
  const verdict = nights[index] as 'ok' | 'bad'
  let count = 0
  while (index >= 0 && nights[index] === verdict) { count++; index-- }
  return { verdict, count }
}

export function tileModel(def: TileDef, metrics: Map<MetricKey, Metric>, days: WindowDays, source: MetricSource | null, lang: DeliveryLanguage): TileModel {
  const text = deliveryText(lang), words = text.metrics[def.key], metric = metrics.get(def.key)
  const window = windowOf(metric, days), also = def.also ? windowOf(metrics.get(def.also), days) : null
  const status: SampleStatus = window?.status ?? 'no_data'
  const sourceText = sourceLabel(def, source, text)
  const cover = coverText(window?.coverage, text)
  const partial = status === 'partial' || !!cover
  const base = {
    def, label: words.label, definition: words.definition, reason: partial ? metric?.reason ?? null : null, target: words.target, source: sourceText,
    status, partial, foot: cover || sourceText,
    delta: deltaOf(def, window, metric?.target?.direction ?? 'max', { days, text, lang, historyLoading: source?.backfill === 'running' }),
  }
  const empty = { ...base, value: null, lineA: { parts: [{ text: 'p50 · p90 –' }], mono: true }, lineB: '' }
  if (!window || window.n === 0 || window.value == null) return def.kind === 'nightly' ? nightlyTile(base, metric, window, days, text, lang, true) : empty
  const n = window.n, count = `${num(n, lang)} ${words.count}`
  switch (def.kind) {
    case 'duration': case 'review': {
      const [main, unit] = durationParts(window.value, lang)
      const pp: Part[] = [{ text: 'p50 ' }, { text: duration(window.p50 ?? window.value, lang), strong: true }, { text: ' · p90 ' }, { text: window.p90 == null ? '–' : duration(window.p90, lang), strong: true }]
      let lineB = count
      if (def.kind === 'review' && also && also.value != null && also.n > 0) {
        const asked = Math.round(also.n * also.value / 100)
        lineB = `${text.changesVerdicts} ${asked} ${text.of} ${also.n} (${pct(also.value, lang)})`
      }
      return { ...base, value: { main, unit }, lineA: { parts: pp, mono: true }, lineB }
    }
    case 'runs':
      return { ...base, value: { main: num(window.value, lang, 1), unit: lang === 'de' ? 'Läufe' : 'runs' },
        lineA: { parts: [{ text: `${text.mean} ` }, { text: num(window.value, lang, 1), strong: true }, { text: ' · p90 ' }, { text: window.p90 == null ? '–' : num(window.p90, lang, window.p90 % 1 ? 1 : 0), strong: true }], mono: true }, lineB: count }
    case 'share':
      return { ...base, value: { main: num(window.value, lang), unit: '%' },
        lineA: { parts: also?.value != null ? [{ text: `${text.flakesOnly} ` }, { text: pct(also.value, lang), strong: true }] : [{ text: `${text.flakesOnly} –` }], mono: false }, lineB: count }
    case 'merge': {
      const byModel = Math.round(n * window.value / 100)
      return { ...base, value: { main: `${num(n - byModel, lang)} ${text.of} ${num(n, lang)}`, unit: text.scripted },
        lineA: { parts: [{ text: `${num(byModel, lang)} ${text.solvedByModel} (${pct(window.value, lang)})` }], mono: false }, lineB: `${count} ${text.reported}` }
    }
    case 'release': {
      const [main, unit] = durationParts(window.value, lang)
      const shown = newestRelease(metric?.releases ?? [], days, metric?.daily ?? [])
      return { ...base, value: { main, unit },
        lineA: { parts: [{ text: 'p50 ' }, { text: duration(window.value, lang), strong: true }, { text: ` · ${num(n, lang)} ${plural(n, text.releaseOne, text.releaseMany)}` }], mono: true },
        lineB: shown ? `${text.release} ${shown.release ?? shown.tag ?? shown.key} · ${shortDate((shown.live_at ?? shown.queued_at).slice(0, 10), lang)}` : '' }
    }
    case 'nightly': return nightlyTile(base, metric, window, days, text, lang, false)
  }
}
type TileBase = Omit<TileModel, 'value' | 'lineA' | 'lineB'>
function nightlyTile(base: TileBase, metric: Metric | undefined, window: MetricWindow | null, days: WindowDays, text: DeliveryText, lang: DeliveryLanguage, empty: boolean): TileModel {
  const from = windowOf(metric, 30)?.coverage.from ?? window?.coverage.from ?? null
  const nights = (metric?.daily ?? []).map(point => nightOf(point, from))
  const last7 = nights.slice(-7).map(night => night === 'nodata' ? 'none' : night)
  const streak = nightStreak(nights)
  const ran = window?.n ?? 0
  const without = Math.max(0, (window?.coverage.covered_days ?? 0) - ran)
  const squares = { squares: last7.length ? last7 : undefined }
  if (empty || !window || window.value == null) return { ...base, value: null, lineA: { parts: [{ text: 'p50 · p90 –' }], mono: true }, lineB: '' }
  const green = Math.round(ran * window.value / 100)
  return {
    ...base, value: { main: `${num(green, lang)} ${text.of} ${num(ran, lang)}`, unit: ran === 1 ? text.nightGreen : text.nightsGreen },
    lineA: { parts: streak && days >= 7 ? [{ text: fill(streak.verdict === 'ok' ? text.greenRow : text.redRow, { n: streak.count }) }] : [], mono: false, ...squares },
    lineB: without ? (without === 1 ? text.withoutRunOne : fill(text.withoutRun, { n: without })) : '',
  }
}
/** The newest reported release that went live inside the window. */
export function newestRelease(releases: MetricRelease[], days: number, daily: MetricPoint[]): MetricRelease | null {
  const today = daily.length ? daily[daily.length - 1].date : new Date().toISOString().slice(0, 10)
  const start = new Date(dateOf(today).getTime() - (days - 1) * 86_400_000).toISOString().slice(0, 10)
  return releases.find(release => release.outcome === 'live' && release.live_at && release.live_at.slice(0, 10) >= start && release.live_at.slice(0, 10) <= today) ?? null
}

// ---------- Chart buckets ----------
export type BucketStatus = 'ok' | 'partial' | 'no_data' | 'empty'
export interface ChartBucket { from: string; to: string; unit: BucketUnit; status: BucketStatus; label: string; long: string; show: boolean }
/** Buckets of one window: days from daily (7, 30), weeks (90, 180) or months (365) from series. A bucket before the
 * source covers it is "no data yet" (no_data); after that, a bucket without samples is "empty", never zero. */
export function bucketsOf(metric: Metric | undefined, days: WindowDays, lang: DeliveryLanguage): { buckets: ChartBucket[]; samples: Measured[] } {
  const text = deliveryText(lang), unit = bucketOf(days)
  const coverage = windowOf(metric, days)?.coverage ?? null
  const coveredFrom = coverage?.from ?? null
  const status = (sample: Measured, last: string): BucketStatus => sample.status !== 'no_data' ? sample.status : coveredFrom && last >= coveredFrom ? 'empty' : 'no_data'
  if (unit === 'day') {
    const points = (metric?.daily ?? []).slice(-days)
    return {
      samples: points,
      buckets: points.map((point, index) => {
        const last = index === points.length - 1, back = points.length - 1 - index
        const label = last ? text.today : days === 7 ? `${fmtDate(point.date, lang, { weekday: 'short' })} ${dateOf(point.date).getUTCDate()}` : shortDate(point.date, lang)
        return { from: point.date, to: point.date, unit, status: status(point, point.date), label, long: last ? text.today : shortDate(point.date, lang), show: days === 7 || back % 7 === 0 }
      }),
    }
  }
  const series = (metric?.series ?? []).filter(bucket => bucket.days === days)
  return {
    samples: series,
    buckets: series.map((bucket, index) => {
      const last = index === series.length - 1, back = series.length - 1 - index
      if (unit === 'week') {
        return { from: bucket.from, to: bucket.to, unit, status: status(bucket, bucket.to),
          label: last ? text.thisWk : shortDate(bucket.from, lang), long: last ? text.thisWeek : `${text.weekOf} ${shortDate(bucket.from, lang)}`,
          show: back % (days === 90 ? 4 : 8) === 0 }
      }
      return { from: bucket.from, to: bucket.to, unit, status: status(bucket, bucket.to),
        label: fmtDate(bucket.from, lang, { month: 'short' }),
        long: last ? `${fmtDate(bucket.from, lang, { month: 'long' })} (${text.soFar})` : fmtDate(bucket.from, lang, { month: 'long', year: 'numeric' }),
        show: back % 3 === 0 }
    }),
  }
}

// ---------- Chart models ----------
export function niceCeil(value: number): number {
  if (!(value > 0)) return 1
  const power = Math.pow(10, Math.floor(Math.log10(value)))
  for (const step of [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (step * power >= value - 1e-9) return step * power
  return 10 * power
}
/** Duration scale: room above the p50 line and the target; p90 only stretches it up to twice that, else it is clipped. */
export function durationTop(p50: number[], p90: number[], target: number | null): { top: number; clippedTo: number | null } {
  const reference = target ?? 0
  const highest = p50.length ? Math.max(...p50) : Math.max(reference * 2, 1)
  let top = Math.max(highest * 1.5, reference * 1.6)
  const tail = p90.length ? Math.max(...p90) : 0
  if (tail <= top * 2) top = Math.max(top, tail)
  const nice = niceCeil(top * 1.04)
  return { top: nice, clippedTo: tail > nice ? tail : null }
}
export interface ChartLine { values: (number | null)[]; tone: 'teal' | 'gold'; connect: boolean }
export interface ChartPanel {
  top: number; share: number; caption: string; lines: ChartLine[]; band: ([number, number] | null)[] | null; fade: boolean
  targets: { value: number; label: string }[]
}
export interface ChartModel {
  key: MetricTextKey; kind: TileKind; label: string; unit: string; buckets: ChartBucket[]; panels: ChartPanel[]
  readouts: string[]; foot: string; legend: 'two' | 'nightly' | null; nights: ('ok' | 'bad' | 'none' | 'nodata')[]
}
const values = (samples: Measured[], pick: (sample: Measured) => number | null) => samples.map(sample => sample.n > 0 ? pick(sample) : null)
const present = (list: (number | null)[]) => list.filter((value): value is number => value != null)

export function chartModel(def: TileDef, metrics: Map<MetricKey, Metric>, days: WindowDays, source: MetricSource | null, lang: DeliveryLanguage): ChartModel {
  const text = deliveryText(lang), words = text.metrics[def.key], metric = metrics.get(def.key)
  const { buckets, samples } = bucketsOf(metric, days, lang)
  const alsoSamples = def.also ? bucketsOf(metrics.get(def.also), days, lang).samples : []
  const target = metric?.target?.value ?? null
  const panels: ChartPanel[] = []
  const unit = def.kind === 'share' || def.kind === 'merge' ? '%' : def.kind === 'runs' ? (lang === 'de' ? 'Läufe' : 'runs') : def.kind === 'nightly' ? '' : 'min'
  let clippedTo: number | null = null
  if (def.kind === 'duration' || def.kind === 'review' || def.kind === 'release') {
    const p50 = values(samples, sample => sample.p50 ?? sample.value), p90 = def.kind === 'release' ? [] : values(samples, sample => sample.p90)
    const scale = durationTop(present(p50), present(p90), target)
    clippedTo = scale.clippedTo
    panels.push({ top: scale.top, share: def.kind === 'review' ? 0.58 : 1, caption: '', lines: [{ values: p50, tone: 'teal', connect: def.kind !== 'release' }],
      band: def.kind === 'release' ? null : p50.map((low, index) => low != null && p90[index] != null ? [low, p90[index]!] : null), fade: clippedTo != null,
      targets: target != null ? [{ value: target, label: words.line }] : [] })
    if (def.kind === 'review') {
      const changes = metrics.get('review_changes_share')?.target?.value ?? null
      panels.push({ top: 100, share: 0.42, caption: text.changesShare, lines: [{ values: values(alsoSamples, sample => sample.value), tone: 'teal', connect: true }], band: null, fade: false,
        targets: changes != null ? [{ value: changes, label: fill(text.targetChanges, { v: pct(changes, lang) }) }] : [] })
    }
  } else if (def.kind === 'runs') {
    const mean = values(samples, sample => sample.value), p50 = values(samples, sample => sample.p50), p90 = values(samples, sample => sample.p90)
    panels.push({ top: Math.max(4, niceCeil(Math.max(4, ...present(p90)))), share: 1, caption: '', lines: [{ values: mean, tone: 'teal', connect: true }],
      band: p50.map((low, index) => low != null && p90[index] != null ? [low, p90[index]!] : null), fade: false, targets: target != null ? [{ value: target, label: words.line }] : [] })
  } else if (def.kind === 'share') {
    const flake = metrics.get('flaked_failures')?.target?.value ?? null
    panels.push({ top: 100, share: 1, caption: '', band: null, fade: false,
      lines: [{ values: values(samples, sample => sample.value), tone: 'teal', connect: true }, { values: values(alsoSamples, sample => sample.value), tone: 'gold', connect: true }],
      targets: [...(target != null ? [{ value: target, label: fill(text.targetGreen, { v: pct(target, lang) }) }] : []), ...(flake != null ? [{ value: flake, label: fill(text.targetFlake, { v: pct(flake, lang) }) }] : [])] })
  } else if (def.kind === 'merge') {
    panels.push({ top: 100, share: 1, caption: '', band: null, fade: false, lines: [{ values: values(samples, sample => sample.value), tone: 'teal', connect: true }],
      targets: target != null ? [{ value: target, label: words.line }] : [] })
  }
  const coveredFrom = windowOf(metric, days)?.coverage.from ?? null
  const nights = def.kind === 'nightly' ? samples.map((sample, index) => {
    if (sample.n > 0 && sample.value != null) return sample.value >= 100 ? 'ok' : 'bad'
    return coveredFrom && buckets[index].to >= coveredFrom ? 'none' : 'nodata'
  }) as ChartModel['nights'] : []
  const readouts = buckets.map((bucket, index) => readout(def, bucket, samples[index], alsoSamples[index], nights[index], text, lang))
  const notes = [sourceLabel(def, source, text)]
  if (clippedTo != null) notes.push(`${text.clip}, ${text.upTo} ${duration(clippedTo, lang)}`)
  const cover = coverText(windowOf(metric, days)?.coverage, text)
  if (cover) notes.push(cover)
  return { key: def.key, kind: def.kind, label: words.label, unit, buckets, panels, readouts, foot: notes.join(' · '),
    legend: def.kind === 'share' ? 'two' : def.kind === 'nightly' ? 'nightly' : null, nights }
}

export function readout(def: TileDef, bucket: ChartBucket, sample: Measured | undefined, also: Measured | undefined, night: ChartModel['nights'][number] | undefined, text: DeliveryText, lang: DeliveryLanguage): string {
  const head = bucket.long
  if (def.kind === 'nightly' && bucket.unit === 'day') {
    return `${head} · ${night === 'ok' ? text.green : night === 'bad' ? `${text.red} (${text.failed})` : night === 'none' ? text.noRun : text.noData}`
  }
  if (!sample || bucket.status === 'no_data') return `${head} · ${text.noData}`
  if (bucket.status === 'empty' || sample.n === 0) return `${head} · ${def.kind === 'nightly' ? text.noRun : text.nothing}`
  const partial = bucket.status === 'partial' ? ` · ${text.partial}` : ''
  const words = text.metrics[def.key]
  const count = `${num(sample.n, lang)} ${plural(sample.n, words.one, words.many)}`
  const pair = (low: number | null, high: number | null) => `p50 ${low == null ? '–' : duration(low, lang)} · p90 ${high == null ? '–' : duration(high, lang)}`
  switch (def.kind) {
    case 'duration': return `${head}${partial} · ${pair(sample.p50 ?? sample.value, sample.p90)} · ${count}`
    case 'review': return `${head}${partial} · ${pair(sample.p50 ?? sample.value, sample.p90)}${also?.value != null && also.n > 0 ? ` · ${text.changes} ${pct(also.value, lang)}` : ''} · ${count}`
    case 'runs': return `${head}${partial} · ${text.mean} ${sample.value == null ? '–' : num(sample.value, lang, 1)} · p90 ${sample.p90 == null ? '–' : num(sample.p90, lang, sample.p90 % 1 ? 1 : 0)} · ${count}`
    case 'share': return `${head}${partial} · ${sample.value == null ? '–' : pct(sample.value, lang)} ${text.greenWord} · ${also?.value == null ? '–' : pct(also.value, lang)} ${text.flake} · ${count}`
    case 'merge': return `${head}${partial} · ${sample.value == null ? '–' : pct(sample.value, lang)} ${text.byModel} · ${Math.round(sample.n * (sample.value ?? 0) / 100)} ${text.of} ${count}`
    case 'release': return `${head}${partial} · ${sample.value == null ? '–' : duration(sample.p50 ?? sample.value, lang)} · ${count}`
    case 'nightly': {
      const green = Math.round(sample.n * (sample.value ?? 0) / 100)
      return `${head}${partial} · ${green} ${text.of} ${sample.n} ${sample.n === 1 ? text.nightGreen : text.nightsGreen}`
    }
  }
}

/** Contiguous runs of partial and no-data buckets, for the tinted and hatched regions. */
export function regions(buckets: ChartBucket[]): { start: number; end: number; status: 'partial' | 'no_data' }[] {
  const out: { start: number; end: number; status: 'partial' | 'no_data' }[] = []
  buckets.forEach((bucket, index) => {
    const status = bucket.status === 'partial' || bucket.status === 'no_data' ? bucket.status : null
    const last = out[out.length - 1]
    if (status && last && last.status === status && last.end === index) last.end = index + 1
    else if (status) out.push({ start: index, end: index + 1, status })
  })
  return out
}

/** All tiles and charts of one window, in Project Arion order. */
export function numbersOf(data: DeliveryMetrics, days: WindowDays, lang: DeliveryLanguage) {
  const metrics = new Map(data.metrics.map(metric => [metric.key, metric] as const))
  return {
    tiles: TILES.map(def => tileModel(def, metrics, days, data.source, lang)),
    charts: TILES.map(def => chartModel(def, metrics, days, data.source, lang)),
  }
}
/** No metric has a sample in any window: the page says so instead of drawing ten empty tiles as if measured. */
export const hasAnyData = (data: DeliveryMetrics) => data.metrics.some(metric => metric.windows.some(window => window.n > 0))
