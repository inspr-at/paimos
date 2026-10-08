<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One Expert number (AEON-994 draft 1): value, p50/p90 or its own detail, the
// count, the change against the window before, the Arion target and the source.
// Every line keeps its height in every state, so the grid never jumps.
import AppIcon from '../AppIcon.vue'
import { vClipTip } from '../../directives/clipTip'
import type { TileModel } from '../../lib/deliveryNumbers'
import type { DeliveryText } from '../../lib/deliveryNumbersText'

const props = defineProps<{ tile: TileModel; state: 'loading' | 'error' | 'ready'; text: DeliveryText; infoOpen: boolean }>()
const emit = defineEmits<{ info: [anchor: HTMLElement, pin: boolean]; 'info-leave': [] }>()
const id = (suffix: string) => `dl-${props.tile.def.key}-${suffix}`
</script>

<template>
  <div class="tile" role="listitem" :aria-labelledby="id('label')" :aria-busy="state === 'loading' || undefined">
    <div class="t-top">
      <span :id="id('label')" v-clip-tip class="t-label">{{ tile.label }}</span>
      <button type="button" class="info" :aria-label="`${text.infoFor} ${tile.label}`" :aria-expanded="infoOpen"
        @click="emit('info', $event.currentTarget as HTMLElement, true)" @mouseenter="emit('info', $event.currentTarget as HTMLElement, false)"
        @mouseleave="emit('info-leave')" @focus="emit('info', $event.currentTarget as HTMLElement, false)" @blur="emit('info-leave')"><AppIcon name="info" :size="14" /></button>
    </div>
    <template v-if="state === 'loading'">
      <div class="t-value"><span class="sk" style="width: 62%; height: 26px" /></div>
      <div class="t-line"><span class="sk" style="width: 80%; height: 10px" /></div>
      <div class="t-line"><span class="sk" style="width: 55%; height: 10px" /></div>
      <div class="t-line"><span class="sk" style="width: 70%; height: 10px" /></div>
    </template>
    <template v-else-if="state === 'error' || !tile.value">
      <div class="t-value empty">{{ state === 'error' ? text.notLoaded : text.noData }}</div>
      <div class="t-line"><span class="t-pp">p50 · p90 –</span></div>
      <div class="t-line"><span>&nbsp;</span></div>
      <div class="t-line t-delta none"><AppIcon name="minus" :size="13" /><span v-clip-tip>{{ state === 'error' ? text.notLoaded : tile.delta.text }}</span></div>
    </template>
    <template v-else>
      <div class="t-value">{{ tile.value.main }}<small>{{ tile.value.unit }}</small></div>
      <div class="t-line">
        <span v-if="tile.lineA.squares" class="mini-n" aria-hidden="true"><i v-for="(night, index) in tile.lineA.squares" :key="index" :class="night" /></span>
        <span v-clip-tip :class="{ 't-pp': tile.lineA.mono }"><template v-for="(part, index) in tile.lineA.parts" :key="index"><b v-if="part.strong">{{ part.text }}</b><template v-else>{{ part.text }}</template></template></span>
      </div>
      <div class="t-line"><span v-clip-tip>{{ tile.lineB || ' ' }}</span></div>
      <div class="t-line t-delta" :class="tile.delta.cls">
        <AppIcon :name="tile.delta.icon" :size="13" />
        <span v-clip-tip>{{ tile.delta.text }}<template v-if="tile.delta.word"> · <span class="word">{{ tile.delta.word }}</span></template></span>
      </div>
    </template>
    <div class="t-line t-target">
      <svg class="lg-sw" width="18" height="8" aria-hidden="true"><line x1="0" y1="4" x2="18" y2="4" class="g-target" /></svg>
      <span v-clip-tip>{{ tile.target }}</span>
    </div>
    <div class="t-foot">
      <template v-if="state === 'loading'"><span class="sk" style="width: 60%; height: 9px" /></template>
      <template v-else>
        <span v-if="state === 'ready' && tile.partial" class="partial"><AppIcon name="half" :size="10" />{{ text.partial }}</span>
        <span v-clip-tip>{{ state === 'ready' ? tile.foot : tile.source }}</span>
      </template>
    </div>
  </div>
</template>

<style scoped>
.tile { position: relative; display: flex; flex-direction: column; gap: 3px; min-width: 0; padding: 12px 14px 11px 16px; border-radius: 16px; background: var(--glass); -webkit-backdrop-filter: blur(14px) saturate(1.3); backdrop-filter: blur(14px) saturate(1.3); box-shadow: 0 0 0 1px var(--line), inset 0 1px 0 var(--glass-edge), 0 14px 34px -22px color-mix(in srgb, var(--primary-line) 35%, transparent); }
.t-top { display: flex; align-items: center; gap: 4px; min-height: 24px; }
.t-label { flex: 1; min-width: 0; font: 600 12.5px/1.3 var(--font); color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.info { position: relative; display: inline-grid; place-items: center; flex: none; width: 24px; height: 24px; margin-right: -6px; padding: 0; border: 0; border-radius: 8px; background: transparent; color: var(--ink-3); }
.info::after { content: ''; position: absolute; inset: -6px; }
.info:hover, .info[aria-expanded="true"] { background: var(--row-hover); color: var(--teal-ink); }
.info:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.t-value { display: flex; align-items: baseline; gap: 5px; height: 36px; font: 650 30px/36px var(--font); letter-spacing: -.025em; font-variant-numeric: tabular-nums; color: var(--ink); white-space: nowrap; overflow: hidden; }
.t-value small { font: 500 13.5px/1 var(--font); letter-spacing: 0; color: var(--ink-2); }
.t-value.empty { font: 600 15px/36px var(--font); letter-spacing: 0; color: var(--ink-3); }
.t-line { display: flex; align-items: center; gap: 6px; min-width: 0; height: 18px; font-size: 12px; line-height: 18px; color: var(--ink-2); white-space: nowrap; }
.t-line > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.t-line > svg { flex: none; }
.t-pp { font-family: var(--mono); font-size: 11.5px; font-variant-numeric: tabular-nums; }
.t-line b { font-weight: 600; color: var(--ink); }
.t-delta { font-weight: 600; }
.t-delta.better { color: var(--ok); }
.t-delta.worse { color: var(--danger); }
.t-delta.same, .t-delta.none { color: var(--ink-3); font-weight: 500; }
.t-delta .word { font-weight: 700; }
.g-target { stroke: var(--ink-2); stroke-width: 1.5; stroke-dasharray: 5 4; fill: none; }
.lg-sw { display: inline-block; flex: none; }
.t-foot { display: flex; align-items: center; gap: 6px; min-width: 0; height: 26px; margin-top: auto; padding-top: 7px; border-top: 1px solid var(--line); font-size: 11.5px; color: var(--ink-3); white-space: nowrap; }
.t-foot > span:last-child { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.partial { display: inline-flex; align-items: center; gap: 4px; flex: none; height: 18px; padding: 0 6px; border-radius: 6px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); font: 600 10.5px/1 var(--font); white-space: nowrap; }
.mini-n { display: inline-flex; flex: none; gap: 3px; }
.mini-n i { width: 10px; height: 10px; border-radius: 3px; }
.mini-n i.ok { background: var(--ok); }
.mini-n i.bad { background: var(--danger); }
.mini-n i.none { box-shadow: inset 0 0 0 1px var(--line-2); }
.mini-n i.nodata { background: repeating-linear-gradient(-45deg, transparent, transparent 2px, var(--line-2) 2px, var(--line-2) 3px); }
.sk { display: block; border-radius: 6px; background: var(--skeleton); }
@media (prefers-reduced-motion: no-preference) {
  .sk { background: linear-gradient(90deg, var(--skeleton) 0%, var(--skeleton-hi) 50%, var(--skeleton) 100%) 0 0 / 200% 100%; animation: dl-sk 1.4s ease-in-out infinite; }
}
@keyframes dl-sk { to { background-position: -200% 0; } }
@container delivery (max-width: 640px) {
  .tile { padding: 10px 10px 9px 12px; }
  .t-value { font-size: 24px; }
  .info { width: 44px; height: 44px; margin: -10px -10px -10px 0; }
  .info::after { inset: 0; }
}
</style>
