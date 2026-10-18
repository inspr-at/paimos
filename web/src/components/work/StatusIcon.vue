<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The one status icon (AEON-1362, design r4b §7). Props: `state` (any spelling statusMeta knows) and `size` in px.
// In progress is a small HTML shape whose 45° stripes slide continuously, because only HTML layers slide on the
// compositor; it pauses off-screen, in hidden tabs and with reduced motion. Every other status is SVG with named parts
// for its one-off, which useStatusMotion plays when the control around the icon is hovered or keyboard-focused.
// Base shapes (rings, the disc, the square) carry no dash, so they draw whole; only the marks (ticks, the slash) carry
// pathLength .98 with one dash a touch longer than the path, so a draw can animate stroke-dashoffset from 1 to 0.
import { computed, ref } from 'vue'
import { statusMeta } from '../../lib/work'
import { useOffscreenPause } from '../../lib/statusMotion'
const props = withDefaults(defineProps<{ state: string; size?: number }>(), { size: 14 })
const key = computed(() => statusMeta(props.state).key)
const shape = ref<HTMLElement | null>(null)
const offscreen = useOffscreenPause(shape)
// Inline, so rules a host writes for its label spans (for example `.status-btn span`) cannot resize or re-display the
// shape: it keeps the SVG icons' box and baseline.
const shapeStyle = computed(() => ({ display: 'inline-block', position: 'relative' as const, flex: 'none', width: `${props.size}px`, height: `${props.size}px`, padding: '0', fontSize: `${props.size}px`, color: 'var(--st-progress)' }))
</script>

<template>
  <span v-if="key === 'progress'" ref="shape" class="status-icon st-progress st-ip" :class="{ offscreen }" :style="shapeStyle" aria-hidden="true"><i /></span>
  <svg v-else class="status-icon" :class="`st-${key}`" :width="size" :height="size" viewBox="0 0 14 14" fill="none" overflow="visible" aria-hidden="true" focusable="false">
    <template v-if="key === 'new'"><circle class="ring" cx="7" cy="7" r="5.1" stroke="var(--st-new)" stroke-width="1.8" /><circle class="dot" cx="11" cy="3" r="2.6" fill="var(--st-new)" stroke="var(--surface)" stroke-width="1.2" /></template>
    <template v-else-if="key === 'open'"><circle class="ring" cx="7" cy="7" r="5.1" stroke="var(--st-progress)" stroke-width="1.8" /><circle class="glint" pathLength="1" stroke-dasharray=".14 .86" cx="7" cy="7" r="5.1" stroke="var(--surface)" stroke-width="2.2" opacity="0" transform="rotate(-90 7 7)" /></template>
    <circle v-else-if="key === 'backlog'" class="dots" cx="7" cy="7" r="5.1" stroke="var(--st-backlog)" stroke-width="1.6" stroke-dasharray="2.1 1.9" />
    <template v-else-if="key === 'blocked'"><circle class="dashes" cx="7" cy="7" r="5.1" stroke="var(--st-blocked)" stroke-width="1.6" stroke-dasharray="2.1 1.9" /><path d="M4.7 7h4.6" stroke="var(--st-blocked)" stroke-width="1.6" stroke-linecap="round" /></template>
    <template v-else-if="key === 'qa'"><circle class="pulse" cx="7" cy="7" r="5.1" stroke="var(--st-qa-ring)" stroke-width="1.4" opacity="0" /><circle class="ring" cx="7" cy="7" r="5.1" fill="var(--st-qa-fill)" stroke="var(--st-qa-ring)" stroke-width="1.8" /></template>
    <template v-else-if="key === 'done'"><circle class="ring" cx="7" cy="7" r="5.1" stroke="var(--st-ok)" stroke-width="1.6" /><path class="tick" pathLength=".98" stroke-dasharray="1 1" d="m4.6 7.2 1.7 1.7 3.2-3.5" stroke="var(--st-ok)" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></template>
    <template v-else-if="key === 'delivered'"><circle class="disc" cx="7" cy="7" r="6" fill="var(--st-ok)" /><path class="tick" pathLength=".98" stroke-dasharray="1 1" d="m4.4 7.2 1.8 1.8 3.4-3.7" stroke="var(--st-ok-on)" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></template>
    <template v-else-if="key === 'accepted'"><circle class="disc" cx="7" cy="7" r="6" fill="var(--st-ok)" /><path class="tick" pathLength=".98" stroke-dasharray="1 1" d="m2.9 7.4 1.6 1.6 3-3.3" stroke="var(--st-ok-on)" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /><path class="tick2" pathLength=".98" stroke-dasharray="1 1" d="m6.6 8.6.4.4 3.2-3.5" stroke="var(--st-ok-on)" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" /></template>
    <template v-else-if="key === 'cancelled'"><circle class="ring" cx="7" cy="7" r="5.1" stroke="var(--st-closed)" stroke-width="1.6" /><path class="slash" pathLength=".98" stroke-dasharray="1 1" d="m3.6 10.4 6.8-6.8" stroke="var(--st-closed)" stroke-width="1.6" stroke-linecap="round" /></template>
    <rect v-else-if="key === 'archived'" class="sq" x="2.2" y="2.2" width="9.6" height="9.6" rx="2.2" stroke="var(--st-closed)" stroke-width="1.6" />
    <template v-else><circle cx="7" cy="7" r="5.1" stroke="var(--st-backlog)" stroke-width="1.6" /><circle cx="7" cy="7" r="1.4" fill="var(--st-backlog)" /></template>
  </svg>
</template>

<style scoped>
.st-qa { filter: drop-shadow(0 0 3px color-mix(in srgb, var(--st-qa-ring) 70%, transparent)); }
</style>
