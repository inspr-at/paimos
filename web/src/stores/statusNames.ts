// SPDX-License-Identifier: AGPL-3.0-only
// The workspace's status names (AEON-1401) for the signed-in person. The router
// reads them once before a page shows, so lists fit their columns to the names
// people read; Settings saves and live events replace them in place.
import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { api } from '../lib/api'
import { scopeOwner } from '../lib/identityScope'
import { onStatusNamesChanged, readStatusNames, setStatusNames, type StatusNames } from '../lib/statusNames'
import { useSession } from './session'

export const useStatusNames = defineStore('statusNames', () => {
  const session = useSession()
  const owner = computed(() => scopeOwner(session.identity))
  const value = ref<StatusNames>({ revision: 0, names: {} })
  const loaded = ref(false), error = ref('')
  let generation = 0, pending: Promise<void> | undefined, controller: AbortController | undefined
  function show(next: unknown) { value.value = readStatusNames(next); setStatusNames(value.value.names) }
  watch(owner, () => {
    generation++; controller?.abort(); pending = undefined
    loaded.value = false; error.value = ''
    show({ revision: 0, names: {} })
  }, { flush: 'sync' })
  // A rename elsewhere (another tab, another person) arrives as a live event: read again.
  const stop = onStatusNamesChanged(() => { if (loaded.value) void load(true) })
  onScopeDispose(() => { generation++; controller?.abort(); stop() })

  // A save answers with the names it stored; an older answer never replaces newer names.
  function accept(next: StatusNames): StatusNames {
    if (!owner.value) return next
    if (loaded.value && next.revision < value.value.revision) return value.value
    generation++; controller?.abort(); pending = undefined
    show(next); loaded.value = true; error.value = ''
    return value.value
  }
  function load(force = false): Promise<void> {
    if (!owner.value || (loaded.value && !force)) return Promise.resolve()
    // A live event during a read may postdate what that read saw: read once more after it.
    if (pending) return force ? pending.then(() => load(true)) : pending
    const run = generation, person = owner.value
    controller = new AbortController()
    // Bounded: a slow read never holds a page; the built-in names show meanwhile.
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(5_000)])
    pending = (async () => {
      try {
        const response = await api('/settings/status-names', { signal })
        if (!response.ok) throw new Error('Status names could not be read. The built-in names show.')
        const next = readStatusNames(await response.json())
        if (run !== generation || person !== owner.value) return
        if (!loaded.value || next.revision >= value.value.revision) show(next)
        loaded.value = true; error.value = ''
      } catch (cause) {
        if (run === generation && person === owner.value) error.value = cause instanceof Error ? cause.message : 'Status names could not be read.'
      } finally { if (run === generation) pending = undefined }
    })()
    return pending
  }
  return { value, loaded, error, load, accept }
})
