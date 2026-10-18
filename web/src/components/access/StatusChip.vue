<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// A state in words, apart from any action: Active, Deactivated, Pending,
// Expired, Revoked, Accepted, Internal.
defineProps<{ tone: 'ok' | 'off' | 'wait' | 'muted' | 'info'; label: string }>()
</script>

<template>
  <span class="status" :class="tone"><span class="pip" aria-hidden="true" />{{ label }}</span>
</template>

<style scoped>
.status { display: inline-flex; align-items: center; gap: 6px; height: 22px; padding: 0 9px 0 8px; border-radius: 999px; background: var(--surface-2); color: var(--ink-2); font-size: 12px; font-weight: 600; white-space: nowrap; }
.pip { width: 7px; height: 7px; border-radius: 50%; background: var(--ink-3); }
.ok { background: color-mix(in srgb, var(--ok) 12%, transparent); color: color-mix(in oklab, var(--ok), var(--ink) 35%); }
.ok .pip { background: var(--ok); }
.wait { background: color-mix(in srgb, var(--gold) 14%, transparent); color: var(--warn-ink); }
.wait .pip { background: var(--gold); }
.off { background: var(--surface-2); color: var(--ink-2); }
.off .pip { background: transparent; box-shadow: inset 0 0 0 1.5px var(--ink-3); }
.muted { color: var(--ink-3); }
.muted .pip { background: var(--line-2); }
.info { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.info .pip { background: var(--teal); }
</style>
