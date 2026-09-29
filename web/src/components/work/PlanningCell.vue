<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { listCostCell, modelCell, paidCell, tokensCell, type PlanningColumn, type PlanningRow } from '../../lib/planning'

// One planning cell (AEON-329). Model is a quiet label with role, area and the
// registry revision in its tooltip. Tokens, ≈ Cost and Paid read spent / estimate
// flush right, the estimate quieter; with nothing spent yet the estimate stands
// alone, marked as one. Spent above the estimate tints the spent figure and says
// so in the label and tooltip. ≈ Cost always carries its "≈". An empty cell is a
// dash whose tooltip gives the reason.
const props = defineProps<{ column: PlanningColumn; row: PlanningRow }>()
const model = computed(() => props.column === 'model' ? modelCell(props.row) : null)
const figure = computed(() => {
  switch (props.column) {
    case 'tokens': return tokensCell(props.row)
    case 'list_cost': return listCostCell(props.row)
    case 'paid': return paidCell(props.row)
    default: return null
  }
})
// An estimate on its own is marked "~" (≈ Cost already says "≈"); a $0 estimate
// under a subscription is exact.
const mark = computed(() => props.column !== 'list_cost' && figure.value && !figure.value.spent && figure.value.estimated !== '$0' ? '~' : '')
const EMPTY_LABEL: Record<PlanningColumn, string> = { model: 'No model', tokens: 'No tokens', list_cost: 'No cost', paid: 'Nothing paid' }
</script>

<template>
  <span v-if="model" class="plan-model" :class="{ empty: !model.text }" :data-tip="model.tip" :aria-label="`${model.text || EMPTY_LABEL.model}. ${model.tip.replace(/\n/g, '. ')}`">{{ model.text || '—' }}</span>
  <span v-else-if="figure?.label" class="plan-figure" :class="{ over: figure.over, alone: !figure.spent }" role="img" :aria-label="figure.label" :data-tip="figure.tip">
    <span v-if="column === 'list_cost'" class="approx" aria-hidden="true">≈</span>
    <span v-if="figure.spent" class="spent" aria-hidden="true">{{ figure.spent }}</span>
    <template v-if="figure.spent && figure.estimated"><span class="sep" aria-hidden="true">/</span><span class="est" aria-hidden="true">{{ figure.estimated }}</span></template>
    <span v-else-if="figure.estimated" class="est" aria-hidden="true">{{ mark }}{{ figure.estimated }}</span>
  </span>
  <span v-else class="empty" :aria-label="EMPTY_LABEL[column]">—</span>
</template>

<style scoped>
.plan-model { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); font-size: 12.5px; }
.plan-model.empty, .empty { color: var(--ink-3); }
.plan-figure { display: inline-flex; align-items: baseline; justify-content: flex-end; gap: 4px; min-width: 0; max-width: 100%; overflow: hidden; white-space: nowrap; font-size: 12.5px; font-variant-numeric: tabular-nums; color: var(--ink); }
.spent { flex: none; }
.est { min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--ink-3); font-size: 12px; }
.alone .est { color: var(--ink-2); font-size: 12.5px; }
.approx, .sep { flex: none; color: var(--ink-3); }
.sep { font-size: 12px; }
/* Over the estimate: the spent figure takes the warning ink; no bar, no fill. */
.over .spent { color: var(--warn-ink); font-weight: 550; }
</style>
