<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The key to CapacityGauge, as inline items for a footer row.
</script>

<template>
  <span class="legend"><span class="sw"><i class="g-today" /></span>today's share</span>
  <span class="legend"><span class="sw"><i class="g-later" /></span>later days</span>
  <span class="legend"><span class="sw"><i class="g-spent" /></span>used today</span>
  <span class="legend"><span class="sw tick" />stop here tonight</span>
</template>

<style scoped>
.legend { display: inline-flex; align-items: center; gap: 7px; white-space: nowrap; }
.sw { position: relative; display: inline-block; width: 18px; height: 8px; border-radius: 999px; overflow: hidden; background: var(--track); }
.sw i { position: absolute; inset: 0; }
.sw.tick { width: 2px; height: 12px; border-radius: 2px; background: var(--ink); overflow: visible; }
.g-later { background: color-mix(in srgb, var(--teal) 32%, transparent); }
.g-today { background: linear-gradient(90deg, color-mix(in srgb, var(--teal) 88%, var(--aqua)), var(--teal)); }
.g-spent { background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--teal) 55%, transparent) 0 1.5px, transparent 1.5px 4.5px); }
</style>
