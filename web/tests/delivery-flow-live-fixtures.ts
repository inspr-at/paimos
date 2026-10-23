// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1697: Builder runs reconstructed from harness records on 23 Oct 2026,
// 08:00–11:00 UTC. Starts: start-queue log; closed intervals: harness stopped_at.
// These are worker intervals, not a captured /delivery/flow API answer. Only
// Builder work is known; outcomes, estimates and ticket completion are unknown.
// Capture-active attempts stay open (fix2): 1674/r2 and the four attempts at the
// capture boundary. Earlier stopped attempts retain their recorded intervals;
// an unfinished ticket alone does not establish that its step is still running.
// No review/CI work is inferred.
// Public fixture: synthetic record IDs, no principal/session/host/account data,
// and neutral titles with exactly the original title lengths. Times are intact.
import type { ApiFlow } from '../src/lib/deliveryFlowData'

export const LIVE_HARNESS_NOW = '2026-10-23T10:06:00Z'
const id = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const utc = (clock: string) => `2026-10-23T${clock}Z`

// [ticket number, attempt within this window, start UTC, end UTC or open, title length].
// Every row has the same two source classes; the handover note identifies each
// start-log line and the matching harness stopped_at without session IDs.
const runs = [
  [1674, 1, '08:25:24', '08:56:11.887834', 91],
  [1675, 1, '08:28:56', '08:52:52.997269', 63],
  [1676, 1, '08:32:27', '09:02:21.176302', 52],
  [1677, 1, '08:36:00', '09:25:46.859006', 59],
  [1678, 1, '08:36:33', '09:08:11.730678', 58],
  [1679, 1, '08:37:05', '08:56:45.546593', 71],
  [1680, 1, '08:37:37', '08:58:10.098082', 72],
  [1681, 1, '08:38:09', '09:23:31.24244', 76],
  [1682, 1, '08:38:40', '09:11:07.795626', 59],
  [1683, 1, '08:39:11', '09:38:02.910928', 52],
  [1675, 2, '09:13:44', '09:18:02.036817', 63],
  [1679, 2, '09:17:16', '09:42:04.028543', 71],
  [1680, 2, '09:19:18', '09:44:54.366755', 72],
  [1676, 2, '09:23:50', '09:27:16.249815', 52],
  [1674, 2, '09:24:22', null, 91],
  [1678, 2, '09:28:53', '09:32:19.476435', 58],
  [1682, 2, '09:33:25', null, 59],
  [1676, 3, '09:46:57', null, 52],
  [1677, 2, '09:47:30', '10:03:09.666841', 59],
  [1678, 3, '09:55:07', null, 58],
  [1683, 2, '09:58:09', null, 52],
] as const

/** A fresh ApiFlow projection of the 21 recorded worker intervals on ten tickets. */
export function liveHarnessFlow(): ApiFlow {
  const steps: ApiFlow['steps'] = runs.map(([ticket, round, start, end], k) => ({
    id: id(20_000 + k), item_id: id(ticket), step_key: 'build', round, kind: 'work',
    actor: { type: 'agent', principal_id: null, label: 'Builder', model: 'gpt-6.1-sol' },
    started_at: utc(start), ended_at: end == null ? null : utc(end), outcome: null, wait_reason: null, waits_for: null,
    side: false, source: 'paimos', norm: { p50_min: null, p90_min: null, arion_min: null },
  }))
  const first = runs.filter((run, k) => runs.findIndex(other => other[0] === run[0]) === k)
  return {
    project_id: id(1), now: LIVE_HARNESS_NOW, at: LIVE_HARNESS_NOW,
    from: utc('08:00:00'), to: utc('11:00:00'), truncated: false, incidents: [], steps,
    items: first.map(([ticket, , start, , length]) => ({
      id: id(ticket), kind: 'change', ref: `AEON-${ticket}`,
      title: 'Recorded change '.repeat(Math.ceil(length / 16)).slice(0, length),
      prs: [], started_at: utc(start), ended_at: null, pct_done: 0,
      current_step_id: steps.find(step => step.item_id === id(ticket) && step.ended_at == null)?.id ?? null,
      eta: { p50_at: null, p90_at: null, basis: 'none', reason: null },
      target: null, next_human_gate: null, qualification_evidence: null, rollback_class: null,
    })),
  }
}
