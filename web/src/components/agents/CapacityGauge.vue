<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { Gauge } from '../../lib/capacity'

// The capacity gauge, shared by the Agents desk and the Usage page: what is left,
// what is kept for you (Keep for you, a dotted tint at the 0 end, the last part
// any agent could reach), today's share at its end, the stop tick, and today's
// spend as a hatch. "% used" mirrors it, so the empty part on the left is what
// has been used. Without a gauge it is an empty track (no reading yet).
withDefaults(defineProps<{
  gauge: Gauge | null
  /** Percent left of the binding window; where today's spend starts. */
  left?: number
  /** The figure the meter announces (left or used, per the mode). */
  value?: number
  used?: boolean
  label?: string
  ahead?: boolean
  dim?: boolean
  estimated?: boolean
}>(), { left: 0, value: 0, used: false, label: '', ahead: false, dim: false })
</script>

<template>
  <div
    v-if="gauge" class="gauge" :class="{ frozen: gauge.frozen, used, ahead, dim, estimated }" role="meter" aria-valuemin="0" aria-valuemax="100"
    :aria-valuenow="Math.round(value)" :aria-label="label" :data-tip="label || undefined"
  >
    <div class="track">
      <i v-if="gauge.yours > 0" class="g-yours" :style="{ left: '0', width: `${gauge.yours}%` }" />
      <i class="g-later" :style="{ left: `${gauge.yours}%`, width: `${gauge.later}%` }" />
      <i class="g-today" :style="{ left: `${gauge.yours + gauge.later}%`, width: `${gauge.today}%` }" />
      <i class="g-spent" :style="{ left: `${left}%`, width: `${gauge.spent}%` }" />
    </div>
    <b v-if="gauge.tick !== null" class="g-tick" :style="{ left: `${gauge.tick}%` }" />
  </div>
  <div v-else class="gauge empty"><div class="track" /></div>
</template>

<style scoped>
.gauge { position: relative; height: 14px; min-width: 0; }
.gauge.used { transform: scaleX(-1); }
.track { position: absolute; inset: 3px 0; border-radius: 999px; background: var(--track); overflow: hidden; box-shadow: inset 0 1px 2px rgba(32, 60, 61, .08); }
.track i { position: absolute; top: 0; bottom: 0; }
.g-later { background: color-mix(in srgb, var(--teal) 32%, transparent); }
.g-yours { background: radial-gradient(circle, color-mix(in srgb, var(--ink-3) 75%, transparent) 0 .8px, transparent 1.2px) 0 0 / 4px 4px, color-mix(in srgb, var(--ink-3) 16%, transparent); }
.g-today { background: linear-gradient(90deg, color-mix(in srgb, var(--teal) 88%, var(--aqua)), var(--teal)); }
.g-spent { background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--teal) 55%, transparent) 0 1.5px, transparent 1.5px 4.5px); }
.ahead .g-spent { background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--gold) 75%, transparent) 0 1.5px, transparent 1.5px 4.5px); }
.g-tick { position: absolute; top: 0; width: 2px; height: 14px; margin-left: -1px; border-radius: 2px; background: var(--ink); box-shadow: 0 0 0 1.5px var(--surface-raised); }
.ahead .g-tick { background: var(--gold-ink); }
.dim .g-later, .frozen .g-later { background: color-mix(in srgb, var(--ink-3) 30%, transparent); }
.estimated .g-later, .estimated .g-today { background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--teal) 65%, transparent) 0 2px, color-mix(in srgb, var(--teal) 18%, transparent) 2px 5px); }
.gauge.frozen { opacity: .6; }
</style>
