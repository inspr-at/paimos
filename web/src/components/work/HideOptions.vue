<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed } from 'vue'
import type { ProjectSummary } from '../../lib/api'
import { HIDE_STATES, hiddenStates, hideChoiceCounts, type HideState } from '../../lib/hideStates'
import { statusMeta } from '../../lib/work'
import StatusIcon from './StatusIcon.vue'
const props = defineProps<{ states?: readonly string[]; summary?: ProjectSummary | null; showClosed: boolean }>()
const emit = defineEmits<{ change: [states: HideState[]] }>()
const selected = computed(() => hiddenStates(props.states))
const counts = computed(() => hideChoiceCounts(props.summary))
const total = computed(() => props.showClosed ? 0 : counts.value ? selected.value.reduce((sum, state) => sum + counts.value![state], 0) : null)
const groups = [{ title: 'Finished', states: HIDE_STATES.slice(0, 3) }, { title: 'Exits', states: HIDE_STATES.slice(3) }]
function toggle(state: HideState) {
  if (selected.value.length === 1 && selected.value.includes(state)) return
  emit('change', selected.value.includes(state) ? selected.value.filter(value => value !== state) : hiddenStates([...selected.value, state]))
}
</script>
<template>
  <section class="hide-options" aria-label="What Hide hides">
    <div class="options-head"><h3>What Hide hides</h3><button type="button" class="reset" @click="emit('change', [...HIDE_STATES])">Reset</button></div>
    <div v-for="group in groups" :key="group.title" class="hide-group" role="group" :aria-label="group.title">
      <p class="eyebrow">{{ group.title }}</p>
      <label v-for="state in group.states" :key="state" class="hide-option" :data-state="state">
        <input type="checkbox" :aria-label="statusMeta(state).label" :checked="selected.includes(state)" :disabled="selected.length === 1 && selected.includes(state)" @change="toggle(state)" />
        <StatusIcon :state="state" :size="14" /><span>{{ statusMeta(state).label }}</span><b>{{ counts ? counts[state].toLocaleString('en-GB') : '—' }}</b>
      </label>
    </div>
    <p class="hide-result" role="status" aria-live="polite">{{ total === null ? 'Hidden count unavailable' : `Hides ${total.toLocaleString('en-GB')} tickets now` }}</p>
    <p class="hide-note">At least one status stays chosen.</p>
  </section>
</template>
<style scoped>
.hide-options { padding: 8px; }
.options-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 32px; }
h3 { margin: 0; font-size: 13px; font-weight: 650; }
.reset { border: 0; background: transparent; color: var(--teal-ink); padding: 6px; font-size: 12px; text-decoration: underline; text-underline-offset: 2px; }
.reset:focus-visible { outline: 2px solid var(--teal); outline-offset: 1px; }
.hide-group .eyebrow { margin: 8px 0 2px; }
.hide-option { display: grid; grid-template-columns: 16px 14px minmax(0, 1fr) auto; align-items: center; gap: 8px; height: 36px; padding: 0 6px; color: var(--ink); font-size: 13px; border-radius: 6px; }
.hide-option:hover { background: var(--row-hover); }
.hide-option input { margin: 0; accent-color: var(--teal); width: 16px; height: 16px; }
.hide-option b { color: var(--ink-3); font: 500 12px/1 var(--mono); font-variant-numeric: tabular-nums; }
.hide-result { margin: 8px 0 0; padding-top: 8px; border-top: 1px solid var(--line); color: var(--ink-2); font-size: 12px; }
.hide-note { margin: 4px 0 0; font-size: 11px; color: var(--ink-3); }
@media (max-width: 900px), (pointer: coarse) { .hide-option { height: 44px; font-size: 14px; } .reset { min-height: 44px; } }
</style>
