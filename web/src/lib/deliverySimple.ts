// SPDX-License-Identifier: AGPL-3.0-only
// Numbers · Simple (AEON-994 draft 5, package 3): the ten numbers in plain words,
// grouped along the path, each with a verdict against its Arion target (shape +
// word, never colour alone), a small chart with the target zone, and one summary
// on top. Worked out here so it is tested without a browser; nothing is
// extrapolated and a missing number is "no data yet", never 0.
import type { DeliveryLanguage } from './delivery'
import {
  bucketOf, bucketsOf, chartModel, coverText, deltaOf, duration, durationParts, jobFactsMissing, newestRelease, num, pct, shortDate, sourceLabel, TILES, windowOf,
  type BucketStatus, type BucketUnit, type DeliveryMetrics, type Metric, type MetricKey, type MetricSource, type MetricWindow, type TileDef, type TileKind, type WindowDays,
} from './deliveryNumbers'
import { deliveryText, fill, type MetricTextKey } from './deliveryNumbersText'
import { simpleText, type SimpleText } from './deliverySimpleText'

const NB = ' '
export type Mode = 'loading' | 'error' | 'ready'
export type VerdictLevel = 'on' | 'close' | 'far' | 'none'
export interface Verdict { level: VerdictLevel; word: string; gap: string; ratio: number | null }
export interface Trend { cls: 'better' | 'worse' | 'same' | 'none'; icon: 'arrow-up' | 'arrow-down' | 'arrow' | 'minus'; text: string }
export type Night = 'ok' | 'bad' | 'none' | 'nodata'
export interface SparkModel {
  kind: 'line' | 'nightly'; percent: boolean; values: (number | null)[]; statuses: BucketStatus[]
  target: { value: number; up: boolean; label: string } | null; nights: Night[]; unit: BucketUnit; mode: Mode
}
export interface SimpleTileModel {
  key: MetricTextKey; name: string; up: boolean; hasTarget: boolean; value: { text: string; unit: string }[] | null; empty: string
  verdict: Verdict; trend: Trend; say: string; partial: boolean; foot: string; spark: SparkModel
  learn: { body: string; expert: string; target: string }
}
export interface SimpleSection { title: string; sub: string; count: string; tiles: SimpleTileModel[] }
export type SimpleSummary =
  | { kind: 'loading' } | { kind: 'error' } | { kind: 'nodata'; title: string; body: string }
  | { kind: 'ready'; big: string; gaps: { label: string; name: string; gap: string }[]; week: string; charts: string }

// Simple's order along the path: making a change ready · getting it merged · shipping it.
export const SECTIONS: readonly MetricTextKey[][] = [
  ['pr_ci_wall', 'first_attempt_green', 'time_to_first_green', 'review_time', 'runner_wait', 'flaked_failures', 'time_to_first_green_commit', 'preflight_red_rate'],
  ['pr_open_to_merged', 'queue_run_wall', 'queue_runs_per_pr', 'merge_rounds_model_share', 'queue_ejections', 'queue_unclassified'],
  ['release_queue_to_live', 'nightly_green'],
  ['review_audits', 'escaped_defects'],
]
/** The number is shown in percent. */
const percentKind = (kind: TileKind) => kind === 'share' || kind === 'merge' || kind === 'nightly' || kind === 'rate'
/** Higher is better when no target says so: green shares, scripted merges and green nights; everything else counts trouble. */
const higherIsBetter = (kind: TileKind) => kind === 'share' || kind === 'merge' || kind === 'nightly'
/** The small chart scales 0 to 100 only for shares that can reach 100; a rare rate scales to its own values. */
const sparkPercent = (kind: TileKind) => kind === 'share' || kind === 'merge' || kind === 'nightly'

/** Minutes the way people say them: "16 min", "2 h 10". */
export function plainDuration(minutes: number, lang: DeliveryLanguage): string {
  const r = Math.round(minutes)
  if (r >= 90) return `${Math.floor(r / 60)}${NB}h${NB}${String(r % 60).padStart(2, '0')}`
  return `${num(minutes, lang, minutes < 10 && minutes % 1 ? 1 : 0)}${NB}min`
}

/** The tile's number in its own unit: merge rounds count the scripted share, so higher is better there. */
function current(def: TileDef, window: MetricWindow | null): number | null {
  // Inside the time reports are watched, no recorded audit or defect is said as zero recorded, never as "no data".
  if (def.kind === 'count' && window && window.n === 0 && window.coverage.covered_days > 0) return 0
  if (!window || window.n === 0 || window.value == null) return null
  return def.kind === 'merge' ? 100 - window.value : window.value
}
/** The exact counts of a window as sentence placeholders: never recomputed from a rounded share. */
function countValues(window: MetricWindow, lang: DeliveryLanguage): Record<string, string> {
  const c = window.counts ?? {}, of = (name: string) => num(c[name] ?? 0, lang)
  return {
    never: of('never_green'), sup: of('superseded'), first: of('first_run_green'), total: num(window.n + (c.never_green ?? 0), lang),
    confirmed: of('confirmed'), suspect: of('suspect'), rescue: of('workflow_rescue'), e: of('events'), b: of('base'),
    high: of('high'), medium: of('medium'), low: of('low'), clean: of('clean'),
  }
}
/** The Arion target in the tile's own unit and which way is better. */
export function targetOf(def: TileDef, metric: Metric | undefined): { value: number; up: boolean } | null {
  const target = metric?.target
  if (!target) return null
  if (def.kind === 'merge') return { value: 100 - target.value, up: target.direction === 'max' }
  return { value: target.value, up: target.direction === 'min' }
}

/** On target ≤ 1×, close ≤ 1.5×, far off beyond (Markus, 2026-10-08). */
export function verdictOf(def: TileDef, value: number | null, target: { value: number; up: boolean } | null, mode: Mode, text: SimpleText, lang: DeliveryLanguage, nightlyGap = ''): Verdict {
  if (mode === 'error') return { level: 'none', word: text.notLoaded, gap: '', ratio: null }
  if (value == null) return { level: 'none', word: text.nodataV, gap: '', ratio: null }
  if (!target) return { level: 'none', word: text.none, gap: '', ratio: null }
  if (def.kind === 'nightly') {
    const on = value >= target.value
    return { level: on ? 'on' : 'far', word: on ? text.on : text.far, gap: on ? '' : nightlyGap, ratio: null }
  }
  const ratio = target.up ? target.value / Math.max(value, 0.01) : target.value > 0 ? value / target.value : value > 0 ? Infinity : 0
  const level: VerdictLevel = ratio <= 1 ? 'on' : ratio <= 1.5 ? 'close' : 'far'
  let gap = ''
  if (level !== 'on') {
    if (percentKind(def.kind)) gap = `${pct(value, lang)} vs. ${pct(target.value, lang)} ${text.wanted}`
    else if (Number.isFinite(ratio)) {
      const rounded = ratio >= 2 ? Math.round(ratio) : Math.round(ratio * 10) / 10
      gap = `${Math.abs(rounded - ratio) > 0.1 ? `${text.about} ` : ''}${num(rounded, lang, rounded % 1 ? 1 : 0)}× ${text.theTarget}`
    }
  }
  return { level, word: text[level], gap, ratio: Number.isFinite(ratio) ? ratio : null }
}

function targetLabel(def: TileDef, target: { value: number }, text: SimpleText, lang: DeliveryLanguage): string {
  const value = percentKind(def.kind) ? pct(target.value, lang) : def.kind === 'runs' ? num(target.value, lang, 1) : def.kind === 'per100' || def.kind === 'count' ? num(target.value, lang) : `${num(target.value, lang)}${NB}min`
  return `${text.target} ${value}`
}

function valueParts(def: TileDef, window: MetricWindow, value: number, text: SimpleText, lang: DeliveryLanguage): { text: string; unit: string }[] {
  const n = window.n, u = text.units
  switch (def.kind) {
    case 'share': return [{ text: num(value, lang), unit: '%' }]
    case 'rate': return [{ text: num(value, lang, value < 10 && value % 1 ? 1 : 0), unit: '%' }]
    case 'per100': return [{ text: num(value, lang), unit: u.per100 }]
    case 'count': {
      const key = def.key as 'review_audits' | 'escaped_defects'
      return [{ text: num(value, lang), unit: n === 0 ? u.recorded : value === 1 ? u.countOne[key] : u.count[key] }]
    }
    case 'runs': return [{ text: num(value, lang, 1), unit: u.times }]
    case 'merge': return [{ text: `${num(n - Math.round(n * window.value! / 100), lang)} ${u.of} ${num(n, lang)}`, unit: u.byScript }]
    case 'nightly': return [{ text: `${num(Math.round(n * value / 100), lang)} ${u.of} ${num(n, lang)}`, unit: n === 1 ? u.nightGreen : u.nightsGreen }]
    default: {
      const r = Math.round(value)
      if (r >= 90) return [{ text: String(Math.floor(r / 60)), unit: u.h }, { text: String(r % 60), unit: u.min }]
      const [main, unit] = durationParts(value, lang)
      return [{ text: main, unit }]
    }
  }
}

function trendOf(def: TileDef, window: MetricWindow | null, target: { up: boolean } | null, mode: Mode, days: WindowDays, text: SimpleText, lang: DeliveryLanguage, historyLoading: boolean): Trend {
  if (mode !== 'ready' || !window || window.n === 0 || window.value == null) return { cls: 'none', icon: 'minus', text: text.tNoData }
  // Merge rounds compare the scripted share, like the tile shows it.
  const flip = def.kind === 'merge'
  const own = flip ? { ...window, value: 100 - window.value, previous: window.previous && { ...window.previous, value: window.previous.value == null ? null : 100 - window.previous.value } } : window
  const delta = deltaOf(def, own, target?.up ? 'min' : 'max', { days, text: deliveryText(lang), lang, historyLoading })
  if (delta.cls === 'none') return { cls: 'none', icon: 'minus', text: delta.text === deliveryText(lang).tLoading ? text.tLoading : fill(text.tNone, { w: days }) }
  const words = { better: text.tBetter, worse: text.tWorse, same: text.tSame }[delta.cls]
  return { cls: delta.cls, icon: delta.icon, text: `${fill(words, { w: days })} (${delta.text})` }
}

export function simpleTile(def: TileDef, metrics: Map<MetricKey, Metric>, days: WindowDays, source: MetricSource | null, mode: Mode, lang: DeliveryLanguage): SimpleTileModel {
  const text = simpleText(lang), words = text.metrics[def.key], expert = deliveryText(lang).metrics[def.key]
  const metric = metrics.get(def.key), window = windowOf(metric, days)
  const also = def.also ? windowOf(metrics.get(def.also), days) : null
  const ready = mode === 'ready'
  const value = ready ? current(def, window) : null
  const target = targetOf(def, metric)
  const from = window?.coverage.from ? shortDate(window.coverage.from, lang) : ''
  const cover = ready ? coverText(window?.coverage, deliveryText(lang)) : ''
  const partial = ready && !!window && window.n > 0 && (window.status === 'partial' || !!cover)
  const part = words.part && (!words.part.includes('{d}') || from) ? fill(words.part, { d: from }) : ''
  // Full calendar coverage still leaves a partial window when facts were truncated.
  // The coverage phrase is empty then; the server's reason is what is missing.
  const ownReason = ready && window?.status === 'partial' && window.coverage.full && metric?.reason?.trim() ? metric.reason.trim() : ''
  // The companion can be partial, or missing facts, while the workflow share is whole. Say that too.
  const alsoMetric = def.also ? metrics.get(def.also) : undefined
  const alsoOpen = ready && !!also && (also.status === 'partial' || !also.coverage.full)
  const factsMissing = jobFactsMissing(also, alsoMetric?.reason)
  const alsoReason = (alsoOpen || factsMissing) && alsoMetric?.reason?.trim() ? alsoMetric.reason.trim() : ''
  const alsoCover = alsoOpen && also ? coverText(also.coverage, deliveryText(lang)) : ''
  const reason = [ownReason, alsoReason].filter(Boolean).join(' · ')
  const footBase = partial && (cover || part) ? cover || part : words.src
  const footBits = [footBase, reason, alsoCover && !footBase.includes(alsoCover) ? alsoCover : ''].filter(Boolean)
  const foot = footBits.join(' · ')

  // The sentence, the Learn text and the nightly verdict's gap, all from this window's numbers.
  const n = window?.n ?? 0
  const nights = def.kind === 'nightly' && ready ? chartModel(def, metrics, days, source, lang).nights : []
  let say = words.wants, learn = expert.definition, nightlyGap = ''
  if (value != null && window) {
    const p50 = duration(window.p50 ?? window.value!, lang), p90 = window.p90 == null ? '–' : def.kind === 'runs' ? num(window.p90, lang, window.p90 % 1 ? 1 : 0) : duration(window.p90, lang)
    const values: Record<string, string> = { p50, p90, n: num(n, lang), d: from, ...countValues(window, lang) }
    let sentence = words.say
    switch (def.kind) {
      case 'duration': values.v = plainDuration(value, lang); break
      case 'runs': values.v = num(value, lang, 1); break
      case 'share': {
        values.v = pct(value, lang)
        values.t = target ? pct(target.value, lang) : '–'
        if (factsMissing) {
          // Unavailable, not a measured zero: the sentence says the result is missing and how many runs have job facts.
          values.f = num(also?.n ?? 0, lang)
          sentence = words.sayFacts ?? words.sayAlt ?? sentence
        } else {
          values.v2 = also?.value != null && also.n > 0 ? pct(also.value, lang) : '–'
          values.n2 = also && also.n > 0 ? num(also.n, lang) : '–'
          if (values.v2 === '–') sentence = words.sayAlt ?? sentence
        }
        break
      }
      case 'rate': values.v = pct(value, lang); break
      case 'per100': values.v = num(value, lang); break
      case 'count':
        values.v = num(value, lang)
        if (n === 0) sentence = words.sayAlt ?? sentence
        break
      case 'review':
        values.v = plainDuration(value, lang)
        if (also?.value != null && also.n > 0) Object.assign(values, { k: num(Math.round(also.n * also.value / 100), lang), n: num(also.n, lang) })
        else { sentence = words.sayAlt ?? sentence; values.k = '–'; values.n = '–' }
        break
      case 'merge': {
        const byModel = Math.round(n * window.value! / 100)
        Object.assign(values, { k: num(n - byModel, lang), km: num(byModel, lang), m: pct(window.value!, lang) })
        break
      }
      case 'release': {
        values.v = plainDuration(value, lang)
        const shown = newestRelease(metric?.releases ?? [], days, metric?.daily ?? [])
        if (n === 1 && shown) values.r = shown.release ?? shown.tag ?? shown.key
        else sentence = words.sayAlt ?? sentence
        break
      }
      case 'nightly': {
        const green = Math.round(n * value / 100)
        values.k = num(green, lang)
        const allRed = green === 0
        if (!(allRed && window.coverage.full === false && from)) sentence = words.sayAlt ?? sentence
        const without = nights.some(night => night === 'none')
        nightlyGap = !allRed ? fill(text.greenOf, values) : from && !window.coverage.full && !without ? fill(text.redSince, { d: from }) : text.redRan
        break
      }
    }
    say = `${fill(sentence, values)} ${words.wants}`
    learn = fill(factsMissing && words.learnFacts ? words.learnFacts : words.learn, { ...values, n: def.kind === 'release' ? (n === 1 ? text.releaseOne : fill(text.releaseMany, { n: num(n, lang) })) : def.kind === 'review' ? values.n : num(n, lang) })
      + (partial && words.learnPart && from ? fill(words.learnPart, { d: from }) : '')
  } else if (def.key === 'flaked_failures' && ready && window && n > 0 && window.value == null && words.sayNone) {
    const values = { n: num(n, lang), v: text.noDataYet, ...countValues(window, lang) }
    say = `${fill(words.sayNone, values)} ${words.wants}`
    learn = fill(words.learn, values)
  }
  if (reason && !learn.includes(reason)) learn = `${learn} ${reason}`

  const { buckets, samples } = bucketsOf(metric, days, lang)
  const spark: SparkModel = {
    kind: def.kind === 'nightly' ? 'nightly' : 'line', percent: sparkPercent(def.kind), mode, unit: bucketOf(days),
    statuses: buckets.map(bucket => bucket.status),
    values: samples.map(sample => {
      if (!ready || sample.n === 0 || sample.value == null) return null
      if (def.kind === 'merge') return 100 - sample.value
      return def.kind === 'duration' || def.kind === 'review' || def.kind === 'release' ? sample.p50 ?? sample.value : sample.value
    }),
    target: target && def.kind !== 'nightly' ? { ...target, label: targetLabel(def, target, text, lang) } : null,
    nights: nights.slice(-14),
  }
  return {
    key: def.key, name: words.name, hasTarget: !!target, up: target?.up ?? higherIsBetter(def.kind), value: value != null && window ? valueParts(def, window, value, text, lang) : null,
    empty: mode === 'error' ? text.notLoaded : deliveryText(lang).noData,
    verdict: verdictOf(def, value, target, mode, text, lang, nightlyGap),
    trend: trendOf(def, window, target, mode, days, text, lang, source?.backfill === 'running'),
    say, partial, foot, spark,
    learn: { body: learn, expert: `${expert.label} · ${sourceLabel(def, source, deliveryText(lang))}`, target: expert.target },
  }
}

/** Summary on top: how many are on target, the closest and the biggest gap, the change, how to read the small charts. */
export function summaryOf(tiles: SimpleTileModel[], days: WindowDays, source: MetricSource | null, mode: Mode, noData: boolean, text: SimpleText, lang: DeliveryLanguage): SimpleSummary {
  if (mode === 'loading') return { kind: 'loading' }
  if (mode === 'error') return { kind: 'error' }
  if (noData) return { kind: 'nodata', title: text.nodataT, body: text.nodataB }
  const withTarget = tiles.filter(tile => tile.hasTarget)
  const on = withTarget.filter(tile => tile.verdict.level === 'on').length
  const ranked = withTarget.filter(tile => tile.verdict.ratio != null && tile.verdict.level !== 'on').sort((a, b) => a.verdict.ratio! - b.verdict.ratio!)
  const name = (tile: SimpleTileModel) => lang === 'de' ? tile.name : tile.name.toLowerCase()
  const gaps = ranked.length > 1
    ? [{ label: text.closest, name: name(ranked[0]), gap: ranked[0].verdict.gap }, { label: text.biggest, name: name(ranked[ranked.length - 1]), gap: ranked[ranked.length - 1].verdict.gap }]
    : ranked.map(tile => ({ label: text.biggest, name: name(tile), gap: tile.verdict.gap }))
  const count = (cls: Trend['cls']) => tiles.filter(tile => tile.trend.cls === cls).length
  const since = source?.backfill_since ?? source?.covered_since ?? null
  return {
    kind: 'ready',
    big: on === 0 ? fill(text.sumNone, { t: withTarget.length }) : fill(text.sumSome, { k: on, t: withTarget.length }),
    gaps,
    week: fill(text.week, { w: days, b: count('better'), x: count('worse'), s: count('same'), n: count('none') }),
    charts: fill(text.charts, { u: text.per[bucketOf(days)] }) + (since ? fill(text.chartsHatch, { d: shortDate(since.slice(0, 10), lang) }) : ''),
  }
}

/** The Simple page of one window: summary and three sections in path order. */
export function simpleNumbersOf(data: DeliveryMetrics | null, days: WindowDays, mode: Mode, noData: boolean, lang: DeliveryLanguage) {
  const text = simpleText(lang)
  const metrics = new Map((data?.metrics ?? []).map(metric => [metric.key, metric] as const))
  const source = data?.source ?? null
  const byKey = new Map(TILES.map(def => [def.key, simpleTile(def, metrics, days, source, mode, lang)] as const))
  const sections: SimpleSection[] = SECTIONS.map((keys, index) => {
    const tiles = keys.map(key => byKey.get(key)!)
    const withTarget = tiles.filter(tile => tile.hasTarget)
    return { ...text.sections[index], tiles, count: mode === 'ready' && !noData && withTarget.length ? fill(text.onTarget, { k: withTarget.filter(tile => tile.verdict.level === 'on').length, t: withTarget.length }) : '' }
  })
  return { summary: summaryOf([...byKey.values()], days, source, mode, noData, text, lang), sections }
}

// ---------- The small chart: target zone, "better" on the axis, hatched no data ----------
export interface SparkGeometry {
  zone: { x: number; y: number; width: number; height: number } | null
  target: { y: number; label: string; labelY: number } | null
  axis: { line: [number, number]; arrow: string; better: { x: number; y: number } }
  hatches: { x: number; width: number; label: boolean }[]
  segments: { d: string; partial: boolean }[]
  dots: { x: number; y: number; last: boolean }[]
  plot: { x: number; y: number; width: number; height: number }
}
/** Geometry of one small chart, W × H pixels. Days outside every source are hatched; covered days without samples stay empty. */
export function sparkGeometry(model: SparkModel, W: number, H: number): SparkGeometry {
  const Lm = 16, Rm = 4, Tm = 6, Bm = 6, pw = W - Lm - Rm, ph = H - Tm - Bm, n = Math.max(1, model.values.length)
  const present = model.values.filter((value): value is number => value != null)
  const top = model.percent ? 100 : Math.max(...present, model.target?.value ?? 0, 1) * 1.18
  const Y = (value: number) => Tm + ph - (Math.max(0, Math.min(top, value)) / top) * ph
  const X = (k: number) => Lm + (k + 0.5) * pw / n
  let zone: SparkGeometry['zone'] = null, target: SparkGeometry['target'] = null
  if (model.target) {
    const ty = Y(model.target.value)
    zone = model.target.up ? { x: Lm, y: Tm, width: pw, height: ty - Tm } : { x: Lm, y: ty, width: pw, height: Tm + ph - ty }
    const below = model.target.up ? ty - 4 < Tm + 10 : ty + 12 < Tm + ph
    target = { y: ty, label: model.target.label, labelY: below ? ty + 11 : ty - 4 }
  }
  const up = model.target?.up ?? false
  const axis = {
    line: [Tm + 2, Tm + ph - 2] as [number, number],
    arrow: up ? `M2.5 ${Tm + 6}L6 ${Tm + 1.5}L9.5 ${Tm + 6}` : `M2.5 ${Tm + ph - 6}L6 ${Tm + ph - 1.5}L9.5 ${Tm + ph - 6}`,
    better: { x: Lm + 2, y: up ? Tm + 10 : Tm + ph - 3 },
  }
  // A failed read hatches the whole plot; otherwise only the buckets no source covers.
  const gapAt = (k: number) => model.mode !== 'ready' || model.statuses[k] === 'no_data'
  const hatches: SparkGeometry['hatches'] = []
  let start: number | null = null
  const flush = (end: number) => {
    if (start == null) return
    const x = Lm + start * pw / n, width = (end - start) * pw / n
    hatches.push({ x, width, label: width > 70 && model.mode !== 'error' })
    start = null
  }
  for (let k = 0; k < n; k++) { if (gapAt(k)) start ??= k; else flush(k) }
  flush(n)
  const segments: SparkGeometry['segments'] = [], dots: SparkGeometry['dots'] = []
  let previous: { x: number; y: number; partial: boolean } | null = null
  model.values.forEach((value, k) => {
    if (value == null) { previous = null; return }
    const point = { x: X(k), y: Y(value), partial: model.statuses[k] === 'partial' }
    if (previous) segments.push({ d: `M${previous.x},${previous.y}L${point.x},${point.y}`, partial: point.partial || previous.partial })
    const last = k === model.values.length - 1
    if (last || (!previous && model.values[k + 1] == null)) dots.push({ x: point.x, y: point.y, last })
    previous = point
  })
  return { zone, target, axis, hatches, segments, dots, plot: { x: Lm, y: Tm, width: pw, height: ph } }
}
