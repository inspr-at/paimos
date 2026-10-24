// SPDX-License-Identifier: AGPL-3.0-only
import type { ReviewMeasure, ReviewRoundNumbers } from '../src/lib/deliveryReviewRounds'
import type { WindowDays } from '../src/lib/deliveryNumbers'

export type ReviewAnswerKind = 'ready' | 'no_data' | 'partial' | 'open_only'
const empty: ReviewMeasure = { design_mean: null, gate_mean: null, design_tickets: 0, gate_tickets: 0, tickets: 0, still_open: 0, weeks_total: 2, weeks_covered: 0 }

// The known cohort: A design 1/changes, 2/changes, 3/ok and gate 1/changes,
// 2/ok in week 42; B design 1/changes, 2/changes still open; C gate-only
// 1/ok and D design-only 1/ok in week 43. Absent tickets are never samples.
export function reviewAnswer(project = 'p-aeon', window: WindowDays = 7, kind: ReviewAnswerKind = 'ready'): ReviewRoundNumbers {
  const current: ReviewMeasure = { design_mean: 2, gate_mean: 1.5, design_tickets: 2, gate_tickets: 2, tickets: 4, still_open: 1, weeks_total: 2, weeks_covered: 2 }
  const a = { ticket_node_id: 'round-a', key: 'AEON-1718', title: 'Prüfergebnisse bleiben bei langen Bezeichnungen und wechselnden Zahlen vollständig nachvollziehbar', design_rounds: 3, gate_rounds: 2, open: false, latest_round: 3, rules: [{ rule: 'U2', count: 2, previous_count: 1 }, { rule: 'A4', count: 1, previous_count: 0 }] }
  const b = { ticket_node_id: 'round-b', key: 'AEON-1719', title: 'A review still in progress', design_rounds: 2, gate_rounds: null, open: true, latest_round: 2, rules: [{ rule: 'U7', count: 1, previous_count: 0 }] }
  const out: ReviewRoundNumbers = {
    project_id: project, window_days: window, generated_at: '2026-10-24T12:00:00Z', since: '2026-10-01T00:00:00Z', status: 'ok', current,
    previous: { ...current, design_mean: 3, gate_mean: 2, tickets: 2, still_open: 0 },
    weekly: [
      { ...current, from: '2026-10-18T00:00:00Z', to: '2026-10-19T00:00:00Z', week: 42, design_mean: 3, gate_mean: 2, tickets: 1, still_open: 0, design_tickets: 1, gate_tickets: 1, weeks_total: 1, weeks_covered: 1 },
      { ...current, from: '2026-10-19T00:00:00Z', to: '2026-10-24T12:00:00Z', week: 43, design_mean: 1, gate_mean: 1, tickets: 3, still_open: 1, design_tickets: 1, gate_tickets: 1, weeks_total: 1, weeks_covered: 1 },
    ],
    most_rounds: [a, b], rules_cited_most: [{ rule: 'U2', count: 2, previous_count: 1 }, { rule: 'A4', count: 1, previous_count: 0 }, { rule: 'U7', count: 1, previous_count: 0 }],
  }
  if (kind === 'partial') { out.status = 'partial'; out.since = '2026-10-20T00:00:00Z'; out.current.weeks_covered = 1; out.weekly[0]!.weeks_covered = 0 }
  if (kind === 'no_data') { out.status = 'no_data'; out.since = null; out.current = { ...empty }; out.previous = { ...empty }; out.weekly = []; out.most_rounds = []; out.rules_cited_most = [] }
  if (kind === 'open_only') { out.current = { ...current, design_mean: null, gate_mean: null, design_tickets: 0, gate_tickets: 0, tickets: 1, still_open: 1 }; out.previous = { ...empty }; out.most_rounds = [b]; out.weekly.forEach(w => { w.design_mean = null; w.gate_mean = null }) }
  return out
}
