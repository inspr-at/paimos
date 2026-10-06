// SPDX-License-Identifier: AGPL-3.0-only
import { agentTheme, agentThemeBound, defaultAgentTheme } from './agentTheme.ts'
import { agentPreviewTokens } from './themeEngine.ts'
import { computed } from 'vue'
import { usePreference } from './preferences.ts'
import { inactiveState, normalizeAgentState, type AgentState, type AgentStatePreference } from './agentSignals.ts'

export const AGENT_STATE_KEY = 'agent-state'
export function useAgentAppearance() {
  const preference = usePreference<AgentStatePreference>(AGENT_STATE_KEY)
  const choice = computed(() => {
    const states = normalizeAgentState(preference.value.value)
    if (!agentThemeBound.value && !agentTheme.value) return states
    const theme = agentTheme.value ?? defaultAgentTheme
    return normalizeAgentState({ ...states, palette: theme.palette, dimInactive: theme.dim_inactive ?? true, inactiveOpacity: theme.inactive_opacity ?? 55 })
  })
  const save = (patch: Partial<AgentStatePreference>) => preference.save(normalizeAgentState({ ...normalizeAgentState(preference.value.value), ...patch }), 0)
  const legacyTokens = computed(() => !agentThemeBound.value && !agentTheme.value ? agentPreviewTokens({
    ...defaultAgentTheme, palette: choice.value.palette === 'custom' ? 'standard' : choice.value.palette,
    dim_inactive: choice.value.dimInactive, inactive_opacity: choice.value.inactiveOpacity,
  }) : {})
  const appearance = (state: AgentState) => ({
    ...legacyTokens.value,
    ...agentStateAppearance(state, choice.value),
  })
  return { choice, ready: preference.ready, save, appearance }
}
export const agentStateAppearance = (state: AgentState, choice: AgentStatePreference) => ({
    '--agent-state-color': state === 'paused' ? 'var(--ink-2)' : `var(--agent-${state === 'pausing' ? 'working' : inactiveState(state) ? 'idle' : state === 'unresponsive' ? 'problem' : state === 'awaiting' ? 'waiting' : state === 'done' ? 'working' : state})`,
    '--agent-state-opacity': inactiveState(state) ? 'var(--agent-idle-opacity)' : '1',
    '--agent-state-saturation': choice.palette === 'monochrome' ? '0' : '1',
  })
