<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, useId } from 'vue'

// A project's progress as a ring with its percentage inside: the list's teal to
// aqua bar, bent round. A project without work shows an empty ring and a dash.
const props = withDefaults(defineProps<{ percent: number; empty?: boolean; size?: number }>(), { empty: false, size: 48 })
const id = useId()
const R = 19
const C = 2 * Math.PI * R
const clamped = computed(() => Math.max(0, Math.min(100, Math.round(props.percent))))
const offset = computed(() => C * (1 - clamped.value / 100))
</script>

<template>
  <span class="ring" :style="{ '--ring': `${size}px` }" aria-hidden="true">
    <svg :width="size" :height="size" viewBox="0 0 48 48" fill="none" focusable="false">
      <defs>
        <linearGradient :id="`${id}-g`" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" class="stop-a" />
          <stop offset="1" class="stop-b" />
        </linearGradient>
      </defs>
      <circle class="track" cx="24" cy="24" :r="R" stroke-width="4" />
      <circle
        v-if="!empty && clamped > 0" class="arc" cx="24" cy="24" :r="R" stroke-width="4" stroke-linecap="round"
        :stroke="`url(#${id}-g)`" :stroke-dasharray="C" :stroke-dashoffset="offset" transform="rotate(-90 24 24)"
      />
    </svg>
    <span class="pct mono">{{ empty ? '—' : `${clamped}%` }}</span>
  </span>
</template>

<style scoped>
.ring { position: relative; display: inline-grid; place-items: center; flex-shrink: 0; width: var(--ring); height: var(--ring); }
.ring svg { position: absolute; inset: 0; }
.track { stroke: var(--track); }
.arc { filter: none; }
.stop-a { stop-color: var(--primary); }
.stop-b { stop-color: var(--primary-hi); }
.pct { position: relative; font-size: 11.5px; font-weight: 600; letter-spacing: -.02em; color: var(--ink); }
</style>
