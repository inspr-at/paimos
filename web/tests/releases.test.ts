// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import { withPublicNoteItems } from '../src/lib/releases.ts'
import assert from 'node:assert/strict'
import { compare, displayHeadline, displayText, evidenceSearch, groupByDay, groupChanges, hasUsableNotes, idMatches, isCalendarVersion, liveServer, localizedNote, localizedPresentation, matches, newSince, pickText, plainSubject, presentChanges, presentCompare, presentRelease, railLine, releaseCopy, releaseLang, releaseLangKey, releaseName, releaseNotice, releaseTitle, releaseView, span, stats, technicalLine, ticketsOf, WRITTEN_AFTER_LABEL, writtenAfterLine, type Release } from '../src/lib/releases.ts'

const rel = (version: string, at: string, extra: Partial<Release> = {}): Release => ({
  version, tag: `v${version}`, release_channel: 'stable', release_sequence: 1, state: 'published', reserved_at: at, tagged_at: at, published_at: at,
  headline: `Release ${version}`, tickets: [], changes: [], changes_omitted: 0,
  evidence: { source_commit: 'abc1234def', source_url: '', image: null, ci: null, release_run: null, release_url: '', unavailable: [] }, ...extra,
})
const change = (commit: string, type: Release['changes'][number]['type'], subject: string, tickets: string[] = []) => ({ commit, type, subject, scope: '', tickets, at: '' })

test('portable public notes retain captured groups and language fallback without tenant IDs (AEON-372)', () => {
  const raw = rel('260929120000.0.0', '2026-09-29T12:00:00Z', {
    notes: { source: 'embedded-product-notes', snapshot_sha256: 'a'.repeat(64), captured_at: null, release_revision: 1, items: [], gaps: [], hidden: 0,
      public_items: [
        { key: 'AEON-7', group: 'fixes', pill_en: 'Frozen repair', pill_de: '', benefit_en: 'It works again.', benefit_de: '' },
        { key: 'AEON-8', group: 'other', pill_en: 'Internal maintenance', pill_de: '', benefit_en: '', benefit_de: '' },
      ],
    },
    changes: [change('a', 'other', 'AEON-7: repair', ['AEON-7']), change('b', 'other', 'AEON-8: maintenance', ['AEON-8'])],
  })
  const normalized = withPublicNoteItems(raw)
  const shown = presentRelease(normalized, 'de')
  assert.equal(shown.fixes[0]?.pill, 'Frozen repair')
  assert.equal(shown.fixes[0]?.pillLang, 'en')
  assert.deepEqual(shown.other.map(c => c.commit), ['b'])
  assert.equal(shown.features.length, 0)
  assert.equal(raw.notes?.items.length, 0)
  assert.equal(normalized.notes?.items[0]?.id, '')
  const emptyTenant = { ...raw, notes: { ...raw.notes!, source: 'database-snapshot', public_items: undefined, items: [] } }
  assert.equal(presentRelease(withPublicNoteItems(emptyTenant)).fixes.length, 0)
})

test('a capture without a group plus a bug ticket is a fix (AEON-372)', () => {
  const item = { id: '44444444-4444-4444-8444-444444444444', key: 'AEON-7', pill_en: 'Frozen repair', pill_de: '', benefit_en: 'Captured benefit.', benefit_de: '' }
  const notes = {
    source: 'database-snapshot', snapshot_sha256: 'a'.repeat(64), captured_at: '2026-09-28T12:00:00Z', release_revision: 1, gaps: [], hidden: 0,
    items: [item],
  }
  const live = { key: 'AEON-7', group: 'fixes' as const, pill_en: 'Live text', pill_de: '', benefit_en: 'Not the snapshot.', benefit_de: '' }
  const bug = { ...change('a', 'other', 'AEON-7: repair the release', ['AEON-7']), group: 'fixes' as const, linked_tickets: [live] }
  const shown = presentRelease(rel('260928120000.0.0', '2026-09-28T12:00:00Z', { notes, changes: [bug] }))
  assert.equal(shown.features.length, 0)
  assert.deepEqual(shown.fixes.map(line => [line.key, line.pill, line.benefit]), [['AEON-7', 'Frozen repair', 'Captured benefit.']])
  const recorded = presentRelease(rel('260928120000.0.0', '2026-09-28T12:00:00Z', {
    notes: { ...notes, items: [{ ...item, group: 'fixes' as const }] },
    changes: [change('a', 'other', 'AEON-7: repair the release', ['AEON-7'])],
  }))
  assert.equal(recorded.features.length, 0)
  assert.deepEqual(recorded.fixes.map(line => [line.key, line.pill]), [['AEON-7', 'Frozen repair']])
})

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

test('each ticket is a feature or a fix of its own, also when one commit names several (AEON-305)', () => {
  const item = (key: string, pill: string) => ({ id: key, key, pill_en: pill, pill_de: '', benefit_en: `${pill}.`, benefit_de: '' })
  const note = (key: string, pill: string, group?: 'features' | 'fixes') => ({ key, pill_en: pill, pill_de: '', benefit_en: `${pill}.`, benefit_de: '', ...(group ? { group } : {}) })
  const f = { q: '', features: false, fixes: true, tickets: false }
  // Two bugs share one commit: both are fixes, on the snapshot and the current path.
  const bugs = (group?: 'fixes') => [{ ...change('s', 'other', 'P0.x: quotes and chat recover (AEON-274, AEON-275)', ['AEON-274', 'AEON-275']), group: 'fixes' as const, linked_tickets: [note('AEON-274', 'Quotes open', group), note('AEON-275', 'Chat recovers', group)] }]
  const notes = { source: 'database-snapshot', snapshot_sha256: 's', captured_at: null, release_revision: 1, gaps: [], hidden: 0, items: [item('AEON-274', 'Quotes open'), item('AEON-275', 'Chat recovers')] }
  for (const group of ['fixes', undefined] as const) {
    const current = rel('2', '2026-09-29T10:00:00Z', { changes: bugs(group) })
    const snapshot = { ...current, notes }
    for (const r of [current, snapshot]) {
      const p = presentRelease(r)
      assert.deepEqual([p.features.length, p.fixes.map(line => line.key), p.other.length], [0, ['AEON-274', 'AEON-275'], 0], `${group} ${r.notes ? 'snapshot' : 'current'}`)
      assert.equal(matches(r, f), true)
      assert.equal(matches(r, { ...f, fixes: false, features: true }), false)
    }
    // Compare reads the same blocks.
    const c = compare([current, rel('1', '2026-09-28T10:00:00Z')], '2', '1')
    assert.deepEqual(presentChanges(c.changes).fixes.map(line => line.key), ['AEON-274', 'AEON-275'])
  }
  // A bug and a feature share a commit: the server's per-ticket group places each.
  const mixed = [{ ...change('m', 'other', 'P0.x: quotes and chat (AEON-274, AEON-273)', ['AEON-274', 'AEON-273']), group: 'fixes' as const, linked_tickets: [note('AEON-274', 'Quotes open', 'fixes'), note('AEON-273', 'Session chat', 'features')] }]
  for (const r of [rel('3', '', { changes: mixed }), rel('3', '', { changes: mixed, notes: { ...notes, items: [item('AEON-274', 'Quotes open'), item('AEON-273', 'Session chat')] } })]) {
    const p = presentRelease(r)
    assert.deepEqual([p.features.map(line => line.key), p.fixes.map(line => line.key)], [['AEON-273'], ['AEON-274']])
    assert.equal(matches(r, f), true)
    assert.equal(matches(r, { ...f, fixes: false, features: true }), true)
  }
  // The ticket's own group wins over a conventional prefix on a shared commit.
  const prefixed = presentChanges([{ ...change('p', 'feat', 'feat: quotes and chat (AEON-274, AEON-273)', ['AEON-274', 'AEON-273']), group: 'features' as const, linked_tickets: [note('AEON-274', 'Quotes open', 'fixes'), note('AEON-273', 'Session chat', 'features')] }])
  assert.deepEqual([prefixed.features.map(line => line.key), prefixed.fixes.map(line => line.key)], [['AEON-273'], ['AEON-274']])
})

test('compare follows the server group without note text, and a shared commit stays there (AEON-386)', () => {
  const frozen = (key: string, group: 'features' | 'fixes', pill: string, benefit: string) => ({ key, group, pill_en: pill, pill_de: '', benefit_en: benefit, benefit_de: '' })
  const changes = [
    { ...change('g', 'other', 'AEON-20: frozen group', ['AEON-20']), group: 'fixes' as const, linked_tickets: [frozen('AEON-20', 'fixes', 'Frozen kept', 'Stays a fix.')] },
    { ...change('h', 'other', 'AEON-99: outside a grouped capture', ['AEON-99']), group: 'fixes' as const },
    { ...change('i', 'other', 'AEON-97: benefit outside a grouped capture', ['AEON-97']), group: 'features' as const },
    { ...change('j', 'other', 'AEON-98: hidden member of a grouped capture', ['AEON-98']), group: 'fixes' as const },
    { ...change('k', 'other', 'AEON-21: shared with a hidden bug', ['AEON-21', 'AEON-98']), group: 'fixes' as const, linked_tickets: [frozen('AEON-21', 'features', 'Frozen feature', 'Stays a feature.')] },
    change('z', 'release', 'release: v1'),
  ]
  const shown = presentCompare(changes)
  assert.deepEqual(shown.features.map(line => [line.key, line.pill, line.commits.map(c => c.commit)]), [['AEON-97', '', ['i']]])
  assert.deepEqual(shown.fixes.map(line => [line.key, line.pill, line.commits.map(c => c.commit)]), [
    ['AEON-20', 'Frozen kept', ['g']],
    ['AEON-99', '', ['h']],
    ['AEON-98', '', ['j', 'k']],
    ['AEON-21', 'Frozen feature', ['k']],
  ])
  assert.equal(shown.fixes.find(line => line.key === 'AEON-21')?.benefit, 'Stays a feature.')
  assert.deepEqual(shown.other, [])
  assert.equal(JSON.stringify(shown).includes('LIVE'), false)
  // German is not invented for frozen English text.
  const de = presentCompare(changes, 'de-AT')
  assert.equal(de.fixes.find(line => line.key === 'AEON-21')?.pillLang, 'en')
  // Highlights still require told text, so the same payload does not move.
  const highlights = presentChanges(changes)
  assert.deepEqual(highlights.features.map(line => line.key), ['AEON-21'])
  assert.deepEqual(highlights.fixes.map(line => line.key), ['AEON-20'])
  assert.deepEqual(highlights.other.map(c => c.commit), ['h', 'i', 'j'])
  const notes = {
    source: 'database-snapshot', snapshot_sha256: 'ab', captured_at: '2026-09-26T12:00:00Z', release_revision: 1, gaps: [], hidden: 1,
    items: [
      { id: '20', key: 'AEON-20', group: 'fixes' as const, pill_en: 'Frozen kept', pill_de: '', benefit_en: 'Stays a fix.', benefit_de: '' },
      { id: '21', key: 'AEON-21', group: 'features' as const, pill_en: 'Frozen feature', pill_de: '', benefit_en: 'Stays a feature.', benefit_de: '' },
    ],
  }
  const detail = presentRelease(rel('260926120000.0.0', '2026-09-26T12:00:00Z', { notes, changes }))
  assert.deepEqual(detail.features.map(line => [line.key, line.pill, line.commits.map(c => c.commit)]), [['AEON-21', 'Frozen feature', ['k']]])
  assert.deepEqual(detail.other.map(c => c.commit), ['h', 'i', 'j'])
  // No server group: Compare still reads told tickets, and a bare feat stays Other.
  const legacy = [
    { ...change('1', 'other', 'P0.x: session chat (AEON-273)', ['AEON-273']), linked_tickets: [{ key: 'AEON-273', pill_en: 'Session chat', pill_de: '', benefit_en: 'The chat stays put.', benefit_de: '' }] },
    change('2', 'feat', 'feat: plain'),
  ]
  assert.deepEqual(presentCompare(legacy), presentChanges(legacy))
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
  // The tag message is evidence: Details shows it, Highlights does not.
  assert.ok(matches(r, { ...f, q: 'journey' }, null, 'details'))
  assert.ok(!matches(r, { ...f, q: 'journey' }))
  assert.ok(matches(r, { ...f, q: 'repair' }))
  assert.ok(matches(r, { ...f, q: 'aeon-78' }))
  assert.ok(!matches(r, { ...f, q: 'hours' }))
  const noted = rel('260924000009.0.0', '', { changes: [{ ...change('n', 'other', 'P0.x: internal', ['AEON-1']), group: 'features', linked_tickets: [{ key: 'AEON-1', pill_en: 'Hosts in minutes', pill_de: 'Hosts in Minuten', benefit_en: 'You approve the price once.', benefit_de: 'Du bestätigst den Preis einmal.' }] }] })
  assert.ok(matches(noted, { ...f, q: 'approve the price' }))
  assert.ok(!matches(noted, { ...f, q: 'minuten' }))
  assert.ok(matches(noted, { ...f, q: 'minuten' }, 'de'))
  assert.ok(!matches(r, { ...f, fixes: true })) // no visible fix line; the row counts it as other
  assert.ok(!matches(r, { ...f, features: true }))
  assert.ok(matches(r, { ...f, tickets: true }))
  assert.ok(!matches({ ...r, tickets: [], changes: [] }, { ...f, tickets: true }))
  assert.deepEqual(ticketsOf(r), ['AEON-77', 'AEON-78'])
})

test('search finds a release by its codename in either language (AEON-430)', () => {
  const r = rel('260930074921.0.0', '', { release_sequence: 111, codename: 'First Force' })
  const f = { q: '', features: false, fixes: false, tickets: false }
  assert.ok(matches(r, { ...f, q: 'first for' }))
  assert.ok(matches(r, { ...f, q: 'FORCE' }, 'de', 'details'))
  assert.ok(!matches(r, { ...f, q: 'aqua' }))
  assert.ok(!matches({ ...r, codename: undefined }, { ...f, q: 'force' }))
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
  // Never blank: English falls back to the German pill (AEON-323).
  assert.equal(matches(deOnly, { ...f, fixes: true }), true)
  assert.equal(presentChanges(deOnly.changes).fixes[0]?.pillLang, 'de')
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
  // The tag message is evidence, which Details shows (AEON-323).
  for (const q of ['', 'journey', 'repair', 'aeon-78']) {
    const f = { q, features: false, fixes: false, tickets: true }
    assert.equal(matches(regenerated, f), matches(archived, f))
    assert.equal(matches(regenerated, f, null, 'details'), matches(archived, f, null, 'details'))
    assert.equal(matches(regenerated, f, null, 'details'), true)
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

const PAGE = '260929095359.0.0'
const SERVER = '260929113854.0.0'

test('an outdated page gets the update notice, including when the server version is missing from history', () => {
  assert.equal(isCalendarVersion(PAGE), true)
  assert.equal(isCalendarVersion('dev'), false)
  assert.equal(isCalendarVersion(''), false)
  assert.equal(releaseNotice(PAGE, SERVER, SERVER), 'update')
  assert.equal(releaseNotice(PAGE, SERVER, ''), 'update')
  assert.equal(releaseNotice(PAGE, SERVER, '260101120000.0.0'), 'update')
  assert.equal(releaseNotice(PAGE, PAGE, SERVER), 'missing')
  assert.equal(releaseNotice(PAGE, PAGE, ''), null)
  assert.equal(releaseNotice(SERVER, PAGE, ''), null)
  assert.equal(releaseNotice('dev', SERVER, ''), null)
  assert.equal(releaseNotice('dev', SERVER, 'nope'), 'missing')
  assert.equal(releaseNotice(null, SERVER, ''), null)
  assert.equal(releaseNotice(PAGE, 'dev', '260101120000.0.0'), 'missing')
})

test('a history cached before the deploy still says a newer version is live', () => {
  assert.equal(liveServer(PAGE, SERVER), SERVER)
  assert.equal(liveServer(SERVER, PAGE), SERVER)
  assert.equal(liveServer(PAGE, null), PAGE)
  assert.equal(liveServer(PAGE, ''), PAGE)
  assert.equal(liveServer('dev', SERVER), SERVER)
  assert.equal(liveServer(SERVER, 'dev'), SERVER)
})

test('language and view: the address, then the remembered choice, then the profile; Highlights by default', () => {
  assert.equal(releaseLang(undefined, 'de-AT'), 'de')
  assert.equal(releaseLang('en', 'de-AT', 'de'), 'en')
  assert.equal(releaseLang(undefined, 'de-AT', 'en'), 'en')
  assert.equal(releaseLang(undefined, 'en-GB', 'de'), 'de')
  assert.equal(releaseLang(undefined, 'en-GB', 'fr'), 'en')
  assert.equal(releaseLang('fr', 'en-GB'), 'en')
  assert.equal(releaseLang(['de'], null), 'de')
  assert.equal(releaseView(undefined), 'highlights')
  assert.equal(releaseView('details'), 'details')
  assert.equal(releaseView('technical'), 'highlights')
  assert.equal(releaseLangKey('p-1'), 'aeon.release-history.lang.p-1')
  assert.match(technicalLine(rel('1', '2026-09-29T12:00:00Z', { changes: [change('a'.repeat(40), 'feat', 'feat(AEON-1): phone state line'), change('b'.repeat(40), 'fix', 'fix: keep the cue')] })), /Phone state line · Keep the cue/)
  assert.equal(writtenAfterLine('de'), 'Notizen nach dem Release geschrieben')
  assert.equal(writtenAfterLine('en'), WRITTEN_AFTER_LABEL)
})

test('a missing translation falls back to the other language, never blank, and says which', () => {
  assert.deepEqual(pickText('Wide lists', 'Breite Listen', 'de'), { text: 'Breite Listen', lang: 'de' })
  assert.deepEqual(pickText('Wide lists', '  ', 'de-AT'), { text: 'Wide lists', lang: 'en' })
  assert.deepEqual(pickText('', 'Breite Listen', 'en'), { text: 'Breite Listen', lang: 'de' })
  assert.deepEqual(pickText(' ', '', 'de'), { text: '', lang: 'de' })
  assert.deepEqual(pickText(undefined, null, 'en'), { text: '', lang: 'en' })
  // Pill and benefit fall back on their own.
  const note = { pill_en: 'Wide lists', pill_de: 'Breite Listen', benefit_en: 'More columns fit.', benefit_de: '' }
  assert.deepEqual(localizedNote(note, 'de'), { pill: 'Breite Listen', benefit: 'More columns fit.', pillLang: 'de', benefitLang: 'en' })
  assert.deepEqual(localizedNote({ ...note, pill_en: '' }, 'en'), { pill: 'Breite Listen', benefit: 'More columns fit.', pillLang: 'de', benefitLang: 'en' })
  // The header too, and a German-only presentation still names the release in English.
  const presentation = { theme_en: 'Lists', theme_de: '', headline_en: '', headline_de: 'Alles auf einen Blick', intro_en: 'Read more.', intro_de: 'Mehr lesen.', revision: 1, updated_at: '' }
  const named = localizedPresentation({ presentation }, 'en')!
  assert.deepEqual([named.theme, named.themeLang, named.headline, named.headlineLang, named.intro, named.introLang], ['Lists', 'en', 'Alles auf einen Blick', 'de', 'Read more.', 'en'])
  const german = localizedPresentation({ presentation }, 'de')!
  assert.deepEqual([german.theme, german.themeLang, german.headlineLang, german.introLang], ['Lists', 'en', 'de', 'de'])
  assert.equal(localizedPresentation({ presentation: { ...presentation, headline_de: '' } }, 'de'), null)
  assert.equal(releaseTitle({ presentation, changes: [] }, 'en'), 'Lists')
})

test('search follows the chosen language and view', () => {
  const f = { q: '', features: false, fixes: false, tickets: false }
  const note = { key: 'AEON-5', pill_en: 'Wide lists', pill_de: 'Breite Listen', benefit_en: 'More columns fit on a laptop.', benefit_de: 'Mehr Spalten passen auf einen Laptop.' }
  const r = rel('260924000012.0.0', '', { headline: 'Stable112 tag', tickets: ['AEON-5'], changes: [
    { ...change('a'.repeat(40), 'feat', 'P0.x: widen the grid (AEON-5)', ['AEON-5']), group: 'features' as const, linked_tickets: [note] },
    change('b'.repeat(40), 'chore', 'P0.x: pin the vendor hash'),
  ] })
  const hit = (q: string, lang: string, view: 'highlights' | 'details') => matches(r, { ...f, q }, lang, view)
  // Pills, ticket keys, version and commit subjects in both views.
  for (const view of ['highlights', 'details'] as const) {
    assert.ok(hit('wide lists', 'en', view))
    assert.ok(hit('breite', 'de', view))
    assert.ok(hit('aeon-5', 'en', view))
    assert.ok(hit('widen the grid', 'en', view))
    assert.ok(hit('vendor hash', 'en', view))
    assert.ok(hit('260924000012', 'en', view))
  }
  // Benefits only where Highlights shows them; the tag message only in Details' evidence.
  assert.ok(hit('laptop', 'en', 'highlights'))
  assert.ok(!hit('laptop', 'en', 'details'))
  assert.ok(!hit('stable112', 'en', 'highlights'))
  assert.ok(hit('stable112', 'en', 'details'))
  // The other language's words are not on screen.
  assert.ok(!hit('breite', 'en', 'highlights'))
  assert.ok(!hit('wide lists', 'de', 'details'))
})

test('Details search finds commit SHAs from their start and the evidence it shows', () => {
  const f = { q: '', features: false, fixes: false, tickets: false }
  const r = rel('260929113854.0.0', '', { headline: 'Stable105', changes: [
    change('5a622e2c0ffee00000000000000000000000000a', 'feat', 'P0.x: tidy dead sessions', []),
  ], notes: { source: 'database-snapshot', snapshot_sha256: 'feedbeef'.repeat(8), captured_at: null, release_revision: 1, items: [], gaps: [], hidden: 0 },
  evidence: {
    source_commit: '9f8e7d6c5b4a39281706f5e4d3c2b1a098765432', source_url: '', release_url: '',
    image: { reference: 'ghcr.io/markus-barta/aeon:260929113854.0.0', digest: 'sha256:0123abcd'.padEnd(71, '9') },
    ci: { name: 'Web and Go checks', url: '', status: 'completed', conclusion: 'success' },
    release_run: { name: 'Release image', url: '', status: 'in_progress', conclusion: '' },
    unavailable: ['The GitHub release is not reachable.'],
  } })
  const hit = (q: string, view: 'highlights' | 'details') => matches(r, { ...f, q }, 'en', view)
  // Commit SHAs, short or full, in both views (Highlights folds them, Other shows them).
  for (const view of ['highlights', 'details'] as const) {
    assert.ok(hit('5a622e2', view))
    assert.ok(hit('5A622E2C0FFEE', view))
  }
  // From the start and from four characters: a word inside a hash is no hit.
  assert.ok(!hit('c0ffee', 'details'))
  assert.ok(!hit('5a6', 'details'))
  assert.ok(idMatches('sha256:0123abcd99', 'sha256:0123'))
  assert.ok(idMatches('sha256:0123abcd99', '0123abc'))
  assert.ok(!idMatches('deadbeef', 'beef'))
  // Evidence as Details shows it open: tag message, source, snapshot, source
  // commit, runs by name and outcome, image, digest and what is unavailable.
  for (const q of ['stable105', 'database-snapshot', 'feedbeef', '9f8e7d6', 'web and go checks', 'passed', 'in progress', 'release image', 'ghcr.io/markus-barta', '0123abcd', 'not reachable']) {
    assert.ok(hit(q, 'details'), q)
    assert.ok(!hit(q, 'highlights'), q)
  }
  assert.deepEqual(evidenceSearch(rel('1.0.0', '', { headline: '', tag: '' })), { texts: [], ids: ['abc1234def'] })
})

test('names in the rail and in Compare say when they fell back to the other language', () => {
  const english = { key: 'AEON-291', pill_en: 'Dead sessions tidy up', pill_de: '', benefit_en: 'Old sessions go.', benefit_de: '' }
  const german = { key: 'AEON-5', pill_en: 'Wide lists', pill_de: 'Breite Listen', benefit_en: '', benefit_de: '' }
  const only = rel('2.0.0', '', { changes: [{ ...change('c'.repeat(40), 'feat', 'x (AEON-291)', ['AEON-291']), group: 'features' as const, linked_tickets: [english] }] })
  assert.deepEqual(railLine(only, 'de'), { text: 'Dead sessions tidy up', themed: false, lang: 'en' })
  assert.deepEqual(railLine(only, 'en'), { text: 'Dead sessions tidy up', themed: false, lang: 'en' })
  assert.deepEqual(releaseName(only, 'de'), { text: 'Dead sessions tidy up', lang: 'en' })
  // Mixed: the line names the language any part fell back to.
  const both = rel('3.0.0', '', { changes: [
    { ...change('d'.repeat(40), 'feat', 'y (AEON-5)', ['AEON-5']), group: 'features' as const, linked_tickets: [german] },
    { ...change('e'.repeat(40), 'fix', 'z (AEON-291)', ['AEON-291']), group: 'fixes' as const, linked_tickets: [english] },
  ] })
  assert.deepEqual(railLine(both, 'de'), { text: 'Breite Listen · Dead sessions tidy up', themed: false, lang: 'en' })
  assert.deepEqual(releaseName(both, 'de'), { text: 'Breite Listen', lang: 'de' })
  // A theme that fell back says so too.
  const presentation = { theme_en: 'Lists', theme_de: '', headline_en: 'All at a glance', headline_de: 'Alles auf einen Blick', intro_en: '', intro_de: '', revision: 1, updated_at: '' }
  assert.deepEqual(railLine(rel('4.0.0', '', { presentation }), 'de'), { text: 'Lists', themed: true, lang: 'en' })
  assert.deepEqual(releaseName(rel('4.0.0', '', { presentation: { ...presentation, theme_en: '' } }), 'de'), { text: 'Alles auf einen Blick', lang: 'de' })
})

test('empty and compare lines follow the chosen language', () => {
  const en = releaseCopy('en'), de = releaseCopy('de')
  assert.equal(en.noChanges, 'No changes are recorded between this release and the one before it.')
  assert.equal(de.noChanges, 'Zwischen diesem und dem vorherigen Release sind keine Änderungen verzeichnet.')
  assert.equal(de.nothingShipped, 'Unter dieser Version wurde nichts ausgeliefert.')
  assert.equal(de.compareNone, 'Zwischen diesen Releases sind keine Änderungen verzeichnet.')
  assert.equal(en.omitted(1), 'And 1 more change not listed here.')
  assert.equal(de.omitted(3), '3 weitere Änderungen sind hier nicht aufgeführt.')
  assert.equal(de.noMatchText(12, 'x'), 'Nichts in 12 Releases passt zu „x“.')
  assert.equal(en.noMatchText(12, ''), 'Nothing in 12 releases fits these filters.')
  // Every line exists in both languages and differs.
  for (const key of Object.keys(en) as (keyof typeof en)[]) assert.notDeepEqual(String(en[key]), String(de[key]), key)
  assert.equal(releaseCopy('de-AT').comparePick, 'Zweites Release wählen')
})
