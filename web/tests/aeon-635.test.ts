// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { matches, presentRelease, type LinkedTicket, type Release, type ReleaseChange, type ReleaseFilter } from '../src/lib/releases.ts'

const at = '2026-10-03T12:00:00Z'
const release = (changes: ReleaseChange[], extra: Partial<Release> = {}): Release => ({
  version: '261003120000.0.0', tag: 'v261003120000.0.0', release_channel: 'stable', release_sequence: 121,
  state: 'published', reserved_at: at, tagged_at: at, published_at: at, headline: '', tickets: [], changes, changes_omitted: 0,
  evidence: { source_commit: 'a'.repeat(40), source_url: '', image: null, ci: null, release_run: null, release_url: '', unavailable: [] },
  ...extra,
})
const change = (commit: string, type: ReleaseChange['type'], extra: Partial<ReleaseChange> = {}): ReleaseChange => ({
  commit, type, subject: `${type}: ${commit}`, scope: '', tickets: [], at, ...extra,
})
const note = (key: string, group: 'features' | 'fixes'): LinkedTicket => ({
  key, group, pill_en: `${key} note`, pill_de: `${key} Notiz`, benefit_en: 'Captured benefit.', benefit_de: 'Festgehaltener Nutzen.',
})
const filter = (extra: Partial<ReleaseFilter> = {}): ReleaseFilter => ({ q: '', features: false, fixes: false, other: false, ...extra })

test('Other follows the displayed groups when an untold feature or fix is shown as maintenance (AEON-635)', () => {
  for (const type of ['feat', 'fix'] as const) {
    const r = release([change(type, type, { group: type === 'feat' ? 'features' : 'fixes', tickets: ['AEON-99'] })])
    const shown = presentRelease(r)
    assert.deepEqual([shown.features.length, shown.fixes.length, shown.other.map(c => c.commit)], [0, 0, [type]])
    assert.equal(matches(r, filter({ other: true })), true)
    assert.equal(matches(r, filter({ features: true })), false)
    assert.equal(matches(r, filter({ fixes: true })), false)
  }
})

test('a named ticket claims all its commits and does not make the release match Other (AEON-635)', () => {
  const feature = note('AEON-305', 'features')
  const r = release([
    change('feature', 'other', { group: 'features', tickets: [feature.key], linked_tickets: [feature] }),
    change('test', 'test', { group: 'other', tickets: [feature.key] }),
    change('version', 'release', { tickets: [feature.key] }),
  ])
  assert.deepEqual(presentRelease(r).features.map(line => line.commits.map(c => c.commit)), [['feature', 'test']])
  assert.equal(presentRelease(r).other.length, 0)
  assert.equal(matches(r, filter({ features: true })), true)
  assert.equal(matches(r, filter({ other: true })), false)
})

test('the three Show filters combine and search still narrows a matching release (AEON-635)', () => {
  const feature = note('AEON-305', 'features'), fix = note('AEON-301', 'fixes')
  const r = release([
    change('shared', 'feat', { group: 'features', tickets: [feature.key, fix.key], linked_tickets: [feature, fix] }),
    change('maintenance', 'docs', { subject: 'docs: document tenant isolation' }),
  ])
  assert.deepEqual([presentRelease(r).features.length, presentRelease(r).fixes.length, presentRelease(r).other.length], [1, 1, 1])
  const all = filter({ features: true, fixes: true, other: true })
  assert.equal(matches(r, all), true)
  assert.equal(matches(r, { ...all, q: 'tenant isolation' }), true)
  assert.equal(matches(r, { ...all, q: 'missing content' }), false)
  assert.equal(matches(release(r.changes.slice(0, 1)), all), false)
})

test('frozen notes control Other membership even when live linked text exists (AEON-635)', () => {
  const feature = note('AEON-305', 'features'), fix = note('AEON-301', 'fixes')
  const changes = [
    change('feature', 'feat', { tickets: [feature.key], linked_tickets: [feature] }),
    change('fix', 'fix', { tickets: [fix.key], linked_tickets: [fix] }),
  ]
  const r = release(changes, {
    notes: { source: 'database-snapshot', snapshot_sha256: 'b'.repeat(64), captured_at: at, release_revision: 1, items: [{ id: 'captured', ...feature }], gaps: [], hidden: 1 },
  })
  assert.deepEqual(presentRelease(r).other.map(c => c.commit), ['fix'])
  for (const lang of ['en', 'de'] as const) {
    for (const view of ['highlights', 'details'] as const) {
      assert.equal(matches(r, filter({ other: true }), lang, view), true)
      assert.equal(matches(r, filter({ fixes: true }), lang, view), false)
    }
  }
  assert.equal(matches(release(changes), filter({ other: true })), false)
})

test('a release-only or empty release never matches Other and older callers may omit it (AEON-635)', () => {
  for (const r of [release([]), release([change('version', 'release', { group: 'other' })])]) {
    assert.deepEqual(presentRelease(r), { features: [], fixes: [], other: [] })
    assert.equal(matches(r, filter({ other: true })), false)
    assert.equal(matches(r, { q: '', features: false, fixes: false, tickets: false }), true)
  }
})
