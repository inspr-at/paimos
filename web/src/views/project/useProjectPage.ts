// SPDX-License-Identifier: AGPL-3.0-only
import { setPageTitle } from '../../lib/brand'
import { computed, inject, provide, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { confirmAction } from '../../lib/confirm'
import { kinds } from '../../lib/useTicket'
import { normaliseState } from '../../lib/work'
import { ticketsFooter, withAgentsIsPartial } from '../../lib/footerProviders'
import { useFooterSummary } from '../../lib/footerSummary'
import { useSession } from '../../stores/session'
import { projectSection } from '../../components/work/projectNavigation'
import BulkBar from '../../components/work/BulkBar.vue'
import { TICKET_PEEK } from '../../lib/ticketPeek'
import { useProjectState } from './useProjectState'
import { useProjectKnowledge } from './useProjectKnowledge'
import { useProjectData } from './useProjectData'
import { useProjectPanel } from './useProjectPanel'
import { useProjectFilters } from './useProjectFilters'
import { useProjectColumns } from './useProjectColumns'
import { useProjectViews } from './useProjectViews'
import { useProjectSelection } from './useProjectSelection'
import { useProjectActions } from './useProjectActions'
import { useProjectLive } from './useProjectLive'
import { useProjectKeyboard } from './useProjectKeyboard'

export function useProjectPage() {
  const state = useProjectState()
  const knowledgePage = useProjectKnowledge(state, () => pane.listQuery())
  const data = useProjectData(state, {
    openRow: row => pane.openRow(row),
    quiet: id => !!state.ticketKey.value && pane.panelItem.value?.id === id,
    holds: id => state.ticketKey.value && pane.panelItem.value?.id === id
      ? !!state.panel.value?.busy() || !!state.panel.value?.isDirty()
      : selection.selected.value.has(id) || state.statusMenu.value?.row.id === id,
    blockers: () => ({
      selected: selection.selected.value.size + (selection.phonePicking.value ? 1 : 0),
      editing: state.creating.value || !!data.outline.createUnder.value || !!state.panel.value?.busy() || !!state.panel.value?.isDirty() || !!state.table.value?.createDirty(),
      menuOpen: !!state.statusMenu.value || !!selection.bulkMenu.value || !!views.savePanel.value || !!document.querySelector('.floating'),
      dialogOpen: !!document.querySelector('dialog[open]'),
    }),
    around: (removing, apply) => live.aroundLiveApply(removing, apply),
    applied: () => live.liveApplied(),
  })
  const pane = useProjectPanel(state, data)
  const controls = useProjectFilters(state, data, pane.resetPanelOrigin)
  const columns = useProjectColumns(state, data, controls.update)
  const views = useProjectViews(state, data, columns, controls)
  const selection = useProjectSelection(state, data, pane)
  const actions = useProjectActions(state, data, pane, knowledgePage)
  const live = useProjectLive(state, data, pane)
  const keyboard = useProjectKeyboard(state, data, pane, selection, actions, live)
  const { projects, session, ticketKey, project, filters, now, knowledgeActive, knowledgeEntryOpen, outlineActive, toolbarWrap, stickMark, table, panel, scrollRoot, toolbarHeight, stuck, cursorId, skipGuard } = state
  const { list, outline, liveActive, liveAgents, liveDataStale, liveUpdatedAt, sequence, total, filtered } = data
  const { panelItem } = pane
  const { knowledgeEntry, knowledgeDocked } = knowledgePage
  const { toggleValue, clearFilters } = controls
  const appPeek = inject(TICKET_PEEK, null)
  provide(TICKET_PEEK, {
    open: (key, from) => { if (ticketKey.value) void pane.openRelated(key); else appPeek?.open(key, from) },
    openKey: computed(() => ticketKey.value ? ticketKey.value.toUpperCase() : appPeek?.openKey.value ?? null),
  })
  // ---------- Footer summary (AEON-785): what this list shows ----------
  // The same total the toolbar counts; blocked from the state counts, workers from the live read.
  useFooterSummary(() => {
    // A first read that failed shows its own error; the footer does not wait for it.
    if (!session.identity || !project.value || !liveActive.value || (list.error.value && !list.loadedOnce.value)) return null
    const states = list.facets.value.state ?? {}
    const blocked = Object.entries(states).reduce((sum, [state, n]) => sum + (normaliseState(state) === 'blocked' ? n : 0), 0)
    const loadedRows = outlineActive.value ? outline.rows.value : list.rows.value
    const loaded = new Set(loadedRows.map(row => row.id))
    const hasMore = outlineActive.value ? outline.hasMoreRoot.value || !!list.cursor.value : !!list.cursor.value
    const worked = new Set(liveAgents.forProject(project.value.id).flatMap(agent => agent.ticket ? [agent.ticket.id] : []))
    const partial = withAgentsIsPartial(filtered.value, loaded.size, total.value, hasMore)
    return ticketsFooter({
      loaded: list.loadedOnce.value, total: total.value, blocked, filtered: filtered.value,
      withAgents: filtered.value ? [...worked].filter(id => loaded.has(id)).length : worked.size,
      withAgentsPartial: partial,
      act: {
        blocked: () => { if (!filters.value.status.some(state => normaliseState(state) === 'blocked')) toggleValue('status', 'blocked') },
        top: () => { if (scrollRoot.value) scrollRoot.value.scrollTo({ top: 0, behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' }) },
        clear: clearFilters,
      },
    })
  }, () => liveActive.value && liveUpdatedAt.value !== null ? { updatedAt: liveUpdatedAt.value, paused: liveDataStale.value } : null)
  // ---------- Unsaved changes ----------
  function dirty() { return !!panel.value?.isDirty() || !!knowledgeEntry.value?.isDirty() }
  async function confirmDiscard() {
    if (skipGuard.value || !dirty()) return true
    const outgoing = panel.value, outgoingKnowledge = knowledgeEntry.value
    const ok = await confirmAction({ title: 'Discard unsaved changes?', body: 'You have edits in this ticket that are not saved yet.', confirmLabel: 'Discard changes', danger: true })
    if (ok) { outgoing?.discard(); outgoingKnowledge?.discard() }
    return ok
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
    return (await confirmDiscard()) && (!table.value?.createDirty() || skipGuard.value || confirmAction({ title: 'Discard the new ticket?', body: 'Its title has not been created yet.', confirmLabel: 'Discard', danger: true }))
  })
  function beforeUnload(event: BeforeUnloadEvent) { if (dirty() || table.value?.createDirty()) { event.preventDefault(); event.returnValue = '' } }
  // Tabbing into the table lands on a visible row, not on an invisible container.
  function focusFirst() { if (!cursorId.value && sequence.value.length) cursorId.value = sequence.value[0].id }

  const bulkBar = ref<InstanceType<typeof BulkBar>>()
  const bulkHeight = ref(0)
  let bulkResize: ResizeObserver | undefined
  watch(bulkBar, bar => {
    bulkResize?.disconnect()
    bulkHeight.value = 0
    const element = bar?.$el.querySelector('.bulk-bar') as HTMLElement | undefined
    if (!element) return
    bulkHeight.value = element.offsetHeight
    bulkResize = new ResizeObserver(() => { bulkHeight.value = element.offsetHeight })
    bulkResize.observe(element)
  }, { flush: 'post' })
  // ---------- Layout: sticky toolbar height and stuck state ----------
  let resize: ResizeObserver | undefined
  let stick: IntersectionObserver | undefined
  let clock: ReturnType<typeof setInterval> | undefined
  onMounted(() => {
    scrollRoot.value = document.getElementById('main')
    void projects.load()
    void kinds().catch(() => { /* The server completion gate remains authoritative. */ })
    window.addEventListener('beforeunload', beforeUnload)
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
    window.removeEventListener('beforeunload', beforeUnload)
    clearInterval(clock)
    resize?.disconnect()
    stick?.disconnect()
    bulkResize?.disconnect()
  })

  // ---------- Document title ----------
  watch([project, panelItem, knowledgeActive, knowledgeEntryOpen, knowledgeDocked], ([current, item, knowledgeOn, entryOn, docked]) => {
    if (!current || entryOn || docked) return
    setPageTitle(item ? `${item.key} ${item.title}` : knowledgeOn ? `Knowledge · ${current.routeKey} ${current.title}` : `${current.routeKey} ${current.title}`)
  }, { immediate: true })

  return { ...state, ...knowledgePage, ...data, ...columns, ...pane, ...controls, ...views, ...actions, ...selection, ...live, ...keyboard, bulkBar, bulkHeight, focusFirst }
}
