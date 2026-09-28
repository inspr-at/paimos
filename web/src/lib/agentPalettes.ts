// SPDX-License-Identifier: AGPL-3.0-only
// Viewer palettes for agent states. Colour only supports the state words and
// marks, which every palette keeps; no palette suits every kind of colour vision.
// Tokens: --agent-<palette>-<working|waiting|throttled|problem|inactive> in agent-states.css.
export type AgentPalette = 'standard' | 'protan' | 'deutan' | 'tritan' | 'monochrome'
export interface AgentPaletteOption { id: AgentPalette; name: string; description: string }
export const AGENT_PALETTES: readonly AgentPaletteOption[] = [
  { id: 'standard', name: 'Standard', description: 'Green, amber, violet and red.' },
  { id: 'protan', name: 'Protan red–green', description: 'For red that looks darker or close to green.' },
  { id: 'deutan', name: 'Deutan red–green', description: 'For green that looks closer to red. The most common kind.' },
  { id: 'tritan', name: 'Tritan blue–yellow', description: 'For blue close to green and yellow close to red.' },
  { id: 'monochrome', name: 'Monochrome', description: 'One ink colour; words and marks show the state.' },
]
// Before AEON-242 there was one red–green palette, saved as "colour-blind".
// Those accounts get Deutan until they choose again; nothing is rewritten on read.
export const LEGACY_AGENT_PALETTES: Readonly<Record<string, AgentPalette>> = { 'colour-blind': 'deutan' }
export function isAgentPalette(value: unknown): value is AgentPalette {
  return AGENT_PALETTES.some(option => option.id === value)
}
export function normalizeAgentPalette(value: unknown): AgentPalette {
  if (isAgentPalette(value)) return value
  return typeof value === 'string' && Object.hasOwn(LEGACY_AGENT_PALETTES, value) ? LEGACY_AGENT_PALETTES[value]! : 'standard'
}
