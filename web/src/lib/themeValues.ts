// SPDX-License-Identifier: AGPL-3.0-only
// Colour maths from the accepted Theme fragment, draft 4 (AEON-638).
import type { AgentThemeAppearance } from './agentTheme.ts'
export interface ThemeAccent { light: string; dark: string | null }
export interface ThemeValues {
  primary: ThemeAccent; secondary: ThemeAccent
  recurring_marker: { source: 'primary' | 'secondary' | 'neutral' | 'custom'; custom: string | null }
  agents: AgentThemeAppearance
}
export const WHITE_INK = '#ffffff', DARK_INK = '#102327'
export const LIGHT_CARD = '#fffefa', DARK_CARD = '#1c393d'
export const PORCELAIN: ThemeValues = {
  primary: { light: '#0e6f6c', dark: '#a4e5df' }, secondary: { light: '#d69b31', dark: '#e2b45a' },
  recurring_marker: { source: 'secondary', custom: null },
  agents: { avatar: 'robot-1', ring: null, hover: false, size: null, palette: 'standard' },
}
export const isHexColour = (v: unknown): v is string => typeof v === 'string' && /^#[0-9a-f]{6}$/i.test(v)
const rgb = (hex: string) => [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255)
function luminance(hex: string) {
  const c = rgb(hex).map(v => v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)
  return .2126 * c[0]! + .7152 * c[1]! + .0722 * c[2]!
}
export function contrastRatio(a: string, b: string) {
  const x = luminance(a), y = luminance(b)
  return (Math.max(x, y) + .05) / (Math.min(x, y) + .05)
}
export const inkOn = (fill: string) => contrastRatio(WHITE_INK, fill) >= contrastRatio(DARK_INK, fill) ? WHITE_INK : DARK_INK
function toHsl(hex: string): [number, number, number] {
  const [r, g, b] = rgb(hex) as [number, number, number], hi = Math.max(r, g, b), lo = Math.min(r, g, b), l = (hi + lo) / 2
  if (hi === lo) return [0, 0, l]
  const d = hi - lo, s = l > .5 ? d / (2 - hi - lo) : d / (hi + lo)
  return [(hi === r ? (g - b) / d + (g < b ? 6 : 0) : hi === g ? (b - r) / d + 2 : (r - g) / d + 4) / 6, s, l]
}
function fromHsl(h: number, s: number, l: number) {
  const f = (p: number, q: number, input: number) => {
    const t = (input + 1) % 1
    return t < 1 / 6 ? p + (q - p) * 6 * t : t < .5 ? q : t < 2 / 3 ? p + (q - p) * (2 / 3 - t) * 6 : p
  }
  const q = l < .5 ? l * (1 + s) : l + s - l * s, p = 2 * l - q
  return '#' + (s === 0 ? [l, l, l] : [f(p, q, h + 1 / 3), f(p, q, h), f(p, q, h - 1 / 3)]).map(v => Math.round(v * 255).toString(16).padStart(2, '0')).join('')
}
export function deriveDark(hex: string) {
  const [h, saturation, lightness] = toHsl(hex), s = Math.min(saturation, .7)
  let l = lightness, out = fromHsl(h, s, l)
  // At most 90 lightness steps, including rounded colours, as in draft 4.
  while (contrastRatio(out, DARK_CARD) < 6 && l < .9) { l += .01; out = fromHsl(h, s, l) }
  return out
}
export function textOn(hex: string, surface: string) {
  const [h, s, lightness] = toHsl(hex), dark = luminance(surface) < .2
  let l = lightness, out = hex
  // Only the destination bound matters: an extreme shade must move inward.
  while (contrastRatio(out, surface) < 4.5 && (dark ? l < .95 : l > .05)) {
    l = dark ? Math.min(.95, l + .02) : Math.max(.05, l - .02)
    out = fromHsl(h, s, l)
  }
  return out
}
export function themeTokens(values: ThemeValues, dark: boolean): Record<string, string> {
  const mode = (accent: ThemeAccent) => dark ? accent.dark ?? deriveDark(accent.light) : accent.light
  const primary = mode(values.primary), secondary = mode(values.secondary), surface = dark ? DARK_CARD : LIGHT_CARD
  const marker = values.recurring_marker.source === 'primary' ? primary : values.recurring_marker.source === 'secondary' ? secondary : values.recurring_marker.source === 'neutral' ? (dark ? '#8aa3a2' : '#7c8c8d') : dark ? deriveDark(values.recurring_marker.custom!) : values.recurring_marker.custom!
  const tint = (colour: string, amount: number, over = 'transparent') => `color-mix(in srgb, ${colour} ${amount}%, ${over})`
  const pText = textOn(primary, surface), sText = textOn(secondary, surface)
  const aqua = tint(primary, dark ? 28 : 38, surface), aqua2 = tint(primary, dark ? 18 : 24, surface), aqua3 = tint(primary, 10, surface)
  return {
    '--teal': primary, '--teal-ink': pText, '--aqua': aqua, '--aqua-2': aqua2, '--aqua-3': aqua3,
    '--gold': secondary, '--gold-2': secondary, '--gold-ink': sText,
    '--brand-teal': primary, '--brand-gold': secondary, '--button-ink': inkOn(primary), '--gold-on': inkOn(secondary),
    ...Object.fromEntries(['working', 'waiting', 'throttled', 'problem', 'inactive'].flatMap(state => [
      [`--agent-${state}`, `var(--agent-${values.agents.palette}-${state})`],
      [`--agent-${state}-ink`, `var(--agent-${values.agents.palette}-${state}-ink)`],
    ])),
    '--recurring-marker': marker, '--recurring-marker-ink': inkOn(marker),
    '--recurring-marker-wash': tint(marker, dark ? 12 : 9), '--recurring-marker-line': tint(marker, 35),
    '--row-hover': tint(primary, dark ? 6 : 5.5), '--row-selected': tint(primary, dark ? 11 : 12),
    '--chip-teal-bg': `linear-gradient(180deg, ${surface}, ${aqua2})`, '--chip-teal-line': tint(primary, 30),
    '--btn-bg-hover': `linear-gradient(180deg, ${surface}, ${aqua3})`, '--seg-on': `linear-gradient(180deg, ${surface}, ${aqua3})`,
    '--avatar-bg': `radial-gradient(circle at 30% 25%, ${surface}, ${aqua2})`,
    '--glass-rim': tint(primary, dark ? 18 : 25), '--aqua-wash': tint(primary, dark ? 9 : 15), '--gold-wash': tint(secondary, dark ? 6 : 12),
    '--mark-hl': tint(secondary, dark ? 38 : 45), '--warn': sText, '--warn-ink': sText,
    '--st-new': secondary, '--st-progress': primary, '--st-qa-fill': aqua, '--st-qa-ring': primary,
    '--eff-2': tint(primary, 65, surface), '--eff-3': primary, '--eff-4': secondary,
    '--wash-1': tint(primary, dark ? 12 : 15), '--wash-3': tint(secondary, dark ? 8 : 16), '--wash-4': tint(primary, dark ? 0 : 10),
    '--focus-ring': `0 0 0 2px ${tint(primary, 70)}, 0 0 18px ${tint(primary, 35)}`,
  }
}
