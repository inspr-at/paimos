// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { api } from '../lib/api'
import { workLevel, type WorkVocabulary } from '../lib/workVocabulary'
import { scopeOwner } from '../lib/identityScope'
import { useSession } from './session'

export const useWorkVocabulary = defineStore('workVocabulary', () => {
  const session = useSession()
  const owner = computed(() => scopeOwner(session.identity))
  const value = ref<WorkVocabulary>({ revision: 0, leaf: { name: '', icon: '' }, levels: [] })
  const loaded = ref(false), error = ref('')
  const leaf = computed(() => workLevel(value.value, true, 1))
  let generation = 0, pending: Promise<void> | undefined, controller: AbortController | undefined
  watch(owner, () => {
    generation++; controller?.abort(); pending = undefined
    loaded.value = false; error.value = ''
    value.value = { revision: 0, leaf: { name: '', icon: '' }, levels: [] }
  }, { flush: 'sync' })
  onScopeDispose(() => { generation++; controller?.abort() })

  function accept(next: WorkVocabulary) {
    if (!owner.value) return
    generation++; controller?.abort(); pending = undefined
    value.value = { ...next, leaf: { ...next.leaf }, levels: next.levels.map(level => ({ ...level })) }
    loaded.value = true; error.value = ''
  }
  function load(): Promise<void> {
    if (!owner.value || loaded.value) return Promise.resolve()
    if (pending) return pending
    const run = generation, person = owner.value
    controller = new AbortController()
    // Bound the complete read, including retries, before rendering named controls.
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10_000)])
    pending = (async () => {
      try {
        const response = await api('/settings/work-vocabulary', { signal })
        if (!response.ok) throw new Error('Workspace names could not be read. Try again before creating.')
        const next = await response.json() as WorkVocabulary
        if (run !== generation || person !== owner.value) return
        value.value = next; loaded.value = true; error.value = ''
      } catch (cause) {
        if (run === generation && person === owner.value) error.value = cause instanceof Error ? cause.message : 'Workspace names could not be read.'
      } finally { if (run === generation) pending = undefined }
    })()
    return pending
  }
  return { value, leaf, loaded, error, load, accept }
})
