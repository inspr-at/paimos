<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { buildPools, pct, poolSentence, sourceLine, consumptionLine, unreportedCapacity } from '../../lib/capacity'
import type { AccountLine } from '../../lib/computerAccounts'
import { bandReadings, bandRows, plural, type BandRow } from '../../lib/usageWork'
import { useAgents } from '../../stores/agents'
import { useCapacity } from '../../stores/capacity'
import AppIcon from '../AppIcon.vue'
import CapacityGauge from '../agents/CapacityGauge.vue'
import CapacityLegend from '../agents/CapacityLegend.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import PlanSentence from '../agents/PlanSentence.vue'

// "Now" on the Usage page: one row per vendor pool, from the same store, gauge
// (with what is kept for you) and plan sentence as the Agents desk. What binds
// first comes first. Pacing is changed on the Agents desk; this band only reads.
const capacity = useCapacity()
const agents = useAgents()
const now = computed(() => agents.now)
const configuredAccounts = computed(() => [...capacity.accountLines.values()])
const rows = computed(() => bandRows(buildPools(configuredAccounts.value.map(account => account.row), now.value), now.value))
// Pool summaries merge shared quotas; this list keeps every configured identity.
const accountsByPool = computed(() => {
  const out = new Map<string, AccountLine[]>()
  for (const line of configuredAccounts.value) {
    const row = line.row
    const pool = row.groupId ? `group:${row.groupId}` : row.harness
    out.set(pool, [...(out.get(pool) ?? []), line])
  }
  return out
})
const readyCount = computed(() => configuredAccounts.value.filter(account => account.readiness.kind === 'ready').length)
// The complete inventory also needs pairing: an account may exist there before
// its first probe registers it in the accounts projection.
const accountsLoaded = computed(() => agents.accountsUpdatedAt !== null)
const computersFailed = computed(() => capacity.computersState === 'error' || capacity.computersStale)
const loading = computed(() => (!capacity.loaded && capacity.state !== 'error') || (!accountsLoaded.value && agents.accountsState !== 'error') || (!capacity.computersLoaded && capacity.computersState !== 'error'))
const failed = computed(() => (capacity.state === 'error' && !capacity.loaded) || (agents.accountsState === 'error' && !accountsLoaded.value))
const retry = () => Promise.all([agents.refreshAccounts(), capacity.load()])

// A pool figure from some of its accounts says so; it is never the pool's figure.
function gaugeLabel(row: BandRow) {
  if (row.left === null) return ''
  const scope = bandReadings(row) ? `, ${bandReadings(row)}` : row.accounts > 1 ? `, ${row.accounts} accounts` : ''
  const kept = row.gauge && row.gauge.yours >= 0.5 ? `, ${pct(row.gauge.yours)} kept for you` : ''
  const base = `${row.pool.name}${scope}: ${Math.round(row.left)}% left${kept}`
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
      <span v-if="computersFailed" class="meta">Account status incomplete</span>
      <span v-else-if="configuredAccounts.length && !loading && !failed" class="meta">{{ readyCount }} of {{ plural(configuredAccounts.length, 'account') }} ready</span>
      <RouterLink class="pacing" to="/agents" data-tip="Work days, Keep for you, nights, Sprint and Hold are on the Agents desk"><AppIcon name="sliders" :size="15" />Pacing</RouterLink>
    </header>

    <p v-if="computersFailed" class="empty partial-notice" role="alert">
      {{ capacity.computersLoaded ? 'Computers could not be refreshed. Showing last known account status.' : 'Computers could not be loaded. Some configured accounts may be missing.' }}
      <button type="button" class="btn sm" @click="retry()">Try again</button>
    </p>
    <div v-if="loading" class="rows" aria-hidden="true">
      <div v-for="n in 2" :key="n" class="row skeleton-row"><span class="skeleton" /><span class="skeleton wide" /></div>
    </div>
    <p v-else-if="failed" class="empty" role="alert">
      Capacity could not be loaded right now. <button type="button" class="btn sm" @click="retry()">Try again</button>
    </p>
    <p v-else-if="!rows.length && !computersFailed" class="empty">
      No accounts yet. Sign in to a harness on a connected computer and it appears here.
      <RouterLink class="btn sm" to="/agents/register-agent"><AppIcon name="monitor" :size="14" />Connect your machine</RouterLink>
    </p>
    <ul v-else class="rows">
      <li v-for="row in rows" :key="row.pool.id" class="row" :data-pool="row.pool.id">
        <div class="name">
          <span class="vendor"><HarnessMark :harness="row.pool.mark || row.pool.id" :size="15" /></span>
          <span class="pool-name">{{ row.pool.name }}</span>
          <span v-if="planText(row)" class="plan-name" :title="planText(row)">{{ planText(row) }}</span>
        </div>
        <CapacityGauge v-if="row.gauge" class="bar" :gauge="row.gauge" :left="row.left ?? 0" :value="row.left ?? 0" :label="gaugeLabel(row)" :ahead="!!row.today?.ahead" :estimated="row.pool.rows.some(r => r.primary?.reading.source === 'estimate')" />
        <span class="left num"><template v-if="row.left !== null"><b>{{ pct(row.left) }}</b> left</template></span>
        <span class="reset num" :data-tip="resetTip(row)">
          <template v-if="row.reset">resets <b>{{ row.reset.label }}</b><span v-if="row.reset.window" class="win"> · {{ row.reset.window }}</span></template>
        </span>
        <p v-if="row.pool.rows.some(r => r.primary?.reading.source === 'estimate' || !r.primary && r.learning)" class="estimate-source">{{ row.pool.rows.filter(r => r.primary?.reading.source === 'estimate' || !r.primary && r.learning).map(r => r.primary ? sourceLine(r, now) : `${consumptionLine(r.learning!)} · ${sourceLine(r, now)}`).join('; ') }}</p>
        <p class="sentence" :class="{ ahead: poolSentence(row.pool, now).ahead }"><span v-if="bandReadings(row)" class="partial">{{ sentenceCase(bandReadings(row)) }}. </span><PlanSentence :sentence="poolSentence(row.pool, now)" /></p>
        <ul class="account-list" :aria-label="`${row.pool.name} accounts`">
          <li v-for="account in accountsByPool.get(row.pool.id) ?? []" :key="account.id" class="account-line" :data-account="account.id" :data-ready="account.readiness.kind">
            <div class="account-head">
              <span class="account-identity">{{ account.identity }}</span>
              <span v-if="account.row.host" class="account-host">{{ account.row.host }}</span>
              <span class="account-state" :class="account.readiness.tone" :data-tip="account.readiness.tip">{{ computersFailed ? capacity.computersLoaded ? `Last known: ${account.readiness.text}` : 'Status not checked' : account.readiness.text }}</span>
            </div>
            <p v-if="account.readiness.hint" class="account-hint">{{ account.readiness.hint }}</p>
            <p v-if="account.readiness.command" class="account-fix">On {{ account.row.host || 'its computer' }}, run <code>{{ account.readiness.command }}</code></p>
            <p v-if="(accountsByPool.get(row.pool.id)?.length ?? 0) > 1" class="account-reading">
              <template v-if="account.capacity.kind === 'bar'">{{ pct(account.capacity.left) }} left {{ account.capacity.window }} · resets {{ account.capacity.resets }}<template v-if="account.capacity.source"> · {{ account.capacity.source }}</template></template>
              <template v-else-if="account.capacity.kind === 'quiet'">{{ account.capacity.text }}</template>
              <template v-else-if="account.capacity.kind === 'offline'">No reading while the computer is offline</template>
              <template v-else>{{ unreportedCapacity(account.harness) }}</template>
            </p>
          </li>
        </ul>
      </li>
    </ul>
    <footer v-if="rows.some(r => r.gauge)" class="band-foot"><CapacityLegend :yours="rows.some(r => (r.gauge?.yours ?? 0) >= 0.5)" /></footer>
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
.partial-notice { margin: 0; color: var(--warn-ink); background: color-mix(in srgb, var(--gold) 8%, transparent); }
.rows { margin: 0; padding: 4px 0; list-style: none; }
.row {
  display: grid; grid-template-columns: 300px minmax(160px, 1fr) 84px 196px; grid-template-areas: "name bar left reset" ". sentence sentence sentence";
  align-items: center; gap: 4px 18px; padding: 12px 20px;
}
.row + .row { border-top: 1px solid var(--line); }
.name { grid-area: name; display: flex; align-items: center; gap: 9px; min-width: 0; }
.vendor { display: grid; place-items: center; flex: none; width: 28px; height: 28px; border-radius: 8px; background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line), 0 1px 2px color-mix(in srgb, var(--shadow-color) 6%, transparent); color: var(--ink); }
.pool-name { color: var(--ink); font-size: 14.5px; font-weight: 650; white-space: nowrap; }
.plan-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: 12.5px; }
.bar { grid-area: bar; }
.left { grid-area: left; justify-self: end; color: var(--ink-3); font-size: 12px; white-space: nowrap; }
.left b { color: var(--ink); font-size: 15px; font-weight: 700; }
.reset { grid-area: reset; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); font-size: 13px; }
.reset b { color: var(--ink); font-weight: 600; }
.win { color: var(--ink-3); }
.num { font-variant-numeric: tabular-nums; }
.estimate-source { grid-column: 2 / -1; margin: 0; font-size: 12px; color: var(--ink-3); }
.sentence { grid-area: sentence; margin: 2px 0 0; color: var(--ink-2); font-size: 13px; line-height: 1.5; text-wrap: pretty; }
.sentence :deep(b) { color: var(--ink); font-weight: 600; }
.sentence :deep(.n) { color: var(--teal-ink); font-weight: 700; font-variant-numeric: tabular-nums; }
.sentence.ahead :deep(.n) { color: var(--warn-ink); }
.partial { color: var(--ink); }
.account-list { grid-column: 1 / -1; display: grid; gap: 9px; margin: 8px 0 0 37px; padding: 0; list-style: none; min-width: 0; }
.account-line { min-width: 0; font-size: 12.5px; line-height: 1.5; }
.account-head { display: flex; flex-wrap: wrap; gap: 3px 10px; align-items: baseline; }
.account-identity { color: var(--ink); font-weight: 550; overflow-wrap: anywhere; }
.account-host { color: var(--ink-3); overflow-wrap: anywhere; }
.account-state { margin-left: auto; color: var(--ink-2); }
.account-state.warn { color: var(--warn-ink); }
.account-hint, .account-fix, .account-reading { margin: 3px 0 0; color: var(--ink-2); overflow-wrap: anywhere; }
.account-hint { color: var(--warn-ink); }
.account-fix code { color: var(--ink); font-size: 12px; user-select: all; }
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
  .estimate-source { grid-column: 1 / -1; }
  .account-list { margin-left: 0; }
  .account-head { align-items: flex-start; }
  .account-state { margin-left: 0; }
  .account-identity { width: 100%; }
  .reset { justify-self: start; font-size: 12.5px; color: var(--ink-3); }
  .band-foot { padding: 10px 14px 12px; gap: 6px 14px; }
}
</style>
