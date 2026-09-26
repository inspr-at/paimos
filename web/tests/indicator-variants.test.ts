// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { indicatorVariants, availableIndicatorVariants, isAgentIndicatorStyle, resolveIndicatorStyle } from '../src/lib/indicatorVariants.ts'

test('nine unique styles in the Settings grid order, with five graded robots', () => {
  assert.deepEqual(indicatorVariants.map(v => v.id), ['pulse', 'robot-1', 'robot-2', 'robot-3', 'robot-4', 'robot-5', 'orbit', 'quill', 'sprite'])
  assert.equal(new Set(indicatorVariants.map(v => v.file)).size, 9)
  assert.deepEqual(indicatorVariants.filter(v => v.family === 'robot').map(v => v.rank), [1, 2, 3, 4, 5])
  assert.deepEqual(indicatorVariants.filter(v => v.family === 'creative').map(v => v.rank), [1, 3, 5])
  for (const variant of indicatorVariants) {
    assert.ok(variant.name && variant.description)
    assert.ok(isAgentIndicatorStyle(variant.id))
  }
  for (const invalid of [null, undefined, '', 'calm', 'playful', 'robot-6', {}, 1]) assert.equal(isAgentIndicatorStyle(invalid), false)
})

test('only existing filenames enable variants, regardless of package landing order', () => {
  const available = availableIndicatorVariants(['../indicators/Robot5.vue', '../indicators/Unknown.vue', '../indicators/Pulse.vue', '../indicators/Robot1.vue'])
  assert.deepEqual(available.map(v => v.id), ['pulse', 'robot-1', 'robot-5'])
  assert.equal(resolveIndicatorStyle('sprite', available), 'robot-1')
  assert.equal(resolveIndicatorStyle('robot-5', available), 'robot-5')
  const merged = availableIndicatorVariants(indicatorVariants.map(v => `../indicators/${v.file}`))
  assert.deepEqual(merged, indicatorVariants)
  assert.equal(resolveIndicatorStyle('sprite', merged), 'sprite')
})
