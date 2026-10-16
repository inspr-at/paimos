// SPDX-License-Identifier: AGPL-3.0-only

import { computed, ref, shallowRef, watch } from 'vue'
import { type SavedView } from '../../lib/api'
import { hideLabel } from '../../lib/hideStates'
import { PINNED } from '../../lib/columns'
import { copyName, duplicateView, loadViews, removeView, renameView, saveNewView, saveViewState, shareView, viewsOf } from '../../lib/savedViews'
import { SAVED_VIEW } from '../../lib/viewSwitch'
import { savedViewId } from '../../lib/ticketListSwitch'
import { bareProjectEntry, type ProjectSection } from '../../components/work/projectNavigation'
import { toast } from '../../lib/toast'
import { apiParams, filtersFromView, filtersToQuery, hasFilters, sameListState, suggestName, type ListFilters } from '../../lib/ticketList'
import { scopeOwner } from '../../lib/identityScope'
import { useViewFigures } from '../../lib/useViewFigures'
import { hidesRows, workHeader, workSlots, type WorkSlots } from '../../lib/workFigures'
import ViewBar from '../../components/work/ViewBar.vue'
import type { ProjectState } from './types'
import type { useProjectData } from './useProjectData'
import type { useProjectFilters } from './useProjectFilters'
import type { useProjectColumns } from './useProjectColumns'

export function useProjectViews(state: ProjectState, data: ReturnType<typeof useProjectData>, columns: ReturnType<typeof useProjectColumns>, controls: ReturnType<typeof useProjectFilters>) {
  const { route, router, session, ticketKey, project, projectId, section, ticketsHeader, sectionPath, filters, activeTicketView, knowledgeActive, settingsActive, deliveryActive, graphActive, outlineActive, toolbarWrap, scrollRoot, collapsed, me, sectionSwitch, ticketSwitch, ticketOpeningQuery } = state
  const { list, outline } = data
  const { listPrefs, toolbarColumns } = columns
  const { chipLabel, update } = controls
  // ---------- Saved views ----------
  const views = computed(() => viewsOf(projectId.value))
  const activeView = computed(() => filters.value.view ? views.value.items.find(view => view.id === filters.value.view) ?? null : null)
  const viewFilters = computed(() => activeView.value ? filtersFromView(activeView.value) : null)
  const savedFilters = computed(() => ({ ...filters.value, mode: activeTicketView.value.id as ListFilters['mode'], cols: toolbarColumns.value.order.filter(id => toolbarColumns.value.visible.includes(id) && !PINNED.includes(id)) }))
  const customised = computed(() => !!listPrefs.value?.visible || filters.value.mode !== 'list' || hasFilters(filters.value) || filters.value.sort.length > 0 || filters.value.group !== 'none' || !!filters.value.cols || filters.value.showClosed || !!filters.value.hideStates)
  const viewDirty = computed(() => !!viewFilters.value && !sameListState(savedFilters.value, viewFilters.value))
  const canSaveView = computed(() => !knowledgeActive.value && !settingsActive.value && !deliveryActive.value && (activeView.value ? viewDirty.value : customised.value))
  // The saved-view strip shows once there is a view to pick or a list worth keeping; the plain list alone needs no strip.
  const showViewBar = computed(() => !knowledgeActive.value && !settingsActive.value && !deliveryActive.value && (views.value.items.length > 0 || !!activeView.value || canSaveView.value))
  // The saved view the project opens with: the ticket list switch's default (AEON-1453).
  const defaultViewId = computed(() => savedViewId(ticketSwitch.defaultId.value))
  function viewQuery(view: SavedView | null): Record<string, string> {
    return view ? filtersToQuery(filtersFromView(view)) : {}
  }
  function hrefFor(id: string | null) {
    const view = id ? views.value.items.find(item => item.id === id) ?? null : null
    const query = new URLSearchParams(view ? viewQuery(view) : { ...(activeTicketView.value.id !== 'list' ? { view: activeTicketView.value.id } : {}) }).toString()
    return `${sectionPath('tickets')}${query ? `?${query}` : ''}`
  }
  function openView(id: string | null, replace = false) {
    const view = id ? views.value.items.find(item => item.id === id) ?? null : null
    collapsed.value = new Set()
    const query = view ? viewQuery(view) : { ...(activeTicketView.value.id !== 'list' ? { view: activeTicketView.value.id } : {}) }
    const location = { path: sectionPath('tickets'), query }
    if (replace) void router.replace(location); else void router.push(location)
  }
  // A link to a view someone cannot see (private, or deleted) keeps its filters.
  watch([() => filters.value.view, () => views.value.loaded], ([id, loaded]) => {
    if (!id || !loaded) return
    const view = views.value.items.find(view => view.id === id)
    if (!view) { update({ view: null }); return }
    // Bare view links adopt the stored presentation; explicit URL edits stay dirty.
    const query = { ...route.query }
    if (query.view === undefined) query.view = view.mode
    if (query.cols === undefined && view.columns.length) query.cols = view.columns.join(',')
    if (query.view !== route.query.view || query.cols !== route.query.cols) void router.replace({ path: route.path, query })
  })
  // The project opens as the person prefers when nothing else was asked for (AEON-1453):
  // a bare /p/KEY opens the default or last used section, and a plain ticket list the
  // list switch's default (a mode or a saved view) or the mode used last. A link (a section
  // address, ?view=, filters, a ticket) wins once and changes neither. The list waits for
  // that answer, so it never shows the plain list first.
  const entryResolved = ref(false)
  let resolvedFor = ''
  // Counted when the address changes, so a result that resumes after the preference
  // read cannot replace a ticket or a section opened meanwhile (AEON-1453).
  let navigation = 0
  watch(() => [ticketKey.value, section.value, route.path, JSON.stringify(route.query ?? {})], () => { navigation++ })
  watch(projectId, async id => {
    if (!id || resolvedFor === id) return
    resolvedFor = id
    const generation = navigation
    const openedTicket = ticketKey.value
    const bare = bareProjectEntry(route)
    const plain = () => section.value === 'tickets' && !ticketKey.value && Object.keys(route.query).length === 0
    if (ticketKey.value || (!bare && !plain())) { entryResolved.value = true; void loadViews(id); return }
    entryResolved.value = false
    await Promise.race([Promise.all([loadViews(id), sectionSwitch.ready(), ticketSwitch.ready()]), new Promise(resolve => setTimeout(resolve, 1500))])
    const stillHere = () => projectId.value === id && navigation === generation && ticketKey.value === openedTicket
    if (!stillHere()) {
      if (projectId.value === id) { entryResolved.value = true; void loadViews(id) }
      return
    }
    const target = sectionSwitch.preferred.value
    if (bare && plain() && stillHere() && target !== 'tickets') await router.replace({ path: sectionPath(target as ProjectSection) })
    else if (plain() && stillHere()) {
      const query = ticketOpeningQuery()
      if (Object.keys(query).length && stillHere()) await router.replace({ path: route.path, query })
    }
    if (projectId.value === id) entryResolved.value = true
  }, { immediate: true })

  const queryKey = computed(() => projectId.value && entryResolved.value ? JSON.stringify(apiParams(projectId.value, filters.value)) : '')

  // ---------- Work at a glance (AEON-1358): the view's work in hours, and the column figures ----------
  // Counted in the background after the list paints, per scope (never Hide closed); the project's summary changing
  // (a status, an estimate, a new ticket) counts the shown scope again quietly.
  const { figures: viewFigures, whole: wholeFigures, busy: figuresBusy, failed: figuresFailed, narrowed: figuresNarrowed, retry: retryFigures } = useViewFigures({
    owner: computed(() => scopeOwner(session.identity)), projectId, filters, ready: entryResolved, listLoading: list.loading,
    freshness: computed(() => project.value ? [project.value.last_activity, project.value.open, project.value.in_progress, project.value.done, project.value.total].join('|') : ''),
  })
  const workHead = computed(() => viewFigures.value ? workHeader(viewFigures.value) : null)
  // The value slots are fixed from the whole project when it opens, like the status counts' digits.
  const workBarSlots = shallowRef<WorkSlots | null>(null)
  watch(projectId, () => { workBarSlots.value = null })
  watch(wholeFigures, value => { if (value && !workBarSlots.value) workBarSlots.value = workSlots(workHeader(value)) }, { immediate: true })
  const hideOn = computed(() => ticketsHeader.value && !filters.value.showClosed)
  const workHideLabel = computed(() => hideOn.value ? hideLabel(filters.value.hideStates) : null)
  const columnFigures = computed(() => ({
    figures: figuresBusy.value || figuresFailed.value ? null : viewFigures.value,
    failed: figuresFailed.value,
    hiddenRows: hidesRows(viewFigures.value, hideOn.value, filters.value.hideStates),
  }))
  // The Outline without filters loads its own levels; the list query then only supplies
  // counts. With filters or Hide closed, the Outline needs the list's whole match set.
  const listLoadMode = computed(() => graphActive.value ? 'graph' : knowledgeActive.value || settingsActive.value || deliveryActive.value ? 'counts' : !outlineActive.value ? 'list' : outline.matchMode.value ? 'all' : 'counts')
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

  // ---------- Saved view actions ----------
  const savePanel = ref<{ mode: 'create' | 'rename'; anchor: HTMLElement; view?: SavedView; name: string } | null>(null)
  const saveBusy = ref(false)
  const saveError = ref('')
  const viewBar = ref<InstanceType<typeof ViewBar>>()
  watch([projectId, () => me.value?.id], () => { savePanel.value = null; saveError.value = ''; saveBusy.value = false })
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
  async function submitSave(value: { name: string; shared: boolean }) {
    const panel = savePanel.value, id = projectId.value, person = me.value?.id, state = { ...savedFilters.value, view: null }
    if (!panel || !id) return
    saveBusy.value = true; saveError.value = ''
    try {
      if (panel.mode === 'rename' && panel.view) {
        const saved = await renameView(id, panel.view, value.name)
        if (projectId.value !== id || me.value?.id !== person || savePanel.value !== panel) return
        toast(`Renamed the view to “${saved.name}”`)
      } else {
        const saved = await saveNewView(id, value.name, state, value.shared)
        if (projectId.value !== id || me.value?.id !== person || savePanel.value !== panel) return
        if (sameListState(savedFilters.value, state)) update({ view: saved.id, cols: filtersFromView(saved).cols })
        toast(`Saved the view “${saved.name}”${saved.shared ? ', shared with the project' : ''}`)
      }
      closeSave(false)
    } catch (e) {
      if (projectId.value === id && savePanel.value === panel) saveError.value = problem(e)
    } finally {
      saveBusy.value = false
    }
  }
  async function saveActive(view: SavedView) {
    const id = projectId.value, person = me.value?.id, state = { ...savedFilters.value, view: null }
    if (!id) return
    try {
      const saved = await saveViewState(id, view, state)
      if (projectId.value !== id || me.value?.id !== person || filters.value.view !== view.id) return
      if (sameListState(savedFilters.value, state)) update({ cols: filtersFromView(saved).cols })
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
  // Right-click (or the menu) on a saved view: the project opens with it, or stops (AEON-1453).
  function setDefaultView(view: SavedView | null) {
    ticketSwitch.setDefault(view ? `${SAVED_VIEW}${view.id}` : null)
    toast(view ? `${project.value?.title ?? 'The project'} opens with “${view.name}” for you` : `${project.value?.title ?? 'The project'} no longer opens with a saved view`)
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

  return { views, activeView, viewDirty, canSaveView, showViewBar, defaultViewId, hrefFor, openView, figuresBusy, figuresFailed, figuresNarrowed, retryFigures, workHead, workBarSlots, workHideLabel, columnFigures, savePanel, saveBusy, saveError, viewBar, startSave, startRename, closeSave, submitSave, saveActive, duplicate, setDefaultView, share, copyViewLink, remove }
}
