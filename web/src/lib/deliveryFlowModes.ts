// SPDX-License-Identifier: AGPL-3.0-only
// Delivery › Flow modes (AEON-994 draft 5, package 6): what the page says about a moment
// and a run — the moment panel, the headline, the in-flight table (Live), where the time
// went (Replay, Compare) — and the replay player. Pure functions over the lane model, so
// the words follow the data and the person's level and language.
import { reactive } from 'vue'
import type { DeliveryLanguage } from './delivery'
import {
  criticalPath, followTick, minutesText, pick, runEnd, stepAt, timeLabel, timeLabelSeconds,
  type FlowData, type FlowLevel, type FlowRun, type FlowStep, type Lane, type LaneItem, type LaneSet, type RecordedAttempt, type Timeline, type Words,
} from './deliveryFlow'
import { put, TIMES, TO, type FlowText } from './deliveryFlowText'
import { OUTCOME_PLAIN, WAIT_OBJECT } from './deliveryFlowWords'

export type FlowMode = 'live' | 'replay' | 'compare'
export const FLOW_MODES: readonly FlowMode[] = ['live', 'replay', 'compare']
export interface Part { text: string; strong?: boolean }
interface Ctx { data: FlowData; level: FlowLevel; lang: DeliveryLanguage; text: FlowText; timeZone?: string }

const dec = (value: number, lang: DeliveryLanguage) => lang === 'de' ? String(value).replace('.', ',') : String(value)
/** Minutes as a number: one decimal below 10, whole above. */
export function fmtMin(minutes: number, lang: DeliveryLanguage): string {
  const v = Math.max(0, minutes)
  return dec(v < 10 ? Math.round(v * 10) / 10 : Math.round(v), lang)
}
const words = (step: FlowStep, level: FlowLevel, lang: DeliveryLanguage) => pick(level === 'simple' ? step.simple : step.expert, lang)
const plain = (step: FlowStep, lang: DeliveryLanguage) => pick(step.simple, lang)
const lower = (value: string) => value.replace(/^\p{Lu}(?!\p{Lu})/u, c => c.toLowerCase())
const upper = (value: string) => value.replace(/^\p{Ll}/u, c => c.toUpperCase())
const actorName = (ctx: Pick<Ctx, 'level' | 'text'>, lane: Lane) => ctx.text.actors[ctx.level][lane]
const incidentAt = (run: FlowRun, at: number) => !!run.incident && at >= run.incident.start && at < run.incident.end
const titleOf = (run: FlowRun, lang: DeliveryLanguage) => pick(run.title, lang).replace(` (a ${TO} l)`, '')

// ---------- The moment panel ----------
export interface MomentLine { key: string; lane: Lane; who: string; kind: 'work' | 'wait' | 'rework' | 'inc' | 'done'; label: string; expected: boolean; tail: string; more: number }
export interface Moment {
  head: string; sentence: string; lines: MomentLine[]
  /** One line for every idle lane, with "nothing waiting on you" when the person is idle. */
  idle: string | null
  detail: { title: string; tag: string; rows: [string, string][] } | null
  incident: { title: string; meta: string } | null
}

export function momentOf(ctx: Ctx & { sets: LaneSet[]; T: number; selected: LaneItem | null }): Moment {
  const { data, sets, T, level, lang, text } = ctx, de = lang === 'de'
  const rel = data.origin == null, live = data.now != null, now = data.now ?? 0
  const isNow = live && Math.abs(T - now) <= 0.01, past = !live || T < now - 0.01
  const lines: MomentLine[] = [], idleLanes: Lane[] = [], youWaits: string[] = []
  let youIdle = false
  sets.forEach((set, si) => {
    const prefix = rel && set.title ? `${titleOf(set.main, lang)} · ` : ''
    // "Live after" needs a recorded end. An open run's last minute is elapsed time, not arrival.
    const complete = set.main.isTarget || set.main.facts?.ended != null
    if (rel && T >= runEnd(set.main)) {
      const end = runEnd(set.main)
      lines.push(complete
        ? { key: `${si}:done`, lane: set.lanes[0]!, who: titleOf(set.main, lang), kind: 'done', label: put(text.liveAfter, { d: minutesText(end) }), expected: false, tail: '', more: 0 }
        : { key: `${si}:open`, lane: set.lanes[0]!, who: titleOf(set.main, lang), kind: 'work', label: put(text.soFarMin, { m: fmtMin(end, lang) }), expected: false, tail: '', more: 0 })
      return
    }
    for (const lane of set.lanes) {
      const act = set.items.filter(i => i.lane === lane && i.step.start <= T + 1e-6 && i.step.end > T + 1e-6)
      if (!act.length) {
        if (lane === 'you') youIdle = true
        else if (!idleLanes.includes(lane)) idleLanes.push(lane)
        continue
      }
      const item = act.find(i => i.run === set.main) ?? act[0]!, s = item.step, norm = s.facts?.norm
      const so = T - s.start, dur = s.end - s.start, round = s.facts?.round ?? 0, outcome = s.facts?.outcome
      const tail = level === 'simple'
        ? `${fmtMin(so, lang)} ${text.of} ${fmtMin(dur, lang)} min${norm?.p50 != null ? ` · ${text.usually} ${fmtMin(norm.p50, lang)}` : ''}${norm?.arion != null ? ` · ${text.arion} ${fmtMin(norm.arion, lang)}` : ''}`
        : `${fmtMin(so, lang)}/${fmtMin(dur, lang)} min${norm?.p50 != null ? ` · p50 ${fmtMin(norm.p50, lang)}` : ''}${norm?.arion != null ? ` · Arion ${fmtMin(norm.arion, lang)}` : ''}${round > 1 ? ` · r${round}` : ''}${outcome ? ` · ${outcome}` : ''}`
      if (lane === 'you' && s.kind === 'wait') youWaits.push(`${item.run.tag}: ${plain(s, lang)}`)
      lines.push({
        key: `${si}:${lane}`, lane, who: `${prefix}${actorName(ctx, lane)}`, kind: s.incident ? 'inc' : s.kind,
        label: `${set.multi ? `${item.run.tag} · ` : ''}${words(s, level, lang)}`, expected: live && s.start >= now, tail, more: act.length - 1,
      })
    }
  })

  // One plain sentence about the moment.
  const tense = isNow ? 'now' : past ? 'past' : 'fut'
  const was = (de ? { now: 'ist', past: 'war', fut: 'ist dann' } : { now: 'is', past: 'was', fut: 'will be' })[tense]
  const main = sets[0]?.main, parts: string[] = []
  if (main && !rel && incidentAt(main, T)) parts.push(de ? `${titleOf(main, lang)} ${was} live, aber gestört (seit ${timeLabel(data, main.incident!.start)})` : `${titleOf(main, lang)} ${was} live but not working properly (since ${timeLabel(data, main.incident!.start)})`)
  const busy = lines.filter(l => l.kind !== 'done' && l.lane !== 'you').sort((a, b) => Number(a.kind === 'wait') - Number(b.kind === 'wait')).slice(0, 2)
  for (const line of busy) {
    const set = sets.find(s => s.lanes.includes(line.lane))!
    const item = set.items.find(i => i.lane === line.lane && i.step.start <= T + 1e-6 && i.step.end > T + 1e-6)
    if (!item) continue
    const said = plain(item.step, lang)
    parts.push(line.kind === 'wait' ? `${item.run.tag}: ${lower(said)}` : de ? `${actorName(ctx, line.lane)}: „${said}“` : `${actorName(ctx, line.lane)} on “${said}”`)
  }
  if (!rel && idleLanes.length) {
    const names = idleLanes.map(lane => actorName(ctx, lane)).join(', ')
    parts.push(de ? `frei: ${names}` : `${names} ${idleLanes.length > 1 ? (tense === 'past' ? 'were' : 'are') : was} idle`)
  }
  const target = sets.find(s => s.main.isTarget)?.main
  if (rel && target) {
    const end = runEnd(target)
    parts.push(T >= end ? (de ? `das Arion-Ziel war nach ${minutesText(end)} live` : `the Arion target was live after ${minutesText(end)}`) : (de ? 'das Arion-Ziel läuft noch' : 'the Arion target is still running'))
  }
  if (!rel) parts.push(youWaits.length ? (de ? `es wartet auf dich (${youWaits[0]})` : `waiting on you: ${youWaits[0]}`) : text.nothingYou)
  const head = `${put(text.at, { t: timeLabel(data, T) })}${isNow ? ` (${text.now})` : ''}`
  const idle = idleLanes.length || youIdle
    ? [idleLanes.map(lane => actorName(ctx, lane)).join(', '), youIdle && !rel ? text.nothingYou : ''].filter(Boolean).join(' · ')
    : null

  // The right column: the selected step, else the incident at this moment.
  let detail: Moment['detail'] = null, incident: Moment['incident'] = null
  const sel = ctx.selected
  if (sel) {
    const s = sel.step, f = s.facts, norm = f?.norm, dur = s.end - s.start, t = text.terms
    const at = (m: number) => rel ? timeLabel(data, m) : timeLabelSeconds(data, m)
    const range = `${at(s.start)} ${TO} ${at(s.end)}`
    // An open step's end is the expected one (facts.open). Elapsed time stops at now;
    // the expected completion is its own row, never a recorded "took".
    const open = f?.open === true, clockNow = data.now
    const projected = open && clockNow != null && s.end > clockNow + 0.01
    const elapsed = open ? Math.max(0, Math.min(T, clockNow ?? T) - s.start) : dur
    const running = open && (clockNow == null || clockNow + 1e-6 >= s.start)
    const startText = open ? `${at(s.start)}${running ? ` · ${text.ongoing}` : ''}` : range
    const usual = `${norm?.p50 != null ? ` · ${text.usually} ${fmtMin(norm.p50, lang)} min` : ''}${norm?.arion != null ? ` · ${text.arion} ${fmtMin(norm.arion, lang)} min` : ''}`
    const expertNorm = `${norm?.p50 != null ? ` · p50 ${fmtMin(norm.p50, lang)}` : ''}${norm?.p90 != null ? ` · p90 ${fmtMin(norm.p90, lang)}` : ''}${norm?.arion != null ? ` · Arion ${fmtMin(norm.arion, lang)}` : ''}`
    const expectedRow: [string, string] | null = projected ? [t.expectedEnd, at(s.end)] : null
    const round = f?.round && (f.round > 1 || ['review', 'copy_gate', 'pin_gate', 'h', 'ci', 'queue'].includes(s.stepKey ?? '')) ? f.round : null
    const waitsFor = s.kind === 'wait' ? f?.waitsFor ?? (f?.waitReason ? pick(pair(WAIT_OBJECT[f.waitReason]), lang) : null) : null
    const rows: ([string, string] | null)[] = level === 'simple' ? [
      [t.started, startText],
      [open ? t.elapsed : t.took, open ? `${fmtMin(elapsed, lang)} min${usual}` : `${minutesText(dur)}${usual}`],
      expectedRow,
      f?.outcome ? [t.outcome, pick(pair(OUTCOME_PLAIN[f.outcome]), lang)] : null,
      round ? [t.round, String(round)] : null,
      waitsFor ? [t.waitsFor, waitsFor] : null,
    ] : [
      [t.step, `${s.stepKey ?? '–'}${s.side ? ` · ${text.sideWord}` : ''} · ${text.kindWords[s.kind]}${s.incident ? ` · ${text.incidentWord}` : ''}`],
      [t.actor, `${text.actors.expert[s.lane]}${f?.model ? ` · ${f.model}` : ''}`],
      [open ? t.started : t.startEnd, open ? startText : range],
      [open ? t.elapsed : t.duration, `${fmtMin(open ? elapsed : dur, lang)} min${expertNorm}`],
      expectedRow,
      f?.outcome ? [t.outcome, f.outcome] : null,
      round ? [t.round, `r${round}`] : null,
      waitsFor ? [t.waitsFor, waitsFor] : null,
      [t.source, f?.source ? text.sources[f.source] ?? f.source : '–'],
    ]
    detail = { title: words(s, level, lang), tag: sel.run.tag, rows: rows.filter((r): r is [string, string] => !!r) }
  } else if (main && incidentAt(main, T)) {
    const inc = main.incident!
    incident = {
      title: pick(level === 'simple' ? inc.simple : inc.expert, lang),
      meta: [`${text.since} ${rel ? timeLabel(data, inc.start) : timeLabelSeconds(data, inc.start)}`, put(text.soFarMin, { m: fmtMin(T - inc.start, lang) }), inc.open ? '' : put(text.ends, { t: rel ? timeLabel(data, inc.end) : timeLabelSeconds(data, inc.end) })].filter(Boolean).join(' · '),
    }
  }
  return { head, sentence: parts.length ? `${upper(parts.join('; '))}.` : '', lines, idle, detail, incident }
}
const pair = (value: readonly [string, string]): Words => ({ en: value[0], de: value[1] })

// ---------- The current step of a run in Live ----------
export function currentStep(run: FlowRun, now: number): FlowStep | null {
  const id = run.facts?.currentStepId
  const byId = id ? run.steps.find(s => s.facts?.id === id) : undefined
  return byId ?? stepAt(criticalPath(run), now)
}

// ---------- Verdict against the target: shape + word, never colour alone ----------
export interface Verdict { level: 'on' | 'close' | 'far'; word: string; text: string }
export function verdictOf(run: FlowRun, now: number | null, text: FlowText, lang: DeliveryLanguage): Verdict | null {
  const target = run.target
  if (!target) return null
  const eta = run.facts?.eta.p50 ?? null, ended = run.facts?.ended ?? null
  const end = ended ?? eta ?? now
  if (end == null || end <= target.start) return null
  const ratio = (end - target.start) / target.minutes
  const level = ratio <= 1 ? 'on' : ratio <= 1.5 ? 'close' : 'far'
  const k = ratio >= 2 ? Math.round(ratio * 2) / 2 : Math.round(ratio * 10) / 10
  return { level, word: text.verdicts[level], text: put(ended == null && eta == null ? text.alreadyTimes : text.timesTarget, { k: dec(k, lang) }) }
}

// ---------- The in-flight table (Live) ----------
export interface FlightRow {
  id: string; tag: string; title: string
  now: string; who: string; waiting: { text: string; you: boolean }; pct: number | null; expected: string; estimated: boolean; verdict: Verdict | null
  nowX: string; whoX: string; waitX: string; stepX: string; etaX: string
}
const LANE_OBJECT: Record<Lane, readonly [string, string]> = {
  you: ['you', 'dich'], lead: ['LEAD', 'LEAD'], ops: ['OPS', 'OPS'], review: ['the review', 'das Review'], build: ['the build', 'den Build'], ci: ['the checks', 'die Checks'],
}
export function flightRows(ctx: Omit<Ctx, 'level'>): FlightRow[] {
  const { data, lang, text } = ctx, de = lang === 'de', now = data.now ?? 0
  const clock = (m: number | null | undefined) => m == null ? '–' : timeLabel(data, m)
  return (data.sets[0]?.runs ?? []).map(run => {
    const s = currentStep(run, now), f = run.facts
    const inc = incidentAt(run, now) ? run.incident! : null
    const nowWords = s ? words(s, 'simple', lang) : '–'
    const since = s ? `${text.since} ${clock(s.start)}` : ''
    const gate = f?.gate ?? null
    let waitText: string, you = false
    if (s?.kind === 'wait') {
      const reason = s.facts?.waitReason
      if (reason === 'human_gate' || s.lane === 'you') { waitText = `${de ? 'dich' : 'you'}${s.facts?.waitsFor ? `: ${s.facts.waitsFor}` : ''}`; you = true }
      else waitText = s.facts?.waitsFor ?? (reason ? pick(pair(WAIT_OBJECT[reason]), lang) : lower(nowWords))
    } else waitText = s ? pick(pair(LANE_OBJECT[s.lane]), lang) : '–'
    if (gate && !you) { waitText = `${waitText}, ${put(text.thenYou, { g: gate })}`; you = true }
    const eta = f?.eta
    const estimated = eta?.p50 != null
    const expected = estimated ? put(text.expected_[f?.kind ?? 'change'], { t: clock(eta!.p50) }) : text.noEstimate
    const norm = s?.facts?.norm
    return {
      id: run.id, tag: run.tag, title: pick(run.title, lang),
      now: inc ? `${pick(inc.simple, lang)} · ${lower(nowWords)}` : nowWords,
      who: s ? text.actors.simple[s.lane] : '–',
      waiting: { text: waitText, you }, pct: f?.pct ?? null, expected, estimated, verdict: verdictOf(run, now, text, lang),
      nowX: inc ? `${text.incidentWord} ${text.since} ${timeLabelSeconds(data, inc.start)} · ${s ? `${words(s, 'expert', lang)} (${since})` : ''}` : s ? `${words(s, 'expert', lang)} · ${since}` : '–',
      whoX: s ? `${text.actors.expert[s.lane]}${s.facts?.model ? ` · ${s.facts.model}` : ''}` : '–',
      waitX: `${s ? words(s, 'expert', lang) : '–'}${gate ? ` · ${put(text.thenGate, { g: gate })}` : ''}`,
      stepX: s && norm?.p50 != null ? `${clock(s.start + norm.p50)}${norm.p90 != null ? ` · ${clock(s.start + norm.p90)}` : ''}` : '–',
      etaX: estimated ? `${clock(eta!.p50)} · ${clock(eta!.p90)}` : put(text.etaNone, { r: eta?.reason ?? '–' }),
    }
  })
}

// ---------- Where the time went (Replay, Compare) ----------
type WentKind = 'work' | 'wait' | 'rework' | 'inc'
export interface Went {
  key: string; title: string; note: string
  parts: { kind: WentKind; label: string; share: number }[]
  items: { key: string; kind: WentKind; minutes: string; label: string; note: string }[]
}
type Span = { start: number; end: number }
/** Join overlaps and touches. The copy keeps the caller's spans intact. */
function mergeSpans(spans: readonly Span[]): Span[] {
  const sorted = spans.filter(s => s.end > s.start).map(s => ({ start: s.start, end: s.end })).sort((a, b) => a.start - b.start || a.end - b.end)
  const out: Span[] = []
  for (const s of sorted) {
    const last = out.at(-1)
    if (last && s.start <= last.end) last.end = Math.max(last.end, s.end)
    else out.push(s)
  }
  return out
}
/** base with every cut removed. Cuts may overlap; each is applied to what remains. */
function subtractSpans(base: readonly Span[], cuts: readonly Span[]): Span[] {
  let out = base.map(s => ({ start: s.start, end: s.end }))
  for (const c of cuts) {
    const next: Span[] = []
    for (const s of out) {
      if (c.end <= s.start || c.start >= s.end) { next.push(s); continue }
      if (c.start > s.start) next.push({ start: s.start, end: c.start })
      if (c.end < s.end) next.push({ start: c.end, end: s.end })
    }
    out = next
  }
  return out
}
const spanMinutes = (spans: readonly Span[]) => spans.reduce((n, s) => n + s.end - s.start, 0)
/**
 * Minutes of the critical path, each moment once. The catalogue is critical and can run inside step a
 * (and outlast it); summing those durations counts the overlap twice. Same-kind overlaps merge. When two
 * kinds share a moment the more specific one keeps it (incident, then doing it again, then waiting, then
 * working), so the parts are a partition of the covered time. The steps stay whole for the lanes and the record.
 */
function criticalMinutes(path: readonly FlowStep[]): Record<WentKind, number> {
  const buckets: Record<WentKind, Span[]> = { inc: [], rework: [], wait: [], work: [] }
  for (const s of path) {
    const kind: WentKind = s.incident ? 'inc' : s.kind === 'wait' || s.kind === 'rework' ? s.kind : 'work'
    buckets[kind].push({ start: s.start, end: s.end })
  }
  const inc = mergeSpans(buckets.inc)
  const reworkMerged = mergeSpans(buckets.rework)
  const waitMerged = mergeSpans(buckets.wait)
  const rework = subtractSpans(reworkMerged, inc)
  const wait = subtractSpans(waitMerged, [...inc, ...reworkMerged])
  const work = subtractSpans(mergeSpans(buckets.work), [...inc, ...reworkMerged, ...waitMerged])
  return { inc: spanMinutes(inc), rework: spanMinutes(rework), wait: spanMinutes(wait), work: spanMinutes(work) }
}
export function wentOf(run: FlowRun, ctx: Omit<Ctx, 'data'>): Went {
  const { level, lang, text } = ctx
  const path = criticalPath(run), sums = criticalMinutes(path)
  const total = sums.work + sums.wait + sums.rework + sums.inc || 1
  const label: Record<WentKind, string> = { work: text.working, wait: text.waiting, rework: text.again, inc: text.incidentW }
  const parts = (['work', 'wait', 'rework', 'inc'] as const).filter(k => sums[k] > 0).map(kind => ({
    kind, share: sums[kind] / total,
    label: `${label[kind]} ${minutesText(sums[kind])}${level === 'expert' ? ` (${Math.round(sums[kind] / total * 100)}${lang === 'de' ? ' ' : ''}%)` : ''}`,
  }))
  const handovers = path.filter((s, i) => i > 0 && s.lane !== path[i - 1]!.lane).length
  const open = !run.isTarget && run.facts != null && run.facts.ended == null
  const items: Went['items'] = []
  if (run.incident) {
    const inc = run.incident, steps = run.steps.filter(s => s.incident).length
    items.push({ key: 'inc', kind: 'inc', minutes: minutesText(inc.end - inc.start), label: pick(level === 'simple' ? inc.simple : inc.expert, lang), note: inc.open ? text.recovering : put(text.recovered, { n: steps }) })
  }
  run.steps.filter(s => (s.kind === 'wait' || s.kind === 'rework') && !s.after).sort((a, b) => (b.end - b.start) - (a.end - a.start)).slice(0, 4).forEach((s, i) => {
    items.push({ key: `s${i}`, kind: s.kind as WentKind, minutes: minutesText(s.end - s.start), label: words(s, level, lang), note: s.side ? text.alongside : '' })
  })
  const note = [open ? text.soFar : '', `${handovers} ${text.handovers}`].filter(Boolean).join(' · ')
  return { key: run.id, title: pick(run.title, lang), note, parts, items }
}

// ---------- The release record (AEON-1022) ----------
/** The ticket a qualification evidence reference starts with (AEON-487/comment/…), when it names one. */
export const evidenceTicket = (evidence: string): { key: string; rest: string } | null => {
  const m = /^([A-Za-z][A-Za-z0-9]*-\d+)(\/.*)?$/.exec(evidence)
  return m ? { key: m[1]!, rest: m[2] ?? '' } : null
}
export interface RecordRow {
  key: 'catalogue' | 'rehearsal' | 'evidence' | 'rollback'; term: string; value: string
  /** Nothing was reported: the row says so instead of leaving a gap or a default. */
  missing: boolean
  /** The evidence reference split into the ticket it starts with and the rest of the path. */
  ticket?: { key: string; rest: string }
}
export interface ReleaseRecord { title: string; rows: RecordRow[] }

/** The latest attempt of the rehearsal or the catalogue in words: how long it took (or has taken so far), its outcome, the usual length, how many runs. */
function attemptWords(attempt: RecordedAttempt, ctx: Ctx): string {
  const { level, lang, text } = ctx, expert = level === 'expert'
  const parts: string[] = []
  if (attempt.open) parts.push(put(text.rec.running, { t: timeLabel({ origin: attempt.startAt }, 0, ctx.timeZone) }), put(text.rec.soFar, { m: fmtMin(attempt.minutes, lang) }))
  else {
    parts.push(minutesText(attempt.minutes))
    if (attempt.outcome) parts.push(expert ? attempt.outcome : pick(pair(OUTCOME_PLAIN[attempt.outcome]), lang))
  }
  if (attempt.p50 != null) parts.push(expert ? `p50 ${fmtMin(attempt.p50, lang)}${attempt.p90 != null ? ` · p90 ${fmtMin(attempt.p90, lang)}` : ''}` : put(text.rec.usually, { m: fmtMin(attempt.p50, lang) }))
  if (attempt.runs > 1) parts.push(put(text.rec.runs, { n: attempt.runs }))
  return parts.join(' · ')
}

/**
 * What a rollout record reported about a release besides its steps: the full test run (catalogue), the
 * rehearsal, the qualification evidence and the rollback class. Always the same four rows, so the panel does
 * not change shape as facts arrive; a fact nobody reported reads "not recorded", and one the answer may have
 * left out (a window that starts after the release did) says so instead. The timing comes from the run's own
 * facts, not from the lanes, so Compare's slice from step a never hides it. Null for a change, for the Arion
 * target and for test data built without one, which have no record to report.
 */
export function recordOf(run: FlowRun, ctx: Ctx): ReleaseRecord | null {
  const record = run.facts?.record
  if (run.isTarget || run.facts?.kind !== 'release' || !record) return null
  const { level, text } = ctx, terms = text.rec.terms[level]
  const timed = (key: 'catalogue' | 'rehearsal'): RecordRow => {
    const attempt = record[key]
    return { key, term: terms[key], value: attempt ? attemptWords(attempt, ctx) : record.partial ? text.rec.partial : text.rec.none, missing: attempt == null }
  }
  const ticket = record.evidence ? evidenceTicket(record.evidence) : null
  return {
    title: text.rec.title,
    rows: [
      timed('catalogue'), timed('rehearsal'),
      { key: 'evidence', term: terms.evidence, value: record.evidence ?? text.rec.none, missing: record.evidence == null, ...(ticket ? { ticket } : {}) },
      { key: 'rollback', term: terms.rollback, value: record.rollback ? text.rec.rollback[level][record.rollback] : text.rec.none, missing: record.rollback == null },
    ],
  }
}

// ---------- The headline above the card ----------
export interface Head { big: Part[]; small: string }
export function headOf(mode: FlowMode, ctx: Ctx & { reduced: boolean }): Head {
  const { data, level, lang, text } = ctx, de = lang === 'de', simple = level === 'simple'
  const set = data.sets[0]
  if (!set) return { big: [], small: '' }
  const main = set.main, clock = (m: number | null | undefined) => m == null ? '–' : timeLabel(data, m)
  if (mode === 'live') {
    const now = data.now ?? 0
    const say = (run: FlowRun) => {
      const s = currentStep(run, now), f = run.facts, eta = f?.eta
      const inc = incidentAt(run, now) ? run.incident! : null
      if (simple) {
        const state = [inc ? (de ? `live, aber gestört seit ${clock(inc.start)}` : `live but not working properly since ${clock(inc.start)}`) : '', s ? lower(words(s, 'simple', lang)) : ''].filter(Boolean).join(' · ')
        return { state, eta: eta?.p50 != null ? clock(eta.p50) : null, gate: f?.gate ?? null }
      }
      const state = [inc ? `${pick(inc.expert, lang)} ${text.since} ${timeLabelSeconds(data, inc.start)}` : '', s ? `${words(s, 'expert', lang)} ${text.since} ${clock(s.start)}` : '', f?.pct != null ? `${f.pct}${de ? ' ' : ''}%` : ''].filter(Boolean).join(' · ')
      return { state, eta: eta?.p50 != null ? `${clock(eta.p50)} p50 · p90 ${clock(eta.p90)}` : null, gate: f?.gate ?? null, reason: eta?.reason ?? null }
    }
    const first = say(main), others = set.runs.filter(r => r !== main)
    const big: Part[] = []
    if (simple) {
      big.push({ text: `${titleOf(main, lang)}:`, strong: true }, { text: ` ${first.state}` })
      if (first.eta) big.push({ text: de ? ` · ${main.facts?.kind === 'release' ? 'gesund' : 'Merge'} erwartet ` : ` · ${main.facts?.kind === 'release' ? 'healthy' : 'merge'} expected ` }, { text: `~${first.eta}`, strong: true })
      if (first.gate) big.push({ text: de ? ' · dann wartet: ' : ' · then waiting: ' }, { text: de ? 'du' : 'you', strong: true }, { text: ` (${first.gate})` })
      const also = others.map(r => { const o = say(r); return `${titleOf(r, lang)}: ${o.state}${o.eta ? ` (~${o.eta})` : ''}` })
      return { big, small: also.length ? `${de ? 'Außerdem unterwegs' : 'Also in flight'}: ${also.join(' · ')}` : '' }
    }
    big.push({ text: main.tag, strong: true }, { text: ` · ${first.state}` })
    if (first.eta) big.push({ text: ' · ETA ' }, { text: first.eta, strong: true })
    if (first.gate) big.push({ text: ` · ${put(text.thenGate, { g: first.gate })}` })
    const also = others.map(r => { const o = say(r); return `${r.tag}: ${o.state}${o.eta ? `, ETA ${o.eta}` : `, ${de ? 'keine ETA' : 'no ETA'}${o.reason ? ` (${o.reason})` : ''}`}` })
    return { big, small: also.join(' · ') }
  }
  if (mode === 'replay') {
    const path = criticalPath(main), start = path[0]?.start ?? 0, f = main.facts
    const ended = f?.ended != null, end = data.play[1], release = f?.kind === 'release'
    const to = ended ? (de ? (release ? 'live und gesund ' : 'gemergt ') : (release ? 'live and healthy ' : 'merged ')) : ''
    const minute = ctx.reduced ? '' : de ? ' in etwa einer Minute' : ' in about a minute'
    const inc = main.incident
    const big: Part[] = de
      ? [{ text: titleOf(main, lang), strong: true }, { text: ` noch einmal ansehen · ${clock(start)} bis ${to}${clock(end)} (${minutesText(end - start)})${minute}` }]
      : [{ text: 'Replay ' }, { text: titleOf(main, lang), strong: true }, { text: ` · ${clock(start)} to ${to}${clock(end)} (${minutesText(end - start)})${minute}` }]
    const small = simple
      ? inc ? (de ? 'Die Figur bleibt stehen, wo gewartet wurde, und geht im roten Bereich durch die Störung und ihre Behebung.' : 'The figure stops wherever it waited, and walks through the incident and its fix in the red area.')
        : !ended ? (de ? 'Die Figur bleibt stehen, wo gewartet wurde; der Lauf geht noch weiter.' : 'The figure stops wherever it waited; the run is still going.')
          : (de ? 'Die Figur bleibt stehen, wo gewartet wurde.' : 'The figure stops wherever it waited.')
      : `${de ? 'Aufgezeichnete Zeitstempel' : 'Recorded timestamps'}${inc ? `; ${de ? 'Störung' : 'incident'} ${timeLabelSeconds(data, inc.start)}–${timeLabelSeconds(data, inc.end)} ${de ? 'als eigenes Band' : 'drawn as its own band'}` : ''}${!ended ? `; ${de ? 'läuft noch' : 'still running'}` : ''}.`
    return { big, small }
  }
  const target = data.sets[1]?.main
  if (!target) return { big: [], small: '' }
  const a = runEnd(main), b = runEnd(target), k = Math.round(a / b * 10) / 10
  // An open release has not gone live; the number is elapsed time, not a finish.
  const openRelease = !main.isTarget && main.facts?.ended == null
  const big: Part[] = openRelease
    ? (de
      ? [{ text: titleOf(main, lang), strong: true }, { text: ' gegen das ' }, { text: titleOf(target, lang), strong: true }, { text: ', ' }, { text: minutesText(a), strong: true }, { text: ` ${text.soFar} statt ` }, { text: minutesText(b), strong: true }]
      : [{ text: titleOf(main, lang), strong: true }, { text: ' vs. the ' }, { text: titleOf(target, lang), strong: true }, { text: ', ' }, { text: minutesText(a), strong: true }, { text: ` ${text.soFar} instead of ` }, { text: minutesText(b), strong: true }])
    : (de
      ? [{ text: titleOf(main, lang), strong: true }, { text: ' gegen das ' }, { text: titleOf(target, lang), strong: true }, { text: ', von der Queue bis live: ' }, { text: minutesText(a), strong: true }, { text: ' statt ' }, { text: minutesText(b), strong: true }, { text: ` · etwa ${dec(k, lang)}${TIMES} so lang` }]
      : [{ text: titleOf(main, lang), strong: true }, { text: ' vs. the ' }, { text: titleOf(target, lang), strong: true }, { text: ', queue to live: ' }, { text: minutesText(a), strong: true }, { text: ' instead of ' }, { text: minutesText(b), strong: true }, { text: ` · about ${dec(k, lang)}${TIMES} as long` }])
  const path = criticalPath(main)
  const incMinutes = path.filter(s => s.incident).reduce((n, s) => n + s.end - s.start, 0)
  let small: string
  if (simple) {
    const pieces = [
      ...(main.incident ? [{ what: de ? 'die Störung' : 'the incident', minutes: incMinutes }] : []),
      ...path.filter(s => !s.incident && s.kind !== 'work').map(s => ({ what: lower(plain(s, lang)), minutes: s.end - s.start })),
    ].sort((x, y) => y.minutes - x.minutes)
    const biggest = pieces[0]
    small = `${de ? 'Grün ist das Ziel.' : 'Green is the target.'}${biggest && biggest.minutes >= 1 ? de ? ` Der größte einzelne Brocken der Lücke ist ${biggest.what} (${minutesText(biggest.minutes)}).` : ` The biggest single piece of the gap is ${biggest.what} (${minutesText(biggest.minutes)}).` : ''}`
  } else {
    small = openRelease
      ? `a ${TO} l ${text.soFar}: ${minutesText(a)}.`
      : `a ${TO} l ${de ? 'bis live und gesund' : 'until live and healthy'}: ${minutesText(a)}${incMinutes >= 1 ? de ? `; davon ${minutesText(incMinutes)} Störung + Behebung` : `; ${minutesText(incMinutes)} of it incident + recovery` : ''}.`
  }
  return { big, small }
}

// ---------- The bar clock ----------
export function clockOf(mode: FlowMode, ctx: Pick<Ctx, 'data' | 'text'> & { T: number }): string {
  const { data, text, T } = ctx
  if (mode === 'compare') {
    const target = data.sets.find(s => s.main.isTarget)?.main
    return `${timeLabel(data, T)}${target && T >= runEnd(target) ? ` · ${text.targetReached}` : ''}`
  }
  return put(text.inRun, { t: timeLabel(data, T), d: minutesText(Math.max(0, T - data.play[0])) })
}

// ---------- Replay: auto-play once per session, 60 s for the run at 1x ----------
export const PLAY_MS = 60_000
export interface Frames { request(cb: (ts: number) => void): number; cancel(id: number): void }
const browserFrames: Frames = { request: cb => requestAnimationFrame(cb), cancel: id => cancelAnimationFrame(id) }
/** The live preference, read at the moment it matters (a change event can arrive late). */
export const reducedMotionQuery = () => typeof window !== 'undefined' && typeof window.matchMedia === 'function' ? window.matchMedia('(prefers-reduced-motion: reduce)') : null

/** Walks the playhead from p0 to p1 in 60 s at 1x (30 s at 2x), following it while it plays. */
export function createPlayer(t: Timeline, frames: Frames = browserFrames) {
  const state = reactive({ playing: false, speed: 1 as 1 | 2 })
  let id = 0, last = 0
  function tick(ts: number) {
    if (!state.playing) return
    const dt = last ? Math.min(100, Math.max(0, ts - last)) : 16
    last = ts
    t.T = Math.min(t.p1, t.T + dt * state.speed / PLAY_MS * (t.p1 - t.p0))
    followTick(t)
    if (t.T >= t.p1 - 1e-9) { stop(); return }
    id = frames.request(tick)
  }
  function play() {
    if (t.T >= t.p1 - 1e-6 || t.T < t.p0) t.T = t.p0
    t.follow = true
    followTick(t)
    state.playing = true
    last = 0
    id = frames.request(tick)
  }
  function stop() { state.playing = false; if (id) frames.cancel(id); id = 0 }
  return { state, play, stop }
}
export type Player = ReturnType<typeof createPlayer>

/** True the first time in this browser session; later calls (and later page visits) get false. */
export function autoplayOnce(store: Pick<Storage, 'getItem' | 'setItem'> | null, key = 'aeon:delivery-flow:autoplayed') {
  let used = false
  return () => {
    if (used) return false
    used = true
    try {
      if (store?.getItem(key)) return false
      store?.setItem(key, '1')
    } catch { /* storage blocked: once per page */ }
    return true
  }
}
const sessionStore = (() => { try { return typeof sessionStorage === 'undefined' ? null : sessionStorage } catch { return null } })()
/** The page's own: once per session. */
export const takeAutoplay = autoplayOnce(sessionStore)
