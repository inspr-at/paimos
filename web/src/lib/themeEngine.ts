// SPDX-License-Identifier: AGPL-3.0-only
// Port of the approved AEON-717 theme-settings-d7 colour engine. No DOM/Vue.
import type { ThemeAccent, ThemeValues } from './themes.ts'
import type { AgentThemeAppearance } from './agentTheme.ts'
import { AGENT_PALETTES, AGENT_STATES, normalizeAgentPalette } from './agentPalettes.ts'
import { nativeIconSize } from './indicatorVariants.ts'

export type ColourMode = 'light' | 'dark'
export type Triple = [number, number, number]
export const CARD_COLOURS = { light: '#fffefa', dark: '#1c393d' } as const
export const validColour = (value: string) => /^#[0-9a-f]{6}$/i.test(value)
const clamp = (v: number, a: number, b: number) => Math.min(b, Math.max(a, v))
const lin = (v: number) => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
const gam = (v: number) => v <= 0.0031308 ? 12.92 * v : 1.055 * v ** (1 / 2.4) - 0.055
function rgb(hex: string): Triple {
  if (!validColour(hex)) throw new TypeError('Expected a six-digit hex colour')
  return [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255) as Triple
}
const toHex = (c: number[]) => '#' + c.map(v => Math.round(clamp(v, 0, 1) * 255).toString(16).padStart(2, '0')).join('')
export function oklab(hex: string): Triple {
  const [r, g, b] = rgb(hex).map(lin) as Triple
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b)
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b)
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b)
  return [0.2104542553 * l + 0.7936177850 * m - 0.0040720468 * s, 1.9779984951 * l - 2.4285922050 * m + 0.4505937099 * s, 0.0259040371 * l + 0.7827717662 * m - 0.8086757660 * s]
}
export function oklch(hex: string): Triple {
  const [L, A, B] = oklab(hex)
  return [L, Math.hypot(A, B), (Math.atan2(B, A) * 180 / Math.PI + 360) % 360]
}
function linRgb(L: number, C: number, H: number): Triple {
  const a = C * Math.cos(H * Math.PI / 180), b = C * Math.sin(H * Math.PI / 180)
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3
  const s = (L - 0.0894841775 * a - 1.2914855480 * b) ** 3
  return [4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s, -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s, -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s]
}
const inGamut = (c: Triple) => c.every(v => v >= -1e-4 && v <= 1 + 1e-4)
/** Keep lightness/hue and reduce chroma to fit sRGB; return rounded hex. */
export function fromOklch(L: number, C: number, H: number): string {
  if (![L, C, H].every(Number.isFinite) || C < 0) throw new TypeError('Invalid OKLCH colour')
  L = clamp(L, 0, 1)
  let c = linRgb(L, C, H)
  if (!inGamut(c)) {
    let lo = 0, hi = C
    for (let i = 0; i < 22; i++) {
      const mid = (lo + hi) / 2
      if (inGamut(linRgb(L, mid, H))) lo = mid
      else hi = mid
    }
    c = linRgb(L, lo, H)
  }
  return toHex(c.map(gam))
}
function luminance(hex: string) {
  const [r, g, b] = rgb(hex).map(lin) as Triple
  return r * 0.2126 + g * 0.7152 + b * 0.0722
}
export function colourContrast(a: string, b: string) {
  const x = luminance(a), y = luminance(b)
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)
}
const ONS = ['#ffffff', '#102327'] as const
export const inkOn = (fill: string) => colourContrast(ONS[0], fill) >= colourContrast(ONS[1], fill) ? ONS[0] : ONS[1]
const TINT: Record<ColourMode, [number, number][]> = {
  light: [[.875, .07], [.923, .045], [.963, .02]],
  dark: [[.44, .055], [.38, .048], [.325, .04]],
}
export function deriveDark(hex: string) {
  const [L, C, H] = oklch(hex), c = Math.min(C, .12)
  let l = clamp(1.36 - L, .78, .9), out = fromOklch(l, c, H)
  for (let i = 0; i < 22 && colourContrast(out, CARD_COLOURS.dark) < 4.5; i++) {
    l += .01; out = fromOklch(l, c, H)
  }
  return out
}
export interface AccentRoles {
  chosen: string; fill: string; hi: string; lo: string; on: string; line: string; ink: string
  t1: string; t2: string; t3: string; nudged: boolean
}
export function roles(hex: string, mode: ColourMode): AccentRoles {
  const light = mode === 'light', card = CARD_COLOURS[mode], [L, C, H] = oklch(hex)
  const [t1, t2, t3] = TINT[mode].map(([l, c]) => fromOklch(l, Math.min(C, c), H)) as [string, string, string]
  let li = light ? Math.min(L - .06, .62) : Math.max(L + .04, .8)
  const ci = Math.min(C, light ? .14 : .11)
  let ink = fromOklch(li, ci, H)
  for (let i = 0; i < 240 && (colourContrast(ink, card) < 4.5 || colourContrast(ink, t2) < 4.5); i++) {
    li += light ? -.005 : .005; ink = fromOklch(li, ci, H)
  }
  let ll = L, line = hex
  for (let i = 0; i < 120 && colourContrast(line, card) < 3; i++) {
    ll += light ? -.01 : .01; line = fromOklch(ll, C, H)
  }
  let fl = L, fill = hex, on = inkOn(fill), nudged = false
  for (let i = 0; i < 120 && colourContrast(on, fill) < 4.5; i++) {
    fl += on === ONS[0] ? -.01 : .01; fill = fromOklch(fl, C, H); on = inkOn(fill); nudged = true
  }
  return { chosen: hex, fill, hi: fromOklch(fl + .06, C, H), lo: fromOklch(fl - .05, C, H), on, line, ink, t1, t2, t3, nudged }
}
export const modeValue = (accent: ThemeAccent, mode: ColourMode) => mode === 'light' ? accent.light : (accent.dark ?? deriveDark(accent.light))
export function markerValue(values: ThemeValues, mode: ColourMode) {
  const marker = values.recurring_marker
  if (marker.source === 'neutral') return mode === 'light' ? '#7c8c8d' : '#8aa3a2'
  if (marker.source === 'custom') {
    if (!marker.custom) throw new TypeError('Custom marker needs a colour')
    return mode === 'light' ? marker.custom : deriveDark(marker.custom)
  }
  return modeValue(values[marker.source], mode)
}
const mix = (c: string, p: number) => `color-mix(in srgb, ${c} ${p}%, transparent)`
const accentTokens = (k: string, r: AccentRoles) => ({
  [`--${k}`]: r.fill, [`--${k}-hi`]: r.hi, [`--${k}-lo`]: r.lo, [`--${k}-on`]: r.on,
  [`--${k}-line`]: r.line, [`--${k}-ink`]: r.ink, [`--${k}-tint`]: r.t1, [`--${k}-tint-2`]: r.t2, [`--${k}-tint-3`]: r.t3,
})
export function agentTokens(agents: AgentThemeAppearance, mode: ColourMode): Record<string, string> {
  const palette = normalizeAgentPalette(agents.palette)
  const preset = AGENT_PALETTES.find(p => p.id === palette) ?? AGENT_PALETTES[0]!
  if (palette === 'custom' && !agents.custom_states) throw new TypeError('Custom states need five accents')
  const colours = AGENT_STATES.map((state, i) => palette === 'custom' ? modeValue(agents.custom_states![state], mode) : preset[mode][i]!)
  return {
    ...Object.fromEntries(AGENT_STATES.flatMap((state, i) => [[`--agent-${state}`, colours[i]!], [`--agent-${state}-ink`, inkOn(colours[i]!)]])),
    '--agent-idle-opacity': String((agents.dim_inactive ?? true) ? (agents.inactive_opacity ?? 55) / 100 : 1),
    '--agent-art-scale': String(agents.size == null ? 1 : Math.round(agents.size / nativeIconSize(agents.avatar) * 1000) / 1000),
  }
}
/** Isolated previews and legacy preferences follow their container's color-scheme. */
export function agentPreviewTokens(agents: AgentThemeAppearance): Record<string, string> {
  const light = agentTokens(agents, 'light'), dark = agentTokens(agents, 'dark')
  return Object.fromEntries(Object.entries(light).map(([key, value]) => [key, AGENT_STATES.some(state => key === `--agent-${state}` || key === `--agent-${state}-ink`) ? `light-dark(${value}, ${dark[key]})` : value]))
}
export function themeTokens(v: ThemeValues, mode: ColourMode): Record<string, string> {
  const light = mode === 'light', P = roles(modeValue(v.primary, mode), mode), S = roles(modeValue(v.secondary, mode), mode), mk = markerValue(v, mode)
  return {
    ...agentTokens(v.agents, mode), ...accentTokens('primary', P), ...accentTokens('secondary', S), '--marker': mk, '--marker-on': inkOn(mk),
    '--teal': P.line, '--teal-ink': P.ink, '--aqua': P.t1, '--aqua-2': P.t2, '--aqua-3': P.t3, '--button-ink': P.on, '--level-person': P.line,
    '--row-hover': light ? mix(P.line, 5.5) : mix(P.fill, 6), '--row-selected': light ? mix(P.t1, 30) : mix(P.fill, 11),
    '--chip-teal-bg': light ? `linear-gradient(180deg, #fff, ${P.t2})` : `linear-gradient(180deg, ${mix(P.fill, 20)}, ${mix(P.fill, 10)})`,
    '--chip-teal-line': light ? mix(P.t1, 95) : mix(P.fill, 40),
    '--seg-on': light ? `linear-gradient(180deg, #fff, ${P.t3})` : `linear-gradient(180deg, ${mix(P.fill, 24)}, ${mix(P.fill, 12)})`,
    '--btn-bg-hover': light ? `linear-gradient(180deg, #fff, ${mix(P.t3, 80)})` : `linear-gradient(180deg, ${mix(P.fill, 18)}, ${mix(P.fill, 8)})`,
    '--glass-rim': light ? mix(P.t1, 45) : mix(P.fill, 18), '--wash-1': light ? mix(P.t2, 85) : mix(P.fill, 12),
    '--focus-ring': `0 0 0 2px ${P.line}`,
    '--avatar-bg': light ? `radial-gradient(circle at 30% 25%, #fff, ${P.t2})` : `radial-gradient(circle at 30% 25%, ${mix(P.fill, 35)}, ${mix(P.fill, 10)})`,
    '--mark-hl': light ? mix(S.t1, 70) : mix(S.fill, 35), '--wash-3': light ? mix(S.t1, 16) : mix(S.fill, 8),
  }
}
export const THEME_SELECTORS = [':root, :root[data-theme="light"]', ':root[data-theme="dark"]', ':root:not([data-theme="light"])'] as const
const block = (values: Record<string, string>) => Object.entries(values).map(([k, x]) => `${k}: ${x};`).join(' ')
export function themeCss(values: ThemeValues) {
  const L = block(themeTokens(values, 'light')), D = block(themeTokens(values, 'dark'))
  return `${THEME_SELECTORS[0]} { ${L} }\n${THEME_SELECTORS[1]} { ${D} }\n@media (prefers-color-scheme: dark) { ${THEME_SELECTORS[2]} { ${D} } }`
}
/** Same saved values as internal/themes.Porcelain, including its explicit dark colours. */
export const PORCELAIN: ThemeValues = {
  primary: { light: '#0e6f6c', dark: '#a4e5df' }, secondary: { light: '#d69b31', dark: '#e2b45a' },
  recurring_marker: { source: 'secondary', custom: null },
  agents: { avatar: 'robot-1', ring: null, hover: false, size: null, palette: 'standard', custom_states: null },
}
