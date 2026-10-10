<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// 7 · 30 · 90 · 180 · 365 days (AEON-994). A radio group with one tab stop;
// arrows, Home and End move the choice. Every option keeps its width.
import { WINDOWS, type WindowDays } from '../../lib/deliveryNumbers'

defineProps<{ modelValue: WindowDays; label: string; unit: string }>()
const emit = defineEmits<{ 'update:modelValue': [days: WindowDays] }>()
function move(event: KeyboardEvent, current: WindowDays) {
  if (event.altKey || event.ctrlKey || event.metaKey) return
  const index = WINDOWS.indexOf(current)
  const next = { ArrowLeft: index - 1, ArrowUp: index - 1, ArrowRight: index + 1, ArrowDown: index + 1, Home: 0, End: WINDOWS.length - 1 }[event.key]
  if (next === undefined) return
  event.preventDefault()
  const days = WINDOWS[Math.max(0, Math.min(WINDOWS.length - 1, next))]
  emit('update:modelValue', days)
  const group = event.currentTarget as HTMLElement
  requestAnimationFrame(() => group.querySelector<HTMLButtonElement>(`[data-days="${days}"]`)?.focus())
}
</script>

<template>
  <span class="win-wrap">
    <span class="seg" role="radiogroup" :aria-label="label" @keydown="move($event, modelValue)">
      <button v-for="days in WINDOWS" :key="days" type="button" role="radio" :data-days="days" :aria-checked="modelValue === days"
        :aria-label="`${days} ${unit}`" :tabindex="modelValue === days ? 0 : -1" @click="emit('update:modelValue', days)">{{ days }}</button>
    </span>
    <span class="win-unit" aria-hidden="true">{{ unit }}</span>
  </span>
</template>

<style scoped>
.win-wrap { display: inline-flex; align-items: center; gap: 6px; }
.seg button { min-width: 38px; padding: 0 8px; font-variant-numeric: tabular-nums; }
.win-unit { font-size: 12px; color: var(--ink-2); }
@container delivery (max-width: 640px) {
  .win-wrap { flex: 1 1 100%; }
  .seg { flex: 1; }
  .seg button { flex: 1; min-width: 0; height: 44px; }
}
</style>
