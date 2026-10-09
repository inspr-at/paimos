// SPDX-License-Identifier: AGPL-3.0-only
// Delivery › Flow data (AEON-994 draft 5, package 6): the recorded runs from
// GET /delivery/flow and /delivery/flow/runs/{itemId} turned into lanes, and the three
// modes built from them — Live (runs moving now), Replay (one whole run) and Compare
// (a release against the Arion target on one relative axis, aligned at step a).
// Times are minutes after `origin` (local midnight of the earliest moment read).
import { api, APIError } from './api'
import type { DeliveryLanguage } from './delivery'
import { criticalPath, LANES, runEnd, type FlowData, type FlowIncident, type FlowRun, type FlowStep, type Lane, type RecordedAttempt, type StepOutcome, type WaitReason, type Words } from './deliveryFlow'
import { TO } from './deliveryFlowText'
import { incidentWords, stepWords } from './deliveryFlowWords'

// ---------- The contract (api/areas/projects-delivery-flow.yaml) ----------
export interface ApiActor { type: 'person' | 'agent' | 'ci' | 'queue'; principal_id: string | null; label: string; model: string | null }
export interface ApiStep {
  id: string; item_id: string; step_key: string; round: number; kind: 'work' | 'wait' | 'rework' | 'recovery'; actor: ApiActor
  started_at: string; ended_at: string | null; outcome: StepOutcome | null; wait_reason: WaitReason | null; waits_for: string | null
  side: boolean; source: 'github_app' | 'paimos' | 'ops_rollout'; norm: { p50_min: number | null; p90_min: number | null; arion_min: number | null }
}
export interface ApiIncident { id: string; item_id: string; started_at: string; ended_at: string | null; severity: 'degraded' | 'down'; summary: string; recovery_step_ids: string[] }
export type RollbackClass = 'digest_safe' | 'restore_required'
export interface ApiItem {
  id: string; kind: 'release' | 'change'; ref: string; title: string; prs: number[]; started_at: string | null; ended_at: string | null
  pct_done: number; current_step_id: string | null
  eta: { p50_at: string | null; p90_at: string | null; basis: 'history' | 'ops' | 'none'; reason: string | null }
  target: { minutes: number; from_step: string; source: string } | null
  next_human_gate: { principal_id: string | null; what: string } | null
  /** The release record (AEON-1022): null until a rollout record reports it. A server older than this field omits both. */
  qualification_evidence?: string | null
  rollback_class?: RollbackClass | null
}
export interface ApiFlow { project_id: string; now: string; at: string; from: string; to: string; items: ApiItem[]; steps: ApiStep[]; incidents: ApiIncident[]; truncated: boolean }
export interface ApiRun { project_id: string; now: string; at: string; item: ApiItem; steps: ApiStep[]; incidents: ApiIncident[]; truncated: boolean }

async function getJSON<T>(path: string, signal: AbortSignal | undefined, valid: (body: unknown) => boolean): Promise<T> {
  const response = await api(path, { signal })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' && data.error ? data.error : `Request failed (${response.status})`)
  }
  const body = await response.json()
  if (!valid(body)) throw new APIError(response.status, 'Invalid delivery flow response')
  return body as T
}
const base = (projectId: string) => `/projects/${encodeURIComponent(projectId)}/delivery/flow`
/** Runs active between from and to (at most 31 days; 50 runs, 4000 steps). */
export function readFlow(projectId: string, range: { from: Date; to?: Date }, signal?: AbortSignal): Promise<ApiFlow> {
  const query = new URLSearchParams({ from: range.from.toISOString() })
  if (range.to) query.set('to', range.to.toISOString())
  return getJSON<ApiFlow>(`${base(projectId)}?${query}`, signal, b => !!b && Array.isArray((b as ApiFlow).items) && Array.isArray((b as ApiFlow).steps) && Array.isArray((b as ApiFlow).incidents))
}
/** One whole run for Replay and Compare. */
export function readFlowRun(projectId: string, itemId: string, signal?: AbortSignal): Promise<ApiRun> {
  return getJSON<ApiRun>(`/projects/${encodeURIComponent(projectId)}/delivery/flow/runs/${encodeURIComponent(itemId)}`, signal, b => !!b && !!(b as ApiRun).item && Array.isArray((b as ApiRun).steps))
}
export const flowStreamURL = (projectId: string) => `/api${base(projectId)}/stream`
export const FLOW_HINTS = ['delivery.step', 'delivery.item', 'delivery.incident'] as const
export interface FlowHint { id: number; type: string; item_id: string }
export function parseHint(data: string): FlowHint | null {
  try {
    const hint = JSON.parse(data)
    return hint && typeof hint.item_id === 'string' && typeof hint.type === 'string' ? { id: Number(hint.id) || 0, type: hint.type, item_id: hint.item_id } : null
  } catch { return null }
}

// ---------- Recorded runs ----------
export interface Recorded { origin: number; now: number; runs: FlowRun[]; truncated: boolean }

/** Lanes are actors: a person is "you", machines are the checks, agents by their role. */
export function laneOf(actor: ApiActor, key: string): Lane {
  if (actor.type === 'person') return 'you'
  if (actor.type === 'ci' || actor.type === 'queue') return 'ci'
  const label = actor.label.toLowerCase()
  if (/\blead\b/.test(label)) return 'lead'
  if (/\bops\b/.test(label)) return 'ops'
  if (/review|gate/.test(label)) return 'review'
  if (/build/.test(label)) return 'build'
  if (/check|\bci\b|queue/.test(label)) return 'ci'
  const byKey: Record<string, Lane> = { build: 'build', merge_round: 'build', review: 'review', copy_gate: 'review', pin_gate: 'review', h: 'review', ci: 'ci', queue: 'ci', a: 'ci', c: 'ci', f: 'ci', i: 'ci', rehearsal: 'ci', catalogue: 'ci', hold: 'lead' }
  return byKey[key] ?? 'ops'
}
/** The short tag on bars and the avatar: a release label, or a ticket number. */
export const runTag = (item: Pick<ApiItem, 'kind' | 'ref'>) => item.kind === 'release' ? item.ref : item.ref.replace(/^[A-Za-z]+-(?=\d)/, '')
export function runTitle(item: Pick<ApiItem, 'kind' | 'ref' | 'title'>): Words {
  const title = item.title.trim()
  if (item.kind === 'release') return title ? { en: title, de: title } : { en: `Release ${item.ref}`, de: `Release ${item.ref}` }
  const text = !title || title.startsWith(item.ref) ? title || item.ref : `${item.ref} · ${title}`
  return { en: text, de: text }
}

/**
 * Recorded runs on one minute axis. An open step ends now; with `extendOpen` (Live) it
 * runs on to its expected end (its usual length, or the estimate of the run a dependency
 * waits for; at least a minute past now), drawn as expected past now. A wait (or a person's step) that
 * starts once the run has ended — the next human gate — is drawn as after the run.
 */
/** The latest attempt of one step key (waits are not attempts), measured from the answer's own clock. */
function attemptOf(steps: readonly ApiStep[], key: string, nowMs: number): RecordedAttempt | null {
  const own = steps.filter(s => s.step_key === key && s.kind !== 'wait').sort((a, b) => Date.parse(a.started_at) - Date.parse(b.started_at))
  const last = own.at(-1)
  if (!last) return null
  const startAt = Date.parse(last.started_at), open = last.ended_at == null
  return { startAt, minutes: Math.max(0, ((open ? nowMs : Date.parse(last.ended_at!)) - startAt) / 60_000), open, outcome: last.outcome, p50: last.norm.p50_min, p90: last.norm.p90_min, runs: own.length }
}

export function recordedRuns(answer: { now: string; from?: string; items: ApiItem[]; steps: ApiStep[]; incidents: ApiIncident[]; truncated: boolean }, { extendOpen }: { extendOpen: boolean }): Recorded {
  const nowMs = Date.parse(answer.now)
  const earliest = Math.min(nowMs, ...answer.steps.map(s => Date.parse(s.started_at)), ...answer.items.flatMap(i => i.started_at ? [Date.parse(i.started_at)] : []))
  const day = new Date(Number.isFinite(earliest) ? earliest : nowMs)
  const origin = new Date(day.getFullYear(), day.getMonth(), day.getDate()).getTime()
  const at = (iso: string) => (Date.parse(iso) - origin) / 60_000
  const now = at(answer.now)
  const refs = new Map(answer.items.map(item => [item.id, item.ref]))
  const etas = new Map(answer.items.flatMap(item => item.eta.p50_at ? [[item.id, at(item.eta.p50_at)] as const] : []))
  // The list read keeps only the steps that touch its window: a run that began before it may have lost early steps.
  const windowStart = answer.from ? Date.parse(answer.from) : null
  const runs: FlowRun[] = []
  for (const item of answer.items) {
    const ended = item.ended_at ? at(item.ended_at) : null
    const incidents = answer.incidents.filter(i => i.item_id === item.id).sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))
    const recovery = new Set(incidents.flatMap(i => i.recovery_step_ids))
    const gate = item.next_human_gate?.what ?? null
    const own = answer.steps.filter(s => s.item_id === item.id)
    const steps: FlowStep[] = own.map(s => {
      const start = at(s.started_at), open = s.ended_at == null
      let end = open ? Math.max(start, now) : Math.max(start, at(s.ended_at!))
      if (open && extendOpen) {
        // Expected: the usual length, or for a dependency the awaited run's estimate; at least a minute past now.
        const expected = s.norm.p50_min != null ? start + s.norm.p50_min : s.wait_reason === 'dependency' && s.waits_for ? etas.get(s.waits_for) ?? null : null
        end = Math.max(end, now + 1, expected ?? -Infinity)
      }
      const lane = laneOf(s.actor, s.step_key)
      const waitsFor = s.wait_reason === 'human_gate' ? gate : s.waits_for ? refs.get(s.waits_for) ?? null : null
      const words = stepWords({ key: s.step_key, kind: s.kind, round: s.round, outcome: s.outcome, waitReason: s.wait_reason, waitsFor, model: s.actor.model, open, person: lane === 'you' })
      return {
        start, end, lane, kind: s.kind === 'recovery' ? 'work' : s.kind, ...words, stepKey: s.step_key,
        side: s.side || undefined, incident: s.kind === 'recovery' || recovery.has(s.id) || s.outcome === 'degraded' || undefined,
        after: ended != null && start >= ended - 1e-6 && (s.kind === 'wait' || lane === 'you') || undefined,
        facts: { id: s.id, round: s.round, outcome: s.outcome, waitReason: s.wait_reason, waitsFor, source: s.source, model: s.actor.model, open, norm: { p50: s.norm.p50_min, p90: s.norm.p90_min, arion: s.norm.arion_min } },
      } satisfies FlowStep
    }).sort((a, b) => a.start - b.start)
    let incident: FlowIncident | undefined
    const latest = incidents[0]
    if (latest) {
      const open = latest.ended_at == null, eta = item.eta.p50_at ? at(item.eta.p50_at) : null
      const start = at(latest.started_at)
      incident = { start, end: open ? Math.max(now + (extendOpen ? 1 : 0), extendOpen && eta != null ? eta : now) : at(latest.ended_at!), open, ...incidentWords(latest.severity, latest.summary) }
    }
    const from = item.target ? steps.filter(s => s.stepKey === item.target!.from_step).sort((a, b) => a.start - b.start)[0] : undefined
    runs.push({
      id: item.id, tag: runTag(item), title: runTitle(item), steps, incident,
      target: item.target && from ? { start: from.start, minutes: item.target.minutes } : undefined,
      facts: {
        kind: item.kind, ref: item.ref, started: item.started_at ? at(item.started_at) : null, ended, pct: item.pct_done, currentStepId: item.current_step_id,
        eta: { p50: item.eta.p50_at ? at(item.eta.p50_at) : null, p90: item.eta.p90_at ? at(item.eta.p90_at) : null, basis: item.eta.basis, reason: item.eta.reason },
        gate, record: {
          evidence: item.qualification_evidence ?? null, rollback: item.rollback_class ?? null,
          catalogue: attemptOf(own, 'catalogue', nowMs), rehearsal: attemptOf(own, 'rehearsal', nowMs),
          partial: answer.truncated || (windowStart != null && item.started_at != null && Date.parse(item.started_at) < windowStart),
        },
      },
    })
  }
  return { origin, now, runs, truncated: answer.truncated }
}

// ---------- Live: what is moving now ----------
/** Runs that ended this long ago still show in Live. */
export const LIVE_RECENT = 45
const firstStart = (run: FlowRun) => run.steps.length ? Math.min(...run.steps.map(s => s.start)) : Infinity
/** Releases first, then what is expected to land soonest; runs without an estimate last; then by start. */
export function liveOrder(runs: readonly FlowRun[]): FlowRun[] {
  const eta = (run: FlowRun) => run.facts?.eta.p50 ?? Infinity
  return [...runs].sort((a, b) => Number(a.facts?.kind !== 'release') - Number(b.facts?.kind !== 'release') || eta(a) - eta(b) || firstStart(a) - firstStart(b))
}
export function liveData(rec: Recorded): FlowData | null {
  const runs = liveOrder(rec.runs.filter(run => run.steps.length && (run.facts?.ended == null || run.facts.ended >= rec.now - LIVE_RECENT)))
  if (!runs.length) return null
  const starts = runs.map(firstStart)
  const ends = runs.flatMap(run => [...run.steps.map(s => s.end), run.facts?.eta.p90 ?? -Infinity])
  const r0 = Math.max(rec.now - 6 * 60, Math.min(Math.floor(Math.min(...starts)) - 8, rec.now - 30))
  const r1 = Math.min(rec.now + 4 * 60, Math.ceil(Math.max(rec.now + 16, ...ends)) + 5)
  return { origin: rec.origin, now: rec.now, range: [r0, r1], play: [r0, r1], sets: [{ runs, lanes: LANES, main: runs[0]!, multi: runs.length > 1 }] }
}

// ---------- Replay: one whole run, from its first step to live (or to now) ----------
export function replayData(run: FlowRun, origin: number, now: number | null): FlowData | null {
  const path = criticalPath(run)
  if (!path.length) return null
  const r0 = Math.floor(Math.min(...run.steps.map(s => s.start))) - 3
  const r1 = Math.ceil(Math.max(...run.steps.map(s => s.end))) + 3
  const p0 = path[0]!.start
  const end = run.facts?.ended ?? runEnd(run)
  const p1 = Math.max(p0 + 1, now != null && run.facts?.ended == null ? Math.min(end, now) : end)
  const lanes = LANES.filter(lane => run.steps.some(s => s.lane === lane))
  return { origin, now: null, range: [r0, r1], play: [p0, Math.min(p1, r1)], sets: [{ runs: [run], lanes, main: run }] }
}

// ---------- Compare: a release against the Arion target, aligned at step a ----------
/**
 * The release path a to l as Project Arion v5 § 4b plans it today ("v5 now"), in minutes: 60.6 in all,
 * without the wait for a person (W). The rehearsal and the catalogue run inside segment a (the merge-group
 * run is the longest of the suite, the rehearsal and the catalogue), so they are not segments of their own.
 * § 4b gives h and i as one segment of 6 (model review, pin CI, merge); i is the measured pin-CI p50 of
 * 1.1 (§ 1b), h the rest. When the catalogue is the pole, a release's own step a runs past 15.4.
 */
export const ARION_PATH: readonly (readonly [key: string, minutes: number, lane: Lane, en: string, de: string])[] = [
  ['a', 15.4, 'ci', 'Through the queue', 'Durch die Queue'], ['b', 0.5, 'ops', 'Version tagged', 'Version getaggt'], ['c', 12.2, 'ci', 'Release built', 'Release gebaut'],
  ['d', 0.5, 'ops', 'Draft inspected', 'Entwurf geprüft'], ['e', 10, 'ops', 'Agent app tested', 'Agent-App getestet'], ['f', 1.5, 'ci', 'Published', 'Veröffentlicht'],
  ['g', 0.5, 'ops', 'Server update prepared', 'Server-Update vorbereitet'], ['h', 4.9, 'review', 'Server update reviewed', 'Server-Update geprüft'], ['i', 1.1, 'ci', 'Server config checked and merged', 'Server-Konfiguration geprüft und gemergt'],
  ['j', 3, 'ops', 'Database backed up', 'Datenbank gesichert'], ['k', 6, 'ops', 'Server switched', 'Server umgestellt'], ['l', 5, 'ops', 'Checked live', 'Live geprüft'],
]
export const ARION_MINUTES = ARION_PATH.reduce((sum, step) => sum + step[1], 0)
/** The target run on a relative axis; scaled when the release names another target length. */
export function arionTarget(lang: DeliveryLanguage, minutes = ARION_MINUTES): FlowRun {
  const scale = minutes / ARION_MINUTES
  let t = 0
  const steps = ARION_PATH.map(([key, length, lane, en, de]): FlowStep => {
    const step: FlowStep = { start: t, end: t + length * scale, kind: 'work', lane, stepKey: key, expert: { en: `${key} · ${en}`, de: `${key} · ${de}` }, simple: { en, de }, facts: { source: 'arion', norm: { p50: null, p90: null, arion: length * scale } } }
    t += length * scale
    return step
  })
  return { id: 'arion', tag: lang === 'de' ? 'Ziel' : 'Target', title: { en: 'Arion target', de: 'Arion-Ziel' }, steps, isTarget: true }
}
/** A run from its step a on, shifted so that step a starts at 0; null without a step a. */
export function fromStepA(run: FlowRun): FlowRun | null {
  const first = run.steps.filter(s => s.stepKey === 'a').sort((a, b) => a.start - b.start)[0]
  if (!first) return null
  const a0 = first.start
  const steps = run.steps.filter(s => s.start >= a0 - 1e-6 && !s.after).map(s => ({ ...s, start: s.start - a0, end: s.end - a0 }))
  const inc = run.incident && run.incident.end > a0 ? { ...run.incident, start: Math.max(0, run.incident.start - a0), end: run.incident.end - a0 } : undefined
  return { ...run, id: `${run.id}:a`, steps, incident: inc, target: undefined, title: { en: `${run.title.en} (a ${TO} l)`, de: `${run.title.de} (a ${TO} l)` } }
}
/** The lanes Compare races in, then any other lane a run used. */
export const COMPARE_LANES: readonly Lane[] = ['review', 'ops', 'ci']
export function compareData(release: FlowRun, other: FlowRun): FlowData | null {
  const a = fromStepA(release), b = other.isTarget ? other : fromStepA(other)
  if (!a || !b || !criticalPath(a).length) return null
  const used = new Set([...a.steps, ...b.steps].map(s => s.lane))
  const lanes = [...COMPARE_LANES, ...LANES.filter(lane => !COMPARE_LANES.includes(lane) && used.has(lane))]
  const end = Math.max(runEnd(a), runEnd(b))
  return {
    origin: null, now: null, range: [-2, Math.ceil(end) + 3], play: [0, end],
    sets: [{ title: a.title, runs: [a], lanes, main: a }, { title: b.title, runs: [b], lanes, main: b }],
  }
}
