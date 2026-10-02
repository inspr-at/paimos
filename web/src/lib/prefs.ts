// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import { DEFAULT_MODEL_DISPLAY, type ModelDisplayPrefs } from './planning'
import { usePreference } from './preferences'

// Row height and the project-header graph are the person's own choices and follow
// them to every device (GET/PUT /api/preferences/list:display). Until that loads,
// lists are comfortable and the header graph is on.
export type Density = 'comfortable' | 'compact'
type DisplayPrefs = { density?: Density; headerGraph?: boolean } & Partial<ModelDisplayPrefs>
export const modelDisplay = ref<ModelDisplayPrefs>({ ...DEFAULT_MODEL_DISPLAY })
export const density = ref<Density>('comfortable')
export const headerGraph = ref(true)
export const headerGraphReady = ref(false)
let loading: Promise<void> | null = null
function settle(saved: DisplayPrefs | null | undefined) {
  if (saved?.density === 'compact' || saved?.density === 'comfortable') density.value = saved.density
  if (typeof saved?.headerGraph === 'boolean') headerGraph.value = saved.headerGraph
  modelDisplay.value = {
    effortMeter: typeof saved?.effortMeter === 'boolean' ? saved.effortMeter : DEFAULT_MODEL_DISPLAY.effortMeter,
    modelNames: saved?.modelNames === 'full' ? 'full' : 'short',
    modelVersion: saved?.modelVersion === 'hide' ? 'hide' : 'show',
  }
  headerGraphReady.value = true
}
export function useDensity() {
  const pref = usePreference<DisplayPrefs>('list:display')
  loading ??= pref.ready.then(() => settle(pref.value.value))
  function set(value: Density) {
    density.value = value
    pref.save({ ...(pref.value.value ?? {}), density: value }, 0)
  }
  return { density, set }
}
export function useHeaderGraph() {
  const pref = usePreference<DisplayPrefs>('list:display')
  loading ??= pref.ready.then(() => settle(pref.value.value))
  function set(value: boolean) {
    headerGraph.value = value
    pref.save({ ...(pref.value.value ?? {}), headerGraph: value }, 0)
  }
  return { headerGraph, ready: headerGraphReady, set }
}

// These choices are per person across projects and saved views.
export function useModelDisplay() {
  const pref = usePreference<DisplayPrefs>('list:display')
  loading ??= pref.ready.then(() => settle(pref.value.value))
  function set<K extends keyof ModelDisplayPrefs>(key: K, value: ModelDisplayPrefs[K]) {
    modelDisplay.value = { ...modelDisplay.value, [key]: value }
    pref.save({ ...(pref.value.value ?? {}), ...modelDisplay.value }, 0)
  }
  return { modelDisplay, ready: loading, set }
}
