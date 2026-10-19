// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1493 fixtures: Delivery › Numbers › Building it answers, as the server sends them. Test data only; the
// product ships none. `empty` is a project that has recorded no round yet.
import type { Page } from '@playwright/test'
import type { BuildingBucket, BuildingMeasure, BuildingMetric, DeliveryBuilding, RoundOutcome } from '../src/lib/deliveryBuilding'

const DAY = 86_400_000
const none: BuildingMeasure = { n: 0, items: 0, count: 0, value: null, p50: null, p90: null, mean: null }

function bucketsFor(window: number, end: number, fill: (index: number, count: number) => BuildingMeasure | null, state: (index: number) => BuildingBucket['state']): BuildingBucket[] {
  const unit = window <= 30 ? 1 : window <= 180 ? 7 : 30
  const count = Math.ceil(window / unit), today = Date.UTC(new Date(end).getUTCFullYear(), new Date(end).getUTCMonth(), new Date(end).getUTCDate())
  return Array.from({ length: count }, (_, index) => {
    const from = today - (count - 1 - index) * unit * DAY
    return { ...(fill(index, count) ?? none), from: new Date(from).toISOString(), to: new Date(Math.min(from + unit * DAY, end)).toISOString(), state: state(index) }
  })
}

const WORST: Record<BuildingMetric['key'], { key: string; title: string; value: number; rounds: RoundOutcome[] }[]> = {
  first_try: [
    { key: 'AEON-418', title: 'Chat composer keeps the draft on reload', value: 5, rounds: ['failed', 'changes', 'merge_forward', 'changes', 'passed'] },
    { key: 'AEON-402', title: 'Export a board as CSV with the visible columns and the filters that were applied', value: 5, rounds: ['changes', 'failed', 'changes', 'merge_forward', 'passed'] },
    { key: 'AEON-431', title: 'Search keeps its filters after going back', value: 4, rounds: ['failed', 'failed', 'changes', 'passed'] },
    { key: 'AEON-397', title: 'Upload shows progress for large files', value: 4, rounds: ['changes', 'merge_forward', 'changes', 'passed'] },
    { key: 'AEON-425', title: 'Settings save when a field loses focus', value: 4, rounds: ['failed', 'changes', 'merge_forward', 'passed'] },
  ],
  rounds: [],
  idle: [
    { key: 'AEON-409', title: 'Notifications group by ticket', value: 130, rounds: ['changes', 'failed', 'passed'] },
    { key: 'AEON-418', title: 'Chat composer keeps the draft on reload', value: 115, rounds: ['failed', 'changes', 'merge_forward', 'changes', 'passed'] },
    { key: 'AEON-436', title: 'Keyboard shortcut to jump to search', value: 100, rounds: ['changes', 'passed'] },
    { key: 'AEON-402', title: 'Export a board as CSV with the visible columns and the filters that were applied', value: 85, rounds: ['changes', 'failed', 'changes', 'merge_forward', 'passed'] },
    { key: 'AEON-391', title: 'Date picker starts the week on Monday', value: 70, rounds: ['none', 'passed'] },
  ],
  lead: [
    { key: 'AEON-402', title: 'Export a board as CSV with the visible columns and the filters that were applied', value: 1860, rounds: ['changes', 'failed', 'changes', 'merge_forward', 'passed'] },
    { key: 'AEON-418', title: 'Chat composer keeps the draft on reload', value: 1620, rounds: ['failed', 'changes', 'merge_forward', 'changes', 'passed'] },
    { key: 'AEON-409', title: 'Notifications group by ticket', value: 1440, rounds: ['changes', 'failed', 'passed'] },
    { key: 'AEON-431', title: 'Search keeps its filters after going back', value: 1320, rounds: ['failed', 'failed', 'changes', 'passed'] },
    { key: 'AEON-397', title: 'Upload shows progress for large files', value: 1140, rounds: ['changes', 'merge_forward', 'changes', 'passed'] },
  ],
}
WORST.rounds = WORST.first_try

/** One answer for the project and window asked for. `since` is the first recorded round; before it every day is no data. */
export function buildingAnswer(projectId: string, window: number, options: { empty?: boolean; now?: number; since?: string } = {}): DeliveryBuilding {
  const end = options.now ?? Date.now()
  const since = options.empty ? null : options.since ?? new Date(end - 40 * DAY).toISOString()
  const sinceAt = since ? Date.parse(since) : Infinity
  const state = (count: number) => (index: number): BuildingBucket['state'] => {
    const unit = window <= 30 ? 1 : window <= 180 ? 7 : 30, today = Date.UTC(new Date(end).getUTCFullYear(), new Date(end).getUTCMonth(), new Date(end).getUTCDate())
    const from = today - (count - 1 - index) * unit * DAY, to = Math.min(from + unit * DAY, end)
    return to <= sinceAt ? 'no_data' : from < sinceAt ? 'partial' : 'observed'
  }
  const unit = window <= 30 ? 1 : window <= 180 ? 7 : 30, count = Math.ceil(window / unit)
  const metric = (key: BuildingMetric['key'], current: BuildingMeasure, previous: BuildingMeasure, change: BuildingMetric['change'], verdict: BuildingMetric['verdict'], target: BuildingMetric['target'], daily: (index: number) => BuildingMeasure | null): BuildingMetric => ({
    key, unit: key === 'first_try' ? 'percent' : key === 'rounds' ? 'rounds' : 'minutes', better: key === 'first_try' ? 'higher' : 'lower', target,
    current: options.empty ? none : current, previous: options.empty ? none : previous,
    change: options.empty ? { value: null, trend: 'none' } : change, verdict: options.empty ? { level: 'none', ratio: null } : verdict,
    buckets: bucketsFor(window, end, index => options.empty || state(count)(index) === 'no_data' ? null : daily(index), state(count)),
    worst: options.empty ? [] : WORST[key].map((row, index) => ({ node_id: `00000000-0000-4000-8000-0000000004${String(10 + index)}`, ...row, more_rounds: 0 })),
  })
  const items = [3, 4, 4, 3, 3, 2, 2]
  return {
    project_id: projectId, generated_at: new Date(end).toISOString(), window, since,
    coverage: { items: options.empty ? 0 : 21, without_rounds: options.empty ? 0 : 2, partial: !since || Date.parse(since) > end - window * DAY },
    metrics: [
      metric('first_try', { n: 21, items: 21, count: 6, value: 28.6, p50: null, p90: null, mean: null }, { n: 18, items: 18, count: 6, value: 33.3, p50: null, p90: null, mean: null },
        { value: -4.7, trend: 'worse' }, { level: 'far', ratio: 2.1 }, { value: 60, direction: 'min', source: 'delivery_settings' },
        index => { const n = items[index % 7]; const k = index % 3 ? 1 : 0; return { n, items: n, count: k, value: Math.round(1000 * k / n) / 10, p50: null, p90: null, mean: null } }),
      metric('rounds', { n: 21, items: 21, count: 76, value: 3.2, p50: 3.2, p90: 6, mean: 3.6 }, { n: 18, items: 18, count: 52, value: 2.9, p50: 2.9, p90: 5, mean: 2.9 },
        { value: 0.3, trend: 'worse' }, { level: 'far', ratio: 2 }, { value: 1.6, direction: 'max', source: 'delivery_settings' },
        index => { const n = items[index % 7]; return { n, items: n, count: n * 3, value: 3 + (index % 2) * .5, p50: 3 + (index % 2) * .5, p90: 5 + (index % 3), mean: 3.3 } }),
      metric('idle', { n: 76, items: 21, count: 0, value: 22, p50: 22, p90: 100, mean: null }, { n: 60, items: 18, count: 0, value: 31, p50: 31, p90: 120, mean: null },
        { value: -9, trend: 'better' }, { level: 'close', ratio: 1.47 }, { value: 15, direction: 'max', source: 'delivery_settings' },
        index => { const n = 10 + index % 5; return { n, items: items[index % 7], count: 0, value: 18 + (index % 4) * 3, p50: 18 + (index % 4) * 3, p90: 70 + (index % 3) * 15, mean: null } }),
      metric('lead', { n: 21, items: 21, count: 0, value: 310, p50: 310, p90: 1140, mean: null }, { n: 18, items: 18, count: 0, value: 380, p50: 380, p90: 1300, mean: null },
        { value: -70, trend: 'better' }, { level: 'none', ratio: null }, null,
        index => { const n = items[index % 7]; return { n, items: n, count: 0, value: 280 + (index % 3) * 20, p50: 280 + (index % 3) * 20, p90: 800 + (index % 4) * 60, mean: null } }),
    ],
  }
}

/** Answers Building it for any project and window; `answer` decides per read (status and body). */
export async function mockBuilding(page: Page, answer: (projectId: string, window: number) => { status: number; body: unknown } | Promise<{ status: number; body: unknown }>) {
  await page.route('**/api/projects/*/delivery/building?*', async route => {
    const url = new URL(route.request().url()), projectId = decodeURIComponent(url.pathname.split('/')[3]), window = Number(url.searchParams.get('window') ?? 7)
    const { status, body } = await answer(projectId, window)
    await route.fulfill({ status, json: body })
  })
}
