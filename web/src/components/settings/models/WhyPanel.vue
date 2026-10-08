<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { resolutionLabel, type ModelResolution } from '../../../lib/modelsSettings'
const props = defineProps<{ resolution: ModelResolution | null; reviewer: ModelResolution | null; error: string; loading: boolean; person: string; german: boolean }>()
const text = (en: string, de: string) => props.german ? de : en
</script>
<template>
  <div class="why">
    <p v-if="error" role="alert">{{ error }}</p><p v-else-if="loading" role="status">{{ text('Checking which models can run…', 'Verfügbare Modelle werden geprüft…') }}</p>
    <template v-else-if="resolution"><p class="chosen">{{ resolutionLabel(resolution, german) }}</p><ol><li><b>{{ text('Whose board', 'Wessen Board') }}</b><p>{{ person }} · {{ resolution.trace.preference_of?.source || resolution.trace.set_by || text('Workspace default', 'Vorgabe des Arbeitsbereichs') }}</p></li><li><b>{{ text('Column and situation', 'Spalte und Situation') }}</b><p>{{ resolution.trace.column || resolution.trace.kind }} · {{ resolution.trace.situation || 'first' }}</p></li><li><b>{{ text('Locks', 'Regeln') }}</b><p v-if="resolution.trace.lock">{{ resolution.trace.lock.why }} · {{ resolution.trace.lock.who }} · {{ resolution.trace.lock.at }}</p><p v-else>{{ text('Your order wins inside the rules.', 'Ihre Reihenfolge gilt innerhalb der Regeln.') }}</p><p v-for="held in resolution.trace.held || []" :key="held.line">{{ held.line }} · {{ held.reason }}</p></li><li><b>{{ text('Pick', 'Auswahl') }}</b><p>{{ resolution.trace.blocked || resolution.trace.fallback || text('The first available card in the server’s order.', 'Die erste verfügbare Karte in der Reihenfolge des Servers.') }}<span v-if="resolution.trace.card_index"> · {{ resolution.trace.card_index }}</span></p></li><li><b>{{ text('Thinking and review', 'Denken und Prüfung') }}</b><p>{{ resolution.profile?.effort }} · {{ text('reviewed by', 'geprüft von') }} {{ resolutionLabel(reviewer, german) }}</p></li></ol></template>
    <p v-else>{{ text('No resolution is available yet.', 'Noch keine Auswahl verfügbar.') }}</p>
  </div>
</template>
<style scoped>.why { font-size: 13px; line-height: 1.6; overflow-wrap: anywhere; }.chosen { font-size: 16px; font-weight: 650; margin-bottom: 16px; }.why ol { list-style: decimal; padding-left: 22px; }.why li { padding: 12px 0; border-top: 1px solid var(--line); }.why li p { color: var(--ink-2); }</style>
