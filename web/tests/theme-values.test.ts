// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { contrastRatio, DARK_CARD, DARK_INK, deriveDark, inkOn, PORCELAIN, themeTokens, WHITE_INK } from '../src/lib/themeValues.ts'

test('filled ink is the higher contrast choice, independent of mode and hex casing', () => {
  for (const fill of ['#ffffff', '#000000', '#a4e5df', '#d69b31', '#8547b0', '#ff0000', '#687078']) {
    const expected = contrastRatio(WHITE_INK, fill) >= contrastRatio(DARK_INK, fill) ? WHITE_INK : DARK_INK
    assert.equal(inkOn(fill), expected)
    assert.equal(inkOn(fill.toUpperCase()), expected)
    for (const dark of [true, false]) {
      const values = structuredClone(PORCELAIN)
      values.primary = { light: fill, dark: fill }
      values.secondary = { light: fill, dark: fill }
      const tokens = themeTokens(values, dark)
      assert.equal(tokens['--button-ink'], expected)
      assert.equal(tokens['--gold-on'], expected)
      assert.equal(tokens['--recurring-marker-ink'], expected)
    }
  }
})
test('dark values use draft 4 HSL lightness steps until 6:1 on dark cards', () => {
  // Golden values independently retained from the accepted fragment algorithm.
  assert.equal(deriveDark('#0e6f6c'), '#24cac4')
  for (const fill of ['#000000', '#0e6f6c', '#8547b0', '#b5642a', '#ff0000', '#0000ff']) assert.ok(contrastRatio(deriveDark(fill), DARK_CARD) >= 6, fill)
  const values = structuredClone(PORCELAIN)
  values.primary.dark = '#123456'
  assert.equal(themeTokens(values, true)['--teal'], '#123456', 'hand-set dark is preserved even at low contrast')
  values.primary.dark = null
  assert.equal(themeTokens(values, true)['--teal'], deriveDark(values.primary.light))
})
test('every recurring source resolves per mode, with the same ink as its fill', () => {
  for (const source of ['primary', 'secondary', 'neutral', 'custom'] as const) for (const dark of [false, true]) {
    const values = structuredClone(PORCELAIN)
    values.recurring_marker = { source, custom: '#8547b0' }
    const tokens = themeTokens(values, dark)
    const expected = source === 'primary' ? tokens['--teal'] : source === 'secondary' ? tokens['--gold'] : source === 'neutral' ? (dark ? '#8aa3a2' : '#7c8c8d') : dark ? deriveDark('#8547b0') : '#8547b0'
    assert.equal(tokens['--recurring-marker'], expected)
    assert.equal(tokens['--recurring-marker-ink'], inkOn(expected!))
  }
})
test('brand consumers and semantic tints follow both saved accents', () => {
  const values = structuredClone(PORCELAIN)
  values.primary = { light: '#8547b0', dark: '#c8a0ee' }
  values.secondary = { light: '#bf3d6d', dark: '#f08db1' }
  const tokens = themeTokens(values, false)
  assert.equal(tokens['--brand-teal'], '#8547b0')
  assert.equal(tokens['--brand-gold'], '#bf3d6d')
  for (const name of ['--aqua', '--aqua-2', '--aqua-3', '--row-selected', '--chip-teal-line', '--focus-ring', '--wash-1']) assert.ok(tokens[name]!.includes('#8547b0'), name)
  for (const name of ['--gold-wash', '--mark-hl', '--wash-3']) assert.ok(tokens[name]!.includes('#bf3d6d'), name)
})
