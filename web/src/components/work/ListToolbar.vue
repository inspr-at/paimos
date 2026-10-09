<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { DIMENSION_BY_KEY, DIMENSIONS, GROUPS, activeDimensions, dateLabel, excluded, fieldLabel, included, type DateFilter, type Dimension, type FacetOption, type GroupBy, type ListFilters } from '../../lib/ticketList'
import { TICKET_GRAPH_FILTERS } from '../../lib/ticketGraphRenderer'
import type { SortKey } from '../../lib/work'
import { menuPanelWidth } from '../../lib/menuColumns'
import AppIcon from '../AppIcon.vue'
import ProjectTabs from './ProjectTabs.vue'
import { TICKET_VIEWS, KNOWLEDGE_VIEWS, type TicketView } from './projectNavigation'
import DateMenu from './DateMenu.vue'
import DisplayMenu from './DisplayMenu.vue'
import DisplayPanel from './DisplayPanel.vue'
import HeaderRoomyChoice from './HeaderRoomyChoice.vue'
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
import { ATTENTION_KINDS, type AttentionFacet, type AttentionFilters, type AttentionGrouping } from '../../lib/attention'

const vocabulary = useWorkVocabulary()
const createLabel = computed(() => `New ${workNoun(vocabulary.leaf.name)}`)

const props = defineProps<{
  filters: ListFilters
  summary?: ProjectSummary | null
  options: (dimension: Dimension) => FacetOption[]
  // The words for a selected value (an epic's title, a person's name).
  label: (dimension: Dimension, value: string) => string
  total: number | null
  loading: boolean
  density: 'comfortable' | 'compact'
  stuck: boolean
  view: TicketView | 'knowledge'
  knowledgeView?: 'entries' | 'graph'
  // The table's columns for the Display menu's picker.
  columns?: { order: ColumnId[]; visible: ColumnId[]; customised: boolean; notes?: Partial<Record<string, string>> } | null
  facetLoading?: boolean
  settingsTarget?: string
  projectHeader?: boolean
  // The project header is collapsed: Display moves into the toolbar and carries what the header hid.
  collapsedHeader?: boolean
  attention?: { owner: string; filters: AttentionFilters; group: AttentionGrouping; facets: { projects: AttentionFacet[]; assignees: AttentionFacet[] }; truncated: boolean; locale: string }
}>()
const emit = defineEmits<{
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
  attentionFilter: [field: keyof AttentionFilters, value: string]
  attentionGroup: [value: AttentionGrouping]
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
// Windows this narrow (and phones) show the collapsed header's Display menu as a sheet.
const sheetMode = ref(window.innerWidth <= 900)
const menuColumns = ref(1)
const onWindowResize = () => { sheetMode.value = window.innerWidth <= 900 }
onMounted(() => window.addEventListener('resize', onWindowResize))
onBeforeUnmount(() => window.removeEventListener('resize', onWindowResize))
const input = ref<HTMLInputElement>()
const filterButton = ref<HTMLButtonElement>()
const open = ref<{ dimension: Dimension; anchor: HTMLElement } | null>(null)
const dateAnchor = ref<HTMLElement | null>(null)
const menuAnchor = ref<HTMLElement | null>(null)
const displayAnchor = ref<HTMLElement | null>(null)
const attentionMenu = ref<{ field: 'kind' | 'project_id' | 'assignee'; anchor: HTMLElement } | null>(null)
const attentionWord = (en: string, de: string) => props.attention?.locale === 'de' ? de : en
const attentionFacets = computed(() => [{ field: 'kind', label: attentionWord('Kind', 'Art') }, { field: 'project_id', label: attentionWord('Project', 'Projekt') }, { field: 'assignee', label: attentionWord('Assignee', 'Zuständig') }] as const)
const attentionOptions = computed<AttentionFacet[]>(() => attentionMenu.value?.field === 'kind'
  ? [{ id: '', label: attentionWord('Every kind', 'Alle Arten') }, ...ATTENTION_KINDS.map(kind => ({ id: kind.id, label: props.attention?.locale === 'de' ? kind.labelDe : kind.label }))]
  : attentionMenu.value?.field === 'project_id' ? [{ id: '', label: attentionWord('All projects', 'Alle Projekte') }, ...(props.attention?.facets.projects ?? [])]
    : [{ id: '', label: attentionWord('Every assignee', 'Alle Zuständigen') }, { id: 'none', label: attentionWord('Unassigned', 'Niemand') }, ...(props.attention?.facets.assignees ?? [])])
function closeAttention(restore: boolean) { const anchor = attentionMenu.value?.anchor; attentionMenu.value = null; if (restore) anchor?.focus() }
function attentionMenuKeys(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const buttons = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')]
  const index = buttons.indexOf(document.activeElement as HTMLButtonElement)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length
  event.preventDefault(); event.stopPropagation(); buttons[next]?.focus()
}
const hideAnchor = ref<HTMLElement | null>(null)
const hideName = computed(() => hideLabel(props.filters.hideStates))
const hideNames = computed(() => hiddenStates(props.filters.hideStates).map(state => statusMeta(state).label).join(', '))
function closeHide(restore: boolean) { const anchor = hideAnchor.value; hideAnchor.value = null; if (restore) anchor?.focus() }
let timer: ReturnType<typeof setTimeout> | undefined
watch(() => props.attention?.owner, () => { clearTimeout(timer); closeAttention(false); displayAnchor.value = null; draft.value = props.filters.q })
watch(() => props.filters.q, value => { if (value !== draft.value.trim()) draft.value = value })
watch(draft, value => {
  clearTimeout(timer)
  timer = setTimeout(() => { if (value.trim() !== props.filters.q) emit('search', props.attention ? value.slice(0, 200) : value.trim()) }, props.attention ? 250 : 220)
})
onBeforeUnmount(() => { clearTimeout(timer); resize?.disconnect() })

const graph = computed(() => props.view === 'graph')
const dimensions = computed(() => DIMENSIONS.filter(d => !graph.value || TICKET_GRAPH_FILTERS.includes(d.key)))
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
const active = computed(() => activeDimensions(presented.value).filter(key => !graph.value || TICKET_GRAPH_FILTERS.includes(key)))
const secondaryActive = computed(() => active.value.filter(key => !DIMENSION_BY_KEY.get(key)!.primary).length + (!graph.value && presented.value.date ? 1 : 0))
const filterCount = computed(() => active.value.reduce((sum, key) => sum + props.filters[key].length, 0) + (props.filters.q ? 1 : 0) + (!graph.value && presented.value.date ? 1 : 0))
const activeFilterCount = computed(() => active.value.length + (!graph.value && presented.value.date ? 1 : 0))
const groupWord = computed(() => props.filters.group === 'tag' ? 'label' : props.filters.group)
const displayLabel = computed(() => props.view === 'outline' || props.filters.group === 'none' ? 'Display' : `Grouped by ${groupWord.value}`)
watch(() => props.view, () => { open.value = null; menuAnchor.value = null; dateAnchor.value = null; displayAnchor.value = null; hideAnchor.value = null })
// Every label the collapsed trigger can show. The button is as wide as the longest,
// so choosing a grouping never resizes it or the New button beside it.
function displayPhrase(group: GroupBy) { return group === 'none' ? 'Display' : `By ${group === 'tag' ? 'label' : group}` }
const displayWords = GROUPS.map(option => displayPhrase(option.value))
const displayText = computed(() => props.view === 'outline' ? 'Display' : displayPhrase(props.filters.group))
// The collapsed header's menu replaces the plain Display panel; the attention host keeps its own.
const collapsedMenu = computed(() => !!props.collapsedHeader && !props.attention)
const displayOn = computed(() => props.view === 'list' && props.filters.group !== 'none')
// Folding or unfolding the header moves the Display button: its popovers anchor to the old place.
watch(() => props.collapsedHeader, () => { displayAnchor.value = null; hideAnchor.value = null })

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
// The gear in the collapsed menu: Hide's choices open where the Display button is, once the menu has closed.
function openHideFromMenu() { const anchor = displayAnchor.value; closeDisplay(false); hideAnchor.value = anchor }
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
    if (props.attention) { input.value?.blur(); return }
    if (draft.value) clearSearch()
    else input.value?.blur()
  }
}
function closeOverlays() { closeAttention(false); closeDisplay(false) }
defineExpose({ focusSearch, openFilterMenu, input, closeOverlays })
</script>

<template>
  <div ref="root" class="toolbar" :class="{ stuck, graph, attention: !!attention, knowledge: view === 'knowledge' }" role="toolbar" :aria-label="attention ? attentionWord('List controls', 'Listensteuerung') : view === 'knowledge' ? 'Knowledge controls' : 'Ticket list controls'">
    <ProjectTabs v-if="!attention" class="view-switch"
      :items="view === 'knowledge' ? KNOWLEDGE_VIEWS : TICKET_VIEWS"
      :selected="view === 'knowledge' ? knowledgeView ?? 'entries' : view"
      :tips="view !== 'knowledge'"
      :label="view === 'knowledge' ? 'Knowledge views' : 'Ticket views'" @select="value => emit('view', value)" />
    <span v-if="attention" class="count-live">
      <span class="count mono" role="status" aria-live="polite"><span v-if="total === null && loading" class="skeleton count-skeleton" aria-label="Counting tickets" /><template v-else-if="total !== null">{{ `${total} ${attentionWord('suggestions', 'Vorschläge')}` }}</template></span>
    </span>
    <span v-if="view !== 'knowledge'" class="phone-break" aria-hidden="true" />
    <template v-if="view !== 'knowledge'">
    <label class="search-field list-search">
      <AppIcon name="search" :size="14" />
      <input ref="input" v-model="draft" class="field" type="search" :maxlength="attention ? 200 : undefined" :placeholder="attention ? attentionWord('Search this list', 'Liste durchsuchen') : narrow ? 'Search' : graph ? 'Search tickets' : 'Search this list'" :aria-label="attention ? 'Search this view' : 'Search tickets in this project'" aria-keyshortcuts="/" autocomplete="off" spellcheck="false" @keydown="searchKey" />
      <kbd v-if="!draft && !narrow" class="keycap slash" aria-hidden="true">/</kbd>
      <button v-if="draft" type="button" class="clear-q" aria-label="Clear search" @click="clearSearch"><AppIcon name="close" :size="12" /></button>
    </label>

    <div v-if="attention" class="facets attention-facets" aria-label="Suggestion filters">
      <button v-for="facet in attentionFacets" :key="facet.field" type="button" class="btn sm facet-button" :class="{ on: !!attention.filters[facet.field] }" :aria-label="`${facet.label} filter`" :aria-expanded="attentionMenu?.field === facet.field" @click="attentionMenu = attentionMenu?.field === facet.field ? null : { field: facet.field, anchor: $event.currentTarget as HTMLElement }">{{ facet.label }}<AppIcon name="chevron" :size="12" /></button>
      <button type="button" class="btn sm ghost reset" :aria-disabled="!Object.values(attention.filters).some(Boolean)" @click="emit('clearAll')">{{ attentionWord('Clear filters', 'Filter löschen') }}</button>
    </div>
    <div v-else class="facets" aria-label="Ticket filters">
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
    <div v-if="!attention" class="hide-control">
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
    <button v-if="!graph" type="button" class="btn sm display-btn" :class="{ on: displayOn }" aria-haspopup="dialog" :aria-expanded="!!displayAnchor" :aria-label="`Display: ${displayLabel}`" data-tip="Grouping, sort, row and header height, and columns" @click="displayAnchor = displayAnchor ? null : ($event.currentTarget as HTMLElement)">
      <AppIcon name="layers" :size="13" /><span class="display-label">{{ attention ? attention.group === 'project' ? attentionWord('By project', 'Nach Projekt') : attention.group === 'kind' ? attentionWord('By kind', 'Nach Art') : attentionWord('Display', 'Anzeige') : displayText }}</span><AppIcon name="chevron" :size="12" class="facet-chevron" />
    </button>

    </div>
    </Teleport>
    <!-- Collapsed header: Display sits beside New. The other densities keep its place free, as an
         inert twin of the same size, so folding or unfolding the header moves nothing in this row. -->
    <div v-if="settingsTarget && !attention" class="collapsed-display" :class="{ reserved: !collapsedHeader }" :inert="!collapsedHeader" :aria-hidden="collapsedHeader ? undefined : 'true'">
      <button type="button" class="btn sm display-btn" :class="{ on: displayOn }" aria-haspopup="dialog" :aria-expanded="!!displayAnchor && collapsedHeader" :aria-label="`Display: ${displayLabel}`" data-tip="Sections, saved views, Hide closed, grouping, sort, row height and columns" :tabindex="collapsedHeader ? undefined : -1" @click="displayAnchor = displayAnchor ? null : ($event.currentTarget as HTMLElement)">
        <AppIcon name="layers" :size="13" /><span class="display-label"><span class="display-current">{{ displayText }}</span><span class="display-sizer" aria-hidden="true"><span v-for="word in displayWords" :key="word">{{ word }}</span></span></span><AppIcon name="chevron" :size="12" class="facet-chevron" />
      </button>
    </div>
    <button v-if="!graph && !attention" type="button" class="btn primary new-btn" :aria-label="createLabel" aria-keyshortcuts="n" :data-tip="`${createLabel} · n`" @click="emit('create')"><AppIcon name="plus" :size="14" /><span class="new-label">New</span></button>
    <button v-if="!attention" type="button" class="btn filters-btn" :class="{ on: filterCount }" aria-label="Filters" aria-haspopup="dialog" data-tip="Filters and display options" @click="emit('openSheet')">
      <AppIcon name="sliders" :size="14" /><span class="filters-label">Filters</span><span v-if="filterCount" class="facet-count mono">{{ filterCount }}</span>
    </button>

    </template>
    <!-- The Knowledge tab teleports its own controls here (KnowledgeTab.vue). -->
    <div v-else id="knowledge-controls" class="knowledge-controls" />

    <FloatingPanel v-if="attention && attentionMenu" :anchor="attentionMenu.anchor" :label="`${attentionFacets.find(facet => facet.field === attentionMenu!.field)!.label} filter`" @close="closeAttention">
      <div class="attention-menu" role="menu" :aria-label="`${attentionFacets.find(facet => facet.field === attentionMenu!.field)!.label} filter`" @keydown="attentionMenuKeys">
        <button v-for="option in attentionOptions" :key="option.id" type="button" role="menuitemradio" :aria-checked="attention.filters[attentionMenu.field] === option.id" :data-autofocus="attention.filters[attentionMenu.field] === option.id ? '' : undefined" @click="emit('attentionFilter', attentionMenu!.field, option.id); closeAttention(true)"><span v-clip-tip>{{ option.label }}</span><AppIcon v-if="attention.filters[attentionMenu.field] === option.id" name="check" :size="13" /></button>
      </div>
      <p v-if="attention.truncated" class="attention-note">{{ attentionWord('Only the first 100 facet choices are shown. Search narrows the ticket list.', 'Nur die ersten 100 Filterwerte werden gezeigt. Die Suche grenzt die Ticket-Liste ein.') }}</p>
    </FloatingPanel>
    <FacetMenu
      v-if="open" :anchor="open.anchor" :dimension="open.dimension" :title="title(open.dimension)" :options="options(open.dimension)" :selected="filters[open.dimension]" :loading="facetLoading"
      @toggle="value => emit('toggle', open!.dimension, value)" @exclude="value => emit('exclude', open!.dimension, value)" @clear="emit('clear', open!.dimension)" @close="closeMenu"
    />
    <FilterMenu v-if="menuAnchor" :anchor="menuAnchor" :filters="filters" :dimensions="graph ? TICKET_GRAPH_FILTERS : undefined" :show-date="!graph" @choose="chooseFilter" @close="restore => { const a = menuAnchor; menuAnchor = null; if (restore) a?.focus() }" />
    <DateMenu v-if="dateAnchor" :anchor="dateAnchor" :value="filters.date" @change="value => emit('date', value)" @close="closeDate" />
    <FloatingPanel v-if="hideAnchor" :anchor="hideAnchor" :width="288" align="end" label="What Hide hides" cycle @close="closeHide">
      <HideOptions :states="filters.hideStates" :summary="summary" :show-closed="filters.showClosed" @change="states => emit('hideStates', states)" />
    </FloatingPanel>
    <FloatingPanel
      v-if="displayAnchor" :anchor="displayAnchor" :width="collapsedMenu ? menuPanelWidth(menuColumns) : 320" :tallest="collapsedMenu ? 4000 : 760" align="end" label="Display options"
      :cycle="collapsedMenu" :sheet="collapsedMenu && sheetMode" @close="closeDisplay"
    >
      <DisplayMenu :plain="!collapsedMenu" :anchor="displayAnchor" :sheet="sheetMode" @columns="count => menuColumns = count" @done="closeDisplay(true)">
        <template #nav><slot name="header-nav" :anchor="displayAnchor!" :close="closeDisplay" /></template>
        <template #hide>
          <div class="menu-hide">
            <label class="switch menu-switch" :data-tip="hideNames">
              <input type="checkbox" :aria-label="hideName === 'Hide' ? `Hide ${hideNames}` : hideName" :checked="!filters.showClosed" @change="emit('showClosed', !($event.target as HTMLInputElement).checked)" />
              <HideLabel :states="filters.hideStates" />
            </label>
            <button type="button" class="menu-gear" aria-label="Choose what Hide hides" data-tip="Choose what Hide hides" aria-haspopup="dialog" @click="openHideFromMenu"><AppIcon name="gear" :size="14" /></button>
          </div>
        </template>
        <DisplayPanel
          v-if="!graph"
          :attention-group="attention?.group" :locale="attention?.locale" @attention-group="value => emit('attentionGroup', value)"
          :filters="filters" :view="view === 'outline' ? 'outline' : 'list'" :density="density" :columns="columns" :grouped="view === 'list' && filters.group !== 'none'"
          :project-header="projectHeader" :sheet="collapsedMenu && sheetMode" :stable-sort="collapsedMenu"
          @group="value => emit('group', value)" @sort="keys => emit('sort', keys)" @density="value => emit('density', value)"
          @columns="(order, visible) => emit('columns', order, visible)" @columns-reset="emit('columnsReset')"
          @expand-all="emit('expandAll'); closeDisplay(false)" @collapse-all="emit('collapseAll'); closeDisplay(false)"
          @expand-groups="emit('expandGroups')" @collapse-groups="emit('collapseGroups')"
        />
        <HeaderRoomyChoice v-else />
      </DisplayMenu>
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
.facet-count { display: inline-grid; place-items: center; min-width: 17px; height: 17px; padding: 0 5px; border-radius: 999px; background: linear-gradient(180deg, var(--primary-hi), var(--primary)); color: var(--primary-on); font-size: 10.5px; font-weight: 700; }
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
  .facet-control, .facet-btn, .facet-x, .clear-all, .new-btn, .collapsed-display .display-btn { min-height: 44px; }
  .facet-x { min-width: 44px; }
}

/* Collapsed project header (AEON-1028): Display sits beside New and carries what the header hid.
   In the other densities the same button waits there inert and unseen, so folding or unfolding
   the header moves nothing in this row. It stays on every window width, where the Display button
   of the header's own bar is hidden below 900px. */
.collapsed-display { order: 4; display: flex; align-items: center; flex: none; }
.collapsed-display.reserved { visibility: hidden; pointer-events: none; }
.collapsed-display .display-btn { display: inline-flex; }
/* As wide as the longest grouping label, so "Display" and "By assignee" share one footprint. */
.collapsed-display .display-label { display: inline-grid; }
.collapsed-display .display-current, .collapsed-display .display-sizer { grid-area: 1 / 1; }
.collapsed-display .display-current { white-space: nowrap; }
.collapsed-display .display-sizer { visibility: hidden; display: grid; }
.collapsed-display .display-sizer > span { grid-area: 1 / 1; white-space: nowrap; }
@container toolbar (max-width: 1000px) { .collapsed-display .display-label { display: none; } }
/* On tablets Filters shares the row: Display still sits right against New. */
@media (min-width: 601px) and (max-width: 900px) { .collapsed-display { order: 5; } .new-btn { order: 6; } }
@media (max-width: 600px) {
  .collapsed-display { order: 3; }
  .collapsed-display .display-btn { width: 44px; height: 44px; padding: 0; justify-content: center; }
  .collapsed-display .display-label, .collapsed-display .facet-chevron { display: none; }
}
/* The collapsed menu's Hide closed row: the switch and the gear to Hide's choices. */
.menu-hide { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 34px; }
.menu-switch { min-height: 34px; font-size: 13px; }
.menu-gear { display: grid; place-items: center; flex: none; width: 34px; height: 34px; padding: 0; border: 0; border-radius: 8px; background: transparent; color: var(--ink-3); }
.menu-gear:hover { background: var(--row-hover); color: var(--ink); }
.menu-gear:focus-visible { box-shadow: var(--focus-ring); }
@media (pointer: coarse), (max-width: 900px) { .menu-hide, .menu-switch { min-height: 44px; } .menu-gear { width: 44px; height: 44px; } }

/* The attention host keeps its three facets and Display on every screen. */
.toolbar.attention { flex-wrap: wrap; gap: 8px 10px; padding: 9px 0; }
.attention .count-live { order: 0; }
.attention .count-live .count { display: inline-block; min-width: 16ch; text-align: left; }
.attention .phone-break { display: none; }
.attention .list-search { flex: 0 1 240px; }
.attention .facets { display: flex; overflow: visible; flex: none; padding: 0; }
.attention .spacer { display: block; }
.attention .view-settings { display: flex; order: 4; }
.attention .display-btn { display: inline-flex; min-inline-size: 11em; }
.attention .display-label { display: inline; }
.attention .facet-button { min-inline-size: 7em; }
.attention-menu { display: grid; }
.attention-menu button { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 34px; padding: 6px 10px; border: 0; border-radius: 6px; background: transparent; color: var(--ink); text-align: left; font-size: 13px; }
.attention-menu button:hover, .attention-menu button[aria-checked="true"] { background: var(--row-selected); }
.attention-menu button:focus-visible { box-shadow: var(--focus-ring); }
.attention-note { padding: 8px 10px; color: var(--ink-3); font-size: 12px; }
@media (pointer: coarse), (max-width: 720px) { .attention .btn.sm, .attention-menu button { min-height: 44px; } }
@media (max-width: 720px) {
  .attention .count-live { flex-basis: 100%; }
  .attention .list-search { flex: 1 1 100%; }
  .attention .list-search .field { height: 44px; font-size: 16px; }
  .attention .facets { flex: 1 1 100%; flex-wrap: wrap; }
  .attention .spacer { display: none; }
}
</style>
