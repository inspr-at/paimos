<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { assistantConnectLink, assistantDeskLink, assistantMoney, assistantOrder, assistantStatus, ASSISTANT_STATUS_ICON, retainAssistantOrder, policyGroups, unavailableAssistantFacts, type Assistant, type AssistantFacts } from '../../lib/assistants'
import { assistantCopy } from '../../lib/assistantsCopy'
import { relativeTime } from '../../lib/work'
import { vClipTip } from '../../directives/clipTip'
import AppIcon from '../AppIcon.vue'
import ProviderMark from './ProviderMark.vue'
import AssistantBudget from './AssistantBudget.vue'
const props = defineProps<{ items: Assistant[]; facts: Record<string, AssistantFacts>; state: string; error: string; truncated: boolean; canConnect: boolean; canApprove: boolean; now: number; sortEpoch: number; selected: string; owner: string }>()
const emit = defineEmits<{ open: [id: string, anchor: HTMLElement]; retry: [] }>()
const words = assistantCopy(), root = ref<HTMLElement>(), order = ref<string[]>([]), expanded = ref(false)
let pointer = false
const storageKey = () => `aeon:assistants:expanded:${props.owner}`
watch(() => props.owner, () => {
  order.value = []; pointer = false
  try { expanded.value = localStorage.getItem(storageKey()) === 'true' } catch { expanded.value = false }
}, { immediate: true })
function reorder() {
  if (pointer || root.value?.contains(document.activeElement)) return
  order.value = assistantOrder(props.items, props.facts, props.now)
}
watch(() => props.items, items => {
  order.value = order.value.length ? retainAssistantOrder(items, order.value) : assistantOrder(items, props.facts, props.now)
}, { immediate: true })
watch(() => props.sortEpoch, reorder)
onMounted(() => window.addEventListener('focus', reorder))
onBeforeUnmount(() => window.removeEventListener('focus', reorder))
function showAll() { expanded.value = true; try { localStorage.setItem(storageKey(), 'true') } catch { /* This visit still expands. */ } }
const ordered = computed(() => order.value.flatMap(id => props.items.find(item => item.principal_id === id) ?? []))
const shown = computed(() => expanded.value ? ordered.value : ordered.value.slice(0, 5))
const factsOf = (item: Assistant) => props.facts[item.principal_id] ?? unavailableAssistantFacts()
const statusOf = (item: Assistant) => assistantStatus(item, factsOf(item), props.now)
const labelOf = (item: Assistant) => statusOf(item) === 'waiting' && !props.canApprove ? words.waitingOther : words.statuses[statusOf(item)]
const approvalsOf = (item: Assistant) => factsOf(item).approvals?.filter(row => !row.decision).length ?? null
const vendorOf = (item: Assistant) => item.vendor === 'grok' ? 'Grok' : words.vendorUnknown
function glance(item: Assistant) {
  const budget = item.policy.weekly_budget
  return [item.policy.allowed_projects.length ? `${item.policy.allowed_projects.length} ${words.projects.toLowerCase()}` : words.noProjects, words.asks.toLowerCase(), `${assistantMoney(budget.amount, budget.unit)} ${words.week}`].join(' · ')
}
function adapterLine(item: Assistant) {
  const adapter = factsOf(item).adapter, vendor = vendorOf(item)
  if (!adapter) return `${vendor} · ${words.unavailable.toLowerCase()}`
  if (adapter.state === 'setup') return words.connectVendor(vendor)
  if (adapter.state === 'ok') return words.adapterOk(vendor)
  return adapter.failingSince ? words.failing(vendor, new Date(adapter.failingSince).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })) : words.noReceipt(vendor, adapter.lastReceipt ? relativeTime(adapter.lastReceipt, props.now) : words.notReported)
}
</script>
<template>
  <section ref="root" class="assistants" aria-labelledby="assistants-heading" @pointerenter="pointer = true" @pointerleave="pointer = false">
    <header class="assistants-head"><h2 id="assistants-heading">{{ words.heading(items.length) }}</h2></header>
    <div v-if="state === 'loading' && !items.length" class="assistant-message" role="status">{{ words.loading }}</div>
    <div v-else-if="state === 'forbidden'" class="assistant-message">{{ words.noAccess }}</div>
    <div v-else-if="state === 'unavailable'" class="assistant-message">{{ words.unavailable }}</div>
    <div v-else-if="state === 'ready' && !items.length" class="assistant-empty">
      <p>{{ words.empty }}</p><RouterLink v-if="canConnect" class="btn sm" :to="assistantConnectLink"><AppIcon name="plus" />{{ words.connect }}</RouterLink>
    </div>
    <template v-if="items.length">
      <div class="assistant-columns" aria-hidden="true"><span>{{ words.assistant }}</span><span>{{ words.status }}</span><span class="last-column">{{ words.lastActivity }}</span><span>{{ words.thisWeek }}</span><span>{{ words.tabs.approvals }}</span><span class="routines-column">{{ words.tabs.routines }}</span><span>{{ words.adapter }}</span></div>
      <div class="assistant-rows">
        <div v-for="item in shown" :key="item.principal_id" class="assistant-row" :class="{ selected: selected === item.principal_id }" :data-assistant="item.principal_id" :data-status="statusOf(item)">
          <button type="button" class="assistant-open" :aria-label="`${item.display_name} · ${labelOf(item)}`" :aria-pressed="selected === item.principal_id" @click="emit('open', item.principal_id, $event.currentTarget as HTMLElement)" />
          <div class="assistant-identity"><div class="assistant-name"><ProviderMark :provider="item.vendor === 'grok' ? 'xai' : 'unknown'" /><strong v-clip-tip>{{ item.display_name }}</strong></div><span v-clip-tip class="assistant-glance" :title="`${glance(item)} · ${policyGroups(item.policy.allowed_scopes).join(', ')}`">{{ glance(item) }}</span><span v-clip-tip class="folded-facts">{{ factsOf(item).lastActivity ? `${factsOf(item).lastActivity!.text} ${relativeTime(factsOf(item).lastActivity!.at, now)}` : words.notReported }} · {{ factsOf(item).routines ? words.routineCount(factsOf(item).routines!.length) : words.routinesUnavailable }}</span><span class="phone-facts"><template v-if="factsOf(item).spend">{{ words.weekAmount(assistantMoney(factsOf(item).spend!.amount, item.policy.weekly_budget.unit, true), assistantMoney(item.policy.weekly_budget.amount, item.policy.weekly_budget.unit)) }}</template><template v-else>{{ labelOf(item) }}</template></span></div>
          <span class="assistant-state"><AppIcon :name="ASSISTANT_STATUS_ICON[statusOf(item)]" :size="14" />{{ labelOf(item) }}<template v-if="statusOf(item) === 'paused' && factsOf(item).spend && Number(factsOf(item).spend!.amount) >= Number(item.policy.weekly_budget.amount)"> · {{ words.budgetReached }}</template></span>
          <span v-clip-tip class="last-column">{{ factsOf(item).lastActivity ? `${factsOf(item).lastActivity!.text} ${relativeTime(factsOf(item).lastActivity!.at, now)}` : words.unavailable }}</span>
          <AssistantBudget class="row-budget" :policy="item.policy" :facts="factsOf(item)" />
          <span class="assistant-approvals"><RouterLink v-if="approvalsOf(item)" :to="assistantDeskLink(item.principal_id)" @click.stop>{{ words.toApprove(approvalsOf(item)!) }}</RouterLink><span v-else>{{ approvalsOf(item) === 0 ? '—' : words.unavailable }}</span></span>
          <span v-clip-tip class="routines-column">{{ factsOf(item).routines === null ? words.unavailable : factsOf(item).routines!.length ? words.routineCount(factsOf(item).routines!.length) : words.noRoutines }}</span>
          <span v-clip-tip class="adapter-column" :title="adapterLine(item)">{{ adapterLine(item) }}</span>
        </div>
      </div>
      <button v-if="!expanded && items.length > 5" type="button" class="assistant-show-all" @click="showAll">{{ words.showAll(items.length) }}<AppIcon name="chevron" :size="14" /></button>
    </template>
    <p v-if="error && state === 'error'" class="assistant-error" role="alert">{{ words.loadFailed }} {{ error }} <button type="button" class="btn sm" @click="emit('retry')">{{ words.retry }}</button></p>
    <p v-if="truncated" class="assistant-note" role="status">{{ words.truncated }}</p>
  </section>
</template>
<style scoped>
.assistants { container: assistants / inline-size; min-width: 0; margin-bottom: 16px; }
.assistants-head { display: flex; align-items: center; min-height: 44px; padding: 0 16px; }
h2 { margin: 0; font-size: 15px; font-weight: 650; color: var(--ink); }
.assistant-columns, .assistant-row { display: grid; grid-template-columns: minmax(180px, 1.7fr) minmax(132px, 1fr) minmax(110px, 1fr) minmax(160px, 1fr) minmax(88px, .65fr) minmax(100px, .8fr) minmax(110px, 1fr); gap: 14px; align-items: center; padding: 0 16px; }
.assistant-columns { height: 28px; font-size: 11px; color: var(--ink-2); }
.assistant-row { position: relative; height: 56px; border-bottom: 1px solid var(--line); font-size: 12px; font-variant-numeric: tabular-nums; color: var(--ink-2); }
.assistant-row:first-child { border-top: 1px solid var(--line); }
.assistant-row:hover, .assistant-row.selected { background: var(--row-hover); }
.assistant-open { position: absolute; inset: 0; width: 100%; height: 100%; padding: 0; border: 0; border-radius: 0; background: transparent; cursor: pointer; }
.assistant-open:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.assistant-row > :not(.assistant-open) { pointer-events: none; min-width: 0; }
.assistant-identity { display: grid; gap: 4px; }
.assistant-name { display: flex; align-items: center; gap: 7px; min-width: 0; color: var(--ink); }
.assistant-name strong { font-size: 14px; font-weight: 600; }
.assistant-name :deep(svg) { width: 16px; height: 16px; }
.assistant-glance { font-size: 12px; }
.assistant-state { display: flex; align-items: center; gap: 6px; color: var(--ink); }
.assistant-state svg { flex: none; }
[data-status="down"] .assistant-state, [data-status="down"] .adapter-column { color: var(--danger); }
[data-status="waiting"] .assistant-state { font-weight: 600; }
.assistant-approvals { white-space: nowrap; }
.assistant-approvals a { display: inline-flex; align-items: center; min-height: 44px; position: relative; z-index: 1; pointer-events: auto; color: var(--teal-ink); }
.assistant-show-all { display: flex; align-items: center; justify-content: center; gap: 8px; min-height: 44px; width: 100%; border: 0; background: transparent; color: var(--ink-2); font-size: 12px; cursor: pointer; }
.assistant-empty, .assistant-message { min-height: 56px; padding: 8px 16px; border: 1px solid var(--line); border-radius: var(--radius); color: var(--ink-2); font-size: 12px; }
.assistant-empty { display: flex; align-items: center; gap: 20px; }
.assistant-empty p { margin: 0; flex: 1; line-height: 1.5; }
.assistant-empty .btn { flex: none; }
.assistant-error, .assistant-note { margin: 8px 16px; color: var(--ink-2); font-size: 12px; }
.assistant-error { color: var(--danger); }
.folded-facts, .phone-facts { display: none; }
@container assistants (max-width: 1150px) {
  .assistant-columns, .assistant-row { grid-template-columns: minmax(200px, 1.8fr) minmax(132px, 1fr) minmax(158px, 1fr) minmax(88px, .65fr) minmax(110px, 1fr); gap: 12px; }
  .last-column, .routines-column { display: none; }
  .assistant-glance { display: none; }
  .folded-facts { display: block; font-size: 12px; }
}
@container assistants (max-width: 850px) {
  .assistant-columns, .assistant-row { grid-template-columns: minmax(140px, 1.4fr) minmax(130px, 1fr) minmax(140px, 1fr); gap: 10px; }
  .assistant-columns > :nth-child(5), .assistant-columns > :last-child, .adapter-column, .assistant-approvals { display: none; }
}
@container assistants (max-width: 520px) {
  .assistants-head { padding-inline: 4px; }
  .assistant-columns { display: none; }
  .assistant-rows { display: grid; gap: 6px; }
  .assistant-row { height: 64px; display: block; padding: 0 12px; border: 1px solid var(--line); border-radius: var(--radius); }
  .assistant-identity { position: absolute; left: 12px; right: 12px; top: 10px; gap: 7px; }
  .assistant-name { padding-right: 9em; }
  .assistant-state { position: absolute; right: 12px; top: 12px; max-width: 48%; font-size: 11px; white-space: nowrap; }
  .assistant-state svg { display: none; }
  .folded-facts, .row-budget, .assistant-glance { display: none; }
  .phone-facts { display: block; font-size: 12px; white-space: nowrap; }
  .assistant-approvals:has(a) { display: block; position: absolute; right: 12px; bottom: 1px; }
  .assistant-approvals a { min-height: 38px; }
  .assistant-open { border-radius: var(--radius); }
  .assistant-empty { flex-direction: column; align-items: flex-start; gap: 10px; padding: 12px; }
  .assistant-empty .btn { min-height: 44px; }
}
</style>
