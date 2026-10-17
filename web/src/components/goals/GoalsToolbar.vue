<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The Goals view's own controls in the Projects toolbar (AEON-1457 decision 2): the
// state chips All · Moving · Queued · Paused · Done (keys 1–5) counting items over
// every visible goal, so they add up and Hide done never changes them; Hide done (H);
// and Expand all / Collapse all (E), both labels reserved in one box. On phones the
// same controls live behind ⋯ (the `more` part).
import { computed, ref } from 'vue'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import GoalStateIcon from './GoalStateIcon.vue'
import type { GoalsPage } from './goalsPage'
import { STATE_FILTERS, STATE_LABEL, type StateFilter } from '../../lib/goals'

const props = defineProps<{ page: GoalsPage; part: 'chips' | 'more' }>()
const counts = computed(() => props.page.counts.value)
const allOpen = computed(() => props.page.allOpen.value)
const label = (state: StateFilter) => state === 'all' ? 'All' : STATE_LABEL[state]
const partial = computed(() => props.page.totalsPartial.value)
const tip = (state: StateFilter, i: number) => `${state === 'all' ? 'All items' : STATE_LABEL[state]} · ${i + 1}${partial.value ? ' · counts omit oversized goals' : ''}`
const countLabel = (state: StateFilter) => `${label(state)}, ${counts.value[state]}${partial.value ? ', incomplete' : ''}`
const moreAnchor = ref<HTMLElement | null>(null)
function closeMore(restore: boolean) { const anchor = moreAnchor.value; moreAnchor.value = null; if (restore) anchor?.focus() }
function pick(state: StateFilter) { props.page.filter.value = state; closeMore(true) }
</script>

<template>
  <template v-if="part === 'chips'">
    <span class="chips-sep" aria-hidden="true" />
    <div class="seg state-seg" role="radiogroup" :aria-label="partial ? 'Show items. Counts omit oversized goals.' : 'Show items'">
      <button
        v-for="(state, i) in STATE_FILTERS" :key="state" type="button" role="radio" :data-state="state" :aria-checked="page.filter.value === state"
        :data-partial="partial ? 1 : 0" :data-zero="counts[state] ? 0 : 1" :data-tip="tip(state, i)" :aria-label="countLabel(state)" aria-keyshortcuts="1 2 3 4 5" @click="page.filter.value = state"
      >
        <span v-if="state === 'all'">All</span><GoalStateIcon v-else :state="state" /><span class="n mono">{{ counts[state] }}</span>
      </button>
    </div>
    <button
      type="button" class="icon-btn toggle-btn" :aria-pressed="page.hideDone.value" aria-label="Hide done" aria-keyshortcuts="h"
      :data-tip="page.hideDone.value ? 'Done goals are hidden · H' : 'Hide done goals · H'" @click="page.setHideDone(!page.hideDone.value)"
    ><AppIcon name="eye-off" :size="15" /></button>
    <button type="button" class="btn sm expand-btn" aria-keyshortcuts="e" :data-tip="`${allOpen ? 'Collapse all' : 'Expand all'} · E`" @click="page.toggleAll()">
      <AppIcon :name="allOpen ? 'collapse-all' : 'expand-all'" :size="14" />
      <span class="lbl"><span :class="{ off: allOpen }">Expand all</span><span :class="{ off: !allOpen }">Collapse all</span></span>
    </button>
  </template>
  <template v-else>
    <button
      type="button" class="icon-btn more-btn" aria-haspopup="menu" :aria-expanded="!!moreAnchor" aria-label="More: state filter, hide done, expand all"
      @click="moreAnchor = moreAnchor ? null : ($event.currentTarget as HTMLElement)"
    ><AppIcon name="more" :size="16" /></button>
    <FloatingPanel v-if="moreAnchor" :anchor="moreAnchor" :width="300" align="end" label="Goal display" @close="closeMore">
      <div class="more-menu" role="menu" aria-label="Goal display">
        <p v-if="partial" class="partial-note" role="status">Counts omit oversized goals.</p>
        <button v-for="state in STATE_FILTERS" :key="state" type="button" role="menuitemradio" class="menu-item" :aria-checked="page.filter.value === state" @click="pick(state)">
          <AppIcon v-if="state === 'all'" name="layers" :size="14" /><GoalStateIcon v-else :state="state" />
          <span>{{ label(state) }}</span><span class="mcount mono">{{ counts[state] }}</span>
          <AppIcon v-if="page.filter.value === state" name="check" :size="14" class="tick" />
        </button>
        <div class="menu-sep" />
        <button type="button" role="menuitemcheckbox" class="menu-item" :aria-checked="page.hideDone.value" @click="page.setHideDone(!page.hideDone.value)">
          <AppIcon name="eye-off" :size="14" /><span>Hide done</span><span class="mini-switch" aria-hidden="true" />
        </button>
        <button type="button" role="menuitem" class="menu-item" @click="page.toggleAll(); closeMore(true)">
          <AppIcon :name="allOpen ? 'collapse-all' : 'expand-all'" :size="14" /><span>{{ allOpen ? 'Collapse all' : 'Expand all' }}</span>
        </button>
      </div>
    </FloatingPanel>
  </template>
</template>

<style scoped>
.chips-sep { flex: none; width: 1px; height: 18px; background: var(--line-2); }
.state-seg { flex: none; gap: 0; }
.state-seg button { padding: 0 7px; gap: 5px; font-size: 12.5px; }
.state-seg .n { font-size: 11px; font-weight: 600; line-height: 1; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.state-seg button[aria-checked="true"] .n { color: var(--teal-ink); }
.state-seg button[data-zero="1"] .n { color: var(--ink-3); }
.state-seg button[data-partial="1"] .n { text-decoration: underline dotted; text-underline-offset: 3px; }
/* The total keeps room for four digits, so the chips never shift as counts change. */
.state-seg [data-state="all"] .n { min-width: 3.3ch; text-align: left; }
.state-seg .n { min-width: 2.2ch; }
.toggle-btn { flex: none; }
.toggle-btn[aria-pressed="true"] { background: var(--seg-on); color: var(--teal-ink); box-shadow: 0 0 0 1px var(--chip-teal-line); }
.btn.sm.expand-btn { flex: none; gap: 4px; padding: 0 9px; color: var(--ink-2); }
/* Both labels share one box, so the button never changes width. */
.lbl { display: inline-grid; }
.lbl > span { grid-area: 1 / 1; }
.lbl > .off { visibility: hidden; }
.more-btn { display: none; width: 44px; height: 44px; }
/* Phones: state chips, Hide done and Expand all live behind ⋯ (and the 44 px Expand all row). */
@media (max-width: 760px) {
  .chips-sep, .state-seg, .toggle-btn, .expand-btn { display: none; }
  .more-btn { display: inline-grid; place-items: center; }
}
.more-menu { display: grid; padding: 6px; }
.partial-note { margin: 0; padding: 6px 10px 2px; font-size: 12.5px; line-height: 1.4; color: var(--ink-3); }
.menu-item { display: flex; align-items: center; gap: 10px; width: 100%; height: 44px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 14px; text-align: left; }
.menu-item > svg { color: var(--ink-3); flex: none; }
.menu-item:hover, .menu-item:focus-visible { background: var(--row-hover); }
.mcount { margin-left: auto; font-size: 11.5px; font-weight: 600; color: var(--ink-3); font-variant-numeric: tabular-nums; }
.menu-item .tick { color: var(--teal-ink); }
.menu-sep { height: 1px; margin: 5px 6px; background: var(--line); }
.mini-switch { position: relative; margin-left: auto; flex: none; width: 34px; height: 20px; border-radius: 999px; background: var(--track); box-shadow: inset 0 1px 3px color-mix(in srgb, var(--shadow-color) 20%, transparent); }
.mini-switch::after { content: ''; position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; border-radius: 50%; background: var(--switch-knob); box-shadow: 0 1px 3px color-mix(in srgb, var(--shadow-color) 30%, transparent); }
[aria-checked="true"] > .mini-switch { background: var(--primary); box-shadow: none; }
[aria-checked="true"] > .mini-switch::after { transform: translateX(14px); }
</style>
