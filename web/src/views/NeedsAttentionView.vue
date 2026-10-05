<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ATTENTION_KINDS, actOnAttention, attentionIdentity, listAttention, type AttentionFacet, type AttentionFilters, type AttentionItem, type AttentionPage } from '../lib/attention'
import { createScope, scopeOwner } from '../lib/identityScope'
import { toast, dismiss } from '../lib/toast'
import { statusMeta, relativeTime } from '../lib/work'
import { useSession } from '../stores/session'
import { useProjects } from '../stores/projects'
import AppIcon, { type IconName } from '../components/AppIcon.vue'
import StatusIcon from '../components/work/StatusIcon.vue'
import TicketLink from '../components/releases/TicketLink.vue'
import KeyCap from '../components/KeyCap.vue'
import FloatingPanel from '../components/work/FloatingPanel.vue'
import { vClipTip } from '../directives/clipTip'

// Built-in shared saved view. Filters live in its URL; resolved rows live only
// in this visit, so neither events nor a bulk result can move a clicked row.
const route = useRoute(), router = useRouter(), session = useSession(), projects = useProjects()
const scope = createScope(() => scopeOwner(session.identity)), reads = scope.lane()
type Row = AttentionItem & { resolved?: 'apply' | 'dismiss'; failure?: string }
const rows = ref<Row[]>([]), counts = ref<AttentionPage['counts']>({}), total = ref(0)
const facets = ref<AttentionPage['facets']>({ projects: [], assignees: [] }), truncated = ref(false)
const next = ref<string | null>(null), loading = ref(false), busy = ref(false), error = ref('')
const selected = ref(new Set<number>()), search = ref(''), sentinel = ref<HTMLElement>(), undoToast = ref<number>()
const menu = ref<{ type: 'kind' | 'project_id' | 'assignee'; anchor: HTMLElement } | null>(null)
let generation = 0, searchTimer: ReturnType<typeof setTimeout> | undefined, observer: IntersectionObserver | undefined
const filters = computed<AttentionFilters>(() => ({ kind: String(route.query.kind ?? ''), project_id: String(route.query.project_id ?? ''), assignee: String(route.query.assignee ?? ''), q: String(route.query.q ?? '') }))
const currentProject = computed(() => projects.byId(filters.value.project_id))
const heading = computed(() => currentProject.value?.title ?? 'All projects')
const chosen = computed(() => rows.value.filter(row => selected.value.has(row.event_id) && !row.resolved && row.editable))
const selectable = computed(() => rows.value.filter(row => row.editable && !row.resolved))
const allSelected = computed(() => selectable.value.length > 0 && selectable.value.every(row => selected.value.has(row.event_id)))
const selectedState = computed(() => allSelected.value ? 'true' : selected.value.size ? 'mixed' : 'false')
const kinds = new Map(ATTENTION_KINDS.map(kind => [kind.id, kind]))
const titleFor = (row: Row) => row.to === 'release' ? row.release_title || 'Choose a release' : statusMeta(row.to).label
const facetLabel = (type: 'kind' | 'project_id' | 'assignee') => type === 'kind' ? 'Kind' : type === 'project_id' ? 'Project' : 'Assignee'
const options = computed<AttentionFacet[]>(() => !menu.value ? [] : menu.value.type === 'kind' ? [{ id: '', label: 'Every kind' }, ...ATTENTION_KINDS.map(kind => ({ id: kind.id, label: kind.label }))] : menu.value.type === 'project_id' ? [{ id: '', label: 'All projects' }, ...facets.value.projects] : [{ id: '', label: 'Every assignee' }, { id: 'none', label: 'Unassigned' }, ...facets.value.assignees])
function closeMenu(restore: boolean) { const anchor = menu.value?.anchor; menu.value = null; if (restore) anchor?.focus() }
function setFilter(field: keyof AttentionFilters, value: string) {
  closeMenu(true)
  const query = { ...route.query, view: 'needs-attention', [field]: value || undefined }
  void router.replace({ path: '/tickets', query })
}
function resetVisit() {
  generation++; reads.cancel(); rows.value = []; selected.value.clear(); counts.value = {}; total.value = 0; next.value = null; error.value = ''; busy.value = false; loading.value = false
  closeMenu(false); if (undoToast.value) dismiss(undoToast.value)
}
function load(more = false) {
  if (loading.value || (more && !next.value)) return
  loading.value = true; error.value = ''
  const cursor = more ? next.value! : undefined, query = { ...filters.value }
  void reads.run(({ after, signal }) => after(listAttention(query, signal, cursor), page => {
    if (more) {
      const existing = new Set(rows.value.map(row => row.event_id)); rows.value.push(...page.items.filter(row => !existing.has(row.event_id)))
    } else rows.value = page.items
    counts.value = page.counts; total.value = page.total; facets.value = page.facets; truncated.value = page.facets_truncated; next.value = page.next_cursor
  }), { failed: e => { error.value = e instanceof Error ? e.message : 'Needs attention could not be loaded.' }, settled: () => { loading.value = false } })
}
watch([() => scopeOwner(session.identity), filters], () => {
  clearTimeout(searchTimer); scope.reset(); resetVisit(); search.value = filters.value.q; load()
}, { immediate: true })
watch(search, value => { if (value === filters.value.q) return; clearTimeout(searchTimer); searchTimer = setTimeout(() => setFilter('q', value.slice(0, 200)), 250) })
function select(row: Row) {
  if (busy.value || row.resolved || !row.editable) return
  if (selected.value.has(row.event_id)) selected.value.delete(row.event_id)
  else if (selected.value.size < 100) selected.value.add(row.event_id)
  else toast('Choose up to 100 tickets at a time.')
}
function selectAll() {
  if (busy.value) return
  if (allSelected.value) selected.value.clear()
  else { selected.value = new Set(selectable.value.slice(0, 100).map(row => row.event_id)); if (selectable.value.length > 100) toast('The first 100 editable tickets are selected.') }
}
function act(action: 'apply' | 'dismiss' | 'undo', targets: Row[], focusID?: number) {
  if (busy.value || !targets.length) return
  const snapshot = targets.map(attentionIdentity), visit = generation
  busy.value = true
  void scope.run(({ after, signal }) => after(actOnAttention(action, snapshot, signal), response => {
    if (visit !== generation) return
    const successes: Row[] = [], failures: string[] = []
    for (const target of snapshot) {
      const row = rows.value.find(row => row.event_id === target.event_id && row.node_id === target.node_id && row.revision === target.revision)
      if (!row) continue
      const result = response.items.find(result => result.event_id === target.event_id)
      if (!result?.ok || !result.revision || (action !== 'undo' && !result.resolution_event_id)) {
        row.failure = result?.error ?? 'The change could not be confirmed. Reload before trying again.'; failures.push(`${row.key}: ${row.failure}`); continue
      }
      row.revision = result.revision; row.failure = ''; row.resolution_event_id = result.resolution_event_id
      if (action === 'undo') { row.resolved = undefined; counts.value[row.kind] = (counts.value[row.kind] ?? 0) + 1; total.value++ }
      else { row.resolved = action; selected.value.delete(row.event_id); counts.value[row.kind] = Math.max(0, (counts.value[row.kind] ?? 0) - 1); total.value = Math.max(0, total.value - 1) }
      successes.push(row)
    }
    if (successes.length && action !== 'undo') {
      const identities = successes.map(attentionIdentity)
      undoToast.value = toast(`${successes.length} ${action === 'apply' ? 'applied' : 'dismissed'}${failures.length ? `; ${failures.length} could not be changed` : ''}.`, { key: 'attention-result', timeout: 10000, action: { label: identities.length > 1 ? 'Undo all' : 'Undo', run: () => {
        if (generation !== visit) return
        const current = identities.flatMap(id => { const row = rows.value.find(row => row.event_id === id.event_id && row.revision === id.revision && row.resolution_event_id === id.resolution_event_id); return row ? [row] : [] })
        act('undo', current)
      } } })
    }
    if (failures.length) toast(failures.join('\n'), { tone: 'error', key: 'attention-errors' })
    if (focusID) void nextTick(() => { if (generation === visit) document.getElementById(`${action === 'undo' ? 'apply' : 'undo'}-${focusID}`)?.focus({ preventScroll: true }) })
  }), { failed: e => { if (visit === generation) toast(e instanceof Error ? e.message : 'The change could not be confirmed.', { tone: 'error' }) }, settled: () => { if (visit === generation) busy.value = false } })
}
function menuKeys(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const buttons = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]')]
  const index = buttons.indexOf(document.activeElement as HTMLButtonElement)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length
  event.preventDefault(); event.stopPropagation(); buttons[next]?.focus()
}
function keys(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
  const typing = event.target instanceof HTMLElement && (event.target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(event.target.tagName))
  if (event.key === 'Escape' && typing) { (event.target as HTMLElement).blur(); event.preventDefault(); return }
  if (typing) return
  if (event.key === '/') { event.preventDefault(); document.getElementById('attention-search')?.focus() }
  if (event.key === 'Escape') { selected.value.clear(); closeMenu(true) }
}
onMounted(() => {
  void projects.load()
  window.addEventListener('keydown', keys)
  observer = new IntersectionObserver(entries => { if (entries.some(entry => entry.isIntersecting) && !error.value) load(true) }, { rootMargin: '200px' })
  if (sentinel.value) observer.observe(sentinel.value)
})
onBeforeUnmount(() => { generation++; scope.dispose(); observer?.disconnect(); clearTimeout(searchTimer); window.removeEventListener('keydown', keys); if (undoToast.value) dismiss(undoToast.value) })
</script>

<template>
  <section class="attention-page" aria-labelledby="attention-title">
    <header class="attention-head">
      <h1 id="attention-title">{{ heading }}</h1>
      <p>Every project you can see. Needs attention collects what the autopilot flagged for a person.</p>
      <div class="stat-line" role="group" aria-label="Filter by kind">
        <button v-for="kind in ATTENTION_KINDS" :key="kind.id" type="button" class="stat" :aria-pressed="filters.kind === kind.id" @click="setFilter('kind', filters.kind === kind.id ? '' : kind.id)">
          <StatusIcon v-if="kind.id === 'cancel'" state="cancelled" :size="12" /><AppIcon v-else :name="kind.icon as IconName" :size="12" /><b>{{ counts[kind.id] ?? '—' }}</b><span>{{ kind.label.toLowerCase() }}</span>
        </button>
      </div>
    </header>
    <div class="navigation-band">
      <nav class="section-tabs" aria-label="Sections"><RouterLink to="/releases"><AppIcon name="box" :size="15" />Releases</RouterLink><RouterLink class="active" to="/tickets?view=needs-attention" aria-current="page"><AppIcon name="ticket" :size="15" />Tickets</RouterLink><RouterLink to="/knowledge"><AppIcon name="book" :size="15" />Knowledge</RouterLink></nav>
      <nav class="view-tabs" aria-label="Saved views"><RouterLink class="active" :to="{ path: '/tickets', query: { ...route.query, view: 'needs-attention' } }" aria-current="page"><AppIcon name="flag" :size="13" />Needs attention<AppIcon name="users" :size="12" /></RouterLink></nav>
    </div>
    <div class="controls-band" role="toolbar" aria-label="List controls">
      <span class="list-mode"><AppIcon name="list" :size="15" />List</span>
      <label class="search-field"><AppIcon name="search" :size="14" /><input id="attention-search" v-model="search" type="search" maxlength="200" placeholder="Search" aria-label="Search this view" /><KeyCap k="/" /></label>
      <div class="facets">
        <button v-for="type in (['kind', 'project_id', 'assignee'] as const)" :key="type" type="button" class="btn sm facet-button" :class="{ filtered: !!filters[type] }" :aria-label="`${facetLabel(type)} filter`" :aria-expanded="menu?.type === type" @click="menu = menu?.type === type ? null : { type, anchor: $event.currentTarget as HTMLElement }">{{ facetLabel(type) }}<AppIcon name="chevron" :size="12" /></button>
      </div>
      <button type="button" class="btn sm reset" :aria-disabled="!Object.values(filters).some(Boolean)" @click="router.replace('/tickets?view=needs-attention')">Clear filters</button>
    </div>
    <div class="table-card">
      <div class="attention-table" role="table" aria-label="Tickets needing attention" :aria-busy="loading || busy">
        <div class="trow thead" role="row">
          <span role="columnheader" class="c-sel"><button type="button" class="cbx" role="checkbox" :aria-checked="selectedState" aria-label="Select every ticket shown" :disabled="busy || !selectable.length" @click="selectAll"><AppIcon :name="allSelected ? 'check' : selected.size ? 'minus' : 'square'" :size="16" /></button></span><span role="columnheader">Key</span><span role="columnheader">Title</span><span role="columnheader">Kind</span><span role="columnheader">Suggestion</span><span role="columnheader" class="c-since">Since</span><span role="columnheader" class="c-act">Actions</span>
        </div>
        <div v-for="row in rows" :key="row.event_id" class="trow" :class="{ resolved: row.resolved, selected: selected.has(row.event_id) }" role="row" :data-event-id="row.event_id">
          <span class="c-sel" role="cell"><button type="button" class="cbx" role="checkbox" :aria-checked="selected.has(row.event_id)" :aria-label="`Select ${row.key}`" :disabled="busy || !!row.resolved || !row.editable" @click="select(row)"><AppIcon :name="selected.has(row.event_id) ? 'check' : 'square'" :size="16" /></button></span>
          <span class="c-key" role="cell"><TicketLink :ticket-key="row.key" /></span>
          <span class="c-title" role="cell"><AppIcon name="ticket" :size="14" /><span v-clip-tip="`${row.title} · ${row.reason}`" tabindex="0" class="title">{{ row.title }}</span></span>
          <span class="c-kind" role="cell"><AppIcon v-if="row.kind !== 'cancel'" :name="kinds.get(row.kind)!.icon as IconName" :size="13" /><StatusIcon v-else state="cancelled" :size="13" />{{ kinds.get(row.kind)!.one }}</span>
          <span class="c-sug" role="cell" :data-tip="row.reason"><StatusIcon :state="row.from" :size="12" />{{ statusMeta(row.from).label }}<AppIcon name="arrow" :size="11" /><span class="sr-only">to</span><AppIcon v-if="row.to === 'release'" name="box" :size="12" /><StatusIcon v-else :state="row.to" :size="12" /><span v-clip-tip="titleFor(row)" tabindex="0">{{ titleFor(row) }}</span></span>
          <time class="c-since" role="cell" :datetime="row.at" :data-tip="row.reason">{{ relativeTime(row.at) }}</time>
          <span class="c-act" role="cell">
            <span class="action-stack">
              <span class="row-actions" :class="{ hidden: row.resolved }" :aria-hidden="!!row.resolved">
                <button :id="`apply-${row.event_id}`" type="button" class="btn sm" :disabled="busy || !row.applicable" :tabindex="row.resolved ? -1 : 0" :aria-label="`Apply to ${row.key}: ${statusMeta(row.from).label} to ${titleFor(row)}`" :data-tip="row.unavailable_reason || row.reason" @click="act('apply', [row], row.event_id)">Apply</button>
                <button type="button" class="btn sm ghost" :disabled="busy || !row.editable" :tabindex="row.resolved ? -1 : 0" :aria-label="`Dismiss for ${row.key}`" :data-tip="row.editable ? 'Keep it as it is; this suggestion does not return' : 'Editing this ticket needs permission'" @click="act('dismiss', [row], row.event_id)">Dismiss</button>
              </span>
              <span class="done-mark" :class="{ hidden: !row.resolved }" :aria-hidden="!row.resolved">{{ row.resolved === 'dismiss' ? 'Dismissed' : 'Applied' }} · <button :id="`undo-${row.event_id}`" type="button" class="link-btn" :tabindex="row.resolved ? 0 : -1" :disabled="busy" :aria-label="`Undo for ${row.key}`" @click="act('undo', [row], row.event_id)">Undo</button></span>
            </span>
          </span>
          <p v-if="row.failure" class="row-error" role="alert">{{ row.failure }}</p>
        </div>
      </div>
      <p v-if="!rows.length" class="empty-list" :role="error ? 'alert' : 'status'">{{ error || (loading ? 'Loading suggestions…' : 'Nothing here needs attention.') }}</p>
    </div>
    <div class="list-footer">
      <p class="list-count">{{ rows.length }} of {{ Math.max(total + rows.filter(row => row.resolved).length, rows.length) }} shown · {{ next ? 'the rest loads as you scroll, 50 at a time' : 'all matching suggestions shown' }}</p>
      <p v-if="error && rows.length" role="alert">{{ error }}</p>
      <button v-if="error" type="button" class="btn sm" @click="load(rows.length > 0)">Retry</button>
      <button v-else-if="next" type="button" class="btn sm" :disabled="loading" @click="load(true)">{{ loading ? 'Loading…' : 'Load 50 more' }}</button>
      <span ref="sentinel" class="sentinel" aria-hidden="true" />
    </div>
    <div v-if="chosen.length" class="selection-dock"><div class="selection-bar" role="toolbar" aria-label="Selected tickets"><span><b>{{ chosen.length }}</b> selected</span><span class="selection-hint">Apply uses each row’s own suggestion</span><button type="button" class="btn sm primary" :disabled="busy || chosen.some(row => !row.applicable)" @click="act('apply', chosen)">Apply {{ chosen.length }}</button><button type="button" class="btn sm" :disabled="busy" @click="act('dismiss', chosen)">Dismiss {{ chosen.length }}</button><button type="button" class="btn sm ghost" aria-label="Clear the selection" :disabled="busy" @click="selected.clear()"><AppIcon name="close" :size="13" />Clear<KeyCap k="esc" /></button></div></div>
    <FloatingPanel v-if="menu" :anchor="menu.anchor" :label="`${facetLabel(menu.type)} filter`" @close="closeMenu">
      <div class="facet-menu" @keydown="menuKeys" role="menu" :aria-label="`${facetLabel(menu.type)} filter`"><button v-for="option in options" :key="option.id" type="button" role="menuitemradio" :aria-checked="filters[menu.type] === option.id" :data-autofocus="filters[menu.type] === option.id ? '' : undefined" @click="setFilter(menu!.type, option.id)"><span v-clip-tip>{{ option.label }}</span><AppIcon v-if="filters[menu.type] === option.id" name="check" :size="13" /></button></div><p v-if="truncated" class="facet-note">Only the first 100 facet choices are shown. Search narrows the ticket list.</p>
    </FloatingPanel>
  </section>
</template>

<style scoped>
.attention-page { max-width: 1800px; margin: 0 auto; width: 100%; padding: 18px 28px 100px; }
.attention-head { display: flex; align-items: baseline; flex-wrap: wrap; gap: 6px 18px; padding-bottom: 14px; }
.attention-head h1 { font-size: 26px; overflow-wrap: anywhere; }
.attention-head > p { color: var(--ink-2); font-size: 13px; }
.stat-line { flex-basis: 100%; display: flex; flex-wrap: wrap; gap: 4px 18px; }
.stat { display: inline-flex; align-items: center; gap: 5px; min-height: 30px; padding: 0; border: 0; background: transparent; color: var(--ink-2); font-size: 12px; }
.stat b { min-width: 4ch; text-align: right; font-family: var(--mono); font-variant-numeric: tabular-nums; }
.stat[aria-pressed="true"] { color: var(--teal-ink); }
.navigation-band { display: flex; flex-wrap: wrap; gap: 12px 18px; padding: 8px 0; border-block: 1px solid var(--line); }
.section-tabs, .view-tabs { display: flex; align-items: center; gap: 4px; }
.section-tabs a, .view-tabs a { display: flex; align-items: center; justify-content: center; gap: 6px; min-height: 36px; padding: 0 12px; border-radius: 8px; font-size: 13px; color: var(--ink-2); text-decoration: none; }
a.active { background: var(--row-selected); color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--line); }
.controls-band { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; padding: 10px 0; }
.list-mode { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.search-field { display: flex; align-items: center; gap: 6px; width: clamp(140px, 20vw, 280px); min-width: 0; padding: 0 10px; min-height: 34px; border-radius: 8px; box-shadow: inset 0 0 0 1px var(--line-2); background: var(--surface); }
.search-field input { width: 100%; min-width: 0; padding: 0; border: 0; background: transparent; color: var(--ink); font-size: 13px; outline: none; }
.search-field:focus-within { box-shadow: var(--focus-ring); }.search-field :deep(.keycap) { flex-shrink: 0; }
.facets { display: flex; flex-wrap: wrap; gap: 8px; }.facet-button.filtered { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--teal); }.reset { margin-left: auto; }
.table-card { border-radius: 14px; box-shadow: var(--shadow), inset 0 0 0 1px var(--line); background: var(--surface-raised); overflow: clip; }
.attention-table { display: grid; grid-template-columns: 46px 10ch minmax(0, 1fr) 18ch 26ch 7ch max-content; }
.trow { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: center; height: 40px; border-bottom: 1px solid var(--line); font-size: 13.5px; position: relative; }
.trow:last-child { border-bottom: 0; }.trow:hover { background: var(--row-hover); }.trow.selected { background: var(--row-selected); }
.trow > [role="cell"], .thead > span { min-width: 0; padding: 0 10px; display: flex; align-items: center; gap: 7px; white-space: nowrap; overflow: hidden; }
.trow > .c-sel { padding: 0; justify-content: center; }.thead { height: 35px; background: var(--surface-2); }.thead > span { font: 500 10.5px/1 var(--mono); text-transform: uppercase; letter-spacing: .1em; color: var(--ink-3); }
.c-key { font: 500 12px/1 var(--mono); }.c-title { color: var(--ink); }.c-title > svg { flex-shrink: 0; color: var(--ink-3); }.title { min-width: 0; overflow: hidden; text-overflow: ellipsis; }.c-kind, .c-sug { font-size: 12.5px; color: var(--ink-2); }.c-kind svg { color: var(--teal-ink); }.c-sug > span:last-child { overflow: hidden; text-overflow: ellipsis; }.c-since { justify-content: end; color: var(--ink-2); font: 500 12px/1 var(--mono); }.trow > .c-act { justify-content: end; overflow: visible; }
.cbx { display: grid; place-items: center; width: 28px; height: 28px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-3); }.cbx[aria-checked="true"] { color: var(--teal-ink); background: var(--row-selected); }.cbx:disabled { opacity: .4; }
.action-stack { display: grid; }.action-stack > * { grid-area: 1 / 1; }.row-actions, .done-mark { display: inline-flex; align-items: center; gap: 6px; }.done-mark { justify-self: end; font-size: 12px; color: var(--ink-2); }.hidden { visibility: hidden; pointer-events: none; }.resolved > [role="cell"]:not(.c-act):not(.c-sel) { opacity: .5; }.link-btn { border: 0; background: transparent; color: var(--teal-ink); font-size: inherit; padding: 4px 0; }.row-error { position: absolute; z-index: 2; top: 100%; right: 8px; max-width: min(400px, 80%); padding: 6px 10px; background: var(--danger-bg); box-shadow: var(--shadow); color: var(--danger); font-size: 12px; }
.empty-list { padding: 28px 18px; text-align: center; color: var(--ink-3); font-size: 13px; }.list-footer { text-align: right; padding-top: 10px; }.list-count { font: 400 12px/1.5 var(--mono); color: var(--ink-2); margin-bottom: 8px; }.sentinel { display: block; height: 1px; }
.selection-dock { position: fixed; left: 0; right: 0; bottom: calc(var(--footer-h, 0px) + 18px + env(safe-area-inset-bottom)); z-index: 30; display: flex; justify-content: center; pointer-events: none; }.selection-bar { pointer-events: auto; display: flex; flex-wrap: wrap; gap: 8px 10px; align-items: center; max-width: calc(100vw - 32px); padding: 8px 8px 8px 16px; border-radius: 16px; background: var(--surface-raised); box-shadow: var(--shadow-pop); font-size: 13px; }.selection-bar b { font-family: var(--mono); }.selection-hint { color: var(--ink-3); font-size: 12px; }
.facet-menu { display: grid; padding: 6px; max-height: min(60dvh, 480px); overflow: auto; }.facet-menu button { min-width: 0; display: flex; justify-content: space-between; align-items: center; gap: 8px; min-height: 44px; padding: 0 10px; border: 0; background: transparent; text-align: left; color: var(--ink); border-radius: 6px; }.facet-menu button > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }.facet-menu button[aria-checked="true"] { background: var(--row-selected); }.facet-note { padding: 10px; font-size: 12px; }
@media (max-width: 1100px) { .attention-head > p { flex-basis: 100%; }.attention-table { grid-template-columns: 40px 10ch minmax(0, 1fr) 17ch 23ch 7ch max-content; }.trow > [role="cell"], .thead > span { padding-inline: 6px; }.c-kind { font-size: 11px; }.c-kind svg { display: none; } }
@media (max-width: 900px) { .attention-table { grid-template-columns: 40px 9ch minmax(0, 1fr) 14ch 20ch 6ch max-content; }.c-kind { overflow: hidden; text-overflow: ellipsis; } }
@media (max-width: 720px) {
 .attention-page { padding: 14px 12px 140px; }.attention-head h1 { font-size: 24px; }.attention-head > p { display: none; }.stat-line { gap: 4px 12px; }.stat { min-height: 44px; }.stat b { min-width: 3ch; }.navigation-band { gap: 4px; }.section-tabs a, .view-tabs a { min-height: 44px; padding-inline: 8px; }.list-mode { display: none; }.search-field { flex: 1; width: auto; min-height: 44px; }.facets { flex-basis: 100%; order: 2; }.reset { margin-left: 0; }.facet-button { min-height: 44px; }
 .attention-table { display: block; }.trow { display: grid; grid-template-columns: 44px minmax(0, 1fr) max-content; grid-template-areas: 'sel key since' 'sel title title' 'sel kind kind' 'sel sug sug' 'sel act act'; height: auto; min-height: 190px; gap: 4px 0; padding: 10px 12px 12px 0; }.thead { display: none; }.trow > [role="cell"] { padding: 0; white-space: normal; }.trow > .c-sel { grid-area: sel; align-self: start; }.c-key { grid-area: key; }.c-since { grid-area: since; }.c-title { grid-area: title; height: 42px; align-items: start !important; font-size: 14.5px; line-height: 1.4; }.title { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }.c-title svg { margin-top: 3px; }.c-kind { grid-area: kind; }.c-kind svg { display: block; }.c-sug { grid-area: sug; flex-wrap: wrap; }.trow > .c-act { grid-area: act; justify-content: start; }.cbx { width: 44px; height: 44px; }.row-actions .btn, .link-btn { min-height: 44px; }.done-mark { justify-self: start; }.selection-dock { bottom: var(--footer-h, 0px); }.selection-bar { width: 100%; max-width: 100%; border-radius: 0; padding: 10px 12px calc(10px + env(safe-area-inset-bottom)); }.selection-hint { display: none; }
}
@media (pointer: coarse) { .cbx { width: 44px; height: 44px; }.trow { min-height: 48px; }.btn.sm, .link-btn { min-height: 44px; } }
@media (prefers-reduced-motion: no-preference) { .trow { transition: background .12s ease; } }
</style>
