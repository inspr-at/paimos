// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { chooseHeader, DEFAULT_HEADER, headerShortcut, headerStorageKey, readHeaderPreference, toggleHeader } from '../src/lib/projectHeader.ts'

test('new and invalid device choices use Compact without importing row height', () => {
  for (const raw of [null, '', 'broken', 'null', '3', '{}', ' '.repeat(513), '{"density":"wide"}', '{"density":"compact","roomy":"wide"}']) {
    assert.deepEqual(readHeaderPreference(raw), DEFAULT_HEADER)
  }
  assert.deepEqual(readHeaderPreference('{"density":"comfortable","roomy":"compact"}'), { density: 'comfortable', roomy: 'comfortable' })
})
test('folding restores each last roomy mode across reloads', () => {
  for (const density of ['comfortable', 'compact'] as const) {
    const open = chooseHeader(DEFAULT_HEADER, density)
    const folded = toggleHeader(open)
    assert.equal(folded.density, 'collapsed')
    assert.deepEqual(toggleHeader(readHeaderPreference(JSON.stringify(folded))), open)
    assert.equal(chooseHeader(folded, 'compact').roomy, 'compact')
  }
})
test('device keys separate workspaces and people without delimiter collisions', () => {
  assert.notEqual(headerStorageKey('a', 'b:c'), headerStorageKey('a:b', 'c'))
  assert.notEqual(headerStorageKey('a', 'b'), headerStorageKey('a', 'c'))
  assert.notEqual(headerStorageKey('a', 'b'), headerStorageKey('c', 'b'))
})
const key = { code: 'Period', metaKey: false, ctrlKey: true, shiftKey: true, altKey: false, repeat: false, isComposing: false, defaultPrevented: false }
test('physical Period works while Shift changes its character on English or German layouts', () => {
  assert.equal(headerShortcut(key, false), true)
  assert.equal(headerShortcut({ ...key, metaKey: true, ctrlKey: false }, true), true)
  assert.equal(headerShortcut(key, true), false)
  assert.equal(headerShortcut({ ...key, metaKey: true, ctrlKey: false }, false), false)
})
test('browser shortcuts, repeats, composition and consumed keys stay native', () => {
  for (const patch of [{ shiftKey: false }, { altKey: true }, { metaKey: true }, { repeat: true }, { isComposing: true }, { defaultPrevented: true }, ...['KeyS', 'KeyR', 'KeyD', 'KeyP', 'KeyA', 'Slash'].map(code => ({ code }))]) {
    assert.equal(headerShortcut({ ...key, ...patch }, false), false)
  }
})
