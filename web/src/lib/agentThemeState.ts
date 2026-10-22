// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import type { ThemeAccent } from './themes.ts'
import type { AgentIndicatorStyle, IndicatorRing } from './indicatorVariants.ts'
import type { AgentColourState, AgentPalette } from './agentPalettes.ts'

export interface AgentThemeAppearance {
  avatar: AgentIndicatorStyle; ring: IndicatorRing | null; hover: boolean; size: number | null; palette: AgentPalette
  custom_states?: Record<AgentColourState, ThemeAccent> | null
  dim_inactive?: boolean; inactive_opacity?: number
}
export const defaultAgentTheme: Readonly<AgentThemeAppearance> = {
  avatar: 'robot-1', ring: null, hover: false, size: null, palette: 'standard', custom_states: null,
}
export const agentTheme = ref<AgentThemeAppearance | null>(null)
// Legacy preference consumers remain compatible in isolated previews. Once a
// session is bound, a missing/failed theme never resurrects another person's cache.
export const agentThemeBound = ref(false)
export function installAgentTheme(value: AgentThemeAppearance) {
  agentTheme.value = { ...value }
}
