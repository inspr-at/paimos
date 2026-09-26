// SPDX-License-Identifier: AGPL-3.0-only
import { computed } from 'vue'
import { usePreference } from './preferences.ts'

export type AgentIndicatorStyle = 'calm' | 'playful'
export interface AgentIndicatorPreference { style: AgentIndicatorStyle; hovering: boolean }
export const AGENT_INDICATOR_KEY = 'agent-indicator'

// A viewer's account preference, shared by every LiveBot (including wrappers).
// Missing/older/malformed settings keep LA2's stationary Calm default. Hovering
// is independent of the drawing; CSS still gives reduced motion precedence.
export function normalizeAgentIndicator(value: unknown): AgentIndicatorPreference {
  const saved = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  return { style: saved.style === 'playful' ? 'playful' : 'calm', hovering: saved.hovering === true }
}

export function useAgentIndicator() {
  const preference = usePreference<AgentIndicatorPreference>(AGENT_INDICATOR_KEY)
  const choice = computed(() => normalizeAgentIndicator(preference.value.value))
  function setStyle(style: AgentIndicatorStyle) { preference.save({ ...choice.value, style }, 0) }
  function setHovering(hovering: boolean) { preference.save({ ...choice.value, hovering }, 0) }
  return { choice, ready: preference.ready, setStyle, setHovering }
}
