// SPDX-License-Identifier: AGPL-3.0-only

import { computed, nextTick, onBeforeUnmount, provide, reactive, ref, toRefs, watch } from 'vue'
import { isNavigationFailure, NavigationFailureType, routeLocationKey, routerKey, type RouteLocationRaw } from 'vue-router'
import { settledNavigation } from '../../lib/navigation'
import type KnowledgeEntryPageType from '../../components/knowledge/KnowledgeEntryPage.vue'
import type KnowledgeTabType from '../../components/knowledge/KnowledgeTab.vue'
import { DOCK_MEDIA, entryPath, parseEntryParam, type KnowledgeEntry, type KnowledgeType } from '../../lib/knowledge'
import { filtersToQuery as knowledgeQuery, useKnowledge, type KnowledgeFilters } from '../../lib/useKnowledge'
import type { ProjectState } from './types'
import type { LocationQuery } from 'vue-router'

export function useProjectKnowledge(state: ProjectState, listQuery: () => LocationQuery) {
  const { route, router, projects, projectKey, ticketKey, projectId, routeKey, ticketSectionQuery, knowledgeActive, knowledgeType, knowledgeSlug, knowledgeEntryOpen, knowledgeFilters, knowledgeView, knowledgeDisplay } = state
  // KG2 owns the Knowledge implementation. Adapt its legacy route reads locally
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

  onBeforeUnmount(() => { knowledge.stop(); dockQuery.removeEventListener('change', onDockWidth) })

  return { knowledgeListQuery, knowledge, knowledgeTab, knowledgeEntry, knowledgeWide, knowledgeDocked, shownEntry, learningAccepted, learningReverted, updateKnowledge, closeKnowledgeDock, closeKnowledgeEntry }
}
