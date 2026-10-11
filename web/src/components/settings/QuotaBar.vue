<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1172 (design AEON-1149): one picture of every limit on an account. Used (teal),
// room left today (pale teal), kept for you while you work (gold hatch), the floor
// (ink hatch) and today's limit (a tick). No reading draws a dashed track, never a
// guess. "sm" is the account list's bar; "lg" names the tick above the bar.
import { computed } from 'vue'
import { quotaSegments, type QuotaBar } from '../../lib/quotaBar'
const props = withDefaults(defineProps<{ bar: QuotaBar; label: string; size?: 'sm' | 'lg' }>(), { size: 'sm' })
const seg = computed(() => quotaSegments(props.bar))
const w = (n: number) => `${n}%`
</script>
<template>
  <div class="qb" :class="[size, { none: !bar.known }]" role="img" :aria-label="label" :data-tip="size === 'sm' ? label : undefined" data-quota-bar>
    <div class="qb-in"><template v-if="bar.known"><i class="u" :style="{ width: w(seg.used) }" /><i v-if="seg.room" class="room" :style="{ left: w(seg.room.at), width: w(seg.room.width) }" /><i v-if="seg.keep" class="keep" :style="{ width: w(seg.keep) }" /><i v-if="seg.floor" class="floor" :style="{ width: w(seg.floor) }" /></template></div>
    <span v-if="seg.tick" class="tick" :class="seg.tick.edge" :style="{ left: w(seg.tick.at) }"><b v-if="size === 'lg'">{{ seg.tick.label }}</b></span>
  </div>
</template>
<style scoped>
.qb { position: relative; height: 10px; }
.qb-in { position: absolute; inset: 0; overflow: hidden; border-radius: 5px; background: var(--track); box-shadow: inset 0 0 0 1px var(--line); }
.qb-in > i { position: absolute; top: 0; bottom: 0; }
.u { left: 0; z-index: 3; background: color-mix(in srgb, var(--teal) 78%, transparent); }
.room { z-index: 2; background: color-mix(in srgb, var(--teal) 24%, transparent); }
.keep { right: 0; z-index: 1; background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--gold) 55%, transparent) 0 1.5px, transparent 1.5px 5px), color-mix(in srgb, var(--gold) 10%, transparent); }
.floor { right: 0; z-index: 1; background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--ink) 55%, transparent) 0 1.5px, transparent 1.5px 3.5px), var(--surface-raised); }
.tick { position: absolute; z-index: 4; top: -4px; bottom: -4px; width: 2px; margin-left: -1px; border-radius: 1px; background: var(--ink); }
.tick b { position: absolute; bottom: calc(100% + 3px); left: 50%; transform: translateX(-50%); font: 600 10.5px/1 var(--mono); color: var(--ink-2); white-space: nowrap; }
.tick.end b { left: auto; right: -2px; transform: none; }
.tick.start b { left: -2px; transform: none; }
.lg { height: 12px; margin-top: 18px; }
.lg .qb-in { border-radius: 6px; }
.sm { height: 6px; }
.sm .qb-in { border-radius: 3px; }
.sm .tick { top: -3px; bottom: -3px; }
.none .qb-in { background: repeating-linear-gradient(90deg, var(--line) 0 4px, transparent 4px 8px); box-shadow: none; }
@media (pointer: coarse) { .sm { pointer-events: none; } }
</style>
