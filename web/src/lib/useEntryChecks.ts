// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, ref, watch } from 'vue'
import type { ListItem } from './api'
import { readEntryChecks, type EntryChecks } from './entryChecks'
import { useSession } from '../stores/session'

export function useEntryChecks(row: () => Pick<ListItem, 'id' | 'updated_at'> | null | undefined) {
  const session = useSession(), data = ref<EntryChecks | null>(null), error = ref(''), loading = ref(false)
  let generation = 0, controller: AbortController | undefined
  async function refresh() {
    controller?.abort(); controller = new AbortController()
    const target = row(), actor = session.identity?.principal.id, tenant = session.identity?.tenant.id, requestId = ++generation
    if (!target) { data.value = null; return }
    const id = target.id, revision = target.updated_at
    loading.value = true; error.value = ''
    try {
      const result = await readEntryChecks(id, controller.signal)
      if (requestId === generation && row()?.id === id && row()?.updated_at === revision && session.identity?.principal.id === actor && session.identity?.tenant.id === tenant && result.node_id === id && (!revision || result.revision === revision)) data.value = result
    } catch (e) { if (requestId === generation && !(e instanceof DOMException && e.name === 'AbortError')) error.value = e instanceof Error ? e.message : 'Checks could not be read' }
    finally { if (requestId === generation) loading.value = false }
  }
  watch(() => [row()?.id, row()?.updated_at, session.identity?.principal.id, session.identity?.tenant.id], (value, old) => {
    if (value[0] !== old?.[0] || value[2] !== old?.[2] || value[3] !== old?.[3]) data.value = null
    void refresh()
  }, { immediate: true })
  onScopeDispose(() => { generation++; controller?.abort() })
  return { data, error, loading, refresh, defined: computed(() => !!data.value?.definition) }
}
