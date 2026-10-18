// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1493 risks: Delivery › Building it shows a figure, change or verdict the server did not send; an empty or
// failed read looks measured; the head line or the summary card disagree with the tiles; the worst rows lose an
// item's round marks; German loses a word of the approved copy.
import { readFileSync } from 'node:fs'
import { afterEach, expect, it, vi } from 'vitest'
vi.mock('../src/lib/api.ts', () => ({ api: vi.fn(), APIError: class extends Error { constructor(public status: number, message: string) { super(message) } } }))
import { api } from '../src/lib/api.ts'
import { buildingView, hm, readDeliveryBuilding } from '../src/lib/deliveryBuilding'
import { buildingText } from '../src/lib/deliveryBuildingText'
import { simpleNumbersOf } from '../src/lib/deliverySimple'
import { simpleText } from '../src/lib/deliverySimpleText'
import { deliveryText } from '../src/lib/deliveryNumbersText'
import type { DeliveryMetrics } from '../src/lib/deliveryNumbers'
import { NO_FILTERS } from '../src/lib/statusTiming'
import { buildingAnswer } from './delivery-building-fixtures'
import { deliveryMetrics } from './delivery-numbers-fixtures'

const NOW = Date.parse('2026-10-18T14:00:00Z')
const answer = (options: Parameters<typeof buildingAnswer>[2] = {}) => buildingAnswer('p-aeon', 7, { now: NOW, ...options })
const tile = (view: ReturnType<typeof buildingView>, key: string) => view.tiles.find(entry => entry.key === key)!
const text = (parts: { text: string }[]) => parts.map(part => part.text).join('')
afterEach(() => { vi.mocked(api).mockReset() })

it('AEON-1493: the four tiles say the server\'s figures, changes and verdicts in Simple and Expert words', () => {
  const view = buildingView(answer(), 7, 'ready', 'en')
  expect(view.tiles.map(entry => entry.simple.name)).toEqual(['First-try rate', 'Rounds per item', 'Idle between rounds', 'Lead time per item'])
  // Two of three targets are far off, one is close: none is on target.
  expect(view.head).toEqual({ kind: 'ready', text: '0 of 3 on target' })

  const first = tile(view, 'first_try')
  expect(first.simple.value).toEqual([{ text: '29', unit: '%' }])
  expect(first.simple.verdict).toMatchObject({ level: 'far', word: 'Far off', gap: '29% vs. 60% wanted' })
  expect(first.simple.trend).toEqual({ cls: 'worse', icon: 'arrow-down', text: 'Worse than the 7 days before (−5 pts)' })
  expect(first.simple.say).toBe('6 of 21 items passed on the first try.')
  expect(text(first.expert.lineA.parts)).toBe('6 of 21 merged after one round')
  expect(first.expert.lineA.parts.find(part => part.strong)?.text).toBe('6 of 21')
  expect(first.expert.lineB).toBe('21 items · 15 needed more rounds')
  expect(first.expert.target).toBe('Target ≥ 60%')
  expect(first.simple.learn.facts).toEqual(['In the Expert view: First-try rate · Event log: rounds, gate verdicts, merges', 'Target ≥ 60%'])

  const rounds = tile(view, 'rounds')
  expect(rounds.simple.value).toEqual([{ text: '3.2', unit: 'rounds' }])
  expect(rounds.simple.verdict).toMatchObject({ level: 'far', gap: '2× the target' })
  expect(rounds.expert.delta).toEqual({ cls: 'worse', icon: 'arrow-up', text: '+0.3', word: 'worse' })
  expect(text(rounds.expert.lineA.parts)).toBe('p50 3.2 · mean 3.6')
  expect(text(rounds.pp)).toBe('p50 3.2 · mean 3.6 · p90 6')
  expect(rounds.simple.say).toBe('Half the items needed 4 rounds or more.')
  expect(rounds.simple.learn.body).toBe('Every round until merged: the first build, each fix round after a gate verdict, and each merge-forward round that brings in the newest main. p50 3.2: half of the items needed this many or fewer.')

  const idle = tile(view, 'idle')
  expect(idle.simple.value).toEqual([{ text: '22', unit: 'min' }])
  expect(idle.simple.verdict).toMatchObject({ level: 'close', word: 'Close', gap: '1.5× the target' })
  expect(idle.expert.lineB).toBe('76 waits in 21 items')
  expect(text(idle.expert.lineA.parts)).toBe('p50 22 min · p90 1 h 40 min')

  const lead = tile(view, 'lead')
  expect(lead.simple.value).toEqual([{ text: '5', unit: 'h' }, { text: '10', unit: 'min' }])
  expect(lead.expert.value).toEqual({ main: '5', unit: 'h', more: { main: '10', unit: 'min' } })
  expect(lead.simple.verdict).toMatchObject({ level: 'none', word: 'No target yet', gap: 'watched', sep: ': ' })
  expect(lead.simple.trend.text).toBe('Better than the 7 days before (−1 h 10 min)')
  expect(lead.chart.panels[0].targets).toEqual([])
  expect(lead.simple.spark.target).toBeNull()
})

it('AEON-1493: charts and small charts follow the server\'s buckets; days before the first round are no data', () => {
  const view = buildingView(answer({ since: '2026-10-15T09:00:00Z' }), 7, 'ready', 'en')
  const idle = tile(view, 'idle')
  expect(idle.chart.buckets.map(bucket => bucket.status)).toEqual(['no_data', 'no_data', 'no_data', 'partial', 'ok', 'ok', 'ok'])
  expect(idle.chart.buckets.at(-1)!.label).toBe('Today')
  expect(idle.chart.readouts[0]).toMatch(/· no data yet$/)
  expect(idle.chart.readouts[4]).toMatch(/· p50 \d+ min · p90 1 h \d+ min · \d+ waits$/)
  expect(idle.chart.panels[0].targets).toEqual([{ value: 15, label: 'Target < 15 min' }])
  expect(idle.simple.spark.values.slice(0, 3)).toEqual([null, null, null])
  // Lead time is drawn in hours; its small chart has no target zone.
  const lead = tile(view, 'lead')
  expect(lead.chart.unit).toBe('h')
  expect(lead.chart.panels[0].lines[0].values[4]).toBeCloseTo((280 + (4 % 3) * 20) / 60)
  const first = tile(view, 'first_try')
  expect(first.chart.panels[0]).toMatchObject({ top: 100, band: null, targets: [{ value: 60, label: 'Target ≥ 60%' }] })
})

it('AEON-1493: loading, a failed read and a window without rounds never show figures, and say so in the head line', () => {
  const loading = buildingView(null, 7, 'loading', 'en')
  expect(loading.head.kind).toBe('loading')
  expect(loading.tiles).toHaveLength(4)
  expect(loading.tiles.every(entry => entry.simple.value === null && entry.expert.value === null && entry.worst.rows.length === 0)).toBe(true)
  expect(loading.summary).toEqual([])

  const failed = buildingView(null, 7, 'error', 'en')
  expect(failed.head).toEqual({ kind: 'error', text: 'Not loaded.' })
  expect(failed.tiles[0].simple.verdict).toMatchObject({ level: 'none', word: 'Not loaded' })
  expect(failed.tiles[0].simple.empty).toBe('Not loaded')

  const empty = buildingView(answer({ empty: true }), 7, 'ready', 'en')
  expect(empty.head).toEqual({ kind: 'nodata', text: 'No rounds recorded in this window yet.' })
  expect(empty.summary).toEqual([])
  for (const entry of empty.tiles) {
    expect(entry.simple.value).toBeNull()
    expect(entry.simple.trend).toMatchObject({ cls: 'none', text: 'Nothing to compare yet' })
    expect(entry.expert.delta.cls).toBe('none')
    expect(entry.chart.buckets.every(bucket => bucket.status === 'no_data')).toBe(true)
  }
  // The sentence still names what is wanted, never a measured zero.
  expect(tile(empty, 'first_try').simple.say).toBe('Wanted: at least 60% of items merged after one round.')
})

it('AEON-1493: the worst rows keep every round mark, say them in words and link the ticket key', () => {
  const view = buildingView(answer(), 7, 'ready', 'en')
  const rows = tile(view, 'rounds').worst.rows
  expect(rows.map(row => [row.key, row.value])).toEqual([['AEON-418', '5 rounds'], ['AEON-402', '5 rounds'], ['AEON-431', '4 rounds'], ['AEON-397', '4 rounds'], ['AEON-425', '4 rounds']])
  expect(rows[0].rounds).toEqual(['failed', 'changes', 'merge_forward', 'changes', 'passed'])
  expect(rows[0].said).toBe('AEON-418 Chat composer keeps the draft on reload, 5 rounds. Rounds: failed, changes, merge-forward, changes, passed')
  expect(tile(view, 'idle').worst.rows[0].value).toBe('idle 2 h 10 min')
  expect(tile(view, 'idle').worst.rows[4].said).toContain('Rounds: no verdict recorded, passed')
  expect(tile(view, 'lead').worst.rows[0].value).toBe('lead 31 h')
  expect(tile(view, 'lead').worst).toMatchObject({ title: 'Worst in this window', sub: 'Lead time per item · last 7 days' })
})

it('AEON-1493: the summary card counts the Building it targets and names their gaps', () => {
  const metrics = deliveryMetrics() as unknown as DeliveryMetrics
  const without = simpleNumbersOf(metrics, 7, 'ready', false, 'en').summary
  const view = buildingView(answer(), 7, 'ready', 'en')
  const withBuilding = simpleNumbersOf(metrics, 7, 'ready', false, 'en', view.summary).summary
  if (without.kind !== 'ready' || withBuilding.kind !== 'ready') throw new Error('summary not ready')
  const total = (big: string) => Number(/of (\d+) numbers|the (\d+) numbers/.exec(big)!.slice(1).find(Boolean))
  expect(total(withBuilding.big)).toBe(total(without.big) + 3)
  // First-try rate is far off at 2.1×: it can be the biggest gap; at least the off-target line may name it.
  const names = withBuilding.gaps.map(gap => gap.name)
  expect(names.length).toBeGreaterThan(0)
  expect(withBuilding.week).not.toBe(without.week)
})

it('AEON-1493: German keeps the approved words, "Runde" among the explained ones', () => {
  const view = buildingView(answer(), 7, 'ready', 'de')
  expect(view.head.text).toBe('0 von 3 im Ziel')
  expect(view.tiles.map(entry => entry.simple.name)).toEqual(['Erfolg im ersten Anlauf', 'Runden pro Änderung', 'Leerlauf zwischen Runden', 'Durchlaufzeit pro Änderung'])
  expect(tile(view, 'rounds').simple.value).toEqual([{ text: '3,2', unit: 'Runden' }])
  expect(tile(view, 'idle').simple.verdict).toMatchObject({ word: 'Knapp dran', gap: '1,5× das Ziel' })
  expect(tile(view, 'lead').simple.verdict).toMatchObject({ word: 'Noch kein Ziel', gap: 'beobachtet' })
  expect(buildingText('de')).toMatchObject({ more: 'Schlechteste 5', src: 'Ereignisprotokoll: Runden, Prüfurteile, Merges', filters: 'Filter gelten: Typ, Priorität, Bearbeitet von' })
  expect(buildingText('de').kinds).toEqual({ passed: 'bestanden', changes: 'Änderungen verlangt', failed: 'gescheitert', merge_forward: 'main nachgezogen', none: 'kein Urteil erfasst' })
  for (const lang of ['en', 'de'] as const) {
    const term = lang === 'de' ? 'Runde' : 'Round'
    expect(simpleText(lang).defs.map(([word]) => word)).toContain(term)
    expect(deliveryText(lang).defs.map(([word]) => word)).toContain(term)
  }
  expect(hm(1860, 'de')).toBe('31 h')
  expect(hm(100, 'en')).toBe('1 h 40 min')
})

it('AEON-1493: an answer for another project or window is refused rather than shown', async () => {
  const ok = answer()
  vi.mocked(api).mockResolvedValueOnce(new Response(JSON.stringify(ok)))
  await expect(readDeliveryBuilding('p-aeon', 7, NO_FILTERS)).resolves.toEqual(ok)
  vi.mocked(api).mockResolvedValueOnce(new Response(JSON.stringify({ ...ok, window: 30 })))
  await expect(readDeliveryBuilding('p-aeon', 7, NO_FILTERS)).rejects.toThrow('Invalid building response')
  vi.mocked(api).mockResolvedValueOnce(new Response(JSON.stringify({ ...ok, project_id: 'p-other' })))
  await expect(readDeliveryBuilding('p-aeon', 7, NO_FILTERS)).rejects.toThrow('Invalid building response')
  vi.mocked(api).mockResolvedValueOnce(new Response(JSON.stringify({ error: 'building read deadline exceeded' }), { status: 503 }))
  await expect(readDeliveryBuilding('p-aeon', 7, { type: 'bug', priority: 'high', worked_by: 'agent' })).rejects.toThrow('building read deadline exceeded')
  expect(String(vi.mocked(api).mock.calls.at(-1)![0])).toBe('/projects/p-aeon/delivery/building?window=7&type=bug&priority=high&worked_by=agent')
})

it('AEON-1493: the tiles are the shipped SimpleTile and ExpertTile with the whole-tile trigger slots', () => {
  const section = readFileSync(new URL('../src/components/delivery/BuildingSection.vue', import.meta.url), 'utf8')
  expect(section).toContain("import SimpleTile from './SimpleTile.vue'")
  expect(section).toContain("import ExpertTile from './ExpertTile.vue'")
  expect(section).toMatch(/<template #trigger>\s*<button type="button" class="bd-open"[^>]*aria-haspopup="dialog"/)
  // No coloured edge accent: the open tile gets a full tint and a thin ring, never a border on one side.
  expect(section).not.toMatch(/border-(left|top)\s*:/)
  const panel = readFileSync(new URL('../src/components/delivery/WorstPanel.vue', import.meta.url), 'utf8')
  expect(panel).toContain(':sheet-below="760"')
  expect(panel).not.toMatch(/font-style:\s*italic/)
})
