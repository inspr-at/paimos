<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { assistantMoney, budgetPercent, type AssistantFacts, type AssistantPolicy } from '../../lib/assistants'
import { assistantCopy } from '../../lib/assistantsCopy'
const props = defineProps<{ policy: AssistantPolicy; facts: AssistantFacts; daily?: boolean }>()
const words = assistantCopy()
const percent = computed(() => budgetPercent(props.policy, props.facts))
const amount = computed(() => props.facts.spend ? words.weekAmount(assistantMoney(props.facts.spend.amount, props.policy.weekly_budget.unit, true), assistantMoney(props.policy.weekly_budget.amount, props.policy.weekly_budget.unit)) : '—')
const dailyMax = computed(() => Math.max(0, ...(props.facts.spend?.daily.map(day => Number(day.amount)) ?? [])) || 1)
</script>
<template>
  <component :is="daily ? 'div' : 'span'" class="assistant-budget" :class="{ warn: percent !== null && percent >= 80, detailed: daily }">
    <template v-if="percent !== null">
      <span class="budget-track" role="meter" :aria-label="words.thisWeek" :aria-valuenow="percent" :aria-valuetext="amount" aria-valuemin="0" aria-valuemax="100"><span :style="{ width: `${percent}%` }" /></span>
      <span class="budget-amount">{{ amount }}<template v-if="percent >= 80"> · {{ Math.floor(percent) }} %</template></span>
    </template>
    <span v-else class="budget-unknown" :title="words.notAvailableYet" :data-tip="words.notAvailableYet">—</span>
    <div v-if="daily && facts.spend && facts.spend.daily.length === 7" class="daily-spend" role="img" :aria-label="facts.spend.daily.map(day => `${day.day}: ${assistantMoney(day.amount, policy.weekly_budget.unit, true)}`).join('; ')">
      <div v-for="day in facts.spend.daily" :key="day.day"><span class="daily-track"><span :style="{ height: `${Number(day.amount) / dailyMax * 100}%` }" /></span><small>{{ day.day }}</small></div>
    </div>
  </component>
</template>
<style scoped>
.assistant-budget { display: flex; align-items: center; gap: 8px; min-width: 0; font-size: 12px; color: var(--ink-2); font-variant-numeric: tabular-nums; }
.budget-track { display: block; flex: none; width: 48px; height: 4px; border-radius: 2px; background: var(--line-2); overflow: hidden; }
.budget-track > span { display: block; height: 100%; background: var(--ink-2); }
.warn { color: var(--warn-ink); }
.warn .budget-track > span { background: var(--warn); }
.budget-amount { white-space: nowrap; min-width: 12ch; }
.budget-unknown { color: var(--ink-2); }
.detailed { flex-wrap: wrap; }
.detailed .budget-track { flex: 1 1 100%; width: auto; height: 6px; }
.daily-spend { flex-basis: 100%; display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 12px; margin-top: 12px; }
.daily-spend > div { display: grid; gap: 6px; text-align: center; }
.daily-track { display: flex; align-items: flex-end; height: 36px; }
.daily-track > span { display: block; width: 100%; background: var(--ink-2); }
.daily-spend small { color: var(--ink-2); font-size: 12px; }
</style>
