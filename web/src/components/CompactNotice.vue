<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Phones and short screens have no footer bar (AEON-1459, design AEON-1279 r2 D5). What the footer
// said that must not go unseen still shows, as one floating line over the page's foot: a save that
// failed (with its action), and live updates that stopped arriving where no list toolbar's status
// slot says so. It is an overlay: nothing in the page moves for it.
import { computed } from 'vue'
import { compactChrome, statusSlotAside, statusSlots } from '../lib/chrome'
import { footerLive, footerSummary, plain } from '../lib/footerSummary'
import AppIcon from './AppIcon.vue'

const compact = compactChrome(), aside = statusSlotAside()
const notice = computed(() => {
  if (!compact.value) return null
  const summary = footerSummary.value
  if (summary?.transient && (summary.tone === 'attention' || summary.tone === 'problem')) return { text: plain(summary.full), aria: summary.aria, action: summary.action, tone: summary.tone }
  if (footerLive.value?.paused && (!statusSlots.value || aside.value)) return { text: 'Updates paused · retrying', aria: 'Live updates paused, retrying.', action: undefined, tone: 'attention' as const }
  return null
})
</script>

<template>
  <component
    :is="notice.action ? 'button' : 'p'" v-if="notice" class="compact-notice" :data-tone="notice.tone" :type="notice.action ? 'button' : undefined"
    :role="notice.action ? undefined : 'status'" :aria-label="notice.aria" @click="notice.action?.()"
  ><AppIcon name="alert" :size="14" /><span>{{ notice.text }}</span></component>
</template>

<style scoped>
.compact-notice {
  position: fixed; left: 50%; bottom: calc(14px + env(safe-area-inset-bottom)); z-index: 55; transform: translateX(-50%); display: inline-flex; align-items: center; gap: 8px;
  max-width: calc(100vw - 24px); min-height: 44px; margin: 0; padding: 0 16px 0 14px; border: 1px solid var(--glass-edge); border-radius: 999px;
  background: var(--surface-raised); box-shadow: var(--shadow-pop); color: var(--ink); font: 500 13px/1.3 var(--font); text-align: left;
}
.compact-notice > svg { flex: none; color: var(--warn-ink); }
.compact-notice[data-tone="problem"] > svg { color: var(--agent-problem); }
button.compact-notice { cursor: pointer; }
button.compact-notice:focus-visible { outline: none; box-shadow: var(--shadow-pop), var(--focus-ring); }
</style>
