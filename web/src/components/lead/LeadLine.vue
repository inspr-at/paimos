<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { IconName } from '../AppIcon.vue'
import { LEAD_WORDS, type Station, type StationId } from '../../lib/lead'
import AppIcon from '../AppIcon.vue'
import TicketPeekLink from '../TicketPeekLink.vue'

// The lead's metaphor: four stations on one hairline. An overview only; every
// station opens the docked lead panel, every key opens its ticket.
defineProps<{ stations: Station[]; routeKey: string; empty?: boolean }>()
const emit = defineEmits<{ open: [from: HTMLElement] }>()
const ICON: Record<StationId, IconName> = { queued: 'queue', working: 'agent', gate: 'shield', merged: 'merge' }
const TIP = computed<Record<StationId, string>>(() => ({
  queued: 'Queued work in this project, in queue order',
  working: 'Tickets its workers are on right now',
  gate: `Waiting for a cross-family review, as the ${LEAD_WORDS.l} reported`,
  merged: `Merged and handed to release today, as the ${LEAD_WORDS.l} reported`,
}))
const href = (routeKey: string, key: string) => `/p/${encodeURIComponent(routeKey)}/${encodeURIComponent(key)}`
</script>

<template>
  <div class="line" :class="{ empty }" role="group" aria-label="Where the work is">
    <div v-for="s in stations" :key="s.id" class="station" :class="s.tone" :data-station="s.id">
      <span class="st-mark"><AppIcon :name="ICON[s.id]" :size="13" /></span>
      <button type="button" class="st-label" :data-tip="TIP[s.id]" :aria-label="`${s.count ?? 'Unknown'} ${s.label}. Open the ${LEAD_WORDS.l} panel`" @click="emit('open', $event.currentTarget as HTMLElement)"><b>{{ s.count ?? '—' }}</b>{{ s.label }}</button>
      <span class="st-keys"><TicketPeekLink v-for="k in s.keys" :key="k" class="key" :ticket-key="k" :href="href(routeKey, k)" /><span v-if="s.more">+{{ s.more }}</span></span>
    </div>
  </div>
</template>

<style scoped>
.line { position: relative; display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); margin: 18px -10px 0 48px; }
.line::before { content: ''; position: absolute; left: 22px; right: calc(25% - 22px); top: 24px; height: 1px; background: var(--line-2); }
.station { position: relative; display: grid; grid-template-columns: minmax(0, 1fr); gap: 2px; align-content: start; justify-items: start; min-height: 96px; padding: 12px 10px; border-radius: var(--radius-row, 8px); }
.station:has(.st-label:hover) { background: var(--row-hover); }
.st-mark { margin-bottom: 8px; display: grid; place-items: center; width: 24px; height: 24px; border-radius: 50%; background: var(--surface-raised); box-shadow: inset 0 0 0 1.5px var(--line-2); color: var(--ink-3); }
.station.live .st-mark { box-shadow: inset 0 0 0 1.5px var(--teal); color: var(--teal-ink); background: color-mix(in srgb, var(--teal) 9%, var(--surface-raised)); }
.station.done .st-mark { background: color-mix(in srgb, var(--ok) 14%, var(--surface-raised)); box-shadow: inset 0 0 0 1.5px color-mix(in srgb, var(--ok) 60%, transparent); color: var(--ok); }
.station.hold .st-mark { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1.5px var(--queue-wait-line); color: var(--queue-wait-ink); }
.st-label { display: flex; align-items: baseline; gap: 8px; padding: 0; border: 0; background: none; color: var(--ink-2); font-size: 12.5px; text-align: left; cursor: pointer; }
.st-label b { color: var(--ink); font-size: 20px; font-weight: 650; letter-spacing: -.01em; font-variant-numeric: tabular-nums; line-height: 1.1; }
.st-keys { display: flex; flex-wrap: wrap; gap: 0 8px; font: 500 11.5px/1.6 var(--mono); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.key { font-family: var(--mono); font-size: 11.5px; color: var(--teal-ink); text-decoration: none; }
.key:hover { text-decoration: underline; text-underline-offset: 3px; }
.line.empty .st-label b, .line.empty .st-keys { visibility: hidden; }
.line.empty .station { pointer-events: none; }
.line.empty .st-label::after { content: ''; display: block; width: 46px; height: 10px; border-radius: 4px; background: var(--skeleton); }
@media (max-width: 720px) {
  .line { grid-template-columns: repeat(2, minmax(0, 1fr)); margin: 14px -6px 0; }
  .line::before { display: none; }
  .station { min-height: 76px; }
}
@media (max-width: 420px) { .st-label b { font-size: 18px; } }
</style>
