// SPDX-License-Identifier: AGPL-3.0-only
// AEON-644 regressions use main's newer approved engine, avoiding a second engine.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { AGENT_PALETTES, AGENT_STATES } from '../src/lib/agentPalettes.ts'
import { agentPreviewTokens, CARD_COLOURS, colourContrast, deriveDark, inkOn, markerValue, PORCELAIN, roles, themeTokens } from '../src/lib/themeEngine.ts'

for (const mode of ['light', 'dark'] as const) {
  test(`extreme ${mode} accents produce readable text without changing their chosen value`, () => {
    for (const accent of mode === 'light' ? ['#ffffff', '#fefefe', '#fffafa', '#ffffe0'] : ['#000000', '#010101', '#010005', '#050000']) {
      const values = structuredClone(PORCELAIN)
      values.primary = { light: accent, dark: accent }; values.secondary = { light: accent, dark: accent }
      const tokens = themeTokens(values, mode)
      for (const token of ['--primary-ink', '--teal-ink', '--secondary-ink']) assert.ok(colourContrast(tokens[token]!, CARD_COLOURS[mode]) >= 4.5, `${accent}: ${token}`)
      assert.equal(roles(accent, mode).chosen, accent)
      assert.equal(values.primary[mode], accent)
    }
  })
}
test('filled ink chooses higher contrast consistently for actual accent fills', () => {
  for (const fill of ['#ffffff', '#000000', '#a4e5df', '#d69b31', '#8547b0', '#ff0000', '#687078']) {
    const expected = colourContrast('#ffffff', fill) >= colourContrast('#102327', fill) ? '#ffffff' : '#102327'
    assert.equal(inkOn(fill), expected); assert.equal(inkOn(fill.toUpperCase()), expected)
    for (const mode of ['light', 'dark'] as const) {
      const values = structuredClone(PORCELAIN)
      values.primary = { light: fill, dark: fill }; values.secondary = { light: fill, dark: fill }
      const tokens = themeTokens(values, mode)
      assert.equal(tokens['--button-ink'], inkOn(tokens['--primary']!))
      assert.equal(tokens['--secondary-on'], inkOn(tokens['--secondary']!))
      assert.equal(tokens['--marker-on'], inkOn(tokens['--marker']!))
    }
  }
})
test('main approved dark derivation and explicit dark choices remain authoritative', () => {
  assert.equal(deriveDark('#3fd997'), '#6acf9d')
  for (const fill of ['#000000', '#0e6f6c', '#8547b0', '#b5642a', '#ff0000', '#0000ff']) assert.ok(colourContrast(deriveDark(fill), CARD_COLOURS.dark) >= 4.5, fill)
  const values = structuredClone(PORCELAIN); values.primary.dark = '#123456'
  assert.equal(markerValue({ ...values, recurring_marker: { source: 'primary', custom: null } }, 'dark'), '#123456')
  values.primary.dark = null
  assert.equal(markerValue({ ...values, recurring_marker: { source: 'primary', custom: null } }, 'dark'), deriveDark(values.primary.light))
})
test('every recurring source resolves per mode with ink for its actual fill', () => {
  for (const source of ['primary', 'secondary', 'neutral', 'custom'] as const) for (const mode of ['light', 'dark'] as const) {
    const values = structuredClone(PORCELAIN); values.recurring_marker = { source, custom: '#8547b0' }
    const tokens = themeTokens(values, mode), expected = markerValue(values, mode)
    assert.equal(tokens['--marker'], expected); assert.equal(tokens['--marker-on'], inkOn(expected))
  }
})
test('saved presets and Custom previews each choose ink within their own mode', () => {
  for (const preset of AGENT_PALETTES) for (const mode of ['light', 'dark'] as const) {
    const agents = { ...PORCELAIN.agents, palette: preset.id }, tokens = themeTokens({ ...PORCELAIN, agents }, mode)
    for (const [i, state] of AGENT_STATES.entries()) {
      assert.equal(tokens[`--agent-${state}`], preset[mode][i])
      assert.equal(tokens[`--agent-${state}-ink`], inkOn(preset[mode][i]!))
      assert.equal(agentPreviewTokens(agents)[`--agent-${state}-ink`], `light-dark(${inkOn(preset.light[i]!)}, ${inkOn(preset.dark[i]!)})`)
    }
  }
  const agents = { ...PORCELAIN.agents, palette: 'custom' as const, custom_states: Object.fromEntries(AGENT_STATES.map(state => [state, { light: '#000000', dark: '#ffffff' }])) as NonNullable<typeof PORCELAIN.agents.custom_states> }
  for (const state of AGENT_STATES) assert.equal(agentPreviewTokens(agents)[`--agent-${state}-ink`], 'light-dark(#ffffff, #102327)')
})
