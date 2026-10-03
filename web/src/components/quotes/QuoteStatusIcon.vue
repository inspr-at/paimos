<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { STATUS_META, type QuoteStatus } from '../../lib/quotes/list'

// A quote's status as a small drawn mark (and its word): a dashed ring while it
// is a draft, a half-filled ring once issued, a clock when its validity passed,
// a filled check when accepted and a struck ring when void. Shape, not colour
// alone, tells them apart.
withDefaults(defineProps<{ status: QuoteStatus; size?: number; label?: boolean }>(), { size: 14, label: true })
</script>

<template>
  <span class="quote-status" :class="status">
    <svg :width="size" :height="size" viewBox="0 0 14 14" fill="none" aria-hidden="true" focusable="false">
      <circle v-if="status === 'draft'" cx="7" cy="7" r="5.1" stroke="var(--st-backlog)" stroke-width="1.6" stroke-dasharray="2.1 1.9" />
      <template v-else-if="status === 'issued'"><circle cx="7" cy="7" r="5.1" stroke="var(--st-progress)" stroke-width="1.8" /><path d="M7 3.6a3.4 3.4 0 0 1 0 6.8Z" fill="var(--st-progress)" /></template>
      <template v-else-if="status === 'expired'"><circle cx="7" cy="7" r="5.1" stroke="var(--gold-ink)" stroke-width="1.6" /><path d="M7 4.3V7l1.9 1.3" stroke="var(--gold-ink)" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></template>
      <template v-else-if="status === 'accepted'"><circle cx="7" cy="7" r="6" fill="var(--st-ok)" /><path d="m4.4 7.2 1.8 1.8 3.4-3.7" stroke="var(--st-ok-on)" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></template>
      <template v-else><circle cx="7" cy="7" r="5.1" stroke="var(--st-closed)" stroke-width="1.6" /><path d="m3.6 10.4 6.8-6.8" stroke="var(--st-closed)" stroke-width="1.6" stroke-linecap="round" /></template>
    </svg>
    <span v-if="label" class="label">{{ STATUS_META[status].label }}</span>
  </span>
</template>

<style scoped>
.quote-status { display: inline-flex; align-items: center; gap: 7px; white-space: nowrap; font-size: 13px; color: var(--ink); }
.quote-status svg { flex-shrink: 0; }
.quote-status.void .label { color: var(--ink-2); }
</style>
