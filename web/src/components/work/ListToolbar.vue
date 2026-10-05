<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { vClipTip } from '../../lib/clipTip'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { DIMENSION_BY_KEY, DIMENSIONS, activeDimensions, dateLabel, excluded, fieldLabel, included, type DateFilter, type Dimension, type FacetOption, type GroupBy, type ListFilters } from '../../lib/ticketList'
import { TICKET_GRAPH_FILTERS } from '../../lib/ticketGraphRenderer'
import { type SortKey } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import ProjectTabs from './ProjectTabs.vue'
import { TICKET_VIEWS, KNOWLEDGE_VIEWS, type TicketView } from './projectNavigation'
import DateMenu from './DateMenu.vue'
import DisplayPanel from './DisplayPanel.vue'
import FacetMenu from './FacetMenu.vue'
import FilterMenu from './FilterMenu.vue'
import FloatingPanel from './FloatingPanel.vue'
import { hiddenStates, hideLabel, type HideState } from '../../lib/hideStates'
import type { ProjectSummary } from '../../lib/api'
import { statusMeta } from '../../lib/work'
import HideLabel from './HideLabel.vue'
import HideOptions from './HideOptions.vue'
import type { ColumnId } from '../../lib/columns'
import { workNoun } from '../../lib/workVocabulary'
import { useWorkVocabulary } from '../../stores/workVocabulary'

const vocabulary = useWorkVocabulary()
const createLabel = computed(() => `New ${workNoun(vocabulary.leaf.name)}`)

const props = defineProps<{
  filters: ListFilters
  summary?: ProjectSummary | null
  options: (dimension: Dimension) => FacetOption[]
  // The words for a selected value (an epic's title, a person's name).
  label: (dimension: Dimension, value: string) => string
  total: number | null
  totalIncomplete?: boolean
  scopeLabel?: string
  pendingChanges?: number
  pendingIncomplete?: boolean
  loading: boolean
  density: 'comfortable' | 'compact'
  stuck: boolean
  view: TicketView | 'journey' | 'knowledge' | 'releases'
  knowledgeView?: 'entries' | 'graph'
  // The table's columns for the Display menu's picker.
  columns?: { order: ColumnId[]; visible: ColumnId[]; customised: boolean; notes?: Partial<Record<string, string>> } | null
  facetErrors?: Record<string, string>
  facetLoading?: boolean
  headerGraph?: boolean
  settingsTarget?: string
  projectHeader?: boolean
}>()
const emit = defineEmits<{
  applyChanges: []
  search: [q: string]
  toggle: [dimension: Dimension, value: string]
  exclude: [dimension: Dimension, value: string]
  clear: [dimension: Dimension]
  clearAll: []
  showClosed: [value: boolean]
  hideStates: [states: HideState[]]
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
const hideAnchor = ref<HTMLElement | null>(null)
const hideName = computed(() => hideLabel(props.filters.hideStates))
const hideNames = computed(() => hiddenStates(props.filters.hideStates).map(state => statusMeta(state).label).join(', '))
function closeHide(restore: boolean) { const anchor = hideAnchor.value; hideAnchor.value = null; if (restore) anchor?.focus() }
let timer: ReturnType<typeof setTimeout> | undefined
watch(() => props.filters.q, value => { if (value !== draft.value.trim()) draft.value = value })
watch(draft, value => {
  clearTimeout(timer)
  timer = setTimeout(() => { if (value.trim() !== props.filters.q) emit('search', value.trim()) }, 220)
})
onBeforeUnmount(() => { clearTimeout(timer); resize?.disconnect() })

const graph = computed(() => props.view === 'graph')
const dimensions = computed(() => DIMENSIONS.filter(d => props.view === 'releases' ? d.key !== 'release' : !graph.value || TICKET_GRAPH_FILTERS.includes(d.key)))
// Keep the trigger's geometry and labels until its popover closes. Actual
// options still read live filters, including selections/exclusions made now.
const presented = ref(props.filters)
// Resolving a person's name or an epic's title can finish after the menu opens.
// Snapshot the words too, for the visible label, accessible name and clip-tip.
const resolvedLabels = computed(() => new Map(DIMENSIONS.map(({ key }) => [key, {
  plain: included(props.filters[key]).map(value => props.label(key, value)),
  not: excluded(props.filters[key]).map(value => props.label(key, value)),
}])))
const presentedLabels = ref(resolvedLabels.value)
watch([() => props.filters, resolvedLabels, open, dateAnchor, menuAnchor], () => {
  if (!open.value && !dateAnchor.value && !menuAnchor.value) {
    presented.value = props.filters
    presentedLabels.value = resolvedLabels.value
  }
}, { flush: 'sync' })
const primary = computed(() => dimensions.value.filter(d => d.primary || presented.value[d.key].length))
const active = computed(() => activeDimensions(presented.value).filter(key => props.view === 'releases' ? key !== 'release' : !graph.value || TICKET_GRAPH_FILTERS.includes(key)))
const secondaryActive = computed(() => active.value.filter(key => !DIMENSION_BY_KEY.get(key)!.primary).length + (!graph.value && presented.value.date ? 1 : 0))
const filterCount = computed(() => active.value.reduce((sum, key) => sum + props.filters[key].length, 0) + (props.filters.q ? 1 : 0) + (!graph.value && presented.value.date ? 1 : 0))
const activeFilterCount = computed(() => active.value.length + (!graph.value && presented.value.date ? 1 : 0))
const groupWord = computed(() => props.filters.group === 'tag' ? 'label' : props.filters.group)
const displayLabel = computed(() => props.view === 'outline' || props.filters.group === 'none' ? 'Display' : `Grouped by ${groupWord.value}`)
watch(() => props.view, () => { open.value = null; menuAnchor.value = null; dateAnchor.value = null; displayAnchor.value = null; hideAnchor.value = null })
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
function filterText(dimension: Dimension, full = false) {
  const { plain, not } = presentedLabels.value.get(dimension)!
  const shorten = (parts: string[]) => !full && parts.length > 2 ? `${parts.slice(0, 2).join(', ')} +${parts.length - 2}` : parts.join(', ')
  return [plain.length ? shorten(plain) : '', not.length ? `not ${shorten(not)}` : ''].filter(Boolean).join(' · ')
}
function filterKey(event: KeyboardEvent, clear: () => void) {
  if (event.key === 'Backspace' || event.key === 'Delete') {
    event.preventDefault()
    const chip = (event.currentTarget as HTMLElement).closest('.facet-control')
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
  <div ref="root" class="toolbar" :class="{ stuck, graph, knowledge: view === 'knowledge' }" role="toolbar" :aria-label="view === 'releases' ? 'Release list controls' : view === 'knowledge' ? 'Knowledge controls' : view === 'journey' ? 'Journey controls' : 'Ticket list controls'">
    <slot name="section-switch" />
    <ProjectTabs v-if="view !== 'journey' && view !== 'releases'" class="view-switch"
      :items="view === 'knowledge' ? KNOWLEDGE_VIEWS : TICKET_VIEWS"
      :selected="view === 'knowledge' ? knowledgeView ?? 'entries' : view"
      :tips="view !== 'knowledge'"
      :label="view === 'knowledge' ? 'Knowledge views' : 'Ticket views'" @select="value => emit('view', value)" />
    <span v-if="pendingChanges" class="count-live apply-slot" aria-live="polite"><button v-if="pendingChanges" type="button" class="btn sm apply-changes" @click="emit('applyChanges')">{{ pendingIncomplete ? '≥ ' : '' }}{{ pendingChanges }} {{ pendingChanges === 1 ? 'change' : 'changes' }} · Apply <kbd class="keycap">a</kbd></button></span>
    <span v-if="view !== 'knowledge' && view !== 'journey'" class="phone-break" aria-hidden="true" />
    <template v-if="view !== 'knowledge' && view !== 'journey'">
    <label class="search-field list-search">
      <AppIcon name="search" :size="14" />
      <input ref="input" v-model="draft" class="field" type="search" placeholder="Search" :aria-label="view === 'releases' ? 'Search releases and work in this project' : 'Search tickets in this project'" aria-keyshortcuts="/" autocomplete="off" spellcheck="false" @keydown="searchKey" />
      <kbd v-if="!draft && !narrow" class="keycap slash" aria-hidden="true">/</kbd>
      <button v-if="draft" type="button" class="clear-q" aria-label="Clear search" @click="clearSearch"><AppIcon name="close" :size="12" /></button>
    </label>

    <div class="facets" aria-label="Ticket filters">
      <span v-for="dimension in primary" :key="dimension.key" class="facet-control" :data-dim="dimension.key" :class="{ on: presented[dimension.key].length }">
        <button type="button" class="btn sm facet-btn" :data-dim="dimension.key"
          :class="{ on: presented[dimension.key].length }" :aria-label="presented[dimension.key].length ? `Edit ${dimension.title} filter: ${filterText(dimension.key, true)}` : dimension.title"
          :data-tip="presented[dimension.key].length ? `${dimension.title}: ${filterText(dimension.key, true)}` : undefined"
          :aria-expanded="open?.dimension === dimension.key" aria-haspopup="dialog" @click="openMenu(dimension.key, $event.currentTarget as HTMLElement)"
          @keydown="filterKey($event, () => emit('clear', dimension.key))">
          {{ dimension.title }}
          <span v-if="presented[dimension.key].length" v-clip-tip="filterText(dimension.key, true)" class="facet-value"> · {{ filterText(dimension.key) }}</span>
          <span class="facet-end"><span v-if="presented[dimension.key].length" class="facet-count mono">{{ presented[dimension.key].length }}</span><AppIcon v-else name="chevron" :size="12" class="facet-chevron" /></span>
        </button>
        <button v-if="presented[dimension.key].length" type="button" class="facet-x" :aria-label="`Remove ${dimension.title} filter`" @click="emit('clear', dimension.key)"><AppIcon name="close" :size="11" /></button>
      </span>
      <span v-if="!graph && presented.date" class="facet-control on date-filter">
        <button type="button" class="btn sm facet-btn on" :aria-label="`Edit date filter: ${fieldLabel(presented.date.field)} ${dateLabel(presented.date)}`"
          :data-tip="`${fieldLabel(presented.date.field)}: ${dateLabel(presented.date)}`" aria-haspopup="dialog" :aria-expanded="!!dateAnchor" @click="dateAnchor = $event.currentTarget as HTMLElement">
          <AppIcon name="calendar" :size="12" />{{ fieldLabel(presented.date.field) }}<span class="facet-value"> · {{ dateLabel(presented.date) }}</span><span class="facet-count date-count">1</span>
        </button>
        <button type="button" class="facet-x" aria-label="Remove date filter" @click="emit('date', null)"><AppIcon name="close" :size="11" /></button>
      </span>
      <button ref="filterButton" type="button" class="btn sm facet-btn more-btn" :class="{ on: secondaryActive }" aria-haspopup="menu" :aria-expanded="!!menuAnchor"
        aria-label="Filter by more" aria-keyshortcuts="Shift+F" :data-tip="graph ? 'Status, priority, type · Shift F' : 'Labels, human check, epic, cost unit, release, date · Shift F'" @click="menuAnchor = menuAnchor ? null : ($event.currentTarget as HTMLElement)">
        <AppIcon name="filter" :size="13" /><span class="more-label">Filter</span>
        <span v-if="secondaryActive" class="facet-count mono">{{ secondaryActive }}</span>
      </button>
      <button v-if="activeFilterCount || presented.q" type="button" class="btn sm ghost clear-all" @click="emit('clearAll')">Clear all</button>
    </div>

    <span class="spacer" />

    <Teleport defer :to="settingsTarget ?? 'body'" :disabled="!settingsTarget">
    <div class="view-settings">
    <div class="hide-control">
    <label class="switch closed-switch" :data-tip="hideNames">
      <input type="checkbox" :aria-label="hideName === 'Hide' ? `Hide ${hideNames}` : hideName" :checked="!filters.showClosed" @change="emit('showClosed', !($event.target as HTMLInputElement).checked)" />
      <HideLabel :states="filters.hideStates" />
    </label>
    <button
      type="button" class="btn sm closed-pill" :class="{ on: !filters.showClosed }" :aria-pressed="!filters.showClosed" :aria-label="`${hideName === 'Hide' ? `Hide ${hideNames}` : hideName} tickets`"
      :data-tip="`${hideNames}: ${filters.showClosed ? 'shown' : 'hidden'}`" @click="emit('showClosed', !filters.showClosed)"
    ><AppIcon :name="filters.showClosed ? 'eye' : 'eye-off'" :size="14" /><HideLabel :states="filters.hideStates" /></button>
    <button type="button" class="hide-gear" aria-label="Choose what Hide hides" data-tip="Choose what Hide hides" aria-haspopup="dialog" :aria-expanded="!!hideAnchor" @click="hideAnchor = hideAnchor ? null : ($event.currentTarget as HTMLElement)"><AppIcon name="gear" :size="14" /></button>
    </div>
    <button v-if="!graph && view !== 'releases'" type="button" class="btn sm display-btn" :class="{ on: view === 'list' && filters.group !== 'none' }" aria-haspopup="dialog" :aria-expanded="!!displayAnchor" :aria-label="`Display: ${displayLabel}`" data-tip="Grouping, sort, row and header height, and columns" @click="displayAnchor = displayAnchor ? null : ($event.currentTarget as HTMLElement)">
      <AppIcon name="layers" :size="13" /><span class="display-label">{{ displayText }}</span><AppIcon name="chevron" :size="12" class="facet-chevron" />
    </button>

    </div>
    </Teleport>
    <button v-if="!graph" type="button" class="btn primary new-btn" :aria-label="createLabel" aria-keyshortcuts="n" :data-tip="`${createLabel} · n`" @click="emit('create')"><AppIcon name="plus" :size="14" /><span class="new-label">New</span></button>
    <button type="button" class="btn filters-btn" :class="{ on: filterCount }" aria-label="Filters" aria-haspopup="dialog" data-tip="Filters and display options" @click="emit('openSheet')">
      <AppIcon name="sliders" :size="14" /><span class="filters-label">Filters</span><span v-if="filterCount" class="facet-count mono">{{ filterCount }}</span>
    </button>

    </template>
    <!-- The Knowledge tab teleports its own controls here (KnowledgeTab.vue). -->
    <div v-else-if="view === 'knowledge'" id="knowledge-controls" class="knowledge-controls" />
    <span v-else class="spacer" />
    <slot name="journey" />

    <FacetMenu
      v-if="open" :anchor="open.anchor" :dimension="open.dimension" :title="title(open.dimension)" :options="options(open.dimension)" :selected="filters[open.dimension]" :loading="facetLoading" :error="facetErrors?.[open.dimension]"
      @toggle="value => emit('toggle', open!.dimension, value)" @exclude="value => emit('exclude', open!.dimension, value)" @clear="emit('clear', open!.dimension)" @close="closeMenu"
    />
    <FilterMenu v-if="menuAnchor" :anchor="menuAnchor" :filters="filters" :dimensions="view === 'releases' ? dimensions.map(d => d.key) : graph ? TICKET_GRAPH_FILTERS : undefined" :show-date="!graph" @choose="chooseFilter" @close="restore => { const a = menuAnchor; menuAnchor = null; if (restore) a?.focus() }" />
    <DateMenu v-if="dateAnchor" :anchor="dateAnchor" :value="filters.date" @change="value => emit('date', value)" @close="closeDate" />
    <FloatingPanel v-if="hideAnchor" :anchor="hideAnchor" :width="288" align="end" label="What Hide hides" cycle @close="closeHide">
      <HideOptions :states="filters.hideStates" :summary="summary" :show-closed="filters.showClosed" @change="states => emit('hideStates', states)" />
    </FloatingPanel>
    <FloatingPanel v-if="displayAnchor" :anchor="displayAnchor" :width="320" :tallest="760" align="end" label="Display options" @close="closeDisplay">
      <DisplayPanel
        :filters="filters" :view="view === 'outline' ? 'outline' : 'list'" :density="density" :columns="columns" :grouped="view === 'list' && filters.group !== 'none'"
        :header-graph="headerGraph" :project-header="projectHeader"
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
.clear-all { padding: 0 8px; }
.spacer { flex: 1; }
/* The count keeps its width while numbers change, so the controls beside it never shift. */
.count { display: inline-block; min-width: 13ch; text-align: right; font-size: 12px; color: var(--ink-2); white-space: nowrap; }
.apply-changes { border: 0; padding: 0; background: transparent; color: var(--teal-ink); font: inherit; }
.count { width: clamp(13ch, 19vw, 30ch); overflow: hidden; text-overflow: ellipsis; }
.count.scoped { width: clamp(13ch, 19vw, 30ch); overflow: hidden; text-overflow: ellipsis; }
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

/* The toolbar's geometry is independent of header density. Search gives way
   first; an overfull filter strip scrolls without widening the page. */
.toolbar:not(.knowledge) { flex-wrap: nowrap; min-height: 44px; padding: 6px 0; }
.view-switch { order: 0; }
.list-search { order: 1; flex: 0 1 240px; min-width: 150px; }
.facets { order: 2; min-width: 0; flex: 0 1 auto; overflow-x: auto; scrollbar-width: thin; padding: 3px; }
.facet-control { display: inline-flex; align-items: center; flex: none; height: 28px; border-radius: 8px; }
.facet-control.on { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.facet-control.on .facet-btn { background: transparent; border-color: transparent; box-shadow: none; border-radius: 8px 0 0 8px; padding-right: 4px; }
.facet-value { max-width: 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.facet-control.on .facet-end { display: none; }
.facet-x { display: grid; place-items: center; width: 24px; height: 28px; flex: none; padding: 0; border: 0; border-radius: 0 8px 8px 0; background: transparent; color: var(--teal-ink); }
.facet-x:hover { background: var(--row-selected); }
.facet-x:focus-visible { box-shadow: var(--focus-ring); }
.more-btn, .clear-all { flex: none; }
.spacer { order: 3; }
.count-live { order: 4; display: inline-flex; align-items: center; gap: 8px; flex: none; }
.count-live .count { min-width: 10ch; order: 1; }
.new-btn { order: 5; flex: none; }
.filters-btn { order: 4; flex: none; }
.hide-control { display: inline-flex; align-items: center; gap: 2px; margin-right: 10px; flex: none; }
.hide-gear { display: grid; place-items: center; flex: none; width: 28px; height: 28px; border: 0; padding: 0; border-radius: 6px; background: transparent; color: var(--ink-3); }
.hide-gear:hover, .hide-gear[aria-expanded="true"] { background: var(--row-hover); color: var(--ink); }
.hide-gear:focus-visible { outline: 2px solid var(--teal); outline-offset: 1px; }
@media (pointer: coarse) { .hide-gear { width: 44px; height: 44px; } .closed-switch, .closed-pill { min-height: 44px; } }
.view-settings { display: flex; align-items: center; gap: 10px; flex: none; }
.phone-break, .date-count { display: none; }
@media (min-width: 601px) {
  @container toolbar (max-width: 1100px) {
    .facet-value { display: none; }
    .facet-control.on .facet-end { display: inline-grid; }
    .count-live .count { display: none; }
    .date-count { display: inline-grid; }
  }
}
@media (max-width: 900px) { .view-settings { display: none; } }
@media (max-width: 600px) {
  .toolbar:not(.knowledge) { flex-wrap: wrap; gap: 8px; padding: 8px 0; }
  .view-switch { flex-basis: auto; width: auto; }
  .count-live { order: 0; margin-left: auto; }
  .count-live .count { display: inline-block; min-width: 0; font-size: 11px; }
  .phone-break { display: block; order: 0; flex-basis: 100%; height: 0; margin-top: -8px; }
  .list-search { flex: 1 1 150px; min-width: 150px; }
  .filters-btn { order: 2; }
  .new-btn { order: 3; }
}
@media (pointer: coarse) and (min-width: 601px) {
  .facet-control, .facet-btn, .facet-x, .clear-all, .new-btn { min-height: 44px; }
  .facet-x { min-width: 44px; }
}


.apply-slot { position: absolute; right: 5rem; top: 6px; z-index: 2; }

.apply-slot { background: var(--surface-raised); border-radius: 6px; max-width: calc(100% - 8rem); overflow: hidden; }
@media (max-width: 600px) { .apply-slot { top: auto; bottom: 8px; right: 4.5rem; } }
</style>
