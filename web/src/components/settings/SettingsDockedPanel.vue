<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import AppIcon, { type IconName } from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import { useVisualViewport } from '../../lib/visualViewport'
import { isSettingsField } from '../../lib/settingsOverlays'

// Owns the Settings layout, so a section list keeps its column when a pane
// opens. Consumers supply navigation, page content, pane content and its one
// primary footer action. contextKey is the section/screen/person/role identity;
// changing it closes the pane, while recordKey may change within a series.
const props = defineProps<{
  open: boolean
  title: string
  fact?: string
  icon?: IconName
  opener?: HTMLElement | null
  contextKey?: string
  recordKey?: string
  // A section embedded beside existing Settings navigation measures that
  // containing frame for the shared 1200/720 layout rule.
  layoutFrameSelector?: string
}>()
const emit = defineEmits<{ 'update:open': [open: boolean]; close: [] }>()
const frame = ref<HTMLElement>()
const navigation = ref<HTMLElement>()
const content = ref<HTMLElement>()
const panel = ref<HTMLElement>()
const dock = ref<HTMLElement>()
const paneTitle = ref<HTMLElement>()
const titleId = useId()
const width = ref(0)
const mode = computed(() => width.value >= 1200 ? 'dock' : width.value > 720 ? 'side' : 'sheet')
const modal = computed(() => props.open && mode.value !== 'dock')
const overlay = ref<Record<string, string>>({})
const dockHeight = ref('calc(100dvh - 32px)')
const dockPosition = ref<Record<string, string>>({})
let observer: ResizeObserver | undefined
let layoutFrame: HTMLElement | null = null
let returnTo: HTMLElement | null = null
let savedScroll: { element: HTMLElement; overflow: string; gutter: string; top: number; left: number }[] = []
let preservedInert: { element: HTMLElement; inert: boolean }[] = []
let openGeneration = 0

useVisualViewport(frame, 720, fit)

function fit() {
  if (!frame.value) return
  const rect = (layoutFrame ?? frame.value).getBoundingClientRect()
  width.value = rect.width
  const viewportTop = parseFloat(frame.value.style.getPropertyValue('--vv-top')) || 0
  const viewportBottom = viewportTop + (parseFloat(frame.value.style.getPropertyValue('--vv-h')) || innerHeight)
  const top = Math.max(viewportTop, rect.top)
  const bottom = Math.min(viewportBottom, rect.bottom)
  overlay.value = { left: `${Math.max(0, rect.left)}px`, top: `${top}px`, width: `${Math.max(0, Math.min(innerWidth, rect.right) - Math.max(0, rect.left))}px`, height: `${Math.max(0, bottom - top)}px` }
  dockHeight.value = `${Math.max(0, innerHeight - Math.max(16, rect.top) - 16)}px`
  if (layoutFrame && mode.value === 'dock' && dock.value) {
    // The existing shell owns scrolling. Pin this embedded dock within its
    // visible viewport, so reaching a low list row cannot hide pane controls.
    let scrollFrame: HTMLElement | null = frame.value.parentElement
    while (scrollFrame && !/auto|scroll/.test(getComputedStyle(scrollFrame).overflowY)) scrollFrame = scrollFrame.parentElement
    const bounds = scrollFrame?.getBoundingClientRect()
    const dockRect = dock.value.getBoundingClientRect()
    const dockTop = Math.max(viewportTop, bounds?.top ?? viewportTop) + 16
    const dockBottom = Math.min(viewportBottom, bounds?.bottom ?? viewportBottom) - 16
    dockHeight.value = `${Math.max(0, dockBottom - dockTop)}px`
    dockPosition.value = { position: 'fixed', top: `${dockTop}px`, left: `${dockRect.left}px`, width: `${dockRect.width}px` }
  } else dockPosition.value = {}
}
function releaseBackground() {
  for (const { element, inert } of preservedInert) element.inert = inert
  preservedInert = []
  for (const { element, overflow, gutter, top, left } of savedScroll) {
    element.style.overflow = overflow
    element.style.scrollbarGutter = gutter
    element.scrollTop = top; element.scrollLeft = left
  }
  savedScroll = []
}
function holdBackground() {
  releaseBackground()
  if (!modal.value) return
  const externalNavigation = layoutFrame?.querySelector<HTMLElement>('.nav-col')
  for (const element of [navigation.value, content.value, externalNavigation]) {
    if (element) { preservedInert.push({ element, inert: element.inert }); element.inert = true }
  }
  // Keep these elements as scroll containers: clip would reset their offsets.
  // A stable gutter preserves the space occupied by a classic scrollbar.
  const elements = new Set<HTMLElement>([document.documentElement, document.body])
  for (let parent = frame.value?.parentElement; parent; parent = parent.parentElement) {
    if (parent.scrollHeight > parent.clientHeight) elements.add(parent)
  }
  for (const element of elements) {
    savedScroll.push({ element, overflow: element.style.overflow, gutter: element.style.scrollbarGutter, top: element.scrollTop, left: element.scrollLeft })
    const style = getComputedStyle(element)
    const scrollbar = element === document.documentElement ? innerWidth - element.clientWidth
      : element.offsetWidth - element.clientWidth - parseFloat(style.borderLeftWidth) - parseFloat(style.borderRightWidth)
    if (scrollbar > 0 && style.scrollbarGutter === 'auto') element.style.scrollbarGutter = 'stable'
    element.style.overflow = 'hidden'
  }
}
function restoreFocus() {
  if (returnTo?.isConnected && !returnTo.closest('[inert]')) returnTo.focus({ preventScroll: true })
  returnTo = null
}
function close(restore = true) {
  if (!props.open) return
  openGeneration++
  releaseBackground()
  emit('update:open', false); emit('close')
  if (restore) void nextTick(restoreFocus)
  else returnTo = null
}
function keys(event: KeyboardEvent) {
  if (!props.open || event.defaultPrevented) return
  // An active shared popover owns the innermost Escape and Tab handling.
  if (document.querySelector('.popover')) return
  if (event.key === 'Escape') {
    event.preventDefault(); event.stopImmediatePropagation()
    if (isSettingsField(event.target) && panel.value?.contains(event.target)) {
      (event.target as HTMLElement).blur(); paneTitle.value?.focus({ preventScroll: true })
    } else close()
    return
  }
  if (event.key !== 'Tab' || !modal.value) return
  const controls = [...(panel.value?.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], input:not(:disabled), textarea:not(:disabled), select:not(:disabled), [tabindex="0"]') ?? [])]
    .filter(element => element.getClientRects().length && !element.closest('[inert], [hidden]'))
  const first = controls[0], last = controls.at(-1)
  if (!first || !last) { event.preventDefault(); paneTitle.value?.focus(); return }
  if (!panel.value?.contains(document.activeElement) || document.activeElement === paneTitle.value || (event.shiftKey && document.activeElement === first) || (!event.shiftKey && document.activeElement === last)) {
    event.preventDefault(); (event.shiftKey ? last : first).focus({ preventScroll: true })
  }
}
function scroll() { fit() }
async function preserveRow(before: DOMRect | undefined, anchor: HTMLElement | null) {
  await nextTick()
  if (!before || !anchor?.isConnected || mode.value !== 'dock') return
  const delta = anchor.getBoundingClientRect().top - before.top
  if (Math.abs(delta) <= .5) return
  // Compensate in the nearest scrolling ancestor, rather than assuming the
  // document scrolls (Aeon screens also use a shell scroll container).
  for (let parent = anchor.parentElement; parent; parent = parent.parentElement) {
    if (parent.scrollHeight > parent.clientHeight && /auto|scroll/.test(getComputedStyle(parent).overflowY)) { parent.scrollTop += delta; return }
  }
  window.scrollBy(0, delta)
}
watch(() => props.open, async open => {
  const generation = ++openGeneration
  const anchor = open ? props.opener ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null) : returnTo
  const before = anchor?.getBoundingClientRect()
  if (!open) {
    releaseBackground()
    await preserveRow(before, anchor)
    if (generation === openGeneration) restoreFocus()
    return
  }
  returnTo = anchor
  await nextTick()
  if (generation !== openGeneration || !props.open) return
  fit(); holdBackground()
  await preserveRow(before, anchor)
  if (generation === openGeneration) paneTitle.value?.focus({ preventScroll: true })
}, { flush: 'pre' })
watch(mode, async () => {
  if (!props.open) return
  await nextTick(); fit(); holdBackground()
  if (modal.value && !panel.value?.contains(document.activeElement)) paneTitle.value?.focus({ preventScroll: true })
})
watch(() => props.contextKey, () => close(false))
watch(() => props.recordKey, async () => {
  if (!props.open) return
  const action = document.activeElement instanceof HTMLButtonElement && panel.value?.contains(document.activeElement) ? document.activeElement : null
  returnTo = props.opener ?? returnTo
  await nextTick()
  if (!props.open) return
  const body = panel.value?.querySelector<HTMLElement>('.pane-body')
  if (body) body.scrollTop = 0
  if (action?.isConnected) action.focus({ preventScroll: true })
  else paneTitle.value?.focus({ preventScroll: true })
})
onMounted(async () => {
  observer = new ResizeObserver(fit)
  layoutFrame = props.layoutFrameSelector ? frame.value?.closest<HTMLElement>(props.layoutFrameSelector) ?? null : null
  if (frame.value) observer.observe(frame.value)
  if (layoutFrame) observer.observe(layoutFrame)
  fit()
  window.addEventListener('resize', fit)
  document.addEventListener('scroll', scroll, true)
  window.addEventListener('keydown', keys)
  if (props.open) { returnTo = props.opener ?? null; await nextTick(); holdBackground(); paneTitle.value?.focus({ preventScroll: true }) }
})
onBeforeUnmount(() => {
  openGeneration++; observer?.disconnect(); releaseBackground(); restoreFocus()
  window.removeEventListener('resize', fit)
  document.removeEventListener('scroll', scroll, true)
  window.removeEventListener('keydown', keys)
})
defineExpose({ frame, close })
</script>
<template>
  <div ref="frame" class="settings-frame" :class="[`mode-${mode}`, { 'dock-open': open, 'has-navigation': !!$slots.navigation }]">
    <nav v-if="$slots.navigation" ref="navigation" class="dock-navigation" aria-label="Settings sections"><slot name="navigation" /></nav>
    <div ref="content" class="dock-content"><slot /></div>
    <div v-if="open" ref="dock" class="dock" :class="{ modal }" :style="modal ? overlay : undefined" @pointerdown.self.prevent="close()">
      <section ref="panel" class="panel pane" role="dialog" :aria-modal="modal || undefined" :aria-labelledby="titleId" :style="{ '--dock-h': dockHeight, ...dockPosition }">
        <header class="pane-bar">
          <AppIcon v-if="icon" :name="icon" />
          <div class="pane-title"><h2 :id="titleId" ref="paneTitle" tabindex="-1">{{ title }}</h2><p v-if="fact">{{ fact }}</p></div>
          <slot name="overflow" />
          <button type="button" class="btn sm ghost pane-close" aria-label="Close details" aria-keyshortcuts="Escape" @click="close()"><AppIcon name="close" :size="14" /><span>Close</span><KeyCap k="Esc" /></button>
        </header>
        <div class="pane-body"><slot name="panel" /></div>
        <footer v-if="$slots.footer" class="pane-foot"><slot name="footer" /></footer>
      </section>
    </div>
  </div>
</template>
<style scoped>
.settings-frame { display: grid; grid-template-columns: minmax(0, 1fr); gap: 24px; align-items: start; min-width: 0; min-height: calc(100dvh - 32px); position: relative; }
.has-navigation { grid-template-columns: 248px minmax(0, 1fr); }.mode-sheet.has-navigation { grid-template-columns: minmax(0, 1fr); }
.dock-navigation { min-width: 0; position: sticky; top: 16px; }.mode-sheet .dock-navigation { position: static; }
.dock-content { min-width: 0; }.dock { min-width: 0; align-self: stretch; }
.mode-dock.dock-open { grid-template-columns: minmax(0, 1fr) clamp(400px, calc(100% - 900px), 560px); }
.mode-dock.has-navigation.dock-open { grid-template-columns: 248px minmax(0, 1fr) clamp(400px, calc(100% - 900px), 560px); }
.mode-dock .dock { grid-column: -2; grid-row: 1; }
.panel { container: settings-pane / inline-size; position: sticky; top: 16px; display: flex; flex-direction: column; height: var(--dock-h); border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
.pane-bar { display: flex; align-items: flex-start; gap: 10px; flex: none; min-height: 62px; padding: 10px 10px 10px 16px; border-bottom: 1px solid var(--line); }.pane-bar > svg { flex: none; margin-top: 4px; }
.pane-title { flex: 1; min-width: 0; }.pane-title h2 { font-size: 16px; overflow-wrap: anywhere; }.pane-title p { font-size: 12.5px; color: var(--ink-2); overflow-wrap: anywhere; }.pane-close { flex: none; gap: 6px; padding: 4px 8px 4px 6px; color: var(--ink-2); }
.pane-body { flex: 1; min-height: 0; overflow: auto; overscroll-behavior: contain; padding: 18px 22px 28px; }
.pane-foot { flex: none; display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 12px; padding: 12px 16px; border-top: 1px solid var(--line); border-radius: 0 0 var(--radius) var(--radius); background: var(--surface-raised-2); }
.modal { position: fixed; z-index: 60; background: var(--scrim); display: flex; justify-content: flex-end; }
.modal .panel { position: relative; top: 0; width: min(480px, 100%); height: 100%; }
.mode-sheet .modal { background: var(--canvas); }.mode-sheet .panel { width: 100%; border-radius: 0; border: 0; box-shadow: none; background: var(--canvas); }
.mode-sheet .pane-bar { padding-top: max(10px, env(safe-area-inset-top)); }.mode-sheet .pane-body { padding: 16px 16px 24px; }.mode-sheet .pane-foot { border-radius: 0; padding: 10px 16px calc(10px + env(safe-area-inset-bottom)); background: var(--surface-raised); }.mode-sheet .pane-foot :deep(.btn) { flex: 1; min-height: 44px; }
.mode-sheet .pane-bar :deep(button), .mode-sheet .pane-body :deep(input), .mode-sheet .pane-body :deep(select) { min-height: 44px; }
.mode-sheet .pane-bar :deep(button) { min-width: 44px; }
@media (pointer: coarse) { .pane-bar :deep(button), .pane-foot :deep(button) { min-height: 44px; } }
@media (prefers-reduced-motion: no-preference) { .modal .panel { animation: pane-in .18s ease-out; } @keyframes pane-in { from { opacity: 0; transform: translateX(12px); } to { opacity: 1; transform: translateX(0); } } }
</style>
<style scoped src="../../styles/settingsButtons.css"></style>
