// SPDX-License-Identifier: AGPL-3.0-only
import { computed } from 'vue'
import { usePreference } from './preferences.ts'
import { isAgentIndicatorStyle, type AgentIndicatorStyle } from './indicatorVariants.ts'

export type { AgentIndicatorStyle } from './indicatorVariants.ts'
export interface AgentIndicatorPreference { style: AgentIndicatorStyle; hovering: boolean }
export const AGENT_INDICATOR_KEY = 'agent-indicator'

// A viewer's account preference, shared by every LiveBot (including wrappers).
// Legacy names migrate on read and are written canonically on the next save.
// Unknown settings keep LA2's stationary default. Hovering is independent.
export function normalizeAgentIndicator(value: unknown): AgentIndicatorPreference {
  const saved = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  const style = saved.style === 'calm' ? 'robot-1' : saved.style === 'playful' ? 'robot-5' : saved.style
  return { style: isAgentIndicatorStyle(style) ? style : 'robot-1', hovering: saved.hovering === true }
}

export function useAgentIndicator() {
  const preference = usePreference<AgentIndicatorPreference>(AGENT_INDICATOR_KEY)
  const choice = computed(() => normalizeAgentIndicator(preference.value.value))
  function setStyle(style: AgentIndicatorStyle) { preference.save({ ...choice.value, style }, 0) }
  function setHovering(hovering: boolean) { preference.save({ ...choice.value, hovering }, 0) }
  return { choice, ready: preference.ready, setStyle, setHovering }
}
