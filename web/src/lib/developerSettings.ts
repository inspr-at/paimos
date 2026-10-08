// SPDX-License-Identifier: AGPL-3.0-only
import { computed, reactive, watch } from 'vue'
import { useSession } from '../stores/session'
import { readPreference, writePreference } from './preferences'

export const DEVELOPER_SETTINGS_KEY = 'developer-ui'
interface Choice { show_reserved_versions?: boolean; show_expert_start?: boolean }
interface State { value: Choice; ready: Promise<void> | null; loaded: boolean; saving: boolean; failed: boolean }
// This opt-in belongs to the authenticated person and workspace, including
// account changes in the same document. Missing or unreadable values are off.
const states = new Map<string, State>()
export function useDeveloperSettings() {
  const session = useSession()
  const state = computed(() => {
    const identity = session.identity
    if (identity?.principal.kind !== 'person') return null
    const scope = JSON.stringify([identity.tenant.id, identity.principal.id])
    let entry = states.get(scope)
    if (!entry) {
      entry = reactive<State>({ value: {}, ready: null, loaded: false, saving: false, failed: false })
      states.set(scope, entry)
    }
    return entry
  })
  watch(state, entry => {
    if (!entry || entry.ready) return
    entry.ready = readPreference(DEVELOPER_SETTINGS_KEY).then(value => { entry.value = value ?? {}; entry.loaded = true })
  }, { immediate: true })
  const showReservedVersions = computed(() => state.value?.value.show_reserved_versions === true)
  // AEON-741: manual Start agent and Start now on… sit behind this expert opt-in.
  const showExpertStart = computed(() => state.value?.value.show_expert_start === true)
  const loading = computed(() => !!state.value && !state.value.loaded)
  const ready = computed(() => state.value?.ready ?? Promise.resolve())
  async function saveChoice(key: keyof Choice, show: boolean) {
    const entry = state.value
    if (!entry || entry.saving) return
    entry.saving = true
    entry.failed = false
    // An initial read cannot race the write or discard other preference fields.
    await entry.ready
    if (entry !== state.value) { entry.saving = false; return }
    const next = { ...entry.value, [key]: show }
    const saved = await writePreference(DEVELOPER_SETTINGS_KEY, next)
    // Reveal only after the deliberate choice has been saved successfully.
    if (saved) entry.value = next
    else entry.failed = true
    entry.saving = false
  }
  const setShowReservedVersions = (show: boolean) => saveChoice('show_reserved_versions', show)
  const setShowExpertStart = (show: boolean) => saveChoice('show_expert_start', show)
  return { showReservedVersions, setShowReservedVersions, showExpertStart, setShowExpertStart, loading, ready, saving: computed(() => state.value?.saving ?? false), failed: computed(() => state.value?.failed ?? false) }
}
