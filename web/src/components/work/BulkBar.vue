<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { plural } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'

// What to do with the selected tickets. It floats over the list's lower edge,
// changes nothing by itself, and every change it starts can be undone at once.
// frame: the list's left edge and width, so the bar centres over the list, not the docked panel.
defineProps<{ count: number; loaded: number; total: number | null; busy: boolean; canWrite: boolean; canRelease?: boolean; frame?: { left: number; width: number } | null }>()
const emit = defineEmits<{
  status: [anchor: HTMLElement]; assignee: [anchor: HTMLElement]; priority: [anchor: HTMLElement]; labels: [anchor: HTMLElement]
  move: [anchor: HTMLElement]; release: [anchor: HTMLElement]; archive: []; clear: []; selectAll: []
}>()
const at = (event: MouseEvent) => event.currentTarget as HTMLElement
</script>

<template>
  <div class="bulk-dock" :style="frame ? { left: `${frame.left}px`, width: `${frame.width}px` } : undefined">
    <div class="bulk-bar" role="toolbar" :aria-label="`${plural(count, 'selected ticket')}`" :aria-busy="busy">
      <span class="count" aria-live="polite"><b class="mono">{{ count.toLocaleString('en-GB') }}</b><span class="count-word">selected</span></span>
      <button v-if="total !== null && count < total && count >= loaded && loaded < total" type="button" class="link" @click="emit('selectAll')">Select all {{ total.toLocaleString('en-GB') }}</button>
      <span class="rule" aria-hidden="true" />
      <template v-if="canWrite">
        <button type="button" class="act" aria-label="Status" aria-keyshortcuts="s" data-tip="Status · s" :disabled="busy" @click="emit('status', at($event))"><AppIcon name="check" :size="14" /><span class="label">Status</span></button>
        <button type="button" class="act" aria-label="Assignee" aria-keyshortcuts="a" data-tip="Assignee · a" :disabled="busy" @click="emit('assignee', at($event))"><AppIcon name="user" :size="14" /><span class="label">Assignee</span></button>
        <button type="button" class="act" aria-label="Priority" aria-keyshortcuts="p" data-tip="Priority · p" :disabled="busy" @click="emit('priority', at($event))"><AppIcon name="gauge" :size="14" /><span class="label">Priority</span></button>
        <button type="button" class="act" aria-label="Labels" aria-keyshortcuts="l" data-tip="Labels · l" :disabled="busy" @click="emit('labels', at($event))"><AppIcon name="tag" :size="14" /><span class="label">Labels</span></button>
        <button type="button" class="act" aria-label="Move" aria-keyshortcuts="m" data-tip="Move to an epic · m" :disabled="busy" @click="emit('move', at($event))"><AppIcon name="epic" :size="14" /><span class="label">Move</span></button>
        <button v-if="canRelease" type="button" class="act" aria-label="Add to release" aria-keyshortcuts="g" data-tip="Add to release · g" :disabled="busy" @click="emit('release', at($event))"><AppIcon name="layers" :size="14" /><span class="label">Release</span></button>
        <button type="button" class="act" aria-label="Archive" data-tip="Archive · closed, and hidden with Hide closed" :disabled="busy" @click="emit('archive')"><AppIcon name="archive" :size="14" /><span class="label">Archive</span></button>
      </template>
      <span v-else class="read-only">Read only</span>
      <button type="button" class="close" aria-label="Clear the selection" aria-keyshortcuts="Escape" @click="emit('clear')"><AppIcon name="close" :size="13" /><KeyCap k="esc" class="esc" /></button>
    </div>
  </div>
</template>

<style scoped>
/* A zero-height dock fixed above the footer: the bar floats over the list's lower edge. */
.bulk-dock { position: fixed; left: 0; width: 100%; bottom: calc(var(--footer-h, 0px) + 16px + env(safe-area-inset-bottom)); z-index: 30; height: 0; display: flex; justify-content: center; pointer-events: none; }
.bulk-bar {
  position: absolute; bottom: 0; display: flex; align-items: center; gap: 2px; max-width: calc(100vw - 24px); height: 48px; padding: 0 6px 0 16px;
  border-radius: 999px; border: 1px solid var(--glass-edge); background: var(--glass); box-shadow: var(--shadow-pop);
  -webkit-backdrop-filter: blur(20px) saturate(1.25); backdrop-filter: blur(20px) saturate(1.25); pointer-events: auto;
}
.count { display: inline-flex; align-items: baseline; gap: 6px; margin-right: 6px; font-size: 13px; color: var(--ink-2); white-space: nowrap; }
.count b { font-size: 14px; font-weight: 700; color: var(--ink); font-variant-numeric: tabular-nums; }
.link { height: 30px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
.link:hover { background: var(--row-hover); }
.rule { width: 1px; height: 22px; margin: 0 6px; background: var(--line-2); }
.act { display: inline-flex; align-items: center; gap: 7px; height: 36px; padding: 0 11px; border: 0; border-radius: 999px; background: transparent; color: var(--ink); font-size: 13px; font-weight: 500; white-space: nowrap; }
.act svg { color: var(--ink-2); }
.act:hover:not(:disabled) { background: var(--row-hover); }
.act:active:not(:disabled) { background: var(--row-selected); }
.act:disabled { opacity: .5; }
.act:focus-visible, .close:focus-visible, .link:focus-visible { box-shadow: var(--focus-ring); }
.read-only { padding: 0 10px; font-size: 12.5px; color: var(--ink-3); }
.close { display: inline-flex; align-items: center; gap: 6px; height: 36px; margin-left: 4px; padding: 0 8px 0 10px; border: 0; border-radius: 999px; background: var(--chip-bg); color: var(--ink-2); }
.close:hover { color: var(--ink); background: var(--row-selected); }
.esc { font-size: 10px; }
@media (max-width: 1100px) { .act .label { display: none; } .act { width: 38px; padding: 0; justify-content: center; } }
@media (max-width: 600px) {
  .bulk-bar { height: 52px; padding: 0 4px 0 12px; }
  .count-word, .esc, .rule, .link { display: none; }
  .act { width: 40px; height: 40px; }
  .close { width: 40px; height: 40px; padding: 0; justify-content: center; margin-left: 2px; }
}
@media (hover: none) { .esc { display: none; } }
@media (prefers-reduced-motion: no-preference) {
  .bulk-bar { animation: bar-in .18s cubic-bezier(.2, .7, .2, 1); }
  @keyframes bar-in { from { opacity: 0; transform: translateY(8px); } to { opacity: 1; transform: none; } }
}
</style>
