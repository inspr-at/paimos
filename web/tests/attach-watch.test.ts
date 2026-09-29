// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { appendWatchText, watchText } from '../src/lib/attachWatch.ts'

test('watch accepts inert text but rejects terminal controls and bidi formatting', () => {
  assert.equal(watchText(JSON.stringify('<script>read me</script>\n\t**text**')), '<script>read me</script>\n\t**text**')
  for (const text of ['\u001b[2J', '\u0000', '\u202eexe.txt', '\u2066hidden']) assert.equal(watchText(JSON.stringify(text)), null)
})
test('watch bounds decoded UTF-8 bytes and rejects non-text envelopes', () => {
  assert.equal(watchText(JSON.stringify('界'.repeat(6000))), null)
  assert.equal(watchText(JSON.stringify('x'.repeat(16_385))), null)
  for (const raw of ['{}', '[]', 'null', '123', '"unterminated', 'x'.repeat(100_001)]) assert.equal(watchText(raw), null)
})
test('watch keeps only the bounded current view', () => {
  const result = appendWatchText('old\n' + 'a'.repeat(65_532), 'new\n')
  assert.equal(result.length, 65_536)
  assert.equal(result.startsWith('old'), false)
  assert.equal(result.endsWith('new\n'), true)
})
