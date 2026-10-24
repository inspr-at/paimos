<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import ExpertTile from './ExpertTile.vue'
import SimpleTile from './SimpleTile.vue'
import ReviewRoundsPanel from './ReviewRoundsPanel.vue'
import { vClipTip } from '../../directives/clipTip'
import type { DeliveryLanguage } from '../../lib/delivery'
import type { WindowDays } from '../../lib/deliveryNumbers'
import { deliveryText } from '../../lib/deliveryNumbersText'
import { simpleText } from '../../lib/deliverySimpleText'
import { reviewRoundView, reviewText, type ReviewRoundNumbers } from '../../lib/deliveryReviewRounds'
import type { TicketTileModel } from '../../lib/deliveryTickets'

const props = defineProps<{ data: ReviewRoundNumbers | null; state: 'loading' | 'error' | 'ready'; level: 'simple' | 'expert'; window: WindowDays; lang: DeliveryLanguage; routeKey: string; infoOpen?: boolean; definitionId?: string }>()
const emit = defineEmits<{ retry: []; info: [tile: TicketTileModel, anchor: HTMLElement, pin: boolean]; 'info-leave': [] }>()
const view = computed(() => reviewRoundView(props.data, props.window, props.lang, props.state))
const t = computed(() => reviewText(props.lang))
const open = ref<{ anchor: HTMLElement; data: ReviewRoundNumbers; keyboard: boolean } | null>(null)
function toggle(anchor: HTMLElement, keyboard: boolean) {
  if (open.value) { close(true); return }
  if (props.state !== 'ready' || !props.data) return
  open.value = { anchor, data: props.data, keyboard }
}
function close(restore: boolean) {
  const anchor = open.value?.anchor
  open.value = null
  if (restore && anchor?.isConnected) anchor.focus({ preventScroll: true })
}
function clipped(event: MouseEvent) {
  const target = event.target as Element
  if (target.closest('button, a[href]') || !target.closest('[data-clip-tip]')) return
  const anchor = (event.currentTarget as HTMLElement).querySelector<HTMLButtonElement>('.rr-open')
  if (anchor) toggle(anchor, event.detail === 0)
}
watch([() => props.window, () => props.level, () => props.lang, () => props.routeKey, () => props.state], () => close(false))
</script>

<template>
  <SimpleTile v-if="level === 'simple'" :tile="view.simple" :state="state" :text="simpleText(lang)" minimal data-testid="review-rounds-simple" />
  <ExpertTile v-else class="rr-tile" :tile="view.expert" :state="state" :text="deliveryText(lang)" :info-open="!!infoOpen" :definition-id="definitionId" hide-detail data-testid="review-rounds-tile" @click="clipped"
    @info="(anchor, pin) => emit('info', view.expert, anchor, pin)" @info-leave="emit('info-leave')">
    <template #trigger><button type="button" class="rr-open" :aria-label="t.openPanel" aria-haspopup="dialog" :aria-expanded="!!open" :aria-disabled="state !== 'ready' || !data || undefined" @click="toggle($event.currentTarget as HTMLElement, $event.detail === 0)" /></template>
    <template #value-companion><small v-if="state === 'ready' && data?.current.tickets" class="rr-gate">{{ view.gate }}</small></template>
    <template #target><span v-clip-tip>{{ t.goal }}</span></template>
    <template #foot><button type="button" class="rr-action" :aria-label="state === 'error' ? t.retry : t.openPanel" :data-tip="state === 'error' ? t.retry : t.openPanel" :aria-disabled="state === 'loading' || undefined" @click="state === 'error' ? emit('retry') : toggle($event.currentTarget as HTMLElement, $event.detail === 0)"><AppIcon :name="state === 'error' ? 'refresh' : 'list'" :size="14" /></button></template>
  </ExpertTile>
  <ReviewRoundsPanel v-if="open && level === 'expert'" :data="open.data" :anchor="open.anchor" :route-key="routeKey" :lang="lang" :keyboard="open.keyboard" @close="close" />
</template>

<style scoped>
.rr-tile { position: relative; }
.rr-open { position: absolute; inset: 0; z-index: 0; width: 100%; height: 100%; padding: 0; border: 0; border-radius: inherit; background: transparent; cursor: pointer; }
.rr-open:hover:not([aria-disabled="true"]) { background: var(--row-hover); }
.rr-open:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 2px; }
.rr-open[aria-disabled="true"] { cursor: default; }
.rr-tile :deep(.t-top), .rr-tile :deep(.t-value), .rr-tile :deep(.t-line), .rr-tile :deep(.t-foot) { pointer-events: none; }
.rr-tile :deep(.info), .rr-tile :deep([data-clip-tip]), .rr-action { position: relative; z-index: 1; pointer-events: auto; }
.rr-tile :deep(.t-value) { min-height: 40px; height: auto; flex-wrap: wrap; gap: 0 6px; }
.rr-tile :deep(.t-main) { display: inline-block; min-width: 5ch; text-align: start; }
.rr-gate { display: inline-block; min-width: 10ch; font: 500 13px/1.3 var(--font); color: var(--ink-2); letter-spacing: 0; font-variant-numeric: tabular-nums; text-align: start; }
.rr-tile :deep(.t-foot) { height: auto; min-height: 52px; }
.rr-tile :deep(.t-line) { font-variant-numeric: tabular-nums; }
.rr-action { display: inline-grid; place-items: center; flex: none; width: 44px; height: 44px; margin-left: auto; padding: 0; border: 0; border-radius: var(--radius-row); background: transparent; color: var(--teal-ink); font: inherit; }
.rr-action:hover { background: var(--row-hover); }
.rr-action:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 2px; }
</style>
