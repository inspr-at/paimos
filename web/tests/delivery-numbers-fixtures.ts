// SPDX-License-Identifier: AGPL-3.0-only
// A deterministic GET /api/projects/{id}/delivery/metrics answer shaped like the
// server's (AEON-993/1001): whole UTC days ending today, coverage and the window
// before per window, weekly (90, 180) and monthly (365) series. Days before a
// source covers them have no samples; nothing is extrapolated.
import type { Page } from '@playwright/test'
import { mockEffectivePermissions } from './authz-fixtures'

export const TODAY = '2026-10-08'
const DAY = 86_400_000
const iso = (time: number) => new Date(time).toISOString().slice(0, 10)
const at = (date: string) => Date.parse(`${date}T00:00:00Z`)
const shift = (date: string, days: number) => iso(at(date) + days * DAY)

type Sample = { n: number; value: number; p50: number; p90: number } | null
interface Spec {
  number: number; key: string; label: string; unit: 'minutes' | 'percent' | 'runs'; from: string
  target: { value: number; direction: 'max' | 'min'; note: string; source: string } | null
  sample: (day: number) => Sample; alwaysPartial?: boolean; aggregate: 'p50' | 'share' | 'mean'
}
// Small repeatable wobble per metric and day.
const wobble = (seed: number, day: number) => { const x = Math.sin(seed * 12.9898 + day * 78.233) * 43758.5453; return x - Math.floor(x) - 0.5 }
const duration = (seed: number, level: number, ratio: number, count: number) => (day: number): Sample => {
  const p50 = Math.round(level * (1 + 0.2 * wobble(seed, day)))
  return { n: Math.max(1, Math.round(count * (1 + 0.4 * wobble(seed + 1, day)))), value: p50, p50, p90: Math.round(p50 * ratio) }
}
const share = (seed: number, level: number, spread: number, count: number) => (day: number): Sample => {
  const value = Math.round(Math.max(0, Math.min(100, level + spread * wobble(seed, day))))
  return { n: Math.max(1, Math.round(count * (1 + 0.3 * wobble(seed + 2, day)))), value, p50: value, p90: value }
}
const arion = (value: number, direction: 'max' | 'min', note = '') => ({ value, direction, note, source: 'Arion' })

export interface MetricsOptions { empty?: boolean; noSource?: boolean; backfill?: 'running' | 'done' }
export function deliveryMetrics(options: MetricsOptions = {}) {
  const gh = '2026-09-11', review = '2026-09-21', merge = '2026-10-05', release = TODAY, nightly = '2026-10-05'
  const specs: Spec[] = [
    { number: 1, key: 'pr_ci_wall', label: 'PR CI duration (wall)', unit: 'minutes', from: gh, target: arion(5, 'max'), aggregate: 'p50', sample: duration(1, 16, 1.9, 30) },
    { number: 2, key: 'queue_run_wall', label: 'Merge-queue run duration (wall)', unit: 'minutes', from: gh, target: arion(7, 'max'), aggregate: 'p50', sample: duration(2, 15, 1.7, 14) },
    { number: 3, key: 'first_attempt_green', label: 'First-attempt green rate', unit: 'percent', from: gh, target: arion(80, 'min', 'D4″'), aggregate: 'share', sample: share(3, 34, 12, 24) },
    { number: 3, key: 'flaked_failures', label: 'Failures that only flaked', unit: 'percent', from: gh, target: arion(5, 'max'), aggregate: 'share', sample: share(4, 24, 8, 16) },
    { number: 4, key: 'time_to_first_green', label: 'Time to first green', unit: 'minutes', from: gh, target: arion(12, 'max'), aggregate: 'p50', sample: duration(5, 55, 9, 7) },
    { number: 5, key: 'pr_open_to_merged', label: 'PR opened → merged', unit: 'minutes', from: gh, target: arion(40, 'max'), aggregate: 'p50', sample: duration(6, 131, 8, 9) },
    { number: 6, key: 'queue_runs_per_pr', label: 'Queue runs per PR', unit: 'runs', from: gh, target: arion(1.1, 'max'), aggregate: 'mean',
      sample: day => ({ n: 9, value: Math.round((1.8 + 0.4 * wobble(7, day)) * 10) / 10, p50: 2, p90: 3 }) },
    { number: 7, key: 'review_time', label: 'Review time', unit: 'minutes', from: review, target: arion(8, 'max'), aggregate: 'p50', alwaysPartial: true, sample: duration(8, 13, 1.8, 2) },
    { number: 7, key: 'review_changes_share', label: 'Share of “changes” verdicts', unit: 'percent', from: review, target: arion(25, 'max'), aggregate: 'share', alwaysPartial: true, sample: share(9, 58, 20, 2) },
    { number: 8, key: 'merge_rounds_model_share', label: 'Merge rounds solved by a model', unit: 'percent', from: merge, target: arion(20, 'max'), aggregate: 'share',
      sample: day => { const report = ({ [-3]: [3, 100], [-2]: [2, 100], [-1]: [3, 67], [0]: [2, 50] } as Record<number, [number, number]>)[day]; return report ? { n: report[0], value: report[1], p50: report[1], p90: report[1] } : null } },
    { number: 9, key: 'release_queue_to_live', label: 'Release: merge queue → live', unit: 'minutes', from: release, target: arion(24, 'max'), aggregate: 'p50',
      sample: day => day === 0 ? { n: 1, value: 128, p50: 128, p90: 128 } : null },
    { number: 10, key: 'nightly_green', label: 'Nightly full run green', unit: 'percent', from: nightly, target: arion(100, 'min'), aggregate: 'share',
      sample: day => day >= -3 ? { n: 1, value: 0, p50: 0, p90: 0 } : null },
  ]
  const dayIndex = (date: string) => Math.round((at(date) - at(TODAY)) / DAY)
  const sampleOn = (spec: Spec, date: string): Sample => options.empty || date < spec.from ? null : spec.sample(dayIndex(date))
  const measure = (spec: Spec, first: string, last: string) => {
    let n = 0, weighted = 0, p50 = 0, p90 = 0, days = 0
    for (let date = first; date <= last; date = shift(date, 1)) {
      const sample = sampleOn(spec, date)
      if (!sample) continue
      n += sample.n; weighted += sample.value * sample.n; p50 += sample.p50; p90 += sample.p90; days++
    }
    const round = (value: number) => Math.round(value * 10) / 10
    const status = n === 0 ? 'no_data' : spec.alwaysPartial || first < spec.from ? 'partial' : 'ok'
    return { status, n, value: n ? round(weighted / n) : null, p50: days ? round(p50 / days) : null, p90: days ? round(p90 / days) : null }
  }
  const unitOf = (days: number) => days <= 30 ? 'day' : days <= 180 ? 'week' : 'month'
  const bucketsOf = (days: number, first: string, last: string) => {
    const unit = unitOf(days), out: { from: string; to: string }[] = []
    if (unit === 'day') for (let date = first; date <= last; date = shift(date, 1)) out.push({ from: date, to: date })
    else if (unit === 'week') for (let end = last; end >= first; end = shift(end, -7)) out.unshift({ from: shift(end, -6) < first ? first : shift(end, -6), to: end })
    else {
      let start = `${first.slice(0, 7)}-01`
      while (start <= last) {
        const next = iso(Date.UTC(Number(start.slice(0, 4)), Number(start.slice(5, 7)), 1))
        out.push({ from: start < first ? first : start, to: shift(next, -1) > last ? last : shift(next, -1) })
        start = next
      }
    }
    return out
  }
  const coverage = (spec: Spec, days: number, first: string, last: string) => {
    const from = options.empty ? null : spec.from
    const buckets = bucketsOf(days, first, last)
    const coveredDays = from ? Math.max(0, dayIndex(last) - dayIndex(from > first ? from : first) + 1) : 0
    return { window_days: days, bucket: unitOf(days), buckets_total: buckets.length, buckets_covered: from ? buckets.filter(bucket => bucket.to >= from).length : 0,
      covered_days: from && from <= last ? coveredDays : 0, from: from && from <= last ? (from > first ? from : first) : null, full: !!from && from <= first }
  }
  const metrics = specs.map(spec => {
    const windows = [7, 30, 90, 180, 365].map(days => {
      const first = shift(TODAY, -(days - 1)), beforeLast = shift(first, -1), beforeFirst = shift(first, -days)
      return { days, ...measure(spec, first, TODAY), coverage: coverage(spec, days, first, TODAY), previous: { ...measure(spec, beforeFirst, beforeLast), coverage: coverage(spec, days, beforeFirst, beforeLast) } }
    })
    const daily = bucketsOf(30, shift(TODAY, -29), TODAY).map(bucket => ({ date: bucket.from, ...measure(spec, bucket.from, bucket.to) }))
    const series = [90, 180, 365].flatMap(days => bucketsOf(days, shift(TODAY, -(days - 1)), TODAY).map(bucket => ({ days, ...bucket, bucket: unitOf(days), ...measure(spec, bucket.from, bucket.to) })))
    const thirty = windows[1]
    return {
      number: spec.number, key: spec.key, label: spec.label, unit: spec.unit, source: 'fixture', definition: spec.label,
      status: thirty.status, reason: thirty.status === 'partial' ? (spec.alwaysPartial ? 'Only PRs with an aeon/review status count.' : `Facts are complete only since ${spec.from}.`) : thirty.status === 'no_data' ? 'Nothing yet.' : null,
      latest: null, windows, daily, series, target: spec.target,
      ...(spec.key === 'release_queue_to_live' ? { releases: options.empty ? [] : [{ key: '126', release: '126', tag: 'v261008161926.0.0', outcome: 'live', queued_at: '2026-10-08T17:42:00Z', live_at: '2026-10-08T19:50:00Z', healthy_at: '2026-10-08T20:36:00Z' }] } : {}),
    }
  })
  return {
    project_id: 'p-aeon', generated_at: '2026-10-08T17:52:00Z',
    source: options.noSource ? null : { repository: 'inspr-at/aeon', ci_workflow: '.github/workflows/ci.yml', nightly_workflow: '.github/workflows/nightly-full.yml', app_connected: true,
      backfill: options.backfill ?? 'done', backfill_since: '2026-09-11T00:00:00Z', backfill_done_at: '2026-10-08T12:00:00Z', covered_since: '2026-09-11T00:00:00Z' },
    metrics: options.noSource ? [] : metrics,
  }
}

/** Grants delivery.read on top of the member fixture and answers the metrics read. Install after mockWork. */
export async function mockDelivery(page: Page, answer: () => { status: number; body: unknown } | Promise<{ status: number; body: unknown }>, permissions: string[] = ['delivery.read']) {
  await page.route('**/api/me/permissions*', route => {
    const answer = mockEffectivePermissions('member', new URL(route.request().url()).searchParams.get('project_id') ?? undefined)
    answer.workspace.permissions.push(...permissions)
    answer.project?.permissions.push(...permissions)
    return route.fulfill({ json: answer })
  })
  await page.route('**/api/projects/*/delivery/metrics', async route => {
    const { status, body } = await answer()
    await route.fulfill({ status, json: body })
  })
}
