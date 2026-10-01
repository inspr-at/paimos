// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { attachCopy } from '../src/lib/attachCopy.ts'

test('pairing and a session link are separate in English and German', () => {
  const en = attachCopy('My Studio', 'en-GB'), de = attachCopy('My Studio', 'de-AT')
  assert.equal(en.computerPaired, 'Computer paired')
  assert.equal(en.sessionUnlinked, 'This session not yet linked')
  assert.equal(en.sessionLinked, 'This session linked')
  assert.match(en.pairingHelp, /each running session separately/)
  assert.match(en.codeHelp, /After the local check, approval opens in your browser/)
  assert.equal(de.computerPaired, 'Computer gekoppelt')
  assert.equal(de.sessionUnlinked, 'Diese Sitzung ist noch nicht verknüpft')
  assert.equal(de.sessionLinked, 'Diese Sitzung ist verknüpft')
  assert.match(de.pairingHelp, /jede laufende Sitzung separat/)
  assert.match(de.codeHelp, /Nach der lokalen Prüfung/)
  assert.equal(attachCopy('My Studio', 'fr').computerPaired, en.computerPaired)
  assert.match(en.pairingHelp, /to My Studio/)
  assert.match(de.pairingHelp, /mit My Studio/)
})
