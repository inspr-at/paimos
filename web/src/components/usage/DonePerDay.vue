<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Tickets agents finished per UTC day. Every day of the range is a column; a day
// with nothing done has no bar (a known zero). Hover shows the day's sessions and
// agent time; screen readers get the same as a table.
import { computed } from 'vue'
import { formatCount, formatDay, formatWhen, type UsageWork } from '../../lib/usageFormat'
import { duration, plural } from '../../lib/usageWork'

const props = defineProps<{ days: UsageWork['days'] }>()

const peak = computed(() => props.days.reduce<UsageWork['days'][number] | null>((best, d) => (d.done > (best?.done ?? 0) ? d : best), null))
const scale = computed(() => {
  const top = peak.value?.done ?? 0
  if (top <= 4) return 4
  const step = top <= 10 ? 2 : top <= 25 ? 5 : top <= 50 ? 10 : 25
  return Math.ceil(top / step) * step
})
const ticks = computed(() => [scale.value, scale.value / 2])
const dense = computed(() => props.days.length > 45)
const axis = computed(() => {
  const n = props.days.length
  if (!n) return []
  const picks = [...new Set([0, Math.floor((n - 1) / 2), n - 1])]
  return picks.map(index => ({ index, label: formatDay(props.days[index]!.day), left: n === 1 ? 50 : ((index + 0.5) / n) * 100 }))
})
function tip(d: UsageWork['days'][number]) {
  const parts = [formatWhen(d.day), `${formatCount(d.done)} done`]
  if (d.sessions) parts.push(plural(d.sessions, 'session'))
  if (d.agent_seconds) parts.push(duration(d.agent_seconds))
  return parts.join(' · ')
}
const summary = computed(() => {
  const total = props.days.reduce((sum, d) => sum + d.done, 0)
  return peak.value ? `Tickets done per day: ${formatCount(total)} in total, most on ${formatWhen(peak.value.day)} (${formatCount(peak.value.done)}).` : 'Tickets done per day: none in this range.'
})
</script>

<template>
  <figure class="chart">
    <div class="plot" role="img" :aria-label="summary">
      <div class="grid" aria-hidden="true">
        <div v-for="value in ticks" :key="value" class="tick" :style="{ bottom: `${(value / scale) * 100}%` }"><span>{{ value }}</span></div>
        <div class="tick base" />
      </div>
      <div class="cols" :class="{ dense }">
        <div v-for="d in days" :key="d.day" class="col" :data-tip="tip(d)">
          <span v-if="d.done" class="bar" :style="{ height: `${(d.done / scale) * 100}%` }" />
        </div>
      </div>
    </div>
    <div class="axis" aria-hidden="true">
      <span v-for="item in axis" :key="item.index" :style="{ left: `${item.left}%` }" :class="{ first: item.index === 0, last: item.index === days.length - 1 && days.length > 1 }">{{ item.label }}</span>
    </div>
    <table class="sr-only">
      <caption>Tickets done, sessions and agent time per UTC day</caption>
      <thead><tr><th>Day</th><th>Done</th><th>Sessions</th><th>Agent time</th></tr></thead>
      <tbody>
        <tr v-for="d in days.filter(x => x.done || x.sessions)" :key="d.day"><td>{{ formatWhen(d.day) }}</td><td>{{ d.done }}</td><td>{{ d.sessions }}</td><td>{{ d.agent_seconds ? duration(d.agent_seconds) : '' }}</td></tr>
      </tbody>
    </table>
  </figure>
</template>

<style scoped>
.chart { margin: 0; }
.plot { position: relative; height: 132px; margin-left: 28px; }
.grid { position: absolute; inset: 0; pointer-events: none; }
.tick { position: absolute; left: 0; right: 0; height: 0; border-top: 1px solid var(--line); }
.tick.base { bottom: 0; border-top-color: var(--line-2); }
.tick span { position: absolute; right: calc(100% + 8px); top: -7px; font: 500 11px/1 var(--font); color: var(--ink-3); font-variant-numeric: tabular-nums; }
.cols { position: absolute; inset: 0; display: flex; align-items: stretch; gap: 3px; }
.cols.dense { gap: 1px; }
.col { position: relative; flex: 1 1 0; min-width: 0; display: flex; align-items: flex-end; justify-content: center; border-radius: 4px 4px 0 0; }
.bar { display: block; width: 100%; max-width: 22px; min-height: 3px; border-radius: 4px 4px 0 0; background: var(--teal); transition: background-color .12s ease; }
.dense .bar { border-radius: 2px 2px 0 0; }
@media (hover: hover) {
  .col:hover { background: var(--row-hover); }
  .col:hover .bar { background: var(--teal-ink); }
}
@media (prefers-reduced-motion: reduce) { .bar { transition: none; } }
.axis { position: relative; height: 16px; margin: 6px 0 0 28px; font: 500 11px/1 var(--font); color: var(--ink-3); }
.axis span { position: absolute; top: 0; transform: translateX(-50%); white-space: nowrap; }
.axis .first { transform: none; left: 0 !important; }
.axis .last { transform: none; left: auto !important; right: 0; }
</style>
