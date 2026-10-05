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
import { useVisualViewport } from '../../lib/visualViewport'
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
const stagedCopy = ref<HTMLElement>()
const stagedActions = ref<HTMLElement>()
const contentScrolls = ref(false)
let stageObserver: ResizeObserver | undefined
const confirmation = computed(() => selected.value?.confirmation ?? props.confirmation)
const confirming = computed(() => !!selected.value || props.mode === 'confirmation')
const role = computed(() => props.mode === 'menu' && !confirming.value ? 'menu' : 'dialog')
const hintId = useId()
const errorId = useId()
const placed = ref(false)
const opensUp = ref(false)
const position = ref<Record<string, string>>({})
let release: (() => void) | undefined
let triggerRect: DOMRect | undefined
let opening = 0
let scrollCheck = 0
let previousExpanded: string | null = null
let ownedAnchor: HTMLElement | null = null

useVisualViewport(panel, 720, () => { if (props.open) place() })

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
  const viewportTop = parseFloat(panel.value.style.getPropertyValue('--vv-top')) || 0
  const viewportBottom = viewportTop + (parseFloat(panel.value.style.getPropertyValue('--vv-h')) || innerHeight)
  const top = Math.max(viewportTop, frame?.top ?? viewportTop) + 12
  const bottom = Math.min(viewportBottom, frame?.bottom ?? viewportBottom) - 12
  const width = Math.max(0, Math.min(320, right - left))
  panel.value.style.width = `${width}px`
  const contentHeight = panel.value.scrollHeight + 2
  const frameHeight = Math.max(0, bottom - top)
  const up = !!props.anchor.closest('.pane-foot') || (trigger.bottom + 4 + Math.min(contentHeight, frameHeight) > bottom && trigger.top - top > bottom - trigger.bottom)
  opensUp.value = up
  const available = Math.max(0, up ? trigger.top - 4 - top : bottom - trigger.bottom - 4)
  const maxHeight = Math.min(frameHeight, available)
  // Place the height that can actually fit on the chosen side. Using the full
  // content height here would slide a constrained panel across its trigger.
  const height = Math.min(contentHeight, maxHeight)
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
// Switch to a pinned footer only when the ordinary staged content cannot fit.
// Measure copy separately from feedback so a failed write cannot move actions
// by changing this layout. Short forms retain their natural, unpadded height.
function fitStagedContent() {
  const copy = stagedCopy.value, actions = stagedActions.value, root = panel.value
  if (!copy || !actions || !root) { contentScrolls.value = false; return }
  const stage = actions.parentElement!
  const rootStyle = getComputedStyle(root), stageStyle = getComputedStyle(stage), actionStyle = getComputedStyle(actions)
  const space = (style: CSSStyleDeclaration) => ['padding-top', 'padding-bottom', 'border-top-width', 'border-bottom-width']
    .reduce((total, property) => total + (parseFloat(style.getPropertyValue(property)) || 0), 0)
  const required = copy.getBoundingClientRect().height + actions.getBoundingClientRect().height
    + (parseFloat(actionStyle.marginTop) || 0) + space(rootStyle) + space(stageStyle)
  contentScrolls.value = required > parseFloat(rootStyle.maxHeight)
}
watch([stagedCopy, stagedActions, position], () => {
  stageObserver?.disconnect()
  fitStagedContent()
  if (stagedCopy.value && stagedActions.value) {
    stageObserver = new ResizeObserver(fitStagedContent)
    stageObserver.observe(stagedCopy.value)
    stageObserver.observe(stagedActions.value)
  }
}, { flush: 'post' })
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
  if (event.key === 'Tab' && !event.metaKey && !event.ctrlKey && !event.altKey) {
    // This teleported overlay is the inner focus scope, including when its
    // trigger belongs to a modal sheet. The sheet yields to this handler.
    const controls = [...panel.value.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], input:not(:disabled), textarea:not(:disabled), select:not(:disabled), [tabindex="0"]')]
      .filter(element => element.tabIndex >= 0 && element.getClientRects().length && !element.closest('[inert], [hidden]'))
    const first = controls[0], last = controls.at(-1)
    if (!first || !last) { event.preventDefault(); event.stopImmediatePropagation(); panel.value.focus({ preventScroll: true }); return }
    if (document.activeElement === panel.value || (event.shiftKey && document.activeElement === first) || (!event.shiftKey && document.activeElement === last)) {
      event.preventDefault(); event.stopImmediatePropagation(); (event.shiftKey ? last : first).focus({ preventScroll: true })
    }
    return
  }
  if (props.mode === 'form' && settingsSubmitKey(event)) { event.preventDefault(); event.stopImmediatePropagation(); submit(); return }
  if (props.mode === 'form' && event.key === 'Enter' && event.target instanceof HTMLInputElement) { event.preventDefault(); return }
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
onBeforeUnmount(() => { stageObserver?.disconnect(); cleanup() })
</script>
<template>
  <Teleport to="body">
    <div v-if="open" ref="panel" class="popover" :class="{ 'opens-up': opensUp, staged: mode === 'form' || confirming, 'content-scrolls': contentScrolls }" :role="role" :aria-label="confirming ? confirmation?.title ?? label : label" :aria-busy="busy || undefined" tabindex="-1" :style="{ ...position, visibility: placed ? 'visible' : 'hidden' }">
      <template v-if="mode === 'menu' && !confirming">
        <button v-for="item in items" :key="item.id" type="button" class="mi" :class="{ danger: item.confirmation }" role="menuitem" :disabled="item.disabled" @click="choose(item)"><AppIcon :name="item.icon ?? 'more'" /><span class="t">{{ item.label }}</span><span v-if="item.detail" class="d">{{ item.detail }}</span></button>
      </template>
      <div v-else-if="confirming && confirmation" class="confirm">
        <div class="staged-body">
          <div ref="stagedCopy"><h3>{{ confirmation.title }}</h3><p>{{ confirmation.effect }}</p><p class="keeps">{{ confirmation.keeps }}</p></div>
          <p v-if="contentScrolls && error" class="err" role="alert">{{ error }}</p>
        </div>
        <div ref="stagedActions" class="confirm-acts"><button type="button" class="btn sm" @click="close()">Cancel</button><button type="button" class="btn sm danger" :disabled="busy" @click="confirm">{{ confirmation.action }}</button></div>
        <p v-if="!contentScrolls && error" class="feedback err" role="alert">{{ error }}</p>
      </div>
      <form v-else-if="mode === 'form'" class="qform" @submit.prevent="submit">
        <div class="staged-body">
          <div ref="stagedCopy"><h3>{{ label }}</h3><div class="form-fields"><slot :hint-id="error ? `${hintId} ${errorId}` : hintId" /></div><p :id="hintId" class="qhint">{{ hint }}</p></div>
          <p v-if="contentScrolls && error" :id="errorId" class="err" role="alert">{{ error }}</p>
        </div>
        <p v-if="!contentScrolls && error" :id="errorId" class="feedback err" role="alert">{{ error }}</p>
        <div ref="stagedActions" class="confirm-acts"><button type="button" class="btn sm" @click="close()">Cancel</button><button type="submit" class="btn sm primary" :disabled="busy" aria-keyshortcuts="Meta+Enter Control+Enter">{{ saveLabel }} <span class="save-keys"><KeyCap k="mod" /><KeyCap k="enter" /></span></button></div>
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
.staged { display: flex; flex-direction: column; overflow: hidden; }
.confirm, .qform { display: flex; flex-direction: column; min-height: 0; padding: 10px 10px 6px; }
.confirm > :not(.feedback), .qform > :not(.feedback) { flex: none; }
.content-scrolls .staged-body { flex: 0 1 auto; min-height: 0; overflow: auto; overflow-wrap: anywhere; overscroll-behavior: contain; }
.staged-body > .err { margin-top: 8px; font-size: 12px; }
/* Feedback grows below a top anchor or above a bottom anchor. Once the
   viewport is full, only feedback scrolls; fields and actions stay put. */
.feedback { order: 1; flex: 0 1 auto; min-height: 0; overflow: auto; overflow-wrap: anywhere; overscroll-behavior: contain; }
.opens-up .feedback { order: -1; }
.confirm h3, .qform h3 { font-size: 14px; overflow-wrap: anywhere; }
.confirm p { margin-top: 6px; font-size: 12.5px; }.keeps { color: var(--ink-3); }
.confirm-acts { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; margin-top: 14px; }.save-keys { display: inline-flex; gap: 2px; }
.qhint { min-height: 2lh; margin-top: 8px; font-size: 12px; color: var(--ink-3); }.err { color: var(--danger); }
@media (pointer: coarse), (max-width: 720px) { .mi, .btn, .qform :deep(.field) { min-height: 44px; } }
@media (prefers-reduced-motion: no-preference) { .popover { animation: appear .14s ease-out; } @keyframes appear { from { opacity: 0; } to { opacity: 1; } } }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
