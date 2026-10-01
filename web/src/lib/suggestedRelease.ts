// SPDX-License-Identifier: AGPL-3.0-only
import type { Release } from './releases.ts'
import { normaliseState } from './work.ts'

export interface ReleaseSuggestion { text: string; kind: 'shipped' | 'planned' | 'empty'; tip: string; version?: string }
export interface SuggestionInput {
  key: string; state: string; kind_slug?: string
  eta?: { eta_ready_at?: string | null; ready_stale?: boolean; eta_stale?: boolean }
  fields: Record<string, unknown>
}
export interface QueueTiming { expected_start?: string | null; estimate_hours?: number | null }
const HOUR = 3_600_000
const instant = (value: unknown) => typeof value === 'string' && Number.isFinite(Date.parse(value)) ? Date.parse(value) : null
const when = (at: number) => new Date(at).toLocaleString('en-GB', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })

/** Measured median, from at most the last ten distinct published release cuts. */
export function releaseCadence(releases: Release[]): { hours: number; latest: number; samples: number } | null {
  const times = [...new Set(releases.filter(r => r.state === 'published').flatMap(r => {
    const at = instant(r.published_at ?? r.tagged_at)
    return at === null ? [] : [at]
  }))].sort((a, b) => b - a).slice(0, 10)
  if (times.length < 2) return null
  const gaps = times.slice(1).map((at, i) => (times[i]! - at) / HOUR).sort((a, b) => a - b)
  const middle = Math.floor(gaps.length / 2)
  const hours = gaps.length % 2 ? gaps[middle]! : (gaps[middle - 1]! + gaps[middle]!) / 2
  return { hours, latest: times[0]!, samples: times.length }
}

export function suggestedRelease(row: SuggestionInput, releases: Release[], now: number, queue?: QueueTiming | null): ReleaseSuggestion {
  row = { ...row, state: normaliseState(row.state) }
  const empty = (tip: string): ReleaseSuggestion => ({ text: '—', kind: 'empty', tip })
  if (row.kind_slug === 'epic') return empty('Releases are suggested for tickets, not epics')
  if (row.state === 'delivered' || row.state === 'accepted') {
    const shipped = releases.filter(r => r.state === 'published' && (r.tickets.includes(row.key) || r.notes?.items?.some(t => t.key === row.key)))
      .sort((a, b) => (instant(a.published_at ?? a.tagged_at) ?? 0) - (instant(b.published_at ?? b.tagged_at) ?? 0))[0]
    return shipped?.codename ? { text: shipped.codename, kind: 'shipped', version: shipped.version, tip: `${row.state === 'accepted' ? 'Accepted' : 'Delivered'} in ${shipped.codename}\n${shipped.version}` } : empty('No shipped release is recorded for this ticket')
  }
  const cadence = releaseCadence(releases)
  const basis = cadence ? `A release every ~${Number(cadence.hours.toFixed(1))} h (last ${cadence.samples})` : 'Release cadence is not measured yet'
  if (row.state === 'done') return { text: 'Next release', kind: 'planned', tip: `Done; ships with the next release\n${basis}` }
  let ready: number | null = null, why = ''
  if (row.state === 'in_progress' && !row.eta?.ready_stale && !row.eta?.eta_stale) {
    const eta = instant(row.eta?.eta_ready_at)
    if (eta !== null) { ready = Math.max(now, eta) + HOUR; why = `ETA ~${when(eta)} + review ~1 h` }
  } else if (row.state === 'qa') { ready = now + HOUR; why = 'In QA: review ~1 h' }
  else if (queue && ['new', 'open', 'backlog', 'blocked'].includes(row.state)) {
    const start = instant(queue.expected_start)
    const hours = queue.estimate_hours ?? row.fields.estimate_hours
    if (start !== null && typeof hours === 'number' && Number.isFinite(hours) && hours > 0) {
      ready = Math.max(now, start) + hours * HOUR + HOUR
      why = `Starts ~${when(start)} (queue wait ~${Number((Math.max(0, start - now) / HOUR).toFixed(1))} h) + work ~${hours} h + review ~1 h`
    }
  }
  if (ready === null) return empty(queue ? 'No expected start yet; waits for capacity or its blocker' : row.state === 'in_progress' ? 'No current ETA reported' : 'No suggestion: not in progress and not queued')
  if (!cadence) return empty(`${why}\n${basis}; no release prediction yet`)
  const interval = cadence.hours * HOUR
  const next = cadence.latest + Math.max(1, Math.floor((now - cadence.latest) / interval) + 1) * interval
  const offset = Math.max(0, Math.ceil((ready - next) / interval))
  return { text: offset ? `Next +${offset}` : 'Next', kind: 'planned', tip: `${offset ? `Likely ${offset + 1} releases from now` : 'Likely the next release'}\n${why}; ready ~${when(ready)}\nNext release ~${when(next)}\n${basis}` }
}
