// SPDX-License-Identifier: AGPL-3.0-only
// Shared, bounded colour math for the Theme editor and its appearance consumers.
export { CARD_COLOURS, colourContrast, validColour, type ColourMode } from './themeEngine.ts'
export const COLOUR_PRESETS = ['#0e6f6c', '#0a4f4d', '#2f7a5a', '#3a5fc4', '#5b52c9', '#8547b0', '#bf3d6d', '#b24a44', '#b5642a', '#d69b31', '#a8680c', '#4c6163']
function rgb(hex: string) { return [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255) }
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
