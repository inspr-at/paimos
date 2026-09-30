// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { validPiModel, piDataNote } from '../src/lib/piModel.ts'
test('OpenRouter accepts slugs, not keys, URLs or commands', () => {
  for (const model of ['stealth/space-bunny-alpha', 'vendor/model:free', 'a/b']) assert.ok(validPiModel('openrouter', model))
  for (const model of ['', 'model', 'https://openrouter.ai/a/b', '../a', 'a/b/c', 'a/b\n', 'a/b:free:high', 'sk-obviously-fake/key']) assert.ok(!validPiModel('openrouter', model))
  assert.ok(validPiModel('anthropic', 'claude-test'))
  assert.ok(piDataNote('stealth/test')); assert.ok(piDataNote('vendor/model:free'))
  assert.ok(!piDataNote('vendor/model'))
})
