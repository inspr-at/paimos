<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Delivery › Numbers › Building it (AEON-1493, Tick's approved design): four numbers from the first build to a change
// that passes its checks, after Tickets and before "Making a change ready". Simple uses the shipped SimpleTile, Expert
// the shipped ExpertTile with the daily TrendChart; on a phone Expert stacks Simple's essentials with one p50/p90
// line. The whole tile opens the worst five of the window: hover tints it, open adds a thin teal ring, the keyboard
// shows the focus ring. Learn, info and the chart keep their own actions. The head carries "N of 3 on target", the
// no-data sentence or "Not loaded · Retry" in one line, so nothing below moves between states (AEON-541).
import { computed, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import ExpertTile from './ExpertTile.vue'
import SimpleTile from './SimpleTile.vue'
import TrendChart from './TrendChart.vue'
import WorstPanel from './WorstPanel.vue'
import { vClipTip } from '../../directives/clipTip'
import type { DeliveryLanguage } from '../../lib/delivery'
import type { BuildingKey, BuildingTileView, BuildingView } from '../../lib/deliveryBuilding'
import { buildingText } from '../../lib/deliveryBuildingText'
import type { WindowDays } from '../../lib/deliveryNumbers'
import { deliveryText, fill } from '../../lib/deliveryNumbersText'
import { simpleText } from '../../lib/deliverySimpleText'
import type { TicketTileModel } from '../../lib/deliveryTickets'

const props = defineProps<{ view: BuildingView; state: 'loading' | 'error' | 'ready'; busy: boolean; level: 'simple' | 'expert'; window: WindowDays; lang: DeliveryLanguage; routeKey: string; openTile?: string | null }>()
const emit = defineEmits<{ retry: []; info: [tile: TicketTileModel, anchor: HTMLElement, pin: boolean]; 'info-leave': [] }>()
const t = computed(() => buildingText(props.lang))
const words = computed(() => simpleText(props.lang))
const page = computed(() => deliveryText(props.lang))
const uid = useId()
const ready = computed(() => props.state === 'ready')
// The worst five open once the window has items; loading, a failed read and no data leave the tiles closed.
const canOpen = computed(() => ready.value && props.view.head.kind === 'ready')

// ---------- Phones: Expert stacks Simple's essentials, decided by the section's own width ----------
const root = ref<HTMLElement>()
const narrow = ref(false)
let resize: ResizeObserver | null = null
onMounted(() => {
  if (!root.value) return
  narrow.value = root.value.clientWidth > 0 && root.value.clientWidth <= 640
  resize = new ResizeObserver(entries => { const width = Math.round(entries[0]?.contentRect.width ?? 0); if (width) narrow.value = width <= 640 })
  resize.observe(root.value)
})
onBeforeUnmount(() => resize?.disconnect())

// ---------- Worst in this window ----------
// The panel keeps the rows it opened with: a refresh landing meanwhile never reorders them under the pointer.
const open = ref<{ key: BuildingKey; anchor: HTMLElement; focusFirst: boolean; tile: BuildingTileView } | null>(null)
function toggle(tile: BuildingTileView, event: MouseEvent) {
  const anchor = event.currentTarget as HTMLElement
  if (open.value?.key === tile.key) { close(true); return }
  if (!canOpen.value) return
  // Enter or Space arrive as a click without a pointer: the first row takes the focus.
  open.value = { key: tile.key, anchor, focusFirst: event.detail === 0, tile }
}
function close(restore: boolean) {
  const anchor = open.value?.anchor
  open.value = null
  if (restore && anchor?.isConnected) anchor.focus({ preventScroll: true })
}
// The items behind an open panel belong to this window, level and answer: another one closes it.
watch([() => props.window, () => props.level, () => props.lang, () => props.state, () => props.busy, narrow], () => { if (open.value) close(false) })
const label = (tile: BuildingTileView) => fill(t.value.open, { n: tile.simple.name })
</script>

<template>
  <div ref="root" class="bd-wrap">
  <section v-if="level === 'simple' || narrow" class="s-sec bd" :class="{ 'bd-x': level === 'expert' }" :aria-labelledby="`${uid}-head`" data-testid="delivery-building">
    <div v-if="level === 'simple'" class="s-sec-head">
      <h3 :id="`${uid}-head`">{{ t.title }}</h3>
      <span v-if="view.head.kind === 'error'" class="cnt bd-cnt err" role="alert"><span v-clip-tip>{{ view.head.text }}</span><button type="button" class="retry" @click="emit('retry')">{{ t.retry }}</button></span>
      <span v-else v-clip-tip class="cnt bd-cnt" :class="{ quiet: view.head.kind === 'nodata' }" :role="view.head.kind === 'nodata' ? 'status' : undefined" :aria-hidden="view.head.kind === 'loading' || undefined" data-testid="delivery-building-head">{{ view.head.text }}</span>
      <p>{{ t.sub }}</p>
    </div>
    <div v-else class="tk-cap-row">
      <p :id="`${uid}-head`" v-clip-tip class="tiles-cap">{{ fill(t.cap, { w: window }) }}</p>
      <span v-if="view.head.kind === 'error'" class="bd-cnt err" role="alert"><span v-clip-tip>{{ view.head.text }}</span><button type="button" class="retry" @click="emit('retry')">{{ t.retry }}</button></span>
      <span v-else class="bd-cnt" :class="{ quiet: view.head.kind === 'nodata' }" :role="view.head.kind === 'nodata' ? 'status' : undefined" :aria-hidden="view.head.kind === 'loading' || undefined" data-testid="delivery-building-head">{{ view.head.text }}</span>
    </div>
    <div class="s-grid" :class="{ busy }" role="list">
      <SimpleTile v-for="tile in view.tiles" :key="tile.key" class="bd-tile" :tile="tile.simple" :state="state" :text="words" :data-building="tile.key">
        <template #trigger>
          <button type="button" class="bd-open" :aria-label="label(tile)" aria-haspopup="dialog" :aria-expanded="open?.key === tile.key" :disabled="!canOpen" data-testid="delivery-building-open" @click="toggle(tile, $event)" />
        </template>
        <template v-if="level === 'expert'" #detail>
          <p class="s-pp"><span v-if="state === 'loading'" class="sk" style="width: 70%; height: 10px" /><span v-else v-clip-tip><template v-for="(part, index) in tile.pp" :key="index"><b v-if="part.strong">{{ part.text }}</b><template v-else>{{ part.text }}</template></template></span></p>
        </template>
        <template #foot><span class="bd-more" :class="{ off: !canOpen }" aria-hidden="true"><AppIcon name="list" :size="12" />{{ t.more }}</span></template>
      </SimpleTile>
    </div>
  </section>

  <section v-else class="bd-x" :aria-labelledby="`${uid}-cap`" data-testid="delivery-building">
    <div class="tk-cap-row">
      <p :id="`${uid}-cap`" v-clip-tip class="tiles-cap">{{ fill(t.cap, { w: window }) }}</p>
      <span v-if="view.head.kind === 'error'" class="bd-cnt err" role="alert"><span v-clip-tip>{{ view.head.text }}</span><button type="button" class="retry" @click="emit('retry')">{{ t.retry }}</button></span>
      <span v-else class="bd-cnt" :class="{ quiet: view.head.kind === 'nodata' }" :role="view.head.kind === 'nodata' ? 'status' : undefined" :aria-hidden="view.head.kind === 'loading' || undefined" data-testid="delivery-building-head">{{ view.head.text }}</span>
    </div>
    <div class="tiles bd" :class="{ busy }" role="list">
      <ExpertTile v-for="tile in view.tiles" :key="tile.key" class="bd-tile" :tile="tile.expert" :state="state" :text="page" :info-open="openTile === tile.expert.def.key" :data-building="tile.key"
        @info="(anchor, pin) => emit('info', tile.expert, anchor, pin)" @info-leave="emit('info-leave')">
        <template #trigger>
          <button type="button" class="bd-open" :aria-label="label(tile)" aria-haspopup="dialog" :aria-expanded="open?.key === tile.key" :disabled="!canOpen" data-testid="delivery-building-open" @click="toggle(tile, $event)" />
        </template>
        <template #extra><TrendChart :model="tile.chart" :state="state" :text="page" small bare /></template>
        <template #foot><span class="bd-more" :class="{ off: !canOpen }" aria-hidden="true"><AppIcon name="list" :size="12" />{{ t.more }}</span></template>
      </ExpertTile>
    </div>
  </section>

  </div>

  <WorstPanel v-if="open" :key="open.key" :tile="open.tile" :anchor="open.anchor" :route-key="routeKey" :text="t" :focus-first="open.focusFirst" @close="close" />
</template>

<style scoped>
.bd-wrap { min-width: 0; }
.s-sec { margin-top: 24px; }
.s-sec-head { display: flex; align-items: baseline; flex-wrap: wrap; gap: 2px 12px; }
.s-sec-head h3 { margin: 0; font: 650 15px/1.3 var(--font); color: var(--ink); }
.s-sec-head p { flex-basis: 100%; margin: 0; font-size: 12.5px; color: var(--ink-2); }
/* The count, the no-data sentence and the failed read share one line of fixed height. */
.bd-cnt { display: inline-flex; align-items: baseline; gap: 6px; flex: 0 1 auto; min-width: 0; min-height: 1lh; font-size: 12.5px; font-weight: 600; color: var(--ink-2); font-variant-numeric: tabular-nums; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.bd-cnt > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.bd-cnt.quiet { font-weight: 500; }
.bd-cnt.err { color: var(--danger); }
.retry { position: relative; display: inline; height: auto; margin: 0; padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; text-decoration: underline; text-underline-offset: 2px; }
.retry:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 2px; border-radius: 3px; }
@media (pointer: coarse) { .retry::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: 44px; transform: translate(-50%, -50%); } }
.s-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin-top: 10px; }
.bd-x { margin-top: 22px; }
.tk-cap-row { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; min-width: 0; }
.tk-cap-row .bd-cnt { flex: none; font-size: 12px; }
.tiles-cap { min-width: 0; margin: 0; font: 500 10.5px/1.5 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.tiles.bd { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin-top: 8px; }
.busy { opacity: .5; }
/* The whole tile opens the worst items; Learn, info, the chart and Retry stay above it with their own actions. */
.bd-tile { position: relative; }
.bd-open { position: absolute; inset: 0; z-index: 0; width: 100%; height: 100%; margin: 0; padding: 0; border: 0; border-radius: inherit; background: transparent; cursor: pointer; }
.bd-open:hover:not(:disabled) { background: var(--row-hover); }
.bd-open:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.bd-open[aria-expanded="true"] { background: var(--row-hover); box-shadow: 0 0 0 1.5px color-mix(in srgb, var(--teal) 60%, transparent); }
.bd-open:disabled { cursor: default; }
.bd-tile :deep(.learn), .bd-tile :deep(.info), .bd-tile :deep(.plot) { z-index: 1; }
.bd-tile :deep(.plot) { position: relative; }
/* Only the plot answers the pointer; the rest of the embedded chart opens the worst items like the tile around it. */
.bd-tile :deep(.chart-card.bare) { position: static; margin-top: 8px; }
.bd-tile :deep(.c-head) { min-height: 20px; }
.bd-tile :deep(.c-head h4) { font-size: 12px; font-weight: 600; color: var(--ink-2); }
.bd-tile :deep(.s-spark) { pointer-events: none; }
.bd-tile :deep(.src-t) { flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.bd-more { display: inline-flex; align-items: center; gap: 4px; flex: none; font-weight: 600; color: var(--ink-3); }
.bd-more.off { visibility: hidden; }
.bd-open:hover:not(:disabled) ~ * .bd-more, .bd-open[aria-expanded="true"] ~ * .bd-more { color: var(--teal-ink); }
.s-pp { display: flex; align-items: center; height: 18px; margin: 0; font: 500 11.5px/18px var(--mono); font-variant-numeric: tabular-nums; color: var(--ink-2); white-space: nowrap; overflow: hidden; }
.s-pp > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.s-pp b { font-weight: 600; color: var(--ink); }
.sk { display: block; border-radius: 6px; background: var(--skeleton); }
@media (prefers-reduced-motion: no-preference) {
  .sk { background: linear-gradient(90deg, var(--skeleton) 0%, var(--skeleton-hi) 50%, var(--skeleton) 100%) 0 0 / 200% 100%; animation: dl-sk 1.4s ease-in-out infinite; }
}
@keyframes dl-sk { to { background-position: -200% 0; } }
@container delivery (max-width: 1100px) { .s-grid, .tiles.bd { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@container delivery (max-width: 640px) {
  .s-grid { grid-template-columns: minmax(0, 1fr); gap: 8px; }
  .tk-cap-row { flex-wrap: wrap; gap: 2px 12px; }
  /* Building it on a phone: the essentials only — value, verdict and one sentence; the chart and the trend stay on wide screens. */
  .bd-tile :deep(.s-spark), .bd-tile :deep(.s-trend) { display: none; }
  .bd-tile :deep(.s-say) { min-height: calc(13px * 1.45 * 2); }
  .bd-tile :deep(.s-foot) { align-items: center; height: 28px; min-height: 0; line-height: inherit; white-space: nowrap; }
  .bd-tile :deep(.s-foot > .src-t) { display: block; }
}
</style>
