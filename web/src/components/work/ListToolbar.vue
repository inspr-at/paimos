<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { DIMENSION_BY_KEY, DIMENSIONS, activeDimensions, dateLabel, excluded, fieldLabel, included, type DateFilter, type Dimension, type FacetOption, type GroupBy, type ListFilters } from '../../lib/ticketList'
import { TICKET_GRAPH_FILTERS } from '../../lib/ticketGraphRenderer'
import { plural, type SortKey } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import ProjectTabs from './ProjectTabs.vue'
import { TICKET_VIEWS, KNOWLEDGE_VIEWS, type TicketView } from './projectNavigation'
import DateMenu from './DateMenu.vue'
import DisplayPanel from './DisplayPanel.vue'
import FacetMenu from './FacetMenu.vue'
import FilterMenu from './FilterMenu.vue'
import FloatingPanel from './FloatingPanel.vue'
import type { ColumnId } from '../../lib/columns'

const props = defineProps<{
  filters: ListFilters
  options: (dimension: Dimension) => FacetOption[]
  // The words for one value in a chip (an epic's title, a person's name).
  label: (dimension: Dimension, value: string) => string
  total: number | null
  loading: boolean
  density: 'comfortable' | 'compact'
  stuck: boolean
  view: TicketView | 'journey' | 'knowledge'
  knowledgeView?: 'entries' | 'graph'
  // The table's columns for the Display menu's picker.
  columns?: { order: ColumnId[]; visible: ColumnId[]; customised: boolean } | null
  facetLoading?: boolean
  headerGraph?: boolean
}>()
const emit = defineEmits<{
  search: [q: string]
  toggle: [dimension: Dimension, value: string]
  exclude: [dimension: Dimension, value: string]
  clear: [dimension: Dimension]
  clearAll: []
  showClosed: [value: boolean]
  group: [value: GroupBy]
  sort: [keys: SortKey[]]
  density: [value: 'comfortable' | 'compact']
  date: [value: DateFilter | null]
  openSheet: []
  needOptions: [dimension: Dimension]
  create: []
  view: [value: string]
  expandAll: []
  collapseAll: []
  expandGroups: []
  collapseGroups: []
  columns: [order: ColumnId[], visible: ColumnId[]]
  columnsReset: []
  headerGraph: [value: boolean]
}>()

const draft = ref(props.filters.q)
const root = ref<HTMLElement>()
// A narrow toolbar (docked panel, small window) gets a placeholder that fits.
const narrow = ref(false)
let resize: ResizeObserver | undefined
onMounted(() => {
  if (!root.value) return
  resize = new ResizeObserver(([entry]) => { narrow.value = entry.contentRect.width < 940 })
  resize.observe(root.value)
})
const input = ref<HTMLInputElement>()
const filterButton = ref<HTMLButtonElement>()
const open = ref<{ dimension: Dimension; anchor: HTMLElement } | null>(null)
const dateAnchor = ref<HTMLElement | null>(null)
const menuAnchor = ref<HTMLElement | null>(null)
const displayAnchor = ref<HTMLElement | null>(null)
let timer: ReturnType<typeof setTimeout> | undefined
watch(() => props.filters.q, value => { if (value !== draft.value.trim()) draft.value = value })
watch(draft, value => {
  clearTimeout(timer)
  timer = setTimeout(() => { if (value.trim() !== props.filters.q) emit('search', value.trim()) }, 220)
})
onBeforeUnmount(() => { clearTimeout(timer); resize?.disconnect() })

const graph = computed(() => props.view === 'graph')
const dimensions = computed(() => DIMENSIONS.filter(d => !graph.value || TICKET_GRAPH_FILTERS.includes(d.key)))
const primary = computed(() => dimensions.value.filter(d => d.primary))
const active = computed(() => activeDimensions(props.filters).filter(key => !graph.value || TICKET_GRAPH_FILTERS.includes(key)))
const secondaryActive = computed(() => active.value.filter(key => !DIMENSION_BY_KEY.get(key)!.primary).length + (!graph.value && props.filters.date ? 1 : 0))
const filterCount = computed(() => active.value.reduce((sum, key) => sum + props.filters[key].length, 0) + (props.filters.q ? 1 : 0) + (!graph.value && props.filters.date ? 1 : 0))
const chipCount = computed(() => active.value.length + (!graph.value && props.filters.date ? 1 : 0))
const groupWord = computed(() => props.filters.group === 'tag' ? 'label' : props.filters.group)
const displayLabel = computed(() => props.view === 'outline' || props.filters.group === 'none' ? 'Display' : `Grouped by ${groupWord.value}`)
watch(() => props.view, () => { open.value = null; menuAnchor.value = null; dateAnchor.value = null; displayAnchor.value = null })
const displayText = computed(() => props.view === 'outline' || props.filters.group === 'none' ? 'Display' : `By ${groupWord.value}`)

function title(dimension: Dimension) { return DIMENSION_BY_KEY.get(dimension)!.title }
function openMenu(dimension: Dimension, anchor: HTMLElement) {
  if (open.value?.dimension === dimension && open.value.anchor === anchor) { open.value = null; return }
  menuAnchor.value = null
  open.value = { dimension, anchor }
  emit('needOptions', dimension)
}
function closeMenu(restore: boolean) {
  const anchor = open.value?.anchor
  open.value = null
  if (restore) anchor?.focus()
}
function chooseFilter(choice: Dimension | 'date') {
  const anchor = menuAnchor.value ?? filterButton.value ?? null
  menuAnchor.value = null
  if (!anchor) return
  // The chosen filter opens where the Filter menu was, so the eye stays put.
  void nextTick(() => { if (choice === 'date') dateAnchor.value = anchor; else openMenu(choice, anchor) })
}
function chipText(dimension: Dimension) {
  const words = (values: string[]) => values.map(value => props.label(dimension, value))
  const plain = words(included(props.filters[dimension]))
  const not = words(excluded(props.filters[dimension]))
  const shorten = (parts: string[]) => parts.length > 2 ? `${parts.slice(0, 2).join(', ')} +${parts.length - 2}` : parts.join(', ')
  return [plain.length ? shorten(plain) : '', not.length ? `not ${shorten(not)}` : ''].filter(Boolean).join(' · ')
}
function chipKey(event: KeyboardEvent, clear: () => void) {
  if (event.key === 'Backspace' || event.key === 'Delete') {
    event.preventDefault()
    const chip = (event.currentTarget as HTMLElement).closest('.filter-chip')
    const next = (chip?.nextElementSibling ?? chip?.previousElementSibling)?.querySelector<HTMLElement>('button')
    clear()
    void nextTick(() => (next ?? input.value)?.focus())
  }
}
function closeDisplay(restore: boolean) {
  const anchor = displayAnchor.value
  displayAnchor.value = null
  if (restore) anchor?.focus()
}
function closeDate(restore: boolean) {
  const anchor = dateAnchor.value
  dateAnchor.value = null
  if (restore) anchor?.focus()
}
function focusSearch() { input.value?.focus(); input.value?.select() }
function openFilterMenu() { if (filterButton.value && filterButton.value.offsetParent) { open.value = null; menuAnchor.value = filterButton.value } else emit('openSheet') }
function clearSearch() { draft.value = ''; emit('search', ''); input.value?.focus() }
function searchKey(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    if (draft.value) clearSearch()
    else input.value?.blur()
  }
}
defineExpose({ focusSearch, openFilterMenu, input })
</script>

<template>
  <div ref="root" class="toolbar" :class="{ stuck, graph, knowledge: view === 'knowledge' }" role="toolbar" :aria-label="view === 'knowledge' ? 'Knowledge controls' : view === 'journey' ? 'Journey controls' : 'Ticket list controls'">
    <ProjectTabs v-if="view !== 'journey'" class="view-switch"
      :items="view === 'knowledge' ? KNOWLEDGE_VIEWS : TICKET_VIEWS"
      :selected="view === 'knowledge' ? knowledgeView ?? 'entries' : view"
      :tips="view !== 'knowledge'"
      :label="view === 'knowledge' ? 'Knowledge views' : 'Ticket views'" @select="value => emit('view', value)" />
    <template v-if="view !== 'knowledge' && view !== 'journey'">
    <label class="search-field list-search">
      <AppIcon name="search" :size="14" />
      <input ref="input" v-model="draft" class="field" type="search" :placeholder="narrow ? 'Search' : graph ? 'Search tickets' : 'Search this list'" aria-label="Search tickets in this project" aria-keyshortcuts="/" autocomplete="off" spellcheck="false" @keydown="searchKey" />
      <kbd v-if="!draft && !narrow" class="keycap slash" aria-hidden="true">/</kbd>
      <button v-if="draft" type="button" class="clear-q" aria-label="Clear search" @click="clearSearch"><AppIcon name="close" :size="12" /></button>
    </label>

    <div class="facets">
      <button
        v-for="dimension in primary" :key="dimension.key" type="button" class="btn sm facet-btn" :data-dim="dimension.key"
        :class="{ on: filters[dimension.key].length }" :aria-expanded="open?.dimension === dimension.key && open.anchor.classList.contains('facet-btn')" aria-haspopup="dialog" @click="openMenu(dimension.key, $event.currentTarget as HTMLElement)"
      >
        {{ dimension.title }}
        <span class="facet-end">
          <span v-if="filters[dimension.key].length" class="facet-count mono">{{ filters[dimension.key].length }}</span>
          <AppIcon v-else name="chevron" :size="12" class="facet-chevron" />
        </span>
      </button>
      <button
        ref="filterButton" type="button" class="btn sm facet-btn more-btn" :class="{ on: secondaryActive }" aria-haspopup="menu" :aria-expanded="!!menuAnchor"
        aria-label="Filter by more" aria-keyshortcuts="Shift+F" :data-tip="graph ? 'Status, priority, type · Shift F' : 'Labels, epic, cost unit, release, date · Shift F'" @click="menuAnchor = menuAnchor ? null : ($event.currentTarget as HTMLElement)"
      >
        <AppIcon name="filter" :size="13" /><span class="more-label">Filter</span>
        <span v-if="secondaryActive" class="facet-count mono">{{ secondaryActive }}</span>
      </button>
    </div>

    <div v-if="chipCount" class="chips" aria-label="Applied filters">
      <span v-for="dimension in active" :key="dimension" class="filter-chip" :class="{ negated: !included(filters[dimension]).length }">
        <button type="button" class="chip-body" :aria-label="`Edit ${title(dimension)} filter: ${chipText(dimension)}`" @click="openMenu(dimension, $event.currentTarget as HTMLElement)" @keydown="chipKey($event, () => emit('clear', dimension))"><span class="chip-dim">{{ title(dimension) }}</span><span class="chip-text">{{ chipText(dimension) }}</span></button>
        <button type="button" class="chip-x" :aria-label="`Remove ${title(dimension)} filter`" @click="emit('clear', dimension)"><AppIcon name="close" :size="11" /></button>
      </span>
      <span v-if="!graph && filters.date" class="filter-chip">
        <button type="button" class="chip-body" :aria-label="`Edit date filter: ${fieldLabel(filters.date.field)} ${dateLabel(filters.date)}`" @click="dateAnchor = $event.currentTarget as HTMLElement" @keydown="chipKey($event, () => emit('date', null))"><AppIcon name="calendar" :size="12" class="chip-icon" /><span class="chip-dim">{{ fieldLabel(filters.date.field) }}</span><span class="chip-text">{{ dateLabel(filters.date) }}</span></button>
        <button type="button" class="chip-x" aria-label="Remove date filter" @click="emit('date', null)"><AppIcon name="close" :size="11" /></button>
      </span>
      <button v-if="chipCount > 1" type="button" class="btn sm ghost clear-all" @click="emit('clearAll')">Clear all</button>
    </div>

    <span class="spacer" />

    <span class="count mono" role="status" aria-live="polite"><span v-if="total === null && loading" class="skeleton count-skeleton" aria-label="Counting tickets" /><template v-else-if="total !== null">{{ plural(total, 'ticket') }}</template></span>
    <label class="switch closed-switch">
      <input type="checkbox" :checked="!filters.showClosed" @change="emit('showClosed', !($event.target as HTMLInputElement).checked)" />
      <span>Hide closed</span>
    </label>
    <button
      type="button" class="btn sm closed-pill" :class="{ on: !filters.showClosed }" :aria-pressed="!filters.showClosed" aria-label="Hide closed tickets"
      :data-tip="filters.showClosed ? 'Closed tickets are shown\nClick to hide them' : 'Closed tickets are hidden\nClick to show them'" @click="emit('showClosed', !filters.showClosed)"
    ><AppIcon :name="filters.showClosed ? 'eye' : 'eye-off'" :size="14" />Closed</button>
    <button v-if="!graph" type="button" class="btn sm display-btn" :class="{ on: view === 'list' && filters.group !== 'none' }" aria-haspopup="dialog" :aria-expanded="!!displayAnchor" :aria-label="`Display: ${displayLabel}`" data-tip="Grouping, sort, row height and columns" @click="displayAnchor = displayAnchor ? null : ($event.currentTarget as HTMLElement)">
      <AppIcon name="layers" :size="13" /><span class="display-label">{{ displayText }}</span><AppIcon name="chevron" :size="12" class="facet-chevron" />
    </button>

    <button v-if="!graph" type="button" class="btn primary new-btn" aria-label="New ticket" aria-keyshortcuts="n" data-tip="New ticket · n" @click="emit('create')"><AppIcon name="plus" :size="14" /><span class="new-label">New</span></button>
    <button type="button" class="btn filters-btn" :class="{ on: filterCount }" aria-label="Filters" @click="emit('openSheet')">
      <AppIcon name="sliders" :size="14" /><span class="filters-label">Filters</span><span v-if="filterCount" class="facet-count mono">{{ filterCount }}</span>
    </button>

    </template>
    <!-- The Knowledge tab teleports its own controls here (KnowledgeTab.vue). -->
    <div v-else-if="view === 'knowledge'" id="knowledge-controls" class="knowledge-controls" />
    <span v-else class="spacer" />
    <slot name="journey" />

    <FacetMenu
      v-if="open" :anchor="open.anchor" :dimension="open.dimension" :title="title(open.dimension)" :options="options(open.dimension)" :selected="filters[open.dimension]" :loading="facetLoading"
      @toggle="value => emit('toggle', open!.dimension, value)" @exclude="value => emit('exclude', open!.dimension, value)" @clear="emit('clear', open!.dimension)" @close="closeMenu"
    />
    <FilterMenu v-if="menuAnchor" :anchor="menuAnchor" :filters="filters" :dimensions="graph ? TICKET_GRAPH_FILTERS : undefined" :show-date="!graph" @choose="chooseFilter" @close="restore => { const a = menuAnchor; menuAnchor = null; if (restore) a?.focus() }" />
    <DateMenu v-if="dateAnchor" :anchor="dateAnchor" :value="filters.date" @change="value => emit('date', value)" @close="closeDate" />
    <FloatingPanel v-if="displayAnchor" :anchor="displayAnchor" :width="320" :tallest="760" align="end" label="Display options" @close="closeDisplay">
      <DisplayPanel
        :filters="filters" :view="view === 'outline' ? 'outline' : 'list'" :density="density" :columns="columns" :grouped="view === 'list' && filters.group !== 'none'"
        :header-graph="headerGraph"
        @group="value => emit('group', value)" @sort="keys => emit('sort', keys)" @density="value => emit('density', value)"
        @columns="(order, visible) => emit('columns', order, visible)" @columns-reset="emit('columnsReset')"
        @header-graph="value => emit('headerGraph', value)"
        @expand-all="emit('expandAll'); closeDisplay(false)" @collapse-all="emit('collapseAll'); closeDisplay(false)"
        @expand-groups="emit('expandGroups')" @collapse-groups="emit('collapseGroups')"
      />
    </FloatingPanel>
  </div>
</template>

<style scoped>
.toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 10px; min-height: 52px; padding: 9px 0; }
.list-search { width: 240px; min-width: 0; flex-shrink: 0; }
.list-search .field { height: 32px; padding-right: 30px; font-size: 13.5px; }
.list-search .field::-webkit-search-cancel-button { display: none; }
.slash { position: absolute; right: 8px; pointer-events: none; }
@media (hover: none) { .slash { display: none; } }
.clear-q { position: absolute; right: 5px; display: grid; place-items: center; width: 22px; height: 22px; padding: 0; border: 0; border-radius: 50%; background: var(--chip-bg); color: var(--ink-2); }
.clear-q:hover { color: var(--ink); background: var(--row-selected); }
.clear-q:focus-visible { box-shadow: var(--focus-ring); }
.facets { display: flex; gap: 6px; }
.facet-btn { gap: 6px; padding: 0 9px 0 12px; font-weight: 600; color: var(--ink-2); }
.facet-btn:hover, .facet-btn[aria-expanded="true"] { color: var(--ink); }
.facet-btn.on { color: var(--teal-ink); }
.more-btn { padding: 0 11px 0 10px; }
/* Chevron and count badge share one slot so buttons never change width. */
.facet-end { display: inline-grid; place-items: center; width: 18px; }
.facet-chevron { color: var(--ink-3); }
.facet-count { display: inline-grid; place-items: center; min-width: 17px; height: 17px; padding: 0 5px; border-radius: 999px; background: linear-gradient(180deg, #1a8683, #0e6f6c); color: #fff; font-size: 10.5px; font-weight: 700; }
/* Applied filters take their own line under the controls, so the controls never move. */
.chips { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; order: 20; flex: 1 0 100%; min-width: 0; }
.filter-chip { display: inline-flex; align-items: center; height: 28px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-size: 12.5px; max-width: 340px; }
.chip-body { display: inline-flex; align-items: center; gap: 6px; min-width: 0; height: 100%; padding: 0 4px 0 11px; border: 0; border-radius: 999px 0 0 999px; background: transparent; color: inherit; white-space: nowrap; overflow: hidden; font-weight: 600; }
.chip-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.chip-body:hover { background: rgba(14, 111, 108, .06); }
.chip-dim { flex-shrink: 0; font: 500 10px/1 var(--mono); letter-spacing: .1em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.chip-icon { flex-shrink: 0; color: var(--ink-3); }
.chip-x { display: grid; place-items: center; flex-shrink: 0; width: 24px; height: 24px; margin-right: 2px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--teal-ink); }
.chip-x:hover { background: rgba(14, 111, 108, .12); }
.chip-x:active { background: rgba(14, 111, 108, .2); }
.chip-body:focus-visible, .chip-x:focus-visible { box-shadow: var(--focus-ring); }
.clear-all { padding: 0 8px; }
.spacer { flex: 1; }
/* The count keeps its width while numbers change, so the controls beside it never shift. */
.count { display: inline-block; min-width: 13ch; text-align: right; font-size: 12px; color: var(--ink-2); white-space: nowrap; }
.count-skeleton { display: inline-block; width: 64px; height: 8px; vertical-align: middle; }
.closed-switch { font-size: 12.5px; }
.display-btn { gap: 6px; color: var(--ink-2); }
.display-btn.on { color: var(--teal-ink); }
.filters-btn { display: none; }
.new-btn { height: 32px; padding: 0 14px 0 11px; gap: 6px; }
.knowledge-controls { display: contents; }
/* PN1 supplies the accessible view switch; KG2 retains its own controls and graph. */
.knowledge-controls :deep(.k-mode), .knowledge-controls :deep(.k-mode-sep) { display: none; }
/* Ticket views reclaim the row as the toolbar narrows. The open view keeps its
   name until 1300px; the others show their icon. Names stay on aria-label and
   the tip. Project sections and Knowledge are not this switch. Phones keep the
   full names on their own row. */
@media (min-width: 601px) {
  @container toolbar (max-width: 1500px) { .more-label { display: none; } .more-btn { padding: 0 9px; } .list-search { width: 208px; } }
  @container toolbar (max-width: 1420px) {
    .toolbar:not(.knowledge) :deep(.view-switch button:not([aria-selected="true"]) .tab-label) { display: none; }
    .toolbar:not(.knowledge) :deep(.view-switch button:not([aria-selected="true"])) { padding: 0 8px; }
  }
  @container toolbar (max-width: 1300px) {
    .toolbar:not(.knowledge) :deep(.view-switch button .tab-label) { display: none; }
    .toolbar:not(.knowledge) :deep(.view-switch button) { padding: 0 8px; }
  }
}
@container toolbar (max-width: 1300px) { .more-label { display: none; } .more-btn { padding: 0 9px; } .list-search { width: 190px; } }
@container toolbar (max-width: 1000px) { .list-search { width: 190px; } .count { display: none; } .new-btn { width: 32px; padding: 0; } .new-label { display: none; } .facet-btn[data-dim="type"]:not(.on) { display: none; } .display-label { display: none; } .display-btn { padding: 0 9px; } }
@container toolbar (max-width: 920px) { .list-search { width: 150px; } .facet-btn { padding: 0 11px; } .facet-btn:not(.on) .facet-end { display: none; } }
@container toolbar (max-width: 820px) { .list-search { width: 112px; } .list-search .field { padding-right: 10px; } .facet-btn { padding: 0 10px; } .facet-btn:not(.on) .facet-end { display: none; } }
@container toolbar (max-width: 760px) { .facet-btn[data-dim="assignee"]:not(.on) { display: none; } }
/* Docked beside a wide ticket panel the list can be phone-narrow: unused quick
   filters wait in the Filter menu, so the controls take two lines, not three. */
@container toolbar (max-width: 460px) { .facet-btn:not(.on):not(.more-btn) { display: none; } }
/* Narrowest docked width: a labelled pill replaces the switch and its longer label. */
.closed-pill { display: none; gap: 6px; padding: 0 11px 0 9px; color: var(--ink-2); }
.closed-pill.on { color: var(--teal-ink); }
@container toolbar (max-width: 800px) { .closed-switch { display: none; } .closed-pill { display: inline-flex; } }
@media (max-width: 900px) { .facets, .chips, .display-btn, .closed-switch, .closed-pill, .spacer { display: none; } .list-search { flex: 1; width: auto; } .filters-btn { display: inline-flex; } }
@media (max-width: 600px) {
  .toolbar { flex-wrap: wrap; gap: 8px; padding: 8px 0; }
  .view-switch { flex-basis: 100%; width: max-content; }
  .list-search .field { height: 44px; font-size: 16px; }
  .filters-btn { height: 44px; width: 44px; padding: 0; position: relative; }
  .filters-label { display: none; }
  .filters-btn .facet-count { position: absolute; top: -2px; right: -2px; }
  .new-btn { order: 3; width: 44px; height: 44px; padding: 0; }
  .new-label { display: none; }
  .count { display: none; }
  /* The view selector is the first row; each section keeps its controls below. */
  .toolbar.knowledge { flex-wrap: wrap; }
  .knowledge-controls { display: contents; }
}
</style>
