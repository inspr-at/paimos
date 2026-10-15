<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One step of a ticket's way (design r4b §3.4; AEON-1359), in the SimpleTile language without a verdict (no Arion
// target yet) and without a chart: name and Learn, the typical time, the slow tail, a count, the change against the
// window before, one plain sentence and the source. Every line keeps its height in every state.
import AppIcon from '../AppIcon.vue'
import LearnPopover from './LearnPopover.vue'
import { vClipTip } from '../../directives/clipTip'
import type { StepTile } from '../../lib/deliveryTickets'
import type { SimpleText } from '../../lib/deliverySimpleText'

defineProps<{ tile: StepTile; state: 'loading' | 'error' | 'ready'; busy: boolean; foot: string; partial: boolean; text: SimpleText }>()
</script>

<template>
  <article class="s-tile" role="listitem" :aria-labelledby="`tk-${tile.key}`" :aria-busy="state === 'loading' || busy || undefined" :data-step="tile.key">
    <div class="s-top">
      <h4 :id="`tk-${tile.key}`" class="s-name">{{ tile.name }}</h4>
      <LearnPopover :id="`tk-${tile.key}`" :name="tile.name" :body="tile.learn.body" expert="" target="" :facts="tile.learn.facts"
        :labels="{ learn: text.learn, expertName: text.expertName, arion: text.arion }" />
    </div>
    <div class="figures" :class="{ busy }">
      <template v-if="state === 'loading'">
        <div class="s-valrow"><span class="sk" style="width: 45%; height: 30px" /></div>
        <p class="tk-slow"><span class="sk" style="width: 60%; height: 12px" /></p>
        <p class="s-count"><span class="sk" style="width: 40%; height: 12px" /></p>
        <p class="s-trend"><span class="sk" style="width: 70%; height: 12px" /></p>
        <p class="s-say"><span class="sk" style="width: 90%; height: 12px" /><span class="sk" style="width: 60%; height: 12px" /></p>
      </template>
      <template v-else>
        <div class="s-valrow">
          <span v-if="state === 'ready' && tile.value" class="s-val">{{ tile.value[0] }}<small>{{ tile.value[1] }}</small></span>
          <span v-else class="s-val empty">{{ state === 'error' ? text.notLoaded : text.nodataV }}</span>
          <span class="s-dir"><AppIcon name="arrow-down" :size="13" />{{ text.lower }}</span>
        </div>
        <p class="tk-slow"><span v-if="state === 'ready' && tile.slow" v-clip-tip>{{ tile.slow.before }}<b>{{ tile.slow.value }}</b>{{ tile.slow.after }}</span></p>
        <p class="s-count" :class="{ rework: tile.rework }"><template v-if="state === 'ready'"><AppIcon v-if="tile.rework" name="rollback" :size="13" /><span v-clip-tip>{{ tile.count.before }}<b>{{ tile.count.value }}</b>{{ tile.count.after }}</span></template></p>
        <p class="s-trend" :class="state === 'ready' ? tile.trend.cls : 'none'"><AppIcon :name="state === 'ready' ? tile.trend.icon : 'minus'" :size="13" /><span v-clip-tip>{{ state === 'ready' ? tile.trend.text : text.notLoaded }}</span></p>
        <p class="s-say">{{ state === 'ready' ? tile.say : '' }}</p>
      </template>
    </div>
    <div class="s-foot">
      <span v-if="state === 'loading'" class="sk" style="width: 55%; height: 9px" />
      <template v-else>
        <span v-if="state === 'ready' && partial" class="partial"><AppIcon name="half" :size="10" />{{ text.partial }}</span>
        <span v-clip-tip>{{ foot }}</span>
      </template>
    </div>
  </article>
</template>

<style scoped>
.s-tile { display: flex; flex-direction: column; gap: 6px; min-width: 0; padding: 14px 16px 11px; border-radius: 16px; background: var(--glass); -webkit-backdrop-filter: blur(14px) saturate(1.3); backdrop-filter: blur(14px) saturate(1.3); box-shadow: 0 0 0 1px var(--line), inset 0 1px 0 var(--glass-edge), 0 14px 34px -22px color-mix(in srgb, var(--primary-line) 35%, transparent); }
.s-top { display: flex; align-items: flex-start; gap: 8px; min-height: 26px; }
.s-name { flex: 1; min-width: 0; margin: 3px 0 0; font: 600 13.5px/1.35 var(--font); color: var(--ink); overflow-wrap: anywhere; }
.figures { display: flex; flex-direction: column; gap: 6px; }
/* While another window or filter is being counted, the previous figures stay, dimmed, and switch in one render. */
.figures.busy { opacity: .5; }
.s-valrow { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 0 8px; min-height: 40px; }
.s-val { font: 650 32px/40px var(--font); letter-spacing: -.025em; font-variant-numeric: tabular-nums; color: var(--ink); white-space: nowrap; }
.s-val small { margin-left: 4px; font: 500 14px/1 var(--font); letter-spacing: 0; color: var(--ink-2); }
.s-val.empty { font: 600 16px/40px var(--font); letter-spacing: 0; color: var(--ink-3); }
.s-dir { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; color: var(--ink-2); white-space: nowrap; }
.tk-slow, .s-count { display: flex; align-items: center; gap: 6px; height: 20px; margin: 0; color: var(--ink-2); white-space: nowrap; overflow: hidden; }
.tk-slow { font-size: 13px; }
.s-count { font-size: 12.5px; }
.tk-slow > span, .s-count > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.tk-slow b { font-weight: 650; color: var(--ink); font-variant-numeric: tabular-nums; }
.s-count b { font-weight: 600; color: var(--ink); font-variant-numeric: tabular-nums; }
.s-count.rework :deep(svg) { flex: none; color: var(--ink-3); }
.s-trend { display: flex; align-items: center; gap: 6px; height: 20px; margin: 0; font-size: 12.5px; font-weight: 600; color: var(--ink-2); white-space: nowrap; overflow: hidden; }
.s-trend > svg { flex: none; }
.s-trend > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.s-trend.better { color: var(--ok); }
.s-trend.worse { color: var(--danger); }
.s-trend.none, .s-trend.same { color: var(--ink-3); font-weight: 500; }
.s-say { display: flex; flex-direction: column; gap: 6px; min-height: calc(13px * 1.45 * 3); margin: 0; font-size: 13px; line-height: 1.45; color: var(--ink-2); }
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
  .s-val { font-size: 28px; }
  .s-say { min-height: calc(13px * 1.45 * 4); }
}
</style>
