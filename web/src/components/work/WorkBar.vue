<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The header work bar (AEON-1358, Tick's approved design r4b §3.1): the view's work in hours, leaves only, Closed out.
// Scope and total on top, the bar drawn to scale (Completed solid, Done, not live outlined, In progress the sliding
// 45° stripes on hover, Open the empty track), and the line with each group's hours. Hover (380 ms), keyboard focus or
// a tap shows the counts. While a new scope is counted, the previous label, total, bar and line stay together, dimmed;
// the new figures replace all four in one render. Value slots are fixed from the whole project, so nothing shifts.
import { computed, useId } from 'vue'
import { closeFigureTip, figureTip, FIGURE_TIP_ID, hoverFigureTip, openFigureTip, cancelFigureTip } from '../../lib/figureTip'
import { fmtInt, scopeLabel, workTipFoot, GROUP_FILL, type WorkHeader, type WorkSlots } from '../../lib/workFigures'
import { fill, workFiguresText } from '../../lib/workFiguresText'
import { vClipTip } from '../../directives/clipTip'
import FigureTip from './FigureTip.vue'

const props = defineProps<{
  // The landed figures of the shown scope (the previous scope's while busy); null before anything landed.
  header: WorkHeader | null
  busy: boolean
  // The current scope could not be counted: the message replaces the figures in place.
  failure: string | null
  // The current scope before its figures land: narrowed by search, a status filter or a saved view.
  narrowed: boolean
  slots: WorkSlots | null
  // Until the whole project is counted, its work items size the "N without estimate" slot.
  fallbackLeaves: number
  density: 'comfortable' | 'compact'
  // Hide closed is on: its label, for the hover's last line.
  hideLabel: string | null
}>()
const emit = defineEmits<{ retry: [] }>()
const text = workFiguresText()
const owner = 'work'
const srId = `wk-sr-${useId()}`

// The figures on screen: none while the current scope failed (honest), the previous ones while counting.
const shown = computed(() => props.failure ? null : props.header)
const label = computed(() => shown.value?.scope ?? { narrowed: props.narrowed, ...scopeLabel(props.narrowed, null, null, text) })
const groups = computed(() => shown.value?.groups ?? (['c', 'd', 'p', 'o'] as const).map(id => ({
  id, name: id === 'c' ? text.groups.completed : id === 'd' ? text.groups.doneNotLive : id === 'p' ? text.groups.inProgress : text.groups.open,
  shown: '–', fill: GROUP_FILL[id], hours: 0, tickets: 0, hoursShare: 0, ticketShare: 0, statuses: [] as [string, number][],
})))
const segments = computed(() => shown.value?.segments ?? [])
const slotStyle = computed(() => ({
  '--wk-digits': `${props.slots?.digits ?? 4}ch`,
  '--wk-total': `${props.slots?.total ?? 5}ch`,
}))
const leafZeros = computed(() => '0'.repeat(props.slots?.leaves ?? Math.max(1, String(props.fallbackLeaves).length)))
const open = computed(() => !!figureTip.anchor && figureTip.owner === owner)

// The tip: the label the header shows (the previous one while counting), the counts per group, one fact per footer line.
const tipFigures = computed(() => props.busy || props.failure ? null : props.header)
const tipFoot = computed(() => props.header ? workTipFoot(props.header, props.hideLabel, text) : [])
const footLines = computed(() => props.header ? tipFoot.value.length : 3 + (props.hideLabel ? 1 : 0))

const mouse = (event: PointerEvent) => event.pointerType === 'mouse'
function enter(event: PointerEvent) { if (mouse(event)) hoverFigureTip(event.currentTarget as HTMLElement, owner, 'below', owner) }
function leave(event: PointerEvent) {
  if (!mouse(event)) return
  cancelFigureTip()
  if (open.value && !figureTip.pinned) closeFigureTip(event.currentTarget as HTMLElement)
}
function focus(event: FocusEvent) { const bar = event.currentTarget as HTMLElement; if (bar.matches(':focus-visible')) openFigureTip(bar, owner, 'below') }
function blur(event: FocusEvent) { if (open.value && !figureTip.pinned) closeFigureTip(event.currentTarget as HTMLElement) }
// A tap (and Enter or Space) pins the counts open; the next one closes them.
function click(event: MouseEvent) {
  const bar = event.currentTarget as HTMLElement
  if (open.value && figureTip.pinned) closeFigureTip(bar)
  else openFigureTip(bar, owner, 'below', true)
}
</script>

<template>
  <div class="work-bar" :class="[`d-${density}`, { narrowed: label.narrowed, failed: !!failure }]" :aria-busy="busy ? 'true' : 'false'" :style="slotStyle">
    <div class="wk-head">
      <span class="wk-scope">
        <span class="wk-long" v-clip-tip><b>{{ label.short }}</b><template v-if="label.label !== label.short">{{ label.label.slice(label.short.length) }}</template></span>
        <span class="wk-short"><b>{{ label.short }}</b></span>
        <span class="wk-probe" aria-hidden="true"><b>{{ text.wholeProject }}</b></span>
      </span>
      <span class="wk-total"><span class="wk-total-l">{{ text.total }} </span><b>{{ shown ? shown.total : '–' }}</b></span>
    </div>
    <button
      type="button" class="wk-bar f-slide-hover" :aria-label="text.workByHours" :aria-describedby="open ? `${srId} ${FIGURE_TIP_ID}` : srId" :aria-expanded="open && figureTip.pinned"
      @pointerenter="enter" @pointerleave="leave" @focus="focus" @blur="blur" @click.stop="click"
    >
      <span class="wk-track"><i v-for="segment in segments" :key="segment.id" class="wk-seg" :class="segment.fill" :data-group="segment.id" :style="{ left: `${segment.left}%`, width: `${segment.width}%` }" /></span>
    </button>
    <p class="wk-line">
      <span v-for="group in groups" :key="group.id" class="wk-item" :data-group="group.id"><i class="wk-sw" :class="group.fill || 'sw-open'" aria-hidden="true" /><span>{{ group.name }}</span><b>{{ group.shown }}</b></span>
      <span class="wk-item ne">
        <span v-if="!shown">–</span>
        <span v-else-if="shown.withoutEstimate"><b>{{ fmtInt(shown.withoutEstimate) }}</b>{{ fill(text.withoutEstimate, { n: '' }) }}</span>
        <span v-else>{{ text.allEstimated }}</span>
        <span class="wk-probe" aria-hidden="true"><b>{{ leafZeros }}</b>{{ fill(text.withoutEstimate, { n: '' }) }}</span>
      </span>
      <span v-if="failure" class="wk-failure" role="status">{{ failure }} <button type="button" class="wk-retry" @click="emit('retry')">{{ text.retry }}</button></span>
    </p>
    <span :id="srId" class="sr-only">{{ shown ? shown.accessible : label.label }}</span>
    <FigureTip v-if="open" width="wide">
      <p class="wf-ttl">{{ label.label }}</p>
      <div class="wf-rule" aria-hidden="true" />
      <div class="wf-tbl">
        <span class="h" /><span class="h">{{ text.head.hours }}</span><span class="h">{{ text.head.share }}</span><span class="h">{{ text.head.tickets }}</span><span class="h">{{ text.head.share }}</span>
        <template v-if="tipFigures">
          <template v-for="group in tipFigures.groups" :key="group.id">
            <span class="g"><i class="wk-sw" :class="group.fill || 'sw-open'" aria-hidden="true" />{{ group.name }}</span><span class="n">{{ group.shown }}</span><span class="s">{{ group.hoursShare }}%</span><span class="n">{{ fmtInt(group.tickets) }}</span><span class="s">{{ group.ticketShare }}%</span>
          </template>
          <span class="g">{{ text.total }}</span><span class="n">{{ tipFigures.total }}</span><span class="s" /><span class="n">{{ fmtInt(tipFigures.tickets) }}</span><span class="s" />
        </template>
        <template v-else>
          <template v-for="(group, index) in (header?.groups ?? groups)" :key="group.id">
            <span class="g wait">{{ index ? '' : (failure ?? text.counting) }}</span><span /><span /><span /><span />
          </template>
          <span class="g" /><span /><span /><span /><span />
        </template>
      </div>
      <p class="wf-foot lines" :style="{ height: `${footLines * 18}px` }">
        <template v-if="tipFigures"><span v-for="line in tipFoot" :key="line" class="wf-fline">{{ line }}</span></template>
      </p>
    </FigureTip>
  </div>
</template>

<style scoped>
.work-bar { position: relative; min-width: 0; container: work / inline-size; }
.wk-head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; min-width: 0; height: 18px; font-size: 12px; line-height: 18px; color: var(--ink-2); }
.wk-scope { min-width: 0; overflow: hidden; white-space: nowrap; }
.wk-long { display: block; overflow: hidden; text-overflow: ellipsis; }
.wk-scope b { font-weight: 500; color: var(--ink-2); }
.narrowed .wk-scope b { font-weight: 650; color: var(--ink); }
.wk-short, .wk-probe { display: none; }
.wk-total { flex: none; white-space: nowrap; color: var(--ink-3); }
.wk-total b { font: 600 12px/1 var(--mono); color: var(--ink); font-variant-numeric: tabular-nums; }
/* The bar is one control: hover, keyboard focus or a tap shows the counts behind it. */
.wk-bar { position: relative; display: block; width: 100%; height: 20px; margin: 0; padding: 6px 0; border: 0; border-radius: 6px; background: transparent; cursor: default; }
.wk-bar:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 1px; box-shadow: none; }
.wk-track { position: relative; display: block; height: 8px; overflow: hidden; border-radius: 999px; background: var(--track); box-shadow: inset 0 1px 2px color-mix(in srgb, var(--shadow-color) 10%, transparent); }
/* Drawn to scale: plain percentages, no gaps, no minimum and no width transition. */
.wk-seg { position: absolute; top: 0; bottom: 0; left: 0; width: 0; border-radius: 2px; }
/* No width transition: label, total, bar and line switch in one frame; while counting all four dim together. */
.work-bar[aria-busy="true"] .wk-head, .work-bar[aria-busy="true"] .wk-track, .work-bar[aria-busy="true"] .wk-line { opacity: .5; }
.wk-line { position: relative; display: flex; flex-wrap: wrap; align-items: center; gap: 2px 14px; margin: 0; font-size: 12px; line-height: 18px; color: var(--ink-2); }
.wk-item { display: inline-flex; align-items: center; gap: 5px; white-space: nowrap; }
.wk-item b { display: inline-block; min-width: var(--wk-digits); font: 600 11.5px/18px var(--mono); color: var(--ink); font-variant-numeric: tabular-nums; }
/* The "N without estimate" slot is the widest it can be for this project; "All estimated" fits in it. */
.wk-item.ne { display: inline-grid; color: var(--ink-3); }
.wk-item.ne > * { grid-area: 1 / 1; }
.wk-item.ne .wk-probe { display: inline; visibility: hidden; }
.wk-item.ne b { min-width: 0; font-weight: 500; color: var(--ink-2); }
/* A failed count replaces the figures in place: the line keeps its size, the message sits over it. */
.failed .wk-item { visibility: hidden; }
.wk-failure { position: absolute; inset: 0 0 auto; display: flex; align-items: center; flex-wrap: wrap; gap: 0 8px; min-height: 18px; color: var(--ink-2); }
.wk-retry { min-height: 18px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; }
.wk-retry:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 1px; border-radius: 4px; }
.sr-only { position: absolute; width: 1px; height: 1px; margin: -1px; padding: 0; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0; }

/* Compact: scope word, bar and total on one row (the scope and total slots are fixed); the line lives in the hover. */
.d-compact { display: flex; align-items: center; gap: 10px; }
.d-compact .wk-head { display: contents; }
.d-compact .wk-scope { order: 1; flex: none; display: inline-grid; justify-items: end; text-align: right; }
.d-compact .wk-scope > * { grid-area: 1 / 1; }
.d-compact .wk-long { display: none; }
.d-compact .wk-short { display: inline; }
.d-compact .wk-probe { display: inline; visibility: hidden; }
.d-compact .wk-probe b { font-weight: 650; }
.d-compact .wk-bar { order: 2; flex: 1; min-width: 80px; }
.d-compact .wk-track { height: 6px; }
.d-compact .wk-total { order: 3; }
.d-compact .wk-total-l { display: none; }
.d-compact .wk-total b { display: inline-block; min-width: var(--wk-total); text-align: right; }
.d-compact .wk-line { display: none; }
.d-compact.failed .wk-line { display: none; }

/* Phones and touch: the bar's reach is 44 px; on phones the block takes the full width and the line wraps calmly. */
@media (pointer: coarse) { .wk-bar { height: 44px; margin: -12px 0; padding: 18px 0; } }
@media (max-width: 600px) {
  .d-compact { display: block; }
  .d-compact .wk-head { display: flex; }
  .d-compact .wk-scope { display: block; text-align: left; }
  .d-compact .wk-long { display: block; }
  .d-compact .wk-short, .d-compact .wk-probe { display: none; }
  .d-compact .wk-total-l { display: inline; }
  .d-compact .wk-total b { min-width: 0; }
  .d-compact .wk-line { display: flex; }
  .d-compact .wk-track { height: 8px; }
  .wk-bar { height: 44px; margin: -12px 0; padding: 18px 0; }
  .wk-line { column-gap: 12px; }
}
</style>
