// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useSession } from './session'
import { onAccessChange } from '../lib/authz'
import { loadDeskProjection, type DeskProjection } from '../lib/decisionDesk'

// The badge uses the server's total, never session presence or a loaded page.
export const useDecisionDesk = defineStore('decisionDesk', () => {
  const session = useSession()
  const owner = computed(() => session.identity?.principal.kind === 'person' ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
  const projection = ref<DeskProjection | null>(null), error = ref(''), loading = ref(false)
  let generation = 0, controller: AbortController | undefined, flight: Promise<void> | undefined
  function invalidate() {
    generation++; controller?.abort(); controller = undefined; flight = undefined
    projection.value = null; error.value = ''; loading.value = false
  }
  function refresh(): Promise<void> {
    if (!owner.value) return Promise.resolve()
    if (flight) return flight
    const identity = owner.value, turn = generation
    const abort = new AbortController(); controller = abort; loading.value = true
    const current = () => turn === generation && identity === owner.value && session.authenticationCurrent()
    flight = (async () => {
      try {
        const page = await loadDeskProjection(abort.signal)
        if (current()) { projection.value = page; error.value = '' }
      } catch (cause) {
        if (current()) { projection.value = null; error.value = cause instanceof Error ? cause.message : 'Decision Desk could not be read. Retry.' }
      } finally { if (current()) { loading.value = false; flight = undefined; controller = undefined } }
    })()
    return flight
  }
  watch(owner, () => { invalidate(); void refresh() }, { immediate: true, flush: 'sync' })
  const stopAccess = onAccessChange(change => { if (change === 'reset') { invalidate(); void refresh() } })
  onScopeDispose(() => { stopAccess(); invalidate() })
  return { projection, error, loading, refresh, invalidate, count: computed(() => projection.value?.counts.open ?? null) }
})
