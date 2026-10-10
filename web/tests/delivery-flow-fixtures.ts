// SPDX-License-Identifier: AGPL-3.0-only
// Recorded delivery flow answers shaped like the server's (AEON-1004 contract) for the
// Flow modes (AEON-1006): release 126 during its incident on 8 Oct 2026 and three
// changes in flight, "now" 20:25 Vienna time. Steps carry facts only, never words.
import type { Page, Route } from '@playwright/test'

export const ZONE = 'Europe/Vienna'
const day = '2026-10-08'
export const iso = (hm: string) => `${day}T${hm.length === 5 ? `${hm}:00` : hm}+02:00`
const id = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`
export const RUN = { r126: id(126), c991: id(991), c983: id(983), c993: id(993) }

type Kind = 'work' | 'wait' | 'rework' | 'recovery'
interface StepSpec {
  n: number; item: string; key: string; kind: Kind; actor: 'person' | 'agent' | 'ci' | 'queue'; label: string; from: string; to: string | null
  round?: number; outcome?: string | null; wait?: string | null; waitsFor?: string | null; side?: boolean; model?: string | null; p50?: number | null; p90?: number | null; arion?: number | null
}
const step = (s: StepSpec) => ({
  id: id(10_000 + s.n), item_id: s.item, step_key: s.key, round: s.round ?? 1, kind: s.kind,
  actor: { type: s.actor, principal_id: null, label: s.label, model: s.model ?? null },
  started_at: iso(s.from), ended_at: s.to ? iso(s.to) : null, outcome: s.outcome ?? null, wait_reason: s.wait ?? null, waits_for: s.waitsFor ?? null,
  side: !!s.side, source: s.actor === 'ci' || s.actor === 'queue' ? 'github_app' : s.item === RUN.r126 ? 'ops_rollout' : 'paimos',
  norm: { p50_min: s.p50 ?? null, p90_min: s.p90 ?? null, arion_min: s.arion ?? null },
})
const R = RUN.r126
const releaseSteps = (final: boolean): StepSpec[] => [
  { n: 1, item: R, key: 'copy_gate', kind: 'work', actor: 'agent', label: 'Reviewer', from: '18:21', to: '18:33', outcome: 'changes' },
  { n: 2, item: R, key: 'copy_gate', kind: 'rework', actor: 'agent', label: 'LEAD', from: '18:33', to: '18:51' },
  { n: 3, item: R, key: 'copy_gate', kind: 'wait', actor: 'agent', label: 'Reviewer', from: '18:51', to: '18:57', wait: 'reviewer' },
  { n: 4, item: R, key: 'copy_gate', kind: 'work', actor: 'agent', label: 'Reviewer', from: '18:57', to: '19:05', round: 2, outcome: 'changes' },
  { n: 5, item: R, key: 'copy_gate', kind: 'rework', actor: 'agent', label: 'LEAD', from: '19:05', to: '19:12', round: 2 },
  { n: 6, item: R, key: 'copy_gate', kind: 'wait', actor: 'agent', label: 'Reviewer', from: '19:12', to: '19:18', wait: 'reviewer' },
  { n: 7, item: R, key: 'copy_gate', kind: 'work', actor: 'agent', label: 'Reviewer', from: '19:18', to: '19:25', round: 3, outcome: 'ok' },
  { n: 8, item: R, key: 'ci', kind: 'work', actor: 'ci', label: 'Checks', from: '18:21', to: '18:38', outcome: 'flaky', side: true },
  { n: 9, item: R, key: 'ci', kind: 'wait', actor: 'ci', label: 'Checks', from: '18:38', to: '18:58', wait: 'rerun', side: true },
  { n: 10, item: R, key: 'ci', kind: 'work', actor: 'ci', label: 'Checks', from: '18:58', to: '19:16', round: 2, outcome: 'green', side: true },
  { n: 11, item: R, key: 'a', kind: 'work', actor: 'queue', label: 'Merge queue', from: '19:25', to: '19:30', outcome: 'ok', p50: 15, arion: 3 },
  { n: 12, item: R, key: 'b', kind: 'work', actor: 'ci', label: 'Checks', from: '19:30', to: '19:42', p50: 5, arion: 0.5 },
  { n: 13, item: R, key: 'b', kind: 'work', actor: 'agent', label: 'OPS', from: '19:42', to: '19:44', p50: 5, arion: 0.5 },
  { n: 14, item: R, key: 'c', kind: 'work', actor: 'ci', label: 'Checks', from: '19:44', to: '19:55', p50: 20, arion: 6 },
  { n: 15, item: R, key: 'd', kind: 'work', actor: 'agent', label: 'OPS', from: '19:55', to: '19:59', p50: 5, arion: 0.5 },
  { n: 16, item: R, key: 'e', kind: 'work', actor: 'agent', label: 'OPS', from: '19:57', to: '19:58', outcome: 'ok', side: true },
  { n: 17, item: R, key: 'f', kind: 'work', actor: 'ci', label: 'Checks', from: '19:59', to: '20:03', p50: 5, arion: 1 },
  { n: 18, item: R, key: 'g', kind: 'work', actor: 'agent', label: 'OPS', from: '20:03', to: '20:05', p50: 5, arion: 0.5 },
  { n: 19, item: R, key: 'h', kind: 'work', actor: 'agent', label: 'Reviewer', from: '20:05', to: '20:06', outcome: 'changes', p50: 15, arion: 1 },
  { n: 20, item: R, key: 'h', kind: 'rework', actor: 'agent', label: 'OPS', from: '20:06', to: '20:07' },
  { n: 21, item: R, key: 'h', kind: 'work', actor: 'agent', label: 'Reviewer', from: '20:07', to: '20:09', round: 2, outcome: 'ok', p50: 15, arion: 1 },
  { n: 22, item: R, key: 'i', kind: 'work', actor: 'ci', label: 'Checks', from: '20:10', to: '20:12:17', p50: 15, arion: 3 },
  { n: 23, item: R, key: 'j', kind: 'work', actor: 'agent', label: 'OPS', from: '20:10:16', to: '20:10:40', side: true },
  { n: 24, item: R, key: 'k', kind: 'work', actor: 'agent', label: 'OPS', from: '20:12:26', to: '20:14:27', p50: 10, arion: 3 },
  { n: 25, item: R, key: 'l', kind: 'work', actor: 'agent', label: 'OPS', from: '20:14:27', to: '20:17', outcome: 'degraded', p50: 20, arion: 3 },
  { n: 26, item: R, key: 'mitigation', kind: 'recovery', actor: 'agent', label: 'OPS', from: '20:17', to: '20:21' },
  { n: 27, item: R, key: 'review', kind: 'recovery', actor: 'agent', label: 'Reviewer', from: '20:21', to: '20:24', outcome: 'ok', model: 'Grok 4.7' },
  { n: 28, item: R, key: 'ci', kind: 'recovery', actor: 'ci', label: 'Checks', from: '20:24', to: final ? '20:32' : null, p50: 8, p90: 14, arion: 8 },
  ...(final ? [
    { n: 29, item: R, key: 'mitigation', kind: 'recovery', actor: 'agent', label: 'OPS', from: '20:32', to: '20:33:13' },
    { n: 30, item: R, key: 'switch', kind: 'recovery', actor: 'agent', label: 'OPS', from: '20:33:13', to: '20:34:01' },
    { n: 31, item: R, key: 'l', kind: 'work', actor: 'agent', label: 'OPS', from: '20:34:01', to: '20:35', round: 2, outcome: 'green', p50: 20, arion: 3 },
    { n: 32, item: R, key: 'hold', kind: 'wait', actor: 'person', label: 'Markus', from: '20:35', to: '20:47', wait: 'human_gate' },
  ] as StepSpec[] : []),
]
// What the OPS rollout record adds from release 127 on (AEON-1022): the exact-SHA rehearsal and the full test
// catalogue inside the merge-group run, both alongside the critical path.
const recordSteps = (): StepSpec[] => [
  { n: 33, item: R, key: 'rehearsal', kind: 'work', actor: 'ci', label: 'Checks', from: '19:25', to: '19:36', outcome: 'green', side: true },
  { n: 34, item: R, key: 'catalogue', kind: 'work', actor: 'ci', label: 'Checks', from: '19:26', to: '19:48', outcome: 'green', side: true, p50: 21, p90: 24 },
]
const C1 = RUN.c991, C3 = RUN.c983, C9 = RUN.c993
const changeSteps = (): StepSpec[] => [
  { n: 101, item: C1, key: 'build', kind: 'work', actor: 'agent', label: 'Builder', from: '19:10', to: '19:46', model: 'Opus 5.5', p50: 35, arion: 30 },
  { n: 102, item: C1, key: 'review', kind: 'wait', actor: 'agent', label: 'Reviewer', from: '19:46', to: '19:49', wait: 'reviewer' },
  { n: 103, item: C1, key: 'review', kind: 'work', actor: 'agent', label: 'Reviewer', from: '19:49', to: '19:59', outcome: 'ok', p50: 13, arion: 8 },
  { n: 104, item: C1, key: 'ci', kind: 'work', actor: 'ci', label: 'Checks', from: '19:59', to: '20:15', outcome: 'red', p50: 16, arion: 8 },
  { n: 105, item: C1, key: 'build', kind: 'rework', actor: 'agent', label: 'Builder', from: '20:15', to: '20:19', round: 2 },
  { n: 106, item: C1, key: 'ci', kind: 'work', actor: 'ci', label: 'Checks', from: '20:19', to: null, round: 2, p50: 16, p90: 30, arion: 8 },
  { n: 201, item: C3, key: 'build', kind: 'work', actor: 'agent', label: 'Builder', from: '18:24', to: '19:36', p50: 35, arion: 30 },
  { n: 202, item: C3, key: 'review', kind: 'wait', actor: 'agent', label: 'Reviewer', from: '19:36', to: '19:50', wait: 'reviewer' },
  { n: 203, item: C3, key: 'review', kind: 'work', actor: 'agent', label: 'Reviewer', from: '19:50', to: '20:14', outcome: 'ok', p50: 13, arion: 8 },
  { n: 204, item: C3, key: 'hold', kind: 'wait', actor: 'agent', label: 'LEAD', from: '20:14', to: null, wait: 'dependency', waitsFor: C1 },
  { n: 301, item: C9, key: 'build', kind: 'work', actor: 'agent', label: 'Builder', from: '19:47', to: null, model: 'Opus 5.5', p50: 35, p90: 46, arion: 30 },
]

const eta = (p50: string | null, p90: string | null, reason: string | null = null) => ({ p50_at: p50 ? iso(p50) : null, p90_at: p90 ? iso(p90) : null, basis: p50 ? 'history' : 'none', reason })
/** The release record facts the server always returns: null until a rollout record reports them. */
const noRecord = { qualification_evidence: null, rollback_class: null }
const items = (final: boolean, record = false) => [
  { id: R, kind: 'release', ref: '126', title: 'Release 126', prs: [405, 936, 937], started_at: iso('18:21'), ended_at: final ? iso('20:34:01') : null, pct_done: final ? 100 : 93, current_step_id: final ? null : id(10_028),
    eta: final ? eta(null, null, 'run ended') : eta('20:35', '20:45'), target: { minutes: 24, from_step: 'a', source: 'Arion' }, next_human_gate: { principal_id: null, what: 'release GO' },
    ...(record ? { qualification_evidence: 'AEON-487/comment/native-qualification', rollback_class: 'digest_safe' } : noRecord) },
  { id: C1, kind: 'change', ref: 'AEON-991', title: 'CI fix', prs: [414], started_at: iso('19:10'), ended_at: null, pct_done: 75, current_step_id: id(10_106),
    eta: eta('20:50', '21:08'), target: { minutes: 40, from_step: 'review', source: 'Arion' }, next_human_gate: null, ...noRecord },
  { id: C3, kind: 'change', ref: 'AEON-983', title: '4 changes', prs: [409], started_at: iso('18:24'), ended_at: null, pct_done: 70, current_step_id: id(10_204),
    eta: eta('21:11', '21:40'), target: null, next_human_gate: null, ...noRecord },
  { id: C9, kind: 'change', ref: 'AEON-993', title: 'delivery numbers', prs: [], started_at: iso('19:47'), ended_at: null, pct_done: 0, current_step_id: id(10_301),
    eta: eta(null, null, 'fewer than 3 finished build steps in 30 days'), target: null, next_human_gate: null, ...noRecord },
]
const incident = (final: boolean) => ({ id: id(900), item_id: R, started_at: iso('20:14:31'), ended_at: final ? iso('20:34:01') : null, severity: 'degraded', summary: 'ready 503, DB pool starved', recovery_step_ids: [26, 27, 28, ...(final ? [29, 30] : [])].map(n => id(10_000 + n)) })

/** GET /delivery/flow at `now` (20:25 by default): the runs in flight. */
export function flowAnswer(now = '20:25', record = false) {
  return {
    project_id: id(1), now: iso(now), at: iso(now), from: iso('16:25'), to: iso(now),
    items: items(false, record), steps: [...releaseSteps(false), ...(record ? recordSteps() : []), ...changeSteps()].map(step), incidents: [incident(false)], truncated: false,
  }
}
/** GET /delivery/flow/runs/{itemId}: release 126 is final; the changes are as at 20:25. */
export function runAnswer(itemId: string, record = false) {
  const final = itemId === R
  const all = final ? [...releaseSteps(true), ...(record ? recordSteps() : [])] : changeSteps().filter(s => s.item === itemId)
  const item = items(final, record).find(i => i.id === itemId)!
  return { project_id: id(1), now: iso('20:25'), at: iso('20:25'), item, steps: all.map(step), incidents: final ? [incident(true)] : [], truncated: false }
}
export const emptyFlow = () => ({ project_id: id(1), now: iso('20:25'), at: iso('20:25'), from: iso('19:40'), to: iso('20:25'), items: [], steps: [], incidents: [], truncated: false })

export interface FlowMock {
  /** Every GET /delivery/flow and /runs read, in order. */
  reads: string[]
  /** Sends one server-sent hint on the open stream (the stream answers once, with a long retry). */
  hint(type: 'delivery.step' | 'delivery.item' | 'delivery.incident', itemId: string): void
  setNow(now: string): void
}
/** Mocks the flow reads and the hint stream. The stream waits until a test sends a hint. */
export async function mockFlow(page: Page, options: { empty?: boolean; record?: boolean } = {}): Promise<FlowMock> {
  const reads: string[] = []
  let now = '20:25'
  let release: (frame: string) => void = () => {}
  let waiting = new Promise<string>(resolve => { release = resolve })
  await page.route(/\/api\/projects\/[^/]+\/delivery\/flow(\?|$)/, route => {
    reads.push(new URL(route.request().url()).pathname + new URL(route.request().url()).search)
    return route.fulfill({ json: options.empty ? emptyFlow() : flowAnswer(now, options.record) })
  })
  await page.route(/\/api\/projects\/[^/]+\/delivery\/flow\/runs\/[^/?]+/, route => {
    const itemId = new URL(route.request().url()).pathname.split('/').pop()!
    reads.push(`run:${itemId}`)
    return route.fulfill({ json: runAnswer(itemId, options.record) })
  })
  await page.route(/\/api\/projects\/[^/]+\/delivery\/flow\/stream/, async (route: Route) => {
    const frame = await waiting
    waiting = new Promise<string>(resolve => { release = resolve })
    await route.fulfill({ status: 200, headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' }, body: `retry: 600000\nid: 1\nevent: stream.ready\ndata: {"after":1}\n\n${frame}` })
  })
  let seq = 1
  return {
    reads,
    hint(type, itemId) { seq++; release(`id: ${seq}\nevent: ${type}\ndata: ${JSON.stringify({ id: seq, type, at: iso(now), item_id: itemId, step_id: id(10_028) })}\n\n`) },
    setNow(value) { now = value },
  }
}
