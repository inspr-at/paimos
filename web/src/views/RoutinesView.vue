<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getRecurrence, listRoutines, pauseRecurrence, runRecurrence } from '../lib/api'
import { getMembers } from '../lib/access'
import { can, myWorkspaceRole } from '../lib/authz'
import type { After } from '../lib/identityScope'
import { ROUTINE_STATE_WORDS, reasonWords, recurrenceName, recurrenceZone, routineState, when, type Recurrence, type RecurrenceResult } from '../lib/recurrences'
import { SCOPE_FILTERS, STATE_FILTERS, STATE_LOOK, countWords, matchesScope, parseScope, parseState, scopeOf, stateCounts, type ScopeFilter } from '../lib/routineRuns'
import { toast } from '../lib/toast'
import { useIdentityScope } from '../lib/useIdentityScope'
import { usePoller } from '../lib/usePolledData'
import type { RoutineState } from '../lib/recurrences'
import { useProjects } from '../stores/projects'
import { useSession } from '../stores/session'
import AppIcon, { type IconName } from '../components/AppIcon.vue'
import KeyCap from '../components/KeyCap.vue'
import FloatingPanel from '../components/work/FloatingPanel.vue'
import RecurrenceEditor from '../components/recurrences/RecurrenceEditor.vue'
import RecurrenceRun from '../components/recurrences/RecurrenceRun.vue'
import RoutineLogbook from '../components/routines/RoutineLogbook.vue'
import RoutineRow from '../components/routines/RoutineRow.vue'

// Agents › Routines (AEON-1192): every routine this person can see, listed once,
// wherever it was made (a project's box, a ticket's Repeat…, Settings › Personal or
// Settings › Routines). Filters for scope, project and state live in the query, so a
// link reopens the same view; the selected routine's logbook opens beside the list
// (?routine=, ?run=). Narrow screens show the list, then one logbook with a way back.
const route = useRoute(), router = useRouter(), session = useSession(), projects = useProjects()
const ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const person = computed(() => !!session.identity && session.identity.principal.kind !== 'agent')
// Every person asks: the server lists what their workspace or project access lets them read.
const identity = useIdentityScope(() => person.value)
const reads = identity.lane(), links = identity.lane(), names = identity.lane()
const items = ref<Recurrence[]>([]), cursor = ref<string | null>(null), loading = ref(false), loaded = ref(false), failure = ref(''), moreFailure = ref('')
const denied = ref(false), refreshing = ref(false), pagesRead = ref(0), known = computed(() => loaded.value && !denied.value)
const linked = ref<Recurrence | null>(null), linkFailure = ref(''), closed = ref(false), revoked = ref(false), busy = ref<string[]>([])
const editor = ref<{ item: Recurrence; consent?: boolean } | null>(null), runPrompt = ref<Recurrence | null>(null)
const menu = ref<{ type: 'new' | 'more'; item?: Recurrence; anchor: HTMLElement } | null>(null)
const owners = ref(new Map<string, string>())
const me = computed(() => session.identity?.principal.id ?? '')
const workspaceAdmin = computed(() => ['owner', 'admin'].includes(myWorkspaceRole()?.key ?? ''))

// ---------- The query: scope, project, state, routine, run ----------
const scopeFilter = computed(() => parseScope(route.query.scope))
const projectFilter = computed(() => typeof route.query.project === 'string' && /^[A-Za-z0-9_-]{1,64}$/.test(route.query.project) ? route.query.project : '')
const stateFilter = computed(() => parseState(route.query.state))
const routineId = computed(() => typeof route.query.routine === 'string' && ID.test(route.query.routine) ? route.query.routine : '')
const runId = computed(() => typeof route.query.run === 'string' && ID.test(route.query.run) ? route.query.run : '')
function queryWith(patch: Record<string, string | undefined>) {
  const next: Record<string, string> = {}
  for (const [key, value] of Object.entries({ ...route.query, ...patch })) if (typeof value === 'string' && value) next[key] = value
  if (next.scope === 'all') delete next.scope
  return { path: '/agents/routines', query: next }
}
const setQuery = (patch: Record<string, string | undefined>) => void router.replace(queryWith(patch))

// ---------- Rows: one per definition, filtered on screen, counted from loaded pages ----------
const projectKeyOf = (item: Recurrence) => projects.byId(item.project_id)?.routeKey ?? ''
const byName = (a: Recurrence, b: Recurrence) => recurrenceName(a).localeCompare(recurrenceName(b)) || a.id.localeCompare(b.id)
const scoped = computed(() => items.value.filter(item => matchesScope(item, scopeFilter.value) && (!projectFilter.value || item.project_id === projectFilter.value)))
const counts = computed(() => stateCounts(scoped.value))
const rows = computed(() => (stateFilter.value ? scoped.value.filter(item => routineState(item) === stateFilter.value) : scoped.value).slice().sort(byName))
const more = computed(() => !!cursor.value)
const scopeCount = (scope: ScopeFilter) => countWords(items.value.filter(item => matchesScope(item, scope)).length, more.value)
const projectOptions = computed(() => {
  const ids = new Set(items.value.map(item => item.project_id))
  if (projectFilter.value) ids.add(projectFilter.value)
  return [...ids].map(id => ({ id, key: projects.byId(id)?.routeKey ?? 'Another project' })).sort((a, b) => a.key.localeCompare(b.key))
})
const filtered = computed(() => scopeFilter.value !== 'all' || !!projectFilter.value || !!stateFilter.value)
watch([scopeFilter, projectFilter, stateFilter], () => { closed.value = false })
function toggleState(state: RoutineState) {
  if (stateFilter.value === state) setQuery({ state: undefined })
  else if (counts.value[state]) setQuery({ state })
}

// ---------- Selection: wide screens show the first row; narrow ones a chosen logbook only ----------
const pageEl = ref<HTMLElement>(), single = ref(window.innerWidth < 1160)
let observer: ResizeObserver | undefined
const chosen = computed<Recurrence | undefined>(() => {
  const id = routineId.value
  if (id) {
    const row = rows.value.find(item => item.id === id)
    if (row) return row
    if (!items.value.some(item => item.id === id) && linked.value?.id === id) return linked.value
  }
  return single.value || closed.value ? undefined : rows.value[0]
})
let openedFromList = false
function select(id: string) {
  closed.value = false
  if (!single.value) { setQuery({ routine: id, run: undefined }); return }
  // Narrow screens: the logbook takes the list's place, its head in view.
  openedFromList = true
  void router.push(queryWith({ routine: id, run: undefined })).then(() => nextTick()).then(() => {
    const column = document.querySelector<HTMLElement>('.routines-page .lb-col')
    if (column && column.getBoundingClientRect().top < 0) column.scrollIntoView({ block: 'start' })
  })
}
async function back() {
  const id = chosen.value?.id
  const fromList = openedFromList && typeof window.history.state?.back === 'string' && window.history.state.back.startsWith('/agents/routines')
  openedFromList = false
  if (fromList) {
    // Back is a history step: focus returns once the list is shown again.
    const shown = new Promise<void>(resolve => { const stop = watch(routineId, value => { if (!value) { stop(); resolve() } }); setTimeout(() => { stop(); resolve() }, 1000) })
    router.back()
    await shown
  } else await router.replace(queryWith({ routine: undefined, run: undefined }))
  await nextTick()
  if (id) document.querySelector<HTMLElement>(`.routines-page [data-routine="${id}"]`)?.focus({ preventScroll: true })
}
function focusRow(id: string) { const row = document.querySelector<HTMLElement>(`.routines-page [data-routine="${id}"]`); row?.focus({ preventScroll: true }); row?.scrollIntoView({ block: 'nearest' }) }
function listKeys(event: KeyboardEvent) {
  if (event.metaKey || event.ctrlKey || event.altKey) return
  const row = (event.target as HTMLElement).closest<HTMLElement>('[data-routine]')
  if (!row) return
  const ids = rows.value.map(item => item.id), at = ids.indexOf(row.dataset.routine ?? '')
  if (['ArrowDown', 'j', 'ArrowUp', 'k'].includes(event.key)) {
    event.preventDefault(); event.stopPropagation()
    const next = ids[at + (['ArrowDown', 'j'].includes(event.key) ? 1 : -1)]
    if (!next) return
    focusRow(next)
    if (!single.value) select(next)
  } else if (event.key === 'n') {
    const item = rows.value[at]
    if (item && mayManageItem(item)) { event.preventDefault(); event.stopPropagation(); run(item) }
  }
}

// ---------- Reads ----------
function clear() {
  reads.cancel(); links.cancel(); names.cancel()
  items.value = []; cursor.value = null; loaded.value = false; loading.value = false; refreshing.value = false; failure.value = ''; moreFailure.value = ''; denied.value = false; pagesRead.value = 0
  linked.value = null; linkFailure.value = ''; closed.value = false; revoked.value = false; busy.value = []; editor.value = null; runPrompt.value = null; menu.value = null; owners.value = new Map()
}
function cancelReads() { reads.cancel(); loading.value = false; refreshing.value = false }
// First reads page one; More appends the next page; a refresh reads the pages loaded so
// far again, following the fresh cursors, so later pages and the routine on screen stay.
function load(mode: 'first' | 'more' | 'refresh' = 'first') {
  if (!identity.owner.value) return
  if ((mode === 'more' && !cursor.value) || (mode === 'refresh' && (loading.value || refreshing.value))) return
  const start = mode === 'more' ? cursor.value ?? undefined : undefined, pages = mode === 'refresh' ? Math.max(1, pagesRead.value) : 1, before = items.value, fresh: Recurrence[] = []
  // A refresh runs quietly; First or More replaces it on the lane, so it no longer counts as running.
  if (mode === 'refresh') refreshing.value = true; else { loading.value = true; refreshing.value = false }
  if (mode === 'more') moreFailure.value = ''; else if (mode === 'first') failure.value = ''
  const read = (then: After, signal: AbortSignal, after: string | undefined, count: number): Promise<void> => then(listRoutines({ after }, signal), page => {
    if (!Array.isArray(page?.items)) throw new Error('The routines answer was not a list.')
    fresh.push(...page.items)
    if (page.next_cursor && count + 1 < pages) return read(then, signal, page.next_cursor, count + 1)
    // Lists are replaced, never merged across people; More appends this list's next page once per definition.
    const seen = new Set<string>(), unique = (list: Recurrence[]) => list.filter(item => !seen.has(item.id) && !!seen.add(item.id))
    items.value = mode === 'more' ? unique([...items.value, ...fresh]) : unique(fresh)
    cursor.value = page.next_cursor; pagesRead.value = mode === 'more' ? pagesRead.value + 1 : count + 1
    loaded.value = true; denied.value = false
    if (mode !== 'more') failure.value = ''
    if (linked.value && items.value.some(item => item.id === linked.value?.id)) linked.value = null
    if (mode === 'refresh') keepShown(before)
  })
  void reads.run(({ after, signal }) => read(after, signal, start, 0), { failed: error => {
    // No read access in any project: nothing is listed, and that is said, never shown as none.
    if ((error as { status?: number }).status === 403) { deny(); return }
    const reason = error instanceof Error ? error.message : 'the server didn’t answer'
    if (mode === 'more') moreFailure.value = reason; else failure.value = reason
  }, settled: () => { if (mode === 'refresh') refreshing.value = false; else loading.value = false } })
}
function deny() {
  links.cancel()
  items.value = []; cursor.value = null; pagesRead.value = 0; linked.value = null; linkFailure.value = ''; failure.value = ''; moreFailure.value = ''; loaded.value = true; denied.value = true
}
// The routine on screen stays on screen: missing from the fresh pages, it is kept and read on its own.
function keepShown(before: Recurrence[]) {
  const id = routineId.value
  if (!id || items.value.some(item => item.id === id) || linked.value?.id === id) return
  const shown = before.find(item => item.id === id)
  if (!shown) return
  linked.value = shown
  readLinked(true)
}
function loadNames() {
  if (!can('members.read')) return
  void names.run(({ after }) => after(getMembers(), list => { owners.value = new Map(list.people.map(person => [person.principal_id, person.name])) }), { failed: () => {} })
}
// A linked routine that isn't on the loaded pages is read on its own, and again with each
// refresh (again: it is on screen already); gone (404) means a calm line.
function readLinked(again = false) {
  const id = routineId.value
  linkFailure.value = ''
  if (!id || !loaded.value || denied.value || items.value.some(item => item.id === id) || (!again && linked.value?.id === id)) return
  void links.run(({ after, signal }) => after(getRecurrence(id, signal), item => {
    if (routineId.value !== id || item.id !== id) return
    if (linked.value?.id !== id || item.revision >= linked.value.revision) linked.value = item
  }), { failed: error => {
    if (routineId.value !== id) return
    if ((error as { status?: number }).status === 404) gone()
    else linkFailure.value = error instanceof Error ? error.message : 'the server didn’t answer'
  } })
}
watch([routineId, loaded], () => readLinked())
function gone() {
  const id = routineId.value || chosen.value?.id
  closed.value = true
  if (id) items.value = items.value.filter(item => item.id !== id)
  if (routineId.value) setQuery({ routine: undefined, run: undefined })
}
// The person (or workspace) changed: everything of the previous one goes, filters included.
let shownFor = ''
watch(() => identity.owner.value, owner => {
  const principal = session.identity?.principal.id ?? '', switched = !!shownFor && !!principal && principal !== shownFor
  const hadOpen = !!editor.value || !!routineId.value || filtered.value
  clear()
  if (switched) {
    if (Object.keys(route.query).length) void router.replace({ path: '/agents/routines', query: {} })
    if (hadOpen) toast(`Signed in as ${session.identity?.principal.name ?? 'someone else'} · the previous person’s open logbook, filters and draft were cleared.`, { timeout: 6000 })
  }
  if (principal) shownFor = principal
  if (owner) { load(); loadNames() }
}, { immediate: true, flush: 'sync' })
// The poll refreshes whatever pages are loaded, and the routine on screen even when no page lists it.
const poller = usePoller(() => {
  if (busy.value.length || editor.value || runPrompt.value || menu.value) return
  load(loaded.value ? 'refresh' : 'first')
  if (linked.value) readLinked(true)
}, 20_000)
onMounted(() => {
  void projects.load(); poller.start()
  observer = new ResizeObserver(([entry]) => { if (entry) single.value = entry.contentRect.width < 1100 })
  if (pageEl.value) observer.observe(pageEl.value)
})
onBeforeUnmount(() => { poller.stop(); observer?.disconnect() })

// ---------- Actions: bound to the record on screen ----------
function mayManageItem(item: Recurrence) {
  if (!person.value) return false
  const scope = scopeOf(item)
  if (scope === 'personal') return item.definition?.owner_principal_id === me.value && can('recurrences.manage', item.project_id)
  if (scope === 'workspace') return can('recurrences.manage') && workspaceAdmin.value && can('recurrences.manage', item.project_id)
  return can('recurrences.manage', item.project_id)
}
const shownRecord = (item: Recurrence) => items.value.find(row => row.id === item.id) ?? (linked.value?.id === item.id ? linked.value : undefined)
const current = (item: Recurrence) => mayManageItem(item) && shownRecord(item)?.revision === item.revision
function replace(item: Recurrence) {
  const index = items.value.findIndex(row => row.id === item.id), known = items.value[index]
  if (known && item.revision >= known.revision && item.occurrence_count >= known.occurrence_count) items.value[index] = { ...known, ...item }
  if (linked.value?.id === item.id && item.revision >= linked.value.revision) linked.value = { ...linked.value, ...item }
}
function toggle(item: Recurrence, undo = false) {
  if (!current(item) || busy.value.includes(item.id)) return
  const owner = identity.owner.value, paused = !item.paused
  cancelReads(); busy.value = [...busy.value, item.id]
  void identity.run(({ after, signal }) => after(pauseRecurrence(item.id, paused, item.revision, signal), updated => {
    replace(updated)
    toast(`${paused ? 'Paused' : 'Resumed'} ${recurrenceName(item)}.`, { timeout: 8000, action: undo ? undefined : { label: 'Undo', run: () => {
      const shown = shownRecord(updated)
      if (owner !== identity.owner.value || !shown || !current(shown) || shown.paused !== updated.paused) { toast('The routine changed since, so nothing was undone.', { tone: 'error' }); return }
      toggle(shown, true)
    } } })
  }), { failed: error => { toast(error instanceof Error ? error.message : 'Pause or resume failed.', { tone: 'error' }) }, settled: () => { busy.value = busy.value.filter(id => id !== item.id) } })
}
function run(item: Recurrence) {
  if (!current(item) || busy.value.includes(item.id)) return
  cancelReads(); busy.value = [...busy.value, item.id]
  void identity.run(({ after, signal }) => after(getRecurrence(item.id, signal), latest => {
    if (!current(item) || latest.id !== item.id) return
    replace(latest)
    if (latest.trigger.kind === 'event' || (latest.open_previous && latest.overlap_policy === 'skip')) { runPrompt.value = latest; return }
    return after(runRecurrence(latest.id, { idempotency_key: crypto.randomUUID(), expected_revision: latest.revision }, signal), ran)
  }), { failed: error => { toast(error instanceof Error ? error.message : 'Run now failed.', { tone: 'error' }) }, settled: () => { busy.value = busy.value.filter(id => id !== item.id) } })
}
function ran(result: RecurrenceResult) {
  runPrompt.value = null
  toast(result.outcome === 'created' ? `Created occurrence #${result.number}.` : `Skipped: ${reasonWords(result.reason)}`)
  void identity.run(({ after, signal }) => after(getRecurrence(result.recurrence_id, signal), replace), { failed: () => {} })
}
function edit(item: Recurrence, consent = false) { if (mayManageItem(item)) { revoked.value = false; editor.value = { item, consent } } }
function saved(item: Recurrence, outcome: 'saved' | 'consented' | 'kept' | 'waiting' = 'saved') {
  editor.value = null; replace(item)
  if (outcome === 'consented') toast(`Agents may run “${recurrenceName(item)}”${item.next_at ? ` · next run ${when(item.next_at, recurrenceZone(item.trigger))}` : ''}.`, { timeout: 8000 })
  else if (outcome === 'kept') toast(`Kept paused. ${recurrenceName(item)} waits until you let agents run it.`)
  else if (outcome === 'waiting') toast(`Not paused. ${recurrenceName(item)} creates its tickets when due; they wait until you let agents run it.`, { timeout: 8000 })
  else toast(`Saved ${recurrenceName(item)}`)
}
const editorProject = computed(() => {
  const item = editor.value?.item
  if (!item || scopeOf(item) !== 'project') return null
  const project = projects.byId(item.project_id)
  return { id: item.project_id, routeKey: project?.routeKey ?? '', title: project?.title }
})
// An open editor or Run now prompt is bound to its own routine's right.
const lostRight = computed(() => !!editor.value && !mayManageItem(editor.value.item) || !!runPrompt.value && !mayManageItem(runPrompt.value))
watch(lostRight, lost => { if (lost) { if (editor.value) revoked.value = true; editor.value = null; runPrompt.value = null; menu.value = null } })
function homeOf(item: Recurrence) {
  const scope = scopeOf(item), id = encodeURIComponent(item.id)
  if (scope === 'personal') return { to: `/settings/personal?recurrence=${id}`, words: 'Settings › Personal' }
  if (scope === 'workspace') return { to: `/settings/routines?recurrence=${id}`, words: 'Settings › Routines' }
  const key = projectKeyOf(item)
  return key ? { to: `/p/${encodeURIComponent(key)}/settings?recurrence=${id}`, words: `${key} › Settings` } : null
}
function copyCLI(item: Recurrence) {
  closeMenu(); const owner = identity.owner.value
  void identity.run(({ after }) => after(navigator.clipboard.writeText(`aeon recur get ${item.id}`), () => { if (owner === identity.owner.value) toast('Copied CLI command.') }), { failed: () => toast('The clipboard is unavailable.', { tone: 'error' }) })
}
function openMenu(type: 'new' | 'more', anchor: HTMLElement, item?: Recurrence) { menu.value = menu.value?.type === type && menu.value.item?.id === item?.id ? null : { type, anchor, item } }
function closeMenu(restore = true) { const anchor = menu.value?.anchor; menu.value = null; if (restore) anchor?.focus({ preventScroll: true }) }
function menuKeys(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const list = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role=menuitem]')]
  const at = list.indexOf(document.activeElement as HTMLElement)
  list[event.key === 'Home' ? 0 : event.key === 'End' ? list.length - 1 : (at + (event.key === 'ArrowDown' ? 1 : -1) + list.length) % list.length]?.focus()
}
const projectHomes = computed(() => projects.projects.filter(project => !project.archived).map(project => ({ key: project.routeKey, title: project.title })).sort((a, b) => a.key.localeCompare(b.key)))
const stateIcon = (state: RoutineState) => STATE_LOOK[state].icon as IconName | 'live'
</script>

<template>
  <section ref="pageEl" class="routines-page" :class="{ single }" aria-labelledby="routines-title">
    <header class="page-head">
      <div><p class="eyebrow">{{ session.identity?.tenant.name ?? 'Workspace' }}</p><h1 id="routines-title">Routines</h1></div>
      <RouterLink class="context-link" to="/agents"><AppIcon name="arrow-left" :size="13" />Agents</RouterLink>
      <span class="grow" />
      <button v-if="person" type="button" class="btn sm" aria-haspopup="menu" :aria-expanded="menu?.type === 'new'" @click="openMenu('new', $event.currentTarget as HTMLElement)"><AppIcon name="plus" :size="13" />New routine…</button>
    </header>
    <p class="lead">Recurring tickets, and the work agents do on them by themselves. Each routine is listed once, wherever it was made.</p>
    <p v-if="!person" class="calm-line" role="note"><AppIcon name="info" :size="13" /><span>Routines are listed for people. Agents read them through the API with Manage recurring work.</span></p>
    <template v-else>
      <div class="tools">
        <div class="seg scope" role="radiogroup" aria-label="Scope"><button v-for="[key, label] in SCOPE_FILTERS" :key="key" type="button" role="radio" :data-scope="key" :aria-checked="scopeFilter === key" @click="setQuery({ scope: key })">{{ label }}<span class="sn">{{ known ? scopeCount(key) : '' }}</span></button></div>
        <label class="proj"><span class="sr-only">Project</span><select id="routines-project" class="field" :value="projectFilter" @change="setQuery({ project: ($event.target as HTMLSelectElement).value || undefined })"><option value="">All projects</option><option v-for="option in projectOptions" :key="option.id" :value="option.id">{{ option.key }}</option></select></label>
        <div class="counts" role="group" aria-label="Show routines by state">
          <button v-for="state in STATE_FILTERS" :key="state" type="button" class="cnt" :class="STATE_LOOK[state].tone" :data-sf="state" :aria-pressed="stateFilter === state" :aria-disabled="!counts[state] && stateFilter !== state ? 'true' : undefined" @click="toggleState(state)"><span v-if="stateIcon(state) === 'live'" class="pulse" aria-hidden="true" /><AppIcon v-else :name="stateIcon(state) as IconName" :size="13" /><b>{{ known ? countWords(counts[state], more) : '·' }}</b><span>{{ ROUTINE_STATE_WORDS[state].toLowerCase() }}</span></button>
        </div>
      </div>
      <p v-if="revoked" class="calm-line" role="status"><AppIcon name="eye" :size="13" /><span><b>The routine editor closed:</b> your rights for that routine changed. Nothing was saved; your draft was discarded. You can still read it.</span></p>
      <p v-if="linkFailure" class="calm-line" role="alert"><AppIcon name="alert" :size="13" /><span><b>The linked routine couldn’t be read:</b> {{ linkFailure }}. </span><button type="button" class="btn sm ghost" @click="readLinked(true)">Try again</button></p>
      <p v-if="closed" class="calm-line" role="status"><AppIcon name="eye" :size="13" /><span><b>This logbook closed.</b> The routine is gone or no longer visible to you, so nothing of it is shown here.</span></p>
      <div class="grid" :class="{ one: loaded && !rows.length && !chosen, 'pv-log': single && !!chosen, 'pv-list': single && !chosen }">
        <section class="glass-card rt-list" aria-label="Routine list" :aria-busy="loading && !loaded" @keydown="listKeys">
          <template v-if="!loaded && !failure">
            <div v-for="index in 5" :key="index" class="sk-row" aria-hidden="true"><span class="skeleton round" /><span class="skeleton" /><span class="skeleton" /></div>
            <p class="sr-only" role="status">Loading routines…</p>
          </template>
          <p v-else-if="denied" class="calm-line list-line" role="status"><AppIcon name="eye" :size="13" /><span><b>No routines to show you.</b> Reading them needs read access to at least one project.</span></p>
          <p v-else-if="failure && !items.length" class="m-err" role="alert"><AppIcon name="alert" :size="15" /><span>Routines could not be loaded: {{ failure }}. Nothing here changed.</span><button type="button" class="btn sm" @click="load()">Try again</button></p>
          <div v-else-if="!items.length" class="rt-empty">
            <span class="glyph big" aria-hidden="true"><AppIcon name="repeat" :size="22" /></span>
            <h2>No routines yet</h2>
            <p>A routine is a recurring ticket, and an agent can work on it by itself. Make one where it belongs: in a project’s Settings, with Repeat… on a ticket, or just for you.</p>
            <div class="empty-acts"><button type="button" class="btn sm" aria-haspopup="menu" @click="openMenu('new', $event.currentTarget as HTMLElement)"><AppIcon name="folder" :size="13" />In a project…</button><RouterLink class="btn sm" to="/settings/personal#routines"><AppIcon name="user" :size="13" />Just for you</RouterLink></div>
          </div>
          <div v-else-if="!rows.length" class="rt-empty small"><p>No routines match these filters.</p><button type="button" class="btn sm" @click="router.replace({ path: '/agents/routines', query: {} })">Show all routines</button></div>
          <template v-else>
            <div class="rt-lh" aria-hidden="true"><span /><span>Routine</span><span>Next run</span><span>State</span><span /></div>
            <RoutineRow v-for="item in rows" :key="item.id" :item="item" :selected="chosen?.id === item.id" :me="me" :owner-name="owners.get(item.definition?.owner_principal_id ?? '')" :project-key="projectKeyOf(item)" @select="select(item.id)" />
          </template>
          <p v-if="failure && items.length" class="m-err small" role="alert"><AppIcon name="alert" :size="13" /><span>Routines couldn’t be refreshed: {{ failure }}. The ones shown stay and may be out of date.</span></p>
          <p v-if="moreFailure" class="m-err small" role="alert"><AppIcon name="alert" :size="13" /><span>More routines couldn’t be read: {{ moreFailure }}. The ones shown stay.</span></p>
          <button v-if="cursor && items.length" type="button" class="btn sm ghost more" :disabled="loading" @click="load('more')">More routines</button>
          <p v-if="rows.length" class="list-hint" aria-hidden="true"><KeyCap k="j" /><KeyCap k="k" /> move · <KeyCap k="enter" /> open the logbook · <KeyCap k="n" /> run now</p>
        </section>
        <div class="lb-col">
          <RoutineLogbook
            v-if="chosen" :key="chosen.id" :item="chosen" :project-key="projectKeyOf(chosen)" :may-manage="mayManageItem(chosen)" :busy="busy.includes(chosen.id)" :back="single" more :open-run="chosen.id === routineId ? runId : ''"
            @toggle="toggle(chosen!)" @run="run(chosen!)" @edit="edit(chosen!)" @consent="edit(chosen!, true)" @back="back" @more="anchor => openMenu('more', anchor, chosen)" @gone="gone"
          />
        </div>
      </div>
    </template>
    <FloatingPanel v-if="menu" :anchor="menu.anchor" align="end" :width="300" :label="menu.type === 'new' ? 'New routine' : `More for ${recurrenceName(menu.item!)}`" @close="restore => closeMenu(restore)">
      <div v-if="menu.type === 'new'" role="menu" class="menu" @keydown="menuKeys">
        <RouterLink role="menuitem" class="mi" to="/settings/personal#routines" @click="menu = null"><AppIcon name="user" :size="14" /><span>Just for you<small>Only you see it; it acts with your rights.</small></span></RouterLink>
        <RouterLink role="menuitem" class="mi" to="/settings/routines" @click="menu = null"><AppIcon name="users" :size="14" /><span>For the workspace<small>{{ workspaceAdmin ? 'Everyone sees it; admins change it.' : 'Everyone sees it; only admins add one.' }}</small></span></RouterLink>
        <template v-if="projectHomes.length">
          <div class="menu-sep" role="separator" /><p class="eyebrow menu-eyebrow">In a project’s Settings</p>
          <RouterLink v-for="project in projectHomes" :key="project.key" role="menuitem" class="mi" :to="`/p/${encodeURIComponent(project.key)}/settings#recurring-work`" @click="menu = null"><AppIcon name="folder" :size="14" /><span>{{ project.key }}<small>{{ project.title }}</small></span></RouterLink>
        </template>
      </div>
      <div v-else-if="menu.item" role="menu" class="menu" @keydown="menuKeys">
        <RouterLink v-if="homeOf(menu.item)" role="menuitem" class="mi" :to="homeOf(menu.item)!.to" @click="menu = null"><AppIcon name="external" :size="14" /><span>Open where it was made<small>{{ homeOf(menu.item)!.words }}</small></span></RouterLink>
        <button type="button" role="menuitem" class="mi" @click="copyCLI(menu.item)"><AppIcon name="terminal" :size="14" /><span>Copy CLI command<small>aeon recur get …</small></span></button>
      </div>
    </FloatingPanel>
    <RecurrenceEditor v-if="editor" :key="editor.item.id" :project="editorProject" :scope="scopeOf(editor.item)" :recurrence="editor.item" :consent="editor.consent" @close="editor = null" @saved="saved" @changed="replace" @revoked="() => { revoked = true }" />
    <RecurrenceRun v-if="runPrompt" :key="runPrompt.id" :item="runPrompt" @close="runPrompt = null" @ran="ran" />
  </section>
</template>

<style scoped>
.routines-page { width: 100%; margin: 0; padding: 22px var(--gutter) 40px; }
.page-head { display: flex; align-items: flex-end; gap: 16px; margin-bottom: 6px; }
.page-head h1 { margin-top: 4px; }
.grow { flex: 1; }
.context-link { display: inline-flex; align-items: center; gap: 6px; height: 32px; margin-bottom: 2px; padding: 0 10px; border-radius: 999px; color: var(--teal-ink); font-size: 13px; font-weight: 600; white-space: nowrap; text-decoration: none; }
@media (hover: hover) { .context-link:hover { background: var(--row-hover); } }
.context-link:focus-visible { box-shadow: var(--focus-ring); }
.lead { max-width: 72ch; font-size: 13.5px; color: var(--ink-2); }
.tools { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 14px; margin: 16px 0 14px; }
.sn { margin-left: 2px; font-weight: 500; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.proj .field { min-width: 10rem; height: 32px; }
.counts { display: flex; flex-wrap: wrap; gap: 4px; margin-left: auto; }
.cnt { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font: inherit; font-size: 12.5px; white-space: nowrap; cursor: pointer; }
.cnt b { font-variant-numeric: tabular-nums; color: var(--ink); }
@media (hover: hover) { .cnt:hover:not([aria-disabled="true"]) { background: var(--row-hover); } }
.cnt[aria-pressed="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.cnt[aria-disabled="true"] { opacity: .5; cursor: default; }
.cnt:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.cnt.wait svg { color: var(--queue-wait-ink); }
.cnt.bad svg { color: var(--danger); }
.calm-line { display: flex; align-items: flex-start; gap: 8px; margin: 0 0 12px; padding: 10px 12px; border-radius: 10px; font-size: 12.5px; line-height: 1.5; color: var(--ink-2); background: var(--surface-sunken); }
.calm-line > svg { flex: none; margin-top: 2px; }
.calm-line b { color: var(--ink); font-weight: 600; }
.calm-line.list-line { margin: 6px 0; }
.grid { display: grid; grid-template-columns: minmax(0, 680px) minmax(0, 1fr); gap: 16px; align-items: start; }
.grid.one { grid-template-columns: minmax(0, 1fr); }
.single .grid { grid-template-columns: minmax(0, 1fr); }
.single .grid.pv-log .rt-list, .single .grid.pv-list .lb-col { display: none; }
.rt-list { container: routines / inline-size; min-width: 0; padding: 8px 10px 12px; }
.lb-col { min-width: 0; }
.rt-lh { display: grid; grid-template-columns: 32px minmax(0, 1fr) 124px 180px 14px; gap: 12px; padding: 6px 10px; border-bottom: 1px solid var(--line); font: 500 10.5px var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.sk-row { display: grid; grid-template-columns: 32px minmax(0, 1fr) minmax(0, .6fr); align-items: center; gap: 12px; min-height: 64px; padding: 8px 10px; }
.sk-row .skeleton.round { width: 32px; height: 32px; border-radius: 10px; }
.rt-empty { display: grid; justify-items: center; gap: 10px; padding: 48px 24px; text-align: center; }
.rt-empty.small { padding: 32px 24px; }
.rt-empty h2 { font: 600 17px/1.3 var(--font); }
.rt-empty p { max-width: 52ch; font-size: 13.5px; color: var(--ink-2); }
.empty-acts { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; margin-top: 6px; }
.empty-acts .btn { text-decoration: none; }
.glyph.big { display: grid; place-items: center; width: 48px; height: 48px; border-radius: 14px; color: var(--marker-on); background: var(--marker); }
.m-err { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; min-height: 120px; padding: 12px 4px; font-size: 13.5px; color: var(--ink-2); }
.m-err.small { min-height: 0; font-size: 12.5px; }
.m-err > svg { flex: none; color: var(--warn-ink); }
.m-err > span { flex: 1 1 240px; }
.more { margin-top: 10px; }
.list-hint { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; margin-top: 12px; font-size: 12px; color: var(--ink-3); }
.menu { display: grid; gap: 1px; }
.mi { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 48px; padding: 8px 10px; border: 0; border-radius: 6px; background: transparent; color: var(--ink); font: inherit; font-size: 13px; text-align: left; text-decoration: none; cursor: pointer; }
@media (hover: hover) { .mi:hover { background: var(--row-hover); } }
.mi:focus-visible { outline: none; background: var(--row-hover); }
.mi > svg { flex: none; color: var(--ink-2); }
.mi > span { display: grid; gap: 3px; min-width: 0; }
.mi small { font-size: 11px; color: var(--ink-3); overflow-wrap: anywhere; }
.menu-sep { height: 1px; margin: 4px 8px; background: var(--line); }
.menu-eyebrow { margin: 0; padding: 8px 10px 2px; }
.pulse { position: relative; display: inline-block; flex: none; width: 8px; height: 8px; border-radius: 50%; background: var(--teal); }
@media (prefers-reduced-motion: no-preference) {
  .pulse::after { content: ''; position: absolute; inset: -3px; border-radius: 50%; box-shadow: 0 0 0 1.5px var(--teal); opacity: 0; animation: ring 1.8s ease-out infinite; }
  @keyframes ring { 0% { transform: scale(.6); opacity: .8; } 100% { transform: scale(1.5); opacity: 0; } }
}
@container routines (max-width: 600px) {
  .rt-lh { display: none; }
  .list-hint { display: none; }
}
@media (max-width: 720px) {
  .routines-page { padding: 16px 12px 28px; }
  .page-head { flex-wrap: wrap; align-items: center; gap: 4px 12px; }
  .page-head > div { flex-basis: 100%; }
  .tools { gap: 8px; }
  .seg.scope { display: grid; grid-template-columns: 1fr 1fr; width: 100%; border-radius: 14px; }
  .proj, .proj .field { width: 100%; }
  .counts { margin-left: 0; }
}
@media (pointer: coarse) {
  .seg.scope button { min-width: 44px; height: auto; min-height: 44px; }
  .cnt, .context-link, .page-head .btn, .more, .empty-acts .btn, .rt-empty .btn { min-height: 44px; }
  .proj .field { min-height: 44px; }
  .mi { min-height: 52px; }
}
</style>
