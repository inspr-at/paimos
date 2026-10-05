<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppIcon, { type IconName } from '../components/AppIcon.vue'
import TicketLink from '../components/releases/TicketLink.vue'
import StatusIcon from '../components/work/StatusIcon.vue'
import { api } from '../lib/api'
import { onAccessChange } from '../lib/authz'
import { useIdentityScope } from '../lib/useIdentityScope'
import { useWorkspaceActivity } from '../lib/useWorkspaceActivity'
import { automaticTarget } from '../lib/statusAutopilot'
import { absoluteTime, statusMeta } from '../lib/work'
import type { ActivityView, WorkspaceActivityItem } from '../lib/workspaceActivity'

const route = useRoute(), router = useRouter()
const views: { value: ActivityView; label: string; icon: IconName }[] = [
  { value: 'everything', label: 'Everything', icon: 'history' }, { value: 'automatic', label: 'Automatic changes', icon: 'sparkle' },
  { value: 'people', label: 'People', icon: 'users' }, { value: 'agents', label: 'Agents', icon: 'agent' },
]
const rules = [ ['new', 'New to Triage list'], ['backlog', 'Backlog to Cancel suggested'], ['blocked', 'Blocked to Reminder'], ['progress', 'In progress to Open'], ['done', 'Done to Missed release'], ['publish', 'Done to Delivered'], ['accept', 'Delivered to Accepted'], ['work_parent', 'Work parent'] ]
const queryValue = (key: string) => typeof route.query[key] === 'string' ? String(route.query[key]) : ''
const view = computed<ActivityView>(() => views.find(v => v.value === queryValue('view'))?.value ?? 'everything')
const filters = computed(() => ({ view: view.value, rule: queryValue('rule'), project_id: queryValue('project_id'), q: queryValue('q') }))
const search = ref(queryValue('q'))
const { items, cursor, loading, loadingOlder, error, pending, rowErrors, load, loadOlder, undo } = useWorkspaceActivity(filters)
const projectScope = useIdentityScope()
const projectLane = projectScope.lane()
const projects = ref<{ id: string; key: string; title: string }[]>([])
const projectError = ref('')
function loadProjects() {
  projects.value = []; projectError.value = ''
  void projectLane.run(({ after, signal }) => after(api('/projects', { signal }), response => {
    if (!response.ok) throw new Error('Project filters could not be loaded.')
    return after(response.json() as Promise<{ items: typeof projects.value }>, page => { projects.value = page.items })
  }), { failed: () => { projectError.value = 'Project filters could not be loaded.' } })
}
watch(projectScope.owner, loadProjects, { immediate: true, flush: 'sync' })
const stopProjectAccess = onAccessChange(loadProjects)
function setFilter(key: string, value: string) {
  const query = { ...route.query }
  if (value) query[key] = value
  else delete query[key]
  void router.replace({ path: '/activity', query })
}
let timer: ReturnType<typeof setTimeout> | undefined
function searchChanged() {
  clearTimeout(timer)
  timer = setTimeout(() => setFilter('q', search.value.trim()), 250)
}
watch(() => route.query.q, () => { search.value = queryValue('q') })
watch(projectScope.owner, () => { clearTimeout(timer); search.value = ''; if (route.query.q || route.query.project_id || route.query.rule) void router.replace({ path: '/activity', query: { view: view.value } }) }, { flush: 'sync' })
const groups = computed(() => {
  const days: { date: string; label: string; items: WorkspaceActivityItem[] }[] = []
  for (const item of items.value) {
    const at = new Date(item.at), date = `${at.getFullYear()}-${at.getMonth()}-${at.getDate()}`
    if (days.at(-1)?.date !== date) days.push({ date, label: at.toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' }), items: [] })
    days.at(-1)!.items.push(item)
  }
  return days
})
const clock = (at: string) => new Date(at).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
const label = (state: string) => automaticTarget(state) || statusMeta(state).label
const summary = (item: WorkspaceActivityItem) => item.reason || `${item.actor || 'Workspace'} · ${item.type.replaceAll('_', ' ').replaceAll('.', ' · ')}`
const sentinel = ref<HTMLElement>()
let observer: IntersectionObserver | undefined
function observeOlder() {
  observer?.disconnect()
  if (sentinel.value) observer?.observe(sentinel.value)
}
watch([cursor, loading, loadingOlder, error], observeOlder, { flush: 'post' })
onMounted(() => {
  observer = new IntersectionObserver(entries => { if (entries.some(entry => entry.isIntersecting) && cursor.value && !error.value) void loadOlder() }, { rootMargin: '160px' })
  observeOlder()
})
onBeforeUnmount(() => { clearTimeout(timer); observer?.disconnect(); stopProjectAccess() })
</script>
<template>
  <section class="activity-page" aria-labelledby="workspace-activity-title">
    <div class="activity-heading"><h1 id="workspace-activity-title">Activity</h1><p>What changed in this workspace, newest first. Undo works while the ticket is unchanged since.</p></div>
    <nav class="activity-views" aria-label="Activity views">
      <button v-for="option in views" :key="option.value" type="button" class="view-tab" :aria-current="view === option.value ? 'page' : undefined" @click="setFilter('view', option.value)"><AppIcon :name="option.icon" :size="13" />{{ option.label }}</button>
    </nav>
    <div class="activity-filters" role="toolbar" aria-label="Activity filters">
      <label class="activity-search"><AppIcon name="search" :size="14" /><input v-model="search" type="search" placeholder="Search" aria-label="Search activity" maxlength="200" @input="searchChanged" @keydown.esc="($event.target as HTMLInputElement).blur()" /></label>
      <div class="facets">
        <label><span class="sr-only">Rule</span><select aria-label="Rule" :value="filters.rule" @change="setFilter('rule', ($event.target as HTMLSelectElement).value)"><option value="">Rule: all</option><option v-for="rule in rules" :key="rule[0]" :value="rule[0]">{{ rule[1] }}</option></select></label>
        <label><span class="sr-only">Project</span><select aria-label="Project" :value="filters.project_id" @change="setFilter('project_id', ($event.target as HTMLSelectElement).value)"><option value="">Project: all</option><option v-if="filters.project_id && !projects.some(p => p.id === filters.project_id)" :value="filters.project_id">Selected project</option><option v-for="project in projects" :key="project.id" :value="project.id">{{ project.key }} {{ project.title }}</option></select></label>
      </div>
      <button class="btn sm" type="button" :disabled="loading || loadingOlder || pending.size > 0" @click="load">Reload</button>
    </div>
    <div class="activity-content" :aria-busy="loading || loadingOlder">
      <p v-if="projectError" class="activity-note" role="alert">{{ projectError }}</p>
      <p v-if="error" class="activity-error" role="alert">{{ error }} <button class="btn sm" type="button" @click="cursor ? loadOlder() : load()">Retry</button></p>
      <p v-if="loading" class="activity-note" role="status">Loading Activity…</p>
      <p v-else-if="!error && !items.length" class="activity-note">No changes match this view.</p>
      <div v-if="items.length" class="history" role="table" aria-label="Workspace activity">
        <template v-for="group in groups" :key="group.date">
          <div class="history-day" role="row"><span role="cell">{{ group.label }}</span></div>
          <div v-for="item in group.items" :key="item.event_id" class="history-row" :class="{ undone: item.undone }" role="row" :data-event-id="item.event_id">
            <time :datetime="item.at" :title="absoluteTime(item.at)" role="cell">{{ clock(item.at) }}</time>
            <span class="history-key" role="cell"><TicketLink v-if="item.key" :ticket-key="item.key" /></span>
            <div class="history-title" role="cell"><span v-clip-tip="item.title" class="title-text"><TicketLink v-if="item.key" class="phone-key" :ticket-key="item.key" />{{ item.title }}</span><span v-clip-tip="summary(item)" class="reason">{{ summary(item) }}</span></div>
            <div class="history-move" role="cell"><template v-if="item.from && item.to"><span><StatusIcon :state="item.from" :size="12" />{{ label(item.from) }}</span><AppIcon name="arrow" :size="12" /><span class="sr-only"> to </span><span><AppIcon v-if="automaticTarget(item.to)" name="list" :size="12" /><StatusIcon v-else :state="item.to" :size="12" />{{ label(item.to) }}</span></template><span v-else>{{ item.automatic ? 'Automatic change' : item.actor }}</span></div>
            <div class="history-action" role="cell">
              <div class="undo-stack" :aria-busy="pending.has(item.event_id)">
                <button type="button" class="btn sm" :class="{ hidden: !item.undoable || item.undone }" :aria-hidden="!item.undoable || item.undone ? true : undefined" :tabindex="!item.undoable || item.undone ? -1 : undefined" :disabled="!item.undoable || item.undone || pending.has(item.event_id)" :aria-label="`Undo: put ${item.key} back to ${label(item.from)}`" @click="undo(item)"><AppIcon name="rollback" :size="13" />Undo</button>
                <span v-if="item.undone" class="undo-state" role="status">Undone</span><span v-else-if="!item.undoable" class="undo-state" :data-tip="rowErrors[item.event_id]" :tabindex="rowErrors[item.event_id] ? 0 : undefined" :aria-label="rowErrors[item.event_id]">{{ item.changed_since ? 'Changed since' : rowErrors[item.event_id] ? 'Undo failed' : item.requires_preview ? 'Review in ticket' : item.automatic ? 'Undo unavailable' : '' }}</span>
              </div>
            </div>
            <p v-if="item.requires_preview && !item.changed_since && !item.undone" class="preview-note">This parent status follows a child change. <TicketLink :ticket-key="item.key" /> opens the ticket to preview and confirm Undo.</p>
          </div>
        </template>
      </div>
      <div ref="sentinel" class="activity-more"><p>{{ items.length }} {{ items.length === 1 ? 'entry' : 'entries' }} loaded{{ cursor ? ' · older entries load as you scroll' : items.length ? ' · end of visible history' : '' }}</p><button v-if="cursor" class="btn sm" type="button" :disabled="loadingOlder || !!error" @click="loadOlder">Load older entries</button><span v-if="loadingOlder" role="status">Loading older entries…</span></div>
    </div>
  </section>
</template>
<style scoped>
.activity-page { width: 100%; max-width: 1560px; margin: 0 auto; padding: 16px clamp(12px, 2.5vw, 32px) 32px; }
.activity-heading { display: flex; align-items: center; gap: 16px; min-width: 0; }
h1 { font-size: 26px; font-weight: 600; letter-spacing: -.03em; }
.activity-heading p { font-size: 13.5px; color: var(--ink-2); }
.activity-views { display: flex; gap: 4px; overflow-x: auto; padding: 8px 2px; margin-top: 4px; border-bottom: 1px solid var(--line); }
.view-tab { display: inline-flex; align-items: center; flex: none; gap: 6px; min-height: 32px; padding: 0 12px; border: 0; border-radius: 999px; font: 500 13px/1.4 var(--font); white-space: nowrap; color: var(--ink-2); background: transparent; }
.view-tab:hover { background: var(--row-hover); }
.view-tab[aria-current] { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.activity-filters { display: flex; align-items: center; gap: 10px; padding: 8px 0; }
.activity-filters > button { margin-left: auto; }
.activity-search { display: flex; align-items: center; flex: 0 1 22rem; min-width: 0; position: relative; }
.activity-search > svg { position: absolute; left: 10px; color: var(--ink-3); pointer-events: none; }
.activity-search input { width: 100%; min-width: 0; min-height: 34px; padding: 6px 10px 6px 32px; border: 1px solid var(--line-2); border-radius: 8px; background: var(--field-bg); font: inherit; font-size: 13px; color: var(--ink); }
.facets { display: flex; gap: 8px; min-width: 0; }
.facets label { min-width: 0; }
.facets select { width: 100%; max-width: 20rem; min-height: 34px; padding: 6px 26px 6px 10px; border: 1px solid var(--line-2); border-radius: 8px; background: var(--field-bg); color: var(--ink); font: inherit; font-size: 13px; }
.facets label:first-child { width: 16rem; }.facets label:last-child { width: 16rem; }
.activity-content { min-width: 0; }
.history { display: grid; grid-template-columns: 7ch 15ch minmax(0, 1fr) minmax(10rem, 18rem) 10rem; border: 1px solid var(--line); border-radius: 12px; background: var(--surface-raised-2); overflow: hidden; }
.history-day { grid-column: 1 / -1; padding: 12px 18px 6px; font: 500 10.5px/1.4 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); border-bottom: 1px solid var(--line); }
.history-row { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: start; min-height: 48px; border-bottom: 1px solid var(--line); font-size: 13px; padding-block: 6px; }
.history-row:last-child { border-bottom: 0; }
.history-row > [role=cell] { min-width: 0; padding: 0 12px; }
.history-row time { padding-left: 18px; padding-top: 8px; color: var(--ink-2); font: 500 12px/1.5 var(--mono); }
.history-key { padding-top: 8px !important; }
.history-title { display: grid; gap: 2px; padding-top: 2px !important; }
.title-text, .reason { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.title-text { color: var(--ink); }.reason { font-size: 12px; color: var(--ink-3); }
.phone-key { display: none; }
.history-move { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 6px; padding-top: 8px !important; }
.history-move span { display: inline-flex; gap: 5px; align-items: center; overflow-wrap: anywhere; }
.undo-stack { display: grid; align-items: start; justify-items: start; min-height: 32px; }
.undo-stack > * { grid-area: 1 / 1; }
.undo-stack .btn { min-height: 32px; }.undo-stack .hidden { visibility: hidden; }
.undo-state { color: var(--ink-3); font-size: 12px; align-self: center; }
.undone .history-title, .undone .history-move, .undone time { opacity: .55; }
.preview-note { grid-column: 3 / -1; margin: 4px 12px 0; font-size: 12px; line-height: 1.5; color: var(--ink-3); }
.activity-note, .activity-error { padding: 16px 0; font-size: 13px; color: var(--ink-2); }.activity-error { color: var(--danger); }
.activity-more { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; padding-top: 12px; font-size: 12px; color: var(--ink-3); }
@media (max-width: 1100px) { .activity-heading { display: block; }.activity-heading p { margin-top: 4px; }.activity-search { flex: 1 1 10rem; }.facets label:first-child, .facets label:last-child { width: 12rem; }.history { grid-template-columns: 7ch 13ch minmax(0,1fr) minmax(8rem,13rem) 9rem; } }
@media (max-width: 720px) {
  .activity-page { padding-top: 10px; }.activity-heading h1 { font-size: 24px; }
  .activity-filters { flex-wrap: wrap; }.activity-search { flex: 1 1 60%; }.facets { order: 2; flex-basis: 100%; }.facets label { flex: 1; }.facets select { max-width: none; }
  .history { display: block; }.history-day { padding-inline: 12px; }
  .history-row { grid-template-columns: minmax(0,1fr) 7.5rem; grid-template-areas: 'time action' 'title action' 'move action'; gap: 2px 6px; padding: 8px 12px; }
  .history-row > [role=cell] { padding: 0 !important; }.history-row time { grid-area: time; }.history-key { display: none; }.phone-key { display: inline; margin-right: 6px; }
  .history-title { grid-area: title; }.title-text { white-space: normal; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }.reason { white-space: normal; overflow-wrap: anywhere; }
  .history-move { grid-area: move; }.history-action { grid-area: action; }.preview-note { grid-column: 1 / -1; margin-inline: 0; }
}
@media (max-width: 720px), (pointer: coarse) { .view-tab, .facets select, .activity-search input, .activity-page .btn, .undo-stack { min-height: 44px; } }
@media (prefers-reduced-motion: no-preference) { .view-tab, .history-row { transition: background-color .14s ease-out; } }
</style>
