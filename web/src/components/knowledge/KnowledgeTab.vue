<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { isNavigationFailure, NavigationFailureType, useRoute, useRouter } from 'vue-router'
import { brand } from '../../lib/brand'
import { DOCK_MEDIA, SORTS, TYPES, entryParam, entryPath, highlightWords, kindToken, statusLabel, type KnowledgeEntry, type KnowledgeItem, type KnowledgeStatus, type KnowledgeType, type SortBy } from '../../lib/knowledge'
import { STATUS_VIEWS, type KnowledgeFilters, type KnowledgeState, type StatusView } from '../../lib/useKnowledge'
import { toast } from '../../lib/toast'
import { settledNavigation } from '../../lib/navigation'
import { absoluteTime, plural, relativeTime } from '../../lib/work'
import { knowledgeFooter } from '../../lib/footerProviders'
import { useFooterSummary } from '../../lib/footerSummary'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import KnowledgeCreateDialog from './KnowledgeCreateDialog.vue'
import MethodLearnings from './MethodLearnings.vue'

// The project's Knowledge tab: every entry grouped by kind (runbooks first), with
// a kind rail, search that also reads the bodies, status and sort, and keys for
// moving (j k), opening (Enter), searching (/) and writing (n). Its controls live
// in the project toolbar (teleported), so the tab reads like the other views.
const props = defineProps<{
  project: { id: string; routeKey: string; title: string }
  state: KnowledgeState; filters: KnowledgeFilters; canWrite: boolean; person: boolean; now: number
  // An entry page is open over the tab: its keys belong to the entry.
  paused: boolean
  // Wide screens open an entry docked beside the list (U25); openEntry is the one docked now.
  dock?: boolean
  openEntry?: { type: KnowledgeType; slug: string } | null
}>()
const emit = defineEmits<{ update: [patch: Partial<KnowledgeFilters>]; accepted: [entry: KnowledgeEntry]; reverted: [] }>()
const router = useRouter()
const route = useRoute()
const KnowledgeGraph = defineAsyncComponent(() => import('./KnowledgeGraph.vue'))
const graphMode = computed(() => route.query.mode === 'graph')
const graph = ref<{ focus: () => void }>()
// A selection carries over between the two displays where the pane can show it; on a narrow
// screen going back to Entries drops it, which would otherwise open the entry's own page.
let modeIntent = 0
async function setMode(graph: boolean) {
  const intent = ++modeIntent
  // Width at the click, not the last resize event: a stale "wide" flag keeps
  // ?entry= on a narrow screen, and the route guard then opens the entry page.
  const go = () => {
    const wide = window.matchMedia(DOCK_MEDIA).matches
    return router.replace({ query: { ...route.query, mode: graph ? 'graph' : undefined, entry: graph || wide ? route.query.entry : undefined } })
  }
  let retriedAbort = false
  while (intent === modeIntent && route.path.endsWith('/knowledge')) {
    const settled = settledNavigation(router)
    let failure
    try { failure = await go() } catch (error) { settled.stop(); throw error }
    if (isNavigationFailure(failure, NavigationFailureType.cancelled)) await settled.promise
    else settled.stop()
    if (intent !== modeIntent || !route.path.endsWith('/knowledge')) return
    if (isNavigationFailure(failure, NavigationFailureType.cancelled)) continue
    if (isNavigationFailure(failure, NavigationFailureType.aborted) && !retriedAbort) { retriedAbort = true; continue }
    return
  }
}
// The page keeps the display (?mode=graph) and the docked entry (?entry=) while filters change.
function updateFilters(patch: Partial<KnowledgeFilters>) { emit('update', patch) }
function accepted(entry: KnowledgeEntry) { props.state.upsert(entry); emit('accepted', entry) }

const input = ref<HTMLInputElement>()
const draft = ref(props.filters.q)
const cursorId = ref<string | null>(null)
const listEl = ref<HTMLElement>()
const createDialog = ref<InstanceType<typeof KnowledgeCreateDialog>>()
const menu = ref<{ kind: 'status' | 'sort'; anchor: HTMLElement } | null>(null)
// Phones get the short placeholder that fits beside Status, Sort and New.
const phoneQuery = window.matchMedia('(max-width: 600px)')
const phone = ref(phoneQuery.matches)
const onPhone = (event: MediaQueryListEvent) => { phone.value = event.matches }
phoneQuery.addEventListener('change', onPhone)
// The address is written in this same turn. A timer or a later message does not
// run while the graph is busy, so the field would show the words and the graph
// would keep the previous results.
watch(() => props.filters.q, value => { if (value !== draft.value.trim()) draft.value = value })
watch(draft, () => {
  const value = draft.value.trim()
  if (value !== props.filters.q) updateFilters({ q: value })
})

const q = computed(() => props.filters.q.trim())
const statusView = computed(() => STATUS_VIEWS.find(view => view.value === props.filters.status) ?? STATUS_VIEWS[0])
const sortLabel = computed(() => SORTS.find(sort => sort.value === props.state.sortBy.value)?.label ?? 'Recently updated')
const sortOptions = computed(() => q.value ? SORTS : SORTS.filter(sort => sort.value !== 'relevance'))
const shown = computed(() => props.state.visible.value.length)
const nothingYet = computed(() => props.state.loaded.value && !props.state.items.value.length)
const skeleton = computed(() => !props.state.loaded.value && (props.state.loading.value || !props.state.error.value))
const listCommand = computed(() => `${brand.value.product.toLowerCase()} knowledge list --project ${props.project.routeKey}`)
const statusCount = (view: StatusView) => {
  const counts = props.state.statusCounts.value
  const statuses = STATUS_VIEWS.find(item => item.value === view)?.statuses ?? []
  const all: KnowledgeStatus[] = ['active', 'proposed', 'archived']
  return (statuses.length ? statuses : all).reduce((sum, status) => sum + (counts[status] ?? 0), 0)
}

// Wide: the entry docks beside the list (?entry=); otherwise it opens its own page.
function link(item: KnowledgeItem) {
  if (props.dock) return { path: `/p/${encodeURIComponent(props.project.routeKey)}/knowledge`, query: { ...filtersQuery(), entry: entryParam(item.type, item.slug) } }
  return { path: entryPath(props.project.routeKey, item.type, item.slug), query: filtersQuery() }
}
const isOpen = (item: KnowledgeItem) => !!props.openEntry && props.openEntry.type === item.type && props.openEntry.slug === item.slug
const openItem = computed(() => props.state.sequence.value.find(isOpen) ?? null)
// The docked entry is the cursor too, so j and k go on from it (also after a reload).
watch(() => openItem.value?.id, id => { if (id && id !== cursorId.value) reveal(id) }, { immediate: true })
// The list's own place (search, kind, status, sort) travels with an opened entry, so
// back returns to the same list and next/previous follow its order.
function filtersQuery() {
  const out: Record<string, string> = {}
  if (props.filters.q) out.q = props.filters.q
  if (props.filters.type) out.type = props.filters.type
  if (props.filters.status !== 'current') out.status = props.filters.status
  if (props.filters.sort) out.sort = props.filters.sort
  return out
}
function setType(type: KnowledgeType | '') { cursorId.value = null; updateFilters({ type }) }
function choose(kind: 'status' | 'sort', value: string) {
  const anchor = menu.value?.anchor
  menu.value = null
  anchor?.focus()
  updateFilters(kind === 'status' ? { status: value as StatusView } : { sort: value as SortBy })
}
function openMenu(kind: 'status' | 'sort', event: MouseEvent) {
  const anchor = event.currentTarget as HTMLElement
  menu.value = menu.value?.kind === kind ? null : { kind, anchor }
}
function closeMenu(restore: boolean) { const anchor = menu.value?.anchor; menu.value = null; if (restore) anchor?.focus() }
function clearSearch() { draft.value = ''; updateFilters({ q: '' }); input.value?.focus() }
function searchKey(event: KeyboardEvent) {
  if (graphMode.value && event.key !== 'Escape') return
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); if (draft.value) clearSearch(); else input.value?.blur() }
  else if (event.key === 'ArrowDown' || (event.key === 'Enter' && props.state.sequence.value.length)) {
    event.preventDefault(); input.value?.blur()
    if (event.key === 'Enter' && q.value) { void open(props.state.sequence.value[0]); return }
    move(cursorId.value ? 0 : 1)
  }
}
function resetFilters() { draft.value = ''; updateFilters({ q: '', type: '', status: 'current' }) }
function showAll() { updateFilters({ status: 'all' }) }

// The footer says what this list shows: entries, and the proposed ones a person has to review (AEON-785).
useFooterSummary(() => {
  if (!props.state.loaded.value && props.state.error.value) return null
  if (!props.state.loaded.value) return knowledgeFooter({ loaded: false, entries: 0, toReview: 0, updatedAt: null, now: props.now, act: { review: showReview, sort: showRecent } })
  const items = props.state.visible.value
  const updated = items.reduce((latest, item) => Math.max(latest, Date.parse(item.updated_at) || 0), 0)
  return knowledgeFooter({
    loaded: true, entries: items.length, toReview: items.filter(item => item.status === 'proposed').length, updatedAt: updated || null, now: props.now,
    act: { review: showReview, sort: showRecent },
  })
})
function showReview() { updateFilters({ status: 'proposed' }) }
function showRecent() { updateFilters({ sort: 'updated' }) }

// ---------- Keyboard: j k move, Enter or o opens, / searches, n writes ----------
function rowEl(id: string) { return listEl.value?.querySelector<HTMLElement>(`[data-id="${id}"]`) ?? null }
function move(step: number) {
  const rows = props.state.sequence.value
  if (!rows.length) return
  const index = rows.findIndex(item => item.id === cursorId.value)
  const next = index === -1 ? (step >= 0 ? 0 : rows.length - 1) : Math.max(0, Math.min(rows.length - 1, index + step))
  cursorId.value = rows[next].id
  void nextTick(() => { const el = rowEl(rows[next].id); el?.focus({ preventScroll: true }); el?.scrollIntoView({ block: 'nearest' }) })
  // A docked entry follows the selection.
  if (props.openEntry && !isOpen(rows[next])) void router.replace(link(rows[next]))
}
// With an entry docked, another one replaces it (back leaves the pane, not each step).
async function open(item: KnowledgeItem | undefined) {
  if (!item) return
  if (props.openEntry) await router.replace(link(item)); else await router.push(link(item))
}
function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}
function keydown(event: KeyboardEvent) {
  if ((graphMode.value && !['/', 'n'].includes(event.key)) || props.paused || event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return
  if (document.querySelector('dialog[open], .floating') || typing(event.target)) return
  switch (event.key) {
    case 'j': case 'ArrowDown': event.preventDefault(); move(1); break
    case 'k': case 'ArrowUp': event.preventDefault(); move(-1); break
    case 'Enter': case 'o': {
      if ((event.target as HTMLElement)?.closest?.('button, summary')) return
      const item = props.state.sequence.value.find(entry => entry.id === cursorId.value)
      if (item) { event.preventDefault(); void open(item) }
      break
    }
    case '/': event.preventDefault(); focusSearch(); break
    case 'n': if (props.canWrite) { event.preventDefault(); openCreate() } break
  }
}
function focusSearch() { input.value?.focus(); input.value?.select() }
function holdFocus() { input.value?.focus({ preventScroll: true }) }
function openCreate(type?: KnowledgeType) { createDialog.value?.open(type ?? (props.filters.type || undefined)) }
async function created(entry: KnowledgeEntry) {
  props.state.upsert(entry)
  toast(`Created ${entry.type} ${entry.slug}`)
  await router.push({ path: entryPath(props.project.routeKey, entry.type, entry.slug), state: { knowledgeEdit: 'body' } })
}
async function copyCommand() {
  try { await navigator.clipboard.writeText(listCommand.value); toast('Copied the command') } catch { toast('The command could not be copied', { tone: 'error' }) }
}
// The row the entry page was on is the cursor when the tab shows again.
function reveal(id: string, focus = false) {
  if (graphMode.value) { if (focus) graph.value?.focus?.(); return }
  if (!props.state.sequence.value.some(item => item.id === id)) return
  cursorId.value = id
  const place = () => {
    const el = rowEl(id)
    if (!el) return
    el.scrollIntoView({ block: 'nearest' })
    if (!focus || el === document.activeElement || el.contains(document.activeElement)) return
    const active = document.activeElement
    const onBody = !active || active === document.body || active === document.documentElement
    const inPane = active instanceof Element && !!active.closest('.entry-page')
    // The pane's removal moves focus onto the body a frame later. Put the row
    // back then. A control the user reached in between keeps the focus.
    if (onBody || inPane) el.focus({ preventScroll: true })
  }
  void nextTick(() => {
    place()
    if (focus) requestAnimationFrame(() => place())
  })
}
onMounted(() => window.addEventListener('keydown', keydown))
onBeforeUnmount(() => { window.removeEventListener('keydown', keydown); phoneQuery.removeEventListener('change', onPhone) })
defineExpose({ focusSearch, openCreate, reveal })
const who = (item: KnowledgeItem) => item.imported ? 'imported' : item.updated_by ? `by ${item.updated_by.name}` : ''
</script>

<template>
  <!-- Controls in the project toolbar, beside the view switch. -->
  <Teleport to="#knowledge-controls" defer>
    <!-- Kept apart from the project's views (List, Outline, Journey, Knowledge) by a hairline and its own words. -->
    <span class="k-mode-sep" aria-hidden="true" />
    <div class="seg k-mode" role="group" aria-label="Knowledge display">
      <button type="button" :aria-pressed="!graphMode" aria-label="Entries" data-tip="Entries · grouped by kind" @click="setMode(false)"><AppIcon name="rows-comfortable" :size="14" /><span class="k-mode-label">Entries</span></button>
      <button type="button" :aria-pressed="graphMode" aria-label="Graph" data-tip="Graph · how entries link" @click="setMode(true)"><AppIcon name="graph" :size="14" /><span class="k-mode-label">Graph</span></button>
    </div>
    <label class="search-field k-search">
      <AppIcon name="search" :size="14" />
      <input
        ref="input" v-model="draft" class="field" type="search" :placeholder="graphMode ? 'Find in graph' : phone ? 'Search' : 'Search knowledge'" :aria-label="`Search knowledge in ${project.title}`"
        aria-keyshortcuts="/" autocomplete="off" spellcheck="false" @keydown="searchKey"
      />
      <span v-if="state.searching.value && draft" class="spinner" aria-hidden="true" />
      <kbd v-else-if="!draft" class="keycap slash" aria-hidden="true">/</kbd>
      <button v-if="draft" type="button" class="clear-q" aria-label="Clear search" @click="clearSearch"><AppIcon name="close" :size="12" /></button>
    </label>
    <button v-if="!nothingYet" type="button" class="btn sm k-menu-btn" :class="{ on: filters.status !== 'current' }" aria-haspopup="dialog" :aria-expanded="menu?.kind === 'status'" :aria-label="`Status: ${statusView.label}`" data-tip="Which entries to show" @click="openMenu('status', $event)">
      <span class="k-menu-dim">Status</span>{{ statusView.label }}<AppIcon name="chevron" :size="12" class="k-chev" />
    </button>
    <button v-if="!nothingYet" type="button" class="btn sm k-menu-btn k-sort-btn" aria-haspopup="dialog" :aria-expanded="menu?.kind === 'sort'" :aria-label="`Sort: ${sortLabel}`" data-tip="Order within each kind" @click="openMenu('sort', $event)">
      <AppIcon name="sliders" :size="13" /><span class="k-sort-label">{{ sortLabel }}</span><AppIcon name="chevron" :size="12" class="k-chev" />
    </button>
    <span class="k-spacer" />
    <span class="k-count mono" role="status" aria-live="polite"><template v-if="state.loaded.value && !nothingYet">{{ plural(shown, 'entry', 'entries') }}</template></span>
    <button v-if="canWrite" type="button" class="btn primary k-new" aria-label="New knowledge entry" aria-keyshortcuts="n" data-tip="New entry · n" @click="openCreate()">
      <AppIcon name="plus" :size="14" /><span class="k-new-label">New entry</span>
    </button>
  </Teleport>

  <div class="k-frame">
  <MethodLearnings :project="project" :entries="state.items.value" :can-write="canWrite" :person="person" :now="now" @accepted="accepted" @reverted="emit('reverted')" @emptied="holdFocus" />
  <div class="k-layout">
    <nav class="k-rail" aria-label="Kinds of knowledge">
      <div class="k-kinds">
        <button type="button" class="k-kind" :aria-current="!filters.type ? 'true' : undefined" @click="setType('')">
          <span class="k-kind-icon"><AppIcon name="book" :size="14" /></span><span class="k-kind-label">All knowledge</span>
          <span class="k-kind-count mono">{{ state.total.value }}</span>
        </button>
        <button
          v-for="meta in TYPES" :key="meta.type" type="button" class="k-kind" :class="{ empty: !state.typeCounts.value[meta.type] }"
          :aria-current="filters.type === meta.type ? 'true' : undefined" :data-tip="meta.hint" @click="setType(filters.type === meta.type ? '' : meta.type)"
        >
          <span class="k-kind-icon" :style="{ '--kind': `var(${kindToken(meta.type)})` }"><AppIcon :name="meta.icon" :size="14" /></span><span class="k-kind-label">{{ meta.plural }}</span>
          <span class="k-kind-count mono">{{ state.typeCounts.value[meta.type] ?? 0 }}</span>
        </button>
      </div>
      <section class="k-agents" aria-labelledby="k-agents-title">
        <p id="k-agents-title" class="k-agents-title"><AppIcon name="terminal" :size="14" />Agents read these</p>
        <p class="k-agents-text">By kind and slug, before they start work. Rename a slug and every prompt that names it has to follow.</p>
        <span class="k-command"><code><template v-for="(part, i) in listCommand.split(' ')" :key="i"><span class="tok">{{ part }}</span>{{ ' ' }}</template></code><button type="button" class="icon-btn sm flat" aria-label="Copy the command" data-tip="Copy" @click="copyCommand"><AppIcon name="copy" :size="13" /></button></span>
      </section>
    </nav>

    <KnowledgeGraph v-if="graphMode && !paused" ref="graph" :project="project" :filters="filters" :can-write="canWrite" :docked="!!openEntry" @create="openCreate()" @reset="resetFilters" @list="setMode(false)" />
    <div v-else ref="listEl" class="k-list" :class="{ stale: state.searching.value && !!state.visible.value.length }">
      <p v-if="state.error.value && state.loaded.value" class="k-banner" role="status"><AppIcon name="alert" :size="14" />{{ state.error.value }}</p>

      <div v-if="skeleton" class="k-skeleton" role="status" aria-label="Loading knowledge">
        <div v-for="n in 2" :key="n" class="k-group glass-card">
          <div class="k-group-head"><span class="skeleton sk-icon" /><span class="skeleton sk-head" /></div>
          <div v-for="m in 3" :key="m" class="sk-row"><span class="skeleton sk-title" :style="{ width: `${38 + ((n * 3 + m) * 13) % 34}%` }" /><span class="skeleton sk-line" /></div>
        </div>
      </div>

      <div v-else-if="state.error.value && !state.loaded.value" class="k-state glass-card" role="alert">
        <span class="state-icon danger"><AppIcon name="alert" :size="18" /></span>
        <h2>Knowledge could not be loaded</h2>
        <p>{{ state.error.value }}</p>
        <button type="button" class="btn" @click="state.load()"><AppIcon name="refresh" :size="14" />Try again</button>
      </div>

      <div v-else-if="nothingYet" class="k-state glass-card">
        <span class="state-icon"><AppIcon name="book" :size="20" /></span>
        <h2>No knowledge in {{ project.title }} yet</h2>
        <p>Runbooks, guidelines and memory live here. Agents read them by slug before they work, and people keep them true.</p>
        <div v-if="canWrite" class="k-starters">
          <button v-for="meta in TYPES.slice(0, 3)" :key="meta.type" type="button" class="k-starter" @click="openCreate(meta.type)">
            <span class="k-kind-icon" :style="{ '--kind': `var(${kindToken(meta.type)})` }"><AppIcon :name="meta.icon" :size="15" /></span>
            <span><strong>Write a {{ meta.label.toLowerCase() }}</strong><span>{{ meta.hint }}</span></span>
          </button>
        </div>
        <p v-else class="k-fine">You can read knowledge here but not write it.</p>
      </div>

      <div v-else-if="!state.visible.value.length && !state.searching.value" class="k-state glass-card">
        <span class="state-icon"><AppIcon name="search" :size="18" /></span>
        <h2 v-if="q">Nothing matches “{{ q }}”{{ filters.type ? ` in ${TYPES.find(t => t.type === filters.type)?.plural.toLowerCase()}` : '' }}</h2>
        <h2 v-else-if="filters.type">No {{ statusView.value === 'current' ? '' : `${statusView.label.toLowerCase()} ` }}{{ TYPES.find(t => t.type === filters.type)?.plural.toLowerCase() }} yet</h2>
        <h2 v-else>Nothing {{ statusView.label.toLowerCase() }} here</h2>
        <p>Search reads titles, slugs and the full text.{{ filters.status !== 'all' ? ' Archived entries are hidden unless you ask for them.' : '' }}</p>
        <div class="state-actions">
          <button v-if="filters.status !== 'all'" type="button" class="btn" @click="showAll">Include archived</button>
          <button type="button" class="btn" @click="resetFilters">Clear the filters</button>
          <button v-if="canWrite && filters.type && filters.type !== 'decision'" type="button" class="btn primary" @click="openCreate(filters.type)"><AppIcon name="plus" :size="14" />New {{ TYPES.find(t => t.type === filters.type)?.label.toLowerCase() }}</button>
        </div>
      </div>

      <template v-else>
        <section v-for="group in state.groups.value" :key="group.type" class="k-group glass-card" :aria-labelledby="`k-group-${group.type}`">
          <header class="k-group-head">
            <span class="k-type-mark" :style="{ '--kind': `var(${kindToken(group.type)})` }"><AppIcon :name="group.meta.icon" :size="15" /></span>
            <h2 :id="`k-group-${group.type}`">{{ group.meta.plural }}</h2>
            <span class="k-group-count mono">{{ group.items.length }}</span>
            <p class="k-group-hint">{{ group.meta.hint }}</p>
            <button v-if="canWrite && group.type !== 'decision'" type="button" class="btn sm ghost k-add" :aria-label="`New ${group.meta.label.toLowerCase()}`" @click="openCreate(group.type)"><AppIcon name="plus" :size="13" /><span>{{ group.meta.label }}</span></button>
          </header>
          <ul class="k-rows">
            <li v-for="item in group.items" :key="item.id">
              <RouterLink
                :to="link(item)" :replace="!!openEntry" class="k-row" :class="{ cursor: cursorId === item.id, open: isOpen(item), archived: item.status === 'archived' }"
                :aria-current="isOpen(item) ? 'true' : undefined" :data-id="item.id" @focus="cursorId = item.id"
              >
                <span class="k-text">
                  <span class="k-title"><template v-for="(part, i) in highlightWords(item.title, q)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                  <span v-if="item.excerpt" class="k-excerpt"><template v-for="(part, i) in highlightWords(item.excerpt, q)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                  <span v-else class="k-excerpt empty">No text yet</span>
                </span>
                <span class="k-meta">
                  <span v-if="item.status !== 'active'" class="k-status" :class="item.status">{{ statusLabel(item.status) }}</span>
                  <span class="k-slug mono"><template v-for="(part, i) in highlightWords(item.slug, q)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                  <span class="k-links mono"><span v-if="item.link_count" class="k-links-inner" :data-tip="`Linked to ${plural(item.link_count, 'ticket or entry', 'tickets or entries')}`"><AppIcon name="link" :size="12" />{{ item.link_count }}<span class="sr-only"> linked</span></span></span>
                  <time class="k-time mono" :datetime="item.updated_at" :data-tip="`Updated ${absoluteTime(item.updated_at)}${who(item) ? ` ${who(item)}` : ''}`">{{ relativeTime(item.updated_at, { now }) }}</time>
                </span>
              </RouterLink>
            </li>
          </ul>
        </section>
        <p v-if="state.truncated.value" class="k-fine">Showing the newest 1,000 entries. Search to find older ones.</p>
      </template>

      <p v-if="state.loaded.value && state.items.value.length" class="k-hint">
        <kbd class="keycap">j</kbd><kbd class="keycap">k</kbd> move · <kbd class="keycap"><AppIcon name="enter" /></kbd> {{ dock ? 'preview' : 'open' }}<template v-if="openEntry"> · <kbd class="keycap">esc</kbd> close</template> · <kbd class="keycap">/</kbd> search<template v-if="canWrite"> · <kbd class="keycap">n</kbd> new entry</template>
      </p>
    </div>
  </div>
  </div>

  <FloatingPanel v-if="menu?.kind === 'status'" :anchor="menu.anchor" :width="236" label="Status" @close="closeMenu">
    <div class="k-menu" role="radiogroup" aria-label="Status">
      <button
        v-for="view in STATUS_VIEWS" :key="view.value" type="button" role="radio" class="k-menu-item" :aria-checked="filters.status === view.value"
        :data-autofocus="filters.status === view.value ? '' : undefined" @click="choose('status', view.value)"
      >
        <span class="k-check"><AppIcon v-if="filters.status === view.value" name="check" :size="13" /></span>{{ view.label }}<span class="k-menu-count mono">{{ statusCount(view.value) }}</span>
      </button>
    </div>
  </FloatingPanel>
  <FloatingPanel v-if="menu?.kind === 'sort'" :anchor="menu.anchor" :width="220" align="end" label="Sort" @close="closeMenu">
    <div class="k-menu" role="radiogroup" aria-label="Sort">
      <button
        v-for="option in sortOptions" :key="option.value" type="button" role="radio" class="k-menu-item" :aria-checked="state.sortBy.value === option.value"
        :data-autofocus="state.sortBy.value === option.value ? '' : undefined" @click="choose('sort', option.value)"
      >
        <span class="k-check"><AppIcon v-if="state.sortBy.value === option.value" name="check" :size="13" /></span>{{ option.label }}
      </button>
    </div>
  </FloatingPanel>
  <KnowledgeCreateDialog ref="createDialog" :project="project" :taken="state.slugs" @created="created" />
</template>

<style scoped>
.k-mode-sep { flex-shrink: 0; width: 1px; height: 22px; margin: 0 2px; background: var(--line-2); }
.k-mode { flex-shrink: 0; }
.k-mode button { display: flex; align-items: center; justify-content: center; gap: 6px; height: 26px; padding: 0 10px; }
.k-mode button[aria-pressed="true"] { background: var(--seg-on); box-shadow: 0 1px 2px rgba(32, 60, 61, .12), inset 0 0 0 1px var(--glass-edge); color: var(--teal-ink); }
/* ---------- Toolbar controls (teleported into the project toolbar) ---------- */
.k-search { width: 260px; flex-shrink: 0; }
.k-search .field { height: 32px; padding-right: 30px; font-size: 13.5px; }
.k-search .field::-webkit-search-cancel-button { display: none; }
.slash { position: absolute; right: 8px; pointer-events: none; }
@media (hover: none) { .slash { display: none; } }
.clear-q { position: absolute; right: 5px; display: grid; place-items: center; width: 22px; height: 22px; padding: 0; border: 0; border-radius: 50%; background: var(--chip-bg); color: var(--ink-2); }
.clear-q:hover { color: var(--ink); background: var(--row-selected); }
.clear-q:focus-visible { box-shadow: var(--focus-ring); }
.spinner { position: absolute; right: 32px; width: 12px; height: 12px; border-radius: 50%; border: 1.6px solid var(--line-2); border-right-color: var(--teal); }
@media (prefers-reduced-motion: no-preference) { .spinner { animation: k-spin .8s linear infinite; } @keyframes k-spin { to { transform: rotate(360deg); } } }
.k-menu-btn { gap: 6px; padding: 0 9px 0 12px; color: var(--ink-2); }
.k-menu-btn:hover, .k-menu-btn[aria-expanded="true"] { color: var(--ink); }
.k-menu-btn.on { color: var(--teal-ink); }
.k-menu-dim { font: 500 10px/1 var(--mono); letter-spacing: .1em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.k-chev { color: var(--ink-3); }
.k-sort-btn { padding-left: 10px; }
.k-spacer { flex: 1; }
.k-count { min-width: 10ch; text-align: right; font-size: 12px; color: var(--ink-2); white-space: nowrap; }
.k-new { height: 32px; padding: 0 14px 0 11px; gap: 6px; }
.k-menu { display: grid; gap: 1px; }
.k-menu-item { display: flex; align-items: center; gap: 8px; width: 100%; height: 34px; padding: 0 10px 0 6px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
@media (hover: hover) { .k-menu-item:hover { background: var(--row-hover); } }
.k-menu-item:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.k-menu-item[aria-checked="true"] { color: var(--teal-ink); font-weight: 600; }
.k-check { display: grid; place-items: center; width: 18px; height: 18px; color: var(--teal-ink); }
.k-menu-count { margin-left: auto; font-size: 11.5px; font-weight: 400; color: var(--ink-3); }

/* ---------- Layout: the kinds rail beside the grouped list ---------- */
/* The frame is a container: with an entry docked beside it the list is narrower
   than the window says, so the rail and the rows fold by the frame's own width. */
.k-frame { container: kframe / inline-size; }
.k-layout { display: grid; grid-template-columns: 232px minmax(0, 1fr); gap: 28px; align-items: start; padding-top: 6px; }
.k-rail { position: sticky; top: calc(var(--toolbar-h, 52px) + 12px); display: grid; gap: 18px; }
.k-kinds { display: grid; gap: 2px; }
.k-kind { display: flex; align-items: center; gap: 10px; height: 36px; padding: 0 10px 0 6px; border: 0; border-radius: 10px; background: transparent; color: var(--ink-2); font-size: 13.5px; text-align: left; }
@media (hover: hover) { .k-kind:hover { background: var(--row-hover); color: var(--ink); } }
.k-kind:focus-visible { box-shadow: var(--focus-ring); }
.k-kind[aria-current="true"] { background: var(--row-selected); color: var(--teal-ink); font-weight: 600; box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.k-kind.empty:not([aria-current]) .k-kind-label { color: var(--ink-3); }
.k-kind-icon { display: grid; place-items: center; flex-shrink: 0; width: 26px; height: 26px; border-radius: 8px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--kind, var(--ink-2)); }
.k-kind[aria-current="true"] .k-kind-icon { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--kind, var(--teal-ink)); }
.k-kind-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.k-kind-count { font-size: 11.5px; font-weight: 500; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.k-kind[aria-current="true"] .k-kind-count { color: var(--teal-ink); }
.k-agents { display: grid; gap: 8px; padding: 14px 14px 12px; border-radius: 14px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
.k-agents-title { display: flex; align-items: center; gap: 8px; font-size: 12.5px; font-weight: 650; color: var(--ink); }
.k-agents-title svg { color: var(--teal-ink); }
.k-agents-text { font-size: 12px; line-height: 1.5; color: var(--ink-2); }
.k-command { display: flex; align-items: flex-start; gap: 4px; padding: 6px 4px 6px 9px; border-radius: 9px; background: var(--code-bg); box-shadow: inset 0 0 0 1px var(--line); }
.k-command code { flex: 1; min-width: 0; padding-top: 5px; font-size: 11.5px; line-height: 1.5; color: var(--ink); overflow-wrap: anywhere; }
.k-command .tok { white-space: nowrap; }

/* ---------- Groups and rows ---------- */
.k-list { display: grid; grid-template-columns: minmax(0, 1fr); gap: 18px; min-width: 0; container: klist / inline-size; }
.k-list.stale .k-group { opacity: .62; }
@media (prefers-reduced-motion: no-preference) { .k-group { transition: opacity .12s ease; } }
.k-group { overflow: clip; }
.k-group-head { display: flex; align-items: center; gap: 10px; min-height: 52px; padding: 10px 12px 10px 16px; border-bottom: 1px solid var(--line); }
/* The kind's hue marks its icon (the graph's bubbles and legend use the same); the tile stays neutral. */
.k-type-mark { display: grid; place-items: center; flex-shrink: 0; width: 30px; height: 30px; border-radius: 9px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--kind, var(--teal-ink)); }
.k-group-head h2 { font: 650 15px/1.3 var(--font); letter-spacing: -.005em; color: var(--ink); }
.k-group-count { font-size: 11.5px; color: var(--ink-3); }
.k-group-hint { flex: 1; min-width: 0; margin-left: 6px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; color: var(--ink-3); }
.k-add { gap: 5px; margin-left: auto; padding: 0 10px 0 8px; color: var(--ink-2); }
.k-add:hover { color: var(--teal-ink); }
.k-rows { margin: 0; padding: 4px 6px 6px; list-style: none; }
.k-row {
  display: flex; align-items: center; gap: 18px; min-height: 58px; padding: 9px 10px 9px 12px; border-radius: 10px; color: var(--ink); text-decoration: none;
  scroll-margin-top: calc(var(--toolbar-h, 52px) + 16px); scroll-margin-bottom: 24px;
}
li + li .k-row { position: relative; }
li + li .k-row::before { content: ''; position: absolute; top: 0; left: 12px; right: 10px; height: 1px; background: var(--line); }
@media (hover: hover) { .k-row:hover { background: var(--row-hover); } .k-row:hover::before, li:hover + li .k-row::before { opacity: 0; } }
.k-row.cursor { background: var(--row-selected); }
/* The entry docked beside the list: the same tint, and a hairline ring in the row's shape (no edge accents). */
.k-row.open { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.k-row.open::before, li:has(.k-row.open) + li .k-row::before { opacity: 0; }
.k-row.open:focus-visible { box-shadow: inset 0 0 0 1px var(--chip-teal-line), var(--focus-ring); }
.k-row.cursor::before, li:has(.k-row.cursor) + li .k-row::before { opacity: 0; }
.k-row:focus-visible { box-shadow: var(--focus-ring); }
.k-text { display: grid; gap: 3px; flex: 1; min-width: 0; }
.k-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; font-weight: 600; line-height: 1.35; color: var(--ink); }
.k-excerpt { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
.k-excerpt.empty { color: var(--ink-3); font-style: italic; }
.k-row.archived .k-title { color: var(--ink-2); }
.k-meta { display: flex; align-items: center; gap: 12px; flex-shrink: 0; }
.k-slug { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11.5px; color: var(--ink-3); font-variant-ligatures: none; }
.k-links { display: inline-flex; justify-content: flex-end; width: 34px; font-size: 11.5px; color: var(--ink-3); }
.k-links-inner { display: inline-flex; align-items: center; gap: 4px; }
.k-time { width: 64px; text-align: right; font-size: 12px; color: var(--ink-2); white-space: nowrap; }
.k-status { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; font: 600 10px/1 var(--mono); letter-spacing: .08em; text-transform: uppercase; font-variant-ligatures: none; }
.k-status.proposed { background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); color: var(--gold-ink); }
.k-status.archived { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.k-banner { display: flex; align-items: center; gap: 8px; padding: 9px 12px; border-radius: 10px; background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .35); font-size: 12.5px; color: var(--ink); }
.k-banner svg { color: var(--gold-ink); flex-shrink: 0; }

/* ---------- States ---------- */
.k-state { display: grid; justify-items: center; gap: 8px; padding: 52px 24px 56px; text-align: center; }
.k-state h2 { font-size: 18px; color: var(--ink); }
.k-state > p { max-width: 460px; font-size: 13.5px; color: var(--ink-2); }
.state-icon { display: grid; place-items: center; width: 48px; height: 48px; margin-bottom: 6px; border-radius: 15px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line), 0 10px 26px -14px var(--teal); color: var(--teal-ink); }
.state-icon.danger { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.state-actions { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; margin-top: 10px; }
.k-starters { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; width: min(100%, 720px); margin-top: 16px; }
.k-starter { display: flex; align-items: flex-start; gap: 12px; padding: 14px; border: 0; border-radius: 12px; background: var(--btn-bg); box-shadow: var(--shadow-btn); color: var(--ink); text-align: left; }
.k-starter:hover { background: var(--btn-bg-hover); box-shadow: var(--shadow-btn-hover); }
.k-starter:focus-visible { box-shadow: var(--focus-ring); }
.k-starter strong { display: block; font-size: 13.5px; }
.k-starter strong + span { display: block; margin-top: 2px; font-size: 12px; line-height: 1.45; color: var(--ink-2); }
.k-starter .k-kind-icon { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--kind, var(--teal-ink)); }
.k-fine { font-size: 12.5px; color: var(--ink-3); }
.k-hint { display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 5px; padding: 6px 0; font-size: 12px; color: var(--ink-3); }
.k-hint .keycap + .keycap { margin-left: 2px; }
.k-skeleton { display: grid; gap: 18px; }
.sk-icon { width: 30px; height: 30px; border-radius: 9px; }
.sk-head { width: 140px; height: 12px; }
.sk-row { display: grid; gap: 8px; padding: 14px 18px; border-top: 1px solid var(--line); }
.sk-title { height: 12px; }
.sk-line { width: 72%; height: 9px; }

/* ---------- Narrower pages ---------- */
@container toolbar (max-width: 1180px) { .k-search { width: 220px; } .k-sort-label { display: none; } .k-sort-btn { padding: 0 9px; } }
@container toolbar (max-width: 1080px) { .k-mode-label { display: none; } .k-mode button { padding: 0 8px; } }
/* Beside the preview pane the toolbar is list-wide: the search gives way before anything wraps. */
@container toolbar (max-width: 720px) { .k-search { width: auto; flex: 1 1 120px; min-width: 120px; } .k-spacer { display: none; } }
@container toolbar (max-width: 1000px) { .k-count { display: none; } .k-new { width: 32px; padding: 0; } .k-new-label { display: none; } .k-menu-dim { display: none; } }
/* Docked beside an entry (U25) the list is narrower than the window says: the
   frame and the list fold by their own width (selectors carry .k-frame / .k-list
   so they win over the window-width rules below). The rail becomes the row of
   kind chips, and narrow rows put their slug and time under the words. */
@container kframe (max-width: 999px) { .k-frame .k-group-hint { display: none; } }
@container kframe (max-width: 819px) {
  .k-frame .k-layout { grid-template-columns: minmax(0, 1fr); gap: 14px; }
  .k-frame .k-rail { position: static; }
  .k-frame .k-kinds { display: flex; gap: 6px; overflow-x: auto; margin: 0 0 0 calc(-1 * var(--gutter)); padding: 2px 0 4px var(--gutter); scrollbar-width: none; }
  .k-frame .k-kinds::-webkit-scrollbar { display: none; }
  .k-frame .k-kind { flex-shrink: 0; height: 34px; padding: 0 12px 0 5px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
  .k-frame .k-kind[aria-current="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
  .k-frame .k-kind-icon { width: 24px; height: 24px; border-radius: 999px; background: transparent; box-shadow: none; }
  .k-frame .k-agents { display: none; }
}
@container klist (max-width: 760px) {
  .k-list .k-row { flex-direction: column; align-items: stretch; gap: 5px; min-height: 0; padding: 10px 10px 10px 12px; }
  .k-list .k-meta { gap: 12px; }
  .k-list .k-slug { flex: 1; max-width: none; }
  .k-list .k-time { width: auto; }
}
@media (max-width: 1280px) { .k-layout { grid-template-columns: 208px minmax(0, 1fr); gap: 22px; } .k-group-hint { display: none; } }
@media (max-width: 1100px) {
  .k-layout { grid-template-columns: minmax(0, 1fr); gap: 14px; }
  .k-rail { position: static; }
  .k-kinds { display: flex; gap: 6px; overflow-x: auto; margin: 0 calc(-1 * var(--gutter)); padding: 2px var(--gutter) 4px; scrollbar-width: none; }
  .k-kinds::-webkit-scrollbar { display: none; }
  .k-kind { flex-shrink: 0; height: 34px; padding: 0 12px 0 5px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
  .k-kind-icon { width: 24px; height: 24px; border-radius: 999px; background: transparent; box-shadow: none; }
  .k-agents { display: none; }
}
@media (max-width: 720px) {
  .k-row { flex-direction: column; align-items: stretch; gap: 6px; min-height: 0; padding: 11px 10px; }
  .k-title { white-space: normal; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; }
  .k-excerpt { white-space: normal; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; }
  .k-meta { gap: 10px; }
  .k-slug { flex: 1; max-width: none; }
  .k-time { width: auto; }
  .k-starters { grid-template-columns: minmax(0, 1fr); }
  .k-add span { display: none; }
  .k-add { width: 32px; padding: 0; }
  .k-hint { display: none; }
}
@media (max-width: 600px) {
  .k-kinds { margin: 0 -12px; padding: 2px 12px 4px; }
  .k-kind, .k-frame .k-kind { height: 44px; }
  .k-mode { padding: 2px; }
  .k-mode button { width: 40px; height: 40px; padding: 0; }
  .k-mode-label { display: none; }
  .k-search { flex: 1 1 100px; width: auto; min-width: 0; }
  .k-search .slash { display: none; }
  .k-search .field { height: 44px; font-size: 16px; }
  .k-menu-btn { height: 44px; }
  .k-sort-btn { width: 44px; padding: 0; justify-content: center; }
  .k-sort-btn .k-chev { display: none; }
  .k-spacer, .k-count { display: none; }
  .k-new { width: 44px; height: 44px; padding: 0; }
  .k-new-label { display: none; }
  .k-group-head { padding: 10px 10px 10px 12px; }
  .k-rows { padding: 2px 4px 4px; }
}
</style>
