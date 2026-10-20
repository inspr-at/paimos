// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { attachCopy, pairingCopy } from '../src/lib/attachCopy.ts'

test('pairing and a session link stay separate in the app language for English and German profiles', () => {
  const en = attachCopy('My Studio', 'en-GB'), de = attachCopy('My Studio', 'de-AT')
  assert.equal(en.computerPaired, 'Computer paired')
  assert.equal(en.sessionUnlinked, 'This session not yet linked')
  assert.equal(en.sessionLinked, 'This session linked')
  assert.match(en.pairingHelp, /each running session separately/)
  assert.match(en.codeHelp, /After the local check, approval opens in your browser/)
  assert.equal(de.computerPaired, 'Computer paired')
  assert.equal(de.sessionUnlinked, 'This session not yet linked')
  assert.equal(de.sessionLinked, 'This session linked')
  assert.match(de.pairingHelp, /each running session separately/)
  assert.match(de.codeHelp, /After the local check/)
  assert.deepEqual(de, en)
  assert.equal(attachCopy('My Studio', 'fr').computerPaired, en.computerPaired)
  assert.match(en.pairingHelp, /to My Studio/)
  assert.match(de.pairingHelp, /to My Studio/)
})

// Fails on 3f13e3a3, whose where-lines used a text chevron as the Settings breadcrumb.
test('AEON-1502 where-lines name Accounts in Settings without a text chevron', () => {
  const en = pairingCopy('en', 'PAIMOS'), de = pairingCopy('de', 'PAIMOS')
  const lines = [
    en.whereNowhere,
    en.whereSome(['Default', 'Acme client work'], 0, false),
    en.whereSome(['Alpha'], 2, true),
    en.whereSome(['Alpha'], 0, true),
    en.whereRule,
    en.whereFailed,
    de.whereNowhere,
    de.whereSome(['Alpha', 'Beta'], 0, false),
    de.whereSome(['Alpha'], 2, true),
    de.whereRule,
    de.whereFailed,
  ]
  assert.deepEqual(lines, [
    'Nowhere yet · allow on Accounts in Settings before first use',
    'Default and Acme client work · change on Accounts in Settings',
    'Alpha and 2+ more · change on Accounts in Settings',
    'Alpha and more · change on Accounts in Settings',
    'The workspace rule decides · Accounts in Settings',
    'The workspace rule could not be read; it still applies · check Accounts in Settings after connecting',
    'Noch nirgends · vor der ersten Nutzung unter Konten in Einstellungen erlauben',
    'Alpha und Beta · unter Konten in Einstellungen änderbar',
    'Alpha und 2+ weitere · unter Konten in Einstellungen änderbar',
    'Die Regel des Arbeitsbereichs entscheidet · Konten in Einstellungen',
    'Regel nicht lesbar; sie gilt trotzdem · nach dem Verbinden unter Konten in Einstellungen prüfen',
  ])
  assert.equal(lines.some(line => /[‹›]/.test(line)), false)
})
