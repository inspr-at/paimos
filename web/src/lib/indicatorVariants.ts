// SPDX-License-Identifier: AGPL-3.0-only
export type AgentIndicatorStyle = 'pulse' | 'robot-1' | 'robot-2' | 'robot-3' | 'robot-4' | 'robot-5' | 'orbit' | 'quill' | 'sprite'
export interface IndicatorVariant {
  id: AgentIndicatorStyle
  name: string
  description: string
  family: 'indicator' | 'robot' | 'creative'
  /** Calm (0) to playful (5), within each family. */
  rank: number
  file: string
}

// Add the named SFC to components/indicators to enable a tile. LiveBot discovers
// files at build time; parallel packages need no router or builtin wiring.
// Every renderer accepts only state, size (default 26), pulse, seed and lead.
export const indicatorVariants: readonly IndicatorVariant[] = [
  { id: 'pulse', name: 'Pulse', description: 'A precise, quiet sweep', family: 'indicator', rank: 0, file: 'Pulse.vue' },
  { id: 'robot-1', name: 'Robot 1', description: 'Calm and composed', family: 'robot', rank: 1, file: 'Robot1.vue' },
  { id: 'robot-2', name: 'Robot 2', description: 'A little more warmth', family: 'robot', rank: 2, file: 'Robot2.vue' },
  { id: 'robot-3', name: 'Robot 3', description: 'Friendly and unhurried', family: 'robot', rank: 3, file: 'Robot3.vue' },
  { id: 'robot-4', name: 'Robot 4', description: 'Bright and expressive', family: 'robot', rank: 4, file: 'Robot4.vue' },
  { id: 'robot-5', name: 'Robot 5', description: 'A playful companion', family: 'robot', rank: 5, file: 'Robot5.vue' },
  { id: 'orbit', name: 'Orbit', description: 'A calm constellation', family: 'creative', rank: 1, file: 'Orbit.vue' },
  { id: 'quill', name: 'Quill', description: 'A thoughtful flourish', family: 'creative', rank: 3, file: 'Quill.vue' },
  { id: 'sprite', name: 'Sprite', description: 'A spark of mischief', family: 'creative', rank: 5, file: 'Sprite.vue' },
]

export function isAgentIndicatorStyle(value: unknown): value is AgentIndicatorStyle {
  return indicatorVariants.some(variant => variant.id === value)
}

export function availableIndicatorVariants(paths: readonly string[]): readonly IndicatorVariant[] {
  const files = new Set(paths.map(path => path.split('/').at(-1)))
  return indicatorVariants.filter(variant => files.has(variant.file))
}

export function resolveIndicatorStyle(style: AgentIndicatorStyle, available: readonly IndicatorVariant[]): AgentIndicatorStyle {
  // Keep a saved future variant intact; use the shipped Calm artwork meanwhile.
  return available.some(variant => variant.id === style) ? style : 'robot-1'
}
