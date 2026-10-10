<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { QuoteProfileDefinition } from '../../../lib/quotes/types'

// A profile at a glance: a tiny A4 sheet in its paper colour with a title line in
// its accent and body lines in its text colours, laid out like its variant.
defineProps<{ definition: QuoteProfileDefinition; size?: number }>()
</script>

<template>
  <svg class="thumb" :width="(size ?? 30) * 210 / 297" :height="size ?? 30" viewBox="0 0 210 297" aria-hidden="true" focusable="false">
    <rect x="0.5" y="0.5" width="209" height="296" rx="10" :fill="definition.colors.paper" stroke="currentColor" stroke-opacity=".22" />
    <template v-if="definition.layout_variant === 'classic-v1'">
      <rect x="28" y="30" width="96" height="14" rx="3" :fill="definition.colors.accent" />
      <rect x="28" y="62" width="150" height="7" rx="3" :fill="definition.colors.ink" />
      <rect x="28" y="84" width="154" height="1.5" :fill="definition.colors.rule" />
      <rect x="28" y="98" width="62" height="6" rx="3" :fill="definition.colors.muted" />
      <rect x="118" y="98" width="62" height="6" rx="3" :fill="definition.colors.muted" />
      <rect x="28" y="140" width="80" height="9" rx="3" :fill="definition.colors.accent" />
      <rect x="28" y="162" width="154" height="6" rx="3" :fill="definition.colors.ink" opacity=".75" />
      <rect x="28" y="178" width="130" height="6" rx="3" :fill="definition.colors.ink" opacity=".75" />
      <rect x="28" y="264" width="154" height="1.5" :fill="definition.colors.rule" />
      <rect x="84" y="274" width="42" height="6" rx="3" :fill="definition.colors.soft" />
    </template>
    <template v-else>
      <rect x="26" y="34" width="60" height="7" rx="3" :fill="definition.colors.muted" />
      <rect x="26" y="52" width="140" height="13" rx="3" :fill="definition.colors.ink" />
      <rect x="26" y="90" width="70" height="6" rx="3" :fill="definition.colors.muted" />
      <rect x="116" y="90" width="68" height="6" rx="3" :fill="definition.colors.muted" />
      <rect x="26" y="136" width="90" height="9" rx="3" :fill="definition.colors.ink" />
      <rect x="26" y="158" width="158" height="6" rx="3" :fill="definition.colors.ink" opacity=".7" />
      <rect x="26" y="174" width="120" height="6" rx="3" :fill="definition.colors.ink" opacity=".7" />
      <rect x="26" y="266" width="158" height="1.5" :fill="definition.colors.rule" />
      <rect x="26" y="276" width="50" height="6" rx="3" :fill="definition.colors.soft" />
    </template>
  </svg>
</template>

<style scoped>
.thumb { flex-shrink: 0; color: var(--ink); border-radius: 3px; box-shadow: 0 1px 3px color-mix(in srgb, var(--shadow-color) 14%, transparent); }
</style>
