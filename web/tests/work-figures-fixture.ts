// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1358: view figures in the AEON-1368 response shape, with the numbers of Tick's approved design r4b (project AIT,
// now = Wed 14 Oct 2026 17:40 CEST). Shared by tests/work-figures.test.ts and tests/work-figures.spec.ts.
import type { ColumnFigures, ProjectFigures } from '../src/lib/api.ts'

export const FIGURES_NOW = Date.parse('2026-10-14T15:40:00Z')
const at = (minutesFromNow: number) => new Date(FIGURES_NOW + minutesFromNow * 60_000).toISOString()
const DAY = 86_400
const column = (rows: number, extra: Partial<ColumnFigures> = {}): ColumnFigures => ({ rows, included: rows, without_value: 0, others: 0, partial: false, ...extra })
const numbers = (count: number, total: number, median: number | null, average: number | null) => ({ count, total, median, average })

// The whole project: 168 rows, 166 leaves (27 closed), 728h over the 139 open-or-finished leaves, 18 without estimate.
// The raw hours are fractional on purpose: the shown group hours (619 · 31 · 35 · 43) come from largest remainder.
export function wholeFigures(options: { marksReleases?: boolean } = {}): ProjectFigures {
  const releases = options.marksReleases ?? true
  const completed = { hours: 618.6, tickets: 118, statuses: { delivered: 28, accepted: 90 } }
  const doneNotLive = { hours: 31.4, tickets: 11, statuses: { done: 11 } }
  return {
    view_key: 'whole', computed_at: at(0),
    scope: { kind: 'whole_project', rows: 168, parents: 2, leaves: 166, project_leaves: 166, closed: 27 },
    marks_releases: releases, total_hours: 728, approximate: true, without_estimate: 18, open_estimated_leaves: 9,
    groups: releases
      ? { completed, done_not_live: doneNotLive, in_progress: { hours: 34.6, tickets: 6, statuses: { in_progress: 5, qa: 1 } }, open: { hours: 43.4, tickets: 4, statuses: { backlog: 1, open: 3 } } }
      : { completed: { hours: 650, tickets: 129, statuses: { done: 129 } }, done_not_live: { hours: 0, tickets: 0, statuses: {} }, in_progress: { hours: 34.6, tickets: 6, statuses: { in_progress: 5, qa: 1 } }, open: { hours: 43.4, tickets: 4, statuses: { backlog: 1, open: 3 } } },
    columns: {
      key: column(168, { parents: 2, leaves: 166, first_key: 'AIT-4', last_key: 'AIT-177' }),
      status: column(166, { values: [{ value: 'accepted', count: 90, hours: 0 }, { value: 'delivered', count: 28, hours: 0 }, { value: 'cancelled', count: 25, hours: 0 }, { value: 'done', count: 11, hours: 0 }, { value: 'in_progress', count: 5, hours: 0 }, { value: 'open', count: 3, hours: 0 }, { value: 'archived', count: 2, hours: 0 }, { value: 'backlog', count: 1, hours: 0 }, { value: 'qa', count: 1, hours: 0 }] }),
      eta_ready: column(166, { running: 5, next_finish: { at: at(2), key: 'AIT-174' }, last_finish: { at: at(210), key: 'AIT-135' }, without_value: 161 }),
      progress: column(166, { included: 139, without_value: 133, numbers: numbers(6, 384, 66, 64), reading_count: 6, weighted_percent: 71.2 }),
      priority: column(166, { values: [{ value: 'medium', count: 85, hours: 0 }, { value: 'high', count: 64, hours: 0 }, { value: 'low', count: 17, hours: 0 }], without_value: 0 }),
      assignee: column(166, { values: [{ value: 'AIT lead', kind: 'agent', count: 126, hours: 0 }, { value: 'Markus Barta', kind: 'person', count: 12, hours: 0 }, { value: 'AIT-105-b4', kind: 'agent', count: 1, hours: 0 }], others: 5, without_value: 22 }),
      estimate: column(166, { included: 139, without_value: 18, numbers: numbers(121, 728, 4, 6.02) }),
      tokens: column(166, { without_value: 22, measured: numbers(48, 1.06e9, 1.6e7, 2.2e7), estimated: numbers(96, 3.14e9, 1.9e7, 3.27e7), typical: { count: 144, median: 1.8e7, average: 2.9e7, approximate: true } }),
      created_at: column(168, { from: '2026-09-07T08:00:00Z', to: at(-100), median_age_seconds: 5.7 * DAY, stale: 1 }),
      updated_at: column(168, { from: '2026-09-12T10:00:00Z', to: at(-8), median_age_seconds: 2.8 * DAY, stale: 0 }),
      epic: column(166, { values: [{ value: 'AIT-12', count: 40, hours: 0 }, { value: 'AIT-3', count: 22, hours: 0 }], others: 9, without_value: 95 }),
      release: column(166, { values: [{ value: '2026.10.7', count: 14, hours: 0 }, { value: '2026.10.6', count: 9, hours: 0 }], others: 85, without_value: 58 }),
      suggested_release: column(166, { values: [], without_value: 166 }),
      tag: column(166, { values: [{ value: 'bug', count: 19, hours: 0 }, { value: 'voice', count: 14, hours: 0 }, { value: 'ci', count: 9, hours: 0 }, { value: 'legal', count: 6, hours: 0 }, { value: 'process-learning', count: 5, hours: 0 }], others: 12, without_value: 101 }),
      cost_unit: column(166, { values: [{ value: 'AIT core', count: 98, hours: 412, measured_cost: 1500, estimated_cost: 400 }], without_value: 68 }),
      model: column(166, { values: [{ value: 'claude-opus-5-5', count: 70, hours: 0 }, { value: 'gpt-6.1-sol', count: 41, hours: 0 }, { value: 'grok-4.7', count: 12, hours: 0 }], others: 3, without_value: 40 }),
    },
  }
}

// "This view · 10 of 166 tickets", ~78h (a search). Nothing closed in it; one ticket without an estimate.
export function viewFigures(): ProjectFigures {
  const whole = wholeFigures()
  return {
    ...whole, view_key: 'q=voice',
    scope: { kind: 'this_view', rows: 10, parents: 0, leaves: 10, project_leaves: 166, closed: 0 },
    total_hours: 78, without_estimate: 1, open_estimated_leaves: 4,
    groups: {
      completed: { hours: 41, tickets: 4, statuses: { accepted: 3, delivered: 1 } }, done_not_live: { hours: 6, tickets: 1, statuses: { done: 1 } },
      in_progress: { hours: 14.5, tickets: 2, statuses: { in_progress: 2 } }, open: { hours: 16.5, tickets: 3, statuses: { open: 3 } },
    },
    columns: {
      ...whole.columns,
      key: column(10, { parents: 0, leaves: 10, first_key: 'AIT-41', last_key: 'AIT-170' }),
      status: column(10, { values: [{ value: 'accepted', count: 3, hours: 0 }, { value: 'open', count: 3, hours: 0 }, { value: 'in_progress', count: 2, hours: 0 }, { value: 'delivered', count: 1, hours: 0 }, { value: 'done', count: 1, hours: 0 }] }),
      estimate: column(10, { without_value: 1, numbers: numbers(9, 78, 7, 8.67) }),
    },
  }
}
