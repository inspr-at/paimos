// SPDX-License-Identifier: AGPL-3.0-only
import { agentTheme, agentThemeBound, defaultAgentTheme } from './agentTheme.ts'
import { computed } from 'vue'
import { usePreference } from './preferences.ts'
import { clampIconSize, isAgentIndicatorStyle, isIndicatorRing, type AgentIndicatorStyle, type IndicatorRing } from './indicatorVariants.ts'

export type { AgentIndicatorStyle, IndicatorRing } from './indicatorVariants.ts'
/** ring and size stay absent until chosen, so older accounts keep each style's own ring and size. */
export interface AgentIndicatorPreference { style: AgentIndicatorStyle; hovering: boolean; ring?: IndicatorRing; size?: number }
export const AGENT_INDICATOR_KEY = 'agent-indicator'

// A viewer's account preference, shared by every LiveBot (including wrappers).
// Legacy names migrate on read and are written canonically on the next save.
// Unknown settings keep LA2's stationary default. Hovering, ring and size are
// independent of each other and of the style.
export function normalizeAgentIndicator(value: unknown): AgentIndicatorPreference {
  const saved = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  const style = saved.style === 'calm' ? 'robot-1' : saved.style === 'playful' ? 'robot-5' : saved.style
  const choice: AgentIndicatorPreference = { style: isAgentIndicatorStyle(style) ? style : 'robot-1', hovering: saved.hovering === true }
  if (isIndicatorRing(saved.ring)) choice.ring = saved.ring
  const size = clampIconSize(saved.size)
  if (size !== undefined) choice.size = size
  return choice
}

export function useAgentIndicator() {
  const preference = usePreference<AgentIndicatorPreference>(AGENT_INDICATOR_KEY)
  const choice = computed(() => {
    if (!agentThemeBound.value && !agentTheme.value) return normalizeAgentIndicator(preference.value.value)
    const theme = agentTheme.value ?? defaultAgentTheme
    return normalizeAgentIndicator({ style: theme.avatar, ring: theme.ring, hovering: theme.hover, size: theme.size })
  })
  const save = (patch: Partial<AgentIndicatorPreference>) => preference.save(normalizeAgentIndicator({ ...choice.value, ...patch }), 0)
  function setStyle(style: AgentIndicatorStyle) { save({ style }) }
  function setHovering(hovering: boolean) { save({ hovering }) }
  function setRing(ring: IndicatorRing) { save({ ring }) }
  /** undefined returns every style to its drawn size. */
  function setSize(size: number | undefined) { save({ size }) }
  return { choice, ready: preference.ready, setStyle, setHovering, setRing, setSize }
}
