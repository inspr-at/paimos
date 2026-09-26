// SPDX-License-Identifier: AGPL-3.0-only
// Per-person graph orbit. Knowledge, tickets and the project glimpse share one
// account preference (the same store as the agent indicator). Missing or
// malformed values stay on Default: one revolution per 120 seconds.
import { computed } from 'vue'
import { usePreference } from './preferences.ts'
import { autoRotateSpeedFor, graphOrbitSeconds, publishGraphOrbitSeconds } from './graphOrbitState.ts'

export type GraphMotionPace = 'slow' | 'default' | 'lively' | 'off'
export interface GraphMotionPreference { pace: GraphMotionPace }
export const GRAPH_MOTION_KEY = 'graph-motion'
export const GRAPH_MOTION_SECONDS: Record<GraphMotionPace, number> = { slow: 180, default: 120, lively: 60, off: 0 }
export { autoRotateSpeedFor, graphOrbitSeconds }

const PACES = new Set<GraphMotionPace>(['slow', 'default', 'lively', 'off'])

export function normalizeGraphMotion(value: unknown): GraphMotionPreference {
  const saved = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  const pace = saved.pace
  return { pace: typeof pace === 'string' && PACES.has(pace as GraphMotionPace) ? pace as GraphMotionPace : 'default' }
}

let hooked = false
export function useGraphMotion() {
  const preference = usePreference<GraphMotionPreference>(GRAPH_MOTION_KEY)
  const choice = computed(() => normalizeGraphMotion(preference.value.value))
  const seconds = computed(() => GRAPH_MOTION_SECONDS[choice.value.pace])
  if (!hooked) {
    hooked = true
    const publish = () => publishGraphOrbitSeconds(seconds.value)
    publish()
    void preference.ready.then(publish)
  }
  function setPace(pace: GraphMotionPace) {
    preference.save({ pace }, 0)
    publishGraphOrbitSeconds(GRAPH_MOTION_SECONDS[pace])
  }
  return { choice, seconds, ready: preference.ready, setPace }
}

// Renderers that are not mounted through GraphCanvas still follow the setting.
export function ensureGraphMotion() { useGraphMotion() }
