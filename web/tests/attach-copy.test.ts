// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { attachCopy } from '../src/lib/attachCopy.ts'

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
