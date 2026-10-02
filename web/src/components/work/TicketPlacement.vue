<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { api, type ListItem } from '../../lib/api'
import { loadTicketKinds, placementPatch, placementSuggested, ticketKinds, type PlacementKind, type PlacementKindPage } from '../../lib/ticketPlacement'
import type { SaveResult } from '../../lib/useTicket'
import { useSession } from '../../stores/session'

const props = defineProps<{ item: ListItem; editable: boolean; save?: (fields: Record<string, unknown>) => Promise<SaveResult> }>()
const session = useSession()
const kinds = ref<PlacementKind[]>([]), loading = ref(true), busy = ref(false), error = ref('')
const area = computed(() => typeof props.item.fields.area === 'string' ? props.item.fields.area : '')
const complexity = computed(() => typeof props.item.fields.complexity === 'string' ? props.item.fields.complexity : '')
const options = computed(() => ticketKinds(kinds.value, props.item.project?.id ?? ''))
const areaLabel = computed(() => options.value.find(kind => kind.slug === area.value)?.label ?? area.value)
const canEdit = computed(() => props.editable && !!props.save)
const feedbackId = useId()
let epoch = 0, controller: AbortController | undefined
watch(() => [props.item.id, props.item.project?.id, session.identity?.tenant.id, session.identity?.principal.id], async () => {
  const started = ++epoch
  controller?.abort(); controller = new AbortController()
  const signal = controller.signal, project = props.item.project?.id
  kinds.value = []; error.value = ''; busy.value = false; loading.value = true
  try {
    const loaded = await loadTicketKinds(async cursor => {
      const query = new URLSearchParams({ limit: '100' })
      if (project) query.set('project_id', project)
      if (cursor) query.set('cursor', cursor)
      const response = await api(`/work-kinds?${query}`, { signal })
      if (!response.ok) throw new Error('Kind of work is unavailable.')
      return await response.json() as PlacementKindPage
    })
    if (started === epoch) kinds.value = loaded
  } catch (e) {
    if (started === epoch && !signal.aborted) error.value = e instanceof Error ? e.message : 'Kind of work is unavailable.'
  } finally { if (started === epoch) loading.value = false }
}, { immediate: true })
onBeforeUnmount(() => { epoch++; controller?.abort() })
async function save(field: 'area' | 'complexity', value: string) {
  if (!canEdit.value || busy.value) return
  const started = epoch
  busy.value = true; error.value = ''
  try {
    // The callback captures the shown ticket id and revision synchronously.
    const result = await props.save!(placementPatch(field, value))
    if (started !== epoch) return
    if (result !== 'ok') error.value = result === 'conflict' ? 'Changed elsewhere. Review and try again.' : 'Classification was not saved. Try again.'
  } catch { if (started === epoch) error.value = 'Classification was not saved. Try again.' }
  finally { if (started === epoch) busy.value = false }
}
function choose(field: 'area' | 'complexity', event: Event) { void save(field, (event.currentTarget as HTMLSelectElement).value) }
</script>

<template>
  <template v-if="['ticket', 'task'].includes(item.kind_slug)">
    <div class="prop placement-prop">
      <dt>Kind of work</dt>
      <dd>
        <select v-if="canEdit" :value="area" aria-label="Kind of work" :disabled="loading || busy || !kinds.length" :aria-describedby="feedbackId" @change="choose('area', $event)">
          <option value="">Unspecified</option>
          <option v-if="area && !options.some(kind => kind.slug === area)" :value="area" disabled>{{ areaLabel }} (unavailable)</option>
          <option v-for="kind in options" :key="kind.id" :value="kind.slug">{{ kind.label }}</option>
        </select>
        <span v-else class="placement-value">{{ areaLabel || 'Unspecified' }}</span>
        <button v-if="canEdit && area && placementSuggested(item.fields, 'area')" type="button" class="confirm" :disabled="busy || loading" @click="save('area', area)">Confirm</button>
      </dd>
    </div>
    <div class="prop placement-prop">
      <dt>Complexity</dt>
      <dd>
        <select v-if="canEdit" :value="complexity" aria-label="Complexity" :disabled="busy" :aria-describedby="feedbackId" @change="choose('complexity', $event)">
          <option value="">Unspecified</option><option value="S">S · Normal</option><option value="M">M · Normal</option><option value="L">L · Complex</option>
        </select>
        <span v-else class="placement-value">{{ complexity || 'Unspecified' }}</span>
        <button v-if="canEdit && complexity && placementSuggested(item.fields, 'complexity')" type="button" class="confirm" :disabled="busy" @click="save('complexity', complexity)">Confirm</button>
      </dd>
    </div>
    <div class="prop placement-feedback"><dt class="sr-only">Classification</dt><dd :id="feedbackId" aria-live="polite">{{ error || (busy ? 'Saving…' : placementSuggested(item.fields, 'area') || placementSuggested(item.fields, 'complexity') ? 'Suggested · confirm or choose a value' : '') }}</dd></div>
  </template>
</template>

<style scoped>
.placement-prop { flex: 0 0 235px; max-width: 100%; }
.placement-prop dd { display: flex; align-items: center; gap: 5px; }
select, .placement-value { box-sizing: border-box; width: 145px; min-width: 0; height: 30px; padding: 3px 6px; color: var(--ink); background: transparent; border: 1px solid var(--line); border-radius: 4px; font: inherit; font-size: 12px; }
select { flex: 0 1 145px; }
select:focus-visible, button:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.placement-value { border-color: transparent; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.confirm { flex: 0 0 65px; height: 30px; padding: 0; border: 0; background: transparent; color: var(--ink-2); font-size: 11px; cursor: pointer; }
.placement-prop dd::after { content: ''; width: 65px; flex: 0 0 65px; }
.placement-prop dd:has(.confirm)::after { display: none; }
.placement-feedback { flex-basis: 100%; height: 30px; color: var(--ink-3); font-size: 11px; }
.placement-feedback dd { overflow: auto; max-height: 30px; }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
@media (max-width: 720px) { select, .placement-value, .confirm { height: 34px; } }
</style>
