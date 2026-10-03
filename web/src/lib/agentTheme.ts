// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import { api } from './api.ts'
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
export async function restoreAgentTheme(identity: string) {
  if (person !== identity) resetAgentTheme(identity)
  const started = ++epoch
  try {
    const response = await api('/me/theme')
    if (!response.ok) return
    const active = await response.json()
    if (started === epoch && person === identity && active?.theme?.values?.agents) {
      agentTheme.value = { ...active.theme.values.agents }
    }
  } catch { /* A failed load leaves the neutral default, never a stale person. */ }
}
