<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One column per UTC start day: the lifetime list estimate of the sessions that
// started that day. A day with sessions but no known price gets a hollow stub,
// never a zero bar; a partly priced day is hatched (texture, not colour alone).
// Hover shows the day; the table behind "Show table" carries every value.
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import { costStateLabel, dayKey, formatCount, formatDay, formatUSD, formatWhen, rangeDays, usdNumber, type UsageTrend } from '../../lib/usageFormat'

const props = defineProps<{ trend: UsageTrend[]; from: string; to: string }>()

interface Column { day: string; sessions: number; usd: number | null; cost: string | null; state: UsageTrend['group']['cost_state'] | null }

const columns = computed<Column[]>(() => {
  const byDay = new Map(props.trend.map(point => [dayKey(point.day), point.group]))
  const days = rangeDays(props.from, props.to)
  // A point outside the stated range still shows, so nothing reported is dropped.
  for (const key of byDay.keys()) if (!days.includes(key)) days.push(key)
  days.sort()
  return days.map(day => {
    const group = byDay.get(day)
    return { day, sessions: group?.sessions ?? 0, usd: group ? usdNumber(group.estimated_cost_usd) : null, cost: group?.estimated_cost_usd ?? null, state: group?.cost_state ?? null }
  })
})

function niceCeil(value: number) {
  if (value <= 0) return 1
  const power = 10 ** Math.floor(Math.log10(value))
  for (const step of [1, 2, 2.5, 5, 10]) if (value <= step * power) return step * power
  return 10 * power
}
const peak = computed(() => columns.value.reduce<Column | null>((best, col) => col.usd !== null && (best?.usd == null || col.usd > best.usd) ? col : best, null))
const scale = computed(() => niceCeil(peak.value?.usd ?? 0))
const ticks = computed(() => [scale.value, scale.value / 2])
const dense = computed(() => columns.value.length > 45)
const hasPartial = computed(() => columns.value.some(col => col.sessions > 0 && (col.state === 'partial' || col.state === 'provisional')))
const hasUnknown = computed(() => columns.value.some(col => col.sessions > 0 && col.usd === null))
const axis = computed(() => {
  const list = columns.value
  if (!list.length) return []
  const picks = [0, Math.floor((list.length - 1) / 2), list.length - 1]
  return [...new Set(picks)].map(index => ({ index, label: formatDay(list[index]!.day), left: list.length === 1 ? 50 : ((index + 0.5) / list.length) * 100 }))
})

// The peak's label stays inside the plot: near an edge it aligns to that edge.
const capSide = computed(() => {
  const index = columns.value.findIndex(col => col.day === peak.value?.day)
  const n = columns.value.length
  if (n < 5) return ''
  if (index >= n - Math.max(2, Math.ceil(n * 0.1))) return 'end'
  if (index < Math.max(2, Math.ceil(n * 0.1))) return 'start'
  return ''
})
function height(col: Column) {
  if (col.usd === null || col.usd <= 0) return 0
  return Math.max(1.5, (col.usd / scale.value) * 100)
}
function tickLabel(value: number) {
  return value >= 1000 ? `${(value / 1000).toLocaleString('en-US', { maximumFractionDigits: 1 })}k` : value.toLocaleString('en-US', { maximumFractionDigits: 2 })
}
function tip(col: Column) {
  const head = `${formatWhen(col.day)} · ${formatCount(col.sessions)} ${col.sessions === 1 ? 'session' : 'sessions'}`
  if (!col.sessions) return head
  if (col.usd === null) return `${head} · cost unknown`
  const note = col.state === 'partial' ? ' · partly unknown' : col.state === 'provisional' ? ' · provisional' : ''
  return `${head} · ${formatUSD(col.cost)}${note}`
}
const summary = computed(() => {
  const top = peak.value
  return top ? `List estimate per start day. Highest: ${formatUSD(top.cost)} on ${formatWhen(top.day)}.` : 'List estimate per start day. No day has a known estimate.'
})
</script>

<template>
  <figure class="trend">
    <div class="plot" role="img" :aria-label="summary">
      <div class="grid" aria-hidden="true">
        <div v-for="value in ticks" :key="value" class="tick" :style="{ bottom: `${(value / scale) * 100}%` }"><span>{{ tickLabel(value) }}</span></div>
        <div class="tick base" />
      </div>
      <div class="cols" :class="{ dense }">
        <div v-for="col in columns" :key="col.day" class="col" :data-tip="tip(col)">
          <span
            v-if="col.usd !== null && col.usd > 0"
            class="bar"
            :class="{ hatch: col.state === 'partial' || col.state === 'provisional' }"
            :style="{ height: `${height(col)}%` }"
          />
          <span v-else-if="col.sessions > 0 && col.usd === null" class="stub" />
          <span v-if="col.day === peak?.day && col.usd" class="cap" :class="capSide" :style="{ bottom: `calc(${height(col)}% + 4px)` }">{{ formatUSD(col.cost).replace(' USD', '') }}</span>
        </div>
      </div>
    </div>
    <div class="axis" aria-hidden="true">
      <span v-for="item in axis" :key="item.index" :style="{ left: `${item.left}%` }" :class="{ first: item.index === 0, last: item.index === columns.length - 1 && columns.length > 1 }">{{ item.label }}</span>
    </div>
    <figcaption v-if="hasPartial || hasUnknown" class="key">
      <span v-if="hasPartial"><i class="swatch hatch" aria-hidden="true" />Partly priced</span>
      <span v-if="hasUnknown"><i class="swatch stub" aria-hidden="true" />Price unknown</span>
    </figcaption>
    <details class="table-view">
      <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Show table</summary>
      <table class="days">
        <caption class="sr-only">Lifetime usage of sessions started each UTC day</caption>
        <thead><tr><th>Day</th><th class="r">Sessions</th><th class="r">List estimate</th></tr></thead>
        <tbody>
          <tr v-for="col in columns.filter(item => item.sessions > 0 || item.cost !== null)" :key="col.day">
            <td>{{ formatWhen(col.day) }}</td>
            <td class="r num">{{ formatCount(col.sessions) }}</td>
            <td class="r num">
              <template v-if="col.cost !== null && col.usd !== null">{{ formatUSD(col.cost) }}<span v-if="col.state && col.state !== 'known'" class="state">{{ costStateLabel[col.state].toLowerCase() }}</span></template>
              <span v-else class="unknown" data-tip="Unknown"><span aria-hidden="true">–</span><span class="sr-only">Unknown</span></span>
            </td>
          </tr>
        </tbody>
      </table>
    </details>
  </figure>
</template>

<style scoped>
.trend { margin: 0; }
.plot { position: relative; height: 168px; margin-left: 40px; }
.grid { position: absolute; inset: 0; pointer-events: none; }
.tick { position: absolute; left: 0; right: 0; height: 0; border-top: 1px solid var(--line); }
.tick.base { bottom: 0; border-top-color: var(--line-2); }
.tick span { position: absolute; right: calc(100% + 8px); top: -7px; font: 500 11px/1 var(--font); color: var(--ink-3); font-variant-numeric: tabular-nums; white-space: nowrap; }
.cols { position: absolute; inset: 0; display: flex; align-items: stretch; gap: 2px; }
.cols.dense { gap: 1px; }
.col { position: relative; flex: 1 1 0; min-width: 0; display: flex; align-items: flex-end; justify-content: center; border-radius: 4px 4px 0 0; }
.bar { position: relative; display: block; width: 100%; max-width: 24px; border-radius: 4px 4px 0 0; background: var(--teal); transition: background-color .12s ease; }
.dense .bar { border-radius: 2px 2px 0 0; }
.bar.hatch { background: repeating-linear-gradient(135deg, var(--teal) 0 3px, color-mix(in srgb, var(--teal) 45%, transparent) 3px 6px); }
.stub, .swatch.stub { display: block; width: 100%; max-width: 24px; height: 6px; border-radius: 2px 2px 0 0; box-shadow: inset 0 0 0 1px var(--ink-3); }
.cap.end { left: auto; right: 0; transform: none; }
.cap.start { left: 0; transform: none; }
.cap { position: absolute; left: 50%; transform: translateX(-50%); z-index: 1; font: 600 11px/1 var(--font); color: var(--ink-2); white-space: nowrap; font-variant-numeric: tabular-nums; }
@media (hover: hover) {
  .col:hover { background: var(--row-hover); }
  .col:hover .bar { background: var(--teal-ink); }
  .col:hover .bar.hatch { background: repeating-linear-gradient(135deg, var(--teal-ink) 0 3px, color-mix(in srgb, var(--teal-ink) 45%, transparent) 3px 6px); }
}
@media (prefers-reduced-motion: reduce) { .bar { transition: none; } }
.axis { position: relative; height: 18px; margin: 6px 0 0 40px; font: 500 11px/1 var(--font); color: var(--ink-3); }
.axis span { position: absolute; top: 0; transform: translateX(-50%); white-space: nowrap; }
.axis .first { transform: none; left: 0 !important; }
.axis .last { transform: none; left: auto !important; right: 0; }
.key { display: flex; font-family: var(--font); flex-wrap: wrap; gap: 6px 16px; margin: 8px 0 0 40px; font-size: 12px; color: var(--ink-2); }
.key span { display: inline-flex; align-items: center; gap: 6px; }
.swatch { display: inline-block; width: 12px; height: 10px; border-radius: 2px; }
.swatch.hatch { background: repeating-linear-gradient(135deg, var(--teal) 0 3px, color-mix(in srgb, var(--teal) 45%, transparent) 3px 6px); }
.swatch.stub { width: 12px; height: 6px; }
.table-view { margin-top: 10px; }
.table-view summary { cursor: pointer; font-size: 12.5px; font-weight: 600; color: var(--ink-2); }
.days { width: 100%; margin-top: 8px; border-collapse: collapse; font-size: 13px; }
.days th { height: 30px; padding: 0 8px; text-align: left; font: 500 11px/1 var(--font); color: var(--ink-3); border-bottom: 1px solid var(--line-2); }
.days td { padding: 7px 8px; border-bottom: 1px solid var(--line); }
.r { text-align: right !important; }
.num { font-variant-numeric: tabular-nums; white-space: nowrap; }
.state { margin-left: 6px; font-size: 11.5px; color: var(--ink-3); }
.unknown { color: var(--ink-3); }
</style>
