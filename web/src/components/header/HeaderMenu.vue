<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// One round header button and the menu it opens (AEON-312): the gear and the
// avatar share it. A popover under the button on wide screens, a bottom sheet on
// phones. Up and down walk the items, Home and End jump, Escape closes and gives
// focus back to the button; Tab or a click elsewhere closes it quietly.
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'

const props = withDefaults(defineProps<{
  id: string
  // The menu's own name (unique on the page) and the button's.
  label: string
  triggerLabel: string
  tip?: string
  // A pane that holds a form (Help & feedback) is a dialog, not a menu.
  role?: 'menu' | 'dialog'
}>(), { tip: undefined, role: 'menu' })
const emit = defineEmits<{ open: []; close: [] }>()

const open = ref(false)
const root = ref<HTMLElement>()
const trigger = ref<HTMLButtonElement>()
const panel = ref<HTMLElement>()
const narrow = window.matchMedia('(max-width: 600px)')
const phone = ref(narrow.matches)
const onNarrow = (event: MediaQueryListEvent) => { phone.value = event.matches }

const ITEMS = '[role="menuitem"]:not([aria-disabled="true"]), [role="menuitemradio"][aria-checked="true"]'
function items() { return [...(panel.value?.querySelectorAll<HTMLElement>(ITEMS) ?? [])].filter(item => item.getClientRects().length > 0) }
function focusAt(index: number) {
  const list = items()
  if (list.length) list[(index + list.length) % list.length].focus()
}
// First item by default; a pane can ask for its own element.
async function focusFirst(selector?: string) {
  await nextTick()
  const target = selector ? panel.value?.querySelector<HTMLElement>(selector) : null
  if (target) target.focus()
  else focusAt(0)
}

async function show(at: 'first' | 'last' = 'first') {
  if (open.value) return
  open.value = true
  emit('open')
  await nextTick()
  if (at === 'last') focusAt(-1)
  else focusAt(0)
}
function close(restoreFocus = false) {
  if (!open.value) return
  open.value = false
  emit('close')
  if (restoreFocus) trigger.value?.focus()
}
function toggle() { if (open.value) close(); else void show() }

function triggerKeys(event: KeyboardEvent) {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    // The keys are the menu's; the page underneath must not also walk its list.
    event.preventDefault(); event.stopPropagation()
    if (!open.value) void show(event.key === 'ArrowUp' ? 'last' : 'first')
    else focusAt(event.key === 'ArrowUp' ? -1 : 0)
  } else if (event.key === 'Escape' && open.value) {
    event.preventDefault(); event.stopPropagation(); close(true)
  }
}
function panelKeys(event: KeyboardEvent) {
  if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(true); return }
  // Keys pressed inside the menu belong to it, not to the page underneath (⌘K still passes).
  if (!event.metaKey && !event.ctrlKey) event.stopPropagation()
  if (props.role !== 'menu') return
  const list = items()
  const index = list.indexOf(document.activeElement as HTMLElement)
  if (event.key === 'ArrowDown') { event.preventDefault(); focusAt(index + 1) }
  else if (event.key === 'ArrowUp') { event.preventDefault(); focusAt(index < 0 ? -1 : index - 1) }
  else if (event.key === 'Home') { event.preventDefault(); focusAt(0) }
  else if (event.key === 'End') { event.preventDefault(); focusAt(-1) }
}
const inside = (node: Node | null) => !!node && (!!root.value?.contains(node) || !!panel.value?.contains(node))
function outside(event: PointerEvent) {
  if (open.value && event.target instanceof Node && !inside(event.target)) close()
}
function focusOut(event: FocusEvent) {
  if (event.relatedTarget instanceof Node && !inside(event.relatedTarget)) close()
}
onMounted(() => { document.addEventListener('pointerdown', outside); narrow.addEventListener('change', onNarrow) })
onBeforeUnmount(() => { document.removeEventListener('pointerdown', outside); narrow.removeEventListener('change', onNarrow) })
defineExpose({ close, focusFirst, open })
</script>

<template>
  <div ref="root" class="header-menu" @focusout="focusOut">
    <button
      ref="trigger" class="icon-btn header-btn hm-trigger" type="button" aria-haspopup="menu" :aria-expanded="open" :aria-controls="open ? id : undefined"
      :aria-label="triggerLabel" :data-tip="open ? undefined : tip" @click="toggle" @keydown="triggerKeys"
    >
      <slot name="trigger" :phone="phone" />
    </button>
    <Teleport to="body" :disabled="!phone">
      <div v-if="open && phone" class="hm-scrim" aria-hidden="true" />
      <div
        v-if="open" :id="id" ref="panel" class="hm-panel pop" :class="{ sheet: phone }" :role="role" :aria-label="label"
        :aria-modal="phone && role === 'dialog' ? 'true' : undefined" @keydown="panelKeys" @focusout="focusOut"
      >
        <span v-if="phone" class="hm-grabber" aria-hidden="true" />
        <slot :close="close" :phone="phone" />
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.header-menu { position: relative; flex-shrink: 0; }
.hm-trigger[aria-expanded="true"] { color: var(--teal-ink); background: var(--seg-on); box-shadow: 0 0 0 1px var(--chip-teal-line), var(--shadow-btn); }
.hm-panel { position: absolute; right: 0; top: 44px; z-index: 30; width: min(320px, calc(100vw - 24px)); padding: 6px; }
@media (prefers-reduced-motion: no-preference) {
  .hm-panel { animation: hm-in .14s ease-out; transform-origin: top right; }
  .hm-panel.sheet { animation: hm-up .2s cubic-bezier(.2, .8, .2, 1); }
  .hm-scrim { animation: hm-fade .2s ease-out; }
}
@keyframes hm-in { from { opacity: 0; transform: translateY(-4px) scale(.98); } }
@keyframes hm-up { from { transform: translateY(24px); opacity: .4; } }
@keyframes hm-fade { from { opacity: 0; } }
/* Phones: a bottom sheet over a scrim, reachable by the thumb. */
.hm-scrim { position: fixed; inset: 0; z-index: 90; background: var(--scrim); }
.hm-panel.sheet {
  position: fixed; top: auto; left: 0; right: 0; bottom: 0; z-index: 91; width: auto; max-height: min(86dvh, 680px); overflow-y: auto; overscroll-behavior: contain;
  padding: 18px 10px calc(12px + env(safe-area-inset-bottom)); border-radius: 18px 18px 0 0; border-bottom: 0;
}
.hm-grabber { position: absolute; top: 7px; left: 50%; width: 36px; height: 4px; margin-left: -18px; border-radius: 999px; background: var(--line-2); }
</style>

<!-- The items both menus share; their markup lives in the menus themselves. -->
<style>
.hm-item {
  display: flex; align-items: center; gap: 10px; width: 100%; min-height: 36px; padding: 0 10px; border: 0; border-radius: 8px;
  background: transparent; color: var(--ink); font: inherit; font-size: 13.5px; text-align: left; text-decoration: none; cursor: pointer;
}
.hm-item > svg { flex-shrink: 0; color: var(--ink-2); }
.hm-item .hm-text { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hm-item .hm-end { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; color: var(--ink-3); font-size: 12px; }
@media (hover: hover) { .hm-item:hover:not([aria-disabled="true"]) { background: var(--row-hover); } }
.hm-item:active:not([aria-disabled="true"]) { background: var(--row-selected); }
.hm-item:focus-visible { outline: none; background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.hm-item[aria-disabled="true"] { color: var(--ink-3); cursor: default; }
.hm-sep { height: 1px; margin: 5px 4px; background: var(--line); border: 0; }
.hm-label { margin: 8px 10px 4px; }
/* Phones and touch: every row is a full 44 px target. */
@media (max-width: 600px), (pointer: coarse) { .hm-item { min-height: 44px; font-size: 14.5px; } }
</style>
