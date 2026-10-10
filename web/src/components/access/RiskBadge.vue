<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { RISK_LABEL, type Risk } from '../../lib/access'
import AppIcon from '../AppIcon.vue'

// A permission's risk. High risk is marked clearly (a tint and a warning icon);
// low risk stays quiet. `compact` shows the icon only, with the words for
// screen readers and the tooltip.
withDefaults(defineProps<{ risk: Risk; compact?: boolean }>(), { compact: false })
</script>

<template>
  <span v-if="risk !== 'low' || !compact" class="risk" :class="[risk, { compact }]" :data-tip="compact ? RISK_LABEL[risk] : undefined">
    <AppIcon v-if="risk === 'high'" name="alert" :size="11" />
    <span :class="{ 'sr-only': compact }">{{ risk === 'high' ? 'High risk' : risk === 'medium' ? 'Medium' : 'Low' }}</span>
  </span>
</template>

<style scoped>
.risk { display: inline-flex; align-items: center; gap: 4px; flex-shrink: 0; height: 20px; padding: 0 7px; border-radius: 999px; font: 600 10.5px/1 var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; white-space: nowrap; }
.risk.low { background: var(--surface-2); color: var(--ink-3); }
.risk.medium { background: color-mix(in srgb, var(--gold) 14%, transparent); color: var(--warn-ink); }
.risk.high { background: var(--danger-bg); color: var(--danger); box-shadow: inset 0 0 0 1px var(--danger-line); }
.risk.compact { width: 20px; padding: 0; justify-content: center; }
</style>
