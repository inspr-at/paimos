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
  source?: 'github_app' | 'paimos' | 'ops_rollout' | 'arion'; norm?: StepNorm | null; model?: string | null
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

/** At most this many rows per lane; anything beyond stacks in the last row. */
export const MAX_ROWS = 4
export interface LaneItem { run: FlowRun; step: FlowStep; lane: Lane; row: number }
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
        if (row < 0) { row = Math.min(ends.length, MAX_ROWS - 1); if (row === ends.length) ends.push(0) }
        ends[row] = Math.max(ends[row]!, step.end)
        items.push({ run, step, lane, row })
      }
      rows[lane] = Math.max(1, ends.length)
    }
    return { ...set, rows, items }
  })
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
function wallClock(origin: number, minutes: number, seconds: boolean): string {
  const at = new Date(origin + Math.round(minutes * 60) * 1000)
  const hh = String(at.getHours()).padStart(2, '0'), mm = String(at.getMinutes()).padStart(2, '0'), ss = at.getSeconds()
  return `${hh}:${mm}${seconds && ss ? `:${String(ss).padStart(2, '0')}` : ''}`
}
/** "20:25" on an absolute axis, "+14 min" on a relative one. */
export function timeLabel(data: Pick<FlowData, 'origin'>, minutes: number): string {
  return data.origin == null ? `+${minutesText(Math.max(0, minutes))}` : wallClock(data.origin, Math.floor(minutes + 1e-6), false)
}
/** As timeLabel, to the second where the second is not :00. */
export const timeLabelSeconds = (data: Pick<FlowData, 'origin'>, minutes: number) => data.origin == null ? timeLabel(data, minutes) : wallClock(data.origin, minutes, true)

/** Grid step in minutes so that labels sit about `every` px apart. */
export function tickStep(span: number, px: number, every = 90): number {
  const per = span / Math.max(1, px / every)
  for (const step of [1, 2, 5, 10, 15, 30, 60, 120]) if (step >= per) return step
  return 120
}

// ---------- Segment labels ----------
/** Average advance of the 11 px label font; the mock measures the same way. */
export const CHAR_W = 5.9
export interface LabelInput { tag?: string; text: string; since?: string; duration?: string; room: number; pad: number }
/**
 * The longest label that fits: run tag + text, then the text alone, then (waits and
 * repeats) the duration. A "since" suffix stays on every candidate, including the
 * duration. Then the text cut with an ellipsis, still keeping "· since HH:MM".
 * Nothing when even that does not fit — a cut wait never drops its suffix.
 */
export function fitLabel({ tag, text, since = '', duration, room, pad }: LabelInput): string {
  const base = text + since
  const durationLabel = duration ? `${duration}${since}` : ''
  const options = [tag ? `${tag} · ${base}` : base, base, durationLabel].filter(Boolean)
  const fits = options.find(option => option.length * CHAR_W + pad + 6 < room)
  if (fits) return fits
  if (room <= 46) return ''
  const source = tag ? `${tag} · ${text}` : text
  const keep = Math.floor((room - pad - 8 - since.length * CHAR_W) / CHAR_W) - 1
  if (keep > 4) return `${source.slice(0, keep).trimEnd()}…${since}`
  if (since) return ''
  return `${source.slice(0, Math.max(3, Math.floor((room - pad - 8) / CHAR_W) - 1)).trimEnd()}…`
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
export function placeIncidentCaption({ text, x0, x1, anchor, healthy = null }: IncidentCaptionInput): IncidentCaption {
  const inner = Math.max(0, x1 - x0)
  const advance = (value: string) => value.length * CHAR_W
  const fitText = (max: number): string => {
    if (advance(text) <= max) return text
    const ellipsis = '…'
    let keep = Math.floor((max - advance(ellipsis)) / CHAR_W)
    while (keep > 0) {
      const shown = `${text.slice(0, keep).trimEnd()}${ellipsis}`
      if (advance(shown) <= max) return shown
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
        const items = row.slice(k, j + 1), ca = X(items[0]!.step.start), cb = X(items[items.length - 1]!.step.end)
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
