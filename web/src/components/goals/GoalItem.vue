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
// row is one line: with nothing beside it the label runs on to the ETA column; with
// a live line or an agent it keeps the goal and agent columns (the badge at the agent
// column's end) and the live line takes Moving up to the ETA column (Tick, 10-18).
// Otherwise the label runs from the key to the ETA (two lines at most) and the live
// line and the badge's faces share one meta line under it.
// AEON-1395 (an epic's item, a direct child): its keys are its own and its children's
// ("+N" counts all of them), and with no agent note its live line is its latest
// unmerged pull request ("#762 · In queue").
// AEON-1618: a linked item (work a person linked) has its own ⋯ in the goal's last
// column for people who may edit the goal; it has no ETA history of its own.
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import AppIcon from '../AppIcon.vue'
import LiveAgents from '../projects/LiveAgents.vue'
import GoalEta from './GoalEta.vue'
import GoalStateIcon from './GoalStateIcon.vue'
import { vClipTip } from '../../lib/clipTip'
import { nbsp, noteByline, STATE_LABEL, ticketHref, type GoalOutcome } from '../../lib/goals'
import { stateLabel } from '../../lib/delivery'
import type { LiveAgent } from '../../lib/liveAgents'

const props = defineProps<{ item: GoalOutcome; goalId: string; agents: LiveAgent[]; project: { title: string; routeKey: string }; now: number; detail?: string; wide?: boolean; epic?: boolean; editable?: boolean; menuOpen?: boolean }>()
const emit = defineEmits<{ menu: [anchor: HTMLElement] }>()
const menu = computed(() => !!props.editable && !!props.item.linked)
const done = computed(() => props.item.state === 'done' || props.item.handed_over)
const note = computed(() => props.item.note.trim())
// The pull request line stands in for a note that is not there.
const pr = computed(() => {
  const delivery = props.item.delivery
  if (note.value || done.value || !delivery) return ''
  const state = stateLabel(delivery.state, 'en')
  return nbsp(delivery.pull_request ? `#${delivery.pull_request} · ${state}` : state)
})
const keyCount = computed(() => Math.max(props.item.keys_total ?? 0, props.item.keys.length))
const moreTip = computed(() => {
  const rest = props.item.keys.slice(1).join(', '), hidden = keyCount.value - props.item.keys.length
  return hidden > 0 ? `${rest} and ${hidden} more` : rest
})
const byline = computed(() => noteByline(props.item, props.now))
const noteTip = computed(() => `${note.value} ${byline.value}`.trim())
const badge = computed(() => props.agents.length > 0 && !done.value)
// Wide rows only: something sits beside the label, so it stops at the agent column.
const side = computed(() => !!props.wide && !props.item.handed_over && (!!note.value || !!pr.value || badge.value))
</script>

<template>
  <li class="item" :class="{ 'is-done': done, handed: item.handed_over, wide, 'has-side': side, 'has-menu': menu }" tabindex="-1" data-nav :data-linked="item.linked ? 1 : undefined">
    <div v-if="item.handed_over" class="i-head">
      <span class="i-state" data-tip="Handed over"><AppIcon name="person-check" :size="15" /><span class="sr-only">Handed over</span></span>
      <span class="i-keys" />
      <div class="i-main"><span class="i-label">Handed over: {{ item.label }}</span><span v-if="detail" class="i-detail">{{ detail }}</span></div>
    </div>
    <template v-else>
      <div class="i-head" :class="{ 'has-badge': wide && badge }">
        <span class="i-state" :data-tip="STATE_LABEL[item.state]"><GoalStateIcon :state="item.state" /><span class="sr-only">{{ STATE_LABEL[item.state] }}</span></span>
        <span class="i-keys mono">
          <RouterLink v-if="item.keys[0]" :to="ticketHref(item.keys[0])">{{ item.keys[0] }}</RouterLink>
          <span v-if="keyCount > 1" class="more" :data-tip="moreTip" tabindex="0">+{{ keyCount - 1 }}</span>
        </span>
        <div class="i-main">
          <span v-clip-tip class="i-label">{{ item.label }}</span>
          <div v-if="!wide && (note || pr || badge)" class="i-meta">
            <span v-if="note" class="i-live is-note">
              <AppIcon name="note" :size="12" /><span v-clip-tip="noteTip" class="t"><span v-clip-tip="noteTip" class="tx">{{ note }}</span> <span v-if="byline" class="by">{{ byline }}</span></span>
            </span>
            <span v-else-if="pr" class="i-live is-pr"><AppIcon name="git-pull-request" :size="12" /><span class="t"><span v-clip-tip class="tx">{{ pr }}</span></span></span>
            <div v-if="badge" class="i-badge"><LiveAgents :agents="agents" :project="project" variant="row" /></div>
          </div>
        </div>
        <div v-if="wide && badge" class="i-badge"><LiveAgents :agents="agents" :project="project" variant="row" /></div>
      </div>
      <span v-if="wide && note" class="i-live is-note">
        <AppIcon name="note" :size="12" /><span class="t"><span v-clip-tip="noteTip" class="tx">{{ note }}</span> <span v-if="byline" class="by">{{ byline }}</span></span>
      </span>
      <span v-else-if="wide && pr" class="i-live is-pr"><AppIcon name="git-pull-request" :size="12" /><span class="t"><span v-clip-tip class="tx">{{ pr }}</span></span></span>
      <!-- Phones hide the key column. These links stay, so an item with no agent still opens its work. -->
      <nav v-if="item.keys.length" class="phone-tickets" :aria-label="`Tickets for ${item.label}`">
        <RouterLink v-for="key in item.keys" :key="key" :to="ticketHref(key)">{{ key }}</RouterLink>
      </nav>
      <GoalEta class="i-eta" :goal-id="goalId" :outcome-id="item.id" :eta="item.eta" :passed="item.passed" :done="done" :now="now" :history="!epic && !item.linked" />
      <button
        v-if="menu" type="button" class="icon-btn flat sm i-more" aria-haspopup="menu" :aria-expanded="!!menuOpen" :aria-label="`Actions for ${item.keys[0] ?? item.label}`"
        @click="emit('menu', $event.currentTarget as HTMLElement)"
      ><AppIcon name="more" :size="14" /></button>
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
/* A linked item's ⋯ sits in the goal row's ⋯ column; like the goal's, it shows on hover or focus. */
.item > .i-more { grid-column: -2 / -1; grid-row: 1; justify-self: end; align-self: start; margin-top: -4px; opacity: 0; }
.item:hover > .i-more, .item:focus-within > .i-more, .i-more[aria-expanded="true"] { opacity: 1; }
@media (hover: none) { .item > .i-more { opacity: 1; } }
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
/* B · Wide: one line per item. With nothing beside it the label runs on to the ETA column.
   With a live line or an agent it keeps the goal and agent columns, the badge at the agent
   column's end (so the label gets 320 px from 1440 on), and the live line takes Moving up
   to the ETA column. */
.wide .i-keys { flex-direction: row; align-items: baseline; }
.wide.has-side > .i-head { grid-column: 3 / 5; }
.wide .i-head.has-badge { grid-template-columns: 20px max-content minmax(0, 1fr) minmax(0, max-content); }
.wide .i-label { display: block; -webkit-line-clamp: none; white-space: nowrap; text-overflow: ellipsis; }
.wide > .i-live { grid-column: 5 / -3; grid-row: 1; line-height: 20px; }
/* The 28 px chip overhangs the 20 px line into the row's padding, so a row with an agent stays one line high. */
.wide .i-head > .i-badge { justify-self: end; align-self: center; margin: -4px 0 -4px 6px; }
@media (min-width: 1400px) { .wide .i-head.has-badge { grid-template-columns: 20px max-content minmax(320px, 1fr) minmax(0, max-content); } }
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
  .item.has-menu { grid-template-columns: 44px auto minmax(0, 1fr) 44px; }
  .item.has-menu .i-label { grid-column: 2 / -2; }
  .item > .i-more { grid-column: 4; grid-row: 1 / 3; width: 44px; height: 44px; margin: -8px 0 0; }
  .phone-tickets a { display: inline-flex; align-items: center; min-height: 44px; min-width: 44px; padding: 0 8px; border-radius: 8px; color: var(--ink-2); font-size: 13px; font-weight: 500; text-decoration: none; }
  .phone-tickets a:hover { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 2px; }
  .phone-tickets a:focus-visible { outline: none; box-shadow: var(--focus-ring); }
}
/* Touch: the agent chips keep their look and get a 44 px hit area. */
@media (max-width: 760px), (pointer: coarse) {
  .i-badge :deep(.live-chip)::before { inset: -8px auto; left: 50%; width: max(calc(100% + 4px), 44px); transform: translateX(-50%); }
}
</style>
