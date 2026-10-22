<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, useId } from 'vue'
import type { RowAction, RowMenuAnchor } from '../../lib/rowActions'
import BizIcon from './BizIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'

// The actions a list row offers now, as a menu: arrow keys, Home and End walk it,
// a letter jumps to the next entry starting with it, Enter or Space runs one.
// Unavailable entries stay reachable so their reason can be read: a tooltip
// beside the menu on hover and focus, the entry's description for screen
// readers, and a line under the entry once it is tapped or pressed (always on
// touch screens, which have no hover).
// phoneSheet: on a portrait phone the menu opens as the shared bottom sheet (AEON-1618).
const props = defineProps<{ anchor: RowMenuAnchor; items: RowAction[]; label: string; phoneSheet?: boolean }>()
const emit = defineEmits<{ select: [id: string]; close: [restoreFocus: boolean] }>()
const id = useId()
const menu = ref<HTMLElement>()
const explained = ref<string | null>(null)
const element = computed<HTMLElement>(() => {
  const anchor = props.anchor
  if (anchor instanceof HTMLElement) return anchor
  return { getBoundingClientRect: () => new DOMRect(anchor.x, anchor.y, 0, 0), contains: () => false } as unknown as HTMLElement
})
const atPointer = computed(() => !(props.anchor instanceof HTMLElement))
const entries = () => [...(menu.value?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])]
function focusAt(index: number) {
  const list = entries()
  if (!list.length) return
  list[(index + list.length) % list.length]!.focus()
}
function keys(event: KeyboardEvent) {
  const list = entries()
  const index = list.indexOf(document.activeElement as HTMLElement)
  if (event.key === 'ArrowDown') { event.preventDefault(); focusAt(index + 1) }
  else if (event.key === 'ArrowUp') { event.preventDefault(); focusAt(index < 0 ? -1 : index - 1) }
  else if (event.key === 'Home') { event.preventDefault(); focusAt(0) }
  else if (event.key === 'End') { event.preventDefault(); focusAt(-1) }
  else if (event.key.length === 1 && /\S/.test(event.key) && !event.metaKey && !event.ctrlKey && !event.altKey) {
    const letter = event.key.toLowerCase()
    const next = [...list.slice(index + 1), ...list.slice(0, index + 1)].find(el => el.textContent?.trim().toLowerCase().startsWith(letter))
    if (next) { event.preventDefault(); next.focus() }
  }
}
function run(item: RowAction) {
  if (item.reason) { explained.value = item.id; return }
  if (item.busy) return
  emit('select', item.id)
}
onMounted(async () => { await nextTick(); focusAt(0) })
</script>

<template>
  <FloatingPanel :anchor="element" :width="280" :align="atPointer ? 'start' : 'end'" :label="label" :phone-sheet="phoneSheet" @close="restore => emit('close', restore)">
    <div ref="menu" role="menu" class="row-menu" :aria-label="label" @keydown="keys">
      <template v-for="(item, index) in items" :key="item.id">
        <div v-if="index > 0 && item.group !== items[index - 1]!.group" class="row-menu-sep" role="separator" />
        <button
          type="button" role="menuitem" class="row-menu-item" :class="{ danger: item.danger, off: !!item.reason }" tabindex="-1"
          :aria-disabled="item.reason || item.busy ? 'true' : undefined" :aria-describedby="item.reason ? `${id}-${item.id}` : undefined" :data-tip="item.reason" :data-tip-side="item.reason ? 'end' : undefined"
          :aria-keyshortcuts="item.keys" @click="run(item)"
        >
          <svg v-if="item.busy" class="row-menu-spin" width="14" height="14" viewBox="0 0 16 16" fill="none" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="5.8" class="track" /><path d="M8 2.2a5.8 5.8 0 0 1 5.8 5.8" class="arc" /></svg>
          <BizIcon v-else :name="item.icon" :size="14" />
          <span class="row-menu-text">
            <span class="row-menu-label">{{ item.label }}</span>
            <span v-if="item.reason" :id="`${id}-${item.id}`" class="row-menu-reason" :class="{ shown: explained === item.id }">{{ item.reason }}</span>
          </span>
          <kbd v-if="item.keys" class="row-menu-keys" aria-hidden="true">{{ item.keys.replace('+', ' ') }}</kbd>
        </button>
      </template>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.row-menu { display: grid; gap: 1px; }
.row-menu-item { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 34px; padding: 0 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; }
.row-menu-item svg { flex-shrink: 0; color: var(--ink-3); }
.row-menu-text { flex: 1; display: grid; min-width: 0; }
.row-menu-label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* The reason of an unavailable entry: read by screen readers, shown once asked for. */
.row-menu-reason:not(.shown) { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
.row-menu-reason { margin-top: 2px; color: var(--ink-2); font-size: 12px; line-height: 1.4; white-space: normal; }
.row-menu-item:has(.row-menu-reason.shown) { align-items: flex-start; padding-top: 8px; padding-bottom: 8px; }
.row-menu-item:has(.row-menu-reason.shown) > svg { margin-top: 2px; }
@media (pointer: coarse), (max-width: 600px) {
  .row-menu-item { min-height: 44px; }
  .row-menu-reason:not(.shown) { position: static; width: auto; height: auto; overflow: visible; clip-path: none; white-space: normal; }
  .row-menu-item:has(.row-menu-reason) { align-items: flex-start; padding-top: 8px; padding-bottom: 8px; }
  .row-menu-item:has(.row-menu-reason) > svg { margin-top: 2px; }
}
@media (hover: hover) { .row-menu-item:hover:not(.off) { background: var(--row-hover); } }
/* Keyboard focus shows as the selected row; a menu opened by pointer starts plain. */
.row-menu-item:focus { outline: none; }
.row-menu-item:focus-visible { background: var(--row-selected); }
.row-menu-item.danger { color: var(--danger); }
.row-menu-item.danger svg { color: var(--danger); }
.row-menu-item.off { color: var(--ink-3); cursor: default; }
.row-menu-item.off svg { color: var(--ink-3); opacity: .7; }
.row-menu-keys { flex-shrink: 0; font: 500 11px/1 var(--mono); color: var(--ink-3); font-variant-ligatures: none; }
.row-menu-item:focus-visible .row-menu-keys { color: var(--ink-2); }
/* Touch screens have no keys to press, and neither has the phone's bottom sheet (AEON-1618). */
@media (pointer: coarse) { .row-menu-keys { display: none; } }
:global(.floating.bottom) .row-menu-keys { display: none; }
@media (hover: hover) { .row-menu-item:hover .row-menu-keys { color: var(--ink-2); } }
.row-menu-sep { height: 1px; margin: 4px 6px; background: var(--line); }
.row-menu-spin { animation: row-menu-spin .8s linear infinite; }
.row-menu-spin .track { stroke: var(--line-2); }
.row-menu-spin .arc { stroke: var(--teal); }
@keyframes row-menu-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .row-menu-spin { animation-duration: 2.4s; } }
</style>
