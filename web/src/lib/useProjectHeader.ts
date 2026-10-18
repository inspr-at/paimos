// SPDX-License-Identifier: AGPL-3.0-only
import { computed, ref, watch } from 'vue'
import { useSession } from '../stores/session'
import { chooseHeader, DEFAULT_HEADER, headerStorageKey, readHeaderPreference, toggleHeader, type HeaderDensity } from './projectHeader'

// Device-local by design. Neither saved views nor the synced row-height setting
// own header density. Switching workspace/person clears the in-memory choice.
const preference = ref({ ...DEFAULT_HEADER })
let owner: string | null = null
export function useProjectHeader() {
  const session = useSession()
  const key = computed(() => session.identity ? headerStorageKey(session.identity.tenant.id, session.identity.principal.id) : null)
  watch(key, value => {
    if (owner === value) return
    owner = value
    let raw: string | null = null
    try { if (value) raw = localStorage.getItem(value) } catch { /* device storage may be disabled */ }
    preference.value = readHeaderPreference(raw)
  }, { immediate: true, flush: 'sync' })
  function save(next: typeof preference.value) {
    if (!key.value || key.value !== owner) return
    preference.value = next
    try { localStorage.setItem(key.value, JSON.stringify(next)) } catch { /* session choice remains usable */ }
  }
  return {
    headerDensity: computed(() => preference.value.density),
    roomyHeader: computed(() => preference.value.roomy),
    setHeaderDensity: (value: HeaderDensity) => save(chooseHeader(preference.value, value)),
    toggleHeader: () => save(toggleHeader(preference.value)),
  }
}
