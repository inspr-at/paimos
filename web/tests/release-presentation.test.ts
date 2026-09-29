// SPDX-License-Identifier: AGPL-3.0-only
// AEON-305: a release introduces itself (theme, headline, intro) and tells its
// benefits as rows, fixes apart, commits last. Tag headlines are evidence only.
import test from 'node:test'
import assert from 'node:assert/strict'
import { localizedPresentation, markParts, matches, railLine, releaseStory, releaseTitle, type Release, type ReleaseChange } from '../src/lib/releases.ts'

const linked = (key: string, pill: string, benefit: string) => ({ key, pill_en: pill, pill_de: '', benefit_en: benefit, benefit_de: '' })
const change = (i: number, group: ReleaseChange['group'], key?: string, pill = '', benefit = ''): ReleaseChange => ({
  commit: `${i}`.padEnd(40, 'a'), subject: `P0.x: change ${i}${key ? ` (${key})` : ''}`, type: 'other', scope: '', tickets: key ? [key] : [], at: '2026-09-29T08:00:00Z',
  ...(group ? { group } : {}), ...(key && (pill || benefit) ? { linked_tickets: [linked(key, pill, benefit)] } : {}),
})
function release(extra: Partial<Release> = {}): Release {
  return {
    version: '260929082208.0.0', tag: 'v260929082208.0.0', release_channel: 'stable', release_sequence: 102, state: 'published',
    reserved_at: null, tagged_at: null, published_at: null, headline: 'Stable102', tickets: [], changes: [], changes_omitted: 0,
    evidence: { source_commit: '', source_url: '', image: null, ci: null, release_run: null, release_url: '', unavailable: [] }, ...extra,
  }
}
const presentation = { theme_en: 'Releases with a name', theme_de: 'Releases mit Namen', headline_en: 'Every release says what it is about.', headline_de: '', intro_en: 'A theme and an intro.', intro_de: 'Ein Thema und eine Einführung.', revision: 1, updated_at: '2026-09-29T08:30:00Z' }

test('presentation follows the viewer language and falls back to English per field', () => {
  assert.equal(localizedPresentation(release()), null)
  const en = localizedPresentation(release({ presentation }))!
  assert.deepEqual([en.theme, en.headline, en.intro, en.themeLang], ['Releases with a name', 'Every release says what it is about.', 'A theme and an intro.', 'en'])
  const de = localizedPresentation(release({ presentation }), 'de-AT')!
  assert.deepEqual([de.theme, de.themeLang, de.headline, de.headlineLang, de.intro, de.introLang], ['Releases mit Namen', 'de', 'Every release says what it is about.', 'en', 'Ein Thema und eine Einführung.', 'de'])
  assert.equal(localizedPresentation(release({ presentation: { ...presentation, headline_en: '  ' } })), null)
})

test('the story lists benefits once, fixes apart, and every commit but the version bump', () => {
  const changes = [
    { ...change(0, undefined), type: 'release' as const },
    change(1, 'features', 'AEON-305', 'Named releases', 'Every release opens with its theme.'),
    change(2, 'features', 'AEON-305', 'Named releases', 'Every release opens with its theme.'),
    change(3, 'fixes', 'AEON-301', 'Search stays put', 'Search stays put'),
    change(4, 'fixes', 'AEON-305', 'Named releases', 'Every release opens with its theme.'),
    change(5, 'other'),
  ]
  const story = releaseStory(release({ changes }))
  assert.deepEqual(story.benefits.map(r => [r.key, r.label, r.detail]), [['AEON-305', 'Named releases', 'Every release opens with its theme.']])
  // A benefit that only repeats its pill is not shown twice.
  assert.deepEqual(story.fixes.map(r => [r.key, r.label, r.detail]), [['AEON-301', 'Search stays put', '']])
  assert.equal(story.commits.length, 5)
})

test('a captured snapshot decides membership and text; fix tickets move to Fixes', () => {
  const notes = { source: 'tag-snapshot', snapshot_sha256: 'x', captured_at: null, release_revision: 1, gaps: [], hidden: 0, items: [
    { id: '1', key: 'AEON-1', pill_en: 'Snapshot pill', pill_de: 'Schnappschuss', benefit_en: 'Snapshot benefit.', benefit_de: '' },
    { id: '2', key: 'AEON-2', pill_en: 'Fixed thing', pill_de: '', benefit_en: 'It works again.', benefit_de: '' },
  ] }
  const changes = [change(1, 'features', 'AEON-1', 'Live pill', 'Live benefit.'), change(2, 'fixes', 'AEON-2', 'Live fix', 'Live fix benefit.'), change(3, 'features', 'AEON-9', 'Not a member', 'Not in the snapshot.')]
  const story = releaseStory(release({ notes, changes }), 'de')
  assert.deepEqual(story.benefits.map(r => [r.label, r.labelLang, r.detail, r.detailLang]), [['Schnappschuss', 'de', 'Snapshot benefit.', 'en']])
  assert.deepEqual(story.fixes.map(r => r.label), ['Fixed thing'])
})

test('without a presentation the rail and titles never fall back to the tag message label', () => {
  const plain = release({ changes: [change(1, 'other')] })
  assert.deepEqual(railLine(plain), { text: '', themed: false })
  const benefits = release({ changes: [change(1, 'features', 'AEON-74', 'Wide lists', 'More columns.'), change(2, 'fixes', 'AEON-75', 'Week total', 'Stays put.')] })
  assert.deepEqual(railLine(benefits), { text: 'Wide lists · Week total', themed: false })
  const named = release({ presentation, changes: benefits.changes })
  assert.deepEqual(railLine(named), { text: 'Releases with a name', themed: true })
  assert.equal(releaseTitle(named, 'de'), 'Releases mit Namen')
  assert.equal(releaseTitle(release({ headline: 'wide lists (AEON-74)', tickets: ['AEON-74'] })), 'Wide lists')
})

test('search finds presentation text and marks every hit', () => {
  const named = release({ presentation })
  const filter = { q: 'einführung', features: false, fixes: false, tickets: false }
  assert.equal(matches(named, filter), true)
  assert.equal(matches(release(), filter), false)
  assert.deepEqual(markParts('Name the name', 'NAME'), [{ text: 'Name', hit: true }, { text: ' the ', hit: false }, { text: 'name', hit: true }])
  assert.deepEqual(markParts('plain', ''), [{ text: 'plain', hit: false }])
})

test('ticket blocks keep each ticket\'s changes together under its pill, loose changes apart', async () => {
  const { ticketBlocks } = await import('../src/lib/releases.ts')
  const changes = [
    change(1, 'features', 'AEON-305', 'Named releases', 'Every release opens with its theme.'),
    change(2, 'features'),
    change(3, 'features', 'AEON-289', 'Changes by ticket', 'Once per ticket.'),
    change(4, 'features', 'AEON-305', 'Named releases', 'Every release opens with its theme.'),
    change(5, 'features', 'AEON-9'),
  ]
  const blocks = ticketBlocks(changes)
  assert.deepEqual(blocks.map(b => [b.key, b.pill, b.changes.length]), [['AEON-305', 'Named releases', 2], ['', '', 2], ['AEON-289', 'Changes by ticket', 1]])
  const de = ticketBlocks([change(1, 'features', 'AEON-1', 'Pill', 'Benefit.')].map(c => ({ ...c, linked_tickets: [{ ...c.linked_tickets![0]!, pill_de: 'Pille' }] })), 'de')
  assert.deepEqual([de[0]!.pill, de[0]!.pillLang], ['Pille', 'de'])
})
