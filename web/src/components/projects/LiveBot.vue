<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
import { defineAsyncComponent, type Component } from 'vue'
import { availableIndicatorVariants, indicatorArtScale, indicatorRing, resolveIndicatorStyle } from '../../lib/indicatorVariants'

// Filename-only discovery lets the other indicator packages land in any order.
// Export availability for Settings; unavailable entries never load or select.
const files = import.meta.glob<Component>('../indicators/*.vue', { import: 'default' })
export const availableVariants = availableIndicatorVariants(Object.keys(files))
const renderers = Object.fromEntries(availableVariants.map(variant => [
  variant.id, defineAsyncComponent(files[`../indicators/${variant.file}`]!),
]))
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import type { Harness } from '../../lib/agents'
import type { LiveBotState } from '../../lib/liveAgents'
import { STATE_LABEL } from '../../lib/agentSignals'
import { agentStateAppearance, useAgentAppearance } from '../../lib/agentAppearance'
import type { AgentThemeAppearance } from '../../lib/agentTheme'
import { normalizeAgentState } from '../../lib/agentSignals'
import { normalizeAgentIndicator, useAgentIndicator, type AgentIndicatorStyle } from '../../lib/agentIndicator'

// Public LA1/LA2 props stay stable for cards, rows and /agents. Artwork is
// decorative; consumers own accessible status labels. Only real event evidence
// advances eventPulse. The viewer's hovering preference belongs to this wrapper.
const props = withDefaults(defineProps<{
  state?: LiveBotState; label?: string; harness?: Harness; size?: number; eventPulse?: number; eventCaption?: string
  indicatorStyle?: AgentIndicatorStyle | 'calm' | 'playful'; id?: string; index?: number; lead?: boolean
  themeAgents?: AgentThemeAppearance
}>(), { state: 'working', size: 28, eventPulse: 0, eventCaption: '', id: '', index: 0, lead: true })
const { choice } = useAgentIndicator()
const drawn = computed(() => props.themeAgents ? normalizeAgentIndicator({ style: props.themeAgents.avatar, ring: props.themeAgents.ring, hovering: props.themeAgents.hover, size: props.themeAgents.size }) : choice.value)
const indicator = computed(() => resolveIndicatorStyle(normalizeAgentIndicator({ style: props.indicatorStyle ?? drawn.value.style }).style, availableVariants))
// Ring and inner size follow the viewer everywhere, previews included; unset keeps each style's drawing.
const ring = computed(() => indicatorRing(indicator.value, drawn.value.ring))
const artScale = computed(() => indicatorArtScale(indicator.value, drawn.value.size))
const { appearance } = useAgentAppearance()
const stateAppearance = computed(() => props.themeAgents ? agentStateAppearance(props.state, normalizeAgentState({ palette: props.themeAgents.palette, dimInactive: props.themeAgents.dim_inactive, inactiveOpacity: props.themeAgents.inactive_opacity })) : appearance(props.state))
const seed = computed(() => `${props.id}:${props.index}`)
const style = computed(() => ({ '--size': `${props.size}px`, '--lag': `${-(props.index * .53 + ((parseInt(props.id.slice(0, 2), 16) || 0) % 7) * .31).toFixed(2)}s` }))
const pulse = ref(0)
let lastPulse = props.eventPulse
let clear: ReturnType<typeof setTimeout> | undefined
watch(() => props.eventPulse, value => {
  if (!Number.isFinite(value) || value <= lastPulse) return
  lastPulse = value
  if (props.state !== 'working') return
  clearTimeout(clear)
  pulse.value++
  clear = setTimeout(() => { pulse.value = 0 }, 600)
})
watch(() => props.state, state => {
  if (state !== 'working') { clearTimeout(clear); pulse.value = 0 }
})
onBeforeUnmount(() => clearTimeout(clear))
</script>

<template>
  <span class="live-bot" :class="[state, { hovering: drawn.hovering, lead }]" :style="[style, stateAppearance]" :data-style="indicator" :data-state="state" :data-harness="harness" :aria-label="label || STATE_LABEL[state]" role="img">
    <span class="indicator-art">
      <component :is="renderers[indicator]" :state="state" :size="size" :pulse="eventPulse" :seed="seed" :lead="lead" :ring="ring" :art-scale="artScale" :data-ring="ring" />
    </span>
    <span v-if="pulse && eventCaption" :key="`caption-${pulse}`" class="event-caption">{{ eventCaption }}</span>
  </span>
</template>

<style scoped>
.live-bot { position: relative; display: inline-grid; place-items: center; flex-shrink: 0; width: var(--size); height: var(--size); vertical-align: middle; }
.indicator-art { display: grid; place-items: center; width: 100%; height: 100%; }
.event-caption { position: absolute; top: calc(100% + 3px); left: 50%; translate: -50% 0; white-space: nowrap; font: 500 10px/1.2 var(--font); color: var(--secondary-ink); pointer-events: none; animation: event-opacity .6s ease-out both; }
@media (prefers-reduced-motion: no-preference) {
  /* The original robots float their faces inside stationary disks. */
  .hovering.working.lead:not([data-style="robot-1"], [data-style="robot-5"]) .indicator-art { animation: indicator-hover 2.4s ease-in-out infinite; animation-delay: var(--lag); }
}
@keyframes indicator-hover { 0%, 100% { transform: translateY(0); } 50% { transform: translateY(-1.1px); } }
@keyframes event-opacity { 0%, 100% { opacity: 0; } 25%, 50% { opacity: 1; } }
</style>

<style>
/* Project chips pause every variant, including hovering, outside the viewport. */
.asleep .live-bot * { animation-play-state: paused !important; }
</style>
