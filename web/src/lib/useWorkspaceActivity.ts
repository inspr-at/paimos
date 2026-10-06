// SPDX-License-Identifier: AGPL-3.0-only
import { onScopeDispose, ref, watch, type Ref } from 'vue'
import { onAccessChange } from './authz'
import { useIdentityScope } from './useIdentityScope'
import { toast } from './toast'
import { ActivityRequestError, getWorkspaceActivity, undoActivity, type WorkspaceActivityFilters, type WorkspaceActivityItem } from './workspaceActivity'

// Every read/Undo belongs to the person and the exact filter generation on
// screen. A response never crosses a change of person, page or row revision.
export function useWorkspaceActivity(filters: Ref<WorkspaceActivityFilters>) {
  const scope = useIdentityScope()
  const lane = scope.lane()
  const items = ref<WorkspaceActivityItem[]>([])
  const cursor = ref<string | null>(null)
  const loading = ref(false)
  const loadingOlder = ref(false)
  const error = ref('')
  const pending = ref(new Set<number>())
  const rowErrors = ref<Record<number, string>>({})

  function reset() {
    scope.reset()
    items.value = []; cursor.value = null; error.value = ''; rowErrors.value = {}; pending.value = new Set()
    loading.value = false; loadingOlder.value = false
  }
  function load() {
    reset()
    if (!scope.owner.value) return Promise.resolve()
    loading.value = true
    const captured = { ...filters.value }
    return lane.run(({ after, signal }) => after(getWorkspaceActivity(captured, null, signal), page => {
      items.value = page.items; cursor.value = page.next_cursor
    }), { failed: e => { error.value = e instanceof Error ? e.message : 'Activity could not be loaded.' }, settled: () => { loading.value = false } })
  }
  function loadOlder() {
    if (loading.value || loadingOlder.value || !cursor.value || !scope.owner.value) return Promise.resolve()
    const captured = { ...filters.value }, next = cursor.value
    loadingOlder.value = true; error.value = ''
    return lane.run(({ after, signal }) => after(getWorkspaceActivity(captured, next, signal), page => {
      const seen = new Set(items.value.map(item => item.event_id))
      items.value = [...items.value, ...page.items.filter(item => !seen.has(item.event_id))]
      cursor.value = page.next_cursor
    }), { failed: e => { error.value = e instanceof Error ? e.message : 'Older activity could not be loaded.' }, settled: () => { loadingOlder.value = false } })
  }
  function undo(item: WorkspaceActivityItem) {
    // Capture both the durable event and its original resource revision before
    // starting. The server event pins the write's revision; no newest-event lookup.
    const captured = { event_id: item.event_id, node_id: item.node_id, revision: item.revision }
    const current = () => items.value.find(row => row.event_id === captured.event_id && row.node_id === captured.node_id && row.revision === captured.revision)
    if (!item.undoable || item.undone || !captured.node_id || !captured.revision || pending.value.has(captured.event_id)) return Promise.resolve()
    pending.value = new Set([...pending.value, captured.event_id]); delete rowErrors.value[captured.event_id]
    return scope.run(({ after, signal }) => after(undoActivity(captured, signal), () => {
      const row = current()
      if (!row) return
      row.undone = true; row.undoable = false
      for (const other of items.value) if (other.node_id === captured.node_id && other.event_id !== captured.event_id && other.automatic) { other.changed_since = true; other.undoable = false }
    }), {
      failed: e => {
        const row = current()
        if (!row) return
        rowErrors.value[captured.event_id] = e instanceof Error ? e.message : 'Undo failed.'
        row.undoable = false
        if (e instanceof ActivityRequestError && e.status === 409) row.changed_since = true
        toast(`Undo for ${row.key}: ${rowErrors.value[captured.event_id]}`, { tone: 'error' })
      },
      settled: () => { const next = new Set(pending.value); next.delete(captured.event_id); pending.value = next },
    })
  }
  watch([filters, scope.owner], () => { void load() }, { immediate: true, deep: true, flush: 'sync' })
  const stopAccess = onAccessChange(() => { void load() })
  onScopeDispose(stopAccess)
  return { items, cursor, loading, loadingOlder, error, pending, rowErrors, load, loadOlder, undo }
}
