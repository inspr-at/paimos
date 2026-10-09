<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One Simple number (AEON-994 draft 5): plain name and Learn, the big value with
// which way is better, the verdict against the Arion target (shape + word), the
// small chart, the change against the window before, one plain sentence with
// Arion's wish, and the source or "Partial". Lines keep their height in every state.
import { ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import LearnPopover from './LearnPopover.vue'
import TileSpark from './TileSpark.vue'
import { vClipTip } from '../../directives/clipTip'
import type { SimpleTileModel } from '../../lib/deliverySimple'
import type { SimpleText } from '../../lib/deliverySimpleText'

const props = defineProps<{ tile: SimpleTileModel; state: 'loading' | 'error' | 'ready'; text: SimpleText }>()
const ICON = { on: 'v-on', close: 'v-close', far: 'v-far', none: 'v-none' } as const
// A shorter error sentence must not shrink the tile, or the next row's Learn button jumps.
const root = ref<HTMLElement>()
const hold = ref(0)
watch(() => props.state, (next, prev) => {
  if (prev === 'ready' && next !== 'ready') hold.value = root.value?.getBoundingClientRect().height ?? 0
  else if (next === 'ready') hold.value = 0
}, { flush: 'pre' })
</script>

<template>
  <article ref="root" class="s-tile" role="listitem" :aria-labelledby="`sn-${tile.key}`" :aria-busy="state === 'loading' || undefined" :data-key="tile.key" :style="hold > 0 ? { minHeight: `${hold}px` } : undefined">
    <div class="s-top">
      <h4 :id="`sn-${tile.key}`" class="s-name">{{ tile.name }}</h4>
      <LearnPopover :id="tile.key" :name="tile.name" :body="tile.learn.body" :expert="tile.learn.expert" :target="tile.learn.target"
        :labels="{ learn: text.learn, expertName: text.expertName, arion: text.arion }" />
    </div>
    <template v-if="state === 'loading'">
      <div class="s-valrow"><span class="sk" style="width: 45%; height: 30px" /></div>
      <div class="s-verdict"><span class="sk" style="width: 65%; height: 14px" /></div>
      <TileSpark :model="tile.spark" :text="text" />
      <div class="s-trend"><span class="sk" style="width: 70%; height: 12px" /></div>
      <div class="s-say"><span class="sk" style="width: 90%; height: 12px" /><span class="sk" style="width: 60%; height: 12px" /></div>
    </template>
    <template v-else>
      <div class="s-valrow">
        <span v-if="tile.value" class="s-val"><template v-for="(part, index) in tile.value" :key="index">{{ index ? ' ' : '' }}{{ part.text }}<small>{{ part.unit }}</small></template></span>
        <span v-else class="s-val empty">{{ tile.empty }}</span>
        <span class="s-dir"><AppIcon :name="tile.up ? 'arrow-up' : 'arrow-down'" :size="13" />{{ tile.up ? text.higher : text.lower }}</span>
      </div>
      <p class="s-verdict" :class="tile.verdict.level" data-testid="verdict">
        <AppIcon :name="ICON[tile.verdict.level]" :size="16" />
        <span v-clip-tip><b>{{ tile.verdict.word }}</b><span v-if="tile.verdict.gap" class="gap"> · {{ tile.verdict.gap }}</span></span>
      </p>
      <TileSpark :model="tile.spark" :text="text" />
      <p class="s-trend" :class="tile.trend.cls"><AppIcon :name="tile.trend.icon" :size="13" /><span v-clip-tip>{{ tile.trend.text }}</span></p>
      <p class="s-say">{{ tile.say }}</p>
    </template>
    <div class="s-foot">
      <span v-if="state === 'loading'" class="sk" style="width: 55%; height: 9px" />
      <template v-else>
        <span v-if="tile.partial && tile.foot" class="partial"><AppIcon name="half" :size="10" />{{ text.partial }}</span>
        <span v-clip-tip>{{ tile.foot }}</span>
      </template>
    </div>
  </article>
</template>

<style scoped>
.s-tile { display: flex; flex-direction: column; gap: 6px; min-width: 0; padding: 14px 16px 11px; border-radius: 16px; background: var(--glass); -webkit-backdrop-filter: blur(14px) saturate(1.3); backdrop-filter: blur(14px) saturate(1.3); box-shadow: 0 0 0 1px var(--line), inset 0 1px 0 var(--glass-edge), 0 14px 34px -22px color-mix(in srgb, var(--primary-line) 35%, transparent); }
.s-top { display: flex; align-items: flex-start; gap: 8px; min-height: 26px; }
.s-name { flex: 1; min-width: 0; margin: 3px 0 0; font: 600 13.5px/1.35 var(--font); color: var(--ink); overflow-wrap: anywhere; }
.s-valrow { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 0 8px; min-height: 40px; }
.s-val { font: 650 32px/40px var(--font); letter-spacing: -.025em; font-variant-numeric: tabular-nums; color: var(--ink); white-space: nowrap; }
.s-val small { margin-left: 4px; font: 500 14px/1 var(--font); letter-spacing: 0; color: var(--ink-2); }
.s-val.empty { font: 600 16px/40px var(--font); letter-spacing: 0; color: var(--ink-3); }
.s-dir { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; color: var(--ink-2); white-space: nowrap; }
.s-verdict { display: flex; align-items: center; gap: 7px; height: 20px; margin: 0; font-size: 13px; color: var(--ink); white-space: nowrap; overflow: hidden; }
.s-verdict > svg { flex: none; }
.s-verdict > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.s-verdict b { font-weight: 650; }
.s-verdict .gap { color: var(--ink-2); }
.s-verdict.on > svg { color: var(--ok); }
.s-verdict.close > svg { color: var(--gold); }
.s-verdict.far > svg { color: var(--danger); }
.s-verdict.none > svg { color: var(--ink-3); }
.s-trend { display: flex; align-items: center; gap: 6px; height: 20px; margin: 0; font-size: 12.5px; font-weight: 600; color: var(--ink-2); white-space: nowrap; overflow: hidden; }
.s-trend > svg { flex: none; }
.s-trend > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.s-trend.better { color: var(--ok); }
.s-trend.worse { color: var(--danger); }
.s-trend.none { color: var(--ink-3); font-weight: 500; }
/* Five lines are reserved, so a window switch rarely moves the tile; a longer sentence grows the tile, never clipped. */
.s-say { display: flex; flex-direction: column; gap: 6px; min-height: calc(13px * 1.45 * 5); margin: 0; font-size: 13px; line-height: 1.45; color: var(--ink-2); }
.s-foot { display: flex; align-items: center; gap: 6px; min-width: 0; height: 28px; margin-top: auto; padding-top: 8px; border-top: 1px solid var(--line); font-size: 11.5px; color: var(--ink-3); white-space: nowrap; }
.s-foot > span:last-child { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.partial { display: inline-flex; align-items: center; gap: 4px; flex: none; height: 18px; padding: 0 6px; border-radius: 6px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); font: 600 10.5px/1 var(--font); white-space: nowrap; }
.sk { display: block; border-radius: 6px; background: var(--skeleton); }
@media (prefers-reduced-motion: no-preference) {
  .sk { background: linear-gradient(90deg, var(--skeleton) 0%, var(--skeleton-hi) 50%, var(--skeleton) 100%) 0 0 / 200% 100%; animation: dl-sk 1.4s ease-in-out infinite; }
}
@keyframes dl-sk { to { background-position: -200% 0; } }
@container delivery (max-width: 640px) {
  .s-tile { padding: 12px 12px 10px 14px; }
  /* Phones: the source line wraps into two reserved lines instead of hiding its end behind a 17 px tip target (AEON-1007). */
  .s-foot { align-items: flex-start; height: auto; min-height: calc(16px * 2 + 8px); line-height: 16px; white-space: normal; }
  .s-foot > span:last-child { overflow: visible; }
  .s-val { font-size: 28px; }
}
</style>
