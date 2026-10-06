// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import type { ActiveTheme } from './themes.ts'
import type { AgentIndicatorStyle, IndicatorRing } from './indicatorVariants.ts'
import type { AgentPalette } from './agentPalettes.ts'
import { publishTheme, resetTheme } from './themeRuntime.ts'

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
let restoreEditor: ((identity: string) => Promise<void>) | null = null
/** The editor registers its shared restoration path without a session import cycle. */
export function registerAgentThemeRestoration(restore: (identity: string) => Promise<void>) {
  restoreEditor = restore
}
export function resetAgentTheme(identity = '') {
  epoch++; person = identity; agentTheme.value = null; agentThemeBound.value = true
  resetTheme(identity)
}
export function installAgentTheme(value: AgentThemeAppearance) {
  agentTheme.value = { ...value }
}
// The theme editor (stores/themeEditor) is the one writer after sign-in: it
// publishes every server-confirmed active theme for the identity it loaded it
// for. An answer for another person, or for a session that has since been
// reset, never reaches the runtime.
export function publishAgentTheme(identity: string, active: ActiveTheme) {
  if (!identity || identity !== person) return false
  return publishTheme(identity, active)
}
export async function restoreAgentTheme(identity: string) {
  if (person !== identity) resetAgentTheme(identity)
  const started = epoch
  try {
    // Load the model once on sign-in, keeping isolated appearance previews
    // independent of the session. Subsequent calls capture their token now.
    if (!restoreEditor) await import('../stores/themeEditor')
    if (started !== epoch || person !== identity) return
    // Capture the operation synchronously, through the same model as edits.
    // No runtime-only read can disagree with the confirmed editor record.
    await restoreEditor!(identity)
  } catch { /* A failed load leaves the neutral default, never a stale person. */ }
}
