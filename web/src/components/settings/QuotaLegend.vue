<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-1172 (design AEON-1149): the quota bar's key. "short" is the account list's
// legend, once above the list; the panel's names the room left today too. Kept for
// you shows only while it keeps something.
withDefaults(defineProps<{ keep?: boolean; short?: boolean }>(), { keep: true, short: false })
</script>
<template>
  <p class="legend" :class="{ short }" aria-hidden="true" data-quota-legend>
    <span><i class="sw u" />used</span><span v-if="!short"><i class="sw room" />room left today</span><span><i class="sw tick" />today’s limit</span><span v-if="keep"><i class="sw keep" />{{ short ? 'kept for you' : 'kept for you while you work' }}</span><span><i class="sw floor" />floor</span>
  </p>
</template>
<style scoped>
.legend { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 16px; margin: 0; font-size: 12px; color: var(--ink-2); }
.legend > span { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.sw { display: inline-block; flex: none; width: 16px; height: 9px; border-radius: 3px; box-shadow: inset 0 0 0 1px var(--line); }
.sw.u { background: color-mix(in srgb, var(--teal) 78%, transparent); }
.sw.room { background: color-mix(in srgb, var(--teal) 24%, transparent); }
.sw.keep { background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--gold) 55%, transparent) 0 1.5px, transparent 1.5px 5px), color-mix(in srgb, var(--gold) 10%, transparent); }
.sw.floor { background: repeating-linear-gradient(135deg, color-mix(in srgb, var(--ink) 55%, transparent) 0 1.5px, transparent 1.5px 3.5px), var(--surface-raised); }
.sw.tick { width: 2px; height: 14px; border-radius: 1px; background: var(--ink); box-shadow: none; }
.short { gap: 4px 12px; font-size: 11.5px; color: var(--ink-3); }
.short .sw { width: 12px; height: 7px; }
.short .sw.tick { width: 2px; height: 11px; }
@container (max-width: 560px) { .legend { gap: 6px 12px; } }
</style>
