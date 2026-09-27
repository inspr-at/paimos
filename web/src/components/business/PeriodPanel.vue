<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { APIError } from '../../lib/api'
import { approvePeriod, getPeriod, type TimeEntry, type TimePeriod } from '../../lib/business'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { absoluteTime } from '../../lib/work'
import { dayKey, periodLabel, timeOfDay, WEEKDAYS } from '../../lib/week'
import { formatClock, formatSpan } from './duration'
import { sumAmounts } from './money'
import { useBusiness } from '../../stores/business'
import AppIcon from './BizIcon.vue'
import MoneyText from './MoneyText.vue'

// One period's entries for review: by ticket and by day, with the exact entry
// set the approval covers. Approving records the admin's decision on that set;
// the server refuses it if an entry was added since this panel read it.
const props = defineProps<{ period: TimePeriod; entries: TimeEntry[]; nodes: Map<string, { key: string; title: string }>; loading: boolean }>()
const emit = defineEmits<{ close: []; approved: [period: TimePeriod]; reload: [] }>()
const business = useBusiness()
const busy = ref(false)
const digest = ref('')
const revision = ref(0)
const who = computed(() => business.nameOf(props.period.principal_id))
const agent = computed(() => business.principals.find(p => p.id === props.period.principal_id)?.kind === 'agent')
const total = computed(() => props.entries.reduce((sum, e) => sum + e.duration_seconds, 0))
const amounts = computed(() => {
  const by = new Map<string, string[]>()
  for (const e of props.entries) by.set(e.currency, [...(by.get(e.currency) ?? []), e.amount])
  return [...by.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([currency, list]) => ({ currency, amount: sumAmounts(list) }))
})
const byTicket = computed(() => {
  const map = new Map<string, { id: string; seconds: number; amounts: Map<string, string[]> }>()
  for (const e of props.entries) {
    const row = map.get(e.node_id) ?? { id: e.node_id, seconds: 0, amounts: new Map() }
    row.seconds += e.duration_seconds
    row.amounts.set(e.currency, [...(row.amounts.get(e.currency) ?? []), e.amount])
    map.set(e.node_id, row)
  }
  return [...map.values()].sort((a, b) => b.seconds - a.seconds).map(row => ({ ...row, totals: [...row.amounts.entries()].map(([currency, list]) => ({ currency, amount: sumAmounts(list) })) }))
})
const byDay = computed(() => {
  const map = new Map<string, TimeEntry[]>()
  for (const e of [...props.entries].sort((a, b) => a.started_at.localeCompare(b.started_at))) { const key = dayKey(new Date(e.started_at)); map.set(key, [...(map.get(key) ?? []), e]) }
  return [...map.entries()].map(([key, list]) => ({ key, date: new Date(list[0].started_at), list, seconds: list.reduce((s, e) => s + e.duration_seconds, 0) }))
})
const running = computed(() => Date.parse(props.period.ends_at) > Date.now())
async function readDigest() {
  try { const fresh = await getPeriod(props.period.id); digest.value = fresh.digest; revision.value = fresh.period.revision }
  catch { digest.value = '' }
}
watch(() => [props.period.id, props.period.revision, props.entries.length], readDigest, { immediate: true })

async function approve() {
  if (busy.value || !digest.value) return
  const ok = await confirmAction({
    title: `Approve ${who.value}’s hours for ${periodLabel(props.period.starts_at, props.period.ends_at)}?`,
    body: `${props.entries.length} ${props.entries.length === 1 ? 'entry' : 'entries'}, ${formatSpan(total.value)}. The approval covers exactly these entries and closes the period: nothing more can be logged in it.${running.value ? ' The period has not ended yet.' : ''}`,
    confirmLabel: 'Approve period',
  })
  if (!ok) return
  busy.value = true
  try {
    const period = await approvePeriod(props.period.id, revision.value, digest.value)
    toast(`Approved ${who.value}’s hours for ${periodLabel(period.starts_at, period.ends_at)}.`)
    emit('approved', period)
  } catch (e) {
    if (e instanceof APIError && e.status === 409) { toast('Entries changed since you opened this period. The latest entries are shown; review them again.', { tone: 'error' }); emit('reload') }
    else toast(`Not approved: ${e instanceof Error ? e.message : 'unknown error'}`, { tone: 'error' })
  } finally { busy.value = false }
}
</script>

<template>
  <aside class="period-panel" aria-label="Period review" tabindex="-1">
    <header class="panel-bar">
      <span class="who-avatar" aria-hidden="true"><AppIcon :name="agent ? 'agent' : 'user'" :size="14" /></span>
      <div class="head-text">
        <h2>{{ who }}</h2>
        <p>{{ periodLabel(period.starts_at, period.ends_at) }}</p>
      </div>
      <span class="spacer" />
      <span v-if="period.state === 'approved'" class="state-chip ok"><AppIcon name="lock" :size="11" />Approved</span>
      <span v-else class="state-chip">{{ running ? 'Running' : 'Ready' }}</span>
      <button type="button" class="icon-btn sm flat" aria-label="Close period review" aria-keyshortcuts="Escape" data-tip="Close · Esc" @click="emit('close')"><AppIcon name="close" :size="15" /></button>
    </header>
    <div class="scroll">
      <div class="metrics">
        <div class="metric"><span class="metric-label">Time</span><b>{{ formatSpan(total) }}</b></div>
        <div class="metric"><span class="metric-label">Entries</span><b>{{ entries.length }}</b></div>
        <div class="metric wide"><span class="metric-label">Amount</span><b><MoneyText v-for="row in amounts" :key="row.currency" :amount="row.amount" :currency="row.currency" /><span v-if="!amounts.length">—</span></b></div>
      </div>
      <p v-if="period.state === 'approved' && period.approval" class="approved-line"><AppIcon name="seal" :size="14" />Approved by {{ business.nameOf(period.approval.approved_by_principal_id) }} · covers {{ formatSpan(period.approval.total_seconds) }} exactly</p>

      <div v-if="loading" class="sk"><span class="skeleton" /><span class="skeleton s" /></div>
      <template v-else-if="entries.length">
        <section class="block" aria-labelledby="by-ticket-title">
          <h3 id="by-ticket-title" class="eyebrow">By ticket</h3>
          <ul class="tickets">
            <li v-for="row in byTicket" :key="row.id" class="t-row">
              <span class="key-badge">{{ nodes.get(row.id)?.key ?? '…' }}</span>
              <span class="t-title">{{ nodes.get(row.id)?.title ?? '' }}</span>
              <span class="t-time mono">{{ formatClock(row.seconds) }}</span>
              <span class="t-amount"><MoneyText v-for="t in row.totals" :key="t.currency" :amount="t.amount" :currency="t.currency" /></span>
            </li>
          </ul>
        </section>
        <section class="block" aria-labelledby="by-day-title">
          <h3 id="by-day-title" class="eyebrow">Entries</h3>
          <div v-for="group in byDay" :key="group.key" class="day">
            <p class="day-head"><span>{{ WEEKDAYS[(group.date.getDay() + 6) % 7] }} {{ group.date.getDate() }}</span><span class="mono">{{ formatClock(group.seconds) }}</span></p>
            <ul class="entries">
              <li v-for="e in group.list" :key="e.id" class="entry">
                <span class="e-time mono" :data-tip="`${absoluteTime(e.started_at)} to ${timeOfDay(e.ended_at)}`">{{ timeOfDay(e.started_at) }}–{{ timeOfDay(e.ended_at) }}</span>
                <span class="e-text"><span class="e-ticket">{{ nodes.get(e.node_id)?.key ?? '' }}</span><span v-if="e.note" class="e-note">{{ e.note }}</span><span v-if="e.source === 'agent_run'" class="agent-chip">Agent run</span></span>
                <span class="e-dur mono">{{ formatClock(e.duration_seconds) }}</span>
              </li>
            </ul>
          </div>
        </section>
      </template>
      <p v-else class="empty-line">No entries in this period.</p>
      <p v-if="digest && period.state === 'open'" class="digest">Entries digest <code>{{ digest.slice(0, 8) }}…{{ digest.slice(-6) }}</code></p>
    </div>
    <footer v-if="period.state === 'open' && business.admin" class="actions">
      <p class="note">{{ running ? 'Still running: approving closes it now.' : 'Approving closes the period for new entries.' }}</p>
      <button type="button" class="btn primary" :disabled="busy || !digest || loading" @click="approve"><AppIcon name="seal" :size="13" />{{ busy ? 'Approving…' : 'Approve period' }}</button>
    </footer>
  </aside>
</template>

<style scoped>
.period-panel {
  position: fixed; z-index: 15; top: calc(var(--header-h) + 10px); right: 10px; bottom: calc(var(--footer-h) + 10px); width: min(520px, calc(100vw - 20px));
  display: flex; flex-direction: column; min-height: 0; outline: none; border-radius: var(--radius); border: 1px solid var(--glass-edge);
  background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow);
  -webkit-backdrop-filter: blur(20px) saturate(1.15); backdrop-filter: blur(20px) saturate(1.15);
}
@media (min-width: 1100px) { .period-panel { width: var(--panel-w); } }
@media (prefers-reduced-motion: no-preference) {
  .period-panel { animation: panel-in .22s cubic-bezier(.2, .7, .2, 1); }
  @keyframes panel-in { from { opacity: 0; transform: translateX(24px); } to { opacity: 1; transform: none; } }
}
.panel-bar { display: flex; align-items: center; gap: 10px; min-height: 60px; padding: 8px 10px 8px 16px; border-bottom: 1px solid var(--line); flex-shrink: 0; }
.who-avatar { display: grid; place-items: center; width: 34px; height: 34px; border-radius: 50%; background: var(--avatar-bg); box-shadow: 0 0 0 1px var(--glass-rim); color: var(--teal-ink); }
.head-text h2 { font-size: 16px; font-weight: 650; }
.head-text p { font-size: 12.5px; color: var(--ink-2); }
.spacer { flex: 1; }
.state-chip { display: inline-flex; align-items: center; gap: 5px; height: 22px; padding: 0 9px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font: 600 10.5px/1 var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.state-chip.ok { background: rgba(47, 122, 90, .1); box-shadow: inset 0 0 0 1px rgba(47, 122, 90, .3); color: var(--ok); }
.scroll { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; padding: 18px 22px 24px; }
.metrics { display: grid; grid-template-columns: 1fr 1fr 2fr; gap: 8px; }
.metric { display: grid; gap: 4px; padding: 10px 12px; border-radius: 10px; background: var(--code-bg); }
.metric-label { font-size: 11.5px; color: var(--ink-2); }
.metric :deep(.money .cur) { color: var(--ink-2); }
.metric b { display: flex; flex-wrap: wrap; gap: 4px 10px; font: 600 15px/1.2 var(--mono); color: var(--ink); font-variant-numeric: tabular-nums; }
.approved-line { display: flex; align-items: center; gap: 8px; margin-top: 12px; padding: 8px 12px; border-radius: 10px; background: rgba(47, 122, 90, .08); color: var(--ok); font-size: 12.5px; }
.block { margin-top: 22px; }
.block .eyebrow { margin-bottom: 8px; }
.tickets, .entries { margin: 0; padding: 0; list-style: none; }
.t-row { display: grid; grid-template-columns: max-content minmax(0, 1fr) 56px auto; align-items: center; gap: 10px; min-height: 36px; border-bottom: 1px solid var(--line); font-size: 13px; }
.t-row:last-child { border-bottom: 0; }
.t-title { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.t-time { text-align: right; font-size: 12.5px; }
.t-amount { display: grid; justify-items: end; font-size: 12.5px; }
.day { margin-top: 10px; }
.day-head { display: flex; justify-content: space-between; padding: 4px 2px; font-size: 12px; font-weight: 650; color: var(--ink-2); border-bottom: 1px solid var(--line-2); }
.day-head .mono { font-size: 11.5px; }
.entry { display: grid; grid-template-columns: 96px minmax(0, 1fr) 48px; align-items: center; gap: 10px; min-height: 32px; font-size: 12.5px; border-bottom: 1px solid var(--line); }
.e-time { font-size: 11.5px; color: var(--ink-2); }
.e-text { display: flex; align-items: center; gap: 8px; min-width: 0; }
.e-ticket { font: 600 11px/1 var(--mono); color: var(--teal-ink); font-variant-ligatures: none; }
.e-note { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); }
.agent-chip { flex-shrink: 0; height: 17px; padding: 0 6px; border-radius: 999px; background: var(--chip-bg); color: var(--ink-2); font: 600 9.5px/17px var(--mono); letter-spacing: .06em; text-transform: uppercase; }
.e-dur { text-align: right; font-size: 11.5px; }
.digest { margin-top: 16px; font-size: 12px; color: var(--ink-3); }
.digest code { padding: 2px 6px; border-radius: 6px; background: var(--code-bg); font-size: 11.5px; color: var(--ink-2); }
.empty-line { margin-top: 16px; font-size: 13px; color: var(--ink-3); }
.sk { display: grid; gap: 12px; margin-top: 20px; } .sk .s { width: 60%; }
.actions { flex-shrink: 0; display: flex; align-items: center; gap: 12px; padding: 10px 14px 12px 20px; border-top: 1px solid var(--line); background: var(--surface-raised-2); border-radius: 0 0 var(--radius) var(--radius); }
.note { flex: 1; font-size: 12.5px; color: var(--ink-2); }
@media (max-width: 720px) {
  .period-panel { z-index: 40; inset: 0; width: auto; height: 100dvh; border-radius: 0; border: 0; background: var(--canvas); }
  .panel-bar .icon-btn { width: 44px; height: 44px; }
  .scroll { padding: 16px; }
  .metrics { grid-template-columns: 1fr 1fr; }
  .metric.wide { grid-column: 1 / -1; }
  .actions { flex-wrap: wrap; border-radius: 0; padding: 10px 12px calc(10px + env(safe-area-inset-bottom)); }
  .actions .btn { flex: 1; height: 44px; }
}
</style>
