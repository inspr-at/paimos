// SPDX-License-Identifier: AGPL-3.0-only
// AEON-242: palette choice, legacy mapping and measured colour pairs. The vision
// simulations (Machado, Oliveira and Fernandes 2009, full severity) are a check
// on separation, not a promise that a palette works for every person.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { AGENT_PALETTES, normalizeAgentPalette, type AgentPalette } from '../src/lib/agentPalettes.ts'
import { normalizeAgentState } from '../src/lib/agentSignals.ts'

const css = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
function block(source: string, selector: string) {
  const wanted = selector.split(',').map(value => value.trim())
  const rule = [...source.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/([^{}]+)\{([^{}]*)\}/g)]
    .find(match => {
      const selectors = match[1]!.split(',').map(value => value.trim())
      return wanted.every(value => selectors.includes(value))
    })
  assert.ok(rule, `missing ${selector}`)
  const body = rule[2]!
  return Object.fromEntries([...body.matchAll(/--([\w-]+):\s*([^;]+);/g)].map(match => [match[1]!, match[2]!.trim()]))
}
const states = css('../src/styles/agent-states.css'), tokens = css('../src/styles/tokens.css')
const themes = {
  light: { ...block(tokens, ':root, :root[data-theme="light"]'), ...block(states, ':root') },
  dark: { ...block(tokens, ':root, :root[data-theme="light"]'), ...block(tokens, ':root[data-theme="dark"]'), ...block(states, ':root'), ...block(states, ':root[data-theme="dark"]') },
}
type Theme = keyof typeof themes
function resolve(theme: Theme, value: string): string {
  const reference = /^var\(--([\w-]+)\)$/.exec(value)
  return reference ? resolve(theme, themes[theme][reference[1]!]!) : value
}
type Rgb = [number, number, number]
const channel = (value: number) => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
function colour(theme: Theme, value: string, over?: Rgb): Rgb {
  const resolved = resolve(theme, value)
  const hex = /^#([\da-f]{6})$/i.exec(resolved)
  if (hex) return [0, 2, 4].map(i => channel(parseInt(hex[1]!.slice(i, i + 2), 16) / 255)) as Rgb
  const rgba = /^rgba\(([\d.]+),\s*([\d.]+),\s*([\d.]+),\s*([\d.]+)\)$/.exec(resolved)
  assert.ok(rgba && over, `unsupported colour ${resolved}`)
  const alpha = Number(rgba[4])
  return [1, 2, 3].map((i, n) => channel(Number(rgba[i]) / 255) * alpha + over[n]! * (1 - alpha)) as Rgb
}
const luminance = ([r, g, b]: Rgb) => 0.2126 * r + 0.7152 * g + 0.0722 * b
const contrast = (a: Rgb, b: Rgb) => { const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x); return (hi! + 0.05) / (lo! + 0.05) }
function surfaces(theme: Theme): Record<string, Rgb> {
  const raised = colour(theme, themes[theme]['surface-raised']!)
  return { raised, canvas: colour(theme, themes[theme].canvas!), surface: colour(theme, themes[theme].surface!), sunken: colour(theme, themes[theme]['surface-sunken']!, raised) }
}
const simulations: Record<'protan' | 'deutan' | 'tritan', number[][]> = {
  protan: [[0.152286, 1.052583, -0.204868], [0.114503, 0.786281, 0.099216], [-0.003882, -0.048116, 1.051998]],
  deutan: [[0.367322, 0.860646, -0.227968], [0.280085, 0.672501, 0.047413], [-0.011820, 0.042940, 0.968881]],
  tritan: [[1.255528, -0.076749, -0.178779], [-0.078411, 0.930809, 0.147602], [0.004733, 0.691367, 0.303900]],
}
function oklab([r, g, b]: Rgb): Rgb {
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b)
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b)
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b)
  return [0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s, 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s, 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s]
}
const simulate = (value: Rgb, matrix: number[][]) => matrix.map(row => Math.min(1, Math.max(0, row[0]! * value[0] + row[1]! * value[1] + row[2]! * value[2]))) as Rgb
const distance = (a: Rgb, b: Rgb) => { const p = oklab(a), q = oklab(b); return Math.hypot(p[0] - q[0], p[1] - q[1], p[2] - q[2]) * 100 }
const stateColours = ['working', 'waiting', 'throttled', 'problem', 'inactive'] as const

test('five named palettes, with the saved colour-blind choice read as Deutan', () => {
  assert.deepEqual(AGENT_PALETTES.map(option => option.id), ['standard', 'protan', 'deutan', 'tritan', 'monochrome'])
  assert.deepEqual(AGENT_PALETTES.map(option => option.name), ['Standard', 'Protan red–green', 'Deutan red–green', 'Tritan blue–yellow', 'Monochrome'])
  for (const option of AGENT_PALETTES) assert.equal(normalizeAgentPalette(option.id), option.id)
  assert.equal(normalizeAgentPalette('colour-blind'), 'deutan')
  for (const value of [undefined, null, '', 'Colour-blind', 'toString', '__proto__', 'constructor', 7, {}, ['deutan']]) assert.equal(normalizeAgentPalette(value), 'standard')
  assert.equal(normalizeAgentState({ palette: 'colour-blind', inactiveOpacity: 70 }).palette, 'deutan')
  assert.equal(normalizeAgentState({ palette: 'tritan' }).palette, 'tritan')
})

test('every palette colour is defined and readable as text on each surface of both themes', () => {
  for (const theme of ['light', 'dark'] as const) {
    const backgrounds = surfaces(theme)
    for (const palette of AGENT_PALETTES) for (const state of stateColours) {
      const token = themes[theme][`agent-${palette.id}-${state}`]
      assert.ok(token, `${theme} ${palette.id} ${state} is defined`)
      for (const [name, background] of Object.entries(backgrounds)) {
        const ratio = contrast(colour(theme, token), background)
        assert.ok(ratio >= 4.5, `${theme} ${palette.id} ${state} on ${name}: ${ratio.toFixed(2)}:1`)
      }
    }
  }
})

test('the system dark scheme and the explicit dark theme use the same state colours', () => {
  const explicit = block(states, ':root[data-theme="dark"]')
  const media = block(states.slice(states.indexOf('@media (prefers-color-scheme: dark)')), ':root:not([data-theme="light"])')
  assert.deepEqual(media, explicit)
})

test('vision-specific palettes keep every pair of states apart under their simulation', () => {
  const targets: [AgentPalette, keyof typeof simulations][] = [['protan', 'protan'], ['deutan', 'deutan'], ['tritan', 'tritan']]
  for (const theme of ['light', 'dark'] as const) for (const [palette, kind] of targets) {
    const colours = stateColours.map(state => colour(theme, themes[theme][`agent-${palette}-${state}`]!))
    for (let i = 0; i < colours.length; i++) for (let j = i + 1; j < colours.length; j++) {
      const simulated = distance(simulate(colours[i]!, simulations[kind]), simulate(colours[j]!, simulations[kind]))
      assert.ok(simulated >= 9, `${theme} ${palette} ${stateColours[i]}/${stateColours[j]} under ${kind}: ${simulated.toFixed(1)}`)
      assert.ok(distance(colours[i]!, colours[j]!) >= 9, `${theme} ${palette} ${stateColours[i]}/${stateColours[j]} for typical vision`)
    }
  }
})
