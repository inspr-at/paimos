// SPDX-License-Identifier: AGPL-3.0-only
import { agentTheme, agentThemeBound } from './agentThemeState.ts'
import { resetTheme } from './themeRuntime.ts'

export { agentTheme, agentThemeBound, defaultAgentTheme, installAgentTheme } from './agentThemeState.ts'
export type { AgentThemeAppearance } from './agentThemeState.ts'
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
