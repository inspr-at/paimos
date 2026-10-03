// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import { api } from './api.ts'
import type { ActiveTheme, ThemeRecord } from './themes.ts'
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
let person = '', epoch = 0, identityEpoch = 0
let selection: { themeId: string; revision: number; themeRevision: number } | null = null
export function resetAgentTheme(identity = '') {
  epoch++; identityEpoch++; person = identity; selection = null; agentTheme.value = null; agentThemeBound.value = true
}
export function installAgentTheme(value: AgentThemeAppearance, current?: ActiveTheme) {
  if (current && selection && (current.revision < selection.revision ||
      (current.revision === selection.revision && current.theme.id === selection.themeId && current.theme.revision < selection.themeRevision))) return
  epoch++; agentTheme.value = { ...value }
  selection = current ? { themeId: current.theme.id, revision: current.revision, themeRevision: current.theme.revision } : null
}
// A committed write outlives its editor. Reconcile only the same session and
// selected record/CAS generation, and never overwrite a newer record revision.
export function captureAgentThemeSave(themeId: string, revision: number) {
  const started = identityEpoch, owner = person
  return (saved: ThemeRecord) => {
    if (started !== identityEpoch || owner !== person || !owner || saved.id !== themeId ||
        selection?.themeId !== themeId || selection.revision !== revision || selection.themeRevision > saved.revision) return
    epoch++; selection.themeRevision = saved.revision; agentTheme.value = { ...saved.values.agents }
  }
}
// Selection commits also outlive the editor. A response may reconcile the
// captured CAS or an already-loaded result, but never a different selection.
export function captureAgentThemeSelection(themeId: string, revision: number) {
  const started = identityEpoch, owner = person
  return (chosen: ActiveTheme) => {
    if (started !== identityEpoch || owner !== person || !owner || chosen.theme.id !== themeId || chosen.revision < revision ||
        !selection || (selection.revision !== revision && selection.revision !== chosen.revision) ||
        (selection.revision === chosen.revision && (selection.themeId !== themeId || selection.themeRevision > chosen.theme.revision))) return false
    installAgentTheme(chosen.theme.values.agents, chosen)
    return true
  }
}
export async function restoreAgentTheme(identity: string) {
  if (person !== identity) resetAgentTheme(identity)
  const started = ++epoch
  try {
    const response = await api('/me/theme')
    if (!response.ok) return
    const active = await response.json()
    if (started === epoch && person === identity && active?.theme?.values?.agents) {
      installAgentTheme(active.theme.values.agents, active)
    }
  } catch { /* A failed load leaves the neutral default, never a stale person. */ }
}
