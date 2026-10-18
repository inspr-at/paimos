<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One item of an opened goal (AEON-1457 decision 6): state icon · key · outcome
// label and its one live line · the agent badge in the goal's agent column · ETA.
// Done items keep their key and label; a handed-over item names what was handed
// over and, when the goal carries it, the detail. The live line is the agent's note
// (AEON-1494 decision 5): a speech icon, closed by its author and time
// ("· AEON-LEAD 18:52"); the whole note is a tip when clipped.
// AEON-1494 addendum (Markus's feedback, Tick's decisions A–C): the item nests under
// its goal, its icon at the step icons' x, then the key, then the label. Wide, the
// row is one line and the live line sits in the Moving → Progress span; otherwise
// the label runs from the key to the ETA (two lines at most) and the live line and
// the badge's faces share one meta line under it.
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import LiveAgents from '../projects/LiveAgents.vue'
import GoalEta from './GoalEta.vue'
import GoalStateIcon from './GoalStateIcon.vue'
import { vClipTip } from '../../lib/clipTip'
import { noteByline, STATE_LABEL, ticketHref, type GoalOutcome } from '../../lib/goals'
import type { LiveAgent } from '../../lib/liveAgents'

const props = defineProps<{ item: GoalOutcome; goalId: string; agents: LiveAgent[]; project: { title: string; routeKey: string }; now: number; detail?: string; wide?: boolean }>()
const done = computed(() => props.item.state === 'done' || props.item.handed_over)
const note = computed(() => props.item.note.trim())
const byline = computed(() => noteByline(props.item, props.now))
const noteTip = computed(() => `${note.value} ${byline.value}`.trim())
const badge = computed(() => props.agents.length > 0 && !done.value)
</script>

<template>
  <li class="item" :class="{ 'is-done': done, handed: item.handed_over, wide }" tabindex="-1" data-nav>
    <div v-if="item.handed_over" class="i-head">
      <span class="i-state" data-tip="Handed over"><AppIcon name="person-check" :size="15" /><span class="sr-only">Handed over</span></span>
      <span class="i-keys" />
      <div class="i-main"><span class="i-label">Handed over: {{ item.label }}</span><span v-if="detail" class="i-detail">{{ detail }}</span></div>
    </div>
    <template v-else>
      <div class="i-head">
        <span class="i-state" :data-tip="STATE_LABEL[item.state]"><GoalStateIcon :state="item.state" /><span class="sr-only">{{ STATE_LABEL[item.state] }}</span></span>
        <span class="i-keys mono">
          <RouterLink v-if="item.keys[0]" :to="ticketHref(item.keys[0])">{{ item.keys[0] }}</RouterLink>
          <span v-if="item.keys.length > 1" class="more" :data-tip="item.keys.slice(1).join(', ')" tabindex="0">+{{ item.keys.length - 1 }}</span>
        </span>
        <div class="i-main">
          <span v-clip-tip class="i-label">{{ item.label }}</span>
          <div v-if="!wide && (note || badge)" class="i-meta">
            <span v-if="note" class="i-live is-note">
              <AppIcon name="note" :size="12" /><span v-clip-tip="noteTip" class="t"><span v-clip-tip="noteTip" class="tx">{{ note }}</span> <span v-if="byline" class="by">{{ byline }}</span></span>
            </span>
            <div v-if="badge" class="i-badge"><LiveAgents :agents="agents" :project="project" variant="row" /></div>
          </div>
        </div>
      </div>
      <span v-if="wide && note" class="i-live is-note">
        <AppIcon name="note" :size="12" /><span class="t"><span v-clip-tip="noteTip" class="tx">{{ note }}</span> <span v-if="byline" class="by">{{ byline }}</span></span>
      </span>
      <div v-if="wide && badge" class="i-badge"><LiveAgents :agents="agents" :project="project" variant="row" /></div>
      <!-- Phones hide the key column. These links stay, so an item with no agent still opens its work. -->
      <nav v-if="item.keys.length" class="phone-tickets" :aria-label="`Tickets for ${item.label}`">
        <RouterLink v-for="key in item.keys" :key="key" :to="ticketHref(key)">{{ key }}</RouterLink>
      </nav>
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
/* A · Nested under the goal: the icon sits in the steps' 20 px icon slot, then the key, then the label.
   Keys share one minimum width, so labels start at one x; a "+N" for more tickets sits under the key
   (beside it on one-line rows). */
.i-head { grid-column: 3 / -3; grid-row: 1; display: grid; grid-template-columns: 20px max-content minmax(0, 1fr); column-gap: 8px; align-items: start; min-width: 0; }
.i-state { display: grid; place-items: center; height: 20px; }
.i-keys { display: flex; flex-direction: column; align-items: flex-start; column-gap: 6px; min-width: 9.2ch; font-size: 11.5px; font-weight: 500; line-height: 20px; letter-spacing: .01em; white-space: nowrap; }
.i-keys a { color: var(--ink-2); border-radius: 4px; text-decoration: none; }
.i-keys a:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; }
.i-keys .more { color: var(--ink-3); border-radius: 4px; }
.i-main { min-width: 0; display: grid; gap: 2px; }
.item > .i-eta { grid-column: -3 / -2; grid-row: 1; justify-self: end; margin: 0 -4px; padding: 0 4px; line-height: 20px; }
.item > .i-eta.passed { line-height: 1.35; padding-top: 1px; padding-bottom: 1px; }
.phone-tickets { display: none; }
/* C · The label runs from the key to the ETA and wraps to two lines at most; the rest is its tip. */
.i-label { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; font-size: 13.5px; font-weight: 500; line-height: 20px; color: var(--ink); text-wrap: pretty; overflow-wrap: anywhere; }
.is-done .i-label { color: var(--ink-2); font-weight: 400; }
.handed .i-label { display: block; -webkit-line-clamp: none; }
.i-detail { font-size: 12px; line-height: 1.4; color: var(--ink-3); }
.handed .i-state { color: var(--ink-2); }
/* The meta line under the label: the live line, then the badge's faces. */
.i-meta { display: flex; align-items: center; gap: 10px; min-width: 0; }
.i-meta > .i-live { flex: 0 1 auto; }
.i-meta > .i-badge { flex: none; }
.i-meta .i-badge :deep(.row-name), .i-meta .i-badge :deep(.chip-state) { display: none; }
.i-meta .i-badge :deep(.live-chip) { grid-template-columns: auto; min-height: 22px; padding-right: 6px; }
/* The live line is one line: the note ends in "…" (its tip has all of it); author and time stay readable. */
.i-live { display: flex; align-items: center; gap: 6px; min-width: 0; font-size: 12px; line-height: 17px; color: var(--ink-3); }
.i-live svg { flex: none; color: var(--ink-3); }
.i-live .t { display: flex; align-items: baseline; gap: .3em; min-width: 0; color: var(--ink-2); }
.i-live .tx { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.i-live .by { flex: none; color: var(--ink-3); white-space: nowrap; }
/* A flex cell: the chip is no inline box, so it adds no line height of its own. */
.i-badge { display: flex; align-items: center; min-width: 0; max-width: 100%; }
.i-badge :deep(.row-name) { max-width: 14ch; }
/* B · Wide: one line per item. The label keeps the goal column, the badge the agent column,
   and the live line takes the Moving → Progress span. */
.wide > .i-head { grid-column: 3; }
.wide .i-keys { flex-direction: row; align-items: baseline; }
.wide.handed > .i-head { grid-column: 3 / -3; }
.wide .i-label { display: block; -webkit-line-clamp: none; white-space: nowrap; text-overflow: ellipsis; }
.wide > .i-live { grid-column: 5 / 9; grid-row: 1; line-height: 20px; }
/* The 28 px chip overhangs the 20 px line into the row's padding, so a row with an agent stays one line high. */
.wide > .i-badge { grid-column: 4; grid-row: 1; justify-self: start; align-self: center; margin-block: -4px; }
@media (max-width: 1399px) {
  .i-badge :deep(.row-name), .i-badge :deep(.chip-state) { display: none; }
  .i-badge :deep(.live-chip) { grid-template-columns: auto; padding-right: 6px; }
}
@media (max-width: 760px) {
  .item { grid-template-columns: 44px auto minmax(0, 1fr); padding: 8px 0; row-gap: 2px; }
  .item::before { inset: 1px -6px; }
  .i-head, .i-main, .i-meta { display: contents; }
  .i-state { grid-column: 1; grid-row: 1; height: 22px; }
  .i-keys { display: none; }
  .i-label { display: block; -webkit-line-clamp: none; grid-column: 2 / -1; grid-row: 1; overflow: visible; font-size: 14px; line-height: 1.35; }
  .i-live { grid-column: 3; grid-row: 2; align-self: center; }
  /* Phones keep the note on up to two balanced lines, its author and time closing it. */
  .i-live.is-note { align-items: flex-start; }
  .i-live.is-note svg { margin-top: 2.5px; }
  .i-live .t { display: block; max-height: 34px; overflow: hidden; text-wrap: balance; overflow-wrap: anywhere; }
  .i-live .tx { display: inline; overflow: visible; white-space: normal; }
  .i-detail { grid-column: 2 / -1; grid-row: 2; }
  .item > .i-eta { grid-column: 2; grid-row: 2; justify-self: start; align-self: center; margin: 0 8px 0 -4px; padding: 0 4px; line-height: 1.4; }
  .item > .i-eta.none { display: none; }
  .i-badge { grid-column: 2 / -1; grid-row: 3; justify-self: start; margin-top: 4px; }
  .i-meta .i-badge :deep(.live-chip) { min-height: 28px; }
  .phone-tickets { display: flex; flex-wrap: wrap; align-items: center; grid-column: 2 / -1; grid-row: 4; gap: 0 2px; margin-top: 2px; }
  .phone-tickets a { display: inline-flex; align-items: center; min-height: 44px; min-width: 44px; padding: 0 8px; border-radius: 8px; color: var(--ink-2); font-size: 13px; font-weight: 500; text-decoration: none; }
  .phone-tickets a:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; }
  .phone-tickets a:focus-visible { outline: none; box-shadow: var(--focus-ring); }
}
/* Touch: the agent chips keep their look and get a 44 px hit area. */
@media (max-width: 760px), (pointer: coarse) {
  .i-badge :deep(.live-chip)::before { inset: -8px auto; left: 50%; width: max(calc(100% + 4px), 44px); transform: translateX(-50%); }
}
</style>
