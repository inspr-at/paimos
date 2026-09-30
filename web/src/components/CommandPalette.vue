<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { listNodes, type ListItem, type WorkNode } from '../lib/api'
import { searchWork, workKindMap } from '../lib/ticketSearch'
import { DOCK_MEDIA, dockPath, entryPath, kindToken, listKnowledge, typeMeta, type KnowledgeItem } from '../lib/knowledge'
import { visibleSections } from '../lib/settings'
import { can } from '../lib/authz'
import { run } from '../lib/commands'
import { actionResults, assemble, keyPrefixOf, keyQuery, knowledgeResults, projectResults, recentResults, ticketResults, viewResults, type ActionResult, type Group, type Result, type TicketResult } from '../lib/palette'
import { loadViews, viewsOf } from '../lib/savedViews'
import { filtersFromView, filtersToQuery } from '../lib/ticketList'
import { usePreference } from '../lib/preferences'
import { useSession } from '../stores/session'
import { recents } from '../lib/recents'
import { dark, toggleTheme } from '../lib/theme'
import { toast } from '../lib/toast'
import { highlight, statusMeta } from '../lib/work'
import { useProjects } from '../stores/projects'
import AppIcon, { type IconName } from './AppIcon.vue'
import KeyCap from './KeyCap.vue'
import BizIcon, { type BizIconName } from './business/BizIcon.vue'
import { useBusiness } from '../stores/business'
import StatusIcon from './work/StatusIcon.vue'

// One input for tickets, projects and actions. Inside a project the search is scoped
// to it (a chip that Backspace on an empty input removes); an empty input shows what
// was opened recently. Stale requests are cancelled; results never jump while typing.
const props = defineProps<{ canWrite: boolean }>()
const route = useRoute()
const router = useRouter()
const projects = useProjects()
const business = useBusiness()
const dialog = ref<HTMLDialogElement>()
const input = ref<HTMLInputElement>()
const list = ref<HTMLElement>()
const term = ref('')
const scope = ref<string | null>(null)
const active = ref(0)
const loading = ref(false)
const failed = ref('')
const listed = ref<ListItem[]>([])
const hits = ref<WorkNode[]>([])
const knowledgeHits = ref<KnowledgeItem[]>([])
const workKinds = ref(new Map<string, string>())
// Phones get a shorter placeholder and a Cancel button in place of the Esc keycap.
const narrowQuery = window.matchMedia('(max-width: 600px)')
const narrow = ref(narrowQuery.matches)
const onNarrow = (event: MediaQueryListEvent) => { narrow.value = event.matches }
narrowQuery.addEventListener('change', onNarrow)
let opener: HTMLElement | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | null = null
let searched = ''
let searchGen = 0
let arming = false
let armFrame = 0
let escapeArmed = false

const routeProject = computed(() => typeof route.params.projectKey === 'string' ? projects.byRouteKey(route.params.projectKey) : undefined)
const scopeProject = computed(() => scope.value ? projects.byRouteKey(scope.value) : undefined)
const query = computed(() => term.value.trim())

// ---------- Actions ----------
const actions = computed<ActionResult[]>(() => {
  const here = routeProject.value
  const out: ActionResult[] = []
  if (here && props.canWrite) out.push({ type: 'action', id: 'new-ticket', label: `New ticket in ${here.routeKey}`, hint: here.title, icon: 'plus', keys: ['n'] })
  const inKnowledge = route.path.includes('/knowledge')
  if (here) {
    const outline = route.query.view === 'outline'
    out.push({ type: 'action', id: outline ? 'go-list' : 'go-outline', label: outline ? 'Go to List' : 'Go to Outline', hint: here.title, icon: outline ? 'list' : 'outline' })
    if (!inKnowledge) out.push({ type: 'action', id: 'go-knowledge', label: 'Go to Knowledge', hint: `${here.title}: runbooks, guidelines, memory`, icon: 'book' })
    if (props.canWrite) out.push({ type: 'action', id: 'new-knowledge', label: `New knowledge entry in ${here.routeKey}`, hint: 'Runbook, guideline, memory and more', icon: 'plus', searchOnly: !inKnowledge })
  }
  if (route.path !== '/') out.push({ type: 'action', id: 'go-projects', label: 'Go to Projects', icon: 'folder', keys: ['g', 'p'] })
  if (!route.path.startsWith('/agents')) out.push({ type: 'action', id: 'go-agents', label: 'Go to Agents', hint: 'Sessions, approvals and pacing', icon: 'agent', keys: ['g', 'a'] })
  // Business: log time (on the open ticket, when one is open) and the overview.
  if (business.open.hours && business.staff) {
    const ticket = typeof route.params.ticketKey === 'string' ? route.params.ticketKey.toUpperCase() : ''
    out.push({ type: 'action', id: 'log-time', label: ticket ? `Log time on ${ticket}` : 'Log time', hint: 'Hours this week', icon: 'clock', keys: route.path.startsWith('/business/hours') ? ['l'] : undefined })
  }
  if (business.anyOpen && route.path !== '/business') out.push({ type: 'action', id: 'go-business', label: 'Go to Business', hint: 'Customers, quotes, hours and rates', icon: 'briefcase', keys: ['g', 'b'] })
  if (!route.path.startsWith('/settings')) out.push({ type: 'action', id: 'settings', label: 'Settings', hint: 'Theme, greeting and keys', icon: 'gear' })
  // Each settings section is found by name ("workspace settings", "agent keys").
  for (const section of visibleSections(business.admin, permission => can(permission))) out.push({ type: 'action', id: `settings-${section.id}`, label: `${section.label} settings`, hint: section.summary, icon: 'gear', searchOnly: true })
  out.push({ type: 'action', id: 'theme', label: dark.value ? 'Switch to light theme' : 'Switch to dark theme', icon: dark.value ? 'sun' : 'moon' })
  out.push({ type: 'action', id: 'releases', label: 'Release history', hint: 'What changed, release by release', icon: 'history' })
  out.push({ type: 'action', id: 'shortcuts', label: 'Keyboard shortcuts', icon: 'keyboard', keys: ['?'] })
  // Knowledge in every project: a search when something is typed, the page otherwise.
  const typed = query.value
  // Only when knowledge matched: an empty search keeps its honest "Nothing matches".
  if (typed.length >= 2 && !keyQuery(typed) && knowledgeHits.value.length) out.push({ type: 'action', id: 'search-knowledge', label: `Search all knowledge for “${typed}”`, hint: 'Every project', icon: 'book' })
  else if (route.path !== '/knowledge') out.push({ type: 'action', id: 'search-knowledge', label: 'Search all knowledge', hint: 'Runbooks, guidelines and memory in every project', icon: 'book', searchOnly: !!here })
  return out
})

// ---------- Results ----------
function projectFor(key: string) {
  const project = projects.byRouteKey(keyPrefixOf(key))
  return project ? project.routeKey : null
}
// The saved views of the project in scope, by name.
const session = useSession()
const viewProject = computed(() => scopeProject.value ?? (scope.value ? undefined : routeProject.value))
const viewPref = computed(() => viewProject.value ? usePreference<{ defaultView?: string | null }>(`list:${viewProject.value.id}`) : null)
const views = computed(() => {
  const project = viewProject.value
  if (!project) return []
  const me = session.identity?.principal.id
  const preferred = viewPref.value?.value.value?.defaultView ?? null
  return viewsOf(project.id).items.map(view => ({ id: view.id, name: view.name, shared: view.shared, mine: view.owner_principal_id === me, isDefault: view.id === preferred }))
})
const groups = computed<Group[]>(() => assemble(query.value, {
  views: viewResults(query.value, views.value),
  recent: recentResults(recents, scope.value),
  tickets: ticketResults(searched, listed.value, hits.value, workKinds.value, projectFor, scope.value),
  knowledge: knowledgeResults(knowledgeHits.value, id => projects.byId(id)?.routeKey ?? null, scope.value),
  projects: scope.value ? [] : projectResults(query.value, projects.projects.map(p => ({ id: p.id, routeKey: p.routeKey, title: p.title, description: p.description, archived: p.archived }))),
  actions: actionResults(query.value, actions.value),
}))
const showSkeleton = computed(() => loading.value && !!query.value && !listed.value.length && !hits.value.length && !knowledgeHits.value.length)
// A newer search is in flight. Keep the actions that already match the query, and
// hide tickets from the previous query so they cannot sit above that action.
const shownGroups = computed(() => {
  const pending = loading.value && !!query.value && searched !== query.value
  return pending ? groups.value.filter(group => group.id === 'actions') : groups.value
})
const flat = computed(() => shownGroups.value.flatMap(group => group.items))
const empty = computed(() => !!query.value && !loading.value && !failed.value && searched === query.value && !flat.value.length)
watch(flat, () => { if (active.value >= flat.value.length) active.value = 0 })

async function search() {
  const q = query.value
  const gen = ++searchGen
  controller?.abort()
  if (!q) { listed.value = []; hits.value = []; knowledgeHits.value = []; loading.value = false; searched = ''; return }
  controller = new AbortController()
  const signal = controller.signal
  loading.value = true; failed.value = ''
  const within = scopeProject.value?.id
  const key = keyQuery(q)
  try {
    if (gen !== searchGen) return
    if (!workKinds.value.size) workKinds.value = await workKindMap()
    // A key prefix ("PHAROS-29") is a key lookup; words also go to the hybrid search
    // (lib/ticketSearch, shared with the relation picker).
    const [work, knowledge] = await Promise.all([
      searchWork(q, { within, signal }),
      // Knowledge reads titles, slugs and text; it never holds up the rest.
      key ? Promise.resolve({ items: [] as KnowledgeItem[] }) : listKnowledge({ q, project_id: within, limit: 8 }, signal).catch(() => ({ items: [] as KnowledgeItem[] })),
    ])
    if (signal.aborted || gen !== searchGen) return
    listed.value = work.listed
    hits.value = work.hits
    knowledgeHits.value = knowledge.items
    searched = q
    active.value = 0
  } catch (e) {
    if (signal.aborted || gen !== searchGen) return
    failed.value = e instanceof Error ? e.message : 'Search is unavailable'
  } finally {
    // The latest search clears the spinner even when it aborted an older request.
    if (gen === searchGen) loading.value = false
  }
}
watch([term, scope], () => {
  clearTimeout(timer)
  if (!query.value) { void search(); active.value = 0; return }
  loading.value = true
  timer = setTimeout(() => void search(), 120)
})

// ---------- Open, close, choose ----------
function open() {
  if (dialog.value?.open) { input.value?.focus(); input.value?.select(); return }
  opener = document.activeElement as HTMLElement
  scope.value = routeProject.value?.routeKey ?? null
  controller?.abort()
  searchGen++
  term.value = ''; listed.value = []; hits.value = []; knowledgeHits.value = []; failed.value = ''; loading.value = false; active.value = 0; searched = ''
  void projects.load()
  if (routeProject.value) void loadViews(routeProject.value.id)
  // Arm before showModal: a cancel fired while the dialog opens must not close it.
  // Two frames later, Escape still closes. Focus now, so the following keys hit the input.
  arming = true
  dialog.value?.showModal()
  input.value?.focus()
  cancelAnimationFrame(armFrame)
  armFrame = requestAnimationFrame(() => { armFrame = requestAnimationFrame(() => { arming = false }) })
}
function close() { controller?.abort(); dialog.value?.close(); opener?.focus({ preventScroll: true }) }
function noteEscape(event: KeyboardEvent) { if (event.key === 'Escape') escapeArmed = true }
function onCancel(event: Event) {
  event.preventDefault()
  const viaEscape = escapeArmed
  escapeArmed = false
  // Opening the dialog can fire cancel from the same key. Escape still closes.
  if (arming && !viaEscape) return
  close()
}
function hrefOf(result: Result): string | null {
  if (result.type === 'project') return `/p/${encodeURIComponent(result.key)}`
  if (result.type === 'ticket' && result.projectKey) return `/p/${encodeURIComponent(result.projectKey)}/${encodeURIComponent(result.key)}`
  // Wide screens show the entry docked beside its project's list; narrower ones its own page.
  if (result.type === 'knowledge' && result.projectKey) return window.matchMedia(DOCK_MEDIA).matches ? dockPath(result.projectKey, result.kind, result.slug) : entryPath(result.projectKey, result.kind, result.slug)
  return null
}
async function resolveTicket(result: TicketResult): Promise<string | null> {
  if (result.projectKey) return hrefOf(result)
  try {
    const page = await listNodes({ q: result.key, limit: 10 })
    const owner = page.items.find(item => item.key === result.key)?.project
    const project = owner ? projects.byId(owner.id) : undefined
    return project ? `/p/${encodeURIComponent(project.routeKey)}/${encodeURIComponent(result.key)}` : null
  } catch { return null }
}
function act(id: string) {
  const here = routeProject.value
  if (id === 'new-ticket' && here) run({ name: 'new-ticket', projectKey: here.routeKey })
  else if (id === 'go-knowledge' && here) void router.push(`/p/${encodeURIComponent(here.routeKey)}/knowledge`)
  else if (id === 'new-knowledge' && here) run({ name: 'new-knowledge', projectKey: here.routeKey })
  else if (id === 'search-knowledge') void router.push({ path: '/knowledge', query: query.value && !keyQuery(query.value) ? { q: query.value } : {} })
  else if (id === 'go-outline' || id === 'go-list') {
    // The Knowledge tab's search and filters are not the list's.
    const { view: _view, ...rest } = route.path.includes('/knowledge') ? { view: undefined } : route.query
    void router.push({ path: here ? `/p/${encodeURIComponent(here.routeKey)}` : route.path, query: id === 'go-outline' ? { ...rest, view: 'outline' } : rest })
  } else if (id === 'go-projects') void router.push('/')
  else if (id === 'go-agents') void router.push('/agents')
  else if (id === 'go-business') void router.push('/business')
  else if (id === 'log-time') {
    const ticket = typeof route.params.ticketKey === 'string' ? route.params.ticketKey.toUpperCase() : ''
    void router.push({ path: '/business/hours', query: ticket ? { log: '1', ticket } : { log: '1' } })
  }
  else if (id === 'theme') toggleTheme()
  else if (id === 'shortcuts') run({ name: 'shortcuts' })
  else if (id === 'releases') run({ name: 'releases' })
  else if (id === 'settings') void router.push('/settings')
  else if (id.startsWith('settings-')) void router.push(`/settings/${id.slice('settings-'.length)}`)
  else if (id.startsWith('view:')) {
    const project = viewProject.value
    const view = project ? viewsOf(project.id).items.find(item => `view:${item.id}` === id) : undefined
    if (project && view) void router.push({ path: `/p/${encodeURIComponent(project.routeKey)}`, query: filtersToQuery(filtersFromView(view)) })
  }
}
async function choose(result: Result | undefined, newTab = false) {
  if (!result) return
  if (result.type === 'action') { close(); act(result.id); return }
  const href = result.type === 'ticket' ? await resolveTicket(result) : hrefOf(result)
  if (!href) { toast(`${result.type === 'knowledge' ? result.slug : result.key} is not part of a project, so it has no list to open in.`); return }
  if (newTab) { window.open(href, '_blank', 'noopener'); return }
  close()
  await router.push(href)
}

// ---------- Keyboard ----------
function move(step: number) {
  if (!flat.value.length) return
  active.value = (active.value + step + flat.value.length) % flat.value.length
  void nextTick(() => list.value?.querySelector(`[data-index="${active.value}"]`)?.scrollIntoView({ block: 'nearest' }))
}
function jumpGroup(step: number) {
  const starts: number[] = []
  let index = 0
  for (const group of groups.value) { starts.push(index); index += group.items.length }
  if (starts.length < 2) return
  const current = starts.reduce((found, start, i) => active.value >= start ? i : found, 0)
  active.value = starts[(current + step + starts.length) % starts.length]
  void nextTick(() => list.value?.querySelector(`[data-index="${active.value}"]`)?.scrollIntoView({ block: 'nearest' }))
}
function keydown(event: KeyboardEvent) {
  const ctrl = event.ctrlKey && !event.metaKey
  // Ctrl+J/K/N/P move like arrows while typing; they stop here so Ctrl+K does not reopen the palette.
  if (ctrl && ['j', 'k', 'n', 'p'].includes(event.key)) event.stopPropagation()
  if (event.key === 'ArrowDown' || (ctrl && (event.key === 'j' || event.key === 'n'))) { event.preventDefault(); move(1) }
  else if (event.key === 'ArrowUp' || (ctrl && (event.key === 'k' || event.key === 'p'))) { event.preventDefault(); move(-1) }
  else if (event.key === 'Tab') { event.preventDefault(); jumpGroup(event.shiftKey ? -1 : 1) }
  else if (event.key === 'Enter') { event.preventDefault(); void choose(flat.value[active.value], event.metaKey || event.ctrlKey) }
  else if (event.key === 'Backspace' && !term.value && scope.value) { event.preventDefault(); scope.value = null }
}
function backdrop(event: MouseEvent) { if (arming) return; if (event.target === dialog.value) close() }
function indexOf(result: Result) { return flat.value.indexOf(result) }
onBeforeUnmount(() => { clearTimeout(timer); cancelAnimationFrame(armFrame); controller?.abort(); narrowQuery.removeEventListener('change', onNarrow) })
defineExpose({ open })
// A body match explains itself with the excerpt; a title match needs nothing more.
const titleMatches = (title: string) => query.value.toLowerCase().split(/\s+/).some(word => word.length > 1 && title.toLowerCase().includes(word))
const iconOf = (result: Result): BizIconName => result.type === 'action' ? result.icon as BizIconName : result.type === 'project' ? 'folder' : result.type === 'knowledge' ? typeMeta(result.kind).icon : result.kind === 'epic' ? 'epic' : result.kind === 'task' ? 'task' : 'ticket'
</script>

<template>
  <dialog ref="dialog" class="palette" aria-label="Search and commands" @keydown="noteEscape" @cancel="onCancel" @click="backdrop">
    <div class="sheet">
      <label class="input-row">
        <AppIcon name="search" :size="18" class="lead" />
        <span v-if="scope" class="scope-chip">
          <span class="scope-in">in</span><span class="scope-key">{{ scope }}</span>
          <button type="button" class="scope-x" :aria-label="`Search everywhere, not only in ${scope}`" data-tip="Search everywhere · Backspace" @click="scope = null; input?.focus()"><AppIcon name="close" :size="11" /></button>
        </span>
        <input
          ref="input" v-model="term" class="palette-input" role="combobox" aria-controls="palette-results" aria-autocomplete="list" :aria-expanded="true"
          :aria-activedescendant="flat.length ? `palette-item-${active}` : undefined" :aria-label="scope ? `Search in ${scope}` : 'Search tickets, projects and actions'"
          :placeholder="narrow ? (scope ? `Search ${scopeProject?.title ?? scope}` : 'Search everything') : scope ? `Search ${scopeProject?.title ?? scope}, or a key like ${scope}-12` : 'Search tickets, projects and actions'" autocomplete="off" spellcheck="false" @keydown="keydown"
        />
        <span v-if="loading && query" class="spinner" aria-hidden="true" />
        <kbd class="keycap esc" aria-hidden="true">esc</kbd>
      </label>
      <button type="button" class="cancel" @click="close">Cancel</button>

      <div id="palette-results" ref="list" class="results" role="listbox" :aria-label="query ? 'Results' : 'Recent and actions'" :class="{ stale: loading && !showSkeleton }">
        <div v-if="failed && !shownGroups.length" class="state" role="alert">
          <AppIcon name="alert" :size="18" class="state-icon danger" />
          <p><strong>Search is not answering.</strong> {{ failed }}</p>
          <button type="button" class="btn sm" @click="search()">Try again</button>
        </div>
        <div v-else-if="empty" class="state">
          <AppIcon name="search" :size="18" class="state-icon" />
          <p><strong>Nothing matches “{{ query }}”{{ scope ? ` in ${scope}` : '' }}.</strong></p>
          <p class="hint">Try a key like <kbd class="keycap wide">{{ scope ?? 'PHAROS' }}-296</kbd> or a few words from a title.</p>
          <button v-if="scope" type="button" class="btn sm" @click="scope = null; input?.focus()">Search everywhere</button>
        </div>
        <template v-else>
          <div v-if="showSkeleton" class="skeleton-lines" aria-hidden="true">
            <span v-for="i in 5" :key="i" class="line"><span class="skeleton dot" /><span class="skeleton key-sk" /><span class="skeleton" :style="{ width: `${34 + ((i * 29) % 40)}%` }" /></span>
          </div>
          <section v-for="group in shownGroups" :key="group.id" class="group" role="group" :aria-label="group.label">
            <p class="group-label eyebrow" aria-hidden="true">{{ group.label }}</p>
            <div
              v-for="result in group.items" :id="`palette-item-${indexOf(result)}`" :key="result.id" class="item" :class="[result.type, { active: indexOf(result) === active }]"
              role="option" :aria-selected="indexOf(result) === active" :data-index="indexOf(result)" @pointermove="active = indexOf(result)" @click="choose(result, $event.metaKey || $event.ctrlKey)"
            >
              <template v-if="result.type === 'ticket'">
                <StatusIcon :state="result.state" :size="13" class="st" :data-tip="statusMeta(result.state).label" />
                <span class="key"><template v-for="(part, i) in highlight(result.key, group.id === 'recent' || !keyQuery(query) ? '' : query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                <AppIcon :name="iconOf(result) as IconName" :size="13" class="kind" :class="result.kind" />
                <span class="title"><template v-for="(part, i) in highlight(result.title, group.id === 'recent' ? '' : query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                <span v-if="result.projectKey && result.projectKey !== scope" class="project-chip">{{ result.projectKey }}</span>
              </template>
              <template v-else-if="result.type === 'knowledge'">
                <span class="knowledge-mark" :data-tip="typeMeta(result.kind).label" :style="{ '--kind': `var(${kindToken(result.kind)})` }"><AppIcon :name="typeMeta(result.kind).icon" :size="13" /></span>
                <span class="title"><template v-for="(part, i) in highlight(result.title, query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                <span v-if="result.excerpt && !titleMatches(result.title)" class="desc match"><template v-for="(part, i) in highlight(result.excerpt, query.split(/\s+/)[0] ?? '')" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                <span class="slug">{{ result.kind }}/{{ result.slug }}</span>
                <span v-if="result.archived" class="archived-chip">Archived</span>
                <span v-if="result.projectKey && result.projectKey !== scope" class="project-chip">{{ result.projectKey }}</span>
              </template>
              <template v-else-if="result.type === 'project'">
                <span class="project-mark"><AppIcon name="folder" :size="13" /></span>
                <span class="key-badge"><template v-for="(part, i) in highlight(result.key, group.id === 'recent' ? '' : query)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                <span class="title">{{ result.title }}</span>
                <span v-if="result.description" class="desc">{{ result.description }}</span>
              </template>
              <template v-else>
                <span class="action-mark"><BizIcon :name="iconOf(result)" :size="14" /></span>
                <span class="title">{{ result.label }}</span>
                <span v-if="result.hint" class="desc">{{ result.hint }}</span>
                <span v-if="result.keys" class="item-keys" aria-hidden="true"><kbd v-for="k in result.keys" :key="k" class="keycap">{{ k }}</kbd></span>
              </template>
              <AppIcon v-if="indexOf(result) === active" name="enter" :size="13" class="enter" />
            </div>
          </section>
          <p v-if="!query && !groups.some(g => g.id === 'recent')" class="first-hint">Tickets and projects you open show up here.</p>
        </template>
      </div>

      <footer class="foot">
        <span><kbd class="keycap"><AppIcon name="arrow-up" /></kbd><kbd class="keycap"><AppIcon name="arrow-down" /></kbd> move</span>
        <span><kbd class="keycap"><AppIcon name="enter" /></kbd> open</span>
        <span><KeyCap k="mod" /><KeyCap k="enter" /> new tab</span>
        <span><kbd class="keycap">tab</kbd> next group</span>
        <span v-if="scope" class="scope-hint"><KeyCap k="backspace" class="wide" /> all projects</span>
      </footer>
    </div>
  </dialog>
</template>

<style scoped>
.palette { width: min(680px, calc(100vw - 24px)); max-width: none; margin: 11dvh auto auto; padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.palette::backdrop { background: var(--palette-scrim); -webkit-backdrop-filter: blur(3px) saturate(1.05); backdrop-filter: blur(3px) saturate(1.05); }
.sheet {
  display: flex; flex-direction: column; border-radius: 18px; border: 1px solid var(--glass-edge); overflow: hidden;
  /* Near-opaque: the list behind must never read through the results. */
  background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2) 70%) var(--surface-raised); box-shadow: var(--shadow-pop), var(--shadow);
  -webkit-backdrop-filter: blur(22px) saturate(1.2); backdrop-filter: blur(22px) saturate(1.2);
}
@media (prefers-reduced-motion: no-preference) {
  .palette[open] .sheet { animation: palette-in .18s cubic-bezier(.2, .7, .2, 1); }
  .palette[open]::backdrop { animation: scrim-in .18s ease; }
  @keyframes palette-in { from { opacity: 0; transform: translateY(-6px) scale(.985); } to { opacity: 1; transform: none; } }
  @keyframes scrim-in { from { opacity: 0; } to { opacity: 1; } }
}
.input-row { display: flex; align-items: center; gap: 10px; height: 60px; padding: 0 14px 0 18px; border-bottom: 1px solid var(--line); }
.lead { color: var(--ink-3); }
.palette-input { flex: 1; min-width: 0; height: 100%; border: 0; background: transparent; color: var(--ink); font-size: 16.5px; }
.palette-input:focus { box-shadow: none; }
.palette-input::placeholder { color: var(--ink-3); }
.scope-chip { display: inline-flex; align-items: center; gap: 5px; flex-shrink: 0; height: 26px; padding: 0 3px 0 9px; border-radius: 999px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.scope-in { font-size: 12px; color: var(--ink-2); }
.scope-key { font: 600 11.5px/1 var(--mono); letter-spacing: .04em; font-variant-ligatures: none; }
.scope-x { display: grid; place-items: center; width: 20px; height: 20px; padding: 0; border: 0; border-radius: 50%; background: transparent; color: var(--teal-ink); }
.scope-x:hover { background: rgba(14, 111, 108, .12); }
.scope-x:focus-visible { box-shadow: var(--focus-ring); }
.esc { flex-shrink: 0; }
.sheet { position: relative; }
.cancel { display: none; }
.spinner { width: 14px; height: 14px; flex-shrink: 0; border-radius: 50%; border: 1.8px solid var(--line-2); border-top-color: var(--teal); }
@media (prefers-reduced-motion: no-preference) { .spinner { animation: spin .8s linear infinite; } @keyframes spin { to { transform: rotate(360deg); } } }
/* A steady height: results change in place rather than resizing the sheet. */
.results { height: min(420px, 56dvh); overflow: auto; padding: 6px; overscroll-behavior: contain; }
.results.stale .group { opacity: .6; }
@media (prefers-reduced-motion: no-preference) { .results .group { transition: opacity .12s ease; } }
.group + .group { margin-top: 4px; }
.group-label { padding: 8px 12px 4px; margin: 0; }
.item { display: flex; align-items: center; gap: 10px; min-height: 38px; padding: 0 12px; border-radius: 10px; cursor: pointer; font-size: 14px; }
.item.active { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.key { flex-shrink: 0; width: 96px; overflow: hidden; text-overflow: ellipsis; font: 500 11.5px/1 var(--mono); color: var(--ink-2); font-variant-ligatures: none; }
.item.active .key { color: var(--teal-ink); }
.kind { flex-shrink: 0; color: var(--ink-3); }
.kind.epic { color: var(--gold); }
.title { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.desc { flex: 1 1 0; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; color: var(--ink-3); }
.item.ticket .title, .item.knowledge .title { flex: 1 1 auto; }
.item.knowledge:has(.desc) .title { flex: 0 1 auto; }
.knowledge-mark { display: grid; place-items: center; flex-shrink: 0; width: 22px; height: 22px; border-radius: 7px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--kind, var(--teal-ink)); }
.slug { flex-shrink: 1; min-width: 0; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 500 11px/1 var(--mono); color: var(--ink-3); font-variant-ligatures: none; }
.archived-chip { flex-shrink: 0; height: 18px; padding: 0 7px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font: 600 9.5px/18px var(--mono); letter-spacing: .08em; text-transform: uppercase; font-variant-ligatures: none; }
.project-chip { flex-shrink: 0; height: 20px; padding: 0 8px; border-radius: 6px; background: var(--code-bg); color: var(--ink-2); font: 500 10.5px/20px var(--mono); letter-spacing: .04em; font-variant-ligatures: none; }
.project-mark, .action-mark { display: grid; place-items: center; flex-shrink: 0; width: 22px; height: 22px; border-radius: 7px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.item.active .action-mark, .item.active .project-mark { color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.item .key-badge { flex-shrink: 0; }
.item-keys { display: inline-flex; gap: 3px; margin-left: auto; }
.enter { flex-shrink: 0; margin-left: 4px; color: var(--ink-3); }
.item-keys + .enter { margin-left: 8px; }
.item:not(.ticket) .desc + .enter, .item:not(.ticket) .title + .enter { margin-left: auto; }
.first-hint { padding: 10px 12px; font-size: 12.5px; color: var(--ink-3); }
.skeleton-lines { display: grid; gap: 2px; padding: 8px 6px; }
.line { display: flex; align-items: center; gap: 12px; height: 38px; padding: 0 6px; }
.line .dot { width: 13px; height: 13px; border-radius: 50%; flex-shrink: 0; }
.line .key-sk { width: 76px; flex-shrink: 0; }
.state { display: grid; justify-items: center; align-content: center; gap: 8px; height: 100%; padding: 24px; text-align: center; font-size: 13.5px; color: var(--ink-2); }
.state strong { color: var(--ink); font-weight: 600; }
.state .hint { display: inline-flex; flex-wrap: wrap; align-items: center; justify-content: center; gap: 5px; }
.state-icon { color: var(--teal); }
.state-icon.danger { color: var(--danger); }
.keycap.wide { padding: 0 6px; }
.foot { display: flex; flex-wrap: wrap; gap: 6px 16px; padding: 10px 18px; border-top: 1px solid var(--line); font-size: 12px; color: var(--ink-3); }
.foot span { display: inline-flex; align-items: center; gap: 4px; }
.scope-hint { margin-left: auto; }
@media (max-width: 600px) {
  .palette { width: calc(100vw - 16px); margin-top: 8px; }
  .input-row { height: 56px; padding: 0 10px 0 14px; }
  .esc { display: none; }
  .results { height: min(460px, 62dvh); }
  .item { min-height: 44px; }
  .desc { display: none; }
  .foot { display: none; }
  .input-row { padding-right: 84px; }
  .cancel { display: grid; place-items: center; position: absolute; top: 6px; right: 6px; height: 44px; padding: 0 12px; border: 0; border-radius: 10px; background: transparent; color: var(--teal-ink); font-weight: 600; font-size: 14.5px; }
  .cancel:focus-visible { box-shadow: var(--focus-ring); }
  /* Two lines on a phone: key, kind and project above; the title gets the full width. */
  .item.ticket {
    display: grid; grid-template-columns: 14px auto 14px minmax(0, 1fr) auto; grid-template-areas: "st key kind . chip" ". ttl ttl ttl ttl";
    align-items: center; column-gap: 8px; row-gap: 4px; padding: 9px 12px;
  }
  .item.ticket .st { grid-area: st; }
  .item.ticket .key { grid-area: key; width: auto; }
  .item.ticket .kind { grid-area: kind; }
  .item.ticket .project-chip { grid-area: chip; }
  .item.ticket .title { grid-area: ttl; white-space: normal; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; line-height: 1.35; }
  .item.ticket .enter { display: none; }
  .item.knowledge { display: grid; grid-template-columns: 22px minmax(0, 1fr) auto; grid-template-areas: "mark ttl chip" ". slug slug"; align-items: center; column-gap: 10px; row-gap: 3px; padding: 9px 12px; }
  .item.knowledge .knowledge-mark { grid-area: mark; }
  .item.knowledge .title { grid-area: ttl; white-space: normal; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; line-height: 1.35; }
  .item.knowledge .slug { grid-area: slug; max-width: none; }
  .item.knowledge .project-chip { grid-area: chip; }
  .item.knowledge .archived-chip, .item.knowledge .enter { display: none; }
}
</style>
