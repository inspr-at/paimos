// SPDX-License-Identifier: AGPL-3.0-only
import type { ReviewMeasure, ReviewRoundNumbers } from '../src/lib/deliveryReviewRounds'
import type { WindowDays } from '../src/lib/deliveryNumbers'

export type ReviewAnswerKind = 'ready' | 'no_data' | 'partial' | 'open_only' | 'rule_trends' | 'rules_first_window'
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
  if (kind === 'rule_trends' || kind === 'rules_first_window') {
    out.rules_cited_most = [
      { rule: 'U4', count: 4, previous_count: 2 },
      { rule: 'U7', count: 3, previous_count: 5 },
      { rule: 'A17', count: 2, previous_count: 2 },
      { rule: 'U10', count: 1, previous_count: 0 },
    ]
    if (kind === 'rules_first_window') {
      out.previous = { ...empty }
      out.rules_cited_most.forEach(rule => { rule.previous_count = 0 })
    }
  }
  return out
}

// Nine completed design-3/gate-2 tickets and one open design-2 ticket.
// Ten rule rows make the phone body scroll while its actions remain pinned.
export function longReviewAnswer(project = 'p-aeon', window: WindowDays = 7): ReviewRoundNumbers {
  const data = reviewAnswer(project, window)
  const rules = ['U1', 'U2', 'U3'].map(rule => ({ rule, count: 2, previous_count: 0 }))
  data.current = { ...data.current, design_mean: 3, gate_mean: 2, design_tickets: 9, gate_tickets: 9, tickets: 10 }
  data.most_rounds = Array.from({ length: 10 }, (_, i) => ({
    ticket_node_id: `long-${i}`, key: `AEON-${1800 + i}`, title: 'A recorded review with its full ticket title available',
    design_rounds: i === 9 ? 2 : 3, gate_rounds: i === 9 ? null : 2, open: i === 9, latest_round: i === 9 ? 2 : 3, rules,
  }))
  data.rules_cited_most = Array.from({ length: 10 }, (_, i) => ({ rule: `U${i + 1}`, count: i < 3 ? 20 : 1, previous_count: 0 }))
    .sort((a, b) => b.count - a.count || Number(a.rule.slice(1)) - Number(b.rule.slice(1)))
  data.weekly = data.weekly.map((week, i) => ({ ...week, ...(i === 0 ? { design_mean: null, gate_mean: null, design_tickets: 0, gate_tickets: 0, tickets: 9, still_open: 9 } : data.current), weeks_total: 1, weeks_covered: 1 }))
  return data
}
