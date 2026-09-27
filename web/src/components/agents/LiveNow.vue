<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { elapsed } from '../../lib/agentState'
import type { SessionView } from '../../stores/agents'
import AppIcon from '../AppIcon.vue'
import TicketPeekLink from '../TicketPeekLink.vue'
import AgentGlyph from './AgentGlyph.vue'
import AgentStateLabel from './AgentStateLabel.vue'
import { useAgentAppearance } from '../../lib/agentAppearance'
const { appearance } = useAgentAppearance()
import { currentStep } from './activity'

const props = defineProps<{ views: SessionView[]; now: number; selected: string; loaded: boolean; canStart: boolean }>()
const emit = defineEmits<{ open: [id: string] }>()
const live = computed(() => props.views.filter(view => !view.session.stopped_at && view.session.phase !== 'stopped' && view.status.group !== 'idle'))
</script>

<template>
  <section v-if="!loaded || live.length || (canStart && views.length)" class="live-now" aria-labelledby="live-now-title">
    <h2 id="live-now-title" class="eyebrow">Live now</h2>
    <div v-if="!loaded" class="tiles" role="status" aria-label="Loading live agents"><span v-for="n in 3" :key="n" class="tile skeleton" /></div>
    <div v-else-if="live.length" class="tiles">
      <article v-for="view in live" :key="view.session.id" class="tile agent-state-surface" :data-state="view.status.state" :style="appearance(view.status.state)" :class="{ selected: selected === view.session.id }">
        <button type="button" class="tile-open" :aria-label="`Open ${view.name}, ${view.status.label}, ${currentStep(view)}`" @click="emit('open', view.session.id)" />
        <AgentGlyph class="glyph" :view="view" :size="43" />
        <span class="tile-body"><strong>{{ view.name }}</strong><TicketPeekLink v-if="view.ticket" class="ticket-key" :ticket-key="view.ticket.key" :href="view.ticket.href" :tip="view.ticket.title">{{ view.ticket.key }}</TicketPeekLink><AgentStateLabel :state="view.status.state" :label="view.status.label" /><span class="step">{{ currentStep(view) }}</span><span class="elapsed"><AppIcon name="clock" :size="13" />{{ elapsed(view.session, now) }}</span></span>
      </article>
    </div>
    <p v-else class="quiet">No agents are working now. <button type="button" @click="emit('open', '')">Start an agent</button></p>
  </section>
</template>

<style scoped>
.live-now { min-width: 0; }
.live-now h2 { margin: 0 0 10px 2px; color: var(--ink-2); }
.tiles { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 235px), 1fr)); gap: 10px; }
.tile { position: relative; display: flex; align-items: flex-start; gap: 13px; min-width: 0; min-height: 122px; padding: 16px; border-radius: 13px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--glass-edge), var(--shadow); color: var(--ink); text-align: left; }
.tile-open { position: absolute; inset: 0; border: 0; border-radius: inherit; background: transparent; cursor: pointer; }
.tile:hover { background: var(--row-hover); }
.tile.selected, .tile:has(.tile-open:focus-visible) { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line), var(--shadow); }
.tile-open:focus-visible { outline: none; }
.glyph, .tile-body { position: relative; z-index: 1; pointer-events: none; }
.tile-body { display: grid; align-content: start; gap: 5px; min-width: 0; }
.ticket-key { pointer-events: auto; text-decoration: none; }
.tile strong, .step { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tile strong { font-size: 14px; }
.ticket-key { justify-self: start; width: fit-content; padding: 3px 6px; border-radius: 5px; background: var(--chip-teal-bg); color: var(--teal-ink); font: 600 11px/1.2 var(--mono); }
.ticket-key:focus-visible { box-shadow: var(--focus-ring); }
.step { max-width: 100%; font-size: 12.5px; color: var(--ink-2); }
.elapsed { display: inline-flex; align-items: center; gap: 6px; color: var(--ink-3); font: 12px/1.2 var(--mono); }
.elapsed :deep(svg) { flex: none; }
.quiet { padding: 16px; border-radius: 12px; background: var(--surface-raised); color: var(--ink-2); font-size: 13px; }
.quiet button { border: 0; background: transparent; color: var(--teal-ink); font: inherit; font-weight: 650; cursor: pointer; }
@media (max-width: 520px) {
  .tiles { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .tile { display: grid; grid-template-columns: 1fr; align-content: start; min-height: 165px; padding: 12px; gap: 6px; }
  .ticket-key { min-height: 32px; }
  .tile-body { width: 100%; }
  .tile strong { white-space: normal; overflow-wrap: anywhere; line-height: 1.25; }
  .step { display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; white-space: normal; overflow: hidden; line-height: 1.3; }
}
@media (max-width: 360px) { .tiles { grid-template-columns: 1fr; } }
</style>
