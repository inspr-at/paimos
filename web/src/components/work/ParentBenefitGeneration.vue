<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api, type ParentBenefitGeneration } from '../../lib/api'
import { useSession } from '../../stores/session'

const props = defineProps<{ nodeId: string; editable: boolean }>()
const emit = defineEmits<{ edit: []; generated: [] }>()
const session = useSession()
const status = ref<ParentBenefitGeneration | null>(null)
const problem = ref('')
const busy = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | undefined
let epoch = 0
const label = computed(() => {
  if (!status.value) return 'Loading parent benefit status…'
  switch (status.value.status) {
    case 'queued': return 'Parent is Done. Benefit generation is queued.'
    case 'running': return 'Parent is Done. Benefits are being generated from its leaves.'
    case 'failed': return status.value.error || 'Benefit generation failed. Retry when ready.'
    case 'generated': return 'From leaf benefits · editable by people.'
    case 'edited': return 'Edited text. Automatic generation keeps this text.'
    case 'cancelled': return 'Generation stopped because the parent changed.'
    default: return 'Parent benefits are editable. Done does not wait for generation.'
  }
})
function stop() { clearTimeout(timer); controller?.abort(); epoch++ }
async function load(id: string, version: number) {
  if (version !== epoch || props.nodeId !== id) return
  controller?.abort(); controller = new AbortController()
  try {
    const response = await api(`/nodes/${encodeURIComponent(id)}/benefit-generation`, { signal: controller.signal })
    if (!response.ok) throw new Error('read failed')
    const next = await response.json() as ParentBenefitGeneration
    if (version !== epoch || props.nodeId !== id) return
    const previous = status.value?.status
    status.value = next; problem.value = ''
    if (next.status === 'generated' && previous !== 'generated') emit('generated')
  } catch {
    if (version === epoch && props.nodeId === id) problem.value = 'Benefit status could not be loaded.'
  }
  if (version === epoch && props.nodeId === id) {
    // Poll while mounted, including after a failed read or a later human edit.
    timer = setTimeout(() => void load(id, version), 5000)
  }
}
watch(() => [props.nodeId, session.identity?.principal.id, session.identity?.tenant.id], () => {
  stop(); status.value = null; busy.value = false; problem.value = ''
  void load(props.nodeId, epoch)
}, { immediate: true })
onBeforeUnmount(stop)
async function retry() {
  const current = status.value
  if (!current || current.status !== 'failed' || busy.value || !props.editable) return
  stop()
  const id = props.nodeId, version = epoch
  busy.value = true; problem.value = ''
  try {
    const response = await api(`/nodes/${encodeURIComponent(id)}/benefit-generation/retry`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ expected_generation: current.generation, expected_revision: current.revision }),
    })
    if (version !== epoch || props.nodeId !== id) return
    if (!response.ok) throw new Error('retry failed')
    const next = await response.json() as ParentBenefitGeneration
    if (version === epoch && props.nodeId === id) status.value = next
  } catch {
    if (version === epoch && props.nodeId === id) problem.value = 'Generation could not be queued. Reload the parent and try again.'
  } finally {
    if (version === epoch && props.nodeId === id) {
      busy.value = false
      timer = setTimeout(() => void load(id, version), 5000)
    }
  }
}
</script>

<template>
  <div v-if="!status || status.is_parent" class="parent-generation">
    <div v-if="editable" class="generation-actions" role="group" aria-label="Parent benefit actions">
      <button type="button" class="btn sm" :disabled="busy || status?.status !== 'failed'" @click="retry">Retry generation</button>
      <button type="button" class="btn sm" @click="emit('edit')">Edit benefit</button>
    </div>
    <p class="generation-status" role="status"><strong v-if="status?.generated">Generated · </strong>{{ label }}</p>
    <p v-if="problem" class="generation-error" role="alert">{{ problem }}</p>
  </div>
</template>

<style scoped>
.parent-generation { display: grid; gap: 8px; min-width: 0; }
.generation-actions { display: flex; flex-wrap: wrap; gap: 8px; align-items: start; }
.generation-actions button { min-height: 32px; white-space: normal; }
.generation-status, .generation-error { margin: 0; font-size: 12.5px; line-height: 1.5; overflow-wrap: anywhere; color: var(--ink-3); }
.generation-status strong { color: var(--ink-2); font-weight: 600; }
.generation-error { color: var(--error-ink, var(--ink)); }
@media (pointer: coarse) { .generation-actions button { min-height: 44px; } }
</style>
