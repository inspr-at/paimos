// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { compare, displayHeadline, displayText, groupByDay, groupChanges, hasUsableNotes, matches, newSince, plainSubject, span, stats, ticketsOf, type Release } from '../src/lib/releases.ts'

const rel = (version: string, at: string, extra: Partial<Release> = {}): Release => ({
  version, tag: `v${version}`, release_channel: 'stable', release_sequence: 1, state: 'published', reserved_at: at, tagged_at: at, published_at: at,
  headline: `Release ${version}`, tickets: [], changes: [], changes_omitted: 0,
  evidence: { source_commit: 'abc1234def', source_url: '', image: null, ci: null, release_run: null, release_url: '', unavailable: [] }, ...extra,
})
const change = (commit: string, type: Release['changes'][number]['type'], subject: string, tickets: string[] = []) => ({ commit, type, subject, scope: '', tickets, at: '' })

test('days group newest first with Today and Yesterday labels', () => {
  const now = new Date(2026, 8, 24, 16, 0).getTime()
  const days = groupByDay([
    rel('260922100000.0.0', new Date(2026, 8, 22, 10).toISOString()),
    rel('260924150000.0.0', new Date(2026, 8, 24, 15).toISOString()),
    rel('260923200000.0.0', new Date(2026, 8, 23, 20).toISOString()),
    rel('260924090000.0.0', new Date(2026, 8, 24, 9).toISOString()),
  ], now)
  assert.deepEqual(days.map(d => d.releases.length), [2, 1, 1])
  assert.deepEqual(days.slice(0, 2).map(d => d.label), ['Today', 'Yesterday'])
  assert.equal(days[0].releases[0].version, '260924150000.0.0')
  assert.match(days[2].label, /Tuesday.*22.*September/)
})

test('changes group into features, fixes and other; version bumps are left out', () => {
  const groups = groupChanges([change('1', 'feat', 'feat: a'), change('2', 'fix', 'fix: b'), change('3', 'release', 'release: v1'), change('4', 'test', 'test: c'), change('5', 'other', 'B10: d')])
  assert.deepEqual([groups.features.length, groups.fixes.length, groups.other.length], [1, 1, 2])
  assert.equal(plainSubject('feat(AEON-74): wide lists and columns'), 'Wide lists and columns')
  assert.equal(plainSubject('B10: repair journey'), 'B10: repair journey')
})

test('stats count today and this week, the gap since the last release and the median gap', () => {
  const now = new Date(2026, 8, 24, 16, 0).getTime() // a Thursday
  const at = (d: number, h: number) => new Date(2026, 8, d, h).toISOString()
  const releases = [rel('1', at(24, 14)), rel('2', at(24, 10)), rel('3', at(23, 12)), rel('4', at(20, 12)), rel('5', at(24, 15), { state: 'reserved' })]
  const s = stats(releases, now, 7)
  assert.equal(s.today, 2)
  assert.equal(s.week, 3) // Monday 21 onwards
  assert.equal(s.last, Date.parse(at(24, 14)))
  assert.equal(s.median, 22 * 3_600_000) // gaps 72h, 22h, 4h
  assert.deepEqual(s.cadence, [0, 0, 1, 0, 0, 1, 2]) // 18 to 24 September
  assert.equal(span(45 * 60_000), '45 min')
  assert.equal(span(192 * 60_000), '3 h 12 min')
  assert.equal(span(3 * 86_400_000), '3 days')
})

test('compare aggregates everything after the older up to the newer release', () => {
  const releases = [
    rel('260924000003.0.0', '', { tickets: ['AEON-3'], changes: [change('c', 'fix', 'fix: c', ['AEON-3'])] }),
    rel('260924000002.0.0', '', { tickets: ['AEON-2'], changes: [change('b', 'feat', 'feat: b', ['AEON-2']), change('a', 'feat', 'feat: a')] }),
    rel('260924000001.5.0', '', { state: 'reserved', changes: [change('x', 'feat', 'feat: reserved')] }),
    rel('260924000001.0.0', '', { tickets: ['AEON-1'], changes: [change('a0', 'feat', 'feat: first')] }),
  ]
  const c = compare(releases, '260924000003.0.0', '260924000001.0.0')
  assert.equal(c.older, '260924000001.0.0')
  assert.deepEqual(c.releases.map(r => r.version), ['260924000003.0.0', '260924000002.0.0'])
  assert.deepEqual([c.groups.features.length, c.groups.fixes.length], [2, 1])
  assert.deepEqual(c.tickets, ['AEON-2', 'AEON-3'])
})

test('search and filters look at headlines, changes and ticket keys', () => {
  const r = rel('260924000001.0.0', '', { headline: 'Journey (AEON-77)', tickets: ['AEON-77'], changes: [change('a', 'fix', 'fix(AEON-78): repair stage', ['AEON-78'])] })
  const f = { q: '', features: false, fixes: false, tickets: false }
  assert.ok(matches(r, { ...f, q: 'journey' }))
  assert.ok(matches(r, { ...f, q: 'repair' }))
  assert.ok(matches(r, { ...f, q: 'aeon-78' }))
  assert.ok(!matches(r, { ...f, q: 'hours' }))
  assert.ok(matches(r, { ...f, fixes: true }))
  assert.ok(!matches(r, { ...f, features: true }))
  assert.ok(matches(r, { ...f, tickets: true }))
  assert.ok(!matches({ ...r, tickets: [], changes: [] }, { ...f, tickets: true }))
  assert.deepEqual(ticketsOf(r), ['AEON-77', 'AEON-78'])
})

test('new since the last visit: newer published releases, nothing on a first visit', () => {
  const releases = [rel('3', ''), rel('2', ''), rel('2.5', '', { state: 'reserved' }), rel('1', '')]
  assert.deepEqual([...newSince(releases, '1')].sort(), ['2', '3'])
  assert.equal(newSince(releases, null).size, 0)
})

test('regenerated missing snapshots preserve the v1 archive headline, tickets and filters with an honest gap', () => {
  const archived = rel('260924000001.0.0', '', {
    headline: 'Journey (AEON-77)', tickets: ['AEON-77'],
    changes: [change('a', 'fix', 'fix(AEON-78): repair stage', ['AEON-78'])],
  })
  const regenerated: Release = { ...archived, notes: {
    source: 'unavailable', snapshot_sha256: '', captured_at: null, release_revision: 0, hidden: 0, items: [],
    gaps: ['Release membership and bilingual ticket fields were not captured. Git mentions do not establish release membership.'],
  } }
  const before = structuredClone(regenerated)
  assert.equal(hasUsableNotes(archived), false)
  assert.equal(hasUsableNotes(regenerated), false)
  assert.equal(displayHeadline(regenerated), displayHeadline(archived))
  assert.equal(displayHeadline(regenerated), 'Journey')
  assert.deepEqual(ticketsOf(regenerated), ['AEON-77', 'AEON-78'])
  for (const q of ['', 'journey', 'repair', 'aeon-78']) {
    const f = { q, features: false, fixes: false, tickets: true }
    assert.equal(matches(regenerated, f), matches(archived, f))
    assert.equal(matches(regenerated, f), true)
  }
  assert.deepEqual(groupChanges(regenerated.changes), groupChanges(archived.changes))
  assert.deepEqual(regenerated, before) // Display never rewrites history or manufactures notes.
})

test('headlines read without the ticket keys their chips show, in sentence case', () => {
  const keys = ['AEON-67', 'AEON-75', 'AEON-72', 'AEON-79', 'AEON-13', 'AEON-77', 'AEON-78']
  assert.equal(displayText('time entry editing (AEON-75)', keys), 'Time entry editing')
  assert.equal(displayText('agent registration (AEON-67), time entry corrections (AEON-75)', keys), 'Agent registration, time entry corrections')
  assert.equal(displayText('Journey (AEON-77, AEON-78)', keys), 'Journey')
  assert.equal(displayText('attachment route fix (AEON-72), journey stage source + plan tickets (AEON-79)', keys), 'Attachment route fix, journey stage source + plan tickets')
  assert.equal(displayText('migration tests count files (WIP, AEON-13)', keys), 'Migration tests count files (WIP)')
  assert.equal(displayText('AEON-75: edit and delete time entries', keys), 'Edit and delete time entries')
  // Only keys the chips show go; the rest of the text stays as written.
  assert.equal(displayText('retry a busy BEGIN (PAI-1057)', keys), 'Retry a busy BEGIN (PAI-1057)')
  assert.equal(displayText('B11: add journey stage provenance (AEON-79)', keys), 'B11: add journey stage provenance')
  assert.equal(displayText('(AEON-75)', keys), '(AEON-75)')
  assert.equal(plainSubject('fix(AEON-72): keep unknown binaries as downloads (AEON-72)', ['AEON-72']), 'Keep unknown binaries as downloads')
  assert.equal(displayHeadline({ headline: 'wide lists (AEON-74)', tickets: ['AEON-74'], changes: [] }), 'Wide lists')
})
