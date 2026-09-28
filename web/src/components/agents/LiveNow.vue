<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { elapsed, GROUPS } from '../../lib/agentState'
import type { SessionView } from '../../stores/agents'
import AgentGlyph from './AgentGlyph.vue'
import { useAgentAppearance } from '../../lib/agentAppearance'
import { currentStep } from './activity'
const { appearance } = useAgentAppearance()

// A glance strip: who is live, in urgency order, each a single compact chip.
// Step, state and time live in the tooltip and the accessible name; the Sessions
// table below carries the full detail, so nothing here is repeated as text.
const props = defineProps<{ views: SessionView[]; now: number; selected: string; loaded: boolean; canStart: boolean }>()
const emit = defineEmits<{ open: [id: string] }>()
const rank = (view: SessionView) => GROUPS.findIndex(g => g.id === view.status.group)
const live = computed(() => props.views
  .filter(view => !view.session.stopped_at && view.session.phase !== 'stopped' && view.status.group !== 'idle')
  .map((view, index) => ({ view, index })).sort((a, b) => rank(a.view) - rank(b.view) || a.index - b.index).map(item => item.view))
const tip = (view: SessionView) => [view.status.label, view.ticket?.key, currentStep(view), elapsed(view.session, props.now)].filter(Boolean).join(' · ')
</script>

<template>
  <section v-if="!loaded || live.length" class="live-now" aria-labelledby="live-now-title">
    <h2 id="live-now-title" class="eyebrow">Live now</h2>
    <div v-if="!loaded" class="tiles" role="status" aria-label="Loading live agents"><span v-for="n in 4" :key="n" class="tile skeleton" /></div>
    <ul v-else class="tiles">
      <li
        v-for="view in live" :key="view.session.id" class="tile agent-state-surface" :data-state="view.status.state" :style="appearance(view.status.state)"
        :class="{ selected: selected === view.session.id }" :data-tip="tip(view)"
      >
        <button type="button" class="tile-open" :aria-label="`Open ${view.name}, ${view.status.label}, ${currentStep(view)}`" @click="emit('open', view.session.id)" />
        <AgentGlyph class="glyph" :view="view" :size="26" />
        <span class="name" :title="view.name">{{ view.name }}</span>
        <span class="sr-only">{{ view.status.label }}. {{ currentStep(view) }}. {{ elapsed(view.session, now) }}</span>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.live-now { display: flex; align-items: flex-start; gap: 12px; min-width: 0; }
.live-now h2 { flex: none; margin: 0 2px; line-height: 36px; color: var(--ink-3); }
.tiles { display: flex; flex-wrap: wrap; gap: 6px; min-width: 0; margin: 0; padding: 0; list-style: none; }
.tile { position: relative; display: inline-flex; align-items: center; gap: 7px; min-width: 0; max-width: 240px; height: 36px; padding: 0 12px 0 5px; border-radius: 999px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--glass-edge); color: var(--ink); }
.tile.skeleton { width: 132px; }
.tile-open { position: absolute; inset: 0; z-index: 0; border: 0; border-radius: inherit; background: transparent; cursor: pointer; }
@media (hover: hover) { .tile:hover { background: var(--row-hover); } }
.tile.selected, .tile:has(.tile-open:focus-visible) { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.tile-open:focus-visible { outline: none; }
.glyph, .name { position: relative; z-index: 1; pointer-events: none; }
.name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; font-weight: 600; }
/* Phones: the strip scrolls sideways inside itself instead of stacking a wall of chips. */
@media (max-width: 600px) {
  .live-now { display: grid; gap: 6px; }
  .live-now h2 { line-height: 1.4; }
  .tiles { flex-wrap: nowrap; overflow-x: auto; overscroll-behavior-x: contain; scrollbar-width: none; margin: 0 -12px; padding: 0 12px 2px; }
  .tiles::-webkit-scrollbar { display: none; }
  .tile { flex: none; max-width: 220px; height: 40px; }
}
</style>
