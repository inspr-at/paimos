// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import { generatedBlock } from '../../scripts/theme-tokens.mjs'
import { CARD_COLOURS, colourContrast, deriveDark, fromOklch, markerValue, modeValue, oklch, PORCELAIN, roles, themeCss, themeTokens, validColour } from '../src/lib/themeEngine.ts'

const source = readFileSync(new URL('../index.html', import.meta.url), 'utf8')
const boot = source.match(/<script id="aeon-theme-boot"[^>]*>([\s\S]*?)<\/script>/)![1]!
function bootstrap(raw: string | null, unavailable = false) {
  const styles: { id: string; nonce: string; textContent: string }[] = []
  let removed = false
  runInNewContext(boot, {
    localStorage: { getItem: () => { if (unavailable) throw Error('disabled'); return raw }, removeItem: () => { removed = true } },
    document: { currentScript: { nonce: 'test-nonce' }, createElement: () => ({}), head: { appendChild: (style: typeof styles[number]) => styles.push(style) } },
  })
  return { styles, removed }
}
test('WCAG contrast measures the rounded hex and actual card surfaces', () => {
  assert.equal(colourContrast('#000000', '#ffffff'), 21)
  assert.equal(colourContrast('#123456', '#123456'), 1)
  assert.ok(colourContrast('#0e6f6c', CARD_COLOURS.light) >= 4.5)
  assert.ok(colourContrast('#a4e5df', CARD_COLOURS.dark) >= 4.5)
  assert.equal(validColour('#abc'), false); assert.equal(validColour('#ABCDEF'), true)
  assert.throws(() => roles('red; color:red', 'light'), TypeError)
  assert.throws(() => fromOklch(NaN, .1, 10), TypeError)
})
test('OKLCH round trips sRGB and maps excessive chroma without changing hue', () => {
  for (const hex of ['#000000', '#ffffff', '#ff0000', '#00ff00', '#0000ff', '#0e6f6c', '#3fd997']) {
    assert.equal(fromOklch(...oklch(hex)), hex)
  }
  const mapped = oklch(fromOklch(.6, .5, 30))
  assert.ok(Math.abs(mapped[0] - .6) < .003)
  assert.ok(Math.abs(mapped[2] - 30) < 1)
  assert.ok(mapped[1] < .5)
})
test('automatic dark mirrors lightness and fixes the unchanged green regression', () => {
  assert.equal(deriveDark('#3fd997'), '#6acf9d')
  for (const hex of ['#0e6f6c', '#b5642a', '#333333', '#000000', '#ffffff', '#3fd997']) {
    const dark = deriveDark(hex), [L, C] = oklch(dark)
    assert.ok(validColour(dark)); assert.ok(colourContrast(dark, CARD_COLOURS.dark) >= 4.5)
    assert.ok(L >= .775 && L <= .905); assert.ok(C <= .123)
    if (colourContrast(hex, CARD_COLOURS.dark) < 4.5) assert.notEqual(dark, hex)
  }
})
test('5000 deterministic colours satisfy ink, line and fill-label contrast in both modes', () => {
  let seed = 749
  for (let i = 0; i < 5000; i++) {
    seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0
    const hex = '#' + (seed & 0xffffff).toString(16).padStart(6, '0')
    for (const mode of ['light', 'dark'] as const) {
      const r = roles(hex, mode)
      assert.ok(colourContrast(r.ink, CARD_COLOURS[mode]) >= 4.5, `${hex} ${mode} card ink`)
      assert.ok(colourContrast(r.ink, r.t2) >= 4.5, `${hex} ${mode} tint ink`)
      assert.ok(colourContrast(r.line, CARD_COLOURS[mode]) >= 3, `${hex} ${mode} line`)
      assert.ok(colourContrast(r.on, r.fill) >= 4.5, `${hex} ${mode} fill label`)
    }
  }
})
test('token aliases follow both accents and every marker source without theming semantic colours', () => {
  for (const mode of ['light', 'dark'] as const) {
    const tokens = themeTokens(PORCELAIN, mode)
    assert.equal(tokens['--teal'], tokens['--primary-line'])
    assert.equal(tokens['--teal-ink'], tokens['--primary-ink'])
    assert.equal(tokens['--aqua-2'], tokens['--primary-tint-2'])
    assert.equal(tokens['--button-ink'], tokens['--primary-on'])
    for (const token of ['--st-progress', '--danger', '--gold', '--ok', '--canvas', '--ink', '--line']) assert.equal(tokens[token], undefined)
    for (const source of ['primary', 'secondary', 'neutral', 'custom'] as const) {
      const values = { ...PORCELAIN, recurring_marker: { source, custom: '#bf3d6d' } }
      assert.equal(themeTokens(values, mode)['--marker'], markerValue(values, mode))
      if (source === 'primary' || source === 'secondary') assert.equal(markerValue(values, mode), modeValue(values[source], mode))
      if (source === 'custom') assert.equal(markerValue(values, mode), mode === 'light' ? '#bf3d6d' : deriveDark('#bf3d6d'))
    }
    const edited = { ...PORCELAIN, primary: { light: '#3a5fc4', dark: null } }
    assert.notEqual(themeTokens(edited, mode)['--primary'], tokens['--primary'])
    assert.equal(themeTokens(edited, mode)['--secondary'], tokens['--secondary'])
  }
})
test('Porcelain generated accent blocks cannot drift from the engine in CI', () => {
  const css = readFileSync(new URL('../src/styles/tokens.css', import.meta.url), 'utf8')
  const blocks = [...css.matchAll(/^[ \t]*\/\* BEGIN GENERATED THEME (light|dark)[^\n]*\*\/[\s\S]*?^[ \t]*\/\* END GENERATED THEME \1 \*\//gm)]
  assert.equal(blocks.length, 3)
  for (const block of blocks) assert.equal(block[0], generatedBlock(block[1], block[0].match(/^[ \t]*/)![0]))
})
test('first-paint bootstrap restores derived CSS before app startup with its CSP nonce', () => {
  const css = themeCss(PORCELAIN)
  const result = bootstrap(JSON.stringify({ principal: 'tenant/person', css }))
  assert.deepEqual(result.styles, [{ id: 'aeon-theme', nonce: 'test-nonce', textContent: css }])
  assert.ok(source.indexOf('aeon-theme-boot') < source.indexOf('<div id="app">'))
})
test('first-paint bootstrap tolerates unavailable storage and rejects corrupt, oversized or unsafe CSS', () => {
  for (const raw of [null, '{', 'null', '0', '{}', JSON.stringify({ principal: '', css: themeCss(PORCELAIN) }), 'x'.repeat(32769),
    ...['body { display: none; }', '@import "https://example.invalid";', themeCss(PORCELAIN).replace('#0e6f6c', 'url(https://example.invalid)'), themeCss(PORCELAIN) + '\nbody {}'].map(css => JSON.stringify({ principal: 'tenant/person', css }))]) {
    assert.equal(bootstrap(raw).styles.length, 0)
  }
  assert.equal(bootstrap(null, true).styles.length, 0)
})
