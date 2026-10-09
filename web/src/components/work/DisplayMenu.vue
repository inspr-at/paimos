<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { fitMenu, maxMenuColumns, MENU_COLUMN_GAP, MENU_COLUMN_WIDTH } from '../../lib/menuColumns'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'

// The Display menu of a collapsed project header: what the header hides (sections,
// saved views, Hide closed) above the usual display options. It never scrolls.
// A popover that is too tall for the room under its button flows into more
// columns, counted once when it opens and again when the window resizes. Saved-view
// rows flow across those columns; only a short window also splits the column list.
// Once open, each block keeps the place that count gave it, so a sort key cannot
// rebalance the menu. A narrow window gets a sheet: a pinned title and action
// around a body that scrolls. `plain` is every other header: just the display
// options, in the popover as it was.
const props = defineProps<{ anchor: HTMLElement | null; sheet: boolean; plain?: boolean }>()
const emit = defineEmits<{ columns: [count: number]; done: [] }>()
const body = ref<HTMLElement>()
const columns = ref(1)
// What the popover takes below its button besides the content: the gap to the button and the
// margin to the window edge (12), its padding and border (14) and the menu's own padding (10).
const BELOW = 36
// Blocks pinned for this open. Cleared before every recount (resize, sheet).
let pinned: HTMLElement[] = []
let sortWatch: ResizeObserver | null = null
let alive = true

function stopWatch() { sortWatch?.disconnect(); sortWatch = null }

function unpin(el: HTMLElement) {
  stopWatch()
  for (const item of pinned) {
    item.style.position = ''
    item.style.left = ''
    item.style.top = ''
    item.style.width = ''
    item.style.margin = ''
  }
  pinned = []
  el.style.position = ''
  el.style.height = ''
  delete el.dataset.pinned
}

// In-flow boxes only. display:contents parents (the saved-view list, and the
// column list when it may split) contribute their children, each row whole.
function flowBoxes(el: HTMLElement): HTMLElement[] {
  const out: HTMLElement[] = []
  const walk = (node: Element) => {
    for (const child of node.children) {
      const style = getComputedStyle(child)
      if (style.display === 'contents') { walk(child); continue }
      if (style.position === 'absolute' || style.position === 'fixed') continue
      out.push(child as HTMLElement)
    }
  }
  walk(el)
  return out
}

function contentBottom(el: HTMLElement) {
  const top = el.getBoundingClientRect().top
  const boxes = flowBoxes(el)
  if (!boxes.length) return 0
  return Math.max(...boxes.map(box => box.getBoundingClientRect().bottom - top + (Number.parseFloat(getComputedStyle(box).marginBottom) || 0)))
}

function spills(el: HTMLElement) {
  const frame = el.getBoundingClientRect()
  return flowBoxes(el).some(box => {
    const rect = box.getBoundingClientRect()
    const margin = Number.parseFloat(getComputedStyle(box).marginBottom) || 0
    return rect.right > frame.right + 1 || rect.bottom + margin > frame.bottom + 1
  })
}

// Freeze the open layout. The sort block sits last, so its keys grow downward
// and the blocks measured here stay where they are. Every box is measured
// before any leaves the flow: taking one out reflows the rest onto it.
function pin(el: HTMLElement) {
  stopWatch()
  const origin = el.getBoundingClientRect()
  const placed = flowBoxes(el).map(item => {
    const rect = item.getBoundingClientRect()
    const margin = Number.parseFloat(getComputedStyle(item).marginBottom) || 0
    return { item, rect, margin }
  })
  let base = 0
  const next: HTMLElement[] = []
  for (const { item, rect, margin } of placed) {
    if (!item.classList.contains('sort-editor')) base = Math.max(base, rect.bottom - origin.top + margin)
    item.style.position = 'absolute'
    item.style.left = `${rect.left - origin.left}px`
    item.style.top = `${rect.top - origin.top}px`
    item.style.width = `${rect.width}px`
    item.style.margin = '0'
    next.push(item)
  }
  pinned = next
  el.style.position = 'relative'
  el.dataset.pinned = ''
  const sort = placed.find(entry => entry.item.classList.contains('sort-editor'))?.item ?? null
  const apply = () => {
    if (el.dataset.pinned === undefined) return
    const sortBottom = sort ? (Number.parseFloat(sort.style.top) || 0) + sort.offsetHeight : 0
    const height = `${Math.ceil(Math.max(base, sortBottom))}px`
    if (el.style.height !== height) el.style.height = height
  }
  apply()
  if (sort && typeof ResizeObserver !== 'undefined') {
    sortWatch = new ResizeObserver(apply)
    sortWatch.observe(sort)
  }
}

// Smallest height at which `count` columns hold the menu, or one past `room`
// when even `room` cannot.
function lay(el: HTMLElement, count: number, split: boolean, room: number) {
  unpin(el)
  el.style.setProperty('--cols', String(count))
  el.toggleAttribute('data-split', split)
  const pack = (height: number) => { el.style.height = `${height}px`; return !spills(el) }
  if (pack(room)) {
    const used = Math.ceil(contentBottom(el))
    if (used < room - 1 && pack(used)) return Math.ceil(contentBottom(el))
    pack(room)
    return Math.min(room, Math.ceil(contentBottom(el)))
  }
  let lo = room
  let hi = room
  while (!pack(hi) && hi < 8000) hi = Math.min(8000, Math.ceil(hi * 1.5 + 40))
  if (!pack(hi)) return room + 1
  while (hi - lo > 1) {
    const mid = Math.floor((lo + hi) / 2)
    if (pack(mid)) hi = mid
    else lo = mid
  }
  pack(hi)
  return hi
}

function fit() {
  const el = body.value
  if (!el || props.plain || !alive) return
  unpin(el)
  if (props.sheet || !props.anchor) {
    el.style.removeProperty('--cols')
    el.toggleAttribute('data-split', false)
    columns.value = 1
    emit('columns', 1)
    return
  }
  const room = Math.max(1, innerHeight - props.anchor.getBoundingClientRect().bottom - BELOW)
  const chosen = fitMenu((count, split) => lay(el, count, split, room), room, maxMenuColumns(innerWidth))
  lay(el, chosen.columns, chosen.split, room)
  pin(el)
  columns.value = chosen.columns
  emit('columns', chosen.columns)
}
let frame = 0
const resized = () => { cancelAnimationFrame(frame); frame = requestAnimationFrame(() => { frame = 0; fit() }) }
watch(() => props.sheet, () => fit())
// Before the panel is placed, so its width already follows the count.
onMounted(() => {
  fit()
  window.addEventListener('resize', resized)
  void document.fonts.ready.then(() => { if (alive) fit() })
})
onBeforeUnmount(() => { alive = false; cancelAnimationFrame(frame); window.removeEventListener('resize', resized); if (body.value) unpin(body.value) })
</script>

<template>
  <slot v-if="plain" />
  <div v-else class="display-menu" :class="{ 'as-sheet': sheet }" :style="{ '--col-w': `${MENU_COLUMN_WIDTH}px`, '--col-gap': `${MENU_COLUMN_GAP}px` }">
    <header v-if="sheet"><h2>Display</h2></header>
    <div ref="body" class="menu-body" :data-columns="columns">
      <slot name="nav" />
      <slot name="hide" />
      <slot />
    </div>
    <footer v-if="sheet"><button type="button" class="btn primary done" @click="emit('done')"><AppIcon name="check" :size="14" />Done<KeyCap k="esc" /></button></footer>
  </div>
</template>

<style scoped>
.display-menu { padding: 4px 8px 6px; text-align: left; font-weight: 400; }
/* Blocks flow down a column and on to the next. Sequential, not balanced:
   balancing would move blocks when a sort key is added. The count is frozen
   for the open, and pin() then keeps each block in the place it landed. */
.display-menu:not(.as-sheet) .menu-body {
  display: flex; flex-direction: column; flex-wrap: wrap; align-content: flex-start; align-items: flex-start;
  column-gap: var(--col-gap); width: calc(var(--cols, 1) * var(--col-w) + (var(--cols, 1) - 1) * var(--col-gap));
}
.menu-body > :deep(*), .menu-body :deep(.display-panel > *) { margin: 0 0 16px; }
.display-menu:not(.as-sheet) .menu-body > :deep(*),
.display-menu:not(.as-sheet) .menu-body :deep(.display-panel > *) { width: var(--col-w); max-width: var(--col-w); min-width: 0; flex: none; margin-bottom: 12px; }
/* Saved-view rows flow across columns. The nav and its list generate no box, so a
   couple dozen views are not one unbreakable block. Each row stays whole. */
.display-menu:not(.as-sheet) .menu-body :deep(.nav-views),
.display-menu:not(.as-sheet) .menu-body :deep(.nav-views .rows) { display: contents; }
.display-menu:not(.as-sheet) .menu-body :deep(.nav-views .eyebrow),
.display-menu:not(.as-sheet) .menu-body :deep(.nav-views .row) { width: var(--col-w); max-width: var(--col-w); min-width: 0; flex: none; }
.display-menu:not(.as-sheet) .menu-body :deep(.nav-views .eyebrow) { margin: 0 0 4px; }
.display-menu:not(.as-sheet) .menu-body :deep(.nav-views .row) { margin: 0 0 1px; }
.display-menu:not(.as-sheet) .menu-body :deep(.nav-views .row:last-child) { margin-bottom: 12px; }
/* Sort is last, so its keys grow downward, away from every other block. */
.display-menu:not(.as-sheet) .menu-body :deep(.sort-editor) { order: 1; }
/* In a window too short for whole blocks, the column list may break between its rows. */
.display-menu:not(.as-sheet) .menu-body[data-split] :deep(.columns),
.display-menu:not(.as-sheet) .menu-body[data-split] :deep(.columns .list) { display: contents; }
.display-menu:not(.as-sheet) .menu-body[data-split] :deep(.columns .head) { width: var(--col-w); max-width: var(--col-w); min-width: 0; flex: none; margin: 0 0 6px; }
.display-menu:not(.as-sheet) .menu-body[data-split] :deep(.columns .row) { width: var(--col-w); max-width: var(--col-w); min-width: 0; flex: none; margin: 0 0 1px; }
.display-menu:not(.as-sheet) .menu-body[data-split] :deep(.columns .fine) { width: var(--col-w); max-width: var(--col-w); min-width: 0; flex: none; margin: 0 0 12px; }
.menu-body :deep(.display-panel) { display: contents; }
.menu-body :deep(.display-panel > .section) { margin-top: 0; padding-top: 0; border-top: 0; }

.as-sheet { display: flex; flex-direction: column; height: 100%; max-height: calc(var(--floating-max) - 12px); padding: 0; }
.as-sheet header { flex: none; padding: calc(10px + env(safe-area-inset-top)) 20px 8px; }
.as-sheet h2 { font-size: 19px; }
/* A flex column, so the panel's own `order` (sort keys last) still applies. */
.as-sheet .menu-body { display: flex; flex-direction: column; flex: 1; min-height: 0; width: auto; overflow: auto; overscroll-behavior: contain; padding: 4px 16px 16px; }
.as-sheet .menu-body > :deep(*), .as-sheet .menu-body :deep(.display-panel > *) { margin-bottom: 20px; }
.as-sheet footer { flex: none; padding: 12px 16px calc(12px + env(safe-area-inset-bottom)); border-top: 1px solid var(--line); }
.done { width: 100%; height: 46px; font-size: 15px; }
@media (hover: none) { .done :deep(.keycap) { display: none; } }
</style>
