import { computed, onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue'
import { ApiError } from '@/api/client'
import { loadPaimos6SessionZoom, type Paimos6SessionZoomTotals } from '@/v6/sessionHomeZoom'
import { loadOrchestration } from '@/services/orchestration'
import type { OrchestrationSnapshotV1 } from '@/services/orchestrationTypes'
import { fetchAgentModeSnapshot, type AgentModeSnapshot } from '@/services/agentMode'

export function useHabitat(options: {
  authority: Readonly<Ref<string>>
  principal: Readonly<Ref<number | null>>
  project: Readonly<Ref<number | null>>
  zoom: Readonly<Ref<string>>
}) {
  const snapshot = shallowRef<OrchestrationSnapshotV1 | null>(null)
  const deliveries = shallowRef<AgentModeSnapshot | null>(null)
  const messageAttention = shallowRef<
    { projectId: number; totals: Paimos6SessionZoomTotals | null }[]
  >([])
  const messageState = ref<'loading' | 'ready'>('loading')
  const state = ref<'loading' | 'ready' | 'unavailable' | 'unauthorized'>('loading')
  const deliveryState = ref<'loading' | 'ready' | 'unavailable'>('loading')
  const refreshing = ref(false)
  const selectedId = ref<string | null>(null)
  const now = ref(Date.now())
  const receivedAt = ref(0)
  const stale = computed(() => receivedAt.value === 0 || now.value - receivedAt.value > 45_000)
  const selectedWorker = computed(
    () =>
      snapshot.value?.fleet.workers.find((w) => w.harness_session_id === selectedId.value) ?? null,
  )
  let generation = 0
  let controller: AbortController | null = null
  let active = true

  async function refresh(clear = false) {
    const version = ++generation
    controller?.abort()
    controller = new AbortController()
    const signal = controller.signal
    const authority = options.authority.value
    if (clear) {
      snapshot.value = null
      deliveries.value = null
      selectedId.value = null
      messageAttention.value = []
      messageState.value = 'loading'
      receivedAt.value = 0
      state.value = 'loading'
      deliveryState.value = 'loading'
    }
    if (options.principal.value === null) {
      refreshing.value = false
      state.value = 'unauthorized'
      return
    }
    refreshing.value = true
    const projectId = options.project.value ?? undefined
    const current = () =>
      active && !signal.aborted && version === generation && authority === options.authority.value
    const projectionRequest = { projectId, zoom: options.zoom.value, signal }
    await Promise.all([
      loadOrchestration(projectionRequest)
        .then(async (result) => {
          if (!current()) return
          snapshot.value = result
          receivedAt.value = Date.now()
          now.value = Date.now()
          state.value = 'ready'
          if (
            selectedId.value &&
            !result.fleet.workers.some((w) => w.harness_session_id === selectedId.value)
          )
            selectedId.value = null
          const rows = result.project_coordination.slice(0, 10)
          const counts = await Promise.allSettled(
            rows.map((row) => loadPaimos6SessionZoom(row.project.id, '1', null, signal)),
          )
          if (!current()) return
          messageAttention.value = counts.map((entry, index) => ({
            projectId: rows[index].project.id,
            totals: entry.status === 'fulfilled' ? entry.value.totals : null,
          }))
          messageState.value = 'ready'
        })
        .catch((error) => {
          if (!current()) return
          snapshot.value = null
          messageAttention.value = []
          messageState.value = 'ready'
          selectedId.value = null
          receivedAt.value = 0
          state.value =
            error instanceof ApiError && [401, 403, 404].includes(error.status)
              ? 'unauthorized'
              : 'unavailable'
        }),
      fetchAgentModeSnapshot({ projectId }, { signal })
        .then((result) => {
          if (!current()) return
          deliveries.value = result
          deliveryState.value = 'ready'
        })
        .catch(() => {
          if (!current()) return
          deliveries.value = null
          deliveryState.value = 'unavailable'
        }),
    ])
    if (current()) refreshing.value = false
  }
  watch(
    [options.authority, options.principal, options.project, options.zoom],
    () => {
      void refresh(true)
    },
    { immediate: true, flush: 'sync' },
  )
  const clock = setInterval(() => {
    now.value = Date.now()
  }, 5000)
  const poll = setInterval(() => {
    if (!document.hidden && !refreshing.value) void refresh()
  }, 15_000)
  function resume() {
    if (!document.hidden) void refresh()
  }
  document.addEventListener('visibilitychange', resume)
  onScopeDispose(() => {
    active = false
    generation++
    controller?.abort()
    clearInterval(clock)
    clearInterval(poll)
    document.removeEventListener('visibilitychange', resume)
  })
  return {
    snapshot,
    messageAttention,
    messageState,
    deliveries,
    state,
    deliveryState,
    refreshing,
    selectedId,
    selectedWorker,
    stale,
    now,
    refresh,
  }
}
