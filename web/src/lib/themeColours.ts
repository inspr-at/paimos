// SPDX-License-Identifier: AGPL-3.0-only
// Shared, bounded colour math for the Theme editor and its appearance consumers.
export type ColourMode = 'light' | 'dark'
export const CARD_COLOURS = { light: '#fffefa', dark: '#1c393d' } as const
export const COLOUR_PRESETS = ['#0e6f6c', '#0a4f4d', '#2f7a5a', '#3a5fc4', '#5b52c9', '#8547b0', '#bf3d6d', '#b24a44', '#b5642a', '#d69b31', '#a8680c', '#4c6163']
export const validColour = (value: string) => /^#[0-9a-f]{6}$/i.test(value)
function rgb(hex: string) { return [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255) }
function luminance(hex: string) {
  const c = rgb(hex).map(v => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)
  return c[0]! * 0.2126 + c[1]! * 0.7152 + c[2]! * 0.0722
}
export function colourContrast(a: string, b: string) {
  const x = luminance(a), y = luminance(b)
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)
}
export function colourHsl(hex: string): [number, number, number] {
  const [r, g, b] = rgb(hex) as [number, number, number]
  const max = Math.max(r, g, b), min = Math.min(r, g, b), l = (max + min) / 2, d = max - min
  if (!d) return [0, 0, l]
  const h = max === r ? (g - b) / d + (g < b ? 6 : 0) : max === g ? (b - r) / d + 2 : (r - g) / d + 4
  return [h / 6, l > 0.5 ? d / (2 - max - min) : d / (max + min), l]
}
export function colourFromHsl(h: number, s: number, l: number) {
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s, p = 2 * l - q
  const channel = (t: number) => {
    t = (t + 1) % 1
    return t < 1 / 6 ? p + (q - p) * 6 * t : t < 1 / 2 ? q : t < 2 / 3 ? p + (q - p) * (2 / 3 - t) * 6 : p
  }
  return '#' + (s ? [channel(h + 1 / 3), channel(h), channel(h - 1 / 3)] : [l, l, l]).map(v => Math.round(v * 255).toString(16).padStart(2, '0')).join('')
}
export function derivedDark(hex: string) {
  const [h, saturation, lightness] = colourHsl(hex), s = Math.min(saturation, 0.7)
  let out = colourFromHsl(h, s, lightness)
  for (let i = 1; i <= 100 && colourContrast(out, CARD_COLOURS.dark) < 6; i++) {
    out = colourFromHsl(h, s, Math.min(0.9, lightness + i / 100))
  }
  return out
}
// Search quantised lightness in both directions; retain hue/saturation and
// choose the nearest passing shade. Verify the rounded hex, not its float RGB.
export function suggestColour(hex: string, mode: ColourMode) {
  if (colourContrast(hex, CARD_COLOURS[mode]) >= 4.5) return hex
  const [h, s, l] = colourHsl(hex)
  for (let i = 1; i <= 10000; i++) {
    for (const direction of mode === 'light' ? [-1, 1] : [1, -1]) {
      const next = l + direction * i / 10000
      if (next < 0 || next > 1) continue
      const candidate = colourFromHsl(h, s, next)
      if (colourContrast(candidate, CARD_COLOURS[mode]) >= 4.5) return candidate
    }
  }
  return hex
}
