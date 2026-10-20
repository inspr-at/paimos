// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1401. The risk: a list or menu shows the wrong words for a status (the
// other locale's name, a stale name), or the card lets two statuses read alike.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { cleanStatusNames, readStatusNames, setStatusNames, statusName, statusNameKey, statusNameProblems } from '../src/lib/statusNames.ts'
import { statusMeta } from '../src/lib/work.ts'
import { valueLabel } from '../src/lib/ticketList.ts'

test('statuses read as the workspace names them, per locale, with built-in fallback', () => {
  setStatusNames({ in_progress: { en: 'Building' }, qa: { de: 'Prüfung' }, queued: { en: 'Up next' } })
  try {
    assert.equal(statusMeta('in_progress').label, 'Building')
    assert.equal(statusMeta('in-progress').label, 'Building')
    assert.equal(statusMeta('in_progress').builtIn, 'In progress')
    assert.equal(statusMeta('in_progress').key, 'progress')
    assert.equal(statusMeta('qa').label, 'QA')
    assert.equal(valueLabel('status', 'queued'), 'Up next')
    // The German UI with only an English rename keeps the German built-in name.
    assert.equal(statusName('in_progress', 'de'), 'In Arbeit')
    assert.equal(statusName('qa', 'de'), 'Prüfung')
    assert.equal(statusNameKey('active'), 'in_progress')
    assert.equal(statusNameKey('canceled'), 'cancelled')
    assert.equal(statusNameKey('recurring'), null)
  } finally { setStatusNames({}) }
  assert.equal(statusMeta('in_progress').label, 'In progress')
  // An unexpected answer reads as no renames instead of breaking every label.
  assert.deepEqual(readStatusNames({ revision: 'x', names: { in_progress: { en: 7 }, bogus: { en: 'X' } } }), { revision: 0, names: {} })
})

test('the card refuses long names and names another status reads as', () => {
  const long = 'x'.repeat(25)
  assert.deepEqual([...statusNameProblems({ in_progress: { en: long } })], [['in_progress.en', { code: 'too_long', name: long }]])
  assert.equal(statusNameProblems({ in_progress: { en: 'x'.repeat(24) } }).size, 0)
  assert.deepEqual([...statusNameProblems({ in_progress: { en: 'Done' } })], [['in_progress.en', { code: 'taken', name: 'Done' }]])
  assert.deepEqual([...statusNameProblems({ in_progress: { en: 'done' } })].map(([field]) => field), ['in_progress.en'])
  // A clash between two renames is blamed on the field being edited.
  assert.deepEqual([...statusNameProblems({ in_progress: { en: 'Building' }, qa: { en: 'building' } }, 'qa.en')].map(([field]) => field), ['qa.en'])
  // Same word in the other locale is fine; a renamed status frees its built-in word.
  assert.equal(statusNameProblems({ in_progress: { en: 'Building', de: 'Building' } }).size, 0)
  assert.equal(statusNameProblems({ open: { en: 'Ready' }, qa: { en: 'Open' } }).size, 0)
  assert.equal(statusNameProblems({ qa: { de: 'In Arbeit' } }).get('qa.de')?.code, 'taken')
  // Empty and built-in names are no renames; the rest is trimmed.
  assert.deepEqual(cleanStatusNames({ in_progress: { en: '  Building ', de: '' }, done: { en: '   ' }, blocked: { en: 'Blocked' } }), { in_progress: { en: 'Building' } })
})
