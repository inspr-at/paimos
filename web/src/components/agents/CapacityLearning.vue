<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import { ago, timeLabel, type CapacityLearning } from '../../lib/capacity'
import AppIcon from '../AppIcon.vue'
import { brand } from '../../lib/brand'

const props = defineProps<{ learning?: CapacityLearning; host?: string; now: number; mayManage?: boolean; saving?: boolean }>()
const emit = defineEmits<{ hours: []; away: [event: Event] }>()
const windows = computed(() => props.learning?.windows.filter(w => w.run_count >= 3 || w.auto_reserve_percent) ?? [])
const show = computed(() => !!props.learning && (windows.value.length || props.learning.suggested_hours || props.learning.correction || props.learning.sleeps_at_night || props.learning.away_suggested))
const name = (kind: string) => kind === '5h' ? '5-hour' : kind === 'weekly' ? 'Weekly' : kind === 'monthly' ? 'Monthly' : 'Window'
</script>

<template>
  <details v-if="show" class="learned">
    <summary><AppIcon name="chevron-right" :size="13" /><span>What {{ brand.short_name }} learned</span></summary>
    <div class="evidence">
      <p v-for="w in windows" :key="`${w.window_kind}/${w.bucket}`">
        <span class="kind">{{ name(w.window_kind) }}</span>
        <span v-if="w.run_count >= 3"><b>~{{ Math.round(w.run_percent ?? 0) }}% a run</b><span class="quiet"> ({{ (w.plus_minus ?? 0) >= 3 ? `±${Math.round(w.plus_minus!)}%, ` : '' }}{{ w.run_count }} runs)</span></span>
        <span v-if="w.auto_reserve_percent"> · Auto keeps <b>~{{ Math.round(w.auto_reserve_percent) }}%</b> for you<span class="quiet"> ({{ w.work_days }} work days)</span></span>
      </p>
      <p v-if="learning?.correction" class="quiet">Estimate was {{ Math.round(Math.abs(learning.correction.points)) }}% too {{ learning.correction.points > 0 ? 'optimistic' : 'cautious' }}; corrected {{ ago(learning.correction.at, now) }}.</p>
      <p v-if="learning?.suggested_hours" class="suggestion">
        <span>You mostly work {{ timeLabel(learning.suggested_hours.start) }}–{{ timeLabel(learning.suggested_hours.end) }}.</span>
        <button v-if="mayManage" type="button" class="btn sm" :disabled="saving" @click="emit('hours')">Use these hours</button>
      </p>
      <p v-if="learning?.away_suggested" class="suggestion"><span>No use from you for two days.</span><button v-if="mayManage" type="button" class="btn sm" @click="emit('away', $event)">I'm away…</button></p>
      <p v-if="learning?.sleeps_at_night" class="quiet">{{ host || 'This computer' }} usually sleeps at night; night work waits for it.</p>
    </div>
  </details>
</template>

<style scoped>
.learned { min-width: 0; color: var(--ink-2); font-size: 12px; }
summary { display: flex; align-items: center; gap: 5px; width: fit-content; min-height: 32px; cursor: pointer; color: var(--ink-3); list-style: none; border-radius: 6px; }
summary::-webkit-details-marker { display: none; }
summary:focus-visible { box-shadow: var(--focus-ring); }
.learned[open] summary :deep(svg) { transform: rotate(90deg); }
.evidence { display: grid; gap: 8px; padding: 8px 12px 12px; border-radius: var(--radius-row); background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line); }
p { margin: 0; line-height: 1.65; overflow-wrap: anywhere; }
b { color: var(--ink); font-weight: 600; }
.kind { margin-right: 10px; color: var(--ink-3); }
.quiet { color: var(--ink-3); }
.suggestion { display: flex; flex-wrap: wrap; gap: 6px 12px; align-items: center; }
@media (max-width: 720px) { summary { min-height: 44px; } .suggestion .btn { min-height: 44px; } }
</style>
