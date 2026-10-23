<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { assistantConnectLink, assistantDeskLink, assistantMoney, assistantOrder, assistantStatus, ASSISTANT_STATUS_ICON, retainAssistantOrder, policyGroups, unavailableAssistantFacts, type Assistant, type AssistantFacts } from '../../lib/assistants'
import { assistantCopy } from '../../lib/assistantsCopy'
import { relativeTime } from '../../lib/work'
import { vClipTip } from '../../directives/clipTip'
import AppIcon from '../AppIcon.vue'
import HarnessMark from './HarnessMark.vue'
import AssistantBudget from './AssistantBudget.vue'
const props = defineProps<{ items: Assistant[]; facts: Record<string, AssistantFacts>; state: string; error: string; truncated: boolean; canConnect: boolean; canApprove: boolean; now: number; sortEpoch: number; selected: string; owner: string; projectNames?: Record<string, string>; controlId?: string }>()
const emit = defineEmits<{ open: [id: string, anchor: HTMLElement]; retry: [] }>()
const words = assistantCopy(), root = ref<HTMLElement>(), order = ref<string[]>([]), expanded = ref(false)
const pointer = ref(false)
let initialApprovalOrder = false
const storageKey = () => `aeon:assistants:expanded:${props.owner}`
watch(() => props.owner, () => {
  order.value = []; pointer.value = false; initialApprovalOrder = false
  try { expanded.value = localStorage.getItem(storageKey()) === 'true' } catch { expanded.value = false }
}, { immediate: true })
function reorder() {
  if (pointer.value || root.value?.contains(document.activeElement)) return
  order.value = assistantOrder(props.items, props.facts, props.now)
  if (props.items.length && props.items.every(item => props.facts[item.principal_id]?.approvals != null)) initialApprovalOrder = true
}
function admitApprovalOrder() {
  if (!initialApprovalOrder && props.items.length && props.items.every(item => props.facts[item.principal_id]?.approvals != null)) reorder()
}
function leavePointer() { pointer.value = false; admitApprovalOrder() }
function leaveFocus() { void nextTick(admitApprovalOrder) }
watch(() => props.items, items => {
  order.value = order.value.length ? retainAssistantOrder(items, order.value) : assistantOrder(items, props.facts, props.now)
}, { immediate: true })
watch(() => props.facts, admitApprovalOrder, { immediate: true })
watch(() => props.sortEpoch, reorder)
onMounted(() => window.addEventListener('focus', reorder))
onBeforeUnmount(() => window.removeEventListener('focus', reorder))
function showAll() { expanded.value = true; try { localStorage.setItem(storageKey(), 'true') } catch { /* This visit still expands. */ } }
const ordered = computed(() => order.value.flatMap(id => props.items.find(item => item.principal_id === id) ?? []))
const shown = computed(() => expanded.value ? ordered.value : ordered.value.slice(0, 5))
const factsOf = (item: Assistant) => props.facts[item.principal_id] ?? unavailableAssistantFacts()
const metricsPending = computed(() => props.items.every(item => { const facts = factsOf(item); return facts.spend === null && facts.adapter === null }))
const statusOf = (item: Assistant) => assistantStatus(item, factsOf(item), props.now)
const labelOf = (item: Assistant) => statusOf(item) === 'waiting' && !(factsOf(item).approvalsCanDecide ?? props.canApprove) ? words.waitingOther : words.statuses[statusOf(item)]
const approvalsOf = (item: Assistant) => factsOf(item).approvals?.filter(row => !row.decision).length ?? null
const approvalLabel = (item: Assistant) => words.toApprove(approvalsOf(item) ?? 0, factsOf(item).approvalsTruncated)
const vendorOf = (item: Assistant) => item.vendor === 'grok' ? 'Grok' : words.vendorUnknown
function activityLine(item: Assistant) {
  const activity = factsOf(item).lastActivity
  return activity ? `${activity.text} · ${relativeTime(activity.at, { now: props.now, long: true })}` : '—'
}
function openCell(item: Assistant, event: MouseEvent) {
  const button = (event.currentTarget as HTMLElement).closest('.assistant-row')?.querySelector<HTMLButtonElement>('.assistant-open')
  if (button) emit('open', item.principal_id, button)
}
function glance(item: Assistant, compact = false) {
  const budget = item.policy.weekly_budget
  return [item.policy.allowed_projects.length ? item.policy.allowed_projects.map(id => props.projectNames?.[id] ?? id).join(', ') : words.noProjects, ...(compact ? [] : [words.asks.toLowerCase()]), `${assistantMoney(budget.amount, budget.unit)} ${words.week}`].join(' · ')
}
function routineLine(item: Assistant) {
  const routines = factsOf(item).routines
  if (routines === null) return '—'
  if (factsOf(item).routinesTruncated) return words.atLeastRoutines(routines.length)
  if (!routines.length) return words.noRoutines
  const next = routines.filter(row => row.next && !row.paused).sort((a, b) => Date.parse(a.next!) - Date.parse(b.next!))[0]
  return next ? words.routinesNext(routines.length, new Date(next.next!).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', timeZone: next.timezone })) : words.routineCount(routines.length)
}
function adapterLine(item: Assistant) {
  const adapter = factsOf(item).adapter, vendor = vendorOf(item)
  if (!adapter) return '—'
  if (adapter.state === 'setup') return words.connectVendor(vendor)
  if (adapter.state === 'ok') return words.adapterOk(vendor)
  return adapter.failingSince ? words.failing(vendor, new Date(adapter.failingSince).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })) : words.noReceipt(vendor, adapter.lastReceipt ? relativeTime(adapter.lastReceipt, { now: props.now }).replace(/ ago$/, '') : words.notReported)
}
</script>
<template>
  <section ref="root" class="assistants glass-card" :class="{ 'metrics-pending': metricsPending }" aria-labelledby="assistants-heading" @pointerenter="pointer = true" @pointerleave="leavePointer" @focusout="leaveFocus">
    <header class="assistants-head" :aria-owns="controlId"><h2 id="assistants-heading">{{ words.heading(items.length) }}</h2><slot name="actions" /></header>
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
          <button type="button" class="assistant-open" :aria-label="`${item.display_name} · ${labelOf(item)}`" :aria-describedby="`assistant-glance-${item.principal_id} assistant-last-${item.principal_id} assistant-spend-${item.principal_id} assistant-routines-${item.principal_id} assistant-adapter-${item.principal_id}`" :aria-pressed="selected === item.principal_id" :title="`${item.display_name} · ${glance(item)} · ${policyGroups(item.policy.allowed_scopes).join(', ')}`" @click="emit('open', item.principal_id, $event.currentTarget as HTMLElement)">
            <span class="assistant-identity"><span class="assistant-name"><HarnessMark :harness="item.vendor" :size="16" /><strong v-clip-tip>{{ item.display_name }}</strong></span><span v-clip-tip :id="`assistant-glance-${item.principal_id}`" class="assistant-glance">{{ glance(item) }}</span><span v-clip-tip class="folded-facts"><span :title="factsOf(item).lastActivity ? undefined : words.notAvailableYet" :data-tip="factsOf(item).lastActivity ? undefined : words.notAvailableYet">{{ activityLine(item) }}</span> · <span :title="factsOf(item).routines === null ? words.notAvailableYet : undefined" :data-tip="factsOf(item).routines === null ? words.notAvailableYet : undefined">{{ factsOf(item).routines ? factsOf(item).routinesTruncated ? words.atLeastRoutines(factsOf(item).routines!.length) : words.routineCount(factsOf(item).routines!.length) : '—' }}</span></span><span v-if="factsOf(item).spend" v-clip-tip class="phone-facts">{{ words.weekAmount(assistantMoney(factsOf(item).spend!.amount, item.policy.weekly_budget.unit, true), assistantMoney(item.policy.weekly_budget.amount, item.policy.weekly_budget.unit)) }}</span><span v-else v-clip-tip class="phone-facts">{{ glance(item, true) }}</span></span>
            <span class="assistant-state"><AppIcon :name="ASSISTANT_STATUS_ICON[statusOf(item)]" :size="14" />{{ labelOf(item) }}<template v-if="statusOf(item) === 'paused' && factsOf(item).spend && Number(factsOf(item).spend!.amount) >= Number(item.policy.weekly_budget.amount)"> · {{ words.budgetReached }}</template></span>
            <span v-if="factsOf(item).lastActivity" v-clip-tip :id="`assistant-last-${item.principal_id}`" class="last-column">{{ activityLine(item) }}</span><span v-else :id="`assistant-last-${item.principal_id}`" class="last-column" :title="words.notAvailableYet" :data-tip="words.notAvailableYet">—</span>
            <AssistantBudget :id="`assistant-spend-${item.principal_id}`" class="row-budget" :policy="item.policy" :facts="factsOf(item)" />
            <span aria-hidden="true" />
            <span v-if="factsOf(item).routines !== null" v-clip-tip :id="`assistant-routines-${item.principal_id}`" class="routines-column">{{ routineLine(item) }}</span><span v-else :id="`assistant-routines-${item.principal_id}`" class="routines-column" :title="words.notAvailableYet" :data-tip="words.notAvailableYet">—</span>
            <span v-if="factsOf(item).adapter" v-clip-tip :id="`assistant-adapter-${item.principal_id}`" class="adapter-column" :title="adapterLine(item)">{{ adapterLine(item) }}</span><span v-else :id="`assistant-adapter-${item.principal_id}`" class="adapter-column" :title="words.notAvailableYet" :data-tip="words.notAvailableYet">—</span>
          </button>
          <span class="assistant-approvals" aria-live="polite" aria-atomic="true"><RouterLink v-if="approvalsOf(item) || factsOf(item).approvalsTruncated" :to="assistantDeskLink(item.principal_id)" :title="factsOf(item).approvalsTruncated ? words.truncated : undefined" :data-tip="factsOf(item).approvalsTruncated ? words.truncated : undefined" @click.stop>{{ approvalLabel(item) }}</RouterLink><span v-else :title="approvalsOf(item) === null ? words.approvalsUnavailable : undefined" :data-tip="approvalsOf(item) === null ? words.approvalsUnavailable : undefined" @click="openCell(item, $event)">—</span></span>
        </div>
      </div>
      <button v-if="!expanded && items.length > 5" type="button" class="assistant-show-all" @click="showAll">{{ words.showAll(items.length) }}<AppIcon name="chevron" :size="14" /></button>
    </template>
    <p v-if="error && state === 'error'" class="assistant-error" role="alert">{{ words.loadFailed }} {{ error }} <button type="button" class="btn sm" @click="emit('retry')">{{ words.retry }}</button></p>
    <p v-if="truncated" class="assistant-note" role="status">{{ words.truncated }}</p>
  </section>
</template>
<style scoped>
.assistants { container: assistants / inline-size; min-width: 0; margin-bottom: 16px; padding: 0; overflow: clip; }
.assistants-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 64px; padding: 8px 18px; background: var(--chrome); border-radius: var(--radius) var(--radius) 0 0; }
h2 { margin: 0; font-size: 15px; font-weight: 650; color: var(--ink); }
.assistant-columns, .assistant-row, .assistant-open { display: grid; grid-template-columns: minmax(180px, 1.7fr) minmax(132px, 1fr) minmax(110px, 1fr) minmax(160px, 1fr) minmax(88px, .65fr) minmax(100px, .8fr) minmax(110px, 1fr); gap: 14px; align-items: center; padding: 0 16px; }
.metrics-pending { --assistant-columns: minmax(180px, 1.6fr) minmax(132px, 1fr) minmax(220px, 1.8fr) minmax(94px, .65fr) minmax(72px, .5fr) minmax(100px, .8fr) minmax(78px, .6fr); }
.metrics-pending .assistant-columns, .metrics-pending .assistant-row, .metrics-pending .assistant-open { grid-template-columns: var(--assistant-columns); }
.assistant-columns { height: 28px; font-size: 11px; color: var(--ink-2); }
.assistant-row { position: relative; height: 56px; border-bottom: 1px solid var(--line); font-size: 12px; font-variant-numeric: tabular-nums; color: var(--ink-2); }
.assistant-row:first-child { border-top: 1px solid var(--line); }
.assistant-row:last-child { border-bottom: 0; }
.assistant-row:hover, .assistant-row.selected { background: var(--row-hover); }
.assistant-open { position: absolute; inset: 0; width: 100%; height: 100%; border: 0; border-radius: 0; background: transparent; cursor: pointer; color: inherit; font: inherit; text-align: left; }
.assistant-open:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.assistant-row > :not(.assistant-open) { pointer-events: none; min-width: 0; }
.assistant-identity { display: grid; gap: 2px; min-width: 0; }
.assistant-name { display: flex; align-items: center; gap: 7px; min-width: 0; color: var(--ink); }
.assistant-name strong { font-size: 14px; font-weight: 600; line-height: 18px; }
.assistant-name strong, .folded-facts, .phone-facts, .last-column, .routines-column, .adapter-column { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.assistant-name :deep(svg) { width: 16px; height: 16px; }
.assistant-glance { min-width: 0; font-size: 12px; line-height: 15px; white-space: normal; }
.assistant-state { display: flex; align-items: center; gap: 6px; color: var(--ink); }
.assistant-state svg { flex: none; }
[data-status="down"] .assistant-state, [data-status="down"] .adapter-column { color: var(--danger); }
[data-status="waiting"] .assistant-state { font-weight: 600; }
.assistant-approvals { grid-column: 5; white-space: nowrap; }
.assistant-approvals > span { position: relative; pointer-events: auto; cursor: pointer; }
.assistant-approvals a { display: flex; align-items: center; min-height: 44px; position: relative; z-index: 1; pointer-events: auto; color: var(--teal-ink); }
.assistant-show-all { display: flex; align-items: center; justify-content: center; gap: 8px; min-height: 44px; width: 100%; border: 0; background: transparent; color: var(--ink-2); font-size: 12px; cursor: pointer; }
.assistant-empty, .assistant-message { min-height: 56px; padding: 8px 16px; border-top: 1px solid var(--line); color: var(--ink-2); font-size: 12px; }
.assistant-empty { display: flex; align-items: center; gap: 20px; }
.assistant-empty p { margin: 0; flex: 1; line-height: 1.5; }
.assistant-empty .btn { flex: none; }
.assistant-error, .assistant-note { margin: 8px 16px; color: var(--ink-2); font-size: 12px; }
.assistant-error { color: var(--danger); }
.folded-facts, .phone-facts { display: none; }
@container assistants (max-width: 1150px) {
  .assistant-columns, .assistant-row, .assistant-open, .metrics-pending .assistant-columns, .metrics-pending .assistant-row, .metrics-pending .assistant-open { grid-template-columns: minmax(200px, 1.8fr) minmax(132px, 1fr) minmax(158px, 1fr) minmax(88px, .65fr) minmax(110px, 1fr); gap: 12px; }
  .assistant-approvals { grid-column: 4; }
  .last-column, .routines-column { display: none; }
  .assistant-glance { display: none; }
  .folded-facts { display: block; font-size: 12px; }
}
@container assistants (max-width: 850px) {
  .assistant-columns, .assistant-row, .assistant-open, .metrics-pending .assistant-columns, .metrics-pending .assistant-row, .metrics-pending .assistant-open { grid-template-columns: minmax(140px, 1.4fr) minmax(130px, 1fr) minmax(140px, 1fr); gap: 10px; }
  .assistant-columns > :nth-child(5), .assistant-columns > :last-child, .adapter-column, .assistant-approvals { display: none; }
  .assistant-open > [aria-hidden] { display: none; }
}
@container assistants (max-width: 520px) {
  .assistants-head { padding-inline: 12px; min-height: 52px; }
  .assistant-columns { display: none; }
  .assistant-rows { display: grid; }
  .assistant-row, .assistant-row:last-child { height: 64px; display: block; padding: 0 12px; border: 0; border-top: 1px solid var(--line); border-radius: 0; }
  .assistant-identity { position: absolute; left: 12px; right: 12px; top: 4px; gap: 7px; }
  .assistant-name { padding-right: 9em; }
  .assistant-state { position: absolute; right: 12px; top: 4px; max-width: 48%; font-size: 11px; line-height: 14px; white-space: nowrap; }
  .assistant-state svg { display: none; }
  .folded-facts, .row-budget, .assistant-glance { display: none; }
  .phone-facts { display: block; font-size: 12px; white-space: nowrap; }
  .assistant-row:has(.assistant-approvals a) .phone-facts { max-width: calc(50% - 8px); }
  .assistant-approvals:has(a) { display: block; position: absolute; left: calc(50% + 8px); right: 12px; bottom: 0; }
  .assistant-approvals a { min-height: 44px; min-width: 16ch; justify-content: flex-start; font-variant-numeric: tabular-nums; }
  .assistant-open { display: block; padding: 0; border-radius: var(--radius); }
  .assistant-empty { flex-direction: column; align-items: flex-start; gap: 10px; padding: 12px; }
  .assistant-empty .btn { min-height: 44px; }
}
</style>
