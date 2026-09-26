<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { DATE_FIELDS, DATE_PRESETS, DIMENSIONS, GROUPS, type DateField, type DateFilter, type Dimension, type FacetOption, type GroupBy, type ListFilters } from '../../lib/ticketList'
import { TICKET_GRAPH_FILTERS } from '../../lib/ticketGraphRenderer'
import { plural } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import FacetOptions from './FacetOptions.vue'

// Phones: every filter in one sheet, and grouping; the same state as the toolbar.
const props = withDefaults(defineProps<{ filters: ListFilters; options: (dimension: Dimension) => FacetOption[]; total: number | null; view?: 'list' | 'outline' | 'graph'; canSave?: boolean }>(), { view: 'list' })
const emit = defineEmits<{
  toggle: [dimension: Dimension, value: string]; exclude: [dimension: Dimension, value: string]; clearAll: []; showClosed: [value: boolean]; group: [value: GroupBy]
  date: [value: DateFilter | null]; opened: []; expandAll: []; collapseAll: []; saveView: [anchor: HTMLElement]
}>()
const dimensions = computed(() => DIMENSIONS.filter(d => props.view !== 'graph' || TICKET_GRAPH_FILTERS.includes(d.key)))
const dialog = ref<HTMLDialogElement>()
const doneButton = ref<HTMLButtonElement>()
const dateField = ref<DateField>('updated')
let opener: HTMLElement | null = null
async function open() {
  opener = document.activeElement as HTMLElement
  dateField.value = props.filters.date?.field ?? 'updated'
  dialog.value?.showModal()
  emit('opened')
  await nextTick()
  doneButton.value?.focus({ preventScroll: true })
}
function close() { dialog.value?.close(); opener?.focus({ preventScroll: true }) }
function backdrop(event: MouseEvent) { if (event.target === dialog.value) close() }
function datePreset(value: DateFilter['preset']) {
  const current = props.filters.date
  emit('date', current?.preset === value && current.field === dateField.value ? null : { field: dateField.value, preset: value, from: null, to: null })
}
function saveView() {
  const anchor = opener ?? document.body
  close()
  emit('saveView', anchor)
}
defineExpose({ open, close })
</script>

<template>
  <dialog ref="dialog" class="filter-sheet" aria-labelledby="filter-sheet-title" @cancel.prevent="close" @click="backdrop">
    <div class="sheet-card">
      <span class="grabber" aria-hidden="true" />
      <header>
        <h2 id="filter-sheet-title">Filters</h2>
        <div class="head-actions">
          <button v-if="canSave" type="button" class="btn sm ghost save" @click="saveView"><AppIcon name="bookmark" :size="14" />Save view</button>
          <button type="button" class="btn sm ghost" @click="emit('clearAll')">Clear all</button>
        </div>
      </header>
      <div class="sheet-scroll">
        <div class="sheet-row">
          <label class="switch">
            <input type="checkbox" :checked="!filters.showClosed" @change="emit('showClosed', !($event.target as HTMLInputElement).checked)" />
            <span>Hide closed tickets</span>
          </label>
        </div>
        <div v-if="view === 'outline'" class="sheet-row group-row">
          <p class="eyebrow">Outline</p>
          <div class="outline-actions">
            <button type="button" class="btn" @click="emit('expandAll'); close()"><AppIcon name="expand-all" :size="14" />Expand all</button>
            <button type="button" class="btn" @click="emit('collapseAll'); close()"><AppIcon name="collapse-all" :size="14" />Collapse all</button>
          </div>
        </div>
        <section v-else-if="view !== 'graph'" class="sheet-section">
          <p class="eyebrow">Group by</p>
          <div class="chip-grid" role="radiogroup" aria-label="Group by">
            <button v-for="option in GROUPS" :key="option.value" type="button" role="radio" class="choice" :aria-checked="filters.group === option.value" @click="emit('group', option.value)">{{ option.label }}</button>
          </div>
        </section>
        <section v-for="dimension in dimensions" :key="dimension.key" class="sheet-section">
          <p class="eyebrow">{{ dimension.title }}</p>
          <FacetOptions
            :dimension="dimension.key" :options="options(dimension.key)" :selected="filters[dimension.key]"
            @toggle="value => emit('toggle', dimension.key, value)" @exclude="value => emit('exclude', dimension.key, value)"
          />
        </section>
        <section v-if="view !== 'graph'" class="sheet-section">
          <p class="eyebrow">Date</p>
          <div class="chip-grid" role="radiogroup" aria-label="Which date">
            <button v-for="option in DATE_FIELDS" :key="option.value" type="button" role="radio" class="choice" :aria-checked="dateField === option.value" @click="dateField = option.value; filters.date && emit('date', { ...filters.date, field: option.value })">{{ option.label }}</button>
          </div>
          <div class="chip-grid presets" role="group" aria-label="Period">
            <button v-for="option in DATE_PRESETS" :key="option.value" type="button" class="choice" :aria-pressed="filters.date?.preset === option.value && filters.date.field === dateField" @click="datePreset(option.value)">{{ option.label }}</button>
          </div>
        </section>
      </div>
      <footer>
        <button ref="doneButton" type="button" class="btn primary done" @click="close">
          <AppIcon name="check" :size="14" />Show {{ total === null ? 'tickets' : plural(total, 'ticket') }}
        </button>
      </footer>
    </div>
  </dialog>
</template>

<style scoped>
.filter-sheet { width: 100vw; max-width: none; height: auto; max-height: 88dvh; margin: auto 0 0; padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.filter-sheet::backdrop { background: var(--scrim); }
.sheet-card { display: flex; flex-direction: column; max-height: 88dvh; border-radius: 20px 20px 0 0; border-top: 1px solid var(--glass-edge); background: var(--surface-raised); box-shadow: 0 -18px 40px -18px rgba(0, 0, 0, .35); }
.grabber { align-self: center; width: 40px; height: 4px; margin-top: 8px; border-radius: 999px; background: var(--line-2); }
header { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 8px 10px 8px 20px; }
h2 { font-size: 19px; }
.head-actions { display: flex; align-items: center; gap: 2px; }
header .btn { height: 44px; }
.save { gap: 6px; color: var(--teal-ink); font-weight: 600; }
.sheet-scroll { flex: 1; min-height: 0; overflow: auto; padding: 0 12px 12px; overscroll-behavior: contain; }
.sheet-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 52px; padding: 0 8px; border-bottom: 1px solid var(--line); }
.sheet-row .switch { min-height: 44px; font-size: 14.5px; color: var(--ink); }
.outline-actions { display: flex; gap: 8px; }
.outline-actions .btn { height: 40px; }
.sheet-section { padding: 14px 0 8px; border-bottom: 1px solid var(--line); }
.sheet-section .eyebrow { padding: 0 8px 8px; }
.sheet-section :deep(.facet-option) { min-height: 44px; }
.sheet-section :deep(.facet-main) { font-size: 15px; }
.chip-grid { display: flex; flex-wrap: wrap; gap: 6px; padding: 0 8px; }
.chip-grid.presets { margin-top: 8px; }
.choice { height: 36px; padding: 0 14px; border: 0; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font-size: 14px; }
.choice[aria-checked="true"], .choice[aria-pressed="true"] { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-weight: 600; }
.choice:focus-visible { box-shadow: var(--focus-ring); }
footer { padding: 12px 16px calc(12px + env(safe-area-inset-bottom)); border-top: 1px solid var(--line); }
.done { width: 100%; height: 46px; font-size: 15px; }
@media (prefers-reduced-motion: no-preference) {
  .filter-sheet[open] .sheet-card { animation: sheet-up .24s cubic-bezier(.2, .7, .2, 1); }
  @keyframes sheet-up { from { transform: translateY(40px); opacity: .6; } to { transform: none; opacity: 1; } }
}
</style>
