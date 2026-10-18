<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Delivery › Flow Live: the runs in flight (AEON-994 draft 5, package 6). Simple says
// what happens now, who is on it, what it waits for, how much is done and when it is
// expected, with a verdict against the target (shape + word); Expert names the step,
// actor, step ETA and run ETA with p90. Phones read each row as a card.
import AppIcon from '../AppIcon.vue'
import type { FlowLevel } from '../../lib/deliveryFlow'
import type { FlightRow } from '../../lib/deliveryFlowModes'
import type { FlowText } from '../../lib/deliveryFlowText'

const props = defineProps<{ rows: FlightRow[]; level: FlowLevel; text: FlowText }>()
const head = (i: number) => props.level === 'simple' ? props.text.thSimple[i - 1] ?? '' : props.text.thExpert[i] ?? ''
</script>

<template>
  <table class="fl-table" :class="{ x: level === 'expert' }" :aria-label="text.tableLabel" data-testid="flow-inflight">
    <thead v-if="level === 'simple'">
      <tr><th scope="col"><span class="sr-only">{{ text.run }}</span></th><th v-for="h in text.thSimple" :key="h" scope="col">{{ h }}</th></tr>
    </thead>
    <thead v-else>
      <tr><th v-for="h in text.thExpert" :key="h" scope="col">{{ h }}</th></tr>
    </thead>
    <tbody>
      <tr v-for="row in rows" :key="row.id">
        <template v-if="level === 'simple'">
          <th scope="row">{{ row.title }}</th>
          <td :data-l="head(1)">{{ row.now }}</td>
          <td :data-l="head(2)">{{ row.who }}</td>
          <td :data-l="head(3)"><span class="fl-wait" :class="{ you: row.waiting.you }"><AppIcon name="clock" :size="12" />{{ row.waiting.text }}</span></td>
          <td class="pc" :data-l="head(4)">
            <template v-if="row.pct != null"><span class="pbar" role="img" :aria-label="`${row.pct}%`"><i :style="{ width: `${row.pct}%` }" /></span><span class="mono">{{ row.pct }}%</span></template>
            <span v-else class="mu">–</span>
          </td>
          <td :data-l="head(5)">
            <b v-if="row.estimated">{{ row.expected }}</b><span v-else class="mu">{{ row.expected }}</span>
            <span v-if="row.verdict" class="fl-ver" :class="row.verdict.level">
              <svg width="13" height="13" viewBox="0 0 16 16" aria-hidden="true" focusable="false">
                <template v-if="row.verdict.level === 'on'"><circle class="fill" cx="8" cy="8" r="6.6" /><path class="tick" d="m5 8.2 2 2 4-4.4" /></template>
                <template v-else-if="row.verdict.level === 'close'"><circle class="ring" cx="8" cy="8" r="5.9" /><path class="fill" d="M8 2.1a5.9 5.9 0 0 1 0 11.8Z" /></template>
                <template v-else><circle class="ring" cx="8" cy="8" r="5.9" /><path class="ring" d="M8 4.9v3.6M8 11.1v.05" /></template>
              </svg>
              <span><b>{{ row.verdict.word }}</b> · {{ row.verdict.text }}</span>
            </span>
          </td>
        </template>
        <template v-else>
          <th scope="row" class="mono">{{ row.tag }}</th>
          <td :data-l="head(1)">{{ row.nowX }}</td>
          <td :data-l="head(2)">{{ row.whoX }}</td>
          <td :data-l="head(3)">{{ row.waitX }}</td>
          <td class="pc" :data-l="head(4)">
            <template v-if="row.pct != null"><span class="pbar" role="img" :aria-label="`${row.pct}%`"><i :style="{ width: `${row.pct}%` }" /></span><span class="mono">{{ row.pct }}%</span></template>
            <span v-else class="mu">–</span>
          </td>
          <td class="mono" :data-l="head(5)">{{ row.stepX }}</td>
          <td class="mono" :data-l="head(6)">{{ row.etaX }}</td>
        </template>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.fl-table { width: 100%; margin-top: 14px; border-collapse: collapse; font-size: 13px; }
.fl-table th, .fl-table td { padding: 10px; border-bottom: 1px solid var(--line); text-align: left; vertical-align: top; }
.fl-table thead th { font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); }
.fl-table tbody th { font-weight: 650; color: var(--ink); }
.fl-table td { color: var(--ink-2); }
.fl-table td b { color: var(--ink); font-weight: 650; }
.fl-table .pc { white-space: nowrap; }
.fl-table.x td.mono, .fl-table td:nth-child(3) { white-space: nowrap; }
.mono { font-family: var(--mono); font-size: 12px; font-variant-numeric: tabular-nums; }
.mu { color: var(--ink-3); }
.fl-wait { display: inline-flex; align-items: center; gap: 5px; padding: 2px 8px; border-radius: 999px; background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); font-size: 12.5px; font-weight: 600; }
.fl-wait svg { flex: none; }
.fl-wait.you { background: color-mix(in srgb, var(--gold) 20%, transparent); }
.fl-ver { display: flex; align-items: center; gap: 5px; margin-top: 3px; font-size: 12px; color: var(--ink-2); }
.fl-ver svg { flex: none; }
.fl-ver b { font-weight: 600; color: var(--ink-2); }
.fl-ver .fill { fill: currentColor; }
.fl-ver .ring { fill: none; stroke: currentColor; stroke-width: 1.8; stroke-linecap: round; }
.fl-ver .tick { fill: none; stroke: var(--canvas); stroke-width: 1.9; stroke-linecap: round; stroke-linejoin: round; }
.fl-ver.on svg { color: var(--ok); }
.fl-ver.close svg { color: var(--gold); }
.fl-ver.far svg { color: var(--danger); }
.pbar { display: inline-block; vertical-align: middle; width: 70px; height: 6px; margin-right: 8px; border-radius: 999px; background: var(--track); overflow: hidden; }
.pbar i { display: block; height: 100%; border-radius: 999px; background: var(--teal); }
@container delivery (max-width: 640px) {
  .fl-table thead { display: none; }
  .fl-table, .fl-table tbody, .fl-table tr, .fl-table th, .fl-table td { display: block; }
  .fl-table tr { padding: 10px 0; border-bottom: 1px solid var(--line); }
  .fl-table th, .fl-table td { padding: 3px 0; border: 0; white-space: normal !important; }
  .fl-table td::before { content: attr(data-l); display: inline-block; min-width: 96px; margin-right: 8px; font: 500 10px/1.4 var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); }
}
</style>
