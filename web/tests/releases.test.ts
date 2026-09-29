// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { compare, displayHeadline, displayText, groupByDay, groupChanges, hasUsableNotes, matches, newSince, plainSubject, presentChanges, presentRelease, span, stats, ticketsOf, type Release } from '../src/lib/releases.ts'

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
  assert.equal(plainSubject('P0.x: bound the managed settings migration\'s lock wait'), 'Bound the managed settings migration\'s lock wait')
  assert.equal(plainSubject('P0.3: repair the gate'), 'Repair the gate')
  assert.equal(plainSubject('P0.x: AEON-274: quotes load'), 'Quotes load')
  assert.equal(plainSubject('P0.x: showcase quotes load without crashing (AEON-274)', ['AEON-274']), 'Showcase quotes load without crashing')
})

test('features and fixes are one block per visible ticket; the rest stay in other', () => {
  const note = (key: string, pillEN: string, pillDE = '', benefitEN = 'You can use it.', benefitDE = '') => ({ key, pill_en: pillEN, pill_de: pillDE, benefit_en: benefitEN, benefit_de: benefitDE })
  const chat = note('AEON-273', 'Session chat', 'Sitzungschat', 'The chat stays put.', 'Der Chat bleibt.')
  const quotes = note('AEON-274', 'Quotes open reliably', 'Angebote öffnen zuverlässig', 'Quotes open.', 'Angebote öffnen sich.')
  const changes = [
    { ...change('1', 'other', 'P0.x: session chat (AEON-273)', ['AEON-273']), group: 'features' as const, linked_tickets: [chat] },
    { ...change('2', 'other', 'P0.x: more chat (AEON-273)', ['AEON-273']), group: 'features' as const, linked_tickets: [chat] },
    { ...change('3', 'other', 'P0.x: quotes crash (AEON-274)', ['AEON-274']), group: 'fixes' as const, linked_tickets: [quotes] },
    { ...change('4', 'other', 'P0.x: refresh the manifest'), group: 'other' as const },
    { ...change('5', 'feat', 'feat: no visible ticket', ['AEON-9']), group: 'features' as const },
    { ...change('6', 'other', 'P0.x: one commit, two tickets', ['AEON-273', 'AEON-274']), group: 'fixes' as const, linked_tickets: [quotes, chat] },
    { ...change('7', 'release', 'release: v1'), group: 'features' as const, linked_tickets: [chat] },
    { ...change('1', 'other', 'P0.x: session chat again', ['AEON-273']), group: 'features' as const, linked_tickets: [chat] },
  ]
  const en = presentChanges(changes, 'en-GB')
  // AEON-305: a ticket is one block, in one group; a commit naming two told tickets sits in both blocks.
  assert.deepEqual(en.features.map(line => [line.key, line.pill, line.commits.map(c => c.commit)]), [['AEON-273', 'Session chat', ['1', '2', '6']]])
  assert.equal(en.features[0].benefit, 'The chat stays put.')
  assert.deepEqual(en.fixes.map(line => [line.key, line.commits.map(c => c.commit)]), [['AEON-274', ['3', '6']]])
  assert.deepEqual(en.other.map(c => c.commit), ['4', '5'])
  assert.equal(plainSubject(en.other[1].subject, en.other[1].tickets), 'No visible ticket')
  const de = presentChanges(changes, 'de-AT')
  assert.equal(de.features[0].pill, 'Sitzungschat')
  assert.equal(de.features[0].benefit, 'Der Chat bleibt.')
  assert.equal(de.features[0].pillLang, 'de')
  const fallback = presentChanges([{ ...changes[0], linked_tickets: [{ ...chat, pill_de: ' ', benefit_de: '' }] }], 'de-DE')
  assert.equal(fallback.features[0].pill, 'Session chat')
  assert.equal(fallback.features[0].pillLang, 'en')
  const hidden = presentChanges([{ ...change('8', 'fix', 'fix: private'), group: 'fixes' as const, linked_tickets: [{ key: 'AEON-1', pill_en: ' ', pill_de: '', benefit_en: '', benefit_de: '' }] }])
  assert.equal(hidden.fixes.length, 0)
  assert.deepEqual(hidden.other.map(c => c.commit), ['8'])
})

test('a backfilled snapshot tells the same blocks as linked tickets (AEON-305)', () => {
  const item = (key: string, pill: string, benefit: string) => ({ id: key, key, pill_en: pill, pill_de: '', benefit_en: benefit, benefit_de: '' })
  const notes = { source: 'database-snapshot', snapshot_sha256: 'c1', captured_at: '2026-09-29T12:40:00Z', release_revision: 1, written_after_release: true, gaps: [], hidden: 1, items: [
    item('AEON-211', 'Deploy target on screen', 'Every approval names its server.'),
    item('AEON-293', 'Readable risk chip', 'The chip is readable.'),
    item('AEON-290', 'Older notes', 'No commit names this ticket.'),
  ] }
  const live = { key: 'AEON-211', pill_en: 'Live text', pill_de: '', benefit_en: 'Not the snapshot.', benefit_de: '' }
  const changes = [
    { ...change('a', 'other', 'AEON-211: reserve stable102', ['AEON-211']), group: 'features' as const, linked_tickets: [live] },
    { ...change('b', 'other', 'P0.x: lift the chip contrast (AEON-293)', ['AEON-293']), group: 'fixes' as const },
    { ...change('c', 'test', 'test(AEON-211): approvals name the target', ['AEON-211']), group: 'other' as const },
    { ...change('d', 'other', 'P0.x: quote flow descriptions (AEON-278)', ['AEON-278']), group: 'other' as const },
    change('e', 'release', 'release: v260929082208.0.0'),
  ]
  const r = rel('260929082208.0.0', '2026-09-29T08:22:08Z', { notes, changes })
  const p = presentRelease(r)
  // The snapshot decides who is told and with which text; the served groups decide feature or fix.
  assert.deepEqual(p.features.map(line => [line.key, line.pill, line.commits.map(c => c.commit)]), [['AEON-211', 'Deploy target on screen', ['a', 'c']], ['AEON-290', 'Older notes', []]])
  assert.deepEqual(p.fixes.map(line => [line.key, line.benefit, line.commits.map(c => c.commit)]), [['AEON-293', 'The chip is readable.', ['b']]])
  // The hidden ticket's commit stays in Other; the version bump is left out.
  assert.deepEqual(p.other.map(c => c.commit), ['d'])
  // Without the snapshot the same commits read from the live linked text.
  const linked = presentRelease({ ...r, notes: undefined })
  assert.deepEqual(linked.features.map(line => [line.key, line.pill]), [['AEON-211', 'Live text']])
  assert.deepEqual(linked.other.map(c => c.commit), ['b', 'd'])
  // A conventional prefix decides when the ticket's type is not known.
  const typed = presentChanges([change('f', 'fix', 'fix(AEON-9): repair', ['AEON-9'])], null, [item('AEON-9', 'Repaired', 'It works.')])
  assert.deepEqual([typed.features.length, typed.fixes.map(line => line.key)], [0, ['AEON-9']])
})

test('a server group wins over the commit type, and a version bump stays out', () => {
  const grouped = groupChanges([
    { ...change('1', 'other', 'P0.x: session chat (AEON-273)', ['AEON-273']), group: 'features' },
    { ...change('2', 'other', 'P0.x: quotes crash (AEON-274)', ['AEON-274']), group: 'fixes' },
    { ...change('3', 'other', 'P0.x: refresh the manifest'), group: 'other' },
    { ...change('4', 'feat', 'feat: conventional'), group: 'fixes' },
    { ...change('5', 'release', 'release: v1'), group: 'features' },
    change('6', 'fix', 'fix: still a fix when the manifest has no group'),
  ])
  assert.deepEqual(grouped.features.map(c => c.commit), ['1'])
  assert.deepEqual(grouped.fixes.map(c => c.commit), ['2', '4', '6'])
  assert.deepEqual(grouped.other.map(c => c.commit), ['3'])
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
  const noted = rel('260924000009.0.0', '', { changes: [{ ...change('n', 'other', 'P0.x: internal', ['AEON-1']), group: 'features', linked_tickets: [{ key: 'AEON-1', pill_en: 'Hosts in minutes', pill_de: 'Hosts in Minuten', benefit_en: 'You approve the price once.', benefit_de: 'Du bestätigst den Preis einmal.' }] }] })
  assert.ok(matches(noted, { ...f, q: 'approve the price' }))
  assert.ok(matches(noted, { ...f, q: 'minuten' }))
  assert.ok(!matches(r, { ...f, fixes: true })) // no visible fix line; the row counts it as other
  assert.ok(!matches(r, { ...f, features: true }))
  assert.ok(matches(r, { ...f, tickets: true }))
  assert.ok(!matches({ ...r, tickets: [], changes: [] }, { ...f, tickets: true }))
  assert.deepEqual(ticketsOf(r), ['AEON-77', 'AEON-78'])
})

test('feature and fix filters follow the visible ticket lines, in the viewer locale', () => {
  const f = { q: '', features: false, fixes: false, tickets: false }
  const bare = rel('260924000010.0.0', '', { changes: [{ ...change('a', 'feat', 'feat: no visible ticket', ['AEON-9']), group: 'features' as const }] })
  assert.equal(presentChanges(bare.changes).features.length, 0)
  assert.equal(matches(bare, { ...f, features: true }), false)
  const note = { key: 'AEON-1', pill_en: 'Hosts in minutes', pill_de: 'Hosts in Minuten', benefit_en: 'You approve the price once.', benefit_de: 'Du bestätigst den Preis einmal.' }
  const lined = rel('260924000009.0.0', '', { changes: [{ ...change('n', 'other', 'P0.x: internal', ['AEON-1']), group: 'features' as const, linked_tickets: [note] }] })
  assert.equal(matches(lined, { ...f, features: true }), true)
  assert.equal(matches(lined, { ...f, fixes: true }), false)
  const deOnly = rel('260924000011.0.0', '', { changes: [{ ...change('d', 'fix', 'fix: price', ['AEON-2']), group: 'fixes' as const, linked_tickets: [{ key: 'AEON-2', pill_en: ' ', pill_de: 'Preis', benefit_en: '', benefit_de: 'Du siehst den Preis.' }] }] })
  assert.equal(matches(deOnly, { ...f, fixes: true }), false)
  assert.equal(matches(deOnly, { ...f, fixes: true }, 'de-AT'), true)
  assert.equal(presentChanges(deOnly.changes, 'de-AT').fixes.length, 1)
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
