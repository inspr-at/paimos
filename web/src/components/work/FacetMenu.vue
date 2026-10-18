<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { heldOptions, type FacetOption, type Dimension } from '../../lib/ticketList'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from './FloatingPanel.vue'
import FacetOptions from './FacetOptions.vue'

const props = defineProps<{ anchor: HTMLElement | null; dimension: Dimension; title: string; options: FacetOption[]; selected: string[]; loading?: boolean }>()
const emit = defineEmits<{ toggle: [value: string]; exclude: [value: string]; clear: []; close: [restoreFocus: boolean] }>()
const term = ref('')
// The rows appear once their names and counts have arrived, then hold their
// places while the menu is open: a later answer never moves a row or inserts
// the search field above them (AEON-974).
const held = ref<FacetOption[] | null>(null)
const searchable = ref(false)
watch(() => props.loading ? null : props.options, live => {
  if (!live) return
  if (!held.value) searchable.value = live.length > 8
  held.value = heldOptions(held.value ?? [], live)
}, { immediate: true })
// Rows that arrive after the menu opened take the focus it gives on opening.
const head = ref<HTMLElement>()
watch(held, (rows, before) => {
  if (before || !rows) return
  void nextTick(() => {
    const panel = head.value?.closest<HTMLElement>('.floating')
    if (!panel || (panel.contains(document.activeElement) && document.activeElement !== panel)) return
    const first = panel.querySelector<HTMLElement>('[data-autofocus]') ?? panel.querySelector<HTMLElement>('input, button:not(:disabled)')
    first?.focus({ preventScroll: true })
  })
})
const shown = computed(() => {
  const needle = term.value.trim().toLowerCase()
  const rows = held.value ?? []
  return needle ? rows.filter(option => `${option.label} ${option.hint ?? ''}`.toLowerCase().includes(needle)) : rows
})
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="264" :label="`Filter by ${title}`" @close="restore => emit('close', restore)">
    <div ref="head" class="facet-head">
      <p class="eyebrow">{{ title }}</p>
      <button type="button" class="clear" :style="{ visibility: selected.length ? 'visible' : 'hidden' }" :disabled="!selected.length" @click="emit('clear')">Clear</button>
    </div>
    <input v-if="searchable" v-model="term" class="field facet-search" :placeholder="`Find ${title.toLowerCase()}…`" :aria-label="`Find ${title.toLowerCase()}`" data-autofocus />
    <FacetOptions v-if="held" :dimension="dimension" :options="shown" :selected="selected" @toggle="value => emit('toggle', value)" @exclude="value => emit('exclude', value)" />
    <p v-if="!held" class="none" role="status">Loading…</p>
    <p v-else-if="!shown.length" class="none">Nothing matches.</p>
    <p class="keys" aria-hidden="true"><KeyCap k="Space" /> include <KeyCap k="minus" /> exclude</p>
  </FloatingPanel>
</template>

<style scoped>
.facet-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 26px; padding: 2px 6px 4px 10px; }
.clear { height: 24px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; }
.clear:hover { background: var(--row-selected); }
.clear:focus-visible { box-shadow: var(--focus-ring); }
.facet-search { height: 30px; margin: 0 0 6px; font-size: 13px; }
.none { padding: 8px 10px; font-size: 13px; color: var(--ink-3); }
.keys { display: flex; align-items: center; gap: 5px; margin: 6px 4px 0; padding: 7px 6px 1px; border-top: 1px solid var(--line); font-size: 11.5px; color: var(--ink-3); }
.keys :deep(.keycap):not(:first-child) { margin-left: 6px; }
@media (hover: none) { .keys { display: none; } }
</style>
