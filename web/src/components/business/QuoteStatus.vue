<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<!-- Parked (AEON-70, 2026-09-24): quotes and organisations will be ported from Markus's current classic Paimos quote builder; this file is not routed or linked. -->
<script lang="ts">
import type { QuoteState } from '../../lib/business'
export const QUOTE_STATES: { value: QuoteState; label: string }[] = [
  { value: 'draft', label: 'Draft' }, { value: 'issued', label: 'Issued' }, { value: 'accepted', label: 'Accepted' }, { value: 'void', label: 'Void' },
]
export function quoteStateLabel(state: string) { return QUOTE_STATES.find(s => s.value === state)?.label ?? state }
</script>
<script setup lang="ts">
withDefaults(defineProps<{ state: QuoteState; size?: number; label?: boolean }>(), { size: 14, label: true })
</script>

<template>
  <span class="quote-status" :class="state">
    <svg :width="size" :height="size" viewBox="0 0 14 14" fill="none" aria-hidden="true" focusable="false">
      <circle v-if="state === 'draft'" cx="7" cy="7" r="5.1" stroke="var(--st-backlog)" stroke-width="1.6" stroke-dasharray="2.1 1.9" />
      <template v-else-if="state === 'issued'"><circle cx="7" cy="7" r="5.1" stroke="var(--st-progress)" stroke-width="1.8" /><path d="M7 3.6a3.4 3.4 0 0 1 0 6.8Z" fill="var(--st-progress)" /></template>
      <template v-else-if="state === 'accepted'"><circle cx="7" cy="7" r="6" fill="var(--st-ok)" /><path d="m4.4 7.2 1.8 1.8 3.4-3.7" stroke="var(--st-ok-on)" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></template>
      <template v-else><circle cx="7" cy="7" r="5.1" stroke="var(--st-closed)" stroke-width="1.6" /><path d="m3.6 10.4 6.8-6.8" stroke="var(--st-closed)" stroke-width="1.6" stroke-linecap="round" /></template>
    </svg>
    <span v-if="label" class="label">{{ quoteStateLabel(state) }}</span>
  </span>
</template>

<style scoped>
.quote-status { display: inline-flex; align-items: center; gap: 8px; white-space: nowrap; font-size: 13px; color: var(--ink); }
.quote-status.void .label { color: var(--ink-2); }
</style>
