<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Delivery › Flow release record (AEON-1022): what the rollout record reported about the
// release on screen besides its steps — the full test run (catalogue) and the rehearsal with
// their timing, the reference to the qualification evidence, and the rollback class. Four rows,
// always, so the panel keeps its shape as facts arrive; a fact nobody reported says "not
// recorded". It sits below the card and grows downward, so no control above it ever moves.
import TicketLink from '../releases/TicketLink.vue'
import type { ReleaseRecord } from '../../lib/deliveryFlowModes'

defineProps<{ record: ReleaseRecord }>()
</script>

<template>
  <section class="fl-record" :aria-label="record.title" data-testid="flow-record">
    <h3>{{ record.title }}</h3>
    <dl>
      <template v-for="row in record.rows" :key="row.key">
        <dt>{{ row.term }}</dt>
        <dd :class="{ mu: row.missing }" :data-testid="`flow-record-${row.key}`">
          <template v-if="row.ticket"><TicketLink :ticket-key="row.ticket.key" variant="inline" /><span class="rest">{{ row.ticket.rest }}</span></template>
          <template v-else>{{ row.value }}</template>
        </dd>
      </template>
    </dl>
  </section>
</template>

<style scoped>
.fl-record { margin-top: 18px; padding-top: 12px; border-top: 1px solid var(--line); }
h3 { margin: 0 0 8px; font: 650 14px/1.3 var(--font); color: var(--ink); }
dl { display: grid; grid-template-columns: minmax(140px, max-content) minmax(0, 1fr); gap: 6px 24px; margin: 0; font-size: 13px; line-height: 1.5; }
dt { color: var(--ink-3); }
dd { margin: 0; color: var(--ink); overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
dd.mu { color: var(--ink-3); }
.rest { font: 500 11px/1.4 var(--mono); color: var(--ink-2); }
@container delivery (max-width: 640px) {
  dl { grid-template-columns: minmax(0, 1fr); gap: 0; }
  dd { margin-bottom: 8px; }
}
</style>
