<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// Learn (AEON-994 draft 5): plain words first, the proper term (p50, p90, wall,
// flake …) and the Expert name kept next to them. Hover or focus shows it, a click
// or Enter pins it, Esc closes it and leaves focus on the button. The popover is
// fixed on the body, so it never moves the page (AEON-541) and closes when a scroll
// moves its button away. One open at a time.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ id: string; name: string; body: string; expert: string; target: string; labels: { learn: string; expertName: string; arion: string } }>()
const button = ref<HTMLButtonElement>()
const tip = ref<HTMLElement>()
const open = ref(false)
const pinned = ref(false)
const placed = ref(false)
const at = ref({ x: 0, y: 0 })
// Where the button sat when the words were placed: a scroll only matters once it has moved the button.
let anchoredAt: { x: number; y: number } | null = null
const tipId = computed(() => `dl-learn-${props.id}`)

// Opening one Learn closes the others; while one is pinned, pointing at another does not open it.
function show(pin: boolean) {
  if (!pin && openLearn.value && openLearn.value !== close && pinnedAnywhere.value) return
  if (openLearn.value && openLearn.value !== close) openLearn.value()
  openLearn.value = close
  open.value = true
  pinned.value = pin
  pinnedAnywhere.value = pin
  placed.value = false
  anchoredAt = null
  void nextTick(place)
}
function close() {
  open.value = false
  pinned.value = false
  anchoredAt = null
  if (openLearn.value === close) { openLearn.value = null; pinnedAnywhere.value = false }
}
function place() {
  const el = tip.value, anchor = button.value
  if (!el || !anchor || !open.value) return
  const rect = anchor.getBoundingClientRect(), width = el.offsetWidth, height = el.offsetHeight
  let y = rect.bottom + 8
  if (y + height > window.innerHeight - 12) y = Math.max(12, rect.top - height - 8)
  at.value = { x: Math.max(12, Math.min(window.innerWidth - width - 12, rect.right - width)), y }
  anchoredAt = { x: rect.left, y: rect.top }
  placed.value = true
}
function onClick() { if (open.value && pinned.value) close(); else show(true) }
function onLeave() { if (!pinned.value) close() }
function onKey(event: KeyboardEvent) {
  if (event.key !== 'Escape' || !open.value) return
  event.stopPropagation()
  close()
  button.value?.focus({ preventScroll: true })
}
function onPointer(event: PointerEvent) {
  if (open.value && pinned.value && !tip.value?.contains(event.target as Node) && !button.value?.contains(event.target as Node)) close()
}
// The words are fixed on the body, so they close when a scroll carries their button away. A scroll
// event is also reported late (the browser bringing the tapped button into view, a layout settling)
// after the words were placed against the button's final spot; that one moved nothing.
function onScroll() {
  const anchor = button.value
  if (!open.value || !anchor || !anchoredAt) return
  const rect = anchor.getBoundingClientRect()
  if (Math.abs(rect.left - anchoredAt.x) < 1 && Math.abs(rect.top - anchoredAt.y) < 1) return
  close()
}
onMounted(() => {
  document.addEventListener('keydown', onKey)
  document.addEventListener('pointerdown', onPointer, true)
  window.addEventListener('scroll', onScroll, { passive: true, capture: true })
})
onBeforeUnmount(() => {
  close()
  document.removeEventListener('keydown', onKey)
  document.removeEventListener('pointerdown', onPointer, true)
  window.removeEventListener('scroll', onScroll, { capture: true })
})
// The words belong to the number on screen: another window or answer closes it.
watch(() => props.body, () => { if (open.value) close() })
</script>

<script lang="ts">
import { ref as moduleRef } from 'vue'
const openLearn = moduleRef<(() => void) | null>(null)
const pinnedAnywhere = moduleRef(false)
</script>

<template>
  <button ref="button" type="button" class="learn" :aria-expanded="open" :aria-label="`${labels.learn}: ${name}`" :aria-describedby="open ? tipId : undefined"
    @click="onClick" @mouseenter="show(false)" @mouseleave="onLeave" @focus="show(false)" @blur="close"><AppIcon name="book" :size="13" />{{ labels.learn }}</button>
  <Teleport to="body">
    <div v-if="open" :id="tipId" ref="tip" class="dl-learn" role="tooltip" :style="{ left: `${at.x}px`, top: `${at.y}px`, visibility: placed ? undefined : 'hidden' }" @mouseleave="onLeave">
      <b>{{ name }}</b>
      <p>{{ body }}</p>
      <p class="meta">{{ labels.expertName }}: {{ expert }}<br>{{ target }} · {{ labels.arion }}</p>
    </div>
  </Teleport>
</template>

<style scoped>
.learn { position: relative; display: inline-flex; align-items: center; justify-content: center; gap: 5px; flex: none; height: 26px; padding: 0 10px; border: 0; border-radius: 999px; background: var(--surface-sunken); color: var(--teal-ink); font: 600 12px/1 var(--font); white-space: nowrap; cursor: pointer; }
.learn::after { content: ''; position: absolute; inset: -9px -4px; }
.learn:hover, .learn[aria-expanded="true"] { background: var(--row-hover); }
.learn:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.learn :deep(svg) { flex: none; }
@container delivery (max-width: 640px) {
  .learn { height: 44px; padding: 0 14px; }
  .learn::after { inset: 0; }
}
@media (pointer: coarse) { .learn { min-height: 44px; } .learn::after { inset: 0; } }
</style>

<style>
.dl-learn { position: fixed; z-index: 80; width: max-content; max-width: min(340px, calc(100vw - 24px)); padding: 10px 12px; border-radius: 12px; background: var(--tip-bg); color: var(--tip-ink); font-size: 12.5px; line-height: 1.45; box-shadow: var(--shadow-pop); }
.dl-learn b { display: block; margin-bottom: 3px; font-weight: 650; }
.dl-learn p { margin: 0; color: inherit; opacity: .92; }
.dl-learn p + p { margin-top: 6px; }
.dl-learn .meta { font: 500 11px/1.5 var(--mono); opacity: .75; }
</style>
