// SPDX-License-Identifier: AGPL-3.0-only
// Example flow (AEON-994 draft 5, package 5): release 126 and the three changes in
// flight with it on 8 Oct 2026, taken from the approved mock (final OPS timeline via
// the LEAD). Live "now" is 20:25, inside the incident. Flow shows it, labelled as an
// example, while a project has no recorded run (package 6); Replay shows release 126
// final and the changes so far, Compare races 126 against the Arion target.
import type { DeliveryLanguage } from './delivery'
import { LANES, type FlowData, type FlowRun, type FlowStep, type Lane, type RunFacts, type StepFacts, type StepKind } from './deliveryFlow'
import { arionTarget, compareData, replayData } from './deliveryFlowData'

const hm = (at: string) => { const [h, m, s = 0] = at.split(':').map(Number); return h! * 60 + m! + s / 60 }
type Extra = Pick<FlowStep, 'side' | 'incident' | 'after' | 'stepKey'>
// step(start, end, kind, lane, expert EN, expert DE, simple EN, simple DE, extra)
const step = (start: string, end: string, kind: StepKind, lane: Lane, xe: string, xd: string, pe: string, pd: string, extra: Extra = {}): FlowStep =>
  ({ start: hm(start), end: hm(end), kind, lane, expert: { en: xe, de: xd }, simple: { en: pe, de: pd }, ...extra })

export const EXAMPLE_NOW = hm('20:25')

const r126: FlowRun = {
  id: 'r126', tag: '126', title: { en: 'Release 126', de: 'Release 126' }, target: { start: hm('19:25'), minutes: 24 },
  incident: { start: hm('20:14:31'), end: hm('20:34:01'), expert: { en: 'DEGRADED · ready 503, DB pool starved', de: 'DEGRADED · ready 503, DB-Pool ausgehungert' }, simple: { en: 'Live, but not working properly', de: 'Live, aber gestört' } },
  steps: [
    step('18:18', '18:21', 'work', 'lead', 'Cut + PR #405', 'Schnitt + PR #405', 'Release cut, PR opened', 'Release geschnitten, PR geöffnet'),
    step('18:21', '18:33', 'work', 'review', 'Copy gate r1 · 15 changes', 'Copy-Gate r1 · 15 Änderungen', 'Release notes checked: 15 changes', 'Release-Notes geprüft: 15 Änderungen'),
    step('18:33', '18:51', 'rework', 'lead', 'Rework · LEAD fixes 15', 'Nacharbeit · LEAD behebt 15', 'Fixing the notes', 'Notes korrigieren'),
    step('18:51', '18:57', 'wait', 'review', 'Wait · copy gate r2', 'Warten · Copy-Gate r2', 'Waiting for the next check', 'Wartet auf die nächste Prüfung'),
    step('18:57', '19:05', 'work', 'review', 'Copy gate r2 · 2 changes', 'Copy-Gate r2 · 2 Änderungen', 'Notes checked again: 2 changes', 'Notes erneut geprüft: 2 Änderungen'),
    step('19:05', '19:12', 'rework', 'lead', 'Rework · 2 fixes', 'Nacharbeit · 2 Fixes', 'Fixing the notes again', 'Notes erneut korrigieren'),
    step('19:12', '19:18', 'wait', 'review', 'Wait · copy gate r3', 'Warten · Copy-Gate r3', 'Waiting for the next check', 'Wartet auf die nächste Prüfung'),
    step('19:18', '19:25', 'work', 'review', 'Copy gate r3 · ok', 'Copy-Gate r3 · ok', 'Notes approved', 'Notes freigegeben'),
    step('18:21', '18:38', 'rework', 'ci', 'PR #405 CI · agentd flake', 'PR #405 CI · agentd-Flake', 'Checks failed on a flaky test', 'Checks an wackeligem Test gescheitert', { side: true }),
    step('18:38', '18:58', 'wait', 'ci', 'Wait · re-run', 'Warten · Wiederholung', 'Waiting for the re-run', 'Wartet auf die Wiederholung', { side: true }),
    step('18:58', '19:16', 'work', 'ci', 'PR #405 CI re-run · green', 'PR #405 CI-Wiederholung · grün', 'Checks re-run: green', 'Checks wiederholt: grün', { side: true }),
    step('19:25', '19:30', 'work', 'ci', 'a · Merge queue #405', 'a · Merge-Queue #405', 'Release change through the queue', 'Release-Änderung durch die Queue', { stepKey: 'a' }),
    step('19:30', '19:42', 'work', 'ci', 'b · Exact-SHA image check', 'b · Exact-SHA-Image-Check', 'Build image checked', 'Build-Image geprüft', { stepKey: 'b' }),
    step('19:42', '19:44', 'work', 'ops', 'b · Tag push 19:43:35', 'b · Tag-Push 19:43:35', 'Version tagged', 'Version getaggt', { stepKey: 'b' }),
    step('19:44', '19:55', 'work', 'ci', 'c · release.yml', 'c · release.yml', 'Release built', 'Release gebaut', { stepKey: 'c' }),
    step('19:55', '19:59', 'work', 'ops', 'd · Draft inspection + attestation', 'd · Entwurf prüfen + Attestierung', 'Release draft inspected', 'Release-Entwurf geprüft', { stepKey: 'd' }),
    step('19:57', '19:58', 'work', 'ops', 'e · agentd qualification · QUALIFIED', 'e · agentd-Qualifizierung · QUALIFIED', 'Agent app tested on a Mac: OK', 'Agent-App auf einem Mac getestet: OK', { side: true, stepKey: 'e' }),
    step('19:59', '20:03', 'work', 'ci', 'f · Publish 20:00 + Homebrew tap', 'f · Veröffentlichen 20:00 + Homebrew-Tap', 'Published', 'Veröffentlicht', { stepKey: 'f' }),
    step('20:03', '20:05', 'work', 'ops', 'g · csb1 pin PR #936', 'g · csb1-Pin-PR #936', 'Server update prepared', 'Server-Update vorbereitet', { stepKey: 'g' }),
    step('20:05', '20:06', 'work', 'review', 'h · Pin gate r1 · changes: facts truncated', 'h · Pin-Gate r1 · Änderungen: Fakten abgeschnitten', 'First review: changes asked', 'Erstes Review: Änderungen verlangt', { stepKey: 'h' }),
    step('20:06', '20:07', 'rework', 'ops', 'Rework · full facts into #936', 'Nacharbeit · vollständige Fakten in #936', 'Fixing the server update', 'Server-Update korrigieren', { stepKey: 'h' }),
    step('20:07', '20:09', 'work', 'review', 'h · Pin gate r2 · ok', 'h · Pin-Gate r2 · ok', 'Second review: OK', 'Zweites Review: OK', { stepKey: 'h' }),
    step('20:09', '20:10', 'work', 'ops', 'Merge #935 (20:09:58)', '#935 gemergt (20:09:58)', 'Server update merged', 'Server-Update gemergt', { stepKey: 'g' }),
    step('20:10', '20:12:17', 'work', 'ci', 'i · #936 CI + merge (20:12:17)', 'i · #936 CI + Merge (20:12:17)', 'Server config checked and merged', 'Server-Konfiguration geprüft und gemergt', { stepKey: 'i' }),
    step('20:10:16', '20:10:40', 'work', 'ops', 'j · DB snapshot', 'j · DB-Snapshot', 'Database backed up', 'Datenbank gesichert', { side: true, stepKey: 'j' }),
    step('20:12:26', '20:14:27', 'work', 'ops', 'k · Switch + 20 migrations', 'k · Switch + 20 Migrationen', 'Server switched to 126', 'Server auf 126 umgestellt', { stepKey: 'k' }),
    step('20:14:27', '20:17', 'work', 'ops', 'l · Live check 1 DEGRADED · restart, no help', 'l · Live-Check 1 DEGRADED · Neustart hilft nicht', 'Problem found; a restart did not help', 'Problem gefunden; Neustart half nicht', { stepKey: 'l', incident: true }),
    step('20:17', '20:21', 'work', 'ops', 'Mitigation PR #937 (pool 20)', 'Gegenmaßnahme PR #937 (Pool 20)', 'Fix written (#937)', 'Fix geschrieben (#937)', { incident: true }),
    step('20:21', '20:24', 'work', 'review', 'Grok gate · ok', 'Grok-Gate · ok', 'Fix reviewed: OK', 'Fix geprüft: OK', { incident: true }),
    step('20:24', '20:32', 'work', 'ci', '#937 CI (until 20:31:59)', '#937 CI (bis 20:31:59)', 'Fix being checked', 'Fix wird geprüft', { incident: true }),
    step('20:32', '20:33:13', 'work', 'ops', '#937 merged (20:32:40)', '#937 gemergt (20:32:40)', 'Fix merged', 'Fix gemergt', { incident: true }),
    step('20:33:13', '20:34:01', 'work', 'ops', 'k · Re-switch', 'k · Erneuter Switch', 'Server switched again', 'Server erneut umgestellt', { incident: true, stepKey: 'k' }),
    step('20:34:01', '20:35', 'work', 'ops', 'l · Live check 2 green · full verify', 'l · Live-Check 2 grün · volle Prüfung', 'Healthy again, fully checked', 'Wieder gesund, voll geprüft', { stepKey: 'l' }),
    step('20:35', '20:47', 'wait', 'you', 'agm1 GO (Markus, after his test)', 'agm1-GO (Markus, nach seinem Test)', 'Waiting for you: agm1 GO', 'Wartet auf dich: agm1-GO', { after: true }),
  ],
}
const c991: FlowRun = {
  id: 'c991', tag: '991', title: { en: 'AEON-991 · CI fix', de: 'AEON-991 · CI-Fix' }, target: { start: hm('19:46'), minutes: 40 },
  steps: [
    step('19:10', '19:46', 'work', 'build', 'Build', 'Build', 'Being built', 'Wird gebaut'),
    step('19:46', '19:49', 'wait', 'review', 'Wait · reviewer', 'Warten · Reviewer', 'Waiting for a reviewer', 'Wartet auf einen Reviewer'),
    step('19:49', '19:59', 'work', 'review', 'Review r1 · ok', 'Review r1 · ok', 'Reviewed: OK', 'Review: OK'),
    step('19:59', '20:15', 'work', 'ci', 'CI run 1 · web shard 12 red', 'CI-Lauf 1 · Web-Shard 12 rot', 'Checks: one part failed', 'Checks: ein Teil rot'),
    step('20:15', '20:19', 'rework', 'build', 'Fix round', 'Fix-Runde', 'Fixing it', 'Wird korrigiert'),
    step('20:19', '20:35', 'work', 'ci', 'CI run 2', 'CI-Lauf 2', 'Checks again', 'Checks erneut'),
    step('20:35', '20:50', 'work', 'ci', 'Merge queue', 'Merge-Queue', 'Through the queue', 'Durch die Queue'),
  ],
}
const c983: FlowRun = {
  id: 'c983', tag: '983–986', title: { en: 'AEON-983–986 · 4 changes', de: 'AEON-983–986 · 4 Änderungen' }, target: { start: hm('19:36'), minutes: 40 },
  steps: [
    step('18:24', '19:36', 'work', 'build', 'Build ×4', 'Build ×4', '4 changes being built', '4 Änderungen werden gebaut'),
    step('19:36', '19:50', 'wait', 'review', 'Wait · reviewer', 'Warten · Reviewer', 'Waiting for a reviewer', 'Wartet auf einen Reviewer'),
    step('19:50', '20:14', 'work', 'review', 'Review ×4 · ok', 'Review ×4 · ok', 'Reviewed: OK', 'Review: OK'),
    step('20:14', '20:50', 'wait', 'lead', 'Held · until AEON-991 merges', 'Angehalten · bis AEON-991 gemergt', 'Waiting for AEON-991 to land first', 'Wartet, bis AEON-991 drin ist'),
    step('20:50', '20:54', 'work', 'build', 'Merge round (rebase)', 'Merge-Runde (Rebase)', 'Updated to the newest main', 'Auf den neuesten main gebracht'),
    step('20:54', '21:11', 'work', 'ci', 'Merge queue', 'Merge-Queue', 'Through the queue', 'Durch die Queue'),
  ],
}
const c993: FlowRun = {
  id: 'c993', tag: '993', title: { en: 'AEON-993 · delivery numbers', de: 'AEON-993 · Lieferzahlen' },
  steps: [
    step('19:47', '20:25', 'work', 'build', 'Build · Opus 5.5 · 38 min, p50 35 · p90 46', 'Build · Opus 5.5 · 38 min, p50 35 · p90 46', 'Being built, a bit longer than usual', 'Wird gebaut, etwas länger als üblich'),
  ],
}

// ---------- Facts the mock derives from the words (usual and target minutes, outcome, round) ----------
const STEP_NORM: Record<string, [number, number]> = { a: [15, 3], b: [5, 0.5], c: [20, 6], d: [5, 0.5], e: [10, 2], f: [5, 1], g: [5, 0.5], h: [15, 1], i: [15, 3], j: [3, 0.5], k: [10, 3], l: [20, 3] }
function normOf(step: FlowStep): [number, number] | null {
  const en = step.expert.en
  if (step.stepKey && STEP_NORM[step.stepKey]) return STEP_NORM[step.stepKey]!
  if (/^Build/.test(en)) return [35, 30]
  if (/^Review/.test(en)) return [13, 8]
  if (/^CI run|CI \(/.test(en)) return [16, 8]
  if (/Merge queue/.test(en)) return [15, 7]
  return null
}
function factsOf(step: FlowStep, source: StepFacts['source']): StepFacts {
  const en = step.expert.en, norm = normOf(step)
  const outcome = /DEGRADED/.test(en) ? 'degraded' : /changes/.test(en) ? 'changes' : /flake/i.test(en) ? 'flaky' : /\bred\b/.test(en) ? 'red' : /green|QUALIFIED|\bok\b/.test(en) ? 'ok' : null
  const m = en.match(/\br(\d)\b|run (\d)|check (\d)/), round = m ? Number(m[1] ?? m[2] ?? m[3]) : undefined
  const waitReason = step.kind !== 'wait' ? null : /reviewer|copy gate/.test(en) ? 'reviewer' : /re-run/.test(en) ? 'rerun' : /Held/.test(en) ? 'dependency' : /GO/.test(en) ? 'human_gate' : null
  const waitsFor = /Held/.test(en) ? 'AEON-991' : /GO/.test(en) ? 'agm1 GO' : null
  return { round, outcome, waitReason, waitsFor, source: step.lane === 'ci' ? 'github_app' : source, norm: norm ? { p50: norm[0], p90: null, arion: norm[1] } : null }
}
const at = (value: string | null) => value == null ? null : hm(value)
function withFacts(run: FlowRun, source: StepFacts['source'], facts: Omit<RunFacts, 'started' | 'eta' | 'currentStepId'> & { eta: [string, string] | null; reason?: string }): FlowRun {
  run.steps.forEach(step => { step.facts = factsOf(step, source) })
  run.facts = {
    kind: facts.kind, ref: facts.ref, started: Math.min(...run.steps.map(s => s.start)), ended: facts.ended, pct: facts.pct, currentStepId: null, gate: facts.gate,
    eta: facts.eta ? { p50: at(facts.eta[0]), p90: at(facts.eta[1]), basis: 'history', reason: null } : { p50: null, p90: null, basis: 'none', reason: facts.reason ?? null },
  }
  return run
}
withFacts(r126, 'ops_rollout', { kind: 'release', ref: '126', ended: null, pct: 93, gate: 'agm1 GO', eta: ['20:35', '20:45'] })
withFacts(c991, 'paimos', { kind: 'change', ref: 'AEON-991', ended: null, pct: 75, gate: null, eta: ['20:50', '21:08'] })
withFacts(c983, 'paimos', { kind: 'change', ref: 'AEON-983', ended: null, pct: 70, gate: null, eta: ['21:11', '21:40'] })
withFacts(c993, 'paimos', { kind: 'change', ref: 'AEON-993', ended: null, pct: null, gate: null, eta: null, reason: 'first build on this model' })
const r126Open = r126.incident!
r126.incident = { ...r126Open, open: true }

export const EXAMPLE_ORIGIN = new Date(2026, 9, 8).getTime()
/** The Live example: four runs in one set of six lanes, release 126 the main one. */
export function exampleLive(): FlowData {
  return {
    origin: EXAMPLE_ORIGIN, now: EXAMPLE_NOW,
    range: [hm('18:10'), hm('21:40')], play: [hm('18:10'), hm('21:40')],
    sets: [{ runs: [r126, c991, c983, c993], lanes: LANES, main: r126, multi: true }],
  }
}
/** Runs Replay offers: release 126 final, the changes so far (cut at now). */
export const EXAMPLE_REPLAY = ['r126', 'c991', 'c983'] as const
const finalRelease = (): FlowRun => ({ ...r126, incident: { ...r126Open, open: false }, facts: { ...r126.facts!, ended: hm('20:34:01'), pct: 100 } })
function soFar(run: FlowRun): FlowRun {
  const steps = run.steps.filter(s => s.start < EXAMPLE_NOW).map(s => s.end > EXAMPLE_NOW ? { ...s, end: EXAMPLE_NOW, facts: { ...s.facts, open: true } } : s)
  return { ...run, steps }
}
export function exampleRun(id: string): FlowRun | null {
  return id === 'r126' ? finalRelease() : id === 'c991' ? soFar(c991) : id === 'c983' ? soFar(c983) : null
}
export function exampleReplay(id: string): FlowData | null {
  const run = exampleRun(id)
  return run ? replayData(run, EXAMPLE_ORIGIN, run.facts?.ended == null ? EXAMPLE_NOW : null) : null
}
/** Compare: release 126 from step a against the Arion target. */
export const exampleCompare = (lang: DeliveryLanguage): FlowData | null => compareData(finalRelease(), arionTarget(lang))
