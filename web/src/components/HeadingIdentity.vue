<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { isClipped } from '../directives/clipTip'

// Capped headings own an interactive reader: the shared decorative tooltip
// cannot scroll. Keep this exception at the heading call sites (AEON-631).
defineOptions({ inheritAttrs: false })
const props = withDefaults(defineProps<{ text: string; as?: 'span' | 'h1' }>(), { as: 'span' })
const source = ref<HTMLElement>()
const reader = ref<HTMLElement>()
const clipped = ref(false)
const opened = ref(false)
const layer = ref<HTMLElement | null>(null)
const x = ref(8), y = ref(8)
const id = useId()
let observer: ResizeObserver | undefined
let mutation: MutationObserver | undefined
let timer: ReturnType<typeof setTimeout> | undefined
let restoring = false

function place() {
  if (!source.value || !reader.value) return
  const anchor = source.value.getBoundingClientRect(), box = reader.value.getBoundingClientRect()
  x.value = Math.min(Math.max(8, anchor.left + (anchor.width - box.width) / 2), innerWidth - box.width - 8)
  const above = anchor.top - box.height - 8
  y.value = Math.min(Math.max(8, above >= 8 ? above : anchor.bottom + 8), innerHeight - box.height - 8)
}
function measure() {
  clipped.value = !!source.value && isClipped(source.value)
  if (!clipped.value) close()
  else if (!restoring && document.activeElement === source.value && source.value?.matches(':focus-visible')) void open()
  if (opened.value) void nextTick(place)
}
async function open(focus = false) {
  clearTimeout(timer)
  if (!clipped.value || !source.value?.isConnected) return
  const text = props.text, element = source.value
  layer.value = element.closest<HTMLElement>('dialog[open]')
  opened.value = true
  await nextTick()
  // A series item or selected profile may change while its reader mounts.
  if (!opened.value || props.text !== text || source.value !== element) return
  place()
  if (focus) reader.value?.focus({ preventScroll: true })
}
function close(restore = false) {
  clearTimeout(timer)
  opened.value = false
  if (restore) {
    restoring = true
    source.value?.focus({ preventScroll: true })
    restoring = false
  }
}
function focus() {
  if (!restoring && source.value?.matches(':focus-visible')) void open()
}
function hover(event: PointerEvent) {
  if (event.pointerType === 'touch') return
  clearTimeout(timer)
  timer = setTimeout(() => void open(), 380)
}
function keep() { clearTimeout(timer) }
function leave(event: PointerEvent) {
  if (event.pointerType === 'touch' || source.value === document.activeElement || reader.value === document.activeElement) return
  clearTimeout(timer)
  timer = setTimeout(() => close(), 160)
}
function outside(event: Event) {
  if (event.target instanceof Node && (source.value?.contains(event.target) || reader.value?.contains(event.target))) return
  close()
}
function scroll(event: Event) {
  if (event.target === reader.value) return
  close()
}
function keys(event: KeyboardEvent) {
  if (event.key === 'Escape' && opened.value) {
    event.preventDefault(); event.stopPropagation(); close(true)
  } else if (event.currentTarget === source.value && event.key === 'ArrowDown' && clipped.value && !event.metaKey && !event.ctrlKey && !event.altKey) {
    event.preventDefault(); event.stopPropagation(); void open(true)
  }
}
watch(() => props.text, () => { close(); void nextTick(measure) })
onMounted(() => {
  observer = new ResizeObserver(measure)
  observer.observe(source.value!)
  // Includes live style changes; fonts and viewport changes use ResizeObserver.
  mutation = new MutationObserver(measure)
  mutation.observe(source.value!, { attributes: true, attributeFilter: ['style', 'class'] })
  document.addEventListener('pointerdown', outside)
  document.addEventListener('focusin', outside)
  document.addEventListener('scroll', scroll, true)
  window.addEventListener('resize', measure)
  measure()
})
onBeforeUnmount(() => {
  close(); observer?.disconnect(); mutation?.disconnect()
  document.removeEventListener('pointerdown', outside)
  document.removeEventListener('focusin', outside)
  document.removeEventListener('scroll', scroll, true)
  window.removeEventListener('resize', measure)
})
</script>

<template>
  <component :is="as" ref="source" v-bind="$attrs" tabindex="0"
    :data-heading-tip="clipped ? text : undefined" :data-clip-tip="clipped ? '' : undefined"
    :aria-controls="clipped ? id : undefined" :aria-expanded="clipped ? opened : undefined"
    @focus="focus" @pointerenter="hover" @pointerleave="leave" @click="open(true)" @keydown="keys"
  >{{ text }}</component>
  <Teleport :to="layer ?? 'body'">
    <div v-if="opened" :id="id" ref="reader" class="tooltip heading-reader" role="region" aria-label="Full heading" tabindex="0"
      :style="{ left: `${x}px`, top: `${y}px` }" @keydown="keys"
      @pointerenter="keep" @pointerleave="leave"
    >{{ text }}</div>
  </Teleport>
</template>

<style scoped>
.heading-reader {
  position: fixed; z-index: 80; box-sizing: border-box;
  width: max-content; max-width: min(320px, calc(100vw - 16px)); max-height: calc(100dvh - 16px);
  padding: 5px 10px; border-radius: 8px; overflow: auto; overscroll-behavior: contain;
  background: var(--tip-bg); color: var(--tip-ink); font-size: 12.5px; line-height: 1.4;
  white-space: pre-line; overflow-wrap: anywhere;
  box-shadow: 0 0 0 1px var(--glass-rim), 0 10px 24px -10px rgba(0, 0, 0, .5);
}
.heading-reader:focus-visible { outline: 2px solid var(--ink-2); outline-offset: -2px; }
</style>
