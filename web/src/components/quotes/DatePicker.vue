<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, useId } from 'vue'
import { addDays, addMonths, formatDocDate, longDate, monthGrid, monthTitle, parseDocDate, todayIso, WEEKDAYS } from '../../lib/quotes/dates'
import FloatingPanel from '../work/FloatingPanel.vue'
import QuoteIcon from './inspector/QuoteIcon.vue'

// A date on a quote, read as the document prints it (24.10.2026). On the paper
// it looks like the document's own text; in the panel like a field. Pressing it
// opens a month grid: arrows move a day or a week, Page Up/Down a month, Enter
// picks, Escape closes; a typed date works too. No native date chrome anywhere.
const props = withDefaults(defineProps<{ modelValue: string; label: string; variant?: 'paper' | 'field'; disabled?: boolean; invalid?: boolean }>(), { variant: 'field', disabled: false, invalid: false })
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const id = useId()
const trigger = ref<HTMLButtonElement>()
const anchor = ref<HTMLElement | null>(null)
const shown = ref(props.modelValue)
const cursor = ref(props.modelValue)
const typed = ref('')
const bad = ref(false)
const text = computed(() => formatDocDate(props.modelValue) || 'Set a date')
const days = computed(() => monthGrid(shown.value))
const today = todayIso()
// The one day in the grid that Tab reaches: the cursor, or the 1st when the month changed.
const tabDay = computed(() => days.value.some(d => d.value === cursor.value) ? cursor.value : days.value.find(d => d.inMonth)!.value)
function open() {
  if (props.disabled) return
  const start = props.modelValue || today
  shown.value = start; cursor.value = start; typed.value = ''; bad.value = false
  anchor.value = trigger.value ?? null
  void nextTick(() => focusCursor())
}
function close(restore: boolean) { anchor.value = null; if (restore) trigger.value?.focus() }
function pick(value: string) { if (value !== props.modelValue) emit('update:modelValue', value); close(true) }
function focusCursor() { document.querySelector<HTMLButtonElement>(`[data-picker="${id}"] [data-day="${cursor.value}"]`)?.focus() }
function move(to: string) {
  cursor.value = to
  if (to.slice(0, 7) !== shown.value.slice(0, 7)) shown.value = to
  void nextTick(focusCursor)
}
function gridKeys(event: KeyboardEvent) {
  const step: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 }
  if (event.key in step) { event.preventDefault(); move(addDays(cursor.value, step[event.key]!)) }
  else if (event.key === 'PageUp' || event.key === 'PageDown') { event.preventDefault(); move(addMonths(cursor.value, event.key === 'PageUp' ? -1 : 1)) }
  else if (event.key === 'Home' || event.key === 'End') {
    event.preventDefault()
    const weekday = (days.value.findIndex(d => d.value === cursor.value) % 7 + 7) % 7
    move(addDays(cursor.value, event.key === 'Home' ? -weekday : 6 - weekday))
  }
}
function commitTyped() {
  const value = parseDocDate(typed.value)
  if (!value) { bad.value = true; return }
  pick(value)
}
</script>

<template>
  <span class="date-picker" :class="`as-${variant}`">
    <button
      ref="trigger" type="button" class="date-trigger" :class="{ invalid }" :disabled="disabled" aria-haspopup="dialog" :aria-expanded="!!anchor"
      :aria-label="`${label}: ${modelValue ? longDate(modelValue) : 'no date'}`" :data-tip="variant === 'paper' && !disabled ? 'Change the date' : undefined" @click="anchor ? close(false) : open()"
    >
      <span class="date-text" lang="de">{{ text }}</span>
      <QuoteIcon v-if="variant === 'field'" name="calendar" :size="14" class="date-icon" />
    </button>
    <FloatingPanel v-if="anchor" :anchor="anchor" :width="272" :label="label" @close="close">
      <div class="picker" :data-picker="id">
        <header class="picker-head">
          <button type="button" class="nav" aria-label="Previous month" data-tip="Previous month · Page Up" @click="shown = addMonths(shown, -1)"><QuoteIcon name="chevron-left" :size="14" /></button>
          <p class="month" aria-live="polite">{{ monthTitle(shown) }}</p>
          <button type="button" class="nav" aria-label="Next month" data-tip="Next month · Page Down" @click="shown = addMonths(shown, 1)"><QuoteIcon name="chevron-right" :size="14" /></button>
        </header>
        <div class="grid" role="grid" :aria-label="monthTitle(shown)" @keydown="gridKeys">
          <div class="week head" role="row"><span v-for="day in WEEKDAYS" :key="day" class="weekday" role="columnheader" :aria-label="day">{{ day }}</span></div>
          <div v-for="w in 6" :key="w" class="week" role="row">
            <span v-for="day in days.slice((w - 1) * 7, w * 7)" :key="day.value" role="gridcell" :aria-selected="day.value === modelValue">
              <button
                type="button" class="day" :class="{ out: !day.inMonth, today: day.value === today, chosen: day.value === modelValue }" :data-day="day.value"
                :tabindex="day.value === tabDay ? 0 : -1" :data-autofocus="day.value === tabDay || undefined" :aria-label="longDate(day.value)" :aria-current="day.value === today ? 'date' : undefined" @click="pick(day.value)" @focus="cursor = day.value"
              >{{ day.day }}</button>
            </span>
          </div>
        </div>
        <form class="typed" @submit.prevent="commitTyped">
          <label class="typed-label" :for="`${id}-typed`">Date</label>
          <input :id="`${id}-typed`" v-model="typed" class="typed-field" autocomplete="off" inputmode="numeric" placeholder="24.10.2026" lang="de" :aria-invalid="bad || undefined" @input="bad = false" />
          <button type="submit" class="btn sm">Set</button>
        </form>
        <p v-if="bad" class="typed-bad" role="alert">Use a date like 24.10.2026.</p>
        <button type="button" class="today-btn" @click="pick(today)">Today, {{ formatDocDate(today) }}</button>
      </div>
    </FloatingPanel>
  </span>
</template>

<style scoped>
.date-picker { display: inline-flex; min-width: 0; }
.date-trigger { display: inline-flex; align-items: center; gap: 8px; min-width: 0; border: 0; background: transparent; color: inherit; font: inherit; text-align: left; cursor: pointer; }
.date-trigger:disabled { cursor: default; }
/* On the paper: the date reads like the text around it; a dotted line hints it can change. */
.as-paper .date-trigger { padding: 0; margin: 0; line-height: inherit; text-decoration: underline dotted color-mix(in srgb, var(--shadow-color) 35%, transparent); text-underline-offset: 2px; }
.as-paper .date-trigger:disabled { text-decoration: none; }
.as-paper .date-trigger:hover:not(:disabled) { text-decoration-color: currentColor; }
.as-paper .date-trigger:focus-visible { outline: 1px solid color-mix(in srgb, var(--primary-line) 50%, transparent); outline-offset: 2px; box-shadow: none; border-radius: 1px; }
/* In the panel: a field like the others. */
.as-field { width: 100%; }
.as-field .date-trigger { justify-content: space-between; width: 100%; height: 30px; padding: 0 8px; border: 1px solid var(--glass-edge); border-radius: 8px; background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); color: var(--ink); font: 500 13px/1 var(--mono); font-variant-numeric: tabular-nums; }
@media (max-width: 600px) { .as-field .date-trigger { height: 44px; } }
.as-field .date-trigger:focus-visible { box-shadow: var(--focus-ring); }
.as-field .date-trigger:disabled { color: var(--ink-3); background: var(--surface-2); }
.as-field .date-trigger.invalid { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); }
.date-icon { flex-shrink: 0; color: var(--ink-3); }
.picker { display: grid; gap: 8px; padding: 4px; }
.picker-head { display: flex; align-items: center; justify-content: space-between; }
.month { font-size: 13.5px; font-weight: 650; color: var(--ink); }
.nav { display: grid; place-items: center; width: 28px; height: 28px; padding: 0; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); }
.nav:hover { background: var(--row-hover); color: var(--ink); }
.nav:focus-visible { box-shadow: var(--focus-ring); }
.grid { display: grid; gap: 2px; }
.week { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 2px; }
.weekday { display: grid; place-items: center; height: 22px; font: 500 10.5px/1 var(--mono); color: var(--ink-3); }
.week > span { display: grid; }
.day { height: 32px; padding: 0; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font: 500 12.5px/1 var(--font); font-variant-numeric: tabular-nums; }
.day.out { color: var(--ink-3); }
@media (hover: hover) { .day:hover { background: var(--row-hover); } }
.day.today { box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.day.chosen { background: var(--seg-on); color: var(--teal-ink); font-weight: 700; box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.day:focus-visible { box-shadow: var(--focus-ring); }
.typed { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 8px; padding-top: 6px; border-top: 1px solid var(--line); }
.typed-label { font-size: 12.5px; color: var(--ink-2); }
.typed-field { width: 100%; height: 30px; padding: 0 8px; border: 1px solid var(--glass-edge); border-radius: 8px; background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); color: var(--ink); font: 500 13px/1 var(--mono); }
.typed-field:focus { outline: none; box-shadow: var(--focus-ring); }
.typed-field[aria-invalid="true"] { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); }
.typed-bad { font-size: 12px; color: var(--danger); }
.today-btn { justify-self: start; height: 26px; padding: 0 8px; border: 0; border-radius: 7px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; }
.today-btn:hover { background: var(--row-hover); }
.today-btn:focus-visible { box-shadow: var(--focus-ring); }
@media print { .as-paper .date-trigger { text-decoration: none; } }
</style>
