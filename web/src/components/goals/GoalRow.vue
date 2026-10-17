<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One goal (AEON-1457 decisions 5 and 6): its row — project keys, kind, name and
// "Next", the one reserved agent-badge column, Moving · Queued · Done, progress, the
// waits-on-you marker and LANDS — and, opened, its panel on a full tint: what's
// missing as an ordered list, then its items (Moving, Queued with the first five
// shown, Paused; Done folded into one line) and the "Handed over" line last.
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import LiveAgents from '../projects/LiveAgents.vue'
import GoalEta from './GoalEta.vue'
import GoalProgress from './GoalProgress.vue'
import GoalStateIcon from './GoalStateIcon.vue'
import type { GoalsPage } from './goalsPage'
import { vClipTip } from '../../lib/clipTip'
import { itemGroups, itemMatches, QUEUE_SHOWN, STATE_LABEL, ticketHref, type Goal, type GoalOutcome } from '../../lib/goals'
import { averageRate, projectionText } from '../../lib/goalRate'
import type { LiveAgent } from '../../lib/liveAgents'

const props = defineProps<{ goal: Goal; page: GoalsPage; now: number; agents: ReadonlyMap<string, LiveAgent>; term: string; menuOpen: boolean }>()
const emit = defineEmits<{ menu: [anchor: HTMLElement] }>()

const open = computed(() => props.page.isOpen(props.goal))
const states = computed(() => props.goal.states ?? { moving: 0, queued: 0, paused: 0, done: 0 })
const done = computed(() => !!props.goal.done)
const keys = computed(() => props.goal.projects.slice(0, 2))
const filtering = computed(() => props.page.filter.value !== 'all' || !!props.term.trim())
const agentsOf = (ids: readonly string[]) => ids.map(id => props.agents.get(id)).filter((agent): agent is LiveAgent => !!agent)
const goalAgents = computed(() => agentsOf([...new Set((props.goal.items ?? []).flatMap(item => item.session_ids))]))
const project = computed(() => ({ title: props.goal.title, routeKey: props.goal.projects[0]?.key ?? '' }))

// ---------- The opened panel ----------
const filter = computed(() => props.page.filter.value)
const shownItems = computed(() => {
  const all = props.goal.items ?? []
  const byState = filter.value === 'all' ? all : all.filter(item => item.state === filter.value)
  const q = props.term.trim()
  return !q || props.page.searchOpens(props.goal) === false ? byState : byState.filter(item => itemMatches(item, q))
})
const groups = computed(() => itemGroups(shownItems.value))
const folds = computed(() => props.page.folds.get(props.goal.id) ?? {})
const queuedShown = computed(() => filtering.value || folds.value.queued ? groups.value.queued : groups.value.queued.slice(0, QUEUE_SHOWN))
const queuedMore = computed(() => filtering.value ? 0 : Math.max(0, groups.value.queued.length - QUEUE_SHOWN))
const doneShown = computed(() => filtering.value || folds.value.done ? groups.value.done : [])
const stepsDone = computed(() => props.goal.steps.filter(step => step.state === 'done').length)
const nextText = computed(() => props.goal.next_step?.text ?? '')
const handedDetail = (item: GoalOutcome) => props.goal.handed_over?.outcome_id === item.id ? props.goal.handed_over.detail : ''

// The ETA popover's projection from the same series as the chart (r5 amendment).
const entry = computed(() => props.page.seriesFor(props.goal))
const projection = computed(() => {
  const ready = entry.value?.state === 'ready' && entry.value.series?.range === props.page.range.value ? entry.value : null
  if (!ready) return null
  return projectionText(averageRate(ready.series?.points ?? [], props.now, ready.createdAt), props.page.range.value, props.now, done.value)
})

// A click anywhere on the row (not on its links and buttons) opens it.
function rowClick(event: MouseEvent) {
  const target = event.target as HTMLElement
  if (target.closest('a, button, [tabindex], .goal-progress, .live')) return
  props.page.toggle(props.goal.id)
}
const statLabel = (n: number, state: keyof typeof STATE_LABEL) => `${n} ${STATE_LABEL[state].toLowerCase()}`
const waitsLabel = computed(() => `${props.goal.waits.count} open Desk ${props.goal.waits.count === 1 ? 'question waits' : 'questions wait'} on you`)
const deskLink = computed(() => props.goal.waits.question_ids[0] ? { path: '/decision-desk', query: { item: `q:${props.goal.waits.question_ids[0]}` } } : '/decision-desk')
</script>

<template>
  <li class="goal" :data-goal-id="goal.id" :data-open="open ? 1 : 0">
    <div class="goal-row" @click="rowClick">
      <button type="button" class="goal-toggle" :aria-expanded="open" :aria-controls="`body-${goal.id}`" :aria-labelledby="`gn-${goal.id}`" data-nav @click="page.toggle(goal.id)"><AppIcon name="chevron-right" :size="14" /></button>
      <span class="goal-keys"><span v-for="badge in keys" :key="badge.id" class="key-badge" :data-tip="badge.name">{{ badge.key }}</span></span>
      <div class="goal-main">
        <div class="goal-title">
          <span class="kind" role="img" :aria-label="goal.kind === 'release' ? 'Release' : 'Goal'" :data-tip="goal.kind === 'release' ? 'Release' : 'Goal'"><AppIcon :name="goal.kind === 'release' ? 'rocket' : 'target'" :size="14" /></span>
          <span :id="`gn-${goal.id}`" v-clip-tip class="goal-name">{{ goal.title }}</span>
        </div>
        <span v-if="!done && nextText" class="goal-next"><span class="lbl">Next</span><span v-clip-tip class="txt">{{ nextText }}</span></span>
      </div>
      <div class="agents"><LiveAgents v-if="goalAgents.length" :agents="goalAgents" :project="project" variant="row" /></div>
      <span class="stat" :data-zero="states.moving ? 0 : 1" :aria-label="statLabel(states.moving, 'moving')">{{ states.moving }}</span>
      <span class="stat" :data-zero="states.queued ? 0 : 1" :aria-label="statLabel(states.queued, 'queued')">{{ states.queued }}</span>
      <span class="stat" :data-zero="states.done ? 0 : 1" :aria-label="statLabel(states.done, 'done')">{{ states.done }}</span>
      <GoalProgress :goal="goal" :open="page.charts.has(goal.id)" :range="page.range.value" :entry="entry" :now="now" @toggle="page.toggleChart(goal.id)" @range="page.setRange" />
      <RouterLink v-if="goal.waits.count" class="waits mono" :to="deskLink" :data-tip="waitsLabel" :aria-label="waitsLabel"><AppIcon name="person-wait" :size="14" /><span>{{ goal.waits.count }}</span></RouterLink>
      <span v-else />
      <GoalEta class="goal-eta" :goal-id="goal.id" :eta="goal.eta" :passed="goal.passed" :done="done" :now="now" :projection="projection" :items="goal.items" />
      <button type="button" class="icon-btn flat sm row-more" aria-haspopup="menu" :aria-expanded="menuOpen" :aria-label="`Actions for ${goal.title}`" @click="emit('menu', $event.currentTarget as HTMLElement)"><AppIcon name="more" :size="14" /></button>
    </div>
    <div :id="`body-${goal.id}`" class="goal-body" role="region" :aria-label="`${goal.title}, items`" :hidden="!open">
      <template v-if="open">
        <div v-if="!filtering && goal.steps.length" class="steps">
          <p class="steps-head">What’s missing<span class="sub">{{ stepsDone }} of {{ goal.steps.length }} steps done</span></p>
          <ol>
            <li v-for="step in goal.steps" :key="step.id" class="step" :class="step.state">
              <span class="s-ic">
                <svg class="st" viewBox="0 0 14 14" aria-hidden="true"><circle cx="7" cy="7" r="5.6" /><path v-if="step.state === 'done'" d="m4.6 7.1 1.7 1.7 3.2-3.5" /><circle v-else-if="step.state === 'now'" cx="7" cy="7" r="2.1" fill="currentColor" stroke="none" /></svg>
              </span>
              <span><span class="sr-only">{{ step.state === 'done' ? 'Done: ' : step.state === 'now' ? 'Now: ' : 'Later: ' }}</span>{{ step.text }}</span>
            </li>
          </ol>
          <p v-if="nextText" class="phone-next"><span class="lbl">Next · </span>{{ nextText }}</p>
        </div>
        <p v-if="goal.expansion !== 'full'" class="too-large">This goal has too many items to list here.</p>
        <ul class="items">
          <template v-for="item in [...groups.moving, ...queuedShown]" :key="item.id">
            <li class="item" tabindex="-1" data-nav>
              <span class="i-state" :data-tip="STATE_LABEL[item.state]"><GoalStateIcon :state="item.state" /><span class="sr-only">{{ STATE_LABEL[item.state] }}</span></span>
              <span class="i-keys mono">
                <RouterLink v-for="key in item.keys.slice(0, 2)" :key="key" :to="ticketHref(key)">{{ key }}</RouterLink>
                <span v-if="item.keys.length > 2" class="more" :data-tip="item.keys.slice(2).join(', ')" tabindex="0">+{{ item.keys.length - 2 }} more</span>
              </span>
              <div class="i-main"><span class="i-label">{{ item.label }}</span><span v-if="item.note" class="i-live">{{ item.note }}</span></div>
              <div v-if="agentsOf(item.session_ids).length" class="i-badge"><LiveAgents :agents="agentsOf(item.session_ids)" :project="project" variant="row" /></div>
              <GoalEta class="i-eta" :goal-id="goal.id" :outcome-id="item.id" :eta="item.eta" :passed="item.passed" :now="now" />
            </li>
          </template>
          <li v-if="queuedMore" class="fold-line">
            <button type="button" class="fold-btn" :aria-expanded="!!folds.queued" data-nav @click="page.toggleFold(goal.id, 'queued')">
              <AppIcon name="chevron-right" :size="12" /><b>{{ queuedMore }}</b> more queued<span class="act"><span :class="{ off: folds.queued }">· show</span><span :class="{ off: !folds.queued }">· hide</span></span>
            </button>
          </li>
          <li v-for="item in groups.paused" :key="item.id" class="item" tabindex="-1" data-nav>
            <span class="i-state" :data-tip="STATE_LABEL[item.state]"><GoalStateIcon :state="item.state" /><span class="sr-only">{{ STATE_LABEL[item.state] }}</span></span>
            <span class="i-keys mono"><RouterLink v-for="key in item.keys.slice(0, 2)" :key="key" :to="ticketHref(key)">{{ key }}</RouterLink><span v-if="item.keys.length > 2" class="more" :data-tip="item.keys.slice(2).join(', ')" tabindex="0">+{{ item.keys.length - 2 }} more</span></span>
            <div class="i-main"><span class="i-label">{{ item.label }}</span><span v-if="item.note" class="i-live">{{ item.note }}</span></div>
            <div v-if="agentsOf(item.session_ids).length" class="i-badge"><LiveAgents :agents="agentsOf(item.session_ids)" :project="project" variant="row" /></div>
            <GoalEta class="i-eta" :goal-id="goal.id" :outcome-id="item.id" :eta="item.eta" :passed="item.passed" :now="now" />
          </li>
          <li v-if="groups.done.length && !filtering" class="fold-line">
            <button type="button" class="fold-btn" :aria-expanded="!!folds.done" data-nav @click="page.toggleFold(goal.id, 'done')">
              <AppIcon name="chevron-right" :size="12" /><b>{{ groups.done.length }}</b> done<span class="act"><span :class="{ off: folds.done }">· show</span><span :class="{ off: !folds.done }">· hide</span></span>
            </button>
          </li>
          <li v-for="item in doneShown" :key="item.id" class="item is-done" tabindex="-1" data-nav>
            <span class="i-state" data-tip="Done"><GoalStateIcon state="done" /><span class="sr-only">Done</span></span>
            <span class="i-keys mono"><RouterLink v-for="key in item.keys.slice(0, 2)" :key="key" :to="ticketHref(key)">{{ key }}</RouterLink><span v-if="item.keys.length > 2" class="more" :data-tip="item.keys.slice(2).join(', ')" tabindex="0">+{{ item.keys.length - 2 }} more</span></span>
            <div class="i-main"><span class="i-label">{{ item.label }}</span><span v-if="item.note" class="i-live">{{ item.note }}</span></div>
            <GoalEta class="i-eta" :goal-id="goal.id" :outcome-id="item.id" :eta="item.eta" :done="true" :now="now" />
          </li>
          <li v-for="item in groups.handed" :key="item.id" class="item handed is-done" tabindex="-1" data-nav>
            <span class="i-state" data-tip="Handed over"><AppIcon name="person-check" :size="15" /></span>
            <span class="i-keys" />
            <div class="i-main"><span class="i-label">Handed over: {{ item.label }}</span><span v-if="handedDetail(item)" class="i-detail">{{ handedDetail(item) }}</span></div>
          </li>
        </ul>
      </template>
    </div>
  </li>
</template>

<style scoped>
.goal, .goal-row, .goal-body, .items, .item, .fold-line { display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; }
.items, .steps ol { margin: 0; padding: 0; list-style: none; }
.goal { position: relative; isolation: isolate; }
.goal + .goal::before { content: ''; position: absolute; left: -6px; right: -6px; top: -1px; height: 1px; background: var(--line); pointer-events: none; }
/* The opened goal sits on a full tint, never an edge bar. */
.goal[data-open="1"]::after { content: ''; position: absolute; inset: 3px -14px 4px -14px; border-radius: 12px; background: var(--surface-sunken); z-index: -2; pointer-events: none; }
.goal-row { position: relative; align-items: center; min-height: 64px; padding: 9px 0; scroll-margin-top: 96px; cursor: default; }
.goal-row::before { content: ''; position: absolute; inset: 3px -14px; border-radius: 12px; z-index: -1; pointer-events: none; }
@media (hover: hover) { .goal-row:hover::before { background: var(--row-hover); } }
.goal.arrived > .goal-row::before { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.goal-row:has(.goal-toggle:focus-visible)::before { box-shadow: var(--focus-ring); background: var(--row-hover); }
.goal-toggle { grid-column: 1; justify-self: center; display: grid; place-items: center; width: 24px; height: 28px; padding: 0; border: 0; border-radius: 7px; background: transparent; color: var(--ink-3); }
.goal-toggle:focus-visible { outline: none; box-shadow: none; }
.goal-toggle svg { transition: transform .15s ease; }
.goal[data-open="1"] .goal-toggle svg { transform: rotate(90deg); }
@media (prefers-reduced-motion: reduce) { .goal-toggle svg, .fold-btn svg { transition: none; } }
.goal-keys { display: grid; gap: 4px; justify-items: start; align-self: center; }
.goal-main { display: grid; gap: 3px; min-width: 0; }
.goal-title { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: 14px; line-height: 1.3; letter-spacing: -.005em; }
.kind { flex: none; display: inline-grid; place-items: center; color: var(--ink-3); }
.goal-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink); font-weight: 650; }
.goal-next { display: flex; align-items: baseline; gap: 6px; min-width: 0; font-size: 13px; line-height: 1.35; color: var(--ink-2); }
.goal-next .lbl { flex: none; color: var(--ink-3); }
.goal-next .txt { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* One agent-badge column, shared with the items; reserved when no one works. */
.agents { display: flex; align-items: center; justify-self: start; min-width: 0; max-width: 100%; }
.agents :deep(.row-name), .i-badge :deep(.row-name) { max-width: 14ch; }
.stat { justify-self: end; font: 500 13px/1 var(--mono); font-variant-numeric: tabular-nums; text-align: right; color: var(--ink); }
.stat[data-zero="1"] { color: var(--ink-3); }
.waits { display: inline-flex; align-items: center; gap: 5px; justify-self: start; height: 28px; padding: 0 8px; margin-left: -8px; border-radius: 8px; color: var(--ink-2); font-size: 12px; font-weight: 600; text-decoration: none; }
.waits svg { color: var(--gold-ink); }
.waits:hover { background: var(--row-hover); }
.waits:focus-visible, .fold-btn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.goal-eta { justify-self: end; }
.row-more { opacity: 0; justify-self: end; }
.goal-row:hover .row-more, .goal-row:focus-within .row-more, .row-more[aria-expanded="true"] { opacity: 1; }
@media (hover: none) { .row-more { opacity: 1; } }

/* The opened goal */
.goal-body { padding: 0 0 12px; }
.goal-body[hidden] { display: none; }
.steps { grid-column: 3 / -3; display: grid; margin: 2px 0 10px; padding: 0; }
.steps-head { display: flex; align-items: baseline; gap: 8px; margin: 0 0 4px; font-size: 12.5px; font-weight: 650; color: var(--ink-2); }
.steps-head .sub { font-weight: 500; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.steps ol { display: grid; }
.step { display: grid; grid-template-columns: 20px minmax(0, 1fr); align-items: center; column-gap: 8px; min-height: 26px; font-size: 13px; line-height: 1.35; }
.s-ic { display: grid; place-items: center; }
.st { flex: none; width: 14px; height: 14px; fill: none; stroke: currentColor; stroke-width: 1.6; stroke-linecap: round; stroke-linejoin: round; }
.step.done { color: var(--ink-2); }
.step.done .s-ic { color: var(--ok); }
.step.now { color: var(--ink); font-weight: 600; }
.step.now .s-ic { color: var(--primary); }
.step.later { color: var(--ink-3); }
.step.later .s-ic { color: var(--ink-3); opacity: .7; }
.phone-next { display: none; }
.too-large { grid-column: 3 / -3; margin: 0 0 8px; font-size: 12.5px; color: var(--ink-3); }

/* Items: state, keys, outcome and its one live line, the badge in the agent column, ETA */
.item { position: relative; align-items: start; padding: 6px 0; }
.item::before { content: ''; position: absolute; inset: 1px -14px; border-radius: 10px; z-index: -1; pointer-events: none; }
@media (hover: hover) { .item:hover::before { background: var(--row-hover); } }
.item:focus-visible { outline: none; box-shadow: none; }
.item:focus-visible::before { box-shadow: var(--focus-ring); background: var(--row-hover); }
.item > .i-state { grid-column: 1; justify-self: center; display: grid; place-items: center; height: 20px; }
.item > .i-keys { grid-column: 2; display: grid; gap: 1px; justify-items: start; font-size: 11.5px; font-weight: 500; line-height: 20px; letter-spacing: .01em; }
.i-keys a { color: var(--ink-2); border-radius: 4px; text-decoration: none; }
.i-keys a:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; }
.i-keys .more { color: var(--ink-3); border-radius: 4px; }
.item > .i-main { grid-column: 3; min-width: 0; display: grid; gap: 1px; }
.item.is-done > .i-main, .item.handed > .i-main { grid-column: 3 / -3; }
.item > .i-badge { grid-column: 4; justify-self: start; align-self: center; min-width: 0; max-width: 100%; }
.item > .i-eta { grid-column: -3 / -2; justify-self: end; margin: 0 -4px; padding: 0 4px; line-height: 20px; }
.item > .i-eta.passed { line-height: 1.35; padding-top: 1px; padding-bottom: 1px; }
.i-label { font-size: 13.5px; font-weight: 500; line-height: 20px; color: var(--ink); text-wrap: pretty; overflow-wrap: anywhere; }
.item.is-done .i-label { color: var(--ink-2); font-weight: 400; }
.i-live { display: flex; align-items: center; gap: 6px; font-size: 12px; line-height: 17px; color: var(--ink-3); overflow-wrap: anywhere; }
.i-detail { font-size: 12px; line-height: 1.4; color: var(--ink-3); }
.handed .i-state { color: var(--ink-2); }
.fold-line { padding: 0; }
.fold-btn { grid-column: 3 / -3; justify-self: start; display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 9px 0 6px; margin-left: -6px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-3); font-size: 12.5px; }
.fold-btn b { font-weight: 600; color: var(--ink-2); font-variant-numeric: tabular-nums; }
/* "show" and "hide" share one box, so the button never changes width. */
.fold-btn .act { display: inline-grid; color: var(--teal-ink); font-weight: 600; }
.fold-btn .act > span { grid-area: 1 / 1; }
.fold-btn .act > .off { visibility: hidden; }
.fold-btn:hover { background: var(--row-hover); color: var(--ink-2); }
.fold-btn svg { transition: transform .15s ease; }
.fold-btn[aria-expanded="true"] svg { transform: rotate(90deg); }

/* Tablet and small laptops: the badge keeps only its faces; below 1200 the counts step aside. */
@media (max-width: 1399px) {
  .agents :deep(.row-name), .agents :deep(.chip-state), .i-badge :deep(.row-name), .i-badge :deep(.chip-state) { display: none; }
  .agents :deep(.live-chip), .i-badge :deep(.live-chip) { grid-template-columns: auto; padding-right: 6px; }
}
@media (max-width: 1199px) { .goal-row > .stat { display: none; } }

/* Phones: name, ETA and progress; the rest stays in the opened goal. */
@media (max-width: 760px) {
  .goal-row > .goal-keys, .goal-row > .stat, .goal-row > .agents, .goal-row > .waits, .goal-row > .row-more, .goal-next, .goal-title .kind { display: none; }
  .goal-row > span:empty { display: none; }
  .goal-row { grid-template-rows: auto auto; min-height: 68px; padding: 10px 0; row-gap: 6px; align-items: center; }
  .goal-toggle { grid-column: 1; grid-row: 1 / 3; width: 44px; height: 44px; align-self: center; }
  .goal-main { grid-column: 2; grid-row: 1; align-self: end; }
  .goal-title { font-size: 14.5px; }
  .goal-name { white-space: normal; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
  .goal-row > .goal-eta { grid-column: 3; grid-row: 1; align-self: start; margin: -4px -6px; padding: 4px 6px; }
  .goal-row > :deep(.goal-progress) { grid-column: 2 / 4; grid-row: 2; align-self: start; }
  .goal-row::before { inset: 3px -6px; }
  .goal[data-open="1"]::after { inset: 3px -6px 4px -6px; }
  .goal-body { padding: 0 0 8px; }
  .steps { grid-column: 2 / -1; margin: 0 0 4px; }
  .steps-head, .steps ol { display: none; }
  .phone-next { display: block; margin: 0; padding: 0 0 6px; font-size: 13.5px; line-height: 1.4; color: var(--ink-2); }
  .phone-next .lbl { color: var(--ink-3); }
  .item { grid-template-columns: 44px auto minmax(0, 1fr); padding: 8px 0; row-gap: 2px; }
  .item::before { inset: 1px -6px; }
  .item > .i-state { grid-column: 1; grid-row: 1; height: 22px; }
  .item > .i-keys { display: none; }
  .item > .i-main { display: contents; }
  .i-label { grid-column: 2 / -1; grid-row: 1; font-size: 14px; line-height: 1.35; }
  .i-live { grid-column: 3; grid-row: 2; align-self: center; }
  .i-detail { grid-column: 2 / -1; grid-row: 2; }
  .item > .i-eta { grid-column: 2; grid-row: 2; justify-self: start; align-self: center; margin: 0 8px 0 -4px; padding: 0 4px; line-height: 1.4; }
  .item > .i-eta.none { display: none; }
  .item > .i-badge { grid-column: 2 / -1; grid-row: 3; justify-self: start; margin-top: 4px; }
  .fold-btn { grid-column: 2 / -1; height: 44px; margin-left: -4px; font-size: 13px; }
}
/* Touch: the agent chips keep their look and get a 44 px hit area. */
@media (max-width: 760px), (pointer: coarse) {
  .agents :deep(.live-chip)::before, .i-badge :deep(.live-chip)::before { inset: -8px auto; left: 50%; width: max(calc(100% + 4px), 44px); transform: translateX(-50%); }
}
</style>
