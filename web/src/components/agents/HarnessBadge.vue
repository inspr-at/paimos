<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import type { Harness } from '../../lib/agents'
defineProps<{ harness: Harness | string }>()
</script>

<template>
  <!-- The harness name is written in the execution cell. This mark only repeats it. -->
  <span class="harness-badge" :data-harness="harness" aria-hidden="true">
    <svg viewBox="0 0 16 16" width="8" height="8" focusable="false">
      <template v-if="harness === 'codex'">
        <path d="M8 1.6 12.8 4.3v6.4L8 14.4 3.2 10.7V4.3Z" />
        <path d="M8 4.4 10.7 6v3.2L8 10.8 5.3 9.2V6Z" />
      </template>
      <template v-else-if="harness === 'claude'">
        <path d="M8 1.4v3.2M8 11.4v3.2M1.4 8h3.2M11.4 8h3.2M3.1 3.1l2.2 2.2M10.7 10.7l2.2 2.2M12.9 3.1l-2.2 2.2M5.3 10.7 3.1 12.9" />
      </template>
      <template v-else-if="harness === 'pi'">
        <path d="M3.4 5h9.2M6.1 5v6.6M10.2 5v4.4" />
      </template>
      <template v-else-if="harness === 'cursor'">
        <path d="M8 1.8 13.2 4.8v6.4L8 14.2 2.8 11.2V4.8Z" />
        <path d="M8 7.8 13.2 4.8M8 7.8 2.8 4.8M8 7.8v6.4" />
      </template>
      <template v-else-if="harness === 'grok'">
        <path d="M3.2 3.4 8 8.6 12.8 3.4M4.4 12.6h7.2" />
      </template>
      <template v-else>
        <circle cx="8" cy="8" r="2.2" />
      </template>
    </svg>
  </span>
</template>

<style scoped>
/* About 20% smaller than the reference corner badge, on a theme surface at 70%. */
.harness-badge {
  position: absolute; right: 0; bottom: 0; display: grid; place-items: center; width: 11px; height: 11px;
  border-radius: 3px; color: var(--ink); background: color-mix(in srgb, var(--surface-raised) 70%, transparent);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--ink) 16%, transparent);
}
svg { display: block; overflow: visible; fill: none; stroke: currentColor; stroke-width: 1.4; stroke-linecap: round; stroke-linejoin: round; }
</style>
