<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { DATE_FIELDS, DATE_PRESETS, dateBounds, shortDate, type DateField, type DateFilter, type DatePreset } from '../../lib/ticketList'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from './FloatingPanel.vue'

// One date filter: which date, then a period that stays relative ("last 7 days"
// in a saved view means the last 7 days whenever it opens) or a custom range of days.
const props = defineProps<{ anchor: HTMLElement | null; value: DateFilter | null }>()
const emit = defineEmits<{ change: [value: DateFilter | null]; close: [restoreFocus: boolean] }>()
const field = ref<DateField>(props.value?.field ?? 'updated')
const from = ref(props.value?.preset ? '' : props.value?.from ?? '')
const to = ref(props.value?.preset ? '' : props.value?.to ?? '')
const custom = computed(() => !!props.value && !props.value.preset)
const rangeError = computed(() => from.value && to.value && to.value < from.value ? 'The end is before the start.' : '')
function setField(value: DateField) {
  field.value = value
  if (props.value) emit('change', { ...props.value, field: value })
}
function preset(value: DatePreset) { emit('change', { field: field.value, preset: value, from: null, to: null }); emit('close', true) }
function applyRange() {
  if (rangeError.value || (!from.value && !to.value)) return
  emit('change', { field: field.value, preset: null, from: from.value || null, to: to.value || null })
  emit('close', true)
}
// The chosen preset's days, as a hint under its name.
function presetHint(value: DatePreset) {
  const bounds = dateBounds({ field: field.value, preset: value, from: null, to: null })
  if (!bounds?.from || !bounds.to) return ''
  const start = new Date(bounds.from), end = new Date(new Date(bounds.to).getTime() - 86_400_000)
  return start.toDateString() === end.toDateString() ? shortDate(start) : `${shortDate(start)} – ${shortDate(end)}`
}
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="300" :tallest="620" label="Filter by date" @close="restore => emit('close', restore)">
    <div class="date-menu">
      <div class="head">
        <p class="eyebrow">Date</p>
        <button v-if="value" type="button" class="clear" @click="emit('change', null); emit('close', true)">Clear</button>
      </div>
      <div class="fields" role="radiogroup" aria-label="Which date">
        <button
          v-for="option in DATE_FIELDS" :key="option.value" type="button" role="radio" class="field-chip" :aria-checked="field === option.value"
          :data-autofocus="field === option.value ? '' : undefined" @click="setField(option.value)"
        >{{ option.label }}</button>
      </div>
      <div class="presets" role="group" aria-label="Period">
        <button
          v-for="option in DATE_PRESETS" :key="option.value" type="button" class="preset" :aria-pressed="value?.preset === option.value && value.field === field"
          @click="preset(option.value)"
        >
          <span class="preset-label">{{ option.label }}</span>
          <span class="preset-hint mono">{{ presetHint(option.value) }}</span>
          <AppIcon v-if="value?.preset === option.value && value.field === field" name="check" :size="13" class="tick" />
        </button>
      </div>
      <form class="range" :class="{ on: custom }" @submit.prevent="applyRange">
        <p class="eyebrow">Between</p>
        <div class="range-row">
          <label class="range-field"><span>From</span><input v-model="from" class="field" type="date" :max="to || undefined" /></label>
          <label class="range-field"><span>To</span><input v-model="to" class="field" type="date" :min="from || undefined" /></label>
        </div>
        <button type="submit" class="btn sm apply" :disabled="!!rangeError || (!from && !to)">Apply range</button>
        <p v-if="rangeError" class="range-error" role="alert">{{ rangeError }}</p>
      </form>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.date-menu { display: grid; gap: 8px; padding: 2px 4px 4px; }
.head { display: flex; align-items: center; justify-content: space-between; min-height: 26px; padding-left: 6px; }
.clear { height: 24px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; }
.clear:hover { background: var(--row-selected); }
.clear:focus-visible { box-shadow: var(--focus-ring); }
.fields { display: flex; flex-wrap: wrap; gap: 5px; padding: 0 2px; }
.field-chip { height: 26px; padding: 0 10px; border: 0; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font-size: 12.5px; font-weight: 600; }
.field-chip:hover { color: var(--ink); }
.field-chip[aria-checked="true"] { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-weight: 600; }
.field-chip:focus-visible { box-shadow: var(--focus-ring); }
.presets { display: grid; gap: 1px; }
.preset { display: flex; align-items: center; gap: 10px; height: 32px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
@media (hover: hover) { .preset:hover { background: var(--row-hover); } }
.preset:focus-visible { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.preset[aria-pressed="true"] { background: var(--row-selected); }
.preset-label { flex: 1; }
.preset-hint { font-size: 11px; color: var(--ink-3); }
.preset[aria-pressed="true"] .preset-hint, .preset:focus-visible .preset-hint { color: var(--ink-2); }
.tick { color: var(--teal); }
.range { display: grid; gap: 6px; margin-top: 2px; padding: 10px 6px 2px; border-top: 1px solid var(--line); }
.range-row { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
.range-field { display: grid; gap: 3px; font-size: 11.5px; color: var(--ink-2); }
.range-field .field { height: 32px; padding: 0 8px; font-size: 13px; font-variant-numeric: tabular-nums; }
.range-error { font-size: 12px; color: var(--danger); }
.apply { justify-self: end; }
</style>
