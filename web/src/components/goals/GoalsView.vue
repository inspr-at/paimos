<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Projects › Goals (AEON-1457): what the work is heading toward. One grid holds
// Waiting on you, the column header and every goal, so keys, names, counts,
// progress and ETAs line up from top to bottom. Header labels are the sort
// controls (LANDS, by ETA, first). Goals open in place; the list never moves a
// control under the pointer. Content comes from the server only: without goals
// the view says so, it never shows examples.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import RowMenu from '../business/RowMenu.vue'
import GoalRow from './GoalRow.vue'
import GoalWaiting from './GoalWaiting.vue'
import type { GoalsPage } from './goalsPage'
import { useSession } from '../../stores/session'
import { useLiveAgents } from '../../stores/liveAgents'
import { onAction } from '../../lib/shortcuts'
import { toast } from '../../lib/toast'
import { plural } from '../../lib/work'
import { STATE_FILTERS, type Goal, type GoalSort } from '../../lib/goals'
import type { LiveAgent } from '../../lib/liveAgents'
import type { RowAction } from '../../lib/rowActions'

const props = defineProps<{ page: GoalsPage; term: string; now: number; hiddenGroups: boolean }>()
const emit = defineEmits<{ clear: []; search: [] }>()
const session = useSession()
const live = useLiveAgents()
const route = useRoute()
const root = ref<HTMLElement>()

const person = computed(() => session.identity?.principal.kind === 'person')
const goals = computed(() => props.page.goals.value)
const loaded = computed(() => props.page.loaded.value)
const filtering = computed(() => props.page.filter.value !== 'all' || !!props.term.trim())
// Every live session by its id, so goal and item rows find their agents.
const agents = computed(() => {
  const map = new Map<string, LiveAgent>()
  for (const list of live.byProject.values()) for (const agent of list) if (agent.session_id) map.set(agent.session_id, agent)
  return map
})
// Rows ask for their progress series as they appear (done goals say nothing).
watch([goals, () => props.page.range.value], ([list]) => { for (const goal of list) if (!goal.done) props.page.want(goal) }, { immediate: true })
watch(() => props.page.charts.size, () => { for (const goal of goals.value) if (props.page.charts.has(goal.id)) props.page.want(goal) })

// ---------- Header ----------
const HEAD: { key: GoalSort; label: string; end?: boolean; stat?: boolean }[] = [
  { key: 'project', label: 'Project' }, { key: 'name', label: 'Goal' },
  { key: 'moving', label: 'Moving', end: true, stat: true }, { key: 'queued', label: 'Queued', end: true, stat: true }, { key: 'done', label: 'Done', end: true, stat: true },
  { key: 'progress', label: 'Progress' }, { key: 'eta', label: 'Lands', end: true },
]
const head = (key: GoalSort) => HEAD.find(h => h.key === key)!
const sortState = (key: GoalSort) => props.page.sort.value === key ? props.page.dir.value : undefined
// The label is the control: its name says the column and, when it sorts the list, which way.
const sortLabel = (key: GoalSort, label: string) => `Sort by ${label.toLowerCase()}${sortState(key) ? `, now ${sortState(key) === 'asc' ? 'ascending' : 'descending'}` : ''}`
const allOpen = computed(() => props.page.allOpen.value)

// ---------- Row menu ----------
const menu = ref<{ goal: Goal; anchor: HTMLElement } | null>(null)
const menuItems = computed<RowAction[]>(() => {
  const goal = menu.value?.goal
  if (!goal) return []
  const open = props.page.isOpen(goal)
  return [
    { id: 'fold', label: open ? 'Collapse' : 'Expand', icon: open ? 'collapse' : 'expand', group: 1 },
    { id: 'chart', label: props.page.charts.has(goal.id) ? 'Hide progress over time' : 'Show progress over time', icon: 'trending-up', group: 1, reason: goal.expansion !== 'full' ? 'This goal is too large to chart.' : undefined },
    { id: 'copy', label: 'Copy link', icon: 'link', group: 2 },
  ]
})
function openMenu(goal: Goal, anchor: HTMLElement) { menu.value = menu.value?.goal.id === goal.id ? null : { goal, anchor } }
function closeMenu(restore: boolean) { const at = menu.value; menu.value = null; if (restore) at?.anchor.focus() }
async function menuAction(action: string) {
  const goal = menu.value?.goal
  menu.value = null
  if (!goal) return
  if (action === 'fold') props.page.toggle(goal.id)
  else if (action === 'chart') props.page.toggleChart(goal.id)
  else if (action === 'copy') {
    try { await navigator.clipboard.writeText(new URL(`/?view=goals&goal=${encodeURIComponent(goal.key)}`, location.origin).href); toast(`Copied the link to ${goal.title}`) }
    catch { toast('The link could not be copied.', { tone: 'error' }) }
  }
}

// ---------- Arriving from a link ----------
// ?goal=KEY opens that goal, brings it into view and marks it until the next press.
const arrived = ref<string | null>(null)
let arrival = typeof route.query.goal === 'string' ? route.query.goal : null
watch(goals, async list => {
  if (!arrival) return
  const goal = list.find(item => item.key === arrival || item.id === arrival)
  if (!goal) return
  arrival = null
  props.page.toggle(goal.id, true)
  arrived.value = goal.id
  await nextTick()
  root.value?.querySelector<HTMLElement>(`[data-goal-id="${CSS.escape(goal.id)}"] .goal-row`)?.scrollIntoView({ block: 'start' })
  const clear = () => { arrived.value = null; document.removeEventListener('pointerdown', clear, true); document.removeEventListener('keydown', clear, true) }
  document.addEventListener('pointerdown', clear, true); document.addEventListener('keydown', clear, true)
}, { immediate: true })

// ---------- Keyboard ----------
// Goal toggles, items and fold lines, in screen order.
function stops() { return [...(root.value?.querySelectorAll<HTMLElement>('.goal-list [data-nav]') ?? [])].filter(el => el.getClientRects().length > 0) }
function step(delta: number) {
  const list = stops()
  if (!list.length) return
  const at = list.findIndex(el => el === document.activeElement || el.contains(document.activeElement))
  const next = at === -1 ? (delta > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, at + delta))
  list[next]!.focus(); list[next]!.scrollIntoView({ block: 'nearest' })
}
// The search field hands Arrow down to the first goal.
function searchKey(event: KeyboardEvent) {
  if (event.key !== 'ArrowDown' || event.metaKey || event.ctrlKey || event.altKey || !(event.target as HTMLElement).matches?.('.project-search input')) return
  event.preventDefault(); event.stopImmediatePropagation(); step(1)
}
const stopKeys: (() => void)[] = []
onMounted(() => {
  window.addEventListener('keydown', searchKey, true)
  STATE_FILTERS.forEach((state, i) => stopKeys.push(onAction((['goals.state.all', 'goals.state.moving', 'goals.state.queued', 'goals.state.paused', 'goals.state.done'] as const)[i]!, () => { props.page.filter.value = state })))
  stopKeys.push(onAction('goals.hide_done', () => props.page.setHideDone(!props.page.hideDone.value)))
  stopKeys.push(onAction('goals.toggle_all', () => props.page.toggleAll()))
  stopKeys.push(onAction('goals.next', () => step(1)))
  stopKeys.push(onAction('goals.prev', () => step(-1)))
})
onBeforeUnmount(() => { window.removeEventListener('keydown', searchKey, true); stopKeys.forEach(stop => stop()) })

const quiet = computed(() => props.page.list.value?.quiet_projects ?? { count: 0, keys: [] })
const more = computed(() => props.page.list.value?.next_cursor)
const emptyTitle = computed(() => props.term.trim() ? `No goal matches “${props.term.trim()}”` : props.hiddenGroups ? 'All goals are in hidden groups' : filtering.value ? 'Nothing here' : props.page.hideDone.value && props.page.counts.value.all > 0 ? 'Every goal is done' : 'No goals yet')
const emptyText = computed(() => props.term.trim() ? 'Check the spelling, or search by project or ticket key.'
  : props.hiddenGroups ? 'Show a group from the chips above.'
    : filtering.value ? 'No goal has items in this state. Try another state.'
      : props.page.hideDone.value && props.page.counts.value.all > 0 ? 'Done goals are hidden. Show them with Hide done (H).'
        : 'Goals appear here once agents or people set them up for a project or the workspace.')
const canClear = computed(() => filtering.value)
</script>

<template>
  <div ref="root" class="goals-view">
    <div v-if="page.error.value && !loaded" class="state" role="alert">
      <AppIcon name="alert" :size="20" />
      <h2>Goals could not be loaded</h2>
      <p>{{ page.error.value }}</p>
      <button class="btn" type="button" @click="page.read()"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <template v-else>
      <div class="goal-grid">
        <GoalWaiting v-if="person" :goals="goals" />
        <template v-if="!loaded">
          <ul class="sk-list" role="status" aria-label="Loading goals">
            <li v-for="n in 6" :key="n" class="sk-row"><span class="skeleton" /><span class="ln"><span class="skeleton" /><span class="skeleton" /></span><span class="skeleton" /><span class="skeleton" /></li>
          </ul>
        </template>
        <template v-else-if="goals.length">
          <div class="goal-head">
            <button type="button" class="head-expand" aria-keyshortcuts="e" @click="page.toggleAll()">
              <AppIcon :name="allOpen ? 'collapse-all' : 'expand-all'" :size="14" />
              <span class="lbl"><span :class="{ off: allOpen }">Expand all</span><span :class="{ off: !allOpen }">Collapse all</span></span>
            </button>
            <span />
            <button v-for="h in [head('project'), head('name')]" :key="h.key" type="button" class="head-sort" :data-sort-dir="sortState(h.key)" :aria-label="sortLabel(h.key, h.label)" @click="page.setSort(h.key)">
              <span>{{ h.label }}</span><AppIcon :name="page.sort.value === h.key && page.dir.value === 'desc' ? 'arrow-down' : 'arrow-up'" :size="11" class="sort-ind" />
            </button>
            <span />
            <button v-for="h in [head('moving'), head('queued'), head('done')]" :key="h.key" type="button" class="head-sort end h-stat" :data-sort-dir="sortState(h.key)" :aria-label="sortLabel(h.key, h.label)" @click="page.setSort(h.key)">
              <AppIcon :name="page.sort.value === h.key && page.dir.value === 'asc' ? 'arrow-up' : 'arrow-down'" :size="11" class="sort-ind" /><span>{{ h.label }}</span>
            </button>
            <button type="button" class="head-sort" :data-sort-dir="sortState('progress')" :aria-label="sortLabel('progress', 'Progress')" @click="page.setSort('progress')">
              <span>Progress</span><AppIcon :name="page.sort.value === 'progress' && page.dir.value === 'asc' ? 'arrow-up' : 'arrow-down'" :size="11" class="sort-ind" />
            </button>
            <span />
            <button type="button" class="head-sort end" :data-sort-dir="sortState('eta')" :aria-label="sortLabel('eta', 'Lands')" @click="page.setSort('eta')">
              <AppIcon :name="page.sort.value === 'eta' && page.dir.value === 'desc' ? 'arrow-down' : 'arrow-up'" :size="11" class="sort-ind" /><span>Lands</span>
            </button>
            <span />
          </div>
          <ul class="goal-list" :aria-busy="page.reading.value">
            <GoalRow
              v-for="goal in goals" :key="goal.id" :class="{ arrived: arrived === goal.id }" :goal="goal" :page="page" :now="now" :agents="agents" :term="term"
              :menu-open="menu?.goal.id === goal.id" @menu="anchor => openMenu(goal, anchor)"
            />
          </ul>
        </template>
      </div>
      <div v-if="loaded && !goals.length" class="state">
        <AppIcon :name="hiddenGroups ? 'folder' : 'target'" :size="20" />
        <h2>{{ emptyTitle }}</h2>
        <p>{{ emptyText }}</p>
        <button v-if="canClear" class="btn" type="button" @click="emit('clear')">Clear filter</button>
      </div>
      <p v-if="loaded && more" class="more-line">
        <span>{{ page.list.value?.truncated ? 'More goals than one page holds.' : 'More goals follow.' }}</span>
        <button type="button" class="btn sm" :disabled="page.loadingMore.value" @click="page.loadMore()">{{ page.loadingMore.value ? 'Reading…' : 'Show more goals' }}</button>
      </p>
      <p v-if="page.error.value && loaded" class="more-line warn" role="status"><AppIcon name="alert" :size="13" />{{ page.error.value }}<button type="button" class="btn sm ghost" @click="page.read()">Try again</button></p>
      <p v-if="loaded && quiet.count && goals.length" class="quiet-line">
        <span>{{ plural(quiet.count, 'project') }} with nothing moving:</span>
        <RouterLink v-for="key in quiet.keys" :key="key" class="pk mono" :to="`/p/${encodeURIComponent(key)}`">{{ key }}</RouterLink>
      </p>
    </template>
    <RowMenu v-if="menu" :anchor="menu.anchor" :items="menuItems" :label="`Goal ${menu.goal.title}`" @select="menuAction" @close="closeMenu" />
  </div>
</template>

<style scoped>
/* One grid for Waiting on you, the header and every row (subgrid all the way down). */
.goal-grid { --goal-cols: 22px max-content minmax(0, 1fr) 244px 56px 56px 56px 160px 36px 132px 30px; display: grid; grid-template-columns: var(--goal-cols); column-gap: 14px; padding: 0 20px; }
.goal-head, .goal-list { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; }
.goal-list { margin: 0; padding: 6px 0; list-style: none; }
.goal-head { position: relative; align-items: center; height: 36px; }
.goal-head::after { content: ''; position: absolute; left: -20px; right: -20px; bottom: 0; height: 1px; background: var(--line); }
.head-sort { display: inline-flex; align-items: center; gap: 4px; justify-self: start; height: 26px; padding: 0 6px; margin-left: -6px; border: 0; border-radius: 6px; background: transparent; font: 500 10.5px/1 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; white-space: nowrap; }
.head-sort .sort-ind { flex: none; opacity: 0; }
.head-sort[data-sort-dir] { color: var(--ink-2); }
.head-sort[data-sort-dir] .sort-ind { opacity: 1; }
.head-sort.end { justify-self: end; margin-left: 0; margin-right: -6px; }
@media (hover: hover) { .head-sort:hover { background: var(--row-hover); color: var(--ink-2); } }
/* Expand all / Collapse all on phones: both labels share one box, so it never changes width. */
.head-expand { display: none; align-items: center; gap: 6px; height: 28px; padding: 0 10px 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.head-expand .lbl { display: inline-grid; }
.head-expand .lbl > span { grid-area: 1 / 1; }
.head-expand .lbl > .off { visibility: hidden; }
.head-expand:hover { background: var(--row-hover); color: var(--teal-ink); }
.head-sort:focus-visible, .head-expand:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.quiet-line { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; margin: 0; padding: 14px 20px 16px; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink-3); }
.quiet-line .pk { font-size: 11.5px; font-weight: 500; letter-spacing: .02em; color: var(--ink-2); padding: 4px 2px; border-radius: 5px; text-decoration: none; }
.quiet-line .pk:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; }
.more-line { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; margin: 0; padding: 12px 20px; border-top: 1px solid var(--line); font-size: 12.5px; color: var(--ink-3); }
.more-line.warn { color: var(--warn-ink); }
.state { display: grid; justify-items: center; gap: 8px; padding: 64px 24px; text-align: center; color: var(--ink-2); }
.state > svg { color: var(--teal); margin-bottom: 4px; }
.state h2 { color: var(--ink); font-size: 17px; }
.state p { font-size: 13.5px; }
.state .btn { margin-top: 10px; }
.sk-list { display: grid; grid-column: 1 / -1; margin: 0; padding: 8px 0 20px; list-style: none; }
.sk-row { display: grid; grid-template-columns: 62px minmax(0, 1fr) 190px 110px; align-items: center; gap: 18px; min-height: 60px; }
.sk-row .skeleton:first-child { height: 20px; border-radius: 6px; }
.sk-row .ln { display: grid; gap: 8px; }
.sk-row .ln .skeleton:first-child { width: 38%; }
.sk-row .ln .skeleton:last-child { width: 66%; height: 8px; opacity: .7; }
@media (max-width: 1399px) { .goal-grid { --goal-cols: 22px max-content minmax(0, 1fr) 84px 56px 56px 56px 160px 36px 132px 30px; } }
@media (max-width: 1199px) {
  .goal-grid { --goal-cols: 22px max-content minmax(0, 1fr) 84px 160px 36px 132px 30px; }
  .goal-head > .h-stat { display: none; }
}
@media (max-width: 900px) { .goal-grid { --goal-cols: 22px max-content minmax(0, 1fr) 84px 136px 34px 112px 30px; } }
@media (max-width: 760px) {
  .goal-grid { --goal-cols: 44px minmax(0, 1fr) auto; column-gap: 8px; padding: 0 8px; }
  .goal-head { display: flex; align-items: center; height: auto; min-height: 44px; padding: 0 2px; }
  .goal-head::after { display: none; }
  .goal-head > :not(.head-expand) { display: none; }
  .head-expand { display: inline-flex; min-height: 44px; padding: 0 14px 0 10px; font-size: 13px; }
  .goal-list { padding: 4px 0; }
  .quiet-line { padding: 10px 14px 12px; font-size: 13px; gap: 0 4px; }
  .quiet-line > span { flex: 1 0 100%; padding: 6px 0 0; }
  .quiet-line .pk { display: inline-flex; align-items: center; justify-content: center; min-height: 44px; min-width: 44px; padding: 0 8px; }
  .state { padding: 44px 20px; }
  .sk-row { grid-template-columns: 56px minmax(0, 1fr); }
  .sk-row > .skeleton:nth-child(n+3) { display: none; }
}
</style>
