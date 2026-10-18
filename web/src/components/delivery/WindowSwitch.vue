<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// 7 · 30 · 90 · 180 · 365 days (AEON-994), a ViewSwitch (AEON-1453): a radio group with
// one tab stop; arrows, Home and End move the choice. Every option keeps its width, and
// right-click or a long press makes one the window this page opens with.
import { computed } from 'vue'
import ViewSwitch from '../ViewSwitch.vue'
import { WINDOWS, type WindowDays } from '../../lib/deliveryNumbers'

const props = defineProps<{ modelValue: WindowDays; label: string; unit: string; defaultValue?: WindowDays | null }>()
const emit = defineEmits<{ 'update:modelValue': [days: WindowDays]; setDefault: [days: WindowDays | null] }>()
const options = computed(() => WINDOWS.map(days => ({ id: String(days), label: String(days), ariaLabel: `${days} ${props.unit}`, data: { 'data-days': String(days) } })))
const days = (id: string) => WINDOWS.find(value => String(value) === id)
function select(id: string) { const value = days(id); if (value) emit('update:modelValue', value) }
function setDefault(id: string | null) { emit('setDefault', id === null ? null : days(id) ?? null) }
</script>

<template>
  <span class="win-wrap">
    <ViewSwitch look="seg" :options="options" :selected="String(modelValue)" :label="label" :default-id="defaultValue ? String(defaultValue) : null" @select="select" @set-default="setDefault" />
    <span class="win-unit" aria-hidden="true">{{ unit }}</span>
  </span>
</template>

<style scoped>
.win-wrap { display: inline-flex; align-items: center; gap: 6px; }
.seg :deep(button) { min-width: 38px; padding: 0 8px; font-variant-numeric: tabular-nums; }
.win-unit { font-size: 12px; color: var(--ink-2); }
@container delivery (max-width: 640px) {
  .win-wrap { flex: 1 1 100%; }
  .seg { flex: 1; }
  .seg :deep(button) { flex: 1; min-width: 0; height: 44px; }
}
</style>
