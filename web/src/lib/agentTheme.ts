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
let selection: ActiveTheme | null = null
export function resetAgentTheme(identity = '') {
  epoch++; identityEpoch++; person = identity; selection = null; agentTheme.value = null; agentThemeBound.value = true
}
export function installAgentTheme(value: AgentThemeAppearance, current?: ActiveTheme) {
  if (current && selection && (current.revision < selection.revision ||
      (current.revision === selection.revision && current.theme.id === selection.theme.id && current.theme.revision < selection.theme.revision))) return false
  epoch++; agentTheme.value = { ...value }
  selection = current ?? null
  return true
}
type ThemeReconciliation =
  | { kind: 'read'; active: ActiveTheme }
  | { kind: 'save'; theme: ThemeRecord }
  | { kind: 'select' | 'delete'; active: ActiveTheme; themeId: string }
// Reads and committed writes share one session/selection fence, independent of
// editor lifetime. Keep the full accepted record so a stale editor read can use
// the reconciled result instead of showing obsolete controls or a blank editor.
export function captureAgentThemeReconciliation(revision?: number) {
  const started = identityEpoch, owner = person, readEpoch = epoch, before = selection
  const sameSession = () => started === identityEpoch && owner === person && !!owner
  function reconcile(change: ThemeReconciliation): ActiveTheme | null {
    if (!sameSession()) return null
    if (change.kind === 'read') {
      if (readEpoch === epoch) installAgentTheme(change.active.theme.values.agents, change.active)
      return selection
    }
    if (!selection || revision === undefined) return null
    let chosen: ActiveTheme
    if (change.kind === 'save') {
      const saved = change.theme
      if (before?.theme.id !== saved.id || selection.theme.id !== saved.id ||
          selection.revision !== revision || selection.theme.revision > saved.revision) return null
      chosen = { ...selection, theme: saved }
    } else {
      chosen = change.active
      if (change.kind === 'select') {
        if (chosen.theme.id !== change.themeId || chosen.revision < revision ||
            (selection.revision !== revision && selection.revision !== chosen.revision) ||
            (selection.revision === chosen.revision && selection.theme.id !== change.themeId)) return null
      } else {
        // Deletion changes the effective theme without advancing selection CAS.
        // Accept only this deletion's fallback, never a later choice/undo.
        if (before?.theme.id !== change.themeId || chosen.revision !== revision ||
            chosen.theme.id !== chosen.default_theme_id || chosen.selected_theme_id !== change.themeId ||
            chosen.fallback_notice?.deleted_theme_id !== change.themeId || selection.revision !== revision ||
            (selection.theme.id !== change.themeId && selection.theme.id !== chosen.theme.id) ||
            (selection.theme.id === change.themeId && selection.theme.revision > before.theme.revision)) return null
      }
    }
    return installAgentTheme(chosen.theme.values.agents, chosen) ? chosen : null
  }
  return { sameSession, reconcile }
}
export async function restoreAgentTheme(identity: string) {
  if (person !== identity) resetAgentTheme(identity)
  ++epoch
  const { reconcile } = captureAgentThemeReconciliation()
  try {
    const response = await api('/me/theme')
    if (!response.ok) return
    const active = await response.json()
    if (active?.theme?.values?.agents) reconcile({ kind: 'read', active })
  } catch { /* A failed load leaves the neutral default, never a stale person. */ }
}
