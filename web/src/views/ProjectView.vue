<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { setPageTitle } from '../lib/brand'
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, provide, reactive, ref, toRefs, watch } from 'vue'
import { isNavigationFailure, NavigationFailureType, routeLocationKey, routerKey, type RouteLocationRaw, onBeforeRouteLeave, onBeforeRouteUpdate, useRoute, useRouter } from 'vue-router'
import { APIError, createNode, listNodes, type BulkChange, type BulkResult, type ListItem, type SavedView } from '../lib/api'
import { askDoneGate } from '../lib/doneGateAsk'
import { benefitGateError, benefitSkip, benefitStepSummary, completionFields, needsBenefitPrompt, skippedStatusLabel } from '../lib/doneGate'
import { can } from '../lib/authz'
import { confirmAction } from '../lib/confirm'
import { asListItem, guardedMove, keyPrefix, kinds } from '../lib/useTicket'
import { useOutline } from '../lib/useOutline'
import { useDensity, useHeaderGraph } from '../lib/prefs'
import { orderOf, PINNED, type ColumnId, type ListPrefs } from '../lib/columns'
import { copyName, duplicateView, loadViews, removeView, renameView, saveNewView, saveViewState, shareView, viewsOf } from '../lib/savedViews'
import { usePreference } from '../lib/preferences'
import { toast } from '../lib/toast'
import { settledNavigation } from '../lib/navigation'
import { command, consume, run } from '../lib/commands'
import { remember } from '../lib/recents'
import { apiParams, clearedFilters, effectiveSort, facetOptions, filtersFromQuery, filtersFromView, filtersToQuery, groupFacet, groupRows, hasFilters, orderByStatus, rowTags, sameListState, suggestName, toggleIn, toggleOut, totalFrom, valueLabel, WORK_KINDS, type DateFilter, type Dimension, type EpicRef, type GroupBy, type ListFilters } from '../lib/ticketList'
import type { TicketGraphState } from '../lib/ticketGraphRenderer'
import { useTicketList } from '../lib/useTicketList'
import { useLiveList } from '../lib/useLiveList'
import { PROJECT_COLUMN_BY_ID, projectProgressTip } from '../lib/projectColumns'
import { absoluteTime, cycleSort, plural, PRIORITIES, priorityLabel, relativeTime, statusMeta, type SortField, type SortKey } from '../lib/work'
import { useProjects } from '../stores/projects'
import { useSession } from '../stores/session'
import AppIcon from '../components/AppIcon.vue'
import PanelSplitter from '../components/PanelSplitter.vue'
import FilterSheet from '../components/work/FilterSheet.vue'
import ListToolbar from '../components/work/ListToolbar.vue'
import ProjectTabs from '../components/work/ProjectTabs.vue'
import { PROJECT_SECTIONS, projectSection, ticketView, type ProjectSection, type TicketView } from '../components/work/projectNavigation'
import StatusIcon from '../components/work/StatusIcon.vue'
import StatusMenu from '../components/work/StatusMenu.vue'
import TicketTable from '../components/work/TicketTable.vue'
import TicketWorkspace from '../components/work/TicketWorkspace.vue'
import ViewBar from '../components/work/ViewBar.vue'
import SaveViewPanel from '../components/work/SaveViewPanel.vue'
import BulkBar from '../components/work/BulkBar.vue'
import LabelMenu, { type LabelChoice } from '../components/work/LabelMenu.vue'
import OptionMenu from '../components/work/OptionMenu.vue'
import EpicPicker from '../components/work/EpicPicker.vue'
import ReleasePicker from '../components/work/ReleasePicker.vue'
import { AssignCancelled, assignToRelease, type ReleaseTarget } from '../lib/releaseAssign'
import { listNativeMemberships, openedMembershipMessage, type NativeReleaseView } from '../lib/releaseMembership'
import { useJourney } from '../stores/journey'
import { flowPillContext } from '../lib/flowPillContext'
import HeaderGlimpse from '../components/work/HeaderGlimpse.vue'
import type KnowledgeEntryPageType from '../components/knowledge/KnowledgeEntryPage.vue'
import type KnowledgeTabType from '../components/knowledge/KnowledgeTab.vue'
import { DOCK_LIST_RESERVE, DOCK_MEDIA, entryPath, isKnowledgeType, parseEntryParam, type KnowledgeEntry, type KnowledgeType } from '../lib/knowledge'
import { filtersFromQuery as knowledgeFiltersFrom, filtersToQuery as knowledgeQuery, useKnowledge, type KnowledgeFilters } from '../lib/useKnowledge'
import type { Stage } from '../lib/journey'
import type { QuickDraft } from '../components/work/QuickCreateRow.vue'

const JourneyView = defineAsyncComponent(() => import('../components/journey/JourneyView.vue'))
// Knowledge loads with its tab, not with every project page.
const KnowledgeTab = defineAsyncComponent(() => import('../components/knowledge/KnowledgeTab.vue'))
const KnowledgeEntryPage = defineAsyncComponent(() => import('../components/knowledge/KnowledgeEntryPage.vue'))

const route = useRoute()
const router = useRouter()
const projects = useProjects()
const session = useSession()

const projectKey = computed(() => String(route.params.projectKey ?? ''))
const ticketKey = computed(() => typeof route.params.ticketKey === 'string' ? route.params.ticketKey : '')
const project = computed(() => projects.byRouteKey(projectKey.value))
const projectId = computed(() => project.value?.id ?? null)
const routeKey = computed(() => project.value?.routeKey ?? projectKey.value)

// ---------- Columns: the person's order, visibility and widths for this project ----------
const listPref = computed(() => projectId.value ? usePreference<ListPrefs>(`list:${projectId.value}`) : null)
const listPrefs = computed(() => listPref.value?.value.value ?? null)
// On the Knowledge tab the address's search and filters are the tab's own, not the ticket list's.
const section = computed(() => projectSection(route))
const sectionPath = (value: ProjectSection = section.value) => `/p/${encodeURIComponent(routeKey.value)}/${value}`
const ticketSectionQuery = (value: ProjectSection = section.value): Record<string, string> => value === 'tickets' ? {} : { section: value }
const onKnowledge = () => section.value === 'knowledge'
const filters = computed(() => filtersFromQuery(section.value !== 'tickets' ? {} : route.query))
// A view (or a shared link) may carry its own column set; widths stay the person's.
const tablePrefs = computed<ListPrefs | null>(() => {
  const cols = filters.value.cols
  if (!cols) return listPrefs.value
  const rest = orderOf(listPrefs.value).filter(id => !PINNED.includes(id) && !cols.includes(id))
  return { ...(listPrefs.value ?? {}), order: [...PINNED, ...cols, ...rest], visible: cols }
})
const tableLayout = ref<{ visible: ColumnId[]; customised: boolean }>({ visible: [], customised: false })
const toolbarColumns = computed(() => ({ order: orderOf(tablePrefs.value), visible: tableLayout.value.visible, customised: tableLayout.value.customised }))
// In a saved view the columns belong to the view (they become part of the list's
// state); on the plain list they are the person's own for this project.
function saveColumns(order: ColumnId[], visible: ColumnId[]) {
  if (filters.value.view) { update({ cols: order.filter(id => visible.includes(id) && !PINNED.includes(id)) }); return }
  listPref.value?.save({ ...(listPrefs.value ?? {}), order, visible }, 0)
}
function resetColumns() {
  if (filters.value.cols) { update({ cols: null }); return }
  const { order: _order, visible: _visible, ...rest } = listPrefs.value ?? {}
  listPref.value?.save(rest, 0)
}
function saveWidths(widths: Partial<Record<ColumnId, number>>) { listPref.value?.save({ ...(listPrefs.value ?? {}), widths }) }
const { density, set: setDensity } = useDensity()
const { headerGraph, ready: headerGraphReady, set: setHeaderGraph } = useHeaderGraph()
const glimpseActive = ref(false)
// A status change that met someone else's newer version opens the ticket to review it.
const list = useTicketList(projectId, filters, { review: row => openRow(row) })
const now = ref(Date.now())
// Sections own their views. The registry also supplies TG1's optional renderer.
type ViewMode = TicketView | 'journey' | 'knowledge'
const activeTicketView = computed(() => ticketView(route.query.view))
// Knowledge has its own address: /p/KEY/knowledge, and /p/KEY/knowledge/<type>/<slug> for one entry.
const knowledgeActive = computed(onKnowledge)
const knowledgeType = computed(() => isKnowledgeType(route.params.knowledgeType) ? route.params.knowledgeType : null)
const knowledgeSlug = computed(() => typeof route.params.slug === 'string' ? route.params.slug : '')
const knowledgeEntryOpen = computed(() => knowledgeActive.value && !!knowledgeType.value && !!knowledgeSlug.value)
const knowledgeFilters = computed(() => knowledgeFiltersFrom(route.query))
// Keep the chosen view when closing an entry or editing its filters.
const knowledgeView = computed(() => route.query.view === 'graph' ? 'graph' : 'entries')
const knowledgeDisplay = computed(() => ({ view: knowledgeView.value }))
// KG2 owns the Knowledge implementation. Adapt its legacy route reads locally;
// the actual address and all writes use ?view=entries|graph. Remove this bridge
// when KnowledgeTab and its descendants consume the new query directly.
provide(routeLocationKey, reactive({ ...toRefs(route), query: computed(() => knowledgeActive.value
  ? { ...route.query, mode: knowledgeView.value === 'graph' ? 'graph' : null } : route.query) }))
// Older child components open bare ticket links. Enrich their push only (never
// browser Back/Forward or incoming bookmarks), preserving the current section.
provide(routerKey, { ...router, push: (to: RouteLocationRaw) => {
  const target = router.resolve(to)
  const sameProject = target.params.projectKey === projectKey.value ||
    projects.byRouteKey(String(target.params.projectKey))?.id === projectId.value
  if (!target.params.ticketKey || !sameProject || target.query.section !== undefined || target.query.view !== undefined) return router.push(to)
  const query: typeof route.query = { ...listQuery(), ...target.query, ...ticketSectionQuery() }
  delete query.entry
  return router.push({ ...(typeof to === 'string' ? { path: target.path, hash: target.hash } : to), query })
} })
const knowledgeListQuery = computed(() => ({ ...knowledgeDisplay.value, ...knowledgeQuery(knowledgeFilters.value) }))
const knowledge = useKnowledge(projectId, knowledgeFilters, knowledgeActive)
const knowledgeTab = ref<InstanceType<typeof KnowledgeTabType>>()
const knowledgeEntry = ref<InstanceType<typeof KnowledgeEntryPageType>>()
// Wide screens dock an entry beside the list (?entry=<type>/<slug>); narrower ones open its own page.
const dockQuery = window.matchMedia(DOCK_MEDIA)
const knowledgeWide = ref(dockQuery.matches)
const onDockWidth = (event: MediaQueryListEvent) => { knowledgeWide.value = event.matches }
dockQuery.addEventListener('change', onDockWidth)
const dockEntry = computed(() => knowledgeActive.value && !knowledgeEntryOpen.value ? parseEntryParam(route.query.entry) : null)
const knowledgeDocked = computed(() => knowledgeWide.value && !!dockEntry.value)
// The entry on show: its own page, or docked. One component either way, so Expand keeps it.
const shownEntry = computed<{ type: KnowledgeType; slug: string; mode: 'page' | 'dock' } | null>(() => {
  if (knowledgeEntryOpen.value && knowledgeType.value) return { type: knowledgeType.value, slug: knowledgeSlug.value, mode: 'page' }
  const docked = knowledgeDocked.value ? dockEntry.value : null
  return docked ? { ...docked, mode: 'dock' } : null
})
// A docked link on a narrow screen (shared, or the window got narrower) opens the entry's page.
// The graph keeps its selection there instead, shown in its own card.
watch([dockEntry, knowledgeWide], ([entry]) => {
  // The same width the route guard reads. The cached flag can lag a resize.
  if (entry && !window.matchMedia(DOCK_MEDIA).matches && knowledgeView.value !== 'graph') void router.replace({ path: entryPath(routeKey.value, entry.type, entry.slug), query: knowledgeListQuery.value, hash: route.hash })
}, { immediate: true })
const fullViewQuery = computed(() => !!ticketKey.value && (route.query.panel === 'full' || route.query.view === 'full'))
const viewMode = computed<ViewMode>(() => section.value === 'tickets' ? activeTicketView.value.id as TicketView : section.value)
const journeyActive = computed(() => section.value === 'journey')
// The journey's own place: the stage looked at, a chosen release and the walker's ticket.
const JOURNEY_KEYS = ['stage', 'release', 'walk'] as const
const queryText = (value: unknown) => typeof value === 'string' && value ? value : null
const journeyStage = computed(() => queryText(route.query.stage))
const journeyRelease = computed(() => queryText(route.query.release))
const journeyWalk = computed(() => queryText(route.query.walk))
function journeyQuery(patch: Partial<Record<typeof JOURNEY_KEYS[number], string | null>> = {}) {
  const query: Record<string, string> = ticketKey.value ? { section: 'journey' } : {}
  for (const key of JOURNEY_KEYS) {
    const value = key in patch ? patch[key] : queryText(route.query[key])
    if (value) query[key] = value
  }
  return query
}
function journeyStageTo(stage: Stage) { void router.push({ path: ticketKey.value ? route.path : sectionPath('journey'), query: journeyQuery({ stage, walk: null }) }) }
function journeyReleaseTo(key: string | null) { void router.replace({ path: route.path, query: journeyQuery({ release: key }) }) }
function journeyWalkTo(key: string | null, mode: 'open' | 'move' | 'close') {
  if (mode === 'open') { void router.push({ path: route.path, query: journeyQuery({ walk: key }) }); return }
  if (mode === 'move') { void router.replace({ path: route.path, query: journeyQuery({ walk: key }) }); return }
  if (typeof window.history.state?.back === 'string' && /(?:\/journey|section=journey)/.test(window.history.state.back) && !window.history.state.back.includes('walk=')) router.back()
  else void router.replace({ path: route.path, query: journeyQuery({ walk: null }) })
}
const graphActive = computed(() => viewMode.value === 'graph')
const graphState = ref<TicketGraphState>({ data: { nodes: [], links: [], truncated: false }, visible: { nodes: [], links: [], truncated: false }, loading: true })
const ticketGraphView = ref<{ focus: () => void }>()
const outlineActive = computed(() => viewMode.value === 'outline')
const outline = useOutline(projectId, filters, outlineActive, list)
// Changes by others, live (AEON-326): the list patches rows in place and holds
// structural updates behind the "N updates · Show" pill until it is safe.
const listActive = computed(() => section.value === 'tickets' && viewMode.value === 'list')
const liveList = useLiveList({
  projectId, filters, rows: list.rows, loading: list.loading, loads: list.loads, loadedOnce: list.loadedOnce,
  more: () => !!list.cursor.value, active: listActive, me: () => session.identity?.principal.id ?? null,
  quiet: id => !!ticketKey.value && panelItem.value?.id === id,
  blockers: () => ({
    selected: selected.value.size + (phonePicking.value ? 1 : 0),
    editing: creating.value || !!outline.createUnder.value || !!panel.value?.busy() || !!panel.value?.isDirty() || !!table.value?.createDirty(),
    menuOpen: !!statusMenu.value || !!bulkMenu.value || !!savePanel.value || !!document.querySelector('.floating'),
    dialogOpen: !!document.querySelector('dialog[open]'),
    dragging: false,
  }),
  around: aroundLiveApply, applied: liveApplied, reload: () => void list.load(),
})

const toolbarWrap = ref<HTMLElement>()
const stickMark = ref<HTMLElement>()
const toolbar = ref<InstanceType<typeof ListToolbar>>()
const table = ref<InstanceType<typeof TicketTable>>()
const panel = ref<InstanceType<typeof TicketWorkspace>>()
const filterSheet = ref<InstanceType<typeof FilterSheet>>()
const scrollRoot = ref<HTMLElement | null>(null)
const toolbarHeight = ref(52)
const stuck = ref(false)
const cursorId = ref<string | null>(null)
const collapsed = ref(new Set<string>())
const statusMenu = ref<{ row: ListItem; anchor: HTMLElement; from: 'list' | 'panel' } | null>(null)
const creating = ref(false)
// Full page: the same ticket workspace in a two-column page instead of the side panel.
const fullView = fullViewQuery
const me = computed(() => session.identity ? { id: session.identity.principal.id, name: session.identity.principal.name } : null)
const writable = computed(() => can('nodes.write', projectId.value ?? undefined))
const nodeDeletable = computed(() => can('nodes.delete', projectId.value ?? undefined))
const nodeMovable = computed(() => can('nodes.move', projectId.value ?? undefined))
const relationLinkable = computed(() => can('relations.write', projectId.value ?? undefined))
const relationUnlinkable = computed(() => can('relations.delete', projectId.value ?? undefined))
const commentable = computed(() => can('comments.write', projectId.value ?? undefined))
const commentDeletable = computed(() => can('comments.delete', projectId.value ?? undefined))
const attachable = computed(() => can('attachments.write', projectId.value ?? undefined) && can('attachments.delete', projectId.value ?? undefined))
const knowledgeWritable = computed(() => can('knowledge.write', projectId.value ?? undefined))
const knowledgeDeletable = computed(() => can('knowledge.delete', projectId.value ?? undefined))

// ---------- Rows, groups and keyboard order ----------
const displayRows = computed(() => {
  const primary = effectiveSort(filters.value)[0]
  return primary?.field === 'state' ? orderByStatus(list.rows.value, primary.desc, liveList.layout) : list.rows.value
})
const rowsById = computed(() => new Map(list.rows.value.map(row => [row.id, row])))
const groups = computed(() => {
  const facet = groupFacet(filters.value.group)
  return groupRows(displayRows.value, filters.value.group, facet ? list.facetCounts(facet) : {}, { me: session.identity?.principal.id, layout: liveList.layout })
})
// Keyboard order: every visible row once (a ticket under two labels is visited once).
const sequence = computed(() => {
  if (outlineActive.value) return outline.rows.value
  const out: ListItem[] = []
  const seen = new Set<string>()
  const push = (row: ListItem) => { if (!seen.has(row.id)) { seen.add(row.id); out.push(row) } }
  for (const group of groups.value) {
    const epicRow = group.epic ? rowsById.value.get(group.epic.id) : undefined
    if (epicRow) push(epicRow)
    if (!collapsed.value.has(group.key)) group.rows.forEach(push)
  }
  return out
})
const total = computed(() => graphActive.value ? graphState.value.loading ? null : graphState.value.visible.nodes.length : totalFrom(list.facets.value))
const showAssignee = computed(() => (outlineActive.value ? outline.rows.value : list.rows.value).some(row => row.assignee))

// One source for the header: the project summary (work counts). It arrives with
// the project itself and refreshes after a status change.
const counts = computed(() => {
  const p = project.value
  return p ? { open: p.open, progress: p.in_progress, done: p.done, cancelled: p.cancelled, total: p.total, percent: p.percent } : null
})
// The skeleton holds about the height the first page will take, so the hint and
// footer below the table do not jump when rows arrive.
const expectedRows = computed(() => {
  const c = counts.value
  if (!c || filtered.value) return 8
  if (outlineActive.value) return 12
  return filters.value.showClosed ? c.total : c.open + c.progress
})
const knownStates = computed(() => Object.keys(list.facets.value.state ?? {}))
const filtered = computed(() => hasFilters(filters.value))

function options(dimension: Dimension) {
  if (graphActive.value) {
    const counts: Record<string, number> = {}
    for (const node of graphState.value.visible.nodes) {
      const value = dimension === 'status' ? node.status : dimension === 'priority' ? node.priority ?? 'none' : node.type
      counts[value] = (counts[value] ?? 0) + 1
    }
    return facetOptions(dimension, counts, filters.value[dimension], list.names).filter(option => dimension !== 'type' || option.value !== 'task')
  }
  return facetOptions(dimension, list.counts(dimension), filters.value[dimension], list.names, session.identity?.principal.id, { colors: list.colors, epics: list.epics.value })
}
function chipLabel(dimension: Dimension, value: string) {
  return valueLabel(dimension, value, { names: list.names, me: session.identity?.principal.id, epics: list.epics.value })
}
// What a menu needs before it opens: names, label counts or the project's epics.
const facetLoading = ref(false)
function needOptions(dimension: Dimension) {
  if (dimension === 'assignee') { void list.resolveNames(options('assignee').map(o => o.value)); return }
  if (dimension === 'epic') { facetLoading.value = true; void list.loadEpics().finally(() => { facetLoading.value = false }); return }
  const facet = dimension === 'tag' ? 'tag' : dimension === 'cost' ? 'cost_unit' : dimension === 'release' ? 'release' : null
  if (facet && !filters.value[dimension].length) { facetLoading.value = true; void list.requestFacet(facet).finally(() => { facetLoading.value = false }) }
}
function sheetOpened() {
  if (graphActive.value) return
  void list.resolveNames(options('assignee').map(o => o.value))
  void list.loadEpics()
  for (const facet of ['tag', 'cost_unit', 'release']) void list.requestFacet(facet)
}
// Chips name epics by title, so the epics load when an epic filter is on.
watch(() => filters.value.epic.length > 0 && !!projectId.value, on => { if (on) void list.loadEpics() }, { immediate: true })

// ---------- URL state ----------
function modeQuery() {
  return { view: route.query.view === 'full' && fullView.value ? 'full' : activeTicketView.value.id,
    ...(ticketKey.value ? ticketSectionQuery() : {}), ...(route.query.panel === 'full' ? { panel: 'full' } : {}) }
}
function update(patch: Partial<ListFilters>) {
  void router.replace({ path: route.path, query: { ...filtersToQuery({ ...filters.value, ...patch }), ...modeQuery() } })
}
// Remember each section's filters and view while moving around this project.
watch(section, () => { creating.value = false; openedFromList = false })
const sectionQueries: Partial<Record<ProjectSection, typeof route.query>> = {}
watch(projectKey, () => { for (const key of Object.keys(sectionQueries)) delete sectionQueries[key as ProjectSection] })
function setSection(id: string) {
  const target = PROJECT_SECTIONS.find(item => item.id === id)?.id
  if (!target || target === section.value) return
  const { section: _section, panel: _panel, entry: _entry, ...query } = route.query
  sectionQueries[section.value] = query
  const saved = sectionQueries[target] ?? {}
  void router.push({ path: ticketKey.value ? ticketPath(ticketKey.value) : sectionPath(target),
    query: { ...saved, ...(ticketKey.value ? ticketSectionQuery(target) : {}) } })
}
watch([project, journeyActive, journeyStage], () => {
  const current = project.value
  if (!current) { flowPillContext.value = null; return }
  const active = journeyActive.value
  const stage = journeyStage.value
  flowPillContext.value = {
    projectId: current.id,
    active,
    open() {
      if (active) journeyStageTo((stage ?? 'inspire') as Stage)
      else setSection('journey')
    },
  }
}, { immediate: true })
let viewIntent = 0
async function setView(view: string) {
  const intent = ++viewIntent, within = projectKey.value, sectionAtClick = section.value
  // Filter writes can overlap the session guard. Retry a cancelled navigation
  // against the settled query, retaining both the new filter and latest view.
  while (intent === viewIntent && within === projectKey.value && sectionAtClick === section.value) {
    const query: typeof route.query = { ...route.query, view }
    // Entries cannot dock on a phone; a graph selection should not open a page.
    if (knowledgeActive.value && view === 'entries' && !window.matchMedia(DOCK_MEDIA).matches) delete query.entry
    const settled = settledNavigation(router)
    let failure
    try { failure = await router.replace({ path: route.path, query }) }
    catch (error) { settled.stop(); throw error }
    if (isNavigationFailure(failure, NavigationFailureType.cancelled)) await settled.promise
    else { settled.stop(); return }
  }
}
function toggleValue(dimension: Dimension, value: string) {
  const next = toggleIn(filters.value[dimension], value)
  const adding = next.includes(value)
  const patch: Partial<ListFilters> = { [dimension]: next }
  // Choosing a closed status while closed tickets are hidden would show nothing.
  if (dimension === 'status' && adding && statusMeta(value).closed && !filters.value.showClosed) patch.showClosed = true
  update(patch)
}
function excludeValue(dimension: Dimension, value: string) { update({ [dimension]: toggleOut(filters.value[dimension], value) }) }
function clearFilters() { update(clearedFilters()) }
function setDate(date: DateFilter | null) { update({ date }) }
function sortBy(field: SortField, additive: boolean) { update({ sort: cycleSort(filters.value.sort, field, additive) }) }
function setSort(sort: SortKey[]) { update({ sort }) }
function setGroup(group: GroupBy) {
  collapsed.value = new Set()
  if (group === 'tag') void list.requestFacet('tag')
  update({ group })
}
function setAllGroups(open: boolean) { collapsed.value = open ? new Set() : new Set(groups.value.map(group => group.key)) }
watch(() => filters.value.group === 'tag' && list.loadedOnce.value, on => { if (on) void list.requestFacet('tag') })
function toggleGroup(key: string) {
  const next = new Set(collapsed.value)
  if (next.has(key)) next.delete(key); else next.add(key)
  collapsed.value = next
}

// ---------- Saved views ----------
const views = computed(() => viewsOf(projectId.value))
const activeView = computed(() => filters.value.view ? views.value.items.find(view => view.id === filters.value.view) ?? null : null)
const viewFilters = computed(() => activeView.value ? filtersFromView(activeView.value) : null)
const customised = computed(() => hasFilters(filters.value) || filters.value.sort.length > 0 || filters.value.group !== 'none' || !!filters.value.cols || filters.value.showClosed)
const viewDirty = computed(() => !!viewFilters.value && !sameListState(filters.value, viewFilters.value))
const canSaveView = computed(() => !journeyActive.value && !knowledgeActive.value && (activeView.value ? viewDirty.value : customised.value))
// The saved-view strip shows once there is a view to pick or a list worth keeping; the plain list alone needs no strip.
const showViewBar = computed(() => !journeyActive.value && !knowledgeActive.value && !graphActive.value && (views.value.items.length > 0 || !!activeView.value || canSaveView.value))
const defaultViewId = computed(() => listPrefs.value?.defaultView ?? null)
function viewQuery(view: SavedView | null): Record<string, string> {
  return view ? filtersToQuery(filtersFromView(view)) : {}
}
function hrefFor(id: string | null) {
  const view = id ? views.value.items.find(item => item.id === id) ?? null : null
  const query = new URLSearchParams({ ...viewQuery(view), ...(outlineActive.value ? { view: 'outline' } : {}) }).toString()
  return `${sectionPath('tickets')}${query ? `?${query}` : ''}`
}
function openView(id: string | null, replace = false) {
  const view = id ? views.value.items.find(item => item.id === id) ?? null : null
  collapsed.value = new Set()
  const query = { ...viewQuery(view), ...(outlineActive.value ? { view: 'outline' } : {}) }
  const location = { path: sectionPath('tickets'), query }
  if (replace) void router.replace(location); else void router.push(location)
}
// A link to a view someone cannot see (private, or deleted) keeps its filters.
watch([() => filters.value.view, () => views.value.loaded], ([id, loaded]) => {
  if (id && loaded && !views.value.items.some(view => view.id === id)) update({ view: null })
})
// The project opens with the person's default view when nothing else was asked for.
// The list waits for that answer, so it never shows the plain list first.
const entryResolved = ref(false)
let resolvedFor = ''
watch(projectId, async id => {
  if (!id || resolvedFor === id) return
  resolvedFor = id
  const plain = () => section.value === 'tickets' && Object.keys(route.query).every(key => key === 'view') && (route.query.view === undefined || route.query.view === 'list' || route.query.view === 'outline')
  // Knowledge has no ticket views: its address stays as it is.
  if (!plain() || ticketKey.value || section.value !== 'tickets') { entryResolved.value = true; void loadViews(id); return }
  entryResolved.value = false
  const pref = usePreference<ListPrefs>(`list:${id}`)
  await Promise.race([Promise.all([loadViews(id), pref.ready]), new Promise(resolve => setTimeout(resolve, 1500))])
  if (projectId.value !== id) return
  const target = pref.value.value?.defaultView
  const view = target ? viewsOf(id).items.find(item => item.id === target) : null
  if (view && plain()) await router.replace({ path: route.path, query: { ...viewQuery(view), ...modeQuery() } })
  entryResolved.value = true
}, { immediate: true })

const queryKey = computed(() => projectId.value && entryResolved.value ? JSON.stringify(apiParams(projectId.value, filters.value)) : '')
// The Outline without filters loads its own levels; the list query then only supplies
// counts. With filters or Hide closed, the Outline needs the list's whole match set.
const listLoadMode = computed(() => graphActive.value ? 'graph' : journeyActive.value || knowledgeActive.value ? 'counts' : !outlineActive.value ? 'list' : outline.matchMode.value ? 'all' : 'counts')
watch([queryKey, listLoadMode], async ([value, mode], old) => {
  if (!value || mode === 'graph') return
  // Switching views on the same query reuses the rows already loaded.
  const sameQuery = !!old && old[0] === value
  if (sameQuery && old![1] !== 'counts' && old![1] !== 'graph' && mode !== 'counts') { if (mode === 'all') void list.loadAll(); return }
  const load = list.load({ pageSize: mode === 'counts' ? 1 : 200 })
  if (mode === 'all') void load.then(() => list.loadAll())
  if (sameQuery) return
  // A new query starts at its first row, with the project header still out of view.
  if (old && scrollRoot.value && toolbarWrap.value && scrollRoot.value.scrollTop > toolbarWrap.value.offsetTop) {
    scrollRoot.value.scrollTop = toolbarWrap.value.offsetTop
  }
}, { immediate: true })

// ---------- Side panel ----------
const fetched = ref<ListItem | null>(null)
const panelError = ref('')
const panelLoading = ref(false)
let panelGeneration = 0
const panelItem = computed(() => {
  const key = ticketKey.value.toLowerCase()
  if (!key) return null
  return list.rows.value.find(row => row.key.toLowerCase() === key) ?? outline.findByKey(key) ?? (fetched.value?.key.toLowerCase() === key ? fetched.value : null)
})
const panelPosition = computed(() => {
  if (graphActive.value) return null // Graph selection has no list-page order.
  const item = panelItem.value
  if (!item) return null
  const index = sequence.value.findIndex(row => row.id === item.id)
  // While more pages exist, count the whole list (its total), not just what is loaded.
  const count = list.cursor.value && total.value ? Math.max(total.value, sequence.value.length) : sequence.value.length
  return index === -1 ? null : { index, count }
})
async function resolvePanel() {
  const key = ticketKey.value
  const within = projectId.value
  panelError.value = ''
  if (!key || !within) return
  if (list.rows.value.some(row => row.key.toLowerCase() === key.toLowerCase()) || fetched.value?.key.toLowerCase() === key.toLowerCase()) return
  const request = ++panelGeneration
  panelLoading.value = true
  try {
    const result = await listNodes({ within, q: key, sort: 'key', limit: 50 })
    if (request !== panelGeneration) return
    const hit = result.items.find(item => item.key.toLowerCase() === key.toLowerCase())
    if (hit) fetched.value = hit
    else if (!list.rows.value.some(row => row.key.toLowerCase() === key.toLowerCase())) panelError.value = `${key.toUpperCase()} is not part of ${project.value?.title ?? 'this project'}.`
  } catch (e) {
    if (request === panelGeneration) panelError.value = e instanceof Error ? e.message : 'The ticket could not be loaded.'
  } finally {
    if (request === panelGeneration) panelLoading.value = false
  }
}
watch([ticketKey, projectId], resolvePanel, { immediate: true })
watch(panelItem, item => {
  if (!item) return
  if (outlineActive.value) outline.reveal(item.id)
  if (sequence.value.some(row => row.id === item.id)) {
    cursorId.value = item.id
    if (!fullView.value) void nextTick(() => table.value?.scrollToRow(item.id))
  }
})
const nativeReleases = ref(new Map<string, NativeReleaseView>())
const membershipIds = computed(() => {
  const ids: string[] = []
  const seen = new Set<string>()
  const project = projectId.value
  const push = (row: { id: string; kind_slug: string; project?: { id: string } | null } | null | undefined) => {
    if (!row || row.kind_slug === 'epic' || seen.has(row.id)) return
    // A project change can render before the previous list is replaced.
    if (project && row.project?.id && row.project.id !== project) return
    seen.add(row.id)
    ids.push(row.id)
  }
  for (const row of sequence.value) push(row)
  push(panelItem.value)
  return ids
})
let membershipGeneration = 0
async function refreshMemberships() {
  const project = projectId.value
  const ids = membershipIds.value
  const request = ++membershipGeneration
  if (!project || !ids.length) {
    if (request === membershipGeneration) nativeReleases.value = new Map()
    return
  }
  const views = await listNativeMemberships(project, ids)
  if (request !== membershipGeneration || projectId.value !== project) return
  nativeReleases.value = views
}
watch(membershipIds, () => { void refreshMemberships() }, { immediate: true })

// ---------- Live updates: applying keeps the panel, cursor, scroll anchor and focus ----------
// The first row still in view that stays, and where it is on screen.
function scrollAnchor(removing: Set<string>) {
  const root = scrollRoot.value, grid = table.value?.el
  // At the very top nothing is out of view: new rows show where they belong.
  if (!root || !grid || root.scrollTop <= 0) return null
  const top = toolbarWrap.value?.getBoundingClientRect().bottom ?? 0
  for (const tr of grid.querySelectorAll<HTMLElement>('tr.ticket-row[id^="row-"]')) {
    const id = tr.id.slice(4)
    if (removing.has(id)) continue
    const rect = tr.getBoundingClientRect()
    if (rect.bottom > top) return { id, top: rect.top }
  }
  return null
}
function aroundLiveApply(removing: Set<string>, apply: () => void) {
  // The panel keeps its ticket when the row leaves the list.
  const open = panelItem.value
  if (open && removing.has(open.id)) fetched.value = open
  if (cursorId.value && removing.has(cursorId.value)) {
    const order = sequence.value
    const at = order.findIndex(row => row.id === cursorId.value)
    const next = order.slice(at + 1).find(row => !removing.has(row.id)) ?? order.slice(0, Math.max(0, at)).reverse().find(row => !removing.has(row.id))
    cursorId.value = next?.id ?? null
  }
  const active = document.activeElement
  const focusLeaves = !!active?.closest?.('tr.ticket-row') && removing.has(active.closest('tr.ticket-row')!.id.slice(4))
  const anchor = scrollAnchor(removing)
  apply()
  void nextTick(() => {
    const root = scrollRoot.value
    const row = anchor && document.getElementById(`row-${anchor.id}`)
    if (root && row) root.scrollTop += row.getBoundingClientRect().top - anchor!.top
    if (focusLeaves) table.value?.focusGrid()
  })
}
// Counts in the toolbar and the header follow, once a burst has settled.
let liveCounts: ReturnType<typeof setTimeout> | undefined
function liveApplied() {
  clearTimeout(liveCounts)
  liveCounts = setTimeout(() => { void list.refreshCounts(); void projects.load(true) }, 300)
}
onBeforeUnmount(() => clearTimeout(liveCounts))
function showUpdates() {
  liveList.apply()
  if (!fullView.value && !panel.value?.el?.contains(document.activeElement)) table.value?.focusGrid()
}

// People who can be assigned: everyone assigned somewhere in this project, and you.
const projectPeople = ref<string[]>([])
watch(projectId, async id => {
  projectPeople.value = []
  if (!id) return
  try {
    const page = await listNodes({ within: id, kind: WORK_KINDS, facets: ['assignee'], limit: 1 })
    const ids = Object.keys(page.facets?.assignee ?? {}).filter(value => value !== 'none')
    await list.resolveNames(ids)
    if (projectId.value === id) projectPeople.value = ids
  } catch { /* the menu still offers you and Unassigned */ }
}, { immediate: true })
const people = computed(() => projectPeople.value.map(id => ({ id, name: list.names.get(id) ?? 'Someone' })))

let openedFromList = false
let openedQuery = ''
function ticketPath(key: string) { return `/p/${encodeURIComponent(routeKey.value)}/${encodeURIComponent(key)}` }
// List navigation (open from the list, j/k, next/previous) replaces the open ticket
// and clears the back trail; following a link inside the panel pushes a step.
function openKey(key: string) {
  const location = { path: ticketPath(key), query: { ...route.query, ...ticketSectionQuery() }, state: { trail: [] } }
  if (ticketKey.value) { void router.replace(location); return }
  openedFromList = true
  openedQuery = JSON.stringify(location.query)
  void router.push(location)
}
function openRow(row: ListItem) { cursorId.value = row.id; openKey(row.key) }
function listQuery() {
  const { panel: _panel, section: _section, ...query } = route.query
  if (ticketKey.value && query.view === 'full') delete query.view
  return query
}
function closePanel() {
  if (!ticketKey.value) return
  const back = !fullView.value && openedFromList && JSON.stringify(route.query) === openedQuery && typeof window.history.state?.back === 'string'
  openedFromList = false
  // Back past every followed link to the list entry the panel was opened from.
  if (back) router.go(-(trail.value.length + 1))
  else void router.replace({ path: sectionPath(), query: listQuery() })
  void nextTick(() => graphActive.value ? ticketGraphView.value?.focus() : table.value?.focusGrid())
}
let expandedFromPanel = false
let listScroll = 0
function expand() {
  if (!ticketKey.value || fullView.value) return
  expandedFromPanel = true
  const query = section.value === 'tickets' && route.query.view === undefined
    ? { ...route.query, view: 'full' }
    : { ...route.query, panel: 'full' }
  void router.push({ path: route.path, query })
}
// The full page starts at its top; going back returns to the same place in the list.
watch(fullView, async (full, was) => {
  const root = scrollRoot.value
  if (!root) return
  if (full && !was) { listScroll = root.scrollTop; await nextTick(); root.scrollTop = 0 }
  else if (!full && was) { await nextTick(); root.scrollTop = listScroll; if (panelItem.value) table.value?.scrollToRow(panelItem.value.id) }
})
function collapse() {
  if (!fullView.value) return
  if (expandedFromPanel && typeof window.history.state?.back === 'string') router.back()
  else void router.replace({ path: route.path, query: { ...listQuery(), ...ticketSectionQuery() } })
  expandedFromPanel = false
}
// ---------- Back trail: links followed inside the panel ----------
const trail = ref<string[]>([])
function readTrail() {
  const saved = window.history.state?.trail
  trail.value = Array.isArray(saved) ? saved.filter((key): key is string => typeof key === 'string') : []
}
watch(() => route.fullPath, readTrail, { immediate: true })
function follow(path: string) {
  const current = panelItem.value?.key ?? ticketKey.value.toUpperCase()
  void router.push({ path, query: { ...route.query, ...ticketSectionQuery() }, state: { trail: [...trail.value, current] } })
}
function trailBack(steps = 1) { if (trail.value.length) router.go(-Math.min(steps, trail.value.length)) }
// Related tickets can live in another project: open them where they belong.
async function openRelated(key: string, newTabRequested = false) {
  if (newTabRequested) { newTab(key); return }
  const here = key.split('-')[0] === routeKey.value || list.rows.value.some(row => row.key === key)
  if (!here) {
    try {
      const page = await listNodes({ q: key, limit: 25 })
      const owner = page.items.find(item => item.key === key)?.project
      const target = owner ? projects.byId(owner.id) : undefined
      if (target && target.id !== projectId.value) { follow(`/p/${encodeURIComponent(target.routeKey)}/${encodeURIComponent(key)}`); return }
    } catch { /* fall back to this project, where the panel explains */ }
  }
  if (ticketKey.value) follow(ticketPath(key)); else openKey(key)
}
watch(ticketKey, key => { if (!key) openedFromList = false })

// ---------- Actions ----------
function copyKey(key: string) {
  navigator.clipboard.writeText(key).then(() => toast(`Copied ${key}`), () => toast(`${key} could not be copied`, { tone: 'error' }))
}
function newTab(key: string) {
  const project = projects.projects.find(p => key.startsWith(`${p.routeKey}-`))
  const path = project ? `/p/${encodeURIComponent(project.routeKey)}/${encodeURIComponent(key)}` : ticketPath(key)
  const query = !project || project.id === projectId.value ? { ...listQuery(), ...ticketSectionQuery() } : {}
  window.open(router.resolve({ path, query }).href, '_blank', 'noopener')
}
function openStatus(row: ListItem, anchor: HTMLElement, from: 'list' | 'panel') {
  statusMenu.value = statusMenu.value?.row.id === row.id && statusMenu.value.from === from ? null : { row, anchor, from }
}
function closeStatus(restore: boolean) {
  const menu = statusMenu.value
  statusMenu.value = null
  if (restore) menu?.anchor.focus()
}
function chooseStatus(state: string) {
  const menu = statusMenu.value
  statusMenu.value = null
  if (!menu) return
  void list.setStatus(menu.row, state).then(changed => { if (changed) { void projects.load(true); outline.refreshStatsFor(menu.row.id) } })
  if (menu.from === 'panel') menu.anchor.focus()
  else table.value?.focusGrid()
}
function openEpic(epic: EpicRef) { openKey(epic.key) }

// ---------- Create and remove ----------
async function startCreate(under: ListItem | null = null) {
  if (fullView.value) collapse()
  if (under && outlineActive.value) {
    creating.value = false
    outline.startCreateUnder(under.id)
    await nextTick(); table.value?.focusCreate()
    return
  }
  outline.startCreateUnder(null)
  creating.value = true
  if (scrollRoot.value && toolbarWrap.value && scrollRoot.value.scrollTop > toolbarWrap.value.offsetTop) scrollRoot.value.scrollTop = toolbarWrap.value.offsetTop
  await nextTick(); table.value?.focusCreate()
}
async function quickCreate(draft: QuickDraft): Promise<boolean> {
  const current = project.value
  if (!current) return false
  try {
    const kind = (await kinds()).find(candidate => candidate.slug === draft.kind)
    if (!kind) throw new Error('this workspace has no such type')
    let fields: Record<string, unknown> = draft.priority ? { priority: draft.priority } : {}
    if (needsBenefitPrompt({ kind_slug: draft.kind, state: 'new', fields }, draft.state)) {
      const text = await askDoneGate({ key: 'New ticket', title: draft.title, state: draft.state, fields })
      if (!text) return false
      fields = completionFields(fields, text)
    }
    const node = await createNode({
      kind_id: kind.id, title: draft.title, state: draft.state, fields,
      parent_id: draft.epic?.id ?? current.id, key_prefix: keyPrefix(current.routeKey),
    })
    const parent = draft.epic ? { ...draft.epic, kind_slug: 'epic' } : { id: current.id, key: current.key, title: current.title, kind_slug: 'project' }
    const created = asListItem(node, kind, parent, { id: current.id, key: current.key, title: current.title })
    list.insertRow(created)
    outline.insert(created)
    cursorId.value = created.id
    toast(`Created ${node.key}`, { action: { label: 'Open', run: () => openKey(node.key) } })
    void projects.load(true)
    return true
  } catch (e) {
    if (benefitGateError(e)) {
      toast('The ticket was not created. Add a 2–4 word pill and a benefit in both languages.', { tone: 'error' })
      return false
    }
    toast(`The ticket was not created: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
    return false
  }
}
function childCreated(item: ListItem) { list.insertRow(item); outline.insert(item); void projects.load(true) }
function childMoved(item: ListItem, fromParent: string | null) {
  outline.relocate(item, fromParent, fromParent && outline.node(fromParent)?.kind_slug === 'epic' ? fromParent : null)
}
function closeCreate() { creating.value = false; outline.startCreateUnder(null) }
// Drag and drop in the Outline: the same guarded move as the workspace's "Move to another epic".
async function moveRow(row: ListItem, epic: ListItem | null) {
  const current = project.value
  if (!current) return
  const parent = epic ? { id: epic.id, key: epic.key, title: epic.title, kind_slug: 'epic' } : { id: current.id, key: current.key, title: current.title, kind_slug: 'project' }
  if (await guardedMove(row, parent, childMoved) === 'ok') {
    if (epic) outline.setExpanded(epic.id, true)
    cursorId.value = row.id
  }
}
let skipGuard = false
function removed(item: ListItem) {
  outline.remove(item)
  list.removeRow(item.id)
  void projects.load(true)
  skipGuard = true
  void router.replace({ path: sectionPath(), query: listQuery() }).finally(() => { skipGuard = false })
}

// Commands from the palette: New ticket in this project.
watch(command, async value => {
  const next = value?.command
  if (next?.name === 'new-knowledge' && project.value && next.projectKey === project.value.routeKey) {
    consume()
    if (!knowledgeActive.value || knowledgeEntryOpen.value) await router.push({ path: `/p/${encodeURIComponent(routeKey.value)}/knowledge`, query: knowledgeListQuery.value })
    setTimeout(() => knowledgeTab.value?.openCreate(), 0)
    return
  }
  if (next?.name !== 'new-ticket' || !project.value || next.projectKey !== project.value.routeKey) return
  consume()
  if (ticketKey.value) closePanel()
  void nextTick(() => startCreate())
}, { immediate: true })
// Recently opened work and projects, for the palette.
watch(panelItem, item => {
  if (item && project.value) remember({ type: 'ticket', key: item.key, title: item.title, state: item.state, kind: item.kind_slug, projectKey: project.value.routeKey })
})
watch(project, current => { if (current) remember({ type: 'project', key: current.routeKey, title: current.title }) }, { immediate: true })

// ---------- Knowledge: the list's place in the URL, and closing an entry ----------
// One write at a time. A later filter reads the address after the earlier one has
// landed, and a replace that lost to another navigation is sent once more.
let knowledgeWrite: Promise<unknown> = Promise.resolve()
function learningAccepted(entry: KnowledgeEntry) {
  knowledge.upsert(entry)
  knowledgeEntry.value?.applyServer(entry)
}
function learningReverted() {
  void knowledge.load()
  void knowledgeEntry.value?.reload()
}
function updateKnowledge(patch: Partial<KnowledgeFilters>) {
  knowledgeWrite = knowledgeWrite.catch(() => undefined).then(async () => {
    const go = () => {
      const entry = typeof route.query.entry === 'string' ? { entry: route.query.entry } : {}
      return router.replace({ path: route.path, query: { ...knowledgeDisplay.value, ...knowledgeQuery({ ...knowledgeFilters.value, ...patch }), ...entry, ...(ticketKey.value ? { section: 'knowledge' } : {}) } })
    }
    let retriedAbort = false
    while (knowledgeActive.value && !knowledgeEntryOpen.value) {
      const settled = settledNavigation(router)
      let failure
      try { failure = await go() } catch (error) { settled.stop(); throw error }
      if (isNavigationFailure(failure, NavigationFailureType.cancelled)) await settled.promise
      else settled.stop()
      if (isNavigationFailure(failure, NavigationFailureType.cancelled)) continue
      if (isNavigationFailure(failure, NavigationFailureType.aborted) && !retriedAbort) { retriedAbort = true; continue }
      return
    }
  })
}
// The next navigation's outcome: true once it has landed, false when a guard kept the page.
function landed() {
  return new Promise<boolean>(resolve => { const stop = router.afterEach((_to, _from, failure) => { stop(); resolve(!failure) }) })
}
// The row for the address, not the entry the pane has finished loading. j and k
// change the address at once; the pane keeps the previous entry until its fetch
// returns, and under load that is still in flight when Esc is pressed.
function dockedRowId(): string | null {
  const open = dockEntry.value
  if (!open) return null
  return knowledge.sequence.value.find(item => item.type === open.type && item.slug === open.slug)?.id ?? knowledgeEntry.value?.entryId() ?? null
}
// Closing the docked entry: back to the list it was opened from, the row selected.
async function closeKnowledgeDock() {
  const id = dockedRowId()
  const list = { path: `/p/${encodeURIComponent(routeKey.value)}/knowledge`, query: knowledgeListQuery.value }
  if (window.history.state?.back === router.resolve(list).fullPath) {
    const settled = settledNavigation(router)
    router.back()
    await settled.promise
  }
  // A graph selection or filter can overtake the close while the /me guard is
  // pending. Retry from the landed address so its display and filters survive.
  let retriedAbort = false
  while (route.path === list.path && route.query.entry) {
    const settled = settledNavigation(router)
    let failure
    try { failure = await router.replace({ path: list.path, query: knowledgeListQuery.value }) }
    catch (error) { settled.stop(); throw error }
    if (isNavigationFailure(failure, NavigationFailureType.cancelled)) await settled.promise
    else settled.stop()
    if (isNavigationFailure(failure, NavigationFailureType.cancelled)) continue
    if (isNavigationFailure(failure, NavigationFailureType.aborted) && !retriedAbort) { retriedAbort = true; continue }
    break
  }
  if (route.path !== list.path || route.query.entry) return
  await nextTick()
  if (id) knowledgeTab.value?.reveal(id, true)
}
let entryFromList = false
watch(() => route.fullPath, (_path, old) => {
  if (!knowledgeEntryOpen.value) { entryFromList = false; return }
  if (old && /\/knowledge(\?|$)/.test(old.split('#')[0]) && !/\/knowledge\/[^/]+\//.test(old)) entryFromList = true
})
async function closeKnowledgeEntry() {
  const id = knowledgeEntry.value?.entryId() ?? null
  const back = entryFromList && typeof window.history.state?.back === 'string' && /\/knowledge(\?|$)/.test(window.history.state.back.split('#')[0])
  const done = landed()
  if (back) router.back()
  else void router.push({ path: `/p/${encodeURIComponent(routeKey.value)}/knowledge`, query: knowledgeListQuery.value })
  if (!(await done)) return
  await nextTick()
  if (id) knowledgeTab.value?.reveal(id)
}

// ---------- Saved view actions ----------
const savePanel = ref<{ mode: 'create' | 'rename'; anchor: HTMLElement; view?: SavedView; name: string } | null>(null)
const saveBusy = ref(false)
const saveError = ref('')
const viewBar = ref<InstanceType<typeof ViewBar>>()
function startSave(anchor: HTMLElement) {
  saveError.value = ''
  const base = activeView.value ? copyName(activeView.value.name, views.value.items.map(v => v.name)) : suggestName(filters.value, chipLabel)
  savePanel.value = { mode: 'create', anchor, name: base }
}
function startRename(view: SavedView, anchor: HTMLElement) { saveError.value = ''; savePanel.value = { mode: 'rename', anchor, view, name: view.name } }
function closeSave(restore: boolean) {
  const anchor = savePanel.value?.anchor
  savePanel.value = null
  if (restore && anchor?.isConnected) anchor.focus()
}
function problem(e: unknown) { return e instanceof Error ? e.message : 'Something went wrong' }
async function submitSave(value: { name: string; shared: boolean; makeDefault: boolean }) {
  const panel = savePanel.value, id = projectId.value
  if (!panel || !id) return
  saveBusy.value = true; saveError.value = ''
  try {
    if (panel.mode === 'rename' && panel.view) {
      const saved = await renameView(id, panel.view, value.name)
      toast(`Renamed the view to “${saved.name}”`)
    } else {
      const saved = await saveNewView(id, value.name, { ...filters.value, view: null }, value.shared)
      if (value.makeDefault) listPref.value?.save({ ...(listPrefs.value ?? {}), defaultView: saved.id }, 0)
      update({ view: saved.id })
      toast(`Saved the view “${saved.name}”${saved.shared ? ', shared with the project' : ''}`)
    }
    closeSave(false)
  } catch (e) {
    saveError.value = problem(e)
  } finally {
    saveBusy.value = false
  }
}
async function saveActive(view: SavedView) {
  const id = projectId.value
  if (!id) return
  try {
    await saveViewState(id, view, { ...filters.value, view: null })
    toast(`Saved the changes to “${view.name}”`)
  } catch (e) { toast(`The view was not saved: ${problem(e)}`, { tone: 'error' }) }
}
async function duplicate(view: SavedView) {
  const id = projectId.value
  if (!id) return
  try {
    const copy = await duplicateView(id, view, copyName(view.name, views.value.items.map(v => v.name)))
    openView(copy.id)
    toast(`Made “${copy.name}”, your own copy`)
  } catch (e) { toast(`The view was not copied: ${problem(e)}`, { tone: 'error' }) }
}
function setDefaultView(view: SavedView | null) {
  listPref.value?.save({ ...(listPrefs.value ?? {}), defaultView: view?.id ?? null }, 0)
  toast(view ? `${project.value?.title ?? 'The project'} opens with “${view.name}” for you` : `${project.value?.title ?? 'The project'} opens with all tickets again`)
}
async function share(view: SavedView, shared: boolean) {
  const id = projectId.value
  if (!id) return
  try {
    await shareView(id, view, shared)
    toast(shared ? `“${view.name}” is shared with everyone in ${project.value?.title ?? 'the project'}` : `“${view.name}” is private again`)
  } catch (e) { toast(`Sharing did not change: ${problem(e)}`, { tone: 'error' }) }
}
function copyViewLink(view: SavedView) {
  const url = new URL(hrefFor(view.id), window.location.origin).toString()
  navigator.clipboard.writeText(url).then(() => toast(`Copied the link to “${view.name}”`), () => toast('The link could not be copied', { tone: 'error' }))
}
async function remove(view: SavedView) {
  const id = projectId.value
  if (!id) return
  try {
    const restore = await removeView(id, view)
    if (filters.value.view === view.id) update({ view: null })
    toast(`Deleted the view “${view.name}”`, { timeout: 8000, action: { label: 'Undo', run: () => void restore().then(back => { toast(`“${back.name}” is back`) }, e => toast(`The view could not be brought back: ${problem(e)}`, { tone: 'error' })) } })
  } catch (e) { toast(`The view was not deleted: ${problem(e)}`, { tone: 'error' }) }
}

// ---------- Selection and bulk changes ----------
const selectable = computed(() => writable.value && !journeyActive.value && !knowledgeActive.value && !graphActive.value && !fullView.value)
const selected = ref(new Set<string>())
// Phone selection can be armed before the first card is chosen.
const phoneQuery = window.matchMedia('(max-width: 720px)')
const phone = ref(phoneQuery.matches)
const phonePicking = ref(false)
const picking = computed(() => phonePicking.value || selected.value.size > 0)
const onPhone = () => { phone.value = phoneQuery.matches; if (!phone.value) phonePicking.value = false }
let selectAnchor: string | null = null
function selectRow(row: ListItem, mode: 'toggle' | 'range') {
  const next = new Set(selected.value)
  const rows = sequence.value
  const from = selectAnchor ? rows.findIndex(item => item.id === selectAnchor) : -1
  const to = rows.findIndex(item => item.id === row.id)
  if (mode === 'range' && from !== -1 && to !== -1) {
    for (let i = Math.min(from, to); i <= Math.max(from, to); i++) next.add(rows[i].id)
  } else {
    if (next.has(row.id)) next.delete(row.id); else next.add(row.id)
    selectAnchor = row.id
  }
  selected.value = next
  if (phone.value && next.size) phonePicking.value = true
  cursorId.value = row.id
}
function selectAll(on: boolean) {
  selected.value = on ? new Set(sequence.value.map(row => row.id)) : new Set()
  selectAnchor = on ? sequence.value[0]?.id ?? null : null
  if (!on) phonePicking.value = false
  else if (phone.value) phonePicking.value = true
}
function clearSelection() { selected.value = new Set(); selectAnchor = null; phonePicking.value = false }
// Everything that matches, beyond the loaded pages (up to the bulk limit of 500).
async function selectAllMatching() {
  if (list.cursor.value) await list.loadAll(500)
  selectAll(true)
}
// Rows that left the list leave the selection.
watch(() => list.rows.value, rows => {
  if (!selected.value.size) return
  const ids = new Set(rows.map(row => row.id))
  const kept = [...selected.value].filter(id => ids.has(id))
  if (kept.length !== selected.value.size) selected.value = new Set(kept)
})
watch(selectable, on => { if (!on) clearSelection() })
const selectedRows = computed(() => list.rows.value.filter(row => selected.value.has(row.id)))
// Selected tickets someone deleted meanwhile: the bulk bar says so, and changes leave them out.
const selectedDeleted = computed(() => liveList.deletedAmong(selected.value))
const liveSelection = () => { const gone = new Set(selectedDeleted.value); return [...selected.value].filter(id => !gone.has(id)) }
// Where the list is, so the bulk bar centres over it (a docked panel takes the right).
const listFrame = ref<{ left: number; width: number } | null>(null)
let frameObserver: ResizeObserver | undefined
function measureFrame() {
  const el = (table.value as unknown as { $el?: HTMLElement } | undefined)?.$el
  if (!el) return
  const rect = el.getBoundingClientRect()
  listFrame.value = { left: Math.round(rect.left), width: Math.round(rect.width) }
}
watch(() => selected.value.size > 0, on => {
  frameObserver?.disconnect()
  window.removeEventListener('resize', measureFrame)
  if (!on) return
  measureFrame()
  const el = (table.value as unknown as { $el?: HTMLElement } | undefined)?.$el
  if (el) { frameObserver = new ResizeObserver(measureFrame); frameObserver.observe(el) }
  window.addEventListener('resize', measureFrame)
})
onBeforeUnmount(() => { frameObserver?.disconnect(); window.removeEventListener('resize', measureFrame) })
const bulkBusy = ref(false)
const bulkMenu = ref<{ kind: 'status' | 'assignee' | 'priority' | 'labels' | 'move' | 'release'; anchor: HTMLElement } | null>(null)
const releaseIds = ref<string[]>([])
function openBulk(kind: NonNullable<typeof bulkMenu.value>['kind'], anchor?: HTMLElement | null) {
  const at = anchor ?? document.querySelector<HTMLElement>(`.bulk-bar [aria-keyshortcuts="${{ status: 's', assignee: 'a', priority: 'p', labels: 'l', move: 'm', release: 'g' }[kind]}"]`)
  if (!at) return
  if (kind === 'release') { openRelease(at, liveSelection()); return }
  if (kind === 'labels') void list.requestFacet('tag')
  if (kind === 'move') void list.loadEpics()
  bulkMenu.value = { kind, anchor: at }
}
function closeBulk(restore: boolean) {
  const anchor = bulkMenu.value?.anchor
  bulkMenu.value = null
  if (restore) anchor?.focus()
}
const bulkPeople = computed(() => {
  const mine = me.value ? [{ value: me.value.id, label: me.value.name, hint: 'you' }] : []
  return [{ value: '', label: 'Unassigned' }, ...mine, ...people.value.filter(person => person.id !== me.value?.id).map(person => ({ value: person.id, label: person.name }))]
})
const bulkPriorities = [...PRIORITIES.map(p => ({ value: p.value as string, label: p.label as string })), { value: '', label: 'No priority' }]
const bulkLabels = computed<LabelChoice[]>(() => {
  const byName = new Map<string, LabelChoice>()
  for (const [name] of Object.entries(list.facetCounts('tag'))) if (name !== 'none' && !byName.has(name.toLowerCase())) byName.set(name.toLowerCase(), { name, color: list.colors.get(name.toLowerCase()) ?? '', on: 0 })
  for (const row of selectedRows.value) for (const tag of rowTags(row)) {
    const key = tag.name.toLowerCase()
    if (!byName.has(key)) byName.set(key, { name: tag.name, color: tag.color, on: 0 })
  }
  for (const choice of byName.values()) choice.on = selectedRows.value.filter(row => rowTags(row).some(tag => tag.name.toLowerCase() === choice.name.toLowerCase())).length
  return [...byName.values()].sort((a, b) => b.on - a.on || a.name.localeCompare(b.name))
})
// Review: the tickets that changed meanwhile become the selection, with their newer values.
function reviewConflicts(ids: string[]) {
  const shown = ids.filter(id => list.rows.value.some(row => row.id === id))
  if (!shown.length) return
  selected.value = new Set(shown)
  selectAnchor = shown[0]
  if (phone.value) phonePicking.value = true
  cursorId.value = shown[0]
  void nextTick(() => { table.value?.scrollToRow(shown[0]); table.value?.focusGrid() })
}
async function runBulk(change: Omit<BulkChange, 'ids'>, done: (count: number) => string, ids = [...selected.value]) {
  const gone = new Set(selectedDeleted.value)
  ids = ids.filter(id => !gone.has(id))
  if (!ids.length || bulkBusy.value) return
  bulkMenu.value = null
  bulkBusy.value = true
  try {
    const result = await list.applyBulk({ ids, ...change })
    // Changed meanwhile: nothing was overwritten; Review shows those tickets.
    const conflicts = result.skipped.filter(item => item.code === 'conflict')
    const reported = result.skipped.filter(item => !benefitSkip(item) && item.code !== 'conflict')
    const held = result.skipped.filter(item => benefitSkip(item))
    const changed = result.items.length
    const skipped = reported.length
    const eventId = result.event_id
    if (changed) {
      toast(`${done(changed)}${skipped ? ` · ${plural(skipped, 'ticket')} skipped` : ''}`, {
        timeout: 8000, action: eventId ? { label: 'Undo', run: () => void undoBulk(eventId) } : undefined,
      })
    } else if (!skipped && !held.length && !conflicts.length) toast(`Nothing to change: ${plural(result.unchanged.length, 'ticket is', 'tickets are')} already so`)
    if (conflicts.length) {
      const ids = conflicts.map(item => item.id)
      const which = conflicts.length === 1 ? `${conflicts[0].key ?? 'A ticket'} was` : `${plural(conflicts.length, 'ticket')} were`
      toast(`${which} changed elsewhere meanwhile and kept ${conflicts.length === 1 ? 'its' : 'their'} newer version.`, { tone: 'error', timeout: 10000, action: { label: 'Review', run: () => reviewConflicts(ids) } })
    }
    if (skipped) {
      const first = reported[0]
      toast(`${first.key ?? 'A ticket'} was skipped: ${first.reason}${skipped > 1 ? ` (and ${skipped - 1} more)` : ''}`, { tone: 'error' })
    }
    if (changed || conflicts.length) { void list.load(); void projects.load(true); for (const node of result.items) outline.refreshStatsFor(node.id) }
    if (change.state && held.length) await offerBenefitSkips(held, change.state)
  } catch (e) {
    toast(`Nothing was changed: ${problem(e)}`, { tone: 'error' })
  } finally {
    bulkBusy.value = false
    table.value?.focusGrid()
  }
}
async function stepBenefitGate(rows: ListItem[], state: string) {
  let done = 0
  const skipped: ListItem[] = []
  for (let index = 0; index < rows.length; index++) {
    const row = list.rows.value.find(item => item.id === rows[index].id) ?? rows[index]
    if (!needsBenefitPrompt(row, state)) {
      if (await list.setStatus(row, state)) { done++; void projects.load(true); outline.refreshStatsFor(row.id) }
      else skipped.push(row)
      continue
    }
    const text = await askDoneGate({ key: row.key, title: row.title, state, fields: row.fields ?? {} }, { index: index + 1, total: rows.length })
    if (!text) { skipped.push(row); continue }
    if (await list.setStatus(row, state, { fields: completionFields(row.fields, text) })) {
      done++
      void projects.load(true)
      outline.refreshStatsFor(row.id)
    } else skipped.push(row)
  }
  return { done, skipped }
}
function announceBenefitStep(done: number, skipped: ListItem[]) {
  if (!done && !skipped.length) return
  const still = skippedStatusLabel(skipped.map(row => statusMeta(row.state).label))
  const phone = window.matchMedia('(max-width: 700px)').matches
  toast(benefitStepSummary(done, skipped.length, still), phone ? { sticky: true, key: 'benefit-step' } : { timeout: 8000, key: 'benefit-step' })
}
async function offerBenefitSkips(held: BulkResult['skipped'], state: string) {
  const rows = held.flatMap(item => {
    const row = list.rows.value.find(entry => entry.id === item.id)
    return row ? [row] : []
  })
  if (!rows.length) return
  const outcome = await stepBenefitGate(rows, state)
  announceBenefitStep(outcome.done, outcome.skipped)
}
async function undoBulk(eventId: number) {
  try {
    await list.undoBulk(eventId)
    toast('Undone: the tickets are as they were')
    void list.load(); void projects.load(true); void refreshMemberships()
  } catch (e) {
    toast(e instanceof APIError && e.status === 409 ? 'Some of them changed since, so nothing was undone.' : `Undo did not work: ${problem(e)}`, { tone: 'error' })
  }
}
const count = (n: number) => plural(n, 'ticket')
async function bulkStatus(state: string) {
  if (bulkBusy.value) return
  bulkMenu.value = null
  const gated = selectedRows.value.filter(row => needsBenefitPrompt(row, state))
  const gatedIds = new Set(gated.map(row => row.id))
  const ready = [...selected.value].filter(id => !gatedIds.has(id))
  if (ready.length) await runBulk({ state }, n => `${count(n)} ${n === 1 ? 'is' : 'are'} now ${statusMeta(state).label}`, ready)
  if (!gated.length) return
  bulkBusy.value = true
  try {
    const outcome = await stepBenefitGate(gated, state)
    announceBenefitStep(outcome.done, outcome.skipped)
  } finally {
    bulkBusy.value = false
    table.value?.focusGrid()
  }
}
function bulkArchive() { void runBulk({ state: 'archived' }, n => `Archived ${count(n)}`) }
function bulkAssign(value: string) {
  const name = bulkPeople.value.find(person => person.value === value)?.label ?? 'someone'
  void runBulk({ assignee: value || null }, n => value ? `Assigned ${count(n)} to ${name}` : `Unassigned ${count(n)}`)
}
function bulkPriority(value: string) { void runBulk({ priority: value || null }, n => value ? `${count(n)} ${n === 1 ? 'is' : 'are'} now ${priorityLabel(value)} priority` : `Cleared the priority of ${count(n)}`) }
function bulkLabelsApply(change: { add: { name: string; color?: string }[]; remove: string[] }) {
  void runBulk({ tags_add: change.add, tags_remove: change.remove }, n => `Changed the labels of ${count(n)}`)
}
function bulkMove(epic: { id: string; key: string; title: string } | null) {
  const target = epic?.id ?? project.value?.id
  if (!target) return
  void runBulk({ parent_id: target }, n => epic ? `Moved ${count(n)} to ${epic.title}` : `Took ${count(n)} out of their epic`)
}
function openRelease(anchor: HTMLElement, ids: string[]) {
  if (!ids.length || !can('releases.write', project.value?.id)) return
  releaseIds.value = ids
  bulkMenu.value = { kind: 'release', anchor }
}
let releaseAttempt = 0
async function chooseRelease(target: ReleaseTarget) {
  const projectId = project.value?.id
  if (!projectId || bulkBusy.value) return
  const ids = [...releaseIds.value]
  const rows = list.rows.value.filter(row => ids.includes(row.id))
  const tickets = ids.map(id => {
    const row = rows.find(item => item.id === id)
    return { id, key: row?.key ?? id, title: row?.title ?? '', state: row?.state, kind: row?.kind_slug }
  })
  bulkMenu.value = null
  const attempt = ++releaseAttempt
  bulkBusy.value = true
  const here = () => attempt === releaseAttempt && project.value?.id === projectId
  try {
    const outcome = await assignToRelease(projectId, tickets, target)
    if (outcome.journey) useJourney().set(projectId, outcome.journey)
    if (!here()) return
    const changed = outcome.opened ? openedMembershipMessage(outcome.opened) : null
    if (changed) {
      toast(changed)
      void refreshMemberships()
      return
    }
    const joined = outcome.opened?.status === 'added'
      ? outcome.opened.count
      : outcome.result?.walker.tickets.filter(ticket => ids.includes(ticket.ticket_node_id) && ticket.included).length ?? 0
    if (!joined) {
      toast('Nothing was added to the release.')
      void refreshMemberships()
      return
    }
    const title = outcome.opened?.status === 'added' ? outcome.opened.releaseTitle : outcome.releaseTitle
    const leftOut = outcome.skipped.length ? ` · ${plural(outcome.skipped.length, 'ticket')} left out` : ''
    const eventId = outcome.result?.event_id
    toast(`Added ${plural(joined, 'ticket')} to ${title}${leftOut}`, {
      timeout: 8000,
      action: eventId ? { label: 'Undo', run: () => void undoBulk(eventId) } : undefined,
    })
    clearSelection()
    void list.load()
    void refreshMemberships()
  } catch (error) {
    if (!here()) return
    if (error instanceof AssignCancelled) return
    toast(problem(error), { tone: 'error' })
  } finally {
    if (attempt === releaseAttempt) {
      bulkBusy.value = false
      if (project.value?.id === projectId) table.value?.focusGrid()
    }
  }
}

// ---------- Unsaved changes ----------
function dirty() { return !!panel.value?.isDirty() || !!knowledgeEntry.value?.isDirty() }
async function confirmDiscard() {
  if (skipGuard || !dirty()) return true
  return confirmAction({ title: 'Discard unsaved changes?', body: 'You have edits in this ticket that are not saved yet.', confirmLabel: 'Discard changes', danger: true })
}
// Which knowledge entry an address shows (its page or the docked pane), so leaving
// an entry asks about unsaved edits, while Expand (same entry, now its page) does not.
function shownIn(location: { params: Record<string, unknown>; query: Record<string, unknown> }) {
  if (typeof location.params.slug === 'string') return `${String(location.params.knowledgeType)}/${location.params.slug}`
  return typeof location.query.entry === 'string' ? location.query.entry : ''
}
onBeforeRouteUpdate(async (to, from) => {
  if (projectSection(to) !== projectSection(from) && table.value?.createDirty()) {
    if (!await confirmAction({ title: 'Discard the new ticket?', body: 'Its title has not been created yet.', confirmLabel: 'Discard', danger: true })) return false
  }
  if (to.params.ticketKey !== from.params.ticketKey || to.params.projectKey !== from.params.projectKey) return confirmDiscard()
  if (shownIn(from) && shownIn(to) !== shownIn(from)) return confirmDiscard()
})
onBeforeRouteLeave(async to => {
  if (to.path === '/signin' && useSession().requiresSignIn) return true
  return (await confirmDiscard()) && (!table.value?.createDirty() || skipGuard || confirmAction({ title: 'Discard the new ticket?', body: 'Its title has not been created yet.', confirmLabel: 'Discard', danger: true }))
})
function beforeUnload(event: BeforeUnloadEvent) { if (dirty() || table.value?.createDirty()) { event.preventDefault(); event.returnValue = '' } }
// Tabbing into the table lands on a visible row, not on an invisible container.
function focusFirst() { if (!cursorId.value && sequence.value.length) cursorId.value = sequence.value[0].id }

// ---------- Keyboard ----------
function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}
async function move(step: number) {
  let rows = sequence.value
  if (!rows.length) return
  // At the end of what is loaded, fetch the next page first so j and Next keep going.
  const at = rows.findIndex(row => row.id === (ticketKey.value && panelItem.value ? panelItem.value.id : cursorId.value))
  if (step > 0 && at === rows.length - 1 && list.cursor.value && !outlineActive.value) {
    await list.loadMore()
    rows = sequence.value
  }
  // With a ticket open, move from that ticket; the cursor follows only once the move happens
  // (an unsaved-changes guard may keep the current ticket open).
  const from = ticketKey.value && panelItem.value ? panelItem.value.id : cursorId.value
  const index = rows.findIndex(row => row.id === from)
  const next = index === -1 ? (step > 0 ? 0 : rows.length - 1) : Math.max(0, Math.min(rows.length - 1, index + step))
  const row = rows[next]
  if (ticketKey.value) {
    openKey(row.key)
    if (!fullView.value && !panel.value?.el?.contains(document.activeElement)) table.value?.focusGrid()
    return
  }
  cursorId.value = row.id
  void nextTick(() => table.value?.scrollToRow(row.id))
  table.value?.focusGrid()
}
// Shift with j/k or the arrows grows the selection from the cursor row.
function extendSelection(step: number) {
  const rows = sequence.value
  const at = rows.findIndex(row => row.id === cursorId.value)
  if (at === -1) { void move(step); return }
  const to = Math.max(0, Math.min(rows.length - 1, at + step))
  const next = new Set(selected.value)
  next.add(rows[at].id); next.add(rows[to].id)
  if (!selectAnchor) selectAnchor = rows[at].id
  selected.value = next
  cursorId.value = rows[to].id
  void nextTick(() => table.value?.scrollToRow(rows[to].id))
  table.value?.focusGrid()
}
function keydown(event: KeyboardEvent) {
  if (event.altKey && event.key === 'ArrowLeft' && ticketKey.value && trail.value.length && !typing(event.target as HTMLElement | null)) { event.preventDefault(); trailBack(); return }
  // The Knowledge tab and its entries have their own keys.
  if (knowledgeActive.value && !ticketKey.value) return
  // Command or Control A in the list selects every loaded row.
  if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 'a' && selectable.value && !event.defaultPrevented
    && !typing(event.target) && !document.querySelector('dialog[open], .floating') && !panel.value?.el?.contains(document.activeElement)) {
    event.preventDefault(); selectAll(true); return
  }
  if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return
  if (document.querySelector('dialog[open]')) return
  const target = event.target as HTMLElement | null
  if (target?.closest?.('.floating') || document.querySelector('.floating')) return
  if (typing(target)) {
    if (!graphActive.value && event.key === 'ArrowDown' && target === toolbar.value?.input) { event.preventDefault(); target.blur(); void move(cursorId.value ? 0 : 1) }
    return
  }
  // The journey has its own keys; with a ticket open, the panel's keys still work.
  if (journeyActive.value && !ticketKey.value) return
  if (graphActive.value && ['j', 'k', 'ArrowDown', 'ArrowUp', 'Enter', 'o', 'n'].includes(event.key)) return
  if ((journeyActive.value || knowledgeActive.value) && ['j', 'k', 'ArrowDown', 'ArrowUp', 'Enter', 'o', '/', 'n'].includes(event.key)) return
  const row = sequence.value.find(item => item.id === cursorId.value)
  if (event.key === 'F') { event.preventDefault(); toolbar.value?.openFilterMenu(); return }
  if (selectable.value && !panel.value?.el?.contains(document.activeElement)) {
    if (event.key === 'Escape' && (selected.value.size || phonePicking.value)) { event.preventDefault(); clearSelection(); return }
    if (!ticketKey.value) {
      if (event.key === 'x' && row) { event.preventDefault(); selectRow(row, 'toggle'); return }
      if (event.key === 'J' || (event.key === 'ArrowDown' && event.shiftKey)) { event.preventDefault(); extendSelection(1); return }
      if (event.key === 'K' || (event.key === 'ArrowUp' && event.shiftKey)) { event.preventDefault(); extendSelection(-1); return }
      const bulkKey = ({ s: 'status', a: 'assignee', p: 'priority', l: 'labels', m: 'move', g: 'release' } as const)[event.key as 's']
      if (selected.value.size && bulkKey) { event.preventDefault(); openBulk(bulkKey); return }
    }
  }
  if (outlineActive.value && !fullView.value && outlineKey(event, row)) return
  switch (event.key) {
    case 'j': case 'ArrowDown': event.preventDefault(); void move(1); break
    case 'k': case 'ArrowUp': event.preventDefault(); void move(-1); break
    case 'Enter': case 'o':
      if (event.key === 'Enter' && target?.closest('button, a, summary')) return
      if (row) { event.preventDefault(); openRow(row) }
      break
    case 'Escape':
      if (creating.value || outline.createUnder.value) { event.preventDefault(); closeCreate() }
      else if (ticketKey.value) { event.preventDefault(); closePanel() }
      break
    case '/': if (!fullView.value) { event.preventDefault(); toolbar.value?.focusSearch() } break
    case 'n': event.preventDefault(); void startCreate(outlineActive.value && !ticketKey.value && row?.kind_slug === 'epic' ? row : null); break
    case 'u': if (liveList.pill.value && listActive.value) { event.preventDefault(); showUpdates() } break
    case 'e': if (ticketKey.value) { event.preventDefault(); void panel.value?.startEdit() } break
    case 's':
      if (ticketKey.value) { event.preventDefault(); panel.value?.openStatus() }
      else if (row) { const anchor = document.querySelector<HTMLElement>(`#row-${row.id} .status-btn`); if (anchor) { event.preventDefault(); openStatus(row, anchor, 'list') } }
      break
    case 'p': if (ticketKey.value) { event.preventDefault(); panel.value?.openPriority() } break
    case 'a': if (ticketKey.value) { event.preventDefault(); panel.value?.openAssignee() } break
    case 'r': if (ticketKey.value) { event.preventDefault(); panel.value?.openLink() } break
    case 'g': if (ticketKey.value) { event.preventDefault(); panel.value?.openRelease() } break
    case 'c': if (ticketKey.value) { event.preventDefault(); panel.value?.focusComposer() } break
    case 'f': if (ticketKey.value) { event.preventDefault(); if (fullView.value) collapse(); else expand() } break
  }
}

// Outline keys: right opens or steps into, left closes or steps out, Space toggles.
function outlineKey(event: KeyboardEvent, row: ListItem | undefined): boolean {
  if (!['ArrowRight', 'ArrowLeft', ' '].includes(event.key) || !row) return false
  if (ticketKey.value && panel.value?.el?.contains(document.activeElement)) return false
  event.preventDefault()
  const open = outline.isExpanded(row.id), children = outline.hasChildren(row.id)
  const focusRow = (id: string | null) => {
    if (!id || !sequence.value.some(item => item.id === id)) return
    cursorId.value = id
    void nextTick(() => table.value?.scrollToRow(id))
  }
  if (event.key === ' ') { if (children) outline.toggle(row.id) }
  else if (event.key === 'ArrowRight') {
    if (children && !open) outline.setExpanded(row.id, true)
    else if (children) { const index = sequence.value.findIndex(item => item.id === row.id); const next = sequence.value[index + 1]; if (next && next.parent_id === row.id) focusRow(next.id) }
  } else {
    if (children && open) outline.setExpanded(row.id, false)
    else focusRow(outline.parentOf(row.id))
  }
  table.value?.focusGrid()
  return true
}

// ---------- Layout: sticky toolbar height and stuck state ----------
let resize: ResizeObserver | undefined
let stick: IntersectionObserver | undefined
let clock: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  scrollRoot.value = document.getElementById('main')
  void projects.load()
  window.addEventListener('keydown', keydown)
  window.addEventListener('beforeunload', beforeUnload)
  phoneQuery.addEventListener('change', onPhone)
  clock = setInterval(() => { now.value = Date.now() }, 60_000)
})
// The toolbar only exists once the project is known, so observe it when it appears.
watch(toolbarWrap, element => {
  resize?.disconnect()
  if (!element) return
  resize = new ResizeObserver(() => { toolbarHeight.value = element.offsetHeight })
  resize.observe(element)
}, { flush: 'post' })
watch([stickMark, scrollRoot], ([element, root]) => {
  stick?.disconnect()
  if (!element || !root) return
  stick = new IntersectionObserver(([entry]) => { stuck.value = !entry.isIntersecting }, { root, threshold: 0 })
  stick.observe(element)
}, { flush: 'post' })
onBeforeUnmount(() => {
  window.removeEventListener('keydown', keydown)
  window.removeEventListener('beforeunload', beforeUnload)
  clearInterval(clock)
  resize?.disconnect()
  stick?.disconnect()
  list.invalidate()
  knowledge.stop()
  dockQuery.removeEventListener('change', onDockWidth)
  phoneQuery.removeEventListener('change', onPhone)
  flowPillContext.value = null
})

// ---------- Document title ----------
watch([project, panelItem, knowledgeActive, knowledgeEntryOpen, knowledgeDocked], ([current, item, knowledgeOn, entryOn, docked]) => {
  if (!current || entryOn || docked) return
  setPageTitle(item ? `${item.key} ${item.title}` : knowledgeOn ? `Knowledge · ${current.routeKey} ${current.title}` : `${current.routeKey} ${current.title}`)
}, { immediate: true })


</script>

<template>
  <section class="project-page" :class="{ 'panel-open': (!!ticketKey && !fullView) || knowledgeDocked, 'full-view': fullView, 'knowledge-entry': knowledgeEntryOpen, 'knowledge-dock': knowledgeDocked }" :style="{ '--toolbar-h': `${toolbarHeight}px` }" :aria-labelledby="project && !knowledgeEntryOpen ? 'project-title' : undefined">
    <template v-if="project">
      <div v-show="!fullView && !knowledgeEntryOpen" class="list-view" :class="{ selecting: selectable && selected.size }">
      <header class="project-head" :class="{ 'glimpse-room': glimpseActive && !showViewBar }">
        <div class="head-flex" :class="{ 'with-glimpse': glimpseActive }">
        <div class="head-main">
          <div class="title-line">
            <span class="key-badge big">{{ project.routeKey }}</span>
            <h1 id="project-title">{{ project.title }}</h1>
            <span v-if="project.frozen" class="chip state-chip">Frozen</span>
            <span v-else-if="project.archived" class="chip state-chip">Archived</span>
          </div>
          <p v-if="project.description" class="description" :data-tip="project.description.length > 120 ? project.description : undefined">{{ project.description }}</p>
        </div>
        <HeaderGlimpse v-if="headerGraphReady && headerGraph && !graphActive" :project-id="project.id" :project-key="project.routeKey" :ticket-count="counts?.total ?? 0" :enabled="headerGraph" @active="glimpseActive = $event" />
        <div v-if="counts" class="head-stats" :aria-label="`${counts.open} open, ${counts.progress} in progress, ${counts.done} done of ${counts.total}`">
          <div class="stat-line">
            <span class="stat" :data-tip="PROJECT_COLUMN_BY_ID.get('open')!.tip"><StatusIcon state="open" :size="11" /><b>{{ counts.open.toLocaleString('en-GB') }}</b> open</span>
            <span class="stat" :data-tip="PROJECT_COLUMN_BY_ID.get('doing')!.tip"><StatusIcon state="in_progress" :size="11" /><b>{{ counts.progress.toLocaleString('en-GB') }}</b> doing</span>
            <span class="stat" :data-tip="PROJECT_COLUMN_BY_ID.get('done')!.tip"><StatusIcon state="done" :size="11" /><b>{{ counts.done.toLocaleString('en-GB') }}</b> done</span>
          </div>
          <div class="progress-line" :data-tip="projectProgressTip(counts.open, counts.progress, counts.done, counts.cancelled)">
            <span class="bar"><i :style="{ width: `${counts.percent}%` }" /></span>
            <span class="mono pct">{{ counts.percent }}%</span>
          </div>
          <p class="activity">Active <time :datetime="project.last_activity" :data-tip="absoluteTime(project.last_activity)">{{ relativeTime(project.last_activity, { now, long: true }) }}</time></p>
        </div>
        <div v-else class="head-stats head-stats-skeleton" aria-hidden="true">
          <span class="skeleton stat-placeholder" /><span class="skeleton progress-placeholder" /><span class="skeleton activity-placeholder" />
        </div>
        </div>
        <ProjectTabs :items="PROJECT_SECTIONS" :selected="section" label="Project sections" sections @select="setSection" />
      </header>

      <ViewBar
        v-if="showViewBar" ref="viewBar" :views="views.items" :active-id="activeView?.id ?? null" :dirty="viewDirty" :default-id="defaultViewId" :can-save-new="canSaveView && !activeView"
        :me="me?.id ?? null" :href-for="hrefFor" @open="id => openView(id)" @save="saveActive" @save-as="startSave" @reset="openView(activeView?.id ?? null, true)"
        @rename="startRename" @duplicate="duplicate" @set-default="setDefaultView" @share="share" @copy-link="copyViewLink" @remove="remove"
      />
      <div ref="stickMark" class="stick-mark" aria-hidden="true" />
      <div v-if="!journeyActive" ref="toolbarWrap" class="toolbar-wrap" :class="{ stuck }">
        <ListToolbar
          ref="toolbar" :filters="filters" :options="options" :label="chipLabel" :total="total" :loading="graphActive ? graphState.loading : list.loading.value" :density="density" :stuck="stuck"
          :facet-loading="facetLoading"
          @search="q => update({ q })" @toggle="toggleValue" @exclude="excludeValue" @clear="dimension => update({ [dimension]: [] })" @clear-all="clearFilters"
          @show-closed="value => update({ showClosed: value })" @group="setGroup" @sort="setSort" @density="setDensity" @date="setDate"
          @open-sheet="filterSheet?.open()" @need-options="needOptions" @create="startCreate()"
          :view="viewMode" :knowledge-view="knowledgeView" @view="setView" @expand-all="outline.expandAll()" @collapse-all="outline.collapseAll()"
          @expand-groups="setAllGroups(true)" @collapse-groups="setAllGroups(false)"
          :columns="toolbarColumns" @columns="saveColumns" @columns-reset="resetColumns"
          :header-graph="headerGraph" @header-graph="setHeaderGraph"
        />
        <div v-if="selectable && (sequence.length || picking)" class="phone-pick" :class="{ on: picking }">
          <p v-if="picking" class="phone-pick-status">
            <span aria-live="polite"><b class="mono">{{ selected.size.toLocaleString('en-GB') }}</b> selected</span>
            <template v-if="selectedDeleted.length"><span class="dot" aria-hidden="true">·</span><span class="gone">{{ selectedDeleted.length.toLocaleString('en-GB') }} deleted</span></template>
            <span class="dot" aria-hidden="true">·</span>
            <button type="button" @click="clearSelection">Cancel</button>
          </p>
          <button v-else type="button" class="quiet" @click="phonePicking = true">Select</button>
        </div>
      </div>

      <JourneyView
        v-if="journeyActive" :project="{ id: project.id, routeKey: project.routeKey, title: project.title }" :stage="journeyStage" :release-key="journeyRelease" :walk-key="journeyWalk"
        :can-write="writable" :person="session.identity?.principal.kind === 'person'" :me="me?.id ?? null"
        @stage="journeyStageTo" @release="journeyReleaseTo" @walk="journeyWalkTo" @open="openKey"
      />
      <KnowledgeTab
        v-else-if="knowledgeActive" ref="knowledgeTab" :project="{ id: project.id, routeKey: project.routeKey, title: project.title }" :state="knowledge"
        :filters="knowledgeFilters" :can-write="knowledgeWritable" :person="session.identity?.principal.kind === 'person'" :now="now" :paused="knowledgeEntryOpen || !!ticketKey" :dock="knowledgeWide" :open-entry="shownEntry?.mode === 'dock' ? shownEntry : null" @update="updateKnowledge" @accepted="learningAccepted" @reverted="learningReverted"
      />
      <component :is="activeTicketView.component" v-else-if="activeTicketView.component"
        :project="project" :filters="filters" ref="ticketGraphView" @open="openKey" @state="(value: TicketGraphState) => graphState = value" />
      <template v-else>
      <div v-if="listActive && liveList.pill.value" class="live-dock">
        <button type="button" class="live-pill" :aria-label="liveList.pill.value" aria-keyshortcuts="u" :data-tip="liveList.pending.overflow ? 'Load the list again' : 'Show the updates · u'" @click="showUpdates">
          <AppIcon :name="liveList.pending.overflow ? 'refresh' : 'arrow-up'" :size="13" />
          <span>{{ liveList.pill.value.split(' · ')[0] }}</span><span class="dot" aria-hidden="true">·</span><b>{{ liveList.pill.value.split(' · ')[1] }}</b>
        </button>
      </div>
      <p v-if="listActive" class="sr-only live-said" role="status" aria-live="polite">{{ liveList.message.value }}</p>
      <TicketTable
        ref="table" :expected-rows="expectedRows" :groups="groups" :group="filters.group" :rows-by-id="rowsById" :cursor-id="cursorId" :open-id="panelItem?.id ?? null"
        :query="filters.q" :sort="filters.sort" :density="density"
        :loading="outlineActive ? outline.loading.value : list.loading.value" :loading-more="outlineActive ? outline.loadingMoreRoot.value : list.loadingMore.value"
        :error="outlineActive ? outline.error.value || list.error.value : list.error.value" :more-error="list.moreError.value"
        :has-more="outlineActive ? outline.hasMoreRoot.value : !!list.cursor.value" :filtered="filtered" :hiding-closed="!filters.showClosed"
        :collapsed="collapsed" :total="total" :project-key="routeKey" :scroll-root="scrollRoot" :now="now" :show-assignee="showAssignee"
        :creating="creating" :project-id="project.id" :known-states="knownStates" :create="quickCreate" @close-create="closeCreate"
        :outline="outlineActive ? outline.entries.value : null" :can-drag="outlineActive && writable" :prefs="tablePrefs"
        :selectable="selectable" :selected="selected" :picking="phonePicking" :can-assign-release="can('releases.write', project.id)" :native-releases="nativeReleases" @select="selectRow" @select-all="selectAll"
        @layout="(visible, customised) => tableLayout = { visible, customised }" @widths="saveWidths"
        @toggle-row="outline.toggle" @toggle-no-epic="outline.noEpicCollapsed.value = !outline.noEpicCollapsed.value"
        @more-children="id => id === project!.id ? outline.loadMoreRoot() : outline.loadChildren(id, true)" @move="moveRow"
        @open="openRow" @cursor="id => cursorId = id" @sort="sortBy" @status="(row, anchor) => openStatus(row, anchor, 'list')" @release="(row, anchor) => openRelease(anchor, [row.id])"
        @copy="row => copyKey(row.key)" @new-tab="row => newTab(row.key)" @toggle-group="toggleGroup" @open-epic="openEpic"
        @retry="outlineActive ? outline.reload() : list.load()" @more="outlineActive ? outline.loadMoreRoot() : list.loadMore()" @grid-focus="focusFirst" @clear-filters="clearFilters" @show-closed="update({ showClosed: true })"
        :live-labels="listActive ? liveList.labels.value : undefined" :live-flash="listActive ? liveList.flash.value : undefined"
        :live-pill="listActive && liveList.pill.value ? { text: liveList.pill.value, overflow: liveList.pending.overflow } : null" @show-updates="showUpdates"
      />
      </template>

      <BulkBar
        v-if="selectable && selected.size" :count="selected.size" :deleted="selectedDeleted.length" :loaded="sequence.length" :total="total" :busy="bulkBusy" :can-write="writable" :can-release="can('releases.write', project.id)" :frame="listFrame"
        @status="anchor => openBulk('status', anchor)" @assignee="anchor => openBulk('assignee', anchor)" @priority="anchor => openBulk('priority', anchor)"
        @labels="anchor => openBulk('labels', anchor)" @move="anchor => openBulk('move', anchor)" @release="anchor => openRelease(anchor, liveSelection())" @archive="bulkArchive" @clear="clearSelection" @select-all="selectAllMatching"
      />
      <p v-if="!journeyActive && !knowledgeActive && !graphActive" class="hint">
        <kbd class="keycap">j</kbd><kbd class="keycap">k</kbd> move · <kbd class="keycap"><AppIcon name="enter" /></kbd> open · <kbd class="keycap">/</kbd> search ·
        <button type="button" class="hint-link" @click="run({ name: 'shortcuts' })"><kbd class="keycap">?</kbd> all shortcuts</button>
      </p>
      </div>

      <KnowledgeEntryPage
        v-if="shownEntry" ref="knowledgeEntry" :project="{ id: project.id, routeKey: project.routeKey, title: project.title }" :mode="shownEntry.mode"
        :type="shownEntry.type" :slug="shownEntry.slug" :state="knowledge" :can-write="knowledgeWritable" :can-delete="knowledgeDeletable" :now="now" :list-query="knowledgeListQuery"
        @close="shownEntry.mode === 'dock' ? closeKnowledgeDock() : closeKnowledgeEntry()"
      />
      <PanelSplitter v-if="knowledgeDocked" field="knowledgePanel" css-var="--knowledge-panel-user-w" target=".entry-page.dock" :reserve="DOCK_LIST_RESERVE" />
      <PanelSplitter v-if="ticketKey && !fullView" />
      <TicketWorkspace
        v-if="ticketKey" ref="panel" :item="panelItem" :ticket-key="ticketKey.toUpperCase()" :resolving="panelLoading" :resolve-error="panelError"
        :position="panelPosition" :now="now" :mode="fullView ? 'full' : 'panel'" :project="{ id: project.id, routeKey: project.routeKey }"
        :names="list.names" :me="me" :can-write="writable" :can-delete="nodeDeletable" :can-move="nodeMovable" :can-link="relationLinkable" :can-unlink="relationUnlinkable"
        :can-comment="commentable" :can-delete-comment="commentDeletable" :can-attach="attachable" :people="people" :native-releases="nativeReleases"
        @close="closePanel" @prev="move(-1)" @next="move(1)" @expand="expand" @collapse="collapse" @new-tab="newTab(panelItem?.key ?? ticketKey)"
        @status="anchor => panelItem && openStatus(panelItem, anchor, 'panel')" @open-key="openRelated" :trail="trail" @trail-back="trailBack" @removed="removed" @created="childCreated" @moved="childMoved" @assigned="() => { void list.load(); void refreshMemberships() }" @retry="resolvePanel"
      />
      <StatusMenu v-if="statusMenu" :anchor="statusMenu.anchor" :current="statusMenu.row.state" :known-states="knownStates" :ticket-key="statusMenu.row.key" @choose="chooseStatus" @close="closeStatus" />
      <FilterSheet
        ref="filterSheet" :filters="filters" :options="options" :total="total" :view="graphActive ? 'graph' : outlineActive ? 'outline' : 'list'" :can-save="!graphActive && canSaveView"
        @expand-all="outline.expandAll()" @collapse-all="outline.collapseAll()"
        @toggle="toggleValue" @exclude="excludeValue" @clear-all="clearFilters" @show-closed="value => update({ showClosed: value })" @group="setGroup" @date="setDate"
        @opened="sheetOpened" @save-view="anchor => startSave(viewBar?.$el ?? anchor)"
      />
      <SaveViewPanel
        v-if="savePanel" :key="`${savePanel.mode}-${savePanel.view?.id ?? 'new'}`" :anchor="savePanel.anchor" :mode="savePanel.mode" :name="savePanel.name" :project-title="project.title"
        :busy="saveBusy" :error="saveError" @submit="submitSave" @close="closeSave"
      />
      <StatusMenu v-if="bulkMenu?.kind === 'status'" :anchor="bulkMenu.anchor" current="" :known-states="knownStates" :ticket-key="plural(selected.size, 'ticket')" @choose="bulkStatus" @close="closeBulk" />
      <OptionMenu
        v-else-if="bulkMenu?.kind === 'assignee'" :anchor="bulkMenu.anchor" title="Assignee" :subject="plural(selected.size, 'ticket')" kind="assignee" :options="bulkPeople" current="-" :searchable="bulkPeople.length > 8"
        @choose="bulkAssign" @close="closeBulk"
      />
      <OptionMenu v-else-if="bulkMenu?.kind === 'priority'" :anchor="bulkMenu.anchor" title="Priority" :subject="plural(selected.size, 'ticket')" kind="priority" :options="bulkPriorities" current="-" @choose="bulkPriority" @close="closeBulk" />
      <LabelMenu v-else-if="bulkMenu?.kind === 'labels'" :anchor="bulkMenu.anchor" :labels="bulkLabels" :count="selectedRows.length" :busy="bulkBusy" @apply="bulkLabelsApply" @close="closeBulk" />
      <EpicPicker v-else-if="bulkMenu?.kind === 'move'" :anchor="bulkMenu.anchor" :project-id="project.id" current="-" :subject="plural(selected.size, 'ticket')" allow-none @choose="bulkMove" @close="closeBulk" />
      <ReleasePicker v-else-if="bulkMenu?.kind === 'release'" :anchor="bulkMenu.anchor" :project-id="project.id" :subject="plural(releaseIds.length, 'ticket')" @choose="chooseRelease" @close="closeBulk" />
    </template>

    <div v-else-if="projects.error && !projects.loaded" class="page-state" role="alert">
      <AppIcon name="alert" :size="20" />
      <h1>Projects could not be loaded</h1>
      <p>{{ projects.error }}</p>
      <button type="button" class="btn" @click="projects.load(true)">Try again</button>
    </div>
    <div v-else-if="projects.loaded" class="page-state">
      <AppIcon name="folder" :size="20" />
      <h1>No project called {{ projectKey.toUpperCase() }}</h1>
      <p>It may have been renamed or archived.</p>
      <RouterLink class="btn" to="/"><AppIcon name="arrow" :size="14" />All projects</RouterLink>
    </div>
    <div v-else class="head-skeleton" role="status" aria-label="Loading project">
      <span class="skeleton sk-a" /><span class="skeleton sk-b" /><span class="skeleton sk-c" />
    </div>
  </section>
</template>

<style scoped>
/* Lists use the full width; the gutter grows with the screen. */
.project-page { width: 100%; margin: 0; padding: 22px var(--gutter) 12px; }
/* The header follows its own width, not the window's: a docked ticket panel can
   leave the list as narrow as a phone on a wide screen (AEON-140). */
.project-head { padding: 4px 0 14px; container: projecthead / inline-size; }
.head-flex { display: flex; align-items: flex-end; justify-content: space-between; gap: 32px; }
.head-flex.with-glimpse { display: grid; grid-template-columns: minmax(0, max-content) minmax(180px, 1fr) auto; align-items: stretch; column-gap: 28px; }
.head-flex.with-glimpse .head-stats { align-self: end; }
.head-flex.with-glimpse .head-main { align-self: center; }
.head-main { min-width: 0; flex: 1; position: relative; z-index: 1; }
.head-stats { position: relative; z-index: 1; }
.title-line { display: flex; align-items: center; gap: 12px; min-width: 0; }
.key-badge.big { height: 26px; padding: 0 10px; font-size: 12px; border-radius: 7px; }
.title-line h1 { font-size: 30px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.state-chip { height: 20px; font-size: 10px; text-transform: uppercase; letter-spacing: .08em; }
.description { margin-top: 6px; max-width: 820px; font-size: 13.5px; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.head-stats { display: grid; justify-items: end; gap: 7px; flex-shrink: 0; }
.head-stats-skeleton { width: 280px; }
.stat-placeholder { width: 100%; height: 19px; }
.progress-placeholder { width: 100%; height: 12px; }
.activity-placeholder { width: 42%; height: 18px; }
.stat-line { display: flex; gap: 16px; font-size: 12.5px; color: var(--ink-2); }
.stat { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.stat b { font: 600 13px/1 var(--mono); color: var(--ink); font-variant-numeric: tabular-nums; font-variant-ligatures: none; }
.progress-line { display: flex; align-items: center; gap: 10px; width: 280px; }
.progress-line .bar { flex: 1; }
.pct { width: 34px; font-size: 12px; color: var(--ink-2); text-align: right; }
.activity { font-size: 12px; color: var(--ink-3); }
.activity time { color: var(--ink-2); }
.stick-mark { height: 1px; margin-bottom: -1px; }
/* Waiting live updates on a phone (the Title header carries them on wider
   screens): a pill floats just under the toolbar and never pushes cards down (AEON-326). */
.live-dock { display: none; }
.live-pill {
  display: inline-flex; align-items: center; gap: 6px; height: 32px; margin-top: 4px; padding: 0 13px 0 11px; border: 1px solid var(--glass-edge); border-radius: 999px;
  background: var(--glass); box-shadow: var(--shadow-pop); color: var(--ink-2); font-size: 12.5px; white-space: nowrap; pointer-events: auto;
  -webkit-backdrop-filter: blur(18px) saturate(1.2); backdrop-filter: blur(18px) saturate(1.2);
}
.live-pill svg { color: var(--teal-ink); }
.live-pill b { font-weight: 600; color: var(--teal-ink); }
.live-pill .dot { color: var(--ink-3); }
.live-pill:hover { color: var(--ink); background: var(--surface-raised-2); }
.live-pill:focus-visible { box-shadow: var(--focus-ring), var(--shadow-pop); }
@media (prefers-reduced-motion: no-preference) {
  .live-pill { animation: live-pill-in .2s cubic-bezier(.2, .7, .2, 1); }
  @keyframes live-pill-in { from { opacity: 0; transform: translateY(-6px); } to { opacity: 1; transform: none; } }
}
/* While tickets are selected the bulk bar floats at the bottom: the list can scroll clear of it. */
.list-view.selecting { padding-bottom: 76px; }
@media (max-width: 720px) { .list-view.selecting { padding-bottom: calc(168px + env(safe-area-inset-bottom)); } }
.phone-pick { display: none; }
@media (max-width: 720px) {
  .phone-pick { display: flex; align-items: center; justify-content: flex-start; min-height: 44px; margin-top: -2px; }
  .phone-pick-status { display: flex; align-items: center; gap: 8px; min-height: 44px; margin: 0; font-size: 15px; color: var(--ink-2); }
  .phone-pick-status b { font-size: 15px; font-weight: 700; color: var(--ink); font-variant-numeric: tabular-nums; }
  .phone-pick .dot { color: var(--ink-3); }
  .phone-pick .gone { color: var(--ink-2); }
  .live-dock { position: sticky; top: calc(var(--toolbar-h, 0px) + 4px); z-index: 4; height: 0; display: flex; justify-content: center; pointer-events: none; }
  .live-pill { font-size: 13px; }
  .phone-pick button { min-height: 44px; padding: 0 12px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font-size: 15px; font-weight: 600; }
  .phone-pick button.quiet { padding-left: 2px; color: var(--ink-2); font-weight: 600; }
  .phone-pick.on button { margin-left: -12px; color: var(--teal-ink); }
  .phone-pick button:focus-visible { box-shadow: var(--focus-ring); }
}
/* Without the saved-view strip the header glimpse keeps that room (14px + the 42px strip) to spread into; the graph was framed for it. */
.project-head.glimpse-room { padding-bottom: 56px; }
.toolbar-wrap { position: sticky; top: 0; z-index: 5; margin: 0 calc(-1 * var(--gutter)); padding: 0 var(--gutter); container: toolbar / inline-size; }
.toolbar-wrap.stuck { background: var(--glass); box-shadow: 0 1px 0 var(--line), 0 12px 24px -20px rgba(16, 35, 39, .35); -webkit-backdrop-filter: blur(18px) saturate(1.2); backdrop-filter: blur(18px) saturate(1.2); }
.hint { display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 5px; padding: 16px 0 6px; font-size: 12px; color: var(--ink-3); }
/* The hint waits for the rows, like the footer, so it never jumps while they load. */
.project-page:has(.skeleton-body) .hint { visibility: hidden; }
.hint .keycap + .keycap { margin-left: 2px; }
.hint-link { display: inline-flex; align-items: center; gap: 5px; padding: 0; border: 0; background: transparent; color: var(--ink-3); font-size: 12px; }
.hint-link:hover { color: var(--teal-ink); }
.project-page.full-view { padding-top: 12px; }
.project-page.knowledge-entry { padding-top: 0; }
/* The docked knowledge entry reads a little wider than a ticket and keeps a width of
   its own (dragged, it is the person's); the list always keeps its 560px. */
.project-page.knowledge-dock {
  --panel-default: clamp(560px, calc(560px + (100vw - 1200px) * .36), 840px);
  --panel-w: min(var(--knowledge-panel-user-w, var(--panel-default)), calc(100vw - 620px));
}
/* Wide screens dock the ticket panel: the list reflows beside it instead of under it. */
@media (min-width: 1100px) {
  .project-page.panel-open { width: 100%; margin: 0; padding-right: calc(var(--panel-w) + 22px); }
  .project-page.panel-open .toolbar-wrap { margin-right: 0; padding-right: 0; }
  .project-page.panel-open .description { max-width: 100%; }
}
.page-state { display: grid; justify-items: center; gap: 10px; padding: 96px 24px; text-align: center; }
.page-state > svg { color: var(--teal); }
.page-state h1 { font-size: 26px; }
.page-state .btn { margin-top: 8px; }
.head-skeleton { display: grid; gap: 12px; padding: 12px 0; }
.sk-a { width: 320px; height: 26px; border-radius: 8px; } .sk-b { width: 520px; } .sk-c { width: 100%; height: 44px; border-radius: 12px; margin-top: 18px; }
@media (max-width: 1080px) { .progress-line { width: 200px; } .stat-line { gap: 12px; } }
@container projecthead (max-width: 760px) {
  .head-flex.with-glimpse { display: flex; }
  .head-flex.with-glimpse .head-main, .head-flex.with-glimpse .head-stats { align-self: stretch; }
  .head-flex.with-glimpse .head-main { padding-top: 0; }
  .head-flex.with-glimpse :deep(.glimpse-col) { display: none; }
  .head-flex { flex-direction: column; align-items: stretch; gap: 12px; }
  .head-stats { justify-items: start; }
  .head-stats-skeleton { width: 100%; min-height: 103px; }
  .progress-line { width: 100%; }
  .title-line h1 { font-size: 24px; }
  .stat-line { flex-wrap: wrap; gap: 4px 14px; }
}
@media (max-width: 720px) {
  .project-page { padding: 14px 12px 16px; }
  .toolbar-wrap { margin: 0 -12px; padding: 0 12px; }
  .title-line { gap: 10px; }
  .title-line h1 { font-size: 24px; }
  .description { white-space: normal; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
  .stat-line { flex-wrap: wrap; gap: 4px 14px; }
  .activity { display: none; }
  .hint { display: none; }
}
</style>
