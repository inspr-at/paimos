<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// The held-changes note at the right of the Sessions card head (AEON-1211, design AEON-1196 §3.2).
// While rows wait to enter, leave or move, it says how many and offers Show now; otherwise it says,
// quietly, how fresh the list is. Both faces share one grid cell sized by an unseen held face, so the
// note keeps its box whichever it shows and never moves a control in the head (AEON-541). Narrower
// heads use the short words; a phone keeps the mark and Show now, the words stay for screen readers.
import { computed } from 'vue'
import AppIcon from '../AppIcon.vue'
import { clockTime } from '../../lib/agentPause'
import { relativeTime } from '../../lib/work'
const props = defineProps<{ pending: number; updatedAt: number | null; now: number; stale: boolean }>()
const emit = defineEmits<{ showNow: [] }>()
const held = computed(() => props.pending > 0)
const count = computed(() => `${props.pending} ${props.pending === 1 ? 'change waits' : 'changes wait'}`)
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
    <span class="face held-face" :class="{ off: !held }">
      <AppIcon name="hold" :size="13" />
      <span class="sr-only" role="status">{{ held ? `${count} while you point here` : '' }}</span>
      <span class="words" aria-hidden="true"><span class="long">{{ count }} while you point here</span><span class="short">{{ count }}</span><span class="num">{{ pending }}</span></span>
      <button type="button" class="show-now" :tabindex="held ? undefined : -1" :aria-hidden="held ? undefined : 'true'" @click="emit('showNow')">Show now</button>
    </span>
    <span class="face calm-face" :class="{ off: held }">
      <AppIcon name="check" :size="13" />
      <span class="words"><span class="long">{{ fresh }}</span><span class="short">{{ stale ? 'Delayed' : 'Live' }}</span><span class="num">{{ stale ? 'Delayed' : 'Live' }}</span></span>
    </span>
    <!-- The room: the widest held face, never seen. -->
    <span class="face held-face room" aria-hidden="true">
      <AppIcon name="hold" :size="13" />
      <span class="words"><span class="long">88 changes wait while you point here</span><span class="short">88 changes wait</span><span class="num">88</span></span>
      <span class="show-now">Show now</span>
    </span>
  </span>
</template>

<style scoped>
.held-note { position: relative; display: inline-grid; flex: none; min-width: 0; font-size: 12px; white-space: nowrap; }
.face { grid-area: 1 / 1; justify-self: end; display: inline-flex; align-items: center; gap: 6px; min-width: 0; height: 28px; padding: 0 10px; border-radius: 999px; color: var(--ink-3); font-weight: 500; }
.face > svg { flex: none; }
.held-face { background: var(--queue-wait-bg); box-shadow: inset 0 0 0 1px var(--queue-wait-line); color: var(--queue-wait-ink); font-weight: 600; }
.room, .off { visibility: hidden; }
.words { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.short, .num { display: none; }
.show-now { position: relative; flex: none; height: 22px; margin-left: 2px; padding: 0 4px; border: 0; border-radius: 6px; background: transparent; color: inherit; font: inherit; font-weight: 650; text-decoration: underline; text-decoration-color: color-mix(in srgb, currentColor 40%, transparent); text-underline-offset: 3px; cursor: pointer; }
.show-now:hover { text-decoration-color: currentColor; }
.show-now:focus-visible { outline: none; box-shadow: var(--focus-ring); }
/* Touch and phones: the target reaches 44 px without moving anything. */
@media (pointer: coarse), (max-width: 720px) { button.show-now::after { content: ''; position: absolute; inset: -11px -6px; } }
@container sessions (max-width: 1240px) {
  .long { display: none; }
  .short { display: inline; }
}
@container sessions (max-width: 1000px) {
  .short { display: none; }
  .num { display: inline; font-variant-numeric: tabular-nums; }
}
@container sessions (max-width: 640px) {
  .words { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
}
</style>
