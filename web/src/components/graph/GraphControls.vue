<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'
import type { GraphDimension, GraphFPS, GraphLabels } from '../../lib/graphRenderer'
defineProps<{ dimension: GraphDimension; paused: boolean; fallback: boolean; labels: GraphLabels; fps: GraphFPS; focus: boolean }>()
const emit = defineEmits<{ fit: []; dimension: [value: GraphDimension]; pause: []; labels: [value: GraphLabels]; fps: [value: GraphFPS]; focus: [] }>()
</script>
<template>
  <div class="graph-controls" role="group" aria-label="Graph controls">
    <button type="button" class="btn sm" aria-label="Fit graph to view" data-tip="Fit to view" @click="emit('fit')"><svg data-icon="frame" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><path d="M8 3H3v5m13-5h5v5M3 16v5h5m13-5v5h-5" /></svg><span>Fit</span></button>
    <div class="seg" role="group" aria-label="Graph dimensions">
      <button type="button" :aria-pressed="dimension === '2d'" @click="emit('dimension', '2d')">2D</button>
      <button type="button" :aria-pressed="dimension === '3d'" :disabled="fallback" :title="fallback ? '3D is unavailable in this browser' : 'Explore in three dimensions'" @click="emit('dimension', '3d')">3D</button>
    </div>
    <button type="button" class="btn sm graph-motion" :aria-pressed="paused" :aria-label="paused ? 'Resume motion' : 'Pause motion'" @click="emit('pause')"><AppIcon :name="paused ? 'play' : 'pause'" :size="14" /><span>Motion</span></button>
    <label class="graph-label-control">Labels<select aria-label="Graph labels" :value="labels" @change="emit('labels', ($event.target as HTMLSelectElement).value as GraphLabels)"><option value="off">Off</option><option value="smart">Smart</option><option value="all">All</option></select></label>
    <slot />
    <details class="graph-menu"><summary class="icon-btn sm flat" aria-label="Graph settings" data-tip="Graph settings"><AppIcon name="sliders" :size="16" /></summary><div class="graph-menu-panel"><label>Frame rate<select aria-label="Graph frame rate" :value="fps" @change="emit('fps', Number(($event.target as HTMLSelectElement).value) as GraphFPS)"><option :value="60">60 FPS</option><option :value="30">30 FPS</option></select></label></div></details>
    <button type="button" class="btn sm" :aria-label="focus ? 'Exit graph focus mode' : 'Maximize graph'" :aria-pressed="focus" :data-tip="focus ? 'Exit focus · Esc' : 'Focus mode'" @click="emit('focus')"><svg data-icon="maximize" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path v-if="!focus" d="M14 3h7v7m0-7-7 7M10 21H3v-7m0 7 7-7" /><path v-else d="M21 3l-7 7m0-7v7h7M3 21l7-7m-7 0h7v7" /></svg><span>{{ focus ? 'Exit' : 'Focus' }}</span></button>
  </div>
</template>
<style scoped>
.graph-controls { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.seg button { min-width: 34px; height: 28px; justify-content: center; padding: 0 9px; }
.seg button[aria-pressed="true"] { background: var(--seg-on); box-shadow: var(--shadow-btn); color: var(--teal-ink); }
.btn[aria-pressed="true"] { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.graph-label-control { display: flex; align-items: center; gap: 5px; color: var(--ink-2); font-size: 11px; }
select { border: 1px solid var(--line); border-radius: 6px; background: var(--surface-raised); color: var(--ink); padding: 5px; font: inherit; }
.graph-menu { position: relative; }
.graph-menu summary { list-style: none; cursor: pointer; }
.graph-menu summary::-webkit-details-marker { display: none; }
.graph-menu-panel { position: absolute; top: calc(100% + 10px); right: 0; width: 185px; padding: 14px; border-radius: 12px; background: var(--surface-raised); box-shadow: var(--shadow-pop); border: 1px solid var(--line); z-index: 10; }
.graph-menu-panel label { display: flex; align-items: center; justify-content: space-between; gap: 10px; font-size: 12px; }
@media (max-width: 600px) { .graph-controls { gap: 6px; } .graph-controls .btn, .seg button, select, .graph-menu summary { min-height: 40px; } .graph-motion span { display: none; } }
</style>
