// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ICON_SIZE, artStyle, clampIconSize, indicatorArtScale, indicatorRing, indicatorVariants, availableIndicatorVariants, isAgentIndicatorStyle, isIndicatorRing, nativeIconSize, resolveIndicatorStyle, safeArtScale } from '../src/lib/indicatorVariants.ts'

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

test('each style keeps its drawn ring until the viewer chooses a ring mode', () => {
  assert.deepEqual(Object.fromEntries(indicatorVariants.map(v => [v.id, indicatorRing(v.id)])), {
    pulse: 'moving', 'robot-1': 'moving', 'robot-2': 'still', 'robot-3': 'still', 'robot-4': 'still', 'robot-5': 'still', orbit: 'moving', quill: 'off', sprite: 'off',
  })
  for (const variant of indicatorVariants) for (const ring of ['moving', 'still', 'off'] as const) assert.equal(indicatorRing(variant.id, ring), ring)
  for (const invalid of [undefined, null, '', 'Moving', 'spin', 0, {}]) assert.equal(isIndicatorRing(invalid), false)
})

test('icon size is how full the space inside the ring is; unset and malformed values keep each drawing', () => {
  assert.deepEqual([ICON_SIZE.min, ICON_SIZE.max, ICON_SIZE.step], [30, 100, 1])
  assert.deepEqual([29, 30, 64.6, 100, 101, -5, 1e9].map(clampIconSize), [30, 30, 65, 100, 100, 30, 100])
  for (const invalid of [undefined, null, NaN, Infinity, -Infinity, '60', {}, [60]]) assert.equal(clampIconSize(invalid), undefined)
  for (const variant of indicatorVariants) {
    assert.equal(indicatorArtScale(variant.id), 1)
    assert.equal(indicatorArtScale(variant.id, NaN), 1)
    assert.equal(indicatorArtScale(variant.id, ICON_SIZE.max), variant.fill)
    assert.equal(indicatorArtScale(variant.id, 1e9), variant.fill)
    // Every step changes the drawing: no plateau anywhere in the range.
    const steps = Array.from({ length: (ICON_SIZE.max - ICON_SIZE.min) / ICON_SIZE.step + 1 }, (_, i) => indicatorArtScale(variant.id, ICON_SIZE.min + i * ICON_SIZE.step))
    assert.ok(steps.every((scale, i) => i === 0 || scale > steps[i - 1]!), variant.id)
    assert.ok(Math.abs(steps[0]! / steps.at(-1)! - .3) < .002, variant.id)
  }
  // As drawn, Pulse is spare and Robot 5 almost fills its disk.
  assert.deepEqual(Object.fromEntries(indicatorVariants.map(v => [v.id, nativeIconSize(v.id)])), {
    pulse: 53, 'robot-1': 83, 'robot-2': 81, 'robot-3': 83, 'robot-4': 93, 'robot-5': 96, orbit: 94, quill: 83, sprite: 96,
  })
  assert.deepEqual([NaN, Infinity, 0, -1, 0.5, 1, 1.1, 3, '1.2', undefined].map(safeArtScale), [1, 1, 0.312, 0.312, 0.5, 1, 1.1, 1.9, 1, 1])
  assert.equal(artStyle(1), undefined)
  assert.equal(artStyle(NaN), undefined)
  assert.deepEqual(artStyle(1.15), { transformOrigin: '16px 16px', scale: '1.15', '--art-stroke': '0.933' })
  assert.deepEqual(artStyle(0.36), { transformOrigin: '16px 16px', scale: '0.36', '--art-stroke': '1.667' })
})
