// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import { api } from './api.ts'
import type { ActiveTheme } from './themes.ts'
import type { AgentIndicatorStyle, IndicatorRing } from './indicatorVariants.ts'
import type { AgentPalette } from './agentPalettes.ts'

export interface AgentThemeAppearance {
  avatar: AgentIndicatorStyle; ring: IndicatorRing | null; hover: boolean; size: number | null; palette: AgentPalette
  dim_inactive?: boolean; inactive_opacity?: number
}
export const defaultAgentTheme: Readonly<AgentThemeAppearance> = {
  avatar: 'robot-1', ring: null, hover: false, size: null, palette: 'standard',
}
export const agentTheme = ref<AgentThemeAppearance | null>(null)
// Legacy preference consumers remain compatible in isolated previews. Once a
// session is bound, a missing/failed theme never resurrects another person's cache.
export const agentThemeBound = ref(false)
let person = '', epoch = 0
export function resetAgentTheme(identity = '') {
  epoch++; person = identity; agentTheme.value = null; agentThemeBound.value = true
}
export function installAgentTheme(value: AgentThemeAppearance) {
  epoch++; agentTheme.value = { ...value }
}
// The theme editor (stores/themeEditor) is the one writer after sign-in: it
// publishes every server-confirmed active theme for the identity it loaded it
// for. An answer for another person, or for a session that has since been
// reset, never reaches the runtime.
export function publishAgentTheme(identity: string, active: ActiveTheme) {
  if (!identity || identity !== person) return false
  installAgentTheme(active.theme.values.agents)
  return true
}
export async function restoreAgentTheme(identity: string) {
  if (person !== identity) resetAgentTheme(identity)
  const started = ++epoch
  try {
    const response = await api('/me/theme')
    if (!response.ok) return
    const active = await response.json()
    // Anything installed meanwhile (an editor answer, a reset) is newer.
    if (started === epoch && person === identity && active?.theme?.values?.agents) installAgentTheme(active.theme.values.agents)
  } catch { /* A failed load leaves the neutral default, never a stale person. */ }
}
