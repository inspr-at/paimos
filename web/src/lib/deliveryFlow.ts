// SPDX-License-Identifier: AGPL-3.0-only
// Delivery › Flow lanes (AEON-994 draft 5, packages 5 and 6): the lane model and the
// time window. Lanes are actors; rows inside a lane are reserved once per data set, so
// zooming and panning never move a lane. Time is in minutes: on an absolute axis
// minutes after `origin` (epoch ms), on a relative axis minutes after the run's start.
import { reactive } from 'vue'
import type { DeliveryLanguage } from './delivery'

export type Lane = 'you' | 'lead' | 'ops' | 'review' | 'build' | 'ci'
export const LANES: readonly Lane[] = ['you', 'lead', 'ops', 'review', 'build', 'ci']
export type StepKind = 'work' | 'wait' | 'rework'
export type FlowLevel = 'simple' | 'expert'
export interface Words { en: string; de: string }
export type StepOutcome = 'ok' | 'changes' | 'red' | 'flaky' | 'degraded' | 'green'
export type WaitReason = 'reviewer' | 'queue' | 'dependency' | 'human_gate' | 'rerun' | 'release_train'
/** Usual minutes of the same step (p50, p90) and Arion's target, where known. */
export interface StepNorm { p50: number | null; p90: number | null; arion: number | null }
/** Recorded facts of a step (package 6): the moment panel and the tables read them. */
export interface StepFacts {
  id?: string; round?: number; outcome?: StepOutcome | null; waitReason?: WaitReason | null
  /** Who or what a wait is for, already in words (a run tag, a person's role). */
  waitsFor?: string | null
  source?: 'github_app' | 'check_suite' | 'paimos' | 'ops_rollout' | 'arion'; norm?: StepNorm | null; model?: string | null
  /** Still running: the end is the expected one. */
  open?: boolean
}
export interface FlowStep {
  start: number; end: number; kind: StepKind; lane: Lane
  expert: Words; simple: Words
  side?: boolean; incident?: boolean; after?: boolean; stepKey?: string; facts?: StepFacts
}
export interface FlowIncident { start: number; end: number; expert: Words; simple: Words; open?: boolean }
/** A run's progress and estimate at the moment of reading (package 6), in minutes on the data's axis. */
export interface RunFacts {
  kind: 'release' | 'change'; ref: string
  started: number | null; ended: number | null
  pct: number | null; currentStepId: string | null
  eta: { p50: number | null; p90: number | null; basis: 'history' | 'ops' | 'none'; reason: string | null }
  gate: string | null
  /** The release record a rollout reported (AEON-1022); absent on the Arion target and on runs no rollout reported on. */
  record?: ReleaseFacts
}
/** The latest attempt of the rehearsal or the catalogue, read from every step the answer holds (never from a lane slice). */
export interface RecordedAttempt {
  /** Epoch ms of its start. */
  startAt: number
  /** Minutes it took, or has taken so far while it is still open. */
  minutes: number; open: boolean; outcome: StepOutcome | null; p50: number | null; p90: number | null
  /** How many attempts the record holds. */
  runs: number
}
export interface ReleaseFacts {
  evidence: string | null; rollback: 'digest_safe' | 'restore_required' | null
  catalogue: RecordedAttempt | null; rehearsal: RecordedAttempt | null
  /** The answer may have left steps out (a window that starts after the release did, or a truncated read), so "none" is not a finding. */
  partial: boolean
}
export interface FlowRun {
  id: string; tag: string; title: Words; steps: FlowStep[]
  incident?: FlowIncident; target?: { start: number; minutes: number }; isTarget?: boolean; facts?: RunFacts
}
export interface FlowSetInput { title?: Words; runs: FlowRun[]; lanes: readonly Lane[]; main: FlowRun; multi?: boolean }
export interface FlowData {
  /** Epoch ms of minute 0 on an absolute axis; null for a relative axis (+N min). */
  origin: number | null
  /** "Now" for Live; null when the data set is finished history. */
  now: number | null
  /** Whole run (overview) and the stretch a playhead walks through. */
  range: [number, number]; play: [number, number]
  sets: FlowSetInput[]
}

/**
 * A lane grows to every row its concurrent steps need, so no two bars ever overlap (AEON-1656).
 * Above this many rows a collapsed lane shows ROW_CAP - 1 rows and one "+N more" row.
 */
export const ROW_CAP = 12
export interface LaneItem { run: FlowRun; step: FlowStep; lane: Lane; row: number }
/** `rows` is what each lane needs, uncapped; laneRows decides what a lane shows. */
export interface LaneSet extends FlowSetInput { rows: Record<string, number>; items: LaneItem[] }

/** Reserve rows per lane for the whole data set: the first row free at a step's start, else a new one. */
export function laneModel(data: FlowData): LaneSet[] {
  return data.sets.map(set => {
    const rows: Record<string, number> = {}, items: LaneItem[] = []
    for (const lane of set.lanes) {
      const steps = set.runs.flatMap(run => run.steps.filter(step => step.lane === lane).map(step => ({ run, step })))
        .sort((a, b) => a.step.start - b.step.start)
      const ends: number[] = []
      for (const { run, step } of steps) {
        let row = ends.findIndex(end => end <= step.start + 1e-6)
        if (row < 0) { row = ends.length; ends.push(0) }
        ends[row] = Math.max(ends[row]!, step.end)
        items.push({ run, step, lane, row })
      }
      rows[lane] = Math.max(1, ends.length)
    }
    return { ...set, rows, items }
  })
}

export interface LaneRows {
  /** Rows drawn with bars: every needed row, or ROW_CAP - 1 while collapsed above the cap. */
  bars: number
  /** The row after the bars: "+N more" (collapsed) or "fewer" (expanded); null within the cap. */
  fold: { kind: 'more' | 'fewer'; hidden: LaneItem[] } | null
  /** Rows the lane is tall: bars plus the fold row. */
  total: number
}
/** What one lane shows. Within the cap every row; above it a person expands or collapses the lane. */
export function laneRows(set: LaneSet, lane: string, expanded: boolean): LaneRows {
  const need = set.rows[lane] ?? 1
  if (need <= ROW_CAP) return { bars: need, fold: null, total: need }
  if (expanded) return { bars: need, fold: { kind: 'fewer', hidden: [] }, total: need + 1 }
  const bars = ROW_CAP - 1
  return { bars, fold: { kind: 'more', hidden: set.items.filter(i => i.lane === lane && i.row >= bars) }, total: ROW_CAP }
}

/** Where a lane sits in the stage: top, height and the height of each of its rows, in px. */
export interface LaneGeo { y: number; h: number; rowH: number }
/**
 * Place one lane at `y` (AEON-1656). Without an anchor it is `total` rows of `rowH`. With one
 * (the pointer, in stage px) a live update never moves what is above it: a lane that ended
 * above the anchor keeps its height and fits its rows inside; the lane under the anchor keeps
 * its row height and grows or shrinks downward; lanes below follow. `prev` is the lane as last drawn.
 */
export function placeLane(prev: LaneGeo | undefined, total: number, y: number, rowH: number, gaps: number, anchor: number | null): LaneGeo {
  const natural = { y, h: total * rowH + gaps, rowH }
  if (anchor == null || !prev || Math.abs(prev.y - y) > 0.01 || prev.y > anchor) return natural
  if (prev.y + prev.h <= anchor) return { y, h: prev.h, rowH: Math.min(rowH, (prev.h - gaps) / total) }
  return { y, h: total * prev.rowH + gaps, rowH: prev.rowH }
}

/** Centre the 20 px avatar artwork on its bar, leaving room inside a stacked row. */
export function placeRowAvatar(barY: number, barH: number, rowH: number): { y: number; scale: number; h: number } {
  const scale = Math.max(0, Math.min(0.8, (rowH - 2) / 20))
  return { y: barY + barH / 2, scale, h: 20 * scale }
}

/** The steps a run walks through, alongside work left out. */
export const criticalPath = (run: FlowRun) => run.steps.filter(step => !step.side && !step.after).sort((a, b) => a.start - b.start)
/** Where the run ends: the end of its last critical step. */
export function runEnd(run: FlowRun): number {
  const path = criticalPath(run)
  return path.length ? Math.max(...path.map(step => step.end)) : 0
}
/** The critical step at a moment: the one running, else the last one before it, else the first. */
export function stepAt(path: readonly FlowStep[], at: number): FlowStep | null {
  if (!path.length) return null
  const running = path.filter(step => at >= step.start - 1e-6 && at < step.end - 1e-6)
  if (running.length) return running[running.length - 1]!
  const before = path.filter(step => step.end <= at + 1e-6)
  return before.length ? before[before.length - 1]! : path[0]!
}

// ---------- Words and times ----------
const NB = ' '
export const pick = (words: Words, lang: DeliveryLanguage) => words[lang] || words.en
export function minutesText(minutes: number): string {
  const rounded = Math.round(minutes)
  if (rounded < 60) return `${rounded}${NB}min`
  return `${Math.floor(rounded / 60)}${NB}h${NB}${String(rounded % 60).padStart(2, '0')}`
}
// Building a formatter costs far more than using one; the lanes label many times per render (AEON-1352).
const zoneClocks = new Map<string, Intl.DateTimeFormat>()
function zoneClock(timeZone: string, seconds: boolean) {
  const key = `${timeZone}|${seconds}`
  let clock = zoneClocks.get(key)
  if (!clock) {
    const options: Intl.DateTimeFormatOptions = { timeZone, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }
    if (seconds) options.second = '2-digit'
    clock = new Intl.DateTimeFormat('en-GB', options)
    zoneClocks.set(key, clock)
  }
  return clock
}
function wallClock(origin: number, minutes: number, seconds: boolean, timeZone?: string): string {
  const at = new Date(origin + Math.round(minutes * 60) * 1000)
  let hh: string, mm: string, ss: number
  if (timeZone) {
    // The named zone, so CI (UTC) and a Vienna reader agree on the same instant.
    const parts = zoneClock(timeZone, seconds).formatToParts(at)
    const pick = (type: Intl.DateTimeFormatPartTypes) => parts.find(part => part.type === type)?.value ?? '00'
    hh = pick('hour') === '24' ? '00' : pick('hour')
    mm = pick('minute')
    ss = seconds ? Number(pick('second')) : 0
  } else {
    hh = String(at.getHours()).padStart(2, '0')
    mm = String(at.getMinutes()).padStart(2, '0')
    ss = at.getSeconds()
  }
  return `${hh}:${mm}${seconds && ss ? `:${String(ss).padStart(2, '0')}` : ''}`
}
/** "20:25" on an absolute axis, "+14 min" on a relative one. `timeZone` names the wall clock; omitted, it is the host's. */
export function timeLabel(data: Pick<FlowData, 'origin'>, minutes: number, timeZone?: string): string {
  return data.origin == null ? `+${minutesText(Math.max(0, minutes))}` : wallClock(data.origin, Math.floor(minutes + 1e-6), false, timeZone)
}
/** As timeLabel, to the second where the second is not :00. */
export const timeLabelSeconds = (data: Pick<FlowData, 'origin'>, minutes: number, timeZone?: string) => data.origin == null ? timeLabel(data, minutes) : wallClock(data.origin, minutes, true, timeZone)

/** Grid step in minutes so that labels sit about `every` px apart. */
export function tickStep(span: number, px: number, every = 90): number {
  const per = span / Math.max(1, px / every)
  for (const step of [1, 2, 5, 10, 15, 30, 60, 120]) if (step >= per) return step
  return 120
}

// ---------- Segment labels ----------
/** Average advance of the 11 px label font; the mock measures the same way. */
export const CHAR_W = 5.9
export interface LabelInput { tag?: string; text: string; since?: string; duration?: string; room: number; pad: number; textWidth?: (value: string) => number }
/**
 * A multi-run bar keeps its complete run tag: shorten the description first,
 * then show just the tag. Keep "since" while it fits beside the tag. Untagged
 * bars fall back to text, duration and clipped text, always keeping "since".
 * Descriptions keep the existing padding; a bare key can use the whole bar.
 */
export function fitLabel({ tag, text, since = '', duration, room, pad, textWidth }: LabelInput): string {
  const advance = (value: string) => textWidth ? textWidth(value) : value.length * CHAR_W
  const fits = (value: string) => advance(value) + pad + 6 < room
  const clipped = (prefix: string, suffix: string, minimum: number): string => {
    let low = 0, high = Math.max(0, text.length - 1)
    while (low < high) {
      const mid = Math.ceil((low + high) / 2)
      if (fits(`${prefix}${text.slice(0, mid).trimEnd()}…${suffix}`)) low = mid
      else high = mid - 1
    }
    return low >= minimum ? `${prefix}${text.slice(0, low).trimEnd()}…${suffix}` : ''
  }
  if (tag) {
    // AEON-1698: a shorter label must still identify its run. Never fall back to
    // description/duration alone, or clip a key into a different-looking key.
    const full = `${tag} · ${text}${since}`
    if (fits(full)) return full
    if (duration && fits(`${tag} · ${duration}${since}`)) return `${tag} · ${duration}${since}`
    const suffix = fits(tag + since) ? since : ''
    const base = `${tag} · ${text}`
    if (fits(base + suffix)) return base + suffix
    // A bare key may use the entire bar; the renderer centres it and omits the
    // optional kind icon. Padding must not hide a key that fits by itself.
    const shortened = clipped(`${tag} · `, suffix, 1)
    if (shortened) return shortened
    if (fits(tag + suffix)) return tag + suffix
    return advance(tag) <= room ? tag : ''
  }
  const base = text + since
  const durationLabel = duration ? `${duration}${since}` : ''
  const candidate = [base, durationLabel].filter(Boolean).find(fits)
  if (candidate) return candidate
  if (room <= 46) return ''
  return clipped('', since, since ? 5 : 3)
}

/** Plot frame for a lanes stage: actor names sit left of x0, the plot ends at x1. */
export function laneFrame(width: number): { x0: number; x1: number } {
  const narrow = width < 640
  const x0 = narrow ? 84 : 156
  return { x0, x1: Math.max(x0 + 1, width - (narrow ? 6 : 12)) }
}

const CAPTION_TEXT = 14
const CAPTION_DOT = 10
const HEALTHY_TEXT = 13
export interface IncidentCaptionInput {
  text: string
  x0: number
  x1: number
  /** Preferred left edge of the marker dot. */
  anchor: number
  /** Drawn at the recovery point when the whole marker fits; otherwise folded into `full`. */
  healthy?: { text: string; anchor: number } | null
  /**
   * Ink width of the words, including the outline the screen paints.
   * CHAR_W is only the fallback: a wider face (Linux UI fonts) plus the
   * caption stroke overflows a frame that the estimate says fits.
   */
  textWidth?: (value: string) => number
}
export interface IncidentCaption {
  /** Left edge of the marker dot. */
  x: number
  /** The words that fit. An ellipsis means `full` is longer. */
  text: string
  /** Every word, including a healthy note the frame could not draw. */
  full: string
  healthyX: number | null
}
/** Keep an incident caption inside the plot. The dot, the words and a healthy note all end at or before x1. */
export function placeIncidentCaption({ text, x0, x1, anchor, healthy = null, textWidth }: IncidentCaptionInput): IncidentCaption {
  const inner = Math.max(0, x1 - x0)
  const advance = (value: string) => value ? (textWidth ? textWidth(value) : value.length * CHAR_W) : 0
  const fitText = (max: number): string => {
    if (advance(text) <= max) return text
    const ellipsis = '…'
    let keep = text.length - 1
    while (keep > 0) {
      const shown = `${text.slice(0, keep).trimEnd()}${ellipsis}`
      if (shown !== ellipsis && advance(shown) <= max) return shown
      keep--
    }
    return ''
  }
  const shown = fitText(Math.max(0, inner - CAPTION_TEXT))
  const width = shown ? CAPTION_TEXT + advance(shown) : Math.min(CAPTION_DOT, inner)
  let x = Math.max(x0, anchor)
  if (x + width > x1) x = Math.max(x0, x1 - width)
  let healthyX: number | null = null
  if (healthy?.text) {
    const hWidth = HEALTHY_TEXT + advance(healthy.text)
    const hx = Math.max(healthy.anchor, x + width + 8)
    if (hx >= x0 && hx + hWidth <= x1) healthyX = hx
  }
  return { x, text: shown, full: healthy?.text && healthyX == null ? `${text} · ${healthy.text}` : text, healthyX }
}

// ---------- Slivers ----------
/** Narrower than this a step is a sliver; neighbouring slivers merge into one "+N". */
export const SLIVER_PX = 7
export type RowPiece<T> = { kind: 'step'; item: T; x: number; w: number } | { kind: 'cluster'; items: T[]; x: number; w: number; from: number; to: number }
/** Lay out one row: steps at their place, runs of close slivers as a "+N" pill that zooms in. */
export function rowPieces<T extends { step: { start: number; end: number } }>(row: readonly T[], X: (minutes: number) => number, x0: number, x1: number): RowPiece<T>[] {
  const out: RowPiece<T>[] = []
  let k = 0
  while (k < row.length) {
    const item = row[k]!, xa = X(item.step.start), xb = X(item.step.end), w = xb - xa
    if (xb < x0 - 2 || xa > x1 + 2) { k++; continue }
    if (w < SLIVER_PX) {
      let j = k
      while (j + 1 < row.length && X(row[j + 1]!.step.start) - X(row[j]!.step.end) < 6 && X(row[j + 1]!.step.end) - X(row[j + 1]!.step.start) < SLIVER_PX) j++
      if (j > k) {
        // A pill is at least 26 px wide. Absorb following pieces until its
        // painted width leaves a 2 px gap, even when the next step is not tiny.
        const ca = X(item.step.start)
        while (j + 1 < row.length && X(row[j + 1]!.step.start) < ca + Math.max(26, X(row[j]!.step.end) - ca) + 2) j++
        const items = row.slice(k, j + 1), cb = X(items[items.length - 1]!.step.end)
        out.push({ kind: 'cluster', items, x: ca, w: Math.max(26, cb - ca), from: items[0]!.step.start, to: items[items.length - 1]!.step.end })
        k = j + 1
        continue
      }
    }
    out.push({ kind: 'step', item, x: xa, w })
    k++
  }
  return out
}

// ---------- Overview touch targets ----------
export interface OverviewHit { hx: number; hw: number; hy: number; hh: number }
export interface OverviewHandleHit extends OverviewHit {
  /** Brush edge the handle belongs to. */
  x: number
  side: 'hl' | 'hr'
  /** Where the 8 px grip is drawn, inside this handle's own target. */
  gx: number
}
/**
 * Phone and touch hits for the overview. A 44 px target stays inside the strip.
 * Handles reach outward so a narrow brush does not stack them, then, at a range
 * end where that outward slot is already taken, sit in order beside the playhead.
 * The drawn grip stays inside its own target, so the playhead cannot cover it.
 */
export function placeOverviewTargets(input: {
  width: number
  left: number
  right: number
  play: number
  /** 44 on a phone or a coarse pointer; 0 keeps the fine-pointer grip. */
  hit: number
  height: number
}): { play: OverviewHit; handles: OverviewHandleHit[] } {
  const limit = Math.max(0, input.width), height = input.height
  const clampBox = (preferred: number, size: number) => {
    const hw = Math.min(size, limit)
    return { hx: Math.min(Math.max(0, preferred), Math.max(0, limit - hw)), hw }
  }
  const overlaps = (a: { hx: number; hw: number }, b: { hx: number; hw: number }) => a.hx < b.hx + b.hw && b.hx < a.hx + a.hw
  const grip = (anchor: number, hx: number, hw: number) => {
    const inset = 4
    return anchor >= hx + inset && anchor <= hx + hw - inset ? anchor : hx + hw / 2
  }
  if (!input.hit) {
    const playBox = clampBox(input.play - 7, 14)
    return {
      play: { ...playBox, hy: 0, hh: height - 14 },
      handles: ([['hl', input.left], ['hr', input.right]] as const).map(([side, x]) => ({ x, side, hx: x - 4, hw: 8, hy: 10, hh: height - 33, gx: x })),
    }
  }
  const size = input.hit
  const playBox = clampBox(input.play - size / 2, size)
  const placed = ([['hl', input.left], ['hr', input.right]] as const).map(([side, x]) => {
    let box = clampBox(side === 'hl' ? x - size + 6 : x - 6, size)
    if (overlaps(box, playBox)) {
      const outward = clampBox(side === 'hl' ? playBox.hx - size : playBox.hx + playBox.hw, size)
      const inward = clampBox(side === 'hl' ? playBox.hx + playBox.hw : playBox.hx - size, size)
      box = overlaps(outward, playBox) ? inward : outward
    }
    return { x, side, ...box, hy: (height - 14 - size) / 2, hh: size, gx: x }
  })
  const left = placed[0]!, right = placed[1]!
  if (overlaps(left, right)) {
    const gaps = [{ start: 0, end: playBox.hx }, { start: playBox.hx + playBox.hw, end: limit }]
    const anchor = (left.x + right.x) / 2
    const fits = gaps.filter(gap => gap.end - gap.start >= size * 2)
    const pool = fits.length ? fits : gaps
    const distance = (gap: { start: number; end: number }) => anchor < gap.start ? gap.start - anchor : anchor > gap.end ? anchor - gap.end : 0
    const gap = pool.slice().sort((a, b) => distance(a) - distance(b) || (b.end - b.start) - (a.end - a.start))[0]
    if (gap && gap.end - gap.start >= size * 2) {
      if (playBox.hx >= gap.end) { right.hx = gap.end - size; left.hx = right.hx - size }
      else if (playBox.hx + playBox.hw <= gap.start) { left.hx = gap.start; right.hx = left.hx + size }
      else { left.hx = Math.min(Math.max(gap.start, left.x - size + 6), gap.end - size * 2); right.hx = left.hx + size }
    } else if (limit >= size * 3) {
      if (anchor >= limit / 2) { playBox.hx = limit - size; right.hx = playBox.hx - size; left.hx = right.hx - size }
      else { playBox.hx = 0; left.hx = size; right.hx = size * 2 }
    }
  }
  for (const handle of placed) handle.gx = grip(handle.x, handle.hx, handle.hw)
  return { play: { ...playBox, hy: 0, hh: height - 14 }, handles: placed }
}

// ---------- The time window ----------
/** The shortest window the zoom allows, in minutes. */
export const MIN_SPAN = 4
export const ZOOMS = [15, 30, 60] as const
const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value))
export interface Timeline {
  /** Playhead, visible window, whole range, play range. */
  T: number; v0: number; v1: number; r0: number; r1: number; p0: number; p1: number
  now: number | null; follow: boolean
  /** Epoch ms of minute 0. A later read can move midnight without moving the person. */
  origin: number | null
}
export interface TimelineDefaults { narrow: boolean }
export const LIVE_AT = 0.65

/** Default windows: Live 45 min (20 on phone) with now at 65 %; otherwise 40 min (15) from the start. */
export function resetTimeline(t: Timeline, data: FlowData, { narrow }: TimelineDefaults) {
  t.origin = data.origin
  t.r0 = data.range[0]; t.r1 = data.range[1]; t.p0 = data.play[0]; t.p1 = data.play[1]; t.now = data.now; t.follow = true
  if (data.now != null) { t.T = data.now; setWindow(t, narrow ? 20 : 45, data.now, LIVE_AT) }
  else { t.T = t.p0; setWindow(t, narrow ? 15 : 40, t.p0, 0.1) }
}
/** A window `span` minutes wide with `at` at `frac` of it, kept inside the range. */
export function setWindow(t: Timeline, span: number, at: number, frac: number) {
  const w = clamp(span, MIN_SPAN, t.r1 - t.r0)
  const v0 = clamp(at - w * frac, t.r0, t.r1 - w)
  t.v0 = v0; t.v1 = v0 + w
}
/** Zoom to `span`, keeping `anchor` where it is on screen (or at `frac`). */
export function zoomTo(t: Timeline, span: number, anchor = t.T, frac?: number) {
  const f = frac ?? clamp((anchor - t.v0) / (t.v1 - t.v0), 0, 1)
  setWindow(t, span, anchor, Number.isFinite(f) ? f : 0.5)
}
export function fitAll(t: Timeline) { t.v0 = t.r0; t.v1 = t.r1 }
export function panBy(t: Timeline, minutes: number) {
  const w = t.v1 - t.v0
  t.v0 = clamp(t.v0 + minutes, t.r0, t.r1 - w); t.v1 = t.v0 + w
}
/** Move the time (never by a click): the window follows only when the time leaves it. */
export function setTime(t: Timeline, minutes: number) {
  t.T = clamp(minutes, t.r0, t.r1)
  if (t.T < t.v0 || t.T > t.v1) setWindow(t, t.v1 - t.v0, t.T, 0.4)
}
/** Following: Live keeps now at 65 %; a playing run keeps its playhead in view. */
export function followTick(t: Timeline) {
  if (!t.follow) return
  const w = t.v1 - t.v0
  if (t.now != null) { setWindow(t, w, t.now, LIVE_AT); return }
  if (t.T > t.v0 + w * 0.72 || t.T < t.v0 + w * 0.05) setWindow(t, w, t.T, 0.3)
}
/** Live is "at now" while the playhead sits on now. */
export const atNow = (t: Timeline) => t.now != null && Math.abs(t.T - t.now) < 0.01
/** The follow button is on (and idle) while following, and in Live only while at now. */
export const following = (t: Timeline) => t.now != null ? t.follow && atNow(t) : t.follow
export function backToFollow(t: Timeline) {
  t.follow = true
  if (t.now != null) t.T = t.now
  followTick(t)
}
/** Which zoom preset the window matches, if any. */
export function zoomPreset(t: Timeline): 'fit' | typeof ZOOMS[number] | null {
  const w = t.v1 - t.v0
  if (Math.abs(w - (t.r1 - t.r0)) < 0.5) return 'fit'
  return ZOOMS.find(z => Math.abs(w - z) < 0.5) ?? null
}

// ---------- Input: wheel and keys move the window or the time, never both ----------
/** ⌘/Ctrl+wheel zooms around the pointer; a sideways (or Shift) wheel pans; a plain vertical wheel is the page's. */
export function wheelInput(t: Timeline, input: { deltaX: number; deltaY: number; zoomKey: boolean; shiftKey: boolean; at: number; minutesPerPx: number }): boolean {
  const w = t.v1 - t.v0
  if (input.zoomKey) {
    t.follow = false
    zoomTo(t, clamp(w * Math.exp(input.deltaY * 0.003), MIN_SPAN, t.r1 - t.r0), input.at, (input.at - t.v0) / w)
    return true
  }
  if (Math.abs(input.deltaX) > Math.abs(input.deltaY) || input.shiftKey) {
    t.follow = false
    panBy(t, (input.shiftKey ? input.deltaY : input.deltaX) * input.minutesPerPx)
    return true
  }
  return false
}
export type KeyResult = 'time' | 'window' | 'lane-up' | 'lane-down' | 'select' | 'clear' | null
/** Shift+←/→ move the time a minute; ←/→ pan 10 %; Home/End jump the time; +/− zoom; ↑/↓, Enter and Esc are the caller's. */
export function keyInput(t: Timeline, key: string, shiftKey: boolean): KeyResult {
  const w = t.v1 - t.v0
  if (shiftKey && (key === 'ArrowLeft' || key === 'ArrowRight')) { t.follow = false; setTime(t, t.T + (key === 'ArrowLeft' ? -1 : 1)); return 'time' }
  switch (key) {
    case 'ArrowLeft': t.follow = false; panBy(t, -w * 0.1); return 'window'
    case 'ArrowRight': t.follow = false; panBy(t, w * 0.1); return 'window'
    case 'Home': t.follow = false; setTime(t, t.p0); return 'time'
    case 'End': t.follow = false; setTime(t, t.p1); return 'time'
    case '+': case '=': t.follow = false; zoomTo(t, w / 1.5); return 'window'
    case '-': t.follow = false; zoomTo(t, w * 1.5); return 'window'
    case 'ArrowUp': return 'lane-up'
    case 'ArrowDown': return 'lane-down'
    case 'Enter': return 'select'
    case 'Escape': return 'clear'
  }
  return null
}

/**
 * New data for the same view (a live update, a refetched run): ranges and "now" move,
 * the person's window and time stay. Minute coordinates belong to an origin, so a read
 * that moves midnight (an older run dropped out) is translated before the clamp.
 * Following Live keeps the playhead on the new now.
 */
export function refreshTimeline(t: Timeline, data: FlowData) {
  const wasNow = atNow(t)
  if (t.origin != null && data.origin != null && t.origin !== data.origin) {
    const shift = (t.origin - data.origin) / 60_000
    t.T += shift
    t.v0 += shift
    t.v1 += shift
  }
  t.origin = data.origin
  t.r0 = data.range[0]; t.r1 = data.range[1]; t.p0 = data.play[0]; t.p1 = data.play[1]; t.now = data.now
  const w = Math.min(t.v1 - t.v0, t.r1 - t.r0)
  t.v0 = clamp(t.v0, t.r0, t.r1 - w); t.v1 = t.v0 + w
  if (data.now != null && wasNow) t.T = data.now
  else t.T = clamp(t.T, t.r0, t.r1)
  followTick(t)
}

export function createTimeline(): Timeline {
  return reactive<Timeline>({ T: 0, v0: 0, v1: 1, r0: 0, r1: 1, p0: 0, p1: 1, now: null, follow: true, origin: null })
}
