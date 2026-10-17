<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One item of an opened goal (AEON-1457 decision 6): state icon · keys · outcome
// label (wraps) and its one live line · the agent badge in the goal's agent column ·
// ETA. Done items keep their keys and label; a handed-over item names what was
// handed over and, when the goal carries it, the detail.
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import LiveAgents from '../projects/LiveAgents.vue'
import GoalEta from './GoalEta.vue'
import GoalStateIcon from './GoalStateIcon.vue'
import { STATE_LABEL, ticketHref, type GoalOutcome } from '../../lib/goals'
import type { LiveAgent } from '../../lib/liveAgents'

const props = defineProps<{ item: GoalOutcome; goalId: string; agents: LiveAgent[]; project: { title: string; routeKey: string }; now: number; detail?: string }>()
const done = computed(() => props.item.state === 'done' || props.item.handed_over)
</script>

<template>
  <li class="item" :class="{ 'is-done': done, handed: item.handed_over }" tabindex="-1" data-nav>
    <template v-if="item.handed_over">
      <span class="i-state" data-tip="Handed over"><AppIcon name="person-check" :size="15" /><span class="sr-only">Handed over</span></span>
      <span class="i-keys" />
      <div class="i-main"><span class="i-label">Handed over: {{ item.label }}</span><span v-if="detail" class="i-detail">{{ detail }}</span></div>
    </template>
    <template v-else>
      <span class="i-state" :data-tip="STATE_LABEL[item.state]"><GoalStateIcon :state="item.state" /><span class="sr-only">{{ STATE_LABEL[item.state] }}</span></span>
      <span class="i-keys mono">
        <RouterLink v-for="key in item.keys.slice(0, 2)" :key="key" :to="ticketHref(key)">{{ key }}</RouterLink>
        <span v-if="item.keys.length > 2" class="more" :data-tip="item.keys.slice(2).join(', ')" tabindex="0">+{{ item.keys.length - 2 }} more</span>
      </span>
      <div class="i-main"><span class="i-label">{{ item.label }}</span><span v-if="item.note" class="i-live">{{ item.note }}</span></div>
      <div v-if="agents.length && !done" class="i-badge"><LiveAgents :agents="agents" :project="project" variant="row" /></div>
      <GoalEta class="i-eta" :goal-id="goalId" :outcome-id="item.id" :eta="item.eta" :passed="item.passed" :done="done" :now="now" />
    </template>
  </li>
</template>

<style scoped>
.item { position: relative; display: grid; grid-template-columns: subgrid; grid-column: 1 / -1; align-items: start; padding: 6px 0; }
.item::before { content: ''; position: absolute; inset: 1px -14px; border-radius: 10px; z-index: -1; pointer-events: none; }
@media (hover: hover) { .item:hover::before { background: var(--row-hover); } }
.item:focus-visible { outline: none; box-shadow: none; }
.item:focus-visible::before { box-shadow: var(--focus-ring); background: var(--row-hover); }
.i-state { grid-column: 1; justify-self: center; display: grid; place-items: center; height: 20px; }
.i-keys { grid-column: 2; display: grid; gap: 1px; justify-items: start; font-size: 11.5px; font-weight: 500; line-height: 20px; letter-spacing: .01em; }
.i-keys a { color: var(--ink-2); border-radius: 4px; text-decoration: none; }
.i-keys a:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; }
.i-keys .more { color: var(--ink-3); border-radius: 4px; }
.i-main { grid-column: 3; min-width: 0; display: grid; gap: 1px; }
.is-done > .i-main, .handed > .i-main { grid-column: 3 / -3; }
.i-badge { grid-column: 4; justify-self: start; align-self: center; min-width: 0; max-width: 100%; }
.i-badge :deep(.row-name) { max-width: 14ch; }
.item > .i-eta { grid-column: -3 / -2; justify-self: end; margin: 0 -4px; padding: 0 4px; line-height: 20px; }
.item > .i-eta.passed { line-height: 1.35; padding-top: 1px; padding-bottom: 1px; }
.i-label { font-size: 13.5px; font-weight: 500; line-height: 20px; color: var(--ink); text-wrap: pretty; overflow-wrap: anywhere; }
.is-done .i-label { color: var(--ink-2); font-weight: 400; }
.i-live { display: flex; align-items: center; gap: 6px; font-size: 12px; line-height: 17px; color: var(--ink-3); overflow-wrap: anywhere; }
.i-detail { font-size: 12px; line-height: 1.4; color: var(--ink-3); }
.handed .i-state { color: var(--ink-2); }
@media (max-width: 1399px) {
  .i-badge :deep(.row-name), .i-badge :deep(.chip-state) { display: none; }
  .i-badge :deep(.live-chip) { grid-template-columns: auto; padding-right: 6px; }
}
@media (max-width: 760px) {
  .item { grid-template-columns: 44px auto minmax(0, 1fr); padding: 8px 0; row-gap: 2px; }
  .item::before { inset: 1px -6px; }
  .i-state { grid-column: 1; grid-row: 1; height: 22px; }
  .i-keys { display: none; }
  .i-main { display: contents; }
  .i-label { grid-column: 2 / -1; grid-row: 1; font-size: 14px; line-height: 1.35; }
  .i-live { grid-column: 3; grid-row: 2; align-self: center; }
  .i-detail { grid-column: 2 / -1; grid-row: 2; }
  .item > .i-eta { grid-column: 2; grid-row: 2; justify-self: start; align-self: center; margin: 0 8px 0 -4px; padding: 0 4px; line-height: 1.4; }
  .item > .i-eta.none { display: none; }
  .i-badge { grid-column: 2 / -1; grid-row: 3; justify-self: start; margin-top: 4px; }
}
/* Touch: the agent chips keep their look and get a 44 px hit area. */
@media (max-width: 760px), (pointer: coarse) {
  .i-badge :deep(.live-chip)::before { inset: -8px auto; left: 50%; width: max(calc(100% + 4px), 44px); transform: translateX(-50%); }
}
</style>
