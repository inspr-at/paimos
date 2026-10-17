<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import AppIcon from './AppIcon.vue'
import FloatingPanel from './work/FloatingPanel.vue'

// The small menu of one view (AEON-1453), opened by right-click, a long press or
// Shift+F10 on its segment: make it the view this place opens with, or stop.
defineProps<{ anchor: HTMLElement; label: string; isDefault: boolean }>()
const emit = defineEmits<{ set: [makeDefault: boolean]; close: [restoreFocus: boolean] }>()
</script>

<template>
  <FloatingPanel :anchor="anchor" :width="264" :label="`${label} view`" @close="restore => emit('close', restore)">
    <div class="view-menu" role="menu" :aria-label="`${label} view`">
      <button type="button" role="menuitem" class="view-menu-item" data-autofocus @click="emit('set', !isDefault)">
        <AppIcon name="pin" :size="16" /><span>{{ isDefault ? 'Stop opening here by default' : 'Open here by default' }}</span>
      </button>
    </div>
  </FloatingPanel>
</template>

<style scoped>
/* The panel-menu style of the session panel's ⋯ menu. */
.view-menu { display: grid; gap: 2px; min-width: 0; }
.view-menu-item { display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 8px 10px; border: 0; border-radius: var(--radius-row); background: transparent; color: var(--ink); text-align: left; font: inherit; font-size: 13px; cursor: pointer; }
.view-menu-item > svg { flex: none; color: var(--ink-2); }
.view-menu-item > span { flex: 1; min-width: 0; }
.view-menu-item:hover { background: var(--row-hover); }
.view-menu-item:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
</style>
