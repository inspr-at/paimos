<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Projects › Goals (AEON-1457): what the work is heading toward. One grid holds
// Waiting on you, the column header and every goal, so keys, names, counts,
// progress and ETAs line up from top to bottom. Header labels are the sort
// controls (LANDS, by ETA, first). Goals open in place; the list never moves a
// control under the pointer. Content comes from the server only: without goals
// the view says so, it never shows examples. AEON-1494: the workspace status note
// leads the grid when an agent wrote one, and the card footer ends with the
// freshness stamp (Live, or Reconnecting) beside the quiet projects. Addendum
// (Tick's decisions B and C): an opened goal's items are one line with their live
// line in the Moving → Progress span when the counts show and the goal column gives
// their key and label at least 280 px; otherwise the label runs to the ETA.
// AEON-1395: Group › Epics shows the open epics in the same grid and anatomy; the
// GOAL/EPIC header label swaps inside one box, so a switch moves no header cell.
// AEON-1618 (Tick spec v1): people create goals (New goal sheet), link work with
// the ticket picker, unlink it from an item's ⋯, and edit or delete a goal from its
// ⋯; each write offers Undo in its toast. The implicit outcome holding the links is
// never named: linked work shows as the goal's own items.
// Tick spec v1.1 (part a): the goal's ⋯ in groups (change the goal, work, for me,
// state): Rename in place (F2), Link work… (L), Delete goal (Del) with Undo and no
// confirm. People who may not edit a goal see only the "for me" group, and its
// keys do nothing for them. On phones the ⋯ opens as the shared bottom sheet.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import RowMenu from '../business/RowMenu.vue'
import RelationPicker, { type PickMode } from '../work/RelationPicker.vue'
import GoalRow from './GoalRow.vue'
import GoalSheet from './GoalSheet.vue'
import GoalWaiting from './GoalWaiting.vue'
import GoalStatusNote from './GoalStatusNote.vue'
import GoalFreshness from './GoalFreshness.vue'
import type { GoalsPage } from './goalsPage'
import { useSession } from '../../stores/session'
import { useLiveAgents } from '../../stores/liveAgents'
import { onAction } from '../../lib/shortcuts'
import { toast } from '../../lib/toast'
import { plural } from '../../lib/work'
import { undoEvent } from '../../lib/api'
import { deleteGoal, linkGoalWork, STATE_FILTERS, unlinkGoalWork, updateGoal, type Goal, type GoalOutcome, type GoalSort } from '../../lib/goals'
import type { LiveAgent } from '../../lib/liveAgents'
import type { RowAction } from '../../lib/rowActions'
import type { RelatedTicket } from '../../lib/useTicket'
import { useProjects } from '../../stores/projects'

const props = defineProps<{ page: GoalsPage; term: string; now: number; hiddenGroups: boolean }>()
const emit = defineEmits<{ clear: [] }>()
const session = useSession()
const live = useLiveAgents()
const route = useRoute()
const root = ref<HTMLElement>()

const person = computed(() => session.identity?.principal.kind === 'person')
// A page without a group (older callers) shows goals.
const epics = computed(() => props.page.group?.value === 'epics')
// A partial page (older callers, server-render fixtures) has no replacing flag.
const replacing = computed(() => props.page.replacing?.value === true)
const noun = computed(() => epics.value ? 'epic' : 'goal')
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

// ---------- Item rows: wide or stacked ----------
// Decided when the goals arrive or the window resizes, never while someone works in the list.
// The goal column is the grid's third track; items give 28 px of it to their icon slot.
const grid = ref<HTMLElement>()
const wideItems = ref(false)
const WIDE_TRACKS = 11, ITEM_ICON_SLOT = 28, ITEM_TEXT_MIN = 280
function measureItems() {
  if (!grid.value) return
  const tracks = getComputedStyle(grid.value).gridTemplateColumns.split(' ').map(parseFloat)
  wideItems.value = tracks.length === WIDE_TRACKS && (tracks[2] ?? 0) - ITEM_ICON_SLOT >= ITEM_TEXT_MIN
}
let gridWidth = -1
const resized = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(([entry]) => {
  const width = entry?.contentRect.width ?? 0
  if (width !== gridWidth) { gridWidth = width; measureItems() }
})
watch(grid, (el, old) => { if (old) resized?.unobserve(old); if (el) resized?.observe(el) })
watch(() => loaded.value && goals.value.length > 0, has => { if (has) void nextTick(measureItems) }, { immediate: true })
// The key badges change width once the mono font arrives.
onMounted(() => { void document.fonts?.ready.then(measureItems) })

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
const headLabel = (key: GoalSort) => key === 'name' && epics.value ? 'Epic' : head(key).label
const allOpen = computed(() => props.page.allOpen.value)

// ---------- Row menu ----------
// A goal's ⋯, or a linked item's ⋯ (item set): Unlink from goal.
const menu = ref<{ goal: Goal; item?: GoalOutcome; anchor: HTMLElement } | null>(null)
// Older callers and fixtures pass a page without people's writes.
const canEdit = (goal: Goal) => props.page.canEdit?.(goal) === true
const menuItems = computed<RowAction[]>(() => {
  const goal = menu.value?.goal
  if (!goal) return []
  if (menu.value?.item) return [{ id: 'unlink', label: 'Unlink from goal', icon: 'close', group: 1 }]
  const open = props.page.isOpen(goal)
  const fold: RowAction = { id: 'fold', label: open ? 'Collapse' : 'Expand', icon: open ? 'collapse' : 'expand', group: 3 }
  const copy: RowAction = { id: 'copy', label: 'Copy link', icon: 'link', group: 3 }
  // Epics have no recorded progress over time to chart.
  if (goal.kind === 'epic') return [fold, copy]
  const forMe = [fold, { id: 'chart', label: props.page.charts.has(goal.id) ? 'Hide progress over time' : 'Show progress over time', icon: 'trending-up', group: 3, reason: goal.expansion !== 'full' ? 'This goal is too large to chart.' : undefined }, copy] satisfies RowAction[]
  if (!canEdit(goal)) return forMe
  return [
    { id: 'rename', label: 'Rename', icon: 'edit', group: 1, keys: 'F2' }, { id: 'edit', label: 'Edit goal', icon: 'sliders', group: 1 },
    { id: 'link', label: 'Link work…', icon: 'plus', group: 2, keys: 'L' },
    ...forMe,
    { id: 'delete', label: 'Delete goal', icon: 'trash', group: 4, danger: true, keys: 'Delete' },
  ]
})
function openMenu(goal: Goal, anchor: HTMLElement, item?: GoalOutcome) { menu.value = menu.value?.goal.id === goal.id && menu.value.item?.id === item?.id ? null : { goal, item, anchor } }
function closeMenu(restore: boolean) { const at = menu.value; menu.value = null; if (restore) at?.anchor.focus() }
async function menuAction(action: string) {
  const at = menu.value
  const goal = at?.goal
  menu.value = null
  if (!goal || !at) return
  if (action === 'unlink' && at.item) void unlink(goal, at.item)
  else if (action === 'rename') startRename(goal)
  else if (action === 'link') openPicker(goal, at.anchor)
  else if (action === 'edit') props.page.openSheet(goal)
  else if (action === 'delete') void remove(goal)
  else if (action === 'fold') props.page.toggle(goal.id)
  else if (action === 'chart') props.page.toggleChart(goal.id)
  else if (action === 'copy') {
    try { await navigator.clipboard.writeText(new URL(`/?view=goals${goal.kind === 'epic' ? '&group=epics' : ''}&goal=${encodeURIComponent(goal.key)}`, location.origin).href); toast(`Copied the link to ${goal.title}`) }
    catch { toast('The link could not be copied.', { tone: 'error' }) }
  }
}

// ---------- People's writes (AEON-1618) ----------
// Each write is bound to the goal it acted on; Undo writes the inverse for that goal.
// The toast drops its Undo when the person changes (toast owner epoch).
const projects = useProjects()
const failure = (cause: unknown, fallback: string) => toast(cause instanceof Error ? cause.message : fallback, { tone: 'error' })
const picker = ref<{ goal: Goal; anchor: HTMLElement } | null>(null)
function openPicker(goal: Goal, anchor: HTMLElement) { picker.value = { goal, anchor } }
function closePicker(restore: boolean) { const at = picker.value; picker.value = null; if (restore && at?.anchor.isConnected) at.anchor.focus() }
const pickMode = computed<PickMode | null>(() => {
  const goal = picker.value?.goal
  if (!goal) return null
  const linked = (goal.items ?? []).filter(item => item.linked).flatMap(item => item.keys)
  const home = goal.project_id ? projects.byId(goal.project_id)?.routeKey ?? goal.projects.find(badge => badge.id === goal.project_id)?.key ?? null : null
  return { label: 'Link work', linked, onlyProject: home, within: goal.project_id, submit: picked => link(goal, picked) }
})
async function link(goal: Goal, picked: RelatedTicket[]): Promise<string | null> {
  const id = goal.id
  try {
    const result = await linkGoalWork(id, picked.map(item => item.id))
    const added = picked.map(item => item.id).filter(item => result.node_ids.includes(item))
    void props.page.read()
    toast(`${added.length} linked`, { action: { label: 'Undo', run: () => { void unlinkGoalWork(id, added).then(() => props.page.read(), cause => failure(cause, 'The link could not be undone.')) } } })
    return null
  } catch (cause) {
    return cause instanceof Error ? cause.message : 'The work could not be linked.'
  }
}
async function unlink(goal: Goal, item: GoalOutcome) {
  const id = goal.id, node = item.id
  try {
    await unlinkGoalWork(id, [node])
    void props.page.read()
    toast(`${item.keys[0] ?? item.label} unlinked`, { action: { label: 'Undo', run: () => { void linkGoalWork(id, [node]).then(() => props.page.read(), cause => failure(cause, 'The link could not be restored.')) } } })
  } catch (cause) { failure(cause, 'The work could not be unlinked.') }
}
const toggleOf = (id: string) => root.value?.querySelector<HTMLElement>(`[data-goal-id="${CSS.escape(id)}"] .goal-toggle`) ?? null
// No confirm: the toast's Undo restores the goal. Focus that was on its row moves to the next goal.
async function remove(goal: Goal) {
  const list = goals.value, at = list.findIndex(item => item.id === goal.id)
  const next = list[at + 1] ?? list[at - 1] ?? null
  const focused = !!root.value?.querySelector(`[data-goal-id="${CSS.escape(goal.id)}"] .goal-row`)?.contains(document.activeElement)
  try {
    const result = await deleteGoal(goal.id)
    const event = result.event_id
    toast('Goal deleted', event ? { action: { label: 'Undo', run: () => { void undoEvent(event).then(() => props.page.read(), cause => failure(cause, 'The goal could not be restored.')) } } } : {})
    await props.page.read()
    if (focused && next) { await nextTick(); toggleOf(next.id)?.focus() }
  } catch (cause) { failure(cause, 'The goal could not be deleted.') }
}
// Rename in place: the row's name becomes a field (GoalRow); the write is bound to the goal it renamed.
const renameBusy = ref<string | null>(null)
function startRename(goal: Goal) { if (canEdit(goal)) props.page.renaming.value = goal.id }
async function rename(goal: Goal, title: string) {
  if (renameBusy.value) return
  const id = goal.id, before = goal.title
  renameBusy.value = id
  try {
    await updateGoal(id, { title })
    await props.page.read()
    if (props.page.renaming.value === id) { props.page.renaming.value = null; await nextTick(); toggleOf(id)?.focus() }
    toast('Goal renamed', { action: { label: 'Undo', run: () => { void updateGoal(id, { title: before }).then(() => props.page.read(), cause => failure(cause, 'The name could not be restored.')) } } })
  } catch (cause) {
    failure(cause, 'The goal could not be renamed.')
    // A rename that left the field (blur) ends with the failure; one still in the field can be tried again.
    if (props.page.renaming.value === id && !(document.activeElement as HTMLElement | null)?.classList.contains('name-field')) props.page.renaming.value = null
  } finally { if (renameBusy.value === id) renameBusy.value = null }
}
// The goal whose row has focus (its toggle or its ⋯), never one of its items.
function focusedGoal(): Goal | null {
  const active = document.activeElement as HTMLElement | null
  const row = active && root.value?.contains(active) ? active.closest('.goal-row') : null
  const id = row?.closest<HTMLElement>('[data-goal-id]')?.dataset.goalId
  const goal = id ? goals.value.find(item => item.id === id) ?? null : null
  return goal && canEdit(goal) ? goal : null
}
// After New goal: the row appears where the sort puts it, opens, and Link work has focus.
async function created(id: string) {
  props.page.closeSheet()
  toast('Goal created')
  await props.page.read()
  props.page.toggle(id, true)
  await nextTick()
  root.value?.querySelector<HTMLElement>(`[data-goal-id="${CSS.escape(id)}"] .link-work`)?.focus()
}
function saved() { props.page.closeSheet(); void props.page.read() }
const sheetPreferred = computed(() => {
  const only = props.page.writableProjects?.value ?? []
  return only.length === 1 ? only[0]!.id : null
})

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
  stopKeys.push(onAction('goals.rename', () => { const goal = focusedGoal(); if (!goal) return false; startRename(goal) }))
  stopKeys.push(onAction('goals.link', () => { const goal = focusedGoal(), anchor = goal && toggleOf(goal.id); if (!goal || !anchor) return false; openPicker(goal, anchor) }))
  stopKeys.push(onAction('goals.delete', () => { const goal = focusedGoal(); if (!goal) return false; void remove(goal) }))
})
onBeforeUnmount(() => { window.removeEventListener('keydown', searchKey, true); stopKeys.forEach(stop => stop()); resized?.disconnect() })

const quiet = computed(() => props.page.list.value?.quiet_projects ?? { count: 0, keys: [] })
const more = computed(() => props.page.list.value?.next_cursor)
const everyDone = computed(() => props.page.hideDone.value && props.page.counts.value.all > 0)
const emptyTitle = computed(() => props.term.trim() ? `No ${noun.value} matches “${props.term.trim()}”` : props.hiddenGroups ? `All ${noun.value}s are in hidden groups` : filtering.value ? 'Nothing here'
  : everyDone.value && !props.page.totalsPartial.value ? `Every ${noun.value} is done` : epics.value ? 'No open epics in these projects.' : 'No goals yet')
// E4 (Tick's spec): no open epics says only that, without examples.
const emptyText = computed(() => props.term.trim() ? 'Check the spelling, or search by project or ticket key.'
  : props.hiddenGroups ? 'Show a group from the chips above.'
    : filtering.value ? `No ${noun.value} has items in this state. Try another state.`
      : everyDone.value ? `Done ${noun.value}s are hidden. Show them with Hide done (H).`
        : epics.value ? '' : 'Goals appear here once agents or people set them up for a project or the workspace.')
const canClear = computed(() => filtering.value)
</script>

<template>
  <div ref="root" class="goals-view">
    <div v-if="page.error.value && !loaded" class="state" role="alert">
      <AppIcon name="alert" :size="20" />
      <h2>{{ epics ? 'Epics' : 'Goals' }} could not be loaded</h2>
      <p>{{ page.error.value }}</p>
      <button class="btn" type="button" @click="page.read()"><AppIcon name="refresh" :size="14" />Try again</button>
    </div>
    <template v-else>
      <p v-if="loaded && page.totalsPartial.value" class="partial-line" role="status">Counts omit oversized {{ noun }}s.</p>
      <div ref="grid" class="goal-grid" :data-items="wideItems ? 'wide' : 'stacked'">
        <GoalStatusNote v-if="loaded && page.statusNote.value" :note="page.statusNote.value" :now="now" />
        <GoalWaiting v-if="person" :goals="goals" />
        <template v-if="!loaded">
          <ul class="sk-list" role="status" :aria-label="`Loading ${noun}s`">
            <li v-for="n in 6" :key="n" class="sk-row"><span class="skeleton" /><span class="ln"><span class="skeleton" /><span class="skeleton" /></span><span class="skeleton" /><span class="skeleton" /></li>
          </ul>
        </template>
        <template v-else-if="goals.length || replacing">
          <div class="goal-head">
            <button type="button" class="head-expand" aria-keyshortcuts="e" @click="page.toggleAll()">
              <AppIcon :name="allOpen ? 'collapse-all' : 'expand-all'" :size="14" />
              <span class="lbl"><span :class="{ off: allOpen }">Expand all</span><span :class="{ off: !allOpen }">Collapse all</span></span>
            </button>
            <span />
            <button v-for="h in [head('project'), head('name')]" :key="h.key" type="button" class="head-sort" :data-head="h.key" :data-sort-dir="sortState(h.key)" :aria-label="sortLabel(h.key, headLabel(h.key))" @click="page.setSort(h.key)">
              <span v-if="h.key === 'name'" class="swap"><span :class="{ off: epics }">Goal</span><span :class="{ off: !epics }">Epic</span></span><span v-else>{{ h.label }}</span><AppIcon :name="page.sort.value === h.key && page.dir.value === 'desc' ? 'arrow-down' : 'arrow-up'" :size="11" class="sort-ind" />
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
          <ul v-if="replacing" class="sk-list" role="status" aria-busy="true" :aria-label="`Loading ${noun}s`">
            <li v-for="n in 6" :key="n" class="sk-row"><span class="skeleton" /><span class="ln"><span class="skeleton" /><span class="skeleton" /></span><span class="skeleton" /><span class="skeleton" /></li>
          </ul>
          <ul v-else class="goal-list" :aria-busy="page.reading.value">
            <GoalRow
              v-for="goal in goals" :key="goal.id" :class="{ arrived: arrived === goal.id }" :goal="goal" :page="page" :now="now" :agents="agents" :term="term"
              :menu-open="menu?.goal.id === goal.id && !menu.item" :item-menu="menu?.goal.id === goal.id ? menu.item?.id ?? null : null" :wide-items="wideItems" :editable="canEdit(goal)" :rename-busy="renameBusy === goal.id"
              @menu="anchor => openMenu(goal, anchor)" @item-menu="(item, anchor) => openMenu(goal, anchor, item)" @link="anchor => openPicker(goal, anchor)" @rename="title => rename(goal, title)"
            />
          </ul>
        </template>
      </div>
      <div v-if="loaded && !goals.length && !replacing" class="state">
        <AppIcon :name="hiddenGroups ? 'folder' : epics ? 'epic' : 'target'" :size="20" />
        <h2>{{ emptyTitle }}</h2>
        <p v-if="emptyText">{{ emptyText }}</p>
        <button v-if="canClear" class="btn" type="button" @click="emit('clear')">Clear filter</button>
      </div>
      <p v-if="loaded && more" class="more-line">
        <span>{{ page.list.value?.truncated ? `More ${noun}s than one page holds.` : `More ${noun}s follow.` }}</span>
        <button type="button" class="btn sm" :disabled="page.loadingMore.value" @click="page.loadMore()">{{ page.loadingMore.value ? 'Reading…' : `Show more ${noun}s` }}</button>
      </p>
      <p v-if="page.error.value && loaded" class="more-line warn" role="status"><AppIcon name="alert" :size="13" />{{ page.error.value }}<button type="button" class="btn sm ghost" @click="page.read()">Try again</button></p>
      <div v-if="loaded && (goals.length || replacing)" class="card-foot">
        <p v-if="quiet.count" class="quiet-line">
          <span>{{ plural(quiet.count, 'project') }} with nothing moving:</span>
          <RouterLink v-for="key in quiet.keys" :key="key" class="pk mono" :to="`/p/${encodeURIComponent(key)}`">{{ key }}</RouterLink>
        </p>
        <GoalFreshness :live="page.live.value" :eta-set-at="page.etaLastSetAt.value" :updated-at="page.updatedAt.value" :now="now" />
      </div>
    </template>
    <RowMenu v-if="menu" phone-sheet :anchor="menu.anchor" :items="menuItems" :label="menu.item ? `${menu.item.keys[0] ?? menu.item.label} in ${menu.goal.title}` : `${menu.goal.kind === 'epic' ? 'Epic' : 'Goal'} ${menu.goal.title}`" @select="menuAction" @close="closeMenu" />
    <RelationPicker
      v-if="picker && pickMode" :anchor="picker.anchor" :subject="picker.goal.title" :self-id="picker.goal.id" :project-key="pickMode.onlyProject ?? ''"
      :related="[]" :picking="pickMode" @close="closePicker"
    />
    <GoalSheet
      v-if="page.sheet?.value" :goal="page.sheet.value.goal" :projects="page.writableProjects.value" :workspace="page.workspaceWrite.value" :preferred="sheetPreferred"
      @close="page.closeSheet()" @created="created" @saved="saved"
    />
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
/* GOAL and EPIC share one box (AEON-1395): switching the group never changes the cell's width. */
.head-sort .swap { display: inline-grid; }
.head-sort .swap > span { grid-area: 1 / 1; }
.head-sort .swap > .off { visibility: hidden; }
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
/* The card footer: projects with nothing moving, then the freshness stamp. */
.card-foot { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 24px; padding: 14px 20px 16px; border-top: 1px solid var(--line); }
.quiet-line { flex: 1 1 auto; min-width: 0; display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; margin: 0; font-size: 12.5px; color: var(--ink-3); }
.quiet-line .pk { font-size: 11.5px; font-weight: 500; letter-spacing: .02em; color: var(--ink-2); padding: 4px 2px; border-radius: 5px; text-decoration: none; }
.quiet-line .pk:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; }
.partial-line { margin: 0; padding: 10px 20px 0; font-size: 12.5px; line-height: 1.4; color: var(--ink-3); }
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
  .partial-line { padding: 8px 14px 0; font-size: 13px; }
  .card-foot { display: block; padding: 10px 14px 12px; }
  .quiet-line { font-size: 13px; gap: 0 4px; }
  .quiet-line > span { flex: 1 0 100%; padding: 6px 0 0; }
  .quiet-line .pk { display: inline-flex; align-items: center; justify-content: center; min-height: 44px; min-width: 44px; padding: 0 8px; }
  .state { padding: 44px 20px; }
  .sk-row { grid-template-columns: 56px minmax(0, 1fr); }
  .sk-row > .skeleton:nth-child(n+3) { display: none; }
}
</style>
