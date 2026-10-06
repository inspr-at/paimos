// SPDX-License-Identifier: AGPL-3.0-only
// Viewer palettes for agent states. Colour only supports the state words and
// marks, which every palette keeps; no palette suits every kind of colour vision.
// The engine publishes --agent-<state>; state words use neutral text ink.
export const AGENT_STATES = ['working', 'waiting', 'throttled', 'problem', 'idle'] as const
export type AgentColourState = typeof AGENT_STATES[number]
export type AgentPreset = 'standard' | 'focus' | 'errors' | 'protan' | 'deutan' | 'tritan' | 'monochrome'
export type AgentPalette = AgentPreset | 'custom'
export type StateColours = readonly [string, string, string, string, string]
export interface AgentPaletteOption {
  id: AgentPreset; name: string; description: string
  light: StateColours; dark: StateColours
  distinct: 'all' | 0 | 3 | null; vision?: 'protan' | 'deutan' | 'tritan'
}
// Exact tested values from the attached, approved AEON-717 theme-settings-d7.
const GL = '#7b8487', GD = '#7a8587'
export const AGENT_PALETTES: readonly AgentPaletteOption[] = [
  { id: 'standard', name: 'Standard', description: 'Every state its own colour: green, amber, violet, red and grey.', light: ['#00870e', '#c47a08', '#7039c6', '#b92229', '#6a7378'], dark: ['#00d773', '#ffd787', '#ab71fa', '#f75653', '#a3a3a3'], distinct: 'all' },
  { id: 'focus', name: 'Focus', description: 'Only working agents shine, in blue. Everything else stays calm grey.', light: ['#1d64e0', GL, GL, GL, GL], dark: ['#7cc0ff', GD, GD, GD, GD], distinct: 0 },
  { id: 'errors', name: 'Errors only', description: 'Calm grey, except an agent with a problem, in red.', light: [GL, GL, GL, '#c62828', GL], dark: [GD, GD, GD, '#ff6f61', GD], distinct: 3 },
  { id: 'monochrome', name: 'One colour', description: 'Text colour only; the words and marks show the state.', light: ['#203c3d', '#203c3d', '#203c3d', '#203c3d', '#5f7274'], dark: ['#edf4f0', '#edf4f0', '#edf4f0', '#edf4f0', '#94aead'], distinct: null },
  { id: 'deutan', name: 'Red–green, most common', description: 'For green that looks close to red (deutan).', light: ['#0075cc', '#c18200', '#592d8b', '#b13d09', '#7b8083'], dark: ['#4799ff', '#ffdf9d', '#dab7ff', '#f96c23', '#7c7f80'], distinct: 'all', vision: 'deutan' },
  { id: 'protan', name: 'Red–green, red looks dark', description: 'For red that looks darker or close to green (protan).', light: ['#1272d0', '#b28700', '#4b2f80', '#a04b06', '#6e7679'], dark: ['#6ec1ff', '#f9d150', '#a575f5', '#ce6400', '#8a9398'], distinct: 'all', vision: 'protan' },
  { id: 'tritan', name: 'Blue–yellow', description: 'For blue close to green and yellow close to red (tritan).', light: ['#006e4b', '#e86513', '#75308d', '#ae2d3b', '#858687'], dark: ['#5fe9c1', '#ffa377', '#c373df', '#ea6a75', '#797e81'], distinct: 'all', vision: 'tritan' },
]
// Before AEON-242 there was one red–green palette, saved as "colour-blind".
// Those accounts get Deutan until they choose again; nothing is rewritten on read.
export const LEGACY_AGENT_PALETTES: Readonly<Record<string, AgentPalette>> = { 'colour-blind': 'deutan' }
export function isAgentPalette(value: unknown): value is AgentPalette {
  return value === 'custom' || AGENT_PALETTES.some(option => option.id === value)
}
export function normalizeAgentPalette(value: unknown): AgentPalette {
  if (isAgentPalette(value)) return value
  return typeof value === 'string' && Object.hasOwn(LEGACY_AGENT_PALETTES, value) ? LEGACY_AGENT_PALETTES[value]! : 'standard'
}
