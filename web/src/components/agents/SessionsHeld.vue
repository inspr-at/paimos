<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The held-changes note at the right of the Sessions card head (AEON-1211, design AEON-1196 §3.2).
// While rows wait to enter, leave or move, it says how many and offers Show now; otherwise it says,
// quietly, how fresh the list is. Both share one slot sized for the longer text, and Show now keeps
// its room unseen, so the note never moves a control in the head (AEON-541).
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import { clockTime } from '../../lib/agentPause'
import { relativeTime } from '../../lib/work'
const props = defineProps<{ pending: number; updatedAt: number | null; now: number; stale: boolean }>()
const emit = defineEmits<{ showNow: [] }>()
const held = computed(() => props.pending > 0)
const waiting = computed(() => `${props.pending} ${props.pending === 1 ? 'change waits' : 'changes wait'} while you point here`)
const ago = computed(() => {
  if (props.updatedAt === null) return ''
  const s = Math.max(0, Math.round((props.now - props.updatedAt) / 1000))
  return s < 5 ? 'just now' : s < 60 ? `${s} s ago` : relativeTime(new Date(props.updatedAt).toISOString(), { now: props.now, long: true })
})
const fresh = computed(() => ago.value ? `${props.stale ? 'Updated' : 'Live · updated'} ${ago.value}` : '')
const tip = computed(() => held.value
  ? 'New sessions, ended rows and moves wait while the pointer is on the list, a row has keyboard focus or a menu is open. They apply 1.5 s after.'
  : props.updatedAt === null ? undefined : `Last update ${clockTime(props.updatedAt)}`)
</script>

<template>
  <span class="held-note" :class="{ held }" :data-tip="tip" data-held-note>
    <AppIcon :name="held ? 'hold' : 'check'" :size="13" class="held-ic" />
    <span class="held-room">
      <span class="held-t" :class="{ off: !held }" role="status">{{ held ? waiting : '' }}</span>
      <span class="calm-t" :class="{ off: held }">{{ fresh }}</span>
      <span class="off" aria-hidden="true">88 changes wait while you point here</span>
    </span>
    <button type="button" class="show-now" :class="{ off: !held }" :tabindex="held ? undefined : -1" :aria-hidden="held ? undefined : 'true'" @click="emit('showNow')">Show now</button>
  </span>
</template>

<style scoped>
.held-note { position: relative; display: inline-flex; align-items: center; gap: 6px; flex: none; min-width: 0; height: 28px; padding: 0 10px; border-radius: 999px; color: var(--ink-3); font-size: 12px; font-weight: 500; white-space: nowrap; }
.held-note.held { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); font-weight: 600; }
.held-ic { flex: none; }
/* Every text sits in one grid cell: the slot keeps the longest one's width. */
.held-room { display: inline-grid; min-width: 0; }
.held-room > * { grid-area: 1 / 1; overflow: hidden; text-overflow: ellipsis; }
.off { visibility: hidden; }
.show-now { position: relative; flex: none; height: 22px; margin-left: 2px; padding: 0 4px; border: 0; border-radius: 6px; background: transparent; color: inherit; font: inherit; font-weight: 650; text-decoration: underline; text-decoration-color: color-mix(in srgb, currentColor 40%, transparent); text-underline-offset: 3px; cursor: pointer; }
.show-now:hover { text-decoration-color: currentColor; }
.show-now:focus-visible { outline: none; box-shadow: var(--focus-ring); }
/* Touch and phones: the target reaches 44 px without moving anything. */
@media (pointer: coarse), (max-width: 720px) { .show-now::after { content: ''; position: absolute; inset: -11px -6px; } }
/* A narrow card head keeps the mark and Show now; the words stay for screen readers. */
@container sessions (max-width: 640px) {
  .held-room { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
}
</style>
