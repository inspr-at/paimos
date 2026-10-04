<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
import type { IconName } from '../AppIcon.vue'
export interface SettingsMenuItem {
  id: string
  label: string
  detail?: string
  icon?: IconName
  disabled?: boolean
  /** Destructive actions cannot run without naming their effect and what remains. */
  confirmation?: { title: string; effect: string; keeps: string; action: string }
}
</script>
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import { claimSettingsPopover, isSettingsField, settingsSubmitKey } from '../../lib/settingsOverlays'

const props = withDefaults(defineProps<{
  open: boolean
  anchor: HTMLElement | null
  /** Boundaries of the Settings frame; defaults to the viewport. */
  frame?: HTMLElement | null
  label: string
  mode?: 'menu' | 'form' | 'confirmation'
  items?: readonly SettingsMenuItem[]
  confirmation?: SettingsMenuItem['confirmation']
  contextKey?: string
  hint?: string
  error?: string
  busy?: boolean
  saveLabel?: string
}>(), { mode: 'menu', items: () => [], saveLabel: 'Save' })
const emit = defineEmits<{
  'update:open': [open: boolean]
  close: []
  select: [id: string, contextKey: string | undefined]
  submit: [contextKey: string | undefined]
}>()
const panel = ref<HTMLElement>()
const selected = ref<SettingsMenuItem>()
const confirmation = computed(() => selected.value?.confirmation ?? props.confirmation)
const confirming = computed(() => !!selected.value || props.mode === 'confirmation')
const role = computed(() => props.mode === 'menu' && !confirming.value ? 'menu' : 'dialog')
const hintId = useId()
const placed = ref(false)
const position = ref<Record<string, string>>({})
let release: (() => void) | undefined
let triggerRect: DOMRect | undefined
let opening = 0
let scrollCheck = 0
let previousExpanded: string | null = null
let ownedAnchor: HTMLElement | null = null

function close(restoreFocus = true) {
  if (!props.open) return
  cleanup()
  emit('update:open', false)
  emit('close')
  if (restoreFocus && props.anchor?.isConnected) props.anchor.focus({ preventScroll: true })
}
function place() {
  if (!props.anchor || !panel.value) return
  const trigger = props.anchor.getBoundingClientRect()
  const frame = props.frame?.getBoundingClientRect()
  const left = Math.max(0, frame?.left ?? 0) + 12
  const right = Math.min(innerWidth, frame?.right ?? innerWidth) - 12
  const top = Math.max(0, frame?.top ?? 0) + 12
  const bottom = Math.min(innerHeight, frame?.bottom ?? innerHeight) - 12
  const width = Math.max(0, Math.min(320, right - left))
  panel.value.style.width = `${width}px`
  const height = Math.min(panel.value.scrollHeight + 2, Math.max(0, bottom - top))
  const up = !!props.anchor.closest('.pane-foot') || (trigger.bottom + 4 + height > bottom && trigger.top - top > bottom - trigger.bottom)
  const available = Math.max(0, up ? trigger.top - 4 - top : bottom - trigger.bottom - 4)
  const maxHeight = Math.min(bottom - top, available || bottom - top)
  const x = Math.max(left, Math.min(trigger.right - width, right - width))
  // Bottom anchoring lets a confirmation or validation message grow upward
  // when opened from a footer, without moving its trigger or the page.
  position.value = {
    width: `${width}px`, left: `${x}px`, maxHeight: `${Math.max(0, maxHeight)}px`,
    ...(up ? { bottom: `${innerHeight - Math.min(bottom, Math.max(top + height, trigger.top - 4))}px`, top: 'auto' }
      : { top: `${Math.max(top, Math.min(trigger.bottom + 4, bottom - height))}px`, bottom: 'auto' }),
  }
  placed.value = true
}
async function focusFirst() {
  await nextTick()
  if (!props.open) return
  panel.value?.querySelector<HTMLElement>('[data-autofocus], input:not(:disabled), textarea:not(:disabled), button:not(:disabled)')?.focus({ preventScroll: true })
}
function choose(item: SettingsMenuItem) {
  if (item.disabled || props.busy) return
  if (item.confirmation) { selected.value = item; void focusFirst(); return }
  const context = props.contextKey
  close()
  emit('select', item.id, context)
}
function confirm() {
  if (props.busy || !confirmation.value) return
  // The owner keeps the popover open while writing and supplies busy/error.
  // A failed write must never be interpreted here as a successful close.
  if (selected.value) emit('select', selected.value.id, props.contextKey)
  else emit('submit', props.contextKey)
}
function submit() { if (!props.busy) emit('submit', props.contextKey) }
function outside(event: PointerEvent) {
  if (!panel.value?.contains(event.target as Node) && !props.anchor?.contains(event.target as Node)) close(false)
}
function scroll(event: Event) {
  if (panel.value?.contains(event.target as Node)) return
  cancelAnimationFrame(scrollCheck)
  scrollCheck = requestAnimationFrame(() => {
    const now = props.anchor?.getBoundingClientRect()
    if (now && triggerRect && (Math.abs(now.top - triggerRect.top) > .5 || Math.abs(now.left - triggerRect.left) > .5)) close(false)
  })
}
function resize() { close(false) }
function keys(event: KeyboardEvent) {
  if (!props.open) return
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopImmediatePropagation()
    if (isSettingsField(event.target) && panel.value?.contains(event.target)) {
      (event.target as HTMLElement).blur(); panel.value?.focus({ preventScroll: true })
    } else close()
    return
  }
  if (!panel.value?.contains(event.target as Node)) return
  if (props.mode === 'form' && settingsSubmitKey(event)) { event.preventDefault(); event.stopImmediatePropagation(); submit(); return }
  if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey || role.value !== 'menu') return
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const items = [...panel.value.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')]
  if (!items.length) return
  event.preventDefault(); event.stopImmediatePropagation()
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length
  items[next]?.focus()
}
function cleanup() {
  opening++
  release?.(); release = undefined
  cancelAnimationFrame(scrollCheck)
  document.removeEventListener('pointerdown', outside, true)
  document.removeEventListener('scroll', scroll, true)
  window.removeEventListener('resize', resize)
  window.removeEventListener('keydown', keys, true)
  if (ownedAnchor) {
    if (previousExpanded === null) ownedAnchor.removeAttribute('aria-expanded')
    else ownedAnchor.setAttribute('aria-expanded', previousExpanded)
  }
  ownedAnchor = null
}
watch(() => props.open, async open => {
  if (!open) { const back = ownedAnchor; cleanup(); back?.focus({ preventScroll: true }); return }
  selected.value = undefined; placed.value = false
  const generation = ++opening
  await nextTick()
  if (generation !== opening || !props.open) return
  if (!props.anchor?.isConnected) { close(false); return }
  release = claimSettingsPopover(() => close(false))
  previousExpanded = props.anchor.getAttribute('aria-expanded')
  ownedAnchor = props.anchor
  props.anchor.setAttribute('aria-expanded', 'true')
  triggerRect = props.anchor.getBoundingClientRect()
  place()
  document.addEventListener('pointerdown', outside, true)
  document.addEventListener('scroll', scroll, true)
  window.addEventListener('resize', resize)
  window.addEventListener('keydown', keys, true)
  void focusFirst()
}, { immediate: true })
watch(() => props.contextKey, () => { if (props.open) close(false) })
watch(() => props.anchor, () => { if (props.open && ownedAnchor) close(false) })
onBeforeUnmount(cleanup)
</script>
<template>
  <Teleport to="body">
    <div v-if="open" ref="panel" class="popover" :role="role" :aria-label="confirming ? confirmation?.title ?? label : label" :aria-busy="busy || undefined" tabindex="-1" :style="{ ...position, visibility: placed ? 'visible' : 'hidden' }">
      <template v-if="mode === 'menu' && !confirming">
        <button v-for="item in items" :key="item.id" type="button" class="mi" :class="{ danger: item.confirmation }" role="menuitem" :disabled="item.disabled" @click="choose(item)"><AppIcon :name="item.icon ?? 'more'" /><span class="t">{{ item.label }}</span><span v-if="item.detail" class="d">{{ item.detail }}</span></button>
      </template>
      <div v-else-if="confirming && confirmation" class="confirm">
        <h3>{{ confirmation.title }}</h3><p>{{ confirmation.effect }}</p><p class="keeps">{{ confirmation.keeps }}</p>
        <div class="confirm-acts"><button type="button" class="btn sm" @click="close()">Cancel</button><button type="button" class="btn sm danger" :disabled="busy" @click="confirm">{{ confirmation.action }}</button></div>
        <p v-if="error" class="err" role="alert">{{ error }}</p>
      </div>
      <form v-else-if="mode === 'form'" class="qform" @submit.prevent="submit">
        <h3>{{ label }}</h3><slot :hint-id="hintId" />
        <p :id="hintId" class="qhint" :class="{ err: error }" aria-live="polite">{{ error || hint }}</p>
        <div class="confirm-acts"><button type="button" class="btn sm" @click="close()">Cancel</button><button type="submit" class="btn sm primary" :disabled="busy" aria-keyshortcuts="Meta+Enter Control+Enter">{{ saveLabel }} <span class="save-keys"><KeyCap k="mod" /><KeyCap k="enter" /></span></button></div>
      </form>
    </div>
  </Teleport>
</template>
<style scoped>
.popover { position: fixed; z-index: 80; overflow: auto; padding: 6px; border-radius: 12px; background: var(--surface-raised); border: 1px solid var(--glass-edge); box-shadow: var(--shadow-pop); overscroll-behavior: contain; }
.mi { display: grid; grid-template-columns: 20px minmax(0, 1fr); gap: 2px 10px; width: 100%; padding: 9px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); text-align: left; }
.mi:hover:not(:disabled), .mi:focus-visible { background: var(--row-hover); outline: none; }.mi svg { grid-row: span 2; margin-top: 2px; color: var(--ink-2); }
.t { font-size: 13.5px; font-weight: 600; }.d { font-size: 12px; line-height: 1.4; color: var(--ink-3); }.danger .t, .danger svg { color: var(--danger); }
.mi:disabled .t, .mi:disabled svg { color: var(--ink-3); }
.confirm, .qform { padding: 10px 10px 6px; }.confirm h3, .qform h3 { font-size: 14px; overflow-wrap: anywhere; }
.confirm p { margin-top: 6px; font-size: 12.5px; }.keeps { color: var(--ink-3); }
.confirm-acts { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; margin-top: 14px; }.save-keys { display: inline-flex; gap: 2px; }
.qhint { min-height: 2lh; margin-top: 8px; font-size: 12px; color: var(--ink-3); }.err { color: var(--danger); }
@media (pointer: coarse), (max-width: 720px) { .mi, .btn, .qform :deep(.field) { min-height: 44px; } }
@media (prefers-reduced-motion: no-preference) { .popover { animation: appear .14s ease-out; } @keyframes appear { from { opacity: 0; } to { opacity: 1; } } }
</style>
