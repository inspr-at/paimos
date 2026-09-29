<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { pct, poolSentence } from '../../lib/capacity'
import { bandReadings, bandRows, plural, type BandRow } from '../../lib/usageWork'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import AppIcon from '../AppIcon.vue'
import CapacityGauge from '../agents/CapacityGauge.vue'
import CapacityLegend from '../agents/CapacityLegend.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import PlanSentence from '../agents/PlanSentence.vue'

// "Now" on the Usage page: one row per vendor pool, from the same store, gauge
// and plan sentence as the Agents desk. What binds first comes first. Pacing is
// changed on the Agents desk; this band only reads.
const capacity = useCapacity()
const agents = useAgents()
const now = computed(() => agents.now)
const rows = computed(() => bandRows(capacity.pools, now.value))
// Both reads decide the band: a failed accounts read is an error, never "No accounts yet".
const accountsLoaded = computed(() => agents.accountsUpdatedAt !== null)
const loading = computed(() => (!capacity.loaded && capacity.state !== 'error') || (!accountsLoaded.value && agents.accountsState !== 'error'))
const failed = computed(() => (capacity.state === 'error' && !capacity.loaded) || (agents.accountsState === 'error' && !accountsLoaded.value))
const retry = () => Promise.all([agents.refreshAccounts(), capacity.load()])

// A pool figure from some of its accounts says so; it is never the pool's figure.
function gaugeLabel(row: BandRow) {
  if (row.left === null) return ''
  const scope = bandReadings(row) ? `, ${bandReadings(row)}` : row.accounts > 1 ? `, ${row.accounts} accounts` : ''
  const base = `${row.pool.name}${scope}: ${Math.round(row.left)}% left`
  return row.today ? `${base}, ${row.today.used} of ${row.today.share} used today` : base
}
const planText = (row: BandRow) => [row.accounts > 1 && !bandReadings(row) ? `${row.accounts} accounts` : '', row.pool.plan].filter(Boolean).join(' · ')
const sentenceCase = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)
function resetTip(row: BandRow) {
  return row.reset ? `${row.reset.kind ? `The ${row.reset.kind} window` : 'It'} resets ${row.reset.full}` : undefined
}
</script>

<template>
  <section class="band glass-card" aria-labelledby="band-title">
    <header class="band-head">
      <h2 id="band-title">Capacity</h2>
      <span v-if="capacity.ready.total" class="meta">{{ capacity.ready.live }} of {{ plural(capacity.ready.total, 'account') }} ready</span>
      <RouterLink class="pacing" to="/agents" data-tip="Work days, nights, Sprint and Hold are on the Agents desk"><AppIcon name="sliders" :size="15" />Pacing</RouterLink>
    </header>

    <div v-if="loading" class="rows" aria-hidden="true">
      <div v-for="n in 2" :key="n" class="row skeleton-row"><span class="skeleton" /><span class="skeleton wide" /></div>
    </div>
    <p v-else-if="failed" class="empty" role="alert">
      Capacity could not be loaded right now. <button type="button" class="btn sm" @click="retry()">Try again</button>
    </p>
    <p v-else-if="!rows.length" class="empty">
      No accounts yet. Sign in to a harness on a connected computer and it appears here.
      <RouterLink class="btn sm" to="/agents/register-agent"><AppIcon name="monitor" :size="14" />Connect your machine</RouterLink>
    </p>
    <ul v-else class="rows">
      <li v-for="row in rows" :key="row.pool.id" class="row" :data-pool="row.pool.id">
        <div class="name">
          <span class="vendor"><HarnessMark :harness="row.pool.id" :size="15" /></span>
          <span class="pool-name">{{ row.pool.name }}</span>
          <span v-if="planText(row)" class="plan-name" :title="planText(row)">{{ planText(row) }}</span>
        </div>
        <CapacityGauge class="bar" :gauge="row.gauge" :left="row.left ?? 0" :value="row.left ?? 0" :label="gaugeLabel(row)" :ahead="!!row.today?.ahead" />
        <span class="left num"><template v-if="row.left !== null"><b>{{ pct(row.left) }}</b> left</template></span>
        <span class="reset num" :data-tip="resetTip(row)">
          <template v-if="row.reset">resets <b>{{ row.reset.label }}</b><span v-if="row.reset.window" class="win"> · {{ row.reset.window }}</span></template>
        </span>
        <p class="sentence" :class="{ ahead: poolSentence(row.pool, now).ahead }"><span v-if="bandReadings(row)" class="partial">{{ sentenceCase(bandReadings(row)) }}. </span><PlanSentence :sentence="poolSentence(row.pool, now)" /></p>
      </li>
    </ul>
    <footer v-if="rows.some(r => r.gauge)" class="band-foot"><CapacityLegend /></footer>
  </section>
</template>

<style scoped>
.band { padding: 0; }
.band-head { display: flex; align-items: baseline; gap: 10px; padding: 14px 18px 12px 20px; border-bottom: 1px solid var(--line); }
.band-head h2 { font-size: 17px; }
.meta { color: var(--ink-3); font-size: 13px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.pacing { display: inline-flex; align-items: center; gap: 7px; align-self: center; height: 32px; margin-left: auto; padding: 0 10px; border-radius: 999px; color: var(--ink-2); font-size: 13px; font-weight: 550; white-space: nowrap; }
@media (hover: hover) { .pacing:hover { background: var(--row-hover); color: var(--ink); } }
.pacing:focus-visible { box-shadow: var(--focus-ring); }
.empty { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 16px 20px; color: var(--ink-2); font-size: 13.5px; }
.rows { margin: 0; padding: 4px 0; list-style: none; }
.row {
  display: grid; grid-template-columns: 300px minmax(160px, 1fr) 84px 196px; grid-template-areas: "name bar left reset" ". sentence sentence sentence";
  align-items: center; gap: 4px 18px; padding: 12px 20px;
}
.row + .row { border-top: 1px solid var(--line); }
.name { grid-area: name; display: flex; align-items: center; gap: 9px; min-width: 0; }
.vendor { display: grid; place-items: center; flex: none; width: 28px; height: 28px; border-radius: 8px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line), 0 1px 2px rgba(32, 60, 61, .06); color: var(--ink); }
.pool-name { color: var(--ink); font-size: 14.5px; font-weight: 650; white-space: nowrap; }
.plan-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: 12.5px; }
.bar { grid-area: bar; }
.left { grid-area: left; justify-self: end; color: var(--ink-3); font-size: 12px; white-space: nowrap; }
.left b { color: var(--ink); font-size: 15px; font-weight: 700; }
.reset { grid-area: reset; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); font-size: 13px; }
.reset b { color: var(--ink); font-weight: 600; }
.win { color: var(--ink-3); }
.num { font-variant-numeric: tabular-nums; }
.sentence { grid-area: sentence; margin: 2px 0 0; color: var(--ink-2); font-size: 13px; line-height: 1.5; text-wrap: pretty; }
.sentence :deep(b) { color: var(--ink); font-weight: 600; }
.sentence :deep(.n) { color: var(--teal-ink); font-weight: 700; font-variant-numeric: tabular-nums; }
.sentence.ahead :deep(.n) { color: var(--gold-ink); }
.partial { color: var(--ink); }
.skeleton-row { display: flex; gap: 18px; }
.skeleton-row .skeleton { width: 160px; }
.skeleton-row .skeleton.wide { flex: 1; width: auto; }
.band-foot { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 18px; padding: 10px 20px 12px; border-top: 1px solid var(--line); color: var(--ink-3); font-size: 12px; }
@media (max-width: 1100px) {
  .row { grid-template-columns: 220px minmax(120px, 1fr) 76px 170px; }
}
@media (max-width: 720px) {
  .band-head { padding: 12px 14px 10px; }
  .pacing { height: 40px; margin-right: -6px; }
  .row {
    grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: "name left" "bar bar" "reset reset" "sentence sentence";
    gap: 6px 12px; padding: 12px 14px;
  }
  .reset { justify-self: start; font-size: 12.5px; color: var(--ink-3); }
  .band-foot { padding: 10px 14px 12px; gap: 6px 14px; }
}
</style>
