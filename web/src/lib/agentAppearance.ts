// SPDX-License-Identifier: AGPL-3.0-only
import { computed } from 'vue'
import { usePreference } from './preferences.ts'
import { inactiveState, normalizeAgentState, type AgentState, type AgentStatePreference } from './agentSignals.ts'

export const AGENT_STATE_KEY = 'agent-state'
export function useAgentAppearance() {
  const preference = usePreference<AgentStatePreference>(AGENT_STATE_KEY)
  const choice = computed(() => normalizeAgentState(preference.value.value))
  const save = (patch: Partial<AgentStatePreference>) => preference.save(normalizeAgentState({ ...choice.value, ...patch }), 0)
  const appearance = (state: AgentState) => ({
    '--agent-state-color': `var(--agent-${choice.value.palette}-${inactiveState(state) ? 'inactive' : state === 'unresponsive' ? 'problem' : state === 'awaiting' ? 'waiting' : state})`,
    '--agent-state-opacity': String(inactiveState(state) && choice.value.dimInactive ? choice.value.inactiveOpacity / 100 : 1),
    '--agent-state-saturation': inactiveState(state) || choice.value.palette === 'monochrome' ? '0' : '1',
  })
  return { choice, ready: preference.ready, save, appearance }
}
