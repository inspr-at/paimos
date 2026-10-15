// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1359 risks: a duration is written outside the design's one format; the lifeline drops or invents a gate (a
// reopen must cross doing and done again, moves inside a group cross none) or merges the wrong final marker; a marker
// sits off its crossing or overlaps a neighbour; "usually" appears for a visit that is not slower than the rule says;
// Delivery › Tickets shows Done → live for a project that does not mark releases, or the chart draws a gap through a
// day without a release (or bridges a day with no data yet).
import { expect, it, vi } from 'vitest'
vi.mock('../src/lib/api.ts', () => ({ api: async () => new Response('{}'), APIError: class extends Error { constructor(public status: number, message: string) { super(message) } } }))
import { lifelineView } from '../src/lib/lifelineView'
import { ticketsModel, trendOf } from '../src/lib/deliveryTickets'
import type { StatusTimingActor, StatusTimingVisit, TicketLifeline, TicketStatusTiming } from '../src/lib/statusTiming'
import { fmtDuration, lifeOf, MARKER_STEP, markersOf, placeMarkers, slowness, stripWidths } from '../src/lib/workFigures'

const H = 3600, D = 86_400
const lead: StatusTimingActor = { name: 'AIT lead', kind: 'agent' }
const worker: StatusTimingActor = { name: 'AIT-157-b2', kind: 'agent' }
const tooling: StatusTimingActor = { name: 'Release tooling', kind: 'unknown' }
/** A lifeline from [status, hours after creation] steps; the last step is open unless it is a closed status. */
function lifeline(steps: [string, number, StatusTimingActor | null][], nowHours: number, extra: Partial<TicketLifeline> = {}): TicketLifeline {
  const created = Date.parse('2026-10-12T16:00:00Z')
  const at = (hours: number) => new Date(created + hours * H * 1000).toISOString()
  const seen = new Set<string>()
  const terminal = ['done', 'delivered', 'accepted', 'cancelled', 'archived']
  const visits: StatusTimingVisit[] = steps.map(([status, hours, by], i) => {
    const next = steps[i + 1], last = i === steps.length - 1, open = last && !terminal.includes(status)
    const visit: StatusTimingVisit = {
      status, from: at(hours), to: next ? at(next[1]) : last && !open ? at(hours) : null,
      seconds: next ? (next[1] - hours) * H : open ? (nowHours - hours) * H : 0,
      moved_by: by, next_status: next?.[0] ?? null, ended_by: next?.[2] ?? null, release: status === 'delivered' ? '2026.10.7' : null,
      source: 'event', again: seen.has(status), ongoing: open,
    }
    seen.add(status)
    return visit
  })
  return { node_id: 'n-1', generated_at: at(nowHours), visits, complete: true, inferred: false, gap_count: 0, initial_offset_seconds: 0, ...extra }
}

it('writes every duration in the one format of the design', () => {
  const table: [number, string][] = [
    [10, '1m'], [40 * 60, '40m'], [59 * 60, '59m'], [2.4 * H, '2.4h'], [5 * H, '5h'], [9.9 * H, '9.9h'], [14 * H, '14h'],
    [23.4 * H, '23h'], [1.5 * D, '1.5d'], [2 * D, '2d'], [9.9 * D, '9.9d'], [12 * D, '12d'],
  ]
  for (const [seconds, text] of table) expect(fmtDuration(seconds), String(seconds)).toBe(text)
  expect(fmtDuration(null)).toBe('—')
  expect(fmtDuration(1.5 * D, 'de')).toBe('1,5T')
})

it('crosses doing and done again after a reopen, and moves inside a group cross no gate', () => {
  // AIT-157: New, In progress, QA, Done, In progress again, QA again, Done again, Delivered (finished).
  const view = lifelineView(lifeline([
    ['new', 0, lead], ['in_progress', 2, worker], ['qa', 7.4, worker], ['done', 8, lead],
    ['in_progress', 9, worker], ['qa', 10, worker], ['done', 11, lead], ['delivered', 30, tooling],
  ], 40), null)
  expect(view.markers.map(marker => [marker.kind, marker.icon, marker.index])).toEqual([
    ['gate', 'open', 0], ['gate', 'in_progress', 1], ['gate', 'done', 3], ['gate', 'in_progress', 4], ['gate', 'done', 6], ['final', 'delivered', 7],
  ])
  expect(view.markers[0].text).toBe(`Entered open · Mon 12 Oct ${clockOf(0)} · created`)
  expect(view.markers[3].text).toMatch(/^Entered doing again · .* · by AIT-157-b2$/)
  expect(view.markers[5].text).toMatch(/^Delivered · .* · by Release tooling · current status$/)
  // The final status is a mark at the end, no time; the line names it last.
  expect(view.segments).toHaveLength(7)
  expect(view.items.map(item => item.label)).toEqual(['New', 'In progress', 'QA', 'Done', 'In progress again', 'QA again', 'Done again', 'Delivered'])
  expect(view.items.at(-1)?.value).toBeNull()
  expect(view.caption).toEqual({ label: 'Created → live', value: '1.3d', after: '' })
  // Who moved it on, with the release that carried it.
  expect(view.segments[6].tip.map(line => line.text)).toContain('Release tooling moved it to Delivered with release 2026.10.7')
})

it('merges a final gate with the final status and pins a live ticket to now', () => {
  const finished = lifelineView(lifeline([['new', 0, lead], ['in_progress', 1, worker], ['qa', 5, worker], ['done', 6, lead]], 10), null)
  expect(finished.markers.map(marker => marker.kind)).toEqual(['gate', 'gate', 'final'])
  expect(finished.markers[2].text).toMatch(/^Entered done · .* · by AIT lead · current status$/)
  expect(finished.caption).toEqual({ label: '', value: '6h', after: 'from created to Done' })

  const open = lifelineView(lifeline([['new', 0, lead], ['in_progress', 2, worker]], 6.5), null)
  expect(open.markers.map(marker => marker.kind)).toEqual(['gate', 'gate', 'now'])
  expect(open.markers[2].text).toMatch(/^In progress now · since .* · by AIT-157-b2$/)
  expect(open.segments.at(-1)?.now).toBe(true)
  expect(open.segments.at(-1)?.tip.map(line => line.text)).toEqual(expect.arrayContaining(['Still in progress: nobody has moved it on yet']))
  expect(open.segments.at(-1)?.tip[1].text).toMatch(/→ now$/)
  expect(open.caption).toEqual({ label: '', value: '6.5h', after: 'so far' })
})

it('says "usually" only above 1.5 × p50 and 10 minutes over it, and attention only above p90', () => {
  const typicals = { qa_to_merged: { p50: 51 * 60, p90: 2.5 * H }, in_progress: { p50: 2.4 * H, p90: 8 * H } }
  expect(slowness({ status: 'qa', seconds: 76 * 60 }, typicals)).toBeNull() // 1.49 × p50
  expect(slowness({ status: 'qa', seconds: 77 * 60 }, typicals)).toEqual({ p50: 51 * 60, p90: 2.5 * H, attn: false })
  expect(slowness({ status: 'qa', seconds: 3 * H }, typicals)?.attn).toBe(true)
  // A tiny p50 needs the 10-minute margin too.
  expect(slowness({ status: 'in_progress', seconds: 5 * 60 }, { in_progress: { p50: 2 * 60, p90: null } })).toBeNull()
  expect(slowness({ status: 'blocked', seconds: 9 * D }, typicals)).toBeNull()

  const view = lifelineView(lifeline([['new', 0, lead], ['in_progress', 1, worker], ['qa', 6.4, worker], ['done', 9.5, lead]], 12), typicals)
  expect(view.items[1].usual).toEqual({ text: ', usually 2.4h', attn: false })
  expect(view.items[2].usual).toEqual({ text: ', usually 51m', attn: true })
  expect(view.segments[2].tip.at(-1)).toEqual({ text: 'Slower than 9 in 10 tickets: usually 51m for QA → merged.', tone: 'attn' })
})

it('keeps every marker on its notch unless a cluster moved it, and never lets two overlap', () => {
  // Created, picked up and done within a few pixels: AIT-116.
  const life = lifeOf(lifeline([['new', 0, lead], ['in_progress', 0.2, worker], ['qa', 0.5, worker], ['done', 0.7, lead], ['delivered', 40, tooling]], 41))
  const markers = markersOf(life), width = 480
  const widths = stripWidths(life.segments.map(segment => segment.seconds), width)
  expect(widths.every(w => w >= 6)).toBe(true)
  expect(widths.reduce((sum, w) => sum + w, 0) + 2 * (widths.length - 1)).toBeCloseTo(width, 6)
  const places = placeMarkers(markers, widths, width)
  expect(places[0]).toEqual({ notch: 0, left: 0.5 - 7.5, displaced: false })
  expect(places.at(-1)).toEqual({ notch: width - 1, left: width - 1 + 0.5 - 7.5, displaced: false })
  for (let k = 1; k < places.length; k++) expect(places[k].left - places[k - 1].left).toBeGreaterThanOrEqual(MARKER_STEP - 1e-9)
  expect(places.some(place => place.displaced)).toBe(true)
  // A marker the cluster did not move is centred exactly on its notch.
  for (const place of places.filter(p => !p.displaced)) expect(place.left + 7.5).toBe(place.notch + 0.5)
})

function clockOf(hours: number) {
  const d = new Date(Date.parse('2026-10-12T16:00:00Z') + hours * H * 1000)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

// ---------- Delivery › Tickets ----------
function timing(final: 'delivered' | 'done', extra: Partial<TicketStatusTiming> = {}): TicketStatusTiming {
  const step = (key: string, n: number, p50: number | null, p90: number | null, prevN = 0, prevP50: number | null = null) => ({ step: key, n, p50, p90, prev_n: prevN, prev_p50: prevP50, prev_p90: null })
  const steps = final === 'delivered'
    ? [step('created_to_live', 108, 1.5 * D, 5.9 * D, 90, 2.8 * D), step('new_to_picked_up', 128, 6.7 * H, 30 * H), step('in_progress', 117, 2.4 * H, 9 * H, 100, 2.5 * H), step('qa_to_merged', 117, 51 * 60, 2.5 * H), step('done_to_live', 108, 19 * H, 2 * D), step('live_to_accepted', 80, D, 2 * D)]
    : [step('created_to_done', 129, 1.2 * D, 4 * D), step('new_to_picked_up', 140, 6 * H, 30 * H), step('in_progress', 129, 2 * H, 9 * H), step('qa_to_merged', 129, 40 * 60, 2 * H)]
  const day = (date: string, state: 'observed' | 'partial' | 'no_data', n: number, p50: number | null) => ({ from: `${date}T00:00:00Z`, to: `${date}T23:59:59Z`, state, n, p50, p90: p50 == null ? null : p50 * 2 })
  return {
    project_id: 'p-1', generated_at: '2026-10-14T15:40:00Z', window: 7, final_status: final, steps: steps as TicketStatusTiming['steps'], rework: 22,
    coverage: { tickets: 166, complete: 166, inferred: 0, gaps: 0, since: '2026-09-07T10:59:26Z', partial: false },
    buckets: [day('2026-10-08', 'no_data', 0, null), day('2026-10-09', 'observed', 5, D), day('2026-10-10', 'observed', 0, null), day('2026-10-11', 'observed', 4, 1.2 * D), day('2026-10-12', 'observed', 3, D), day('2026-10-13', 'observed', 6, 0.9 * D), day('2026-10-14', 'observed', 15, 20 * H)],
    ...extra,
  }
}

it('follows the project: five steps to live with releases, three steps and Created → done without', () => {
  const live = ticketsModel(timing('delivered'), 7, 'en')
  expect(live.head).toBe('Tickets, from created to live')
  expect(live.count).toBe('108 tickets went live')
  expect(live.steps.map(step => step.name)).toEqual(['New → picked up', 'In progress', 'QA → merged', 'Done → live', 'Live → accepted'])
  expect(live.hero.say).toBe('Half of the tickets go live within 1.5d of being created; 1 in 10 takes 5.9d or longer. The longest steps: Done → live and New → picked up.')
  expect(live.hero.trend).toMatchObject({ cls: 'better', text: 'Better than the 7 days before', prev: '2.8d' })
  expect(live.steps[1].trend).toMatchObject({ cls: 'same', text: 'Steady vs. the 7 days before' })
  expect(live.steps[0].trend.text).toBe('No data in the 7 days before')
  expect(live.steps[2].count).toEqual({ before: '', value: '22', after: ' went back from QA' })
  expect(live.steps[0].count).toEqual({ before: '', value: '128', after: ' picked up' })
  expect(live.heroFoot).toBe('From the event log · 108 tickets went live · 22 went back from QA')
  expect(live.expert.map(tile => tile.label)).toEqual(['Created → live', 'New → picked up', 'In progress', 'QA → merged', 'Done → live', 'Live → accepted'])
  expect(live.expert[3].lineB).toBe('117 merged · 22 went back')
  expect(live.expert[0].delta).toMatchObject({ cls: 'better', text: '−1.3d', word: 'better' })

  const done = ticketsModel(timing('done'), 7, 'en')
  expect(done.head).toBe('Tickets, from created to done')
  expect(done.steps.map(step => step.key)).toEqual(['new_to_picked_up', 'in_progress', 'qa_to_merged'])
  expect(done.typical.map(step => step.key)).toEqual(['new_to_picked_up', 'in_progress', 'qa_to_merged'])
  expect(done.chart.label).toBe('Created → done')
  expect(done.expert).toHaveLength(4)
})

it('bridges a day without a release, keeps a gap for no data yet, and says which is which', () => {
  const chart = ticketsModel(timing('delivered'), 7, 'en').chart
  expect(chart.buckets.map(bucket => bucket.status)).toEqual(['no_data', 'ok', 'empty', 'ok', 'ok', 'ok', 'ok'])
  expect(chart.panels[0].lines[0]).toMatchObject({ connect: true, bridge: true })
  expect(chart.readouts[0]).toBe('8 Oct · no data yet')
  expect(chart.readouts[2]).toBe('10 Oct · no release that day, none went live')
  expect(chart.readouts[6]).toBe('Today · p50 20h · p90 1.7d · 15 tickets went live')
})

it('keeps every slot while loading, and names no trend without a value', () => {
  const empty = ticketsModel(null, 30, 'en')
  expect(empty.steps).toHaveLength(5)
  expect(empty.steps.every(step => step.value === null && step.slow === null)).toBe(true)
  expect(trendOf(null, 30, 'en')).toMatchObject({ cls: 'none', text: 'Nothing to compare yet' })
})
