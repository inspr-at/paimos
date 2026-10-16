// SPDX-License-Identifier: AGPL-3.0-only

import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { listNodes, type ListItem } from '../../lib/api'
import { WORK_KINDS } from '../../lib/ticketList'
import { rowStore } from '../../lib/rowStore'
import { ticketRef } from '../../lib/ticketLinks'
import { scopeOwner } from '../../lib/identityScope'
import { listNativeMemberships, type NativeReleaseView } from '../../lib/releaseMembership'
import type { ProjectState } from './types'
import type { useProjectData } from './useProjectData'

export function useProjectPanel(state: ProjectState, data: ReturnType<typeof useProjectData>) {
  const { route, router, projects, session, projectKey, ticketKey, recovery, recovering, authorizedProject, project, projectId, routeKey, section, sectionPath, ticketSectionQuery, graphActive, ticketGraphView, outlineActive, table, panel, scrollRoot, cursorId, fullView, ticketPath } = state
  const { list, outline, sequence, total } = data
  // ---------- Side panel ----------
  const fetched = ref<ListItem | null>(null)
  const panelError = ref('')
  const panelLoading = ref(false)
  let panelGeneration = 0
  const panelItem = computed(() => {
    if (recovering.value) return recovery.value!.item
    const key = ticketKey.value.toLowerCase()
    if (!key) return null
    return list.rows.value.find(row => row.key.toLowerCase() === key) ?? outline.findByKey(key) ?? (fetched.value?.key.toLowerCase() === key ? fetched.value : null)
  })
  watch(() => session.requiresSignIn, ended => {
    const who = session.identity, current = authorizedProject.value, item = panelItem.value
    if (!ended || !who || !current || !item || !panel.value?.isDirty()) return
    recovery.value = {
      owner: scopeOwner(who), projectKey: projectKey.value, ticketKey: ticketKey.value,
      project: { ...current }, person: { id: who.principal.id, name: who.principal.name },
      item: { ...item, fields: { ...item.fields } },
    }
  }, { flush: 'sync' })
  watch(() => scopeOwner(session.identity), owner => {
    // A new authenticated document/owner never resumes the old editor, even if
    // it opens the same project and node. Drop pending lookups and list answers.
    if (owner) recovery.value = null
    panelGeneration++
    list.invalidate()
    fetched.value = null
    list.rows.value = []
  }, { flush: 'sync' })
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
    // Invalidate an older lookup even when the new route is empty or cached.
    const request = ++panelGeneration
    const key = ticketKey.value
    const within = projectId.value
    const owner = scopeOwner(session.identity)
    panelError.value = ''
    panelLoading.value = false
    if (!key || !within || !owner) return
    const current = () => request === panelGeneration && owner === scopeOwner(session.identity) && session.authenticationCurrent()
    if (list.rows.value.some(row => row.key.toLowerCase() === key.toLowerCase()) || fetched.value?.key.toLowerCase() === key.toLowerCase()) return
    panelLoading.value = true
    try {
      const sent = rowStore.mark()
      const result = await listNodes({ within, q: key, sort: 'key', limit: 50 })
      if (!current()) return
      const hit = result.items.find(item => item.key.toLowerCase() === key.toLowerCase())
      // The row store's display object for it; one it knows deleted opens as gone.
      if (hit) fetched.value = rowStore.adopt(hit, sent) ?? hit
      else if (!list.rows.value.some(row => row.key.toLowerCase() === key.toLowerCase())) panelError.value = `${key.toUpperCase()} is not part of ${project.value?.title ?? 'this project'}.`
    } catch (e) {
      if (current()) panelError.value = e instanceof Error ? e.message : 'The ticket could not be loaded.'
    } finally {
      if (current()) panelLoading.value = false
    }
  }
  watch([ticketKey, projectId], resolvePanel, { immediate: true })
  // A lookup can find the store row before its list page lands. Object identity
  // then stays the same, so also observe when that row joins the visible view.
  watch([panelItem, () => sequence.value.some(row => row.id === panelItem.value?.id)], ([item, shown], [before]) => {
    if (!item) return
    if (item !== before && outlineActive.value) outline.reveal(item.id)
    if (shown) {
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
      if (!row || seen.has(row.id)) return
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

  // Discover everyone assigned in the project only when an assignment menu needs
  // them. Cold ticket navigation must not scan assignees and fetch each name.
  const projectPeople = ref<string[]>([])
  let peopleGeneration = 0
  let projectPeopleLoad: Promise<void> | null = null
  watch([projectId, () => scopeOwner(session.identity)], () => {
    peopleGeneration++
    projectPeople.value = []
    projectPeopleLoad = null
  }, { flush: 'sync' })
  onBeforeUnmount(() => { peopleGeneration++ })
  function loadProjectPeople(): Promise<void> {
    const id = projectId.value
    if (!id) return Promise.resolve()
    if (projectPeopleLoad) return projectPeopleLoad
    const request = peopleGeneration
    projectPeopleLoad = (async () => {
      try {
        const page = await listNodes({ within: id, kind: WORK_KINDS, facets: ['assignee'], limit: 1 })
        if (request !== peopleGeneration) return
        const ids = Object.keys(page.facets?.assignee ?? {}).filter(value => value !== 'none')
        await list.resolveNames(ids)
        if (request === peopleGeneration) projectPeople.value = ids
      } catch {
        // A later opening retries; already loaded people remain available.
        if (request === peopleGeneration) projectPeopleLoad = null
      }
    })()
    return projectPeopleLoad
  }
  const people = computed(() => {
    const known = new Map<string, string>()
    for (const row of [...list.rows.value, panelItem.value]) {
      if (row?.project?.id === projectId.value && row.assignee) known.set(row.assignee.id, row.assignee.name)
    }
    for (const id of projectPeople.value) known.set(id, list.names.get(id) ?? 'Someone')
    return [...known].map(([id, name]) => ({ id, name }))
  })

  let openedFromList = false
  let openedQuery = ''
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
    // Markdown displays the key that was written, but its resolved link may
    // name a moved record. Use the same canonical key and owner as its href.
    const resolved = ticketRef(key)
    const target = resolved ? projects.byId(resolved.projectId) : undefined
    if (resolved && target) {
      follow(`/p/${encodeURIComponent(target.routeKey)}/${encodeURIComponent(resolved.key)}`)
      return
    }
    const from = route.fullPath
    const ownerScope = scopeOwner(session.identity)
    const stale = () => route.fullPath !== from || scopeOwner(session.identity) !== ownerScope
    const here = key.split('-')[0] === routeKey.value || list.rows.value.some(row => row.key === key)
    if (!here) {
      try {
        const page = await listNodes({ q: key, limit: 25 })
        if (stale()) return
        const owner = page.items.find(item => item.key === key)?.project
        const target = owner ? projects.byId(owner.id) : undefined
        if (target && target.id !== projectId.value) { follow(`/p/${encodeURIComponent(target.routeKey)}/${encodeURIComponent(key)}`); return }
      } catch { /* fall back to this project, where the panel explains */ }
    }
    if (stale()) return
    if (ticketKey.value) follow(ticketPath(key)); else openKey(key)
  }
  watch(ticketKey, key => { if (!key) openedFromList = false })

  function getPeopleGeneration() { return peopleGeneration }
  function resetPanelOrigin() { openedFromList = false }
  function newTab(key: string) {
    const project = projects.projects.find(p => key.startsWith(`${p.routeKey}-`))
    const path = project ? `/p/${encodeURIComponent(project.routeKey)}/${encodeURIComponent(key)}` : ticketPath(key)
    const query = !project || project.id === projectId.value ? { ...listQuery(), ...ticketSectionQuery() } : {}
    window.open(router.resolve({ path, query }).href, '_blank', 'noopener')
  }

  return { fetched, panelError, panelLoading, panelItem, panelPosition, resolvePanel, nativeReleases, refreshMemberships, loadProjectPeople, people, openKey, openRow, listQuery, closePanel, expand, collapse, trail, trailBack, openRelated, getPeopleGeneration, resetPanelOrigin, newTab }
}
