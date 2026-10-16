// SPDX-License-Identifier: AGPL-3.0-only

import { ref, watch } from 'vue'
import { isNavigationFailure, NavigationFailureType } from 'vue-router'
import { can, ensurePermissions } from '../../lib/authz'
import { selectionHiddenByPolicy, type HideState } from '../../lib/hideStates'
import { groupStates, reconcileStatusHide, type HeaderStatusGroup } from '../../lib/projectStatusCounts'
import { toast } from '../../lib/toast'
import { settledNavigation } from '../../lib/navigation'
import { clearedFilters, facetOptions, levelOf, toggleMembers, filtersToQuery, toggleIn, toggleOut, valueLabel, type DateFilter, type Dimension, type GroupBy, type ListFilters } from '../../lib/ticketList'
import { cycleSort, type SortField, type SortKey } from '../../lib/work'
import { PROJECT_SECTIONS, type ProjectSection } from '../../components/work/projectNavigation'
import { DOCK_MEDIA } from '../../lib/knowledge'
import type { ProjectState } from './types'
import type { useProjectData } from './useProjectData'

export function useProjectFilters(state: ProjectState, data: ReturnType<typeof useProjectData>, resetPanelOrigin: () => void) {
  const { route, router, session, projectKey, ticketKey, project, projectId, vocabulary, queueSnapshot, section, sectionPath, ticketSectionQuery, filters, activeTicketView, knowledgeActive, deliveryActive, graphActive, graphState, collapsed, creating, fullView, ticketPath } = state
  const { list, groups } = data
  function options(dimension: Dimension) {
    if (graphActive.value) {
      const counts: Record<string, number> = {}
      for (const node of graphState.value.visible.nodes) {
        const level = levelOf({ is_leaf: node.is_leaf, depth: node.depth, kind_slug: node.type })
        const value = dimension === 'status' ? node.status : dimension === 'priority' ? node.priority ?? 'none' : dimension === 'type' ? level
          : dimension === 'shape' ? level === 'leaf' ? 'leaf' : 'parent' : dimension === 'depth' ? String(node.depth ?? 1) : node.type
        counts[value] = (counts[value] ?? 0) + 1
      }
      return facetOptions(dimension, counts, filters.value[dimension], list.names, undefined, { workName: vocabulary.leaf.name, vocabulary: vocabulary.value })
    }
    const result = facetOptions(dimension, list.counts(dimension), filters.value[dimension].filter(value => value !== 'queued' && value !== '!queued'), list.names, session.identity?.principal.id, { colors: list.colors, epics: list.epics.value, workName: vocabulary.leaf.name, vocabulary: vocabulary.value })
    return dimension === 'status' ? [{ value: 'queued', label: 'Queued', count: queueSnapshot.value?.items.length ?? 0, hint: 'Open + a place' }, ...result] : result
  }
  function chipLabel(dimension: Dimension, value: string) {
    return valueLabel(dimension, value, { names: list.names, me: session.identity?.principal.id, epics: list.epics.value, workName: vocabulary.leaf.name, vocabulary: vocabulary.value })
  }
  // What a menu needs before it opens: names, label counts or the project's epics.
  // Type also needs the workspace's names. Only the newest menu's wait counts.
  const facetLoading = ref(false)
  let facetWait = 0
  function needOptions(dimension: Dimension) {
    const work: Promise<unknown>[] = []
    if (dimension === 'assignee') void list.resolveNames(options('assignee').map(o => o.value))
    if (dimension === 'epic') work.push(list.loadEpics())
    if (dimension === 'type') work.push(vocabulary.load())
    const facet = dimension === 'tag' ? 'tag' : dimension === 'cost' ? 'cost_unit' : dimension === 'release' ? 'release' : dimension === 'human_check' ? 'person_waiting'
      : dimension === 'type' ? 'level' : dimension === 'shape' ? 'shape' : dimension === 'depth' ? 'depth' : null
    // An applied filter's own counts come without it, with the newest load.
    if (!graphActive.value && facet && !filters.value[dimension].length) work.push(list.requestFacet(facet))
    else if (!graphActive.value && filters.value[dimension].length) work.push(list.settled())
    const request = ++facetWait
    facetLoading.value = work.length > 0
    if (work.length) void Promise.allSettled(work).then(() => { if (request === facetWait) facetLoading.value = false })
  }
  // The sheet shows its filters once their names and counts are in (an applied
  // filter's own counts with the newest load), so a late answer moves nothing.
  const sheetLoading = ref(false)
  let sheetWait = 0
  function sheetOpened() {
    const request = ++sheetWait
    const work: Promise<unknown>[] = [vocabulary.load()]
    if (!graphActive.value) {
      void list.resolveNames(options('assignee').map(o => o.value))
      work.push(list.settled(), list.loadEpics())
      for (const facet of ['tag', 'cost_unit', 'release', 'person_waiting', 'level']) work.push(list.requestFacet(facet))
    }
    sheetLoading.value = true
    void Promise.allSettled(work).then(() => { if (request === sheetWait) sheetLoading.value = false })
  }
  // Chips name epics by title, so the epics load when an epic filter is on.
  watch(() => filters.value.epic.length > 0 && !!projectId.value, on => { if (on) void list.loadEpics() }, { immediate: true })

  // ---------- URL state ----------
  function modeQuery() {
    return { view: route.query.view === 'full' && fullView.value ? 'full' : activeTicketView.value.id,
      ...(ticketKey.value ? ticketSectionQuery() : {}), ...(route.query.panel === 'full' ? { panel: 'full' } : {}) }
  }
  function update(patch: Partial<ListFilters>) {
    if (patch.status !== undefined || patch.hideStates !== undefined) {
      const nextStatus = patch.status ?? filters.value.status
      if (patch.status !== undefined && !('statusScope' in patch)) patch = { ...patch, statusScope: undefined }
      const hidden = selectionHiddenByPolicy(nextStatus, patch.statusScope ?? (patch.status === undefined ? filters.value.statusScope : undefined), project.value?.status_counts, patch.hideStates ?? filters.value.hideStates, project.value?.status_counts_truncated)
      const result = reconcileStatusHide(filters.value.showClosed, filters.value.hideRestore ?? false, hidden)
      patch = { ...patch, showClosed: result.showClosed, hideRestore: result.automatic || undefined }
      if (result.note) toast(result.note, { key: 'project-status-hide' })
    }
    void router.replace({ path: route.path, query: { ...filtersToQuery({ ...filters.value, ...patch }), ...modeQuery() } })
  }
  function setHideStates(hideStates: HideState[]) { update({ hideStates }) }
  function manualShowClosed(showClosed: boolean) { update({ showClosed, hideRestore: undefined }) }
  function selectCountGroup(group: HeaderStatusGroup) {
    if (!project.value) return
    const selected = filters.value.statusScope === group.id && filters.value.status.length > 0
    update({ status: selected ? [] : groupStates(project.value, group), statusScope: selected ? undefined : group.id })
  }
  function selectCountStatus(state: string) {
    const selected = filters.value.statusScope === 'canonical' && filters.value.status.length === 1 && filters.value.status[0] === state
    update({ status: selected ? [] : [state], statusScope: selected ? undefined : 'canonical' })
  }
  // The temporary override travels with the URL/view, so reload and navigation
  // preserve restoration. There is no per-person or per-project override cache.
  watch(() => [filters.value.status, filters.value.statusScope, filters.value.hideRestore, filters.value.hideStates, project.value], () => {
    if (project.value && filters.value.hideRestore && !selectionHiddenByPolicy(filters.value.status, filters.value.statusScope, project.value.status_counts, filters.value.hideStates, project.value.status_counts_truncated)) {
      if (filters.value.showClosed) { update({ showClosed: false, hideRestore: undefined }); toast('Hide is on again.', { key: 'project-status-hide' }) }
    }
  })
  // Remember each section's filters and view while moving around this project.
  watch(section, () => { creating.value = false; resetPanelOrigin() })
  // A Delivery address without delivery.read leads to the project's tickets once permissions are known.
  watch([deliveryActive, projectId], async ([active, id]) => {
    if (!active || !id) return
    if (await ensurePermissions(id) !== 'known' || !deliveryActive.value || projectId.value !== id || can('delivery.read', id)) return
    void router.replace({ path: sectionPath('tickets'), hash: route.hash })
  }, { immediate: true })
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
  // A Type option stands for every level with its name: they go on and off together.
  function members(dimension: Dimension, value: string): string[] | undefined {
    return dimension === 'type' ? options('type').find(option => option.value === value)?.members : undefined
  }
  function toggleValue(dimension: Dimension, value: string) {
    const group = members(dimension, value)
    const next = group ? toggleMembers(filters.value[dimension], group, 'in') : toggleIn(filters.value[dimension], value)
    const patch: Partial<ListFilters> = { [dimension]: next }
    update(patch)
  }
  function excludeValue(dimension: Dimension, value: string) {
    const group = members(dimension, value)
    update({ [dimension]: group ? toggleMembers(filters.value[dimension], group, 'out') : toggleOut(filters.value[dimension], value) })
  }
  function clearFilters() { update(clearedFilters()) }
  function setDate(date: DateFilter | null) { update({ date }) }
  function sortBy(field: SortField, additive: boolean) { update({ sort: cycleSort(filters.value.sort, field, additive) }) }
  function setSort(sort: SortKey[]) { update({ sort }) }

  // ---------- Footer summary (AEON-785): what this list shows ----------
  // The same total the toolbar counts; blocked from the state counts, workers from the live read.
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

  return { options, chipLabel, facetLoading, needOptions, sheetLoading, sheetOpened, update, setHideStates, manualShowClosed, selectCountGroup, selectCountStatus, setSection, setView, toggleValue, excludeValue, clearFilters, setDate, sortBy, setSort, setGroup, setAllGroups, toggleGroup }
}
