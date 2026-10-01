<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, useId } from 'vue'
import { listCostCell, modelCell, tokensCell, type PlanningColumn, type PlanningRow } from '../../lib/planning'
import { useModelDisplay } from '../../lib/prefs'
import EffortMeter from './EffortMeter.vue'
import HarnessMark from '../agents/HarnessMark.vue'
import MeasuredMark from './MeasuredMark.vue'

// AEON-511 approved fragment: the measured mark has a fixed figure slot.
// Measured values stand alone; running values retain a quiet estimate.
const { modelDisplay } = useModelDisplay()
const autoId = useId()
const props = defineProps<{ column: PlanningColumn; row: PlanningRow; rowId?: string }>()
const descriptionId = computed(() => props.rowId ? `plan-${props.rowId}-${props.column}` : autoId)
const model = computed(() => props.column === 'model' ? modelCell(props.row, modelDisplay.value) : null)
const figure = computed(() => props.column === 'tokens' ? tokensCell(props.row) : listCostCell(props.row))
const EMPTY_LABEL = { model: 'No model', tokens: 'No tokens', list_cost: 'No cost', paid: 'No cost' }
</script>

<template>
  <span v-if="model" class="plan-model" :class="{ empty: model.state === 'none', planned: model.state === 'planned' }" :data-tip="model.tip" role="img" :aria-label="model.label" :aria-describedby="descriptionId">
    <HarnessMark v-if="model.harness" class="brand" :harness="model.harness" :size="12" />
    <EffortMeter :level="model.effort" :enabled="modelDisplay.effortMeter" :planned="model.state === 'planned'" />
    <span class="model-name" aria-hidden="true">{{ model.state === 'planned' ? '~' : '' }}{{ model.text || '—' }}</span>
    <MeasuredMark v-if="model.state === 'measured'" />
    <span v-if="model.more" class="more" aria-hidden="true">+{{ model.more }}</span>
  </span>
  <span v-else class="plan-figure" :class="{ over: figure.over, alone: figure.state === 'estimated' }" role="img" :aria-describedby="descriptionId" :aria-label="figure.label || EMPTY_LABEL[column]" :data-tip="figure.tip">
    <span v-if="figure.plan" class="plan-tag" aria-hidden="true">plan</span>
    <span v-if="figure.spent" class="spent" aria-hidden="true">{{ figure.spent }}</span>
    <template v-if="figure.state === 'running' && figure.estimated"><span class="sep" aria-hidden="true">/</span><span class="est" aria-hidden="true">~{{ figure.estimated }}</span></template>
    <span v-else-if="figure.state === 'estimated'" class="est" aria-hidden="true">~{{ figure.estimated }}</span>
    <span v-else-if="figure.state === 'none'" class="empty" aria-hidden="true">—</span>
    <span class="slot" aria-hidden="true"><MeasuredMark v-if="figure.state === 'measured'" /></span>
  </span>
  <span :id="descriptionId" class="sr-only">{{ model?.tip || figure.tip }}</span>
</template>

<style scoped>
.plan-model { display: flex; align-items: center; gap: 6px; min-width: 0; overflow: hidden; white-space: nowrap; color: var(--ink-2); font-size: 12.5px; }
.plan-model .brand { flex: none; color: inherit; }
.model-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.more { flex: none; margin-left: -1px; font: 500 11px/1 var(--mono); color: var(--ink-3); font-variant-ligatures: none; }
.plan-model.empty, .plan-model.planned, .empty { color: var(--ink-3); }
.plan-figure { display: inline-flex; align-items: baseline; justify-content: flex-end; gap: 4px; min-width: 0; max-width: 100%; overflow: hidden; white-space: nowrap; font-size: 12.5px; font-variant-numeric: tabular-nums; color: var(--ink); }
.spent { flex: none; }
.est { min-width: 0; overflow: hidden; text-overflow: ellipsis; color: var(--ink-3); font-size: 12px; }
.alone .est { color: var(--ink-2); font-size: 12.5px; }
.sep { flex: none; color: var(--ink-3); font-size: 12px; }
.slot { flex: none; align-self: center; display: inline-grid; place-items: center; width: 12px; height: 12px; color: var(--mark-measured); }
.plan-tag { flex: none; align-self: center; height: 15px; padding: 0 4px; border-radius: 4px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-3); font: 500 10px/15px var(--mono); letter-spacing: .02em; font-variant-ligatures: none; }
/* Only a running overrun takes warning ink. Finished values stay normal. */
.over .spent { color: var(--warn-ink); font-weight: 550; }
</style>
