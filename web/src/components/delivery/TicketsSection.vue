<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Delivery › Tickets (design r4b §3.4; AEON-1359): how long tickets wait and work in each status, from every status
// change in the event log. Simple: the section head, three filters, one hero tile for Created → live (value, slow tail,
// trend, the typical time per step in the status-icon fills, one sentence and the small chart) and one tile per step.
// Expert: the caption, the same filters and six tiles in the Expert tile language. The filters sit above content
// that keeps its height in every state, so neither they nor anything below them move (AEON-541).
import { computed, onBeforeUnmount, onMounted, ref, useId } from 'vue'
import '../../styles/status-fills.css'
import AppIcon from '../AppIcon.vue'
import ExpertTile from './ExpertTile.vue'
import LearnPopover from './LearnPopover.vue'
import TicketStepTile from './TicketStepTile.vue'
import TrendChart from './TrendChart.vue'
import { vClipTip } from '../../directives/clipTip'
import type { DeliveryLanguage } from '../../lib/delivery'
import type { WindowDays } from '../../lib/deliveryNumbers'
import { deliveryText, fill } from '../../lib/deliveryNumbersText'
import { simpleText } from '../../lib/deliverySimpleText'
import type { TicketsModel, TicketTileModel } from '../../lib/deliveryTickets'
import type { TimingFilters } from '../../lib/statusTiming'
import { stripWidths } from '../../lib/workFigures'

const props = defineProps<{ model: TicketsModel; state: 'loading' | 'error' | 'ready'; busy: boolean; filters: TimingFilters; level: 'simple' | 'expert'; window: WindowDays; lang: DeliveryLanguage; openTile?: string | null }>()
const emit = defineEmits<{ filters: [next: TimingFilters]; retry: []; info: [tile: TicketTileModel, anchor: HTMLElement, pin: boolean]; 'info-leave': [] }>()
const text = computed(() => simpleText(props.lang))
const t = computed(() => text.value.tickets)
const pageText = computed(() => deliveryText(props.lang))
const uid = useId()

// ---------- Filters: three radio groups, one tab stop each; arrows, Home and End move the choice ----------
type Group = { key: keyof TimingFilters; label: string; options: { value: string; label: string }[] }
const groups = computed<Group[]>(() => [
  { key: 'type', label: t.value.filters.type, options: (['all', 'bug', 'other'] as const).map(value => ({ value, label: t.value.types[value] })) },
  { key: 'priority', label: t.value.filters.priority, options: (['all', 'high', 'medium', 'low'] as const).map(value => ({ value, label: t.value.priorities[value] })) },
  { key: 'worked_by', label: t.value.filters.workedBy, options: (['all', 'agent', 'person'] as const).map(value => ({ value, label: t.value.workers[value] })) },
])
function choose(group: Group, value: string) {
  if (props.filters[group.key] === value) return
  emit('filters', { ...props.filters, [group.key]: value } as TimingFilters)
}
function move(event: KeyboardEvent, group: Group) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  const values = group.options.map(option => option.value), index = values.indexOf(props.filters[group.key])
  const next = { ArrowLeft: index - 1, ArrowUp: index - 1, ArrowRight: index + 1, ArrowDown: index + 1, Home: 0, End: values.length - 1 }[event.key]
  if (next === undefined) return
  event.preventDefault()
  const value = values[Math.max(0, Math.min(values.length - 1, next))]
  choose(group, value)
  const host = event.currentTarget as HTMLElement
  requestAnimationFrame(() => host.querySelector<HTMLButtonElement>(`[data-v="${value}"]`)?.focus())
}

// ---------- Typical time per step: widths in proportion, at least 6 px, 2 px apart ----------
const typ = ref<HTMLElement>()
const typWidth = ref(0)
let resize: ResizeObserver | null = null
onMounted(() => {
  if (!typ.value) return
  resize = new ResizeObserver(entries => { typWidth.value = Math.floor(entries[0]?.contentRect.width ?? 0) })
  resize.observe(typ.value)
  typWidth.value = Math.floor(typ.value.clientWidth)
})
onBeforeUnmount(() => resize?.disconnect())
const typSegments = computed(() => {
  const shown = props.state === 'ready' ? props.model.typical.filter(step => step.seconds != null) : []
  const widths = typWidth.value ? stripWidths(shown.map(step => step.seconds!), typWidth.value) : []
  let x = 0
  return shown.map((step, i) => { const left = x, width = widths[i] ?? 0; x += width + 2; return { ...step, left, width } })
})
const ready = computed(() => props.state === 'ready')
</script>

<template>
  <section v-if="level === 'simple'" class="s-sec tk" :aria-labelledby="`${uid}-head`" data-testid="delivery-tickets">
    <div class="s-sec-head">
      <h3 :id="`${uid}-head`">{{ model.head }}</h3>
      <span class="cnt" :aria-hidden="ready ? undefined : true">{{ ready ? model.count : ' ' }}</span>
      <p>{{ t.sub }}</p>
    </div>
    <div class="tk-filters">
      <span v-for="group in groups" :key="group.key" class="seg" role="radiogroup" :aria-label="group.label" @keydown="move($event, group)">
        <button v-for="option in group.options" :key="option.value" type="button" role="radio" :data-v="option.value" :aria-checked="filters[group.key] === option.value"
          :tabindex="filters[group.key] === option.value ? 0 : -1" @click="choose(group, option.value)">{{ option.label }}</button>
      </span>
    </div>
    <article class="tk-hero" :aria-labelledby="`${uid}-hero`" :aria-busy="state === 'loading' || busy || undefined" data-testid="delivery-tickets-hero">
      <div class="s-top">
        <h4 :id="`${uid}-hero`" class="s-name">{{ model.hero.name }}</h4>
        <LearnPopover :id="`tk-${model.hero.key}`" :name="model.hero.name" :body="model.hero.learn.body" expert="" target="" :facts="model.hero.learn.facts"
          :labels="{ learn: text.learn, expertName: text.expertName, arion: text.arion }" />
      </div>
      <div class="hero-a" :class="{ busy }">
        <div class="s-valrow">
          <span v-if="state === 'loading'" class="sk" style="width: 40%; height: 36px" />
          <span v-else-if="ready && model.hero.value" class="s-val">{{ model.hero.value[0] }}<small>{{ model.hero.value[1] }}</small></span>
          <span v-else class="s-val empty">{{ state === 'error' ? text.notLoaded : text.nodataV }}</span>
          <span class="s-dir"><AppIcon name="arrow-down" :size="13" />{{ text.lower }}</span>
        </div>
        <p class="tk-slow"><span v-if="ready && model.hero.slow" v-clip-tip>{{ model.hero.slow.before }}<b>{{ model.hero.slow.value }}</b>{{ model.hero.slow.after }}</span></p>
        <p class="s-trend" :class="ready ? model.hero.trend.cls : 'none'">
          <AppIcon :name="ready ? model.hero.trend.icon : 'minus'" :size="13" />
          <span v-clip-tip>{{ ready ? `${model.hero.trend.text}${model.hero.trend.prev ? ` (${model.hero.trend.prev})` : ''}` : state === 'error' ? text.notLoaded : ' ' }}</span>
        </p>
        <p class="typ-cap">{{ t.typical }}</p>
        <div ref="typ" class="typ f-slide-hover" aria-hidden="true" data-testid="delivery-tickets-typical">
          <i v-for="step in typSegments" :key="step.key" class="sg" :class="[step.fill, { 'f-short': step.width < 10 }]" :data-step="step.key" :style="{ left: `${step.left}px`, width: `${step.width}px` }" />
        </div>
        <p class="typ-line">
          <span v-for="step in model.typical" :key="step.key"><i class="sw" :class="step.fill" aria-hidden="true" />{{ step.name }} <b>{{ ready ? step.value : '—' }}</b></span>
        </p>
        <p class="s-say">
          <template v-if="ready">{{ model.hero.say }}</template>
          <template v-else-if="state === 'error'"><span>{{ t.errT }} <button type="button" class="retry" @click="emit('retry')">{{ t.retry }}</button></span></template>
        </p>
      </div>
      <div class="hero-b" :class="{ busy }">
        <TrendChart :model="model.chart" :state="state" :text="pageText" small bare />
      </div>
      <div class="s-foot">
        <span v-if="state === 'loading'" class="sk" style="width: 45%; height: 9px" />
        <template v-else>
          <span v-if="ready && model.partial" class="partial"><AppIcon name="half" :size="10" />{{ text.partial }}</span>
          <span v-clip-tip>{{ ready ? model.heroFoot : t.foot }}</span>
        </template>
      </div>
    </article>
    <div class="tk-steps" :class="{ three: model.steps.length === 3 }" role="list">
      <TicketStepTile v-for="tile in model.steps" :key="tile.key" :tile="tile" :state="state" :busy="busy" :foot="model.foot" :partial="model.partial" :text="text" />
    </div>
  </section>

  <div v-else class="tk-expert" data-testid="delivery-tickets-expert">
    <div class="tk-cap-row">
      <p class="tiles-cap">{{ fill(t.expertCap, { w: window }) }}</p>
      <p v-if="state === 'error'" class="tk-err" role="alert"><span v-clip-tip>{{ t.errT }}</span><button type="button" class="retry" @click="emit('retry')">{{ t.retry }}</button></p>
    </div>
    <div class="tk-filters">
      <span v-for="group in groups" :key="group.key" class="seg" role="radiogroup" :aria-label="group.label" @keydown="move($event, group)">
        <button v-for="option in group.options" :key="option.value" type="button" role="radio" :data-v="option.value" :aria-checked="filters[group.key] === option.value"
          :tabindex="filters[group.key] === option.value ? 0 : -1" @click="choose(group, option.value)">{{ option.label }}</button>
      </span>
    </div>
    <div class="tiles tk" :class="{ busy, four: model.expert.length === 4 }" role="list">
      <ExpertTile v-for="tile in model.expert" :key="tile.def.key" :tile="tile" :state="state" :text="pageText" :info-open="openTile === tile.def.key"
        @info="(anchor, pin) => emit('info', tile, anchor, pin)" @info-leave="emit('info-leave')" />
    </div>
  </div>
</template>

<style scoped>
.s-sec { margin-top: 24px; }
.s-sec-head { display: flex; align-items: baseline; flex-wrap: wrap; gap: 2px 12px; }
.s-sec-head h3 { margin: 0; font: 650 15px/1.3 var(--font); color: var(--ink); }
.s-sec-head .cnt { font-size: 12.5px; font-weight: 600; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.s-sec-head p { flex-basis: 100%; margin: 0; font-size: 12.5px; color: var(--ink-2); }
.tk-filters { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 10px; margin-top: 10px; }
.tk-hero { display: grid; grid-template-columns: minmax(0, 5fr) minmax(0, 6fr); gap: 4px 28px; margin-top: 10px; padding: 14px 18px 11px; border-radius: 16px; background: var(--glass); -webkit-backdrop-filter: blur(14px) saturate(1.3); backdrop-filter: blur(14px) saturate(1.3); box-shadow: 0 0 0 1px var(--line), inset 0 1px 0 var(--glass-edge), 0 14px 34px -22px color-mix(in srgb, var(--primary-line) 35%, transparent); }
.tk-hero .s-top, .tk-hero .s-foot { grid-column: 1 / -1; }
.hero-a, .hero-b { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.busy { opacity: .5; }
.s-top { display: flex; align-items: flex-start; gap: 8px; min-height: 26px; }
.s-name { flex: 1; min-width: 0; margin: 3px 0 0; font: 600 13.5px/1.35 var(--font); color: var(--ink); overflow-wrap: anywhere; }
.s-valrow { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 0 8px; min-height: 46px; }
.s-val { font: 650 40px/46px var(--font); letter-spacing: -.025em; font-variant-numeric: tabular-nums; color: var(--ink); white-space: nowrap; }
.s-val small { margin-left: 4px; font: 500 14px/1 var(--font); letter-spacing: 0; color: var(--ink-2); }
.s-val.empty { font: 600 16px/46px var(--font); letter-spacing: 0; color: var(--ink-3); }
.s-dir { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; color: var(--ink-2); white-space: nowrap; }
.tk-slow { display: flex; height: 20px; margin: 0; font-size: 13px; color: var(--ink-2); white-space: nowrap; overflow: hidden; }
.tk-slow > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.tk-slow b { font-weight: 650; color: var(--ink); font-variant-numeric: tabular-nums; }
.s-trend { display: flex; align-items: center; gap: 6px; height: 20px; margin: 0; font-size: 12.5px; font-weight: 600; color: var(--ink-2); white-space: nowrap; overflow: hidden; }
.s-trend > svg { flex: none; }
.s-trend > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.s-trend.better { color: var(--ok); }
.s-trend.worse { color: var(--danger); }
.s-trend.none, .s-trend.same { color: var(--ink-3); font-weight: 500; }
.typ-cap { height: 18px; margin: 6px 0 0; font-size: 12px; line-height: 18px; color: var(--ink-3); }
/* The step bar slides its in-progress stripes only while the pointer is over it; the swatches never slide. */
.typ { position: relative; height: 10px; margin-top: 4px; }
.typ .sg { position: absolute; top: 0; height: 10px; border-radius: 3px; }
.typ-line { display: flex; flex-wrap: wrap; gap: 0 14px; min-height: calc(2 * 18px); margin: 4px 0 0; font-size: 12px; line-height: 18px; color: var(--ink-2); }
.typ-line span { display: inline-flex; align-items: center; gap: 5px; white-space: nowrap; }
.typ-line b { font-weight: 600; color: var(--ink); font-variant-numeric: tabular-nums; }
.sw { display: inline-block; flex: none; width: 12px; height: 8px; border-radius: 2px; }
.s-say { min-height: calc(13px * 1.45 * 3); margin: 0; font-size: 13px; line-height: 1.45; color: var(--ink-2); }
.s-foot { display: flex; align-items: center; gap: 6px; min-width: 0; height: 28px; margin-top: auto; padding-top: 8px; border-top: 1px solid var(--line); font-size: 11.5px; color: var(--ink-3); white-space: nowrap; }
.s-foot > span:last-child { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.partial { display: inline-flex; align-items: center; gap: 4px; flex: none; height: 18px; padding: 0 6px; border-radius: 6px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); font: 600 10.5px/1 var(--font); white-space: nowrap; }
.tk-steps { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 12px; margin-top: 12px; }
/* Projects that don't mark releases stop at QA → merged: three steps, four Expert tiles. */
.tk-steps.three { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.retry { position: relative; display: inline; height: auto; margin: 0; padding: 0; border: 0; background: none; color: var(--teal-ink); font: inherit; font-weight: 600; cursor: pointer; text-decoration: underline; text-underline-offset: 2px; }
.retry:focus-visible { outline: 2px solid var(--primary-line); outline-offset: 2px; border-radius: 3px; }
/* Expert */
.tk-cap-row { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; min-width: 0; margin-top: 18px; }
.tiles-cap { margin: 0; font: 500 10.5px/1.5 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.tk-err { display: flex; gap: 6px; min-width: 0; margin: 0; font-size: 12px; line-height: 1.5; color: var(--danger); white-space: nowrap; }
.tk-err > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.tiles { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 12px; margin-top: 8px; }
.tiles.four { grid-template-columns: repeat(4, minmax(0, 1fr)); }
.sk { display: block; border-radius: 6px; background: var(--skeleton); }
@media (prefers-reduced-motion: no-preference) {
  .sk { background: linear-gradient(90deg, var(--skeleton) 0%, var(--skeleton-hi) 50%, var(--skeleton) 100%) 0 0 / 200% 100%; animation: dl-sk 1.4s ease-in-out infinite; }
}
@keyframes dl-sk { to { background-position: -200% 0; } }
@media (pointer: coarse) { .retry::before { content: ''; position: absolute; top: 50%; left: 50%; width: max(100%, 44px); height: 44px; transform: translate(-50%, -50%); } }
@container delivery (max-width: 1200px) {
  .tk-steps { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .tiles.tk { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .tiles.tk.four { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@container delivery (max-width: 1000px) { .tk-hero { grid-template-columns: minmax(0, 1fr); } }
@container delivery (max-width: 640px) {
  .tk-filters .seg { width: 100%; }
  .tk-filters .seg button { flex: 1; height: 44px; min-width: 0; padding: 0 6px; }
  .tk-hero { padding: 12px 12px 10px 14px; }
  .s-val { font-size: 34px; }
  .tk-steps, .tk-steps.three { grid-template-columns: minmax(0, 1fr); }
  .tiles.tk, .tiles.tk.four { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
}
@container delivery (max-width: 460px) { .tiles.tk, .tiles.tk.four { grid-template-columns: minmax(0, 1fr); } }
</style>
