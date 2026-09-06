<script setup lang="ts">
import { onScopeDispose, ref, shallowRef, watch } from 'vue'
import { api } from '@/api/client'
import { epoch, object, positive, invalid } from './habitatBoundary'
const props = defineProps<{
  projectId: number
  authority: string
  modelValue: number | null
  disabled: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [value: number | null] }>()
const query = ref('')
const tickets = shallowRef<{ id: number; key: string; title: string }[]>([])
const state = ref<'loading' | 'ready' | 'error'>('loading')
let generation = 0
let controller: AbortController | null = null
async function load() {
  const version = ++generation
  controller?.abort()
  controller = new AbortController()
  const signal = controller.signal
  state.value = 'loading'
  try {
    const raw = object(
      epoch(
        await api.getWithMeta<unknown>(
          `/projects/${props.projectId}/issues?fields=list&envelope=1&limit=50&offset=0${query.value.trim().length >= 2 ? `&q=${encodeURIComponent(query.value.trim())}` : ''}`,
          { signal },
        ),
      ),
    )
    if (!Array.isArray(raw.issues) || raw.issues.length > 50) invalid()
    const rows = (raw.issues as unknown[]).map((value) => {
      const row = object(value)
      if (
        !positive(row.id) ||
        row.project_id !== props.projectId ||
        typeof row.issue_key !== 'string' ||
        row.issue_key.length > 64 ||
        typeof row.title !== 'string' ||
        row.title.length > 2048
      )
        invalid()
      return { id: Number(row.id), key: String(row.issue_key), title: String(row.title) }
    })
    if (version !== generation || signal.aborted) return
    tickets.value = rows
    state.value = 'ready'
  } catch {
    if (version !== generation || signal.aborted) return
    tickets.value = []
    state.value = 'error'
  }
}
watch(
  () => [props.authority, props.projectId],
  () => {
    tickets.value = []
    query.value = ''
    void load()
  },
  { immediate: true, flush: 'sync' },
)
onScopeDispose(() => {
  generation++
  controller?.abort()
})
</script>
<template>
  <div class="habitat-stack">
    <label
      >Find a ticket<input
        v-model="query"
        type="search"
        maxlength="100"
        :disabled="disabled"
        placeholder="Ticket key or title"
        @keydown.enter.prevent="load"
    /></label>
    <button type="button" :disabled="disabled || state === 'loading'" @click="load">
      Search tickets
    </button>
    <label
      >Ticket<select
        aria-label="Ticket"
        :value="modelValue ?? ''"
        :disabled="disabled || state !== 'ready'"
        @change="
          emit(
            'update:modelValue',
            ($event.target as HTMLSelectElement).value
              ? Number(($event.target as HTMLSelectElement).value)
              : null,
          )
        "
      >
        <option value="">No ticket binding</option>
        <option
          v-if="modelValue && !tickets.some((row) => row.id === modelValue)"
          :value="modelValue"
          disabled
        >
          Current ticket #{{ modelValue }} · outside these results
        </option>
        <option v-for="ticket in tickets" :key="ticket.id" :value="ticket.id">
          {{ ticket.key }} · {{ ticket.title }}
        </option>
      </select></label
    >
    <small v-if="state === 'loading'" role="status">Loading tickets…</small>
    <small v-else-if="state === 'error'" role="status"
      >Tickets unavailable. Retry the search; existing choices are checked again when you
      submit.</small
    >
    <small v-else>Up to 50 matching tickets. Refine the search to find another assignment.</small>
  </div>
</template>
