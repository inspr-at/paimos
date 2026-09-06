<script setup lang="ts">
import { ref, shallowRef, watch, onScopeDispose } from 'vue'
import { ApiError } from '@/api/client'
import { loadAssignmentHistory, type AssignmentEvent } from './habitatControls'
import { humanize, type HabitatWorker } from './habitatModel'
const props = defineProps<{ worker: HabitatWorker; workers: HabitatWorker[]; authority: string }>()
const open = ref(false)
const events = shallowRef<AssignmentEvent[]>([])
const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
const next = ref<number | null>(null)
let generation = 0
let controller: AbortController | null = null
function clear() {
  generation++
  controller?.abort()
  events.value = []
  state.value = 'idle'
  next.value = null
}
async function load(more = false) {
  if (state.value === 'loading') return
  const version = ++generation
  controller?.abort()
  controller = new AbortController()
  const signal = controller.signal
  const after = more ? (next.value ?? 0) : 0
  state.value = 'loading'
  try {
    const result = await loadAssignmentHistory(
      props.worker.project.id,
      props.worker.harness_session_id,
      after,
      signal,
    )
    if (version !== generation || signal.aborted) return
    events.value = more ? [...events.value, ...result.events] : result.events
    next.value = result.next
    state.value = 'ready'
  } catch (error) {
    if (version !== generation || signal.aborted) return
    // Never retain assignment evidence after access loss; disconnected history is
    // labelled unavailable rather than silently presenting an old page as current.
    events.value = []
    next.value = null
    state.value = 'error'
    if (error instanceof ApiError && [401, 403].includes(error.status)) open.value = false
  }
}
watch(
  () => [props.authority, props.worker.project.id, props.worker.harness_session_id],
  () => {
    clear()
    if (open.value) void load()
  },
  { flush: 'sync' },
)
watch(
  () => props.worker.revision,
  () => {
    if (open.value) void load()
  },
)
function toggle(event: Event) {
  open.value = (event.target as HTMLDetailsElement).open
  if (open.value && state.value === 'idle') void load()
}
function parent(id: string | null) {
  if (id === null) return 'No parent'
  return (
    props.workers.find(
      (row) => row.project.id === props.worker.project.id && row.harness_session_id === id,
    )?.agent.name ?? `Worker ${id.slice(0, 8)} (outside current view)`
  )
}
function ticket(id: number | null) {
  if (id === null) return 'No ticket'
  const known = props.workers.find(
    (row) => row.project.id === props.worker.project.id && row.ticket?.id === id,
  )?.ticket
  return known?.key ?? `Ticket #${id}`
}
onScopeDispose(clear)
</script>
<template>
  <details :open="open" @toggle="toggle" data-testid="habitat-assignment-history">
    <summary>Assignment history</summary>
    <p v-if="state === 'loading'" role="status">Loading assignment changes…</p>
    <p v-if="state === 'error'" role="status">
      Assignment history is unavailable. Retry when access and connection are restored.
    </p>
    <p v-if="state === 'ready' && !events.length">
      No assignment changes were returned for this worker.
    </p>
    <ol v-if="events.length" class="habitat-history">
      <li v-for="event in events" :key="event.revision">
        <strong>Revision {{ event.revision }}</strong> ·
        <time :datetime="event.created_at">{{ new Date(event.created_at).toLocaleString() }}</time>
        <dl class="habitat-facts">
          <dt>Ticket</dt>
          <dd>{{ ticket(event.before_ticket_id) }} → {{ ticket(event.after_ticket_id) }}</dd>
          <dt>Parent</dt>
          <dd>
            {{ parent(event.before_parent_harness_session_id) }} →
            {{ parent(event.after_parent_harness_session_id) }}
          </dd>
          <dt>Work shape</dt>
          <dd>
            {{ humanize(event.before_work_shape ?? 'unknown') }} →
            {{ humanize(event.after_work_shape ?? 'unknown') }}
          </dd>
        </dl>
      </li>
    </ol>
    <div class="habitat-actions">
      <button type="button" :disabled="state === 'loading'" @click="load()">
        Refresh assignment history
      </button>
      <button
        v-if="next !== null && events.length < 200"
        type="button"
        :disabled="state === 'loading'"
        @click="load(true)"
      >
        Check for later changes
      </button>
    </div>
    <small v-if="events.length >= 200"
      >Showing up to 200 changes. Refresh starts at the earliest retained change.</small
    >
  </details>
</template>
