// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { CARD_COLOURS, colourContrast, colourFromHsl, colourHsl, derivedDark, suggestColour, validColour } from '../src/lib/themeColours.ts'

test('contrast uses WCAG luminance and both actual preview card surfaces', () => {
  assert.equal(colourContrast('#000000', '#ffffff'), 21)
  assert.equal(colourContrast('#123456', '#123456'), 1)
  assert.ok(colourContrast('#0e6f6c', CARD_COLOURS.light) >= 4.5)
  assert.ok(colourContrast('#a4e5df', CARD_COLOURS.dark) >= 4.5)
  assert.ok(colourContrast('#d69b31', CARD_COLOURS.light) < 4.5)
})
test('derived dark retains the hue and produces a legible shade', () => {
  for (const hex of ['#0e6f6c', '#b5642a', '#333333', '#000000', '#ffffff']) {
    const dark = derivedDark(hex)
    assert.ok(validColour(dark)); assert.ok(colourContrast(dark, CARD_COLOURS.dark) >= 6)
    if (colourHsl(hex)[1] > 0) assert.ok(Math.abs(colourHsl(hex)[0] - colourHsl(dark)[0]) < 0.01)
  }
})
test('Suggest changes failing shades, keeps readable shades, hue and saturation', () => {
  for (const [hex, mode] of [['#d69b31', 'light'], ['#183034', 'dark'], ['#ffffff', 'light'], ['#000000', 'dark']] as const) {
    const suggested = suggestColour(hex, mode)
    assert.notEqual(suggested, hex); assert.ok(colourContrast(suggested, CARD_COLOURS[mode]) >= 4.5)
    if (colourHsl(hex)[1] > 0) assert.ok(Math.abs(colourHsl(suggested)[0] - colourHsl(hex)[0]) < 0.01)
    // A small move back towards the original (beyond hex rounding) fails AA;
    // this rejects an unnecessarily distant black/white replacement.
    const [h, s, original] = colourHsl(hex), adjusted = colourHsl(suggested)[2]
    const closer = colourFromHsl(h, s, adjusted + Math.sign(original - adjusted) * 0.005)
    assert.ok(colourContrast(closer, CARD_COLOURS[mode]) < 4.5)
  }
  assert.equal(suggestColour('#0e6f6c', 'light'), '#0e6f6c')
  assert.equal(suggestColour('#a4e5df', 'dark'), '#a4e5df')
  assert.equal(validColour('#abc'), false); assert.equal(validColour('#ABCDEF'), true)
})
