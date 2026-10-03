// SPDX-License-Identifier: AGPL-3.0-only
import { shallowRef } from 'vue'
import { api } from './api.ts'
import { AGENT_PALETTES, isAgentPalette } from './agentPalettes.ts'
import { inkOn, isHexColour, PORCELAIN, themeTokens, type ThemeValues } from './themeValues.ts'
import { toast } from './toast.ts'
import { installAgentTheme, resetAgentTheme } from './agentTheme.ts'

export const activeThemeValues = shallowRef<ThemeValues | null>(null)
let generation = 0
let applied: string[] = []
let currentDark = false
export function validThemeValues(value: unknown): value is ThemeValues {
  if (!value || typeof value !== 'object') return false
  const v = value as ThemeValues
  return [v.primary, v.secondary].every(a => a && isHexColour(a.light) && (a.dark === null || isHexColour(a.dark))) &&
    !!v.recurring_marker && ['primary', 'secondary', 'neutral', 'custom'].includes(v.recurring_marker.source) &&
    (v.recurring_marker.custom === null || isHexColour(v.recurring_marker.custom)) &&
    (v.recurring_marker.source !== 'custom' || isHexColour(v.recurring_marker.custom)) && !!v.agents && isAgentPalette(v.agents.palette)
}
export function agentPaletteInkTokens(styles: Pick<CSSStyleDeclaration, 'getPropertyValue'>): Record<string, string> {
  const tokens: Record<string, string> = {}
  for (const palette of AGENT_PALETTES) for (const state of ['working', 'waiting', 'throttled', 'problem', 'inactive']) {
    const token = `--agent-${palette.id}-${state}`
    const colour = styles.getPropertyValue(token).trim()
    const resolved = colour.startsWith('var(') ? styles.getPropertyValue(colour.slice(4, -1)).trim() : colour
    if (isHexColour(resolved)) tokens[`${token}-ink`] = inkOn(resolved)
  }
  return tokens
}
// The same function serves a saved selection, saved edit and mode/OS changes.
// Editors apply only an accepted server response; drafts use themeTokens on their preview.
export function applyAppearanceTheme(values: ThemeValues, dark = currentDark) {
  if (!validThemeValues(values)) throw new Error('Invalid theme colours')
  generation++
  activeThemeValues.value = structuredClone(values)
  installAgentTheme(values.agents)
  renderAppearanceTheme(dark)
}
export function renderAppearanceTheme(dark: boolean) {
  currentDark = dark
  if (typeof document === 'undefined') return
  const root = document.documentElement, tokens = themeTokens(activeThemeValues.value ?? PORCELAIN, dark)
  // Semantic aliases preserve all five accessible palettes, independent of the accents.
  const styles = getComputedStyle(root)
  for (const name of ['danger', 'st-ok']) {
    const colour = styles.getPropertyValue(`--${name}`).trim()
    if (isHexColour(colour)) tokens[`--${name}-on`] = inkOn(colour)
  }
  Object.assign(tokens, agentPaletteInkTokens(styles))
  for (const [key, value] of Object.entries(tokens)) root.style.setProperty(key, value)
  applied = Object.keys(tokens)
}
export function resetAppearanceTheme() {
  generation++
  activeThemeValues.value = null
  resetAgentTheme()
  if (typeof document !== 'undefined') for (const key of applied) document.documentElement.style.removeProperty(key)
  applied = []
}
// The caller supplies an identity+epoch check. It is rechecked after each await,
// so a late read can neither recolour a different person nor announce their theme.
export async function restoreAppearanceTheme(current: () => boolean = () => true) {
  const started = ++generation
  const live = () => started === generation && current()
  try {
    const response = await api('/me/theme', {}, 5000)
    if (!live()) return
    if (response.status === 404) { applyAppearanceTheme(PORCELAIN); return }
    if (!response.ok) throw new Error('Theme could not be loaded')
    const limit = 16 * 1024
    if (Number(response.headers.get('Content-Length')) > limit) { await response.body?.cancel(); throw new Error('Theme response is too large') }
    if (!response.body) throw new Error('Empty theme response')
    const reader = response.body.getReader(), chunks: Uint8Array[] = []
    let size = 0
    try {
      for (;;) {
        const chunk = await reader.read()
        if (chunk.done) break
        size += chunk.value.byteLength
        if (size > limit) { await reader.cancel(); throw new Error('Theme response is too large') }
        chunks.push(chunk.value)
      }
    } finally { reader.releaseLock() }
    const raw = new Uint8Array(size)
    let offset = 0
    for (const chunk of chunks) { raw.set(chunk, offset); offset += chunk.byteLength }
    const bytes = new TextDecoder('utf-8', { fatal: true }).decode(raw)
    if (!live()) return
    const body = JSON.parse(bytes)
    if (!validThemeValues(body?.theme?.values)) throw new Error('Invalid theme response')
    applyAppearanceTheme(body.theme.values)
    if (body.fallback_notice) toast('The theme you used was deleted. The workspace default is now in use.')
  } catch {
    if (live()) { applyAppearanceTheme(PORCELAIN); toast('Your theme could not be loaded. Default colours are shown.', { tone: 'error' }) }
  }
}
