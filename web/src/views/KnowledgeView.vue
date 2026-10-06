<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon from '../components/AppIcon.vue'
import { brand, setPageTitle } from '../lib/brand'
import { TYPES, countBy, entryPath, filterItems, highlightWords, isKnowledgeType, kindToken, listKnowledge, sortItems, statusLabel, typeMeta, type KnowledgeItem, type KnowledgeStatus, type KnowledgeType } from '../lib/knowledge'
import { absoluteTime, plural, relativeTime } from '../lib/work'
import { useProjects } from '../stores/projects'
import { knowledgeFooter } from '../lib/footerProviders'
import { useFooterSummary } from '../lib/footerSummary'

// Knowledge across every project: one search over titles, slugs and text, results
// grouped by project (each group leads into that project's Knowledge tab), the
// kinds as filters, archived entries only on request. Keys: / searches, j k move,
// Enter opens.
const route = useRoute()
const router = useRouter()
const projects = useProjects()
const input = ref<HTMLInputElement>()
const listEl = ref<HTMLElement>()
const items = ref<KnowledgeItem[]>([])
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const searchedFor = ref('')
const cursorId = ref<string | null>(null)
const now = ref(Date.now())
const PER_PROJECT = 6

const q = computed(() => typeof route.query.q === 'string' ? route.query.q : '')
const type = computed<KnowledgeType | ''>(() => isKnowledgeType(route.query.type) ? route.query.type : '')
const archived = computed(() => route.query.archived === '1')
// ?status=proposed shows only the entries waiting for a person (the footer's "to review").
const proposedOnly = computed(() => route.query.status === 'proposed' && !archived.value)
const expanded = ref(new Set<string>())
const draft = ref(q.value)
let timer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | null = null
watch(q, value => { if (value !== draft.value.trim()) draft.value = value })
watch(draft, value => { clearTimeout(timer); timer = setTimeout(() => { if (value.trim() !== q.value) setQuery({ q: value.trim() }) }, 180) })
function setQuery(patch: Record<string, string>) {
  const next: Record<string, string> = {}
  for (const [key, value] of Object.entries({ q: q.value, type: type.value, archived: archived.value ? '1' : '', status: proposedOnly.value ? 'proposed' : '', ...patch })) if (value) next[key] = value
  void router.replace({ path: '/knowledge', query: next })
}

async function load() {
  controller?.abort()
  const current = controller = new AbortController()
  const query = q.value
  loading.value = true
  try {
    const page = await listKnowledge({ q: query, limit: 1000 }, current.signal)
    if (current.signal.aborted) return
    items.value = page.items
    searchedFor.value = query
    error.value = ''
    loaded.value = true
    cursorId.value = null
  } catch (e) {
    if (!current.signal.aborted) error.value = e instanceof Error ? e.message : 'Knowledge could not be loaded.'
  } finally {
    if (!current.signal.aborted) loading.value = false
  }
}
watch(q, () => { expanded.value = new Set(); void load() }, { immediate: true })
const stale = computed(() => loaded.value && (loading.value || !!error.value || searchedFor.value !== q.value))

const statuses = computed<KnowledgeStatus[]>(() => archived.value ? [] : proposedOnly.value ? ['proposed'] : ['active', 'proposed'])
const counts = computed(() => countBy(filterItems(items.value, [], statuses.value)).type)
const total = computed(() => filterItems(items.value, [], statuses.value).length)
const visible = computed(() => filterItems(items.value, type.value ? [type.value] : [], statuses.value))
interface ProjectGroup { id: string; routeKey: string; title: string; items: KnowledgeItem[]; shown: KnowledgeItem[] }
const groups = computed<ProjectGroup[]>(() => {
  const map = new Map<string, ProjectGroup>()
  for (const item of visible.value) {
    const id = item.project?.id ?? 'none'
    if (!map.has(id)) {
      const project = item.project ? projects.byId(item.project.id) : undefined
      map.set(id, { id, routeKey: project?.routeKey ?? item.project?.key ?? '', title: project?.title ?? item.project?.title ?? 'No project', items: [], shown: [] })
    }
    map.get(id)!.items.push(item)
  }
  const out = [...map.values()]
  for (const group of out) {
    group.items = searchedFor.value ? group.items : sortItems(group.items, 'updated')
    group.shown = expanded.value.has(group.id) || searchedFor.value ? group.items : group.items.slice(0, PER_PROJECT)
  }
  // With a search, the project with the best match leads; otherwise the most recently written.
  return searchedFor.value ? out : out.sort((a, b) => Date.parse(b.items[0].updated_at) - Date.parse(a.items[0].updated_at))
})
const sequence = computed(() => groups.value.flatMap(group => group.shown))
// The footer says what the list shows: entries, and the proposed ones to review (AEON-785).
useFooterSummary(() => {
  const act = { review: () => setQuery({ status: 'proposed' }) }
  if (!loaded.value && error.value) return null
  if (!loaded.value) return knowledgeFooter({ loaded: false, entries: 0, toReview: 0, updatedAt: null, now: now.value, act })
  const updated = visible.value.reduce((latest, item) => Math.max(latest, Date.parse(item.updated_at) || 0), 0)
  return knowledgeFooter({ loaded: true, entries: visible.value.length, toReview: visible.value.filter(item => item.status === 'proposed').length, updatedAt: updated || null, now: now.value, act })
})
const projectLink = (group: ProjectGroup) => ({ path: `/p/${encodeURIComponent(group.routeKey)}/knowledge`, query: type.value ? { type: type.value } : {} })
const itemLink = (group: ProjectGroup, item: KnowledgeItem) => entryPath(group.routeKey, item.type, item.slug)

function rowEl(id: string) { return listEl.value?.querySelector<HTMLElement>(`[data-id="${id}"]`) ?? null }
function move(step: number) {
  const rows = sequence.value
  if (!rows.length) return
  const index = rows.findIndex(item => item.id === cursorId.value)
  const next = index === -1 ? (step >= 0 ? 0 : rows.length - 1) : Math.max(0, Math.min(rows.length - 1, index + step))
  cursorId.value = rows[next].id
  void nextTick(() => { const el = rowEl(rows[next].id); el?.focus({ preventScroll: true }); el?.scrollIntoView({ block: 'nearest' }) })
}
function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}
function keydown(event: KeyboardEvent) {
  if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || document.querySelector('dialog[open], .floating') || typing(event.target)) return
  if (event.key === 'j' || event.key === 'ArrowDown') { event.preventDefault(); move(1) }
  else if (event.key === 'k' || event.key === 'ArrowUp') { event.preventDefault(); move(-1) }
  else if (event.key === '/') { event.preventDefault(); input.value?.focus(); input.value?.select() }
}
function searchKey(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); if (draft.value) { draft.value = ''; setQuery({ q: '' }) } else input.value?.blur() }
  else if (event.key === 'ArrowDown') { event.preventDefault(); input.value?.blur(); move(1) }
  else if (event.key === 'Enter' && sequence.value.length) {
    event.preventDefault()
    const first = groups.value[0]
    if (first) void router.push(itemLink(first, first.shown[0]))
  }
}
function expand(id: string) { const next = new Set(expanded.value); next.add(id); expanded.value = next }
let clock: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  setPageTitle('Knowledge')
  void projects.load()
  window.addEventListener('keydown', keydown)
  clock = setInterval(() => { now.value = Date.now() }, 60_000)
  if (!q.value) void nextTick(() => input.value?.focus({ preventScroll: true }))
})
onBeforeUnmount(() => { window.removeEventListener('keydown', keydown); clearTimeout(timer); controller?.abort(); clearInterval(clock) })
const listCommand = computed(() => `${brand.value.product.toLowerCase()} knowledge list --project KEY`)
</script>

<template>
  <section class="knowledge-page" aria-labelledby="knowledge-title">
    <header class="kp-head">
      <p class="eyebrow">Across projects</p>
      <h1 id="knowledge-title">Knowledge</h1>
      <p class="kp-lead">Runbooks, guidelines, memory, external systems and related projects. Agents read them by slug before they work; <code>{{ listCommand }}</code> lists a project’s.</p>
    </header>

    <div class="kp-controls">
      <label class="search-field kp-search">
        <AppIcon name="search" :size="16" />
        <input ref="input" v-model="draft" class="field" type="search" placeholder="Search titles, slugs and text in every project" aria-label="Search knowledge in every project" aria-keyshortcuts="/" autocomplete="off" spellcheck="false" @keydown="searchKey" />
        <span v-if="loading && draft" class="spinner" aria-hidden="true" />
        <kbd v-else-if="!draft" class="keycap slash" aria-hidden="true">/</kbd>
      </label>
      <div class="kp-kinds" role="group" aria-label="Kinds">
        <button type="button" class="kp-kind" :aria-pressed="!type" @click="setQuery({ type: '' })">All<span class="mono">{{ total }}</span></button>
        <button v-for="meta in TYPES" :key="meta.type" type="button" class="kp-kind" :aria-pressed="type === meta.type" :class="{ empty: !counts[meta.type] }" @click="setQuery({ type: type === meta.type ? '' : meta.type })">
          <AppIcon :name="meta.icon" :size="13" />{{ meta.plural }}<span class="mono">{{ counts[meta.type] ?? 0 }}</span>
        </button>
        <label class="switch kp-archived"><input type="checkbox" :checked="archived" @change="setQuery({ archived: ($event.target as HTMLInputElement).checked ? '1' : '' })" /><span>Include archived</span></label>
      </div>
    </div>

    <p v-if="proposedOnly" class="kp-review">
      <span>Showing entries to review</span>
      <button type="button" @click="setQuery({ status: '' })">All entries</button>
    </p>

    <div v-if="error" class="kp-state" role="alert">
      <h2>{{ loaded ? 'Search could not be refreshed' : 'Knowledge could not be loaded' }}</h2>
      <p>{{ error }}</p>
      <button type="button" class="btn" :disabled="loading" @click="load()"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <p v-if="stale" class="kp-retained" role="status">{{ searchedFor ? `Previous results for “${searchedFor}”` : 'Previous results across all knowledge' }}.{{ loading ? ' Loading the requested search…' : ' The requested search has not completed.' }}</p>
    <div v-if="loaded || !error" ref="listEl" class="kp-results" :class="{ stale }" aria-live="polite">
      <div v-if="!loaded && !error" class="kp-skeleton" role="status" aria-label="Loading knowledge">
        <div v-for="n in 3" :key="n" class="kp-group glass-card"><div class="kp-group-head"><span class="skeleton sk-badge" /><span class="skeleton sk-head" /></div><div v-for="m in 3" :key="m" class="sk-row"><span class="skeleton sk-title" :style="{ width: `${40 + (n * m * 11) % 36}%` }" /><span class="skeleton sk-line" /></div></div>
      </div>
      <div v-else-if="!items.length && !searchedFor" class="kp-state glass-card">
        <span class="state-icon"><AppIcon name="book" :size="20" /></span>
        <h2>No knowledge in any project yet</h2>
        <p>Open a project and its Knowledge tab to write the first runbook, guideline or memory.</p>
        <RouterLink class="btn" to="/"><AppIcon name="folder" :size="14" />Projects</RouterLink>
      </div>
      <div v-else-if="!visible.length" class="kp-state glass-card">
        <span class="state-icon"><AppIcon name="search" :size="18" /></span>
        <h2 v-if="searchedFor">Nothing matches “{{ searchedFor }}”{{ type ? ` in ${typeMeta(type).plural.toLowerCase()}` : '' }}</h2>
        <h2 v-else>No {{ typeMeta(type || 'runbook').plural.toLowerCase() }} yet</h2>
        <p>Search reads titles, slugs and the full text of every entry.{{ archived ? '' : ' Archived entries are left out.' }}</p>
        <div class="state-actions">
          <button v-if="!archived" type="button" class="btn" @click="setQuery({ archived: '1' })">Include archived</button>
          <button v-if="type" type="button" class="btn" @click="setQuery({ type: '' })">All kinds</button>
          <button v-if="searchedFor" type="button" class="btn" @click="draft = ''; setQuery({ q: '' })">Clear the search</button>
        </div>
      </div>
      <template v-else>
        <p class="kp-summary mono">{{ plural(visible.length, 'entry', 'entries') }} in {{ plural(groups.length, 'project') }}</p>
        <section v-for="group in groups" :key="group.id" class="kp-group glass-card" :aria-labelledby="`kp-${group.id}`">
          <header class="kp-group-head">
            <span v-if="group.routeKey" class="key-badge">{{ group.routeKey }}</span>
            <h2 :id="`kp-${group.id}`" v-clip-tip="group.title">{{ group.title }}</h2>
            <span class="kp-count mono">{{ group.items.length }}</span>
            <span class="spacer" />
            <RouterLink v-if="group.routeKey" class="btn sm ghost kp-open" :to="projectLink(group)" :aria-label="`Open the knowledge of ${group.title}`"><span class="kp-open-label">Open its Knowledge</span><AppIcon name="arrow" :size="13" /></RouterLink>
          </header>
          <ul class="kp-rows">
            <li v-for="item in group.shown" :key="item.id">
              <RouterLink class="kp-row" :class="{ cursor: cursorId === item.id }" :to="itemLink(group, item)" :data-id="item.id" @focus="cursorId = item.id">
                <span class="kp-kind-icon" :data-tip="typeMeta(item.type).label" :style="{ '--kind': `var(${kindToken(item.type)})` }"><AppIcon :name="typeMeta(item.type).icon" :size="14" /></span>
                <span class="kp-text">
                  <span v-clip-tip="`${item.title}\n${item.type}/${item.slug}`" class="kp-title"><template v-for="(part, i) in highlightWords(item.title, searchedFor)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                  <span v-if="item.excerpt" class="kp-excerpt"><template v-for="(part, i) in highlightWords(item.excerpt, searchedFor)" :key="i"><mark v-if="part.match">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></span>
                </span>
                <span class="kp-meta">
                  <span v-if="item.status !== 'active'" class="k-status" :class="item.status">{{ statusLabel(item.status) }}</span>
                  <span v-clip-tip="`${item.title}\n${item.type}/${item.slug}`" class="kp-slug mono"><span class="kp-type">{{ item.type }}/</span>{{ item.slug }}</span>
                  <time class="kp-time mono" :datetime="item.updated_at" :data-tip="`Updated ${absoluteTime(item.updated_at)}`">{{ relativeTime(item.updated_at, { now }) }}</time>
                </span>
              </RouterLink>
            </li>
          </ul>
          <button v-if="group.shown.length < group.items.length" type="button" class="kp-more" @click="expand(group.id)">Show {{ group.items.length - group.shown.length }} more in {{ group.routeKey || group.title }}</button>
        </section>
        <p class="kp-hint"><kbd class="keycap">/</kbd> search · <kbd class="keycap">j</kbd><kbd class="keycap">k</kbd> move · <kbd class="keycap"><AppIcon name="enter" /></kbd> open</p>
      </template>
    </div>
  </section>
</template>

<style scoped>
.knowledge-page { width: 100%; max-width: 1180px; margin: 0 auto; padding: 30px var(--gutter) 24px; }
.kp-head h1 { margin-top: 4px; }
.kp-lead { max-width: 760px; margin-top: 10px; font-size: 14px; line-height: 1.55; color: var(--ink-2); }
.kp-lead code { padding: 1px 5px; border-radius: 5px; background: var(--code-bg); font-size: 12px; color: var(--ink); white-space: nowrap; }
.kp-controls { display: grid; gap: 12px; margin: 22px 0 18px; }
.kp-review { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 32px; margin: -6px 0 14px; font-size: 13px; color: var(--ink-2); }
.kp-review button { min-height: 32px; padding: 0 2px; border: 0; background: transparent; color: var(--teal-ink); font: 600 13px/1 var(--font); cursor: pointer; }
.kp-review button:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.kp-retained { margin: 12px 0; font-size: 13px; color: var(--ink-2); }
.kp-search .field { height: 46px; padding-left: 40px; padding-right: 40px; border-radius: 14px; font-size: 15.5px; }
.kp-search > svg { left: 14px; }
.kp-search .field::-webkit-search-cancel-button { display: none; }
.slash { position: absolute; right: 12px; pointer-events: none; }
@media (hover: none) { .slash { display: none; } }
.spinner { position: absolute; right: 14px; width: 14px; height: 14px; border-radius: 50%; border: 1.8px solid var(--line-2); border-right-color: var(--teal); }
@media (prefers-reduced-motion: no-preference) { .spinner { animation: kp-spin .8s linear infinite; } @keyframes kp-spin { to { transform: rotate(360deg); } } }
.kp-kinds { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.kp-kind { display: inline-flex; align-items: center; gap: 7px; height: 32px; padding: 0 12px; border: 0; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font-size: 13px; font-weight: 600; }
.kp-kind .mono { font-size: 11px; font-weight: 500; color: var(--ink-3); }
@media (hover: hover) { .kp-kind:hover { background: var(--row-hover); color: var(--ink); } }
.kp-kind[aria-pressed="true"] { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.kp-kind[aria-pressed="true"] .mono { color: var(--teal-ink); }
.kp-kind.empty:not([aria-pressed="true"]) { color: var(--ink-3); }
.kp-kind:focus-visible { box-shadow: var(--focus-ring); }
.kp-archived { margin-left: auto; font-size: 12.5px; }
.kp-results { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; }
.kp-results.stale .kp-group { opacity: .62; }
.kp-summary { font-size: 12px; color: var(--ink-3); }
.kp-group { overflow: clip; }
.kp-group-head { display: flex; align-items: center; gap: 10px; min-height: 52px; padding: 10px 12px 10px 16px; border-bottom: 1px solid var(--line); }
.kp-group-head h2 { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 650 15px/1.3 var(--font); color: var(--ink); }
.kp-count { font-size: 11.5px; color: var(--ink-3); }
.spacer { flex: 1; }
.kp-open { gap: 6px; flex-shrink: 0; }
.kp-rows { margin: 0; padding: 4px 6px 6px; list-style: none; }
.kp-row { display: flex; align-items: center; gap: 14px; min-height: 58px; padding: 9px 10px; border-radius: 10px; color: var(--ink); scroll-margin: 80px 0 24px; }
li + li .kp-row { position: relative; }
li + li .kp-row::before { content: ''; position: absolute; top: 0; left: 50px; right: 10px; height: 1px; background: var(--line); }
@media (hover: hover) { .kp-row:hover { background: var(--row-hover); } .kp-row:hover::before, li:hover + li .kp-row::before { opacity: 0; } }
.kp-row.cursor { background: var(--row-selected); }
.kp-row.cursor::before, li:has(.kp-row.cursor) + li .kp-row::before { opacity: 0; }
.kp-row:focus-visible { box-shadow: var(--focus-ring); }
/* The kind's hue, as in the project's Knowledge and its graph; the tile stays neutral. */
.kp-kind-icon { display: grid; place-items: center; flex-shrink: 0; width: 28px; height: 28px; border-radius: 8px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--kind, var(--teal-ink)); }
.kp-text { display: grid; gap: 3px; flex: 1; min-width: 0; }
.kp-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; font-weight: 600; }
.kp-excerpt { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; color: var(--ink-2); }
.kp-meta { display: flex; align-items: center; gap: 12px; flex-shrink: 0; min-width: 0; }
.kp-slug { max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11.5px; color: var(--ink-2); font-variant-ligatures: none; }
.kp-type { color: var(--ink-3); }
.kp-time { width: 64px; text-align: right; font-size: 12px; color: var(--ink-2); }
.k-status { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; font: 600 10px/1 var(--mono); letter-spacing: .08em; text-transform: uppercase; font-variant-ligatures: none; }
.k-status.proposed { background: var(--gold-wash); box-shadow: inset 0 0 0 1px rgba(214, 155, 49, .45); color: var(--gold-ink); }
.k-status.archived { background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.kp-more { display: block; width: calc(100% - 12px); height: 36px; margin: 0 6px 6px; border: 0; border-radius: 9px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; }
.kp-more:hover { background: var(--row-hover); }
.kp-more:focus-visible { box-shadow: var(--focus-ring); }
.kp-state { display: grid; justify-items: center; gap: 8px; padding: 52px 24px 56px; text-align: center; }
.kp-state h2 { font-size: 18px; }
.kp-state > p { max-width: 460px; font-size: 13.5px; color: var(--ink-2); }
.kp-state .btn { margin-top: 8px; }
.state-icon { display: grid; place-items: center; width: 48px; height: 48px; margin-bottom: 6px; border-radius: 15px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line), 0 10px 26px -14px var(--teal); color: var(--teal-ink); }
.state-icon.danger { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--danger); }
.state-actions { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; }
.state-actions .btn { margin: 0; }
.kp-hint { display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 5px; padding: 4px 0; font-size: 12px; color: var(--ink-3); }
.kp-hint .keycap + .keycap { margin-left: 2px; }
.kp-skeleton { display: grid; gap: 16px; }
.sk-badge { width: 54px; height: 22px; border-radius: 6px; }
.sk-head { width: 160px; height: 12px; }
.sk-row { display: grid; gap: 8px; padding: 14px 18px; border-top: 1px solid var(--line); }
.sk-title { height: 12px; }
.sk-line { width: 70%; height: 9px; }
@media (max-width: 720px) {
  .knowledge-page { padding: 18px 12px 16px; }
  .kp-review, .kp-review button { min-height: 44px; }
  .kp-lead code { white-space: normal; overflow-wrap: anywhere; }
  .kp-kinds { flex-wrap: nowrap; overflow-x: auto; margin: 0 -12px; padding: 2px 12px 4px; scrollbar-width: none; }
  .kp-kinds::-webkit-scrollbar { display: none; }
  .kp-kind { flex-shrink: 0; height: 44px; }
  .kp-archived { flex-shrink: 0; margin-left: 6px; }
  .kp-row { flex-wrap: wrap; align-items: flex-start; gap: 6px 12px; padding: 11px 10px; }
  .kp-text { flex: 1 1 calc(100% - 42px); }
  .kp-group-head { align-items: flex-start; }
  .kp-group-head h2 { white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
  .kp-title, .kp-slug { overflow-wrap: anywhere; }
  .kp-title, .kp-excerpt { white-space: normal; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2; }
  .kp-meta { flex: 1 1 100%; padding-left: 42px; }
  .kp-slug { flex: 1; max-width: none; }
  .kp-time { width: auto; }
  .kp-hint { display: none; }
  li + li .kp-row::before { left: 10px; }
}
@media (max-width: 600px) {
  .kp-search .field { height: 48px; font-size: 16px; }
  .kp-open { width: 36px; padding: 0; }
  .kp-open-label { display: none; }
}
</style>
