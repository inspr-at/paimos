<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from '../AppIcon.vue'

// Shared phone action for List and Outline. The containing view reserves scroll
// clearance and supplies --live-obstacle-h for its bottom selection sheet.
defineProps<{ text: string; overflow: boolean }>()
defineEmits<{ show: [] }>()
</script>

<template>
  <div class="live-dock">
    <button type="button" class="live-pill" :aria-label="text" aria-keyshortcuts="u" :data-tip="overflow ? 'Load the view again' : 'Show the updates · u'" @click="$emit('show')">
      <AppIcon :name="overflow ? 'refresh' : 'arrow-up'" :size="14" />
      <span>{{ text.split(' · ')[0] }}</span><span class="dot" aria-hidden="true">·</span><b>{{ text.split(' · ')[1] }}</b>
    </button>
  </div>
</template>

<style scoped>
.live-dock { display: none; }
@media (max-width: 720px) {
  .live-dock {
    position: fixed; left: 12px; right: 12px; bottom: calc(var(--footer-h, 0px) + var(--live-obstacle-h, 0px) + 12px + env(safe-area-inset-bottom));
    z-index: 31; display: flex; justify-content: center; pointer-events: none;
  }
  .live-pill {
    display: inline-flex; align-items: center; justify-content: center; gap: 6px; min-height: 44px; max-width: 100%; padding: 0 16px;
    border: 1px solid var(--glass-edge); border-radius: 999px; background: var(--surface-raised); box-shadow: var(--shadow-pop);
    color: var(--ink-2); font-size: 13px; white-space: nowrap; pointer-events: auto;
  }
  .live-pill svg, .live-pill b { color: var(--teal-ink); }
  .live-pill b { font-weight: 600; }
  .live-pill .dot { color: var(--ink-3); }
  .live-pill:hover { color: var(--ink); background: var(--surface-raised-2); }
  .live-pill:focus-visible { box-shadow: var(--focus-ring), var(--shadow-pop); }
}
@media (prefers-reduced-motion: no-preference) {
  .live-pill { animation: live-pill-in .2s cubic-bezier(.2, .7, .2, 1); }
  @keyframes live-pill-in { from { opacity: 0; transform: translateY(6px); } to { opacity: 1; transform: none; } }
}
</style>
