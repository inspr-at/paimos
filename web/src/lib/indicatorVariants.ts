// SPDX-License-Identifier: AGPL-3.0-only
export type AgentIndicatorStyle = 'pulse' | 'robot-1' | 'robot-2' | 'robot-3' | 'robot-4' | 'robot-5' | 'orbit' | 'quill' | 'sprite'
export type IndicatorRing = 'moving' | 'still' | 'off'
export interface IndicatorVariant {
  id: AgentIndicatorStyle
  name: string
  description: string
  family: 'indicator' | 'robot' | 'creative'
  /** Calm (0) to playful (5), within each family. */
  rank: number
  file: string
  /** The ring as originally drawn; used until a viewer chooses a ring mode. */
  ring: IndicatorRing
  /**
   * Inner-artwork scale at an icon size of 100%: the body then fills the space
   * inside this style's ring (Robot 5: its disk), keeping a hairline gap. Antenna
   * tips and Quill's ink tail may still cross the ring, as they do as drawn.
   */
  fill: number
}

// Add the named SFC to components/indicators to enable a tile. LiveBot discovers
// files at build time; parallel packages need no router or builtin wiring.
// Every renderer accepts state (AgentState: working/waiting/throttled/problem/
// idle/stale/stopped), size (default 26), pulse, seed and lead, plus optional
// ring (moving/still/off, default its own) and artScale (inner artwork only,
// default 1; the disk, ring, state mark and event glint keep their geometry).
// LiveBot supplies --agent-state-color and viewer dimming, plus the accessible
// state word; each renderer supplies AgentStateMark and stops motion outside
// working. Never use a brand accent for a state. SC1 needs no router/module/
// plugin registration.
// fill = the gap-inset radius inside the ring over the body's measured radius
// (outline and half stroke, 32-unit box). tests/indicator-controls.spec.ts
// re-measures both and fails when artwork or rings change.
export const indicatorVariants: readonly IndicatorVariant[] = [
  { id: 'pulse', name: 'Pulse', description: 'A precise, quiet sweep', family: 'indicator', rank: 0, file: 'Pulse.vue', ring: 'moving', fill: 1.9 },
  { id: 'robot-1', name: 'Robot 1', description: 'Calm and composed', family: 'robot', rank: 1, file: 'Robot1.vue', ring: 'moving', fill: 1.21 },
  { id: 'robot-2', name: 'Robot 2', description: 'A little more warmth', family: 'robot', rank: 2, file: 'Robot2.vue', ring: 'still', fill: 1.24 },
  { id: 'robot-3', name: 'Robot 3', description: 'Friendly and unhurried', family: 'robot', rank: 3, file: 'Robot3.vue', ring: 'still', fill: 1.2 },
  { id: 'robot-4', name: 'Robot 4', description: 'Bright and expressive', family: 'robot', rank: 4, file: 'Robot4.vue', ring: 'still', fill: 1.07 },
  { id: 'robot-5', name: 'Robot 5', description: 'A playful companion', family: 'robot', rank: 5, file: 'Robot5.vue', ring: 'still', fill: 1.04 },
  { id: 'orbit', name: 'Orbit', description: 'A calm constellation', family: 'creative', rank: 1, file: 'Orbit.vue', ring: 'moving', fill: 1.06 },
  { id: 'quill', name: 'Quill', description: 'A thoughtful flourish', family: 'creative', rank: 3, file: 'Quill.vue', ring: 'off', fill: 1.21 },
  { id: 'sprite', name: 'Sprite', description: 'A spark of mischief', family: 'creative', rank: 5, file: 'Sprite.vue', ring: 'off', fill: 1.04 },
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

export const INDICATOR_RINGS: readonly IndicatorRing[] = ['moving', 'still', 'off']
export function isIndicatorRing(value: unknown): value is IndicatorRing {
  return INDICATOR_RINGS.includes(value as IndicatorRing)
}
export function indicatorRing(style: AgentIndicatorStyle, chosen?: IndicatorRing): IndicatorRing {
  return chosen ?? indicatorVariants.find(variant => variant.id === style)?.ring ?? 'still'
}

// Icon size is how much of the space inside the ring the drawing fills: 100%
// reaches the ring, 30% is smaller than Pulse's dot. The same percentage means
// the same fullness in every style. Unset keeps each style as drawn.
export const ICON_SIZE = { min: 30, max: 100, step: 1 } as const
export function clampIconSize(value: unknown): number | undefined {
  if (typeof value !== 'number' || !Number.isFinite(value)) return undefined
  return Math.min(ICON_SIZE.max, Math.max(ICON_SIZE.min, Math.round(value)))
}
const fillOf = (style: AgentIndicatorStyle) => indicatorVariants.find(variant => variant.id === style)?.fill ?? 1
/** The renderer scale for a saved size; 1 while none is saved. */
export function indicatorArtScale(style: AgentIndicatorStyle, size?: number): number {
  const percent = clampIconSize(size)
  return percent === undefined ? 1 : Math.round(percent / 100 * fillOf(style) * 1000) / 1000
}
/** The icon size a style is drawn at, for showing an unset preference. */
export function nativeIconSize(style: AgentIndicatorStyle): number {
  return Math.round(100 / fillOf(style))
}
const fills = indicatorVariants.map(variant => variant.fill)
const SCALE_LIMITS = [Math.round(ICON_SIZE.min / 100 * Math.min(...fills) * 1000) / 1000, Math.max(...fills)] as const
/** Guards a renderer against direct callers passing NaN, zero or extreme values. */
export function safeArtScale(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return 1
  return Math.min(SCALE_LIMITS[1], Math.max(SCALE_LIMITS[0], value))
}
/**
 * Inline style for a renderer's inner-artwork group, scaled about the 32-unit
 * centre. Strokes follow the square root of the scale, so small icons keep
 * legible lines and large ones do not turn heavy.
 */
export function artStyle(scale: unknown): Record<string, string> | undefined {
  const factor = safeArtScale(scale)
  return factor === 1 ? undefined : { transformOrigin: '16px 16px', scale: String(factor), '--art-stroke': artStroke(factor) }
}
export function artStroke(scale: number): string {
  return (1 / Math.sqrt(scale)).toFixed(3)
}
