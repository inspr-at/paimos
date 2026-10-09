<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { fitMenu, maxMenuColumns, MENU_COLUMN_GAP, MENU_COLUMN_WIDTH } from '../../lib/menuColumns'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'

// The Display menu of a collapsed project header: what the header hides (sections,
// saved views, Hide closed) above the usual display options. It never scrolls.
// A popover that is too tall for the room under its button flows into more
// columns, counted once when it opens and again when the window resizes. A narrow
// window gets a sheet: a pinned title and action around a body that scrolls.
// `plain` is every other header: just the display options, in the popover as it was.
const props = defineProps<{ anchor: HTMLElement | null; sheet: boolean; plain?: boolean }>()
const emit = defineEmits<{ columns: [count: number]; done: [] }>()
const body = ref<HTMLElement>()
const columns = ref(1)
// What the popover takes below its button besides the content: the gap to the button and the
// margin to the window edge (12), its padding and border (14) and the menu's own padding (10).
const BELOW = 36

function fit() {
  const el = body.value
  if (!el || props.plain) return
  const lay = (count: number, split: boolean) => {
    el.style.setProperty('--cols', String(count))
    el.toggleAttribute('data-split', split)
    return el.getBoundingClientRect().height
  }
  let chosen = { columns: 1, split: false }
  if (!props.sheet && props.anchor) chosen = fitMenu(lay, innerHeight - props.anchor.getBoundingClientRect().bottom - BELOW, maxMenuColumns(innerWidth))
  lay(chosen.columns, chosen.split)
  columns.value = chosen.columns
  emit('columns', chosen.columns)
}
let frame = 0
const resized = () => { cancelAnimationFrame(frame); frame = requestAnimationFrame(() => { frame = 0; fit() }) }
// Before the panel is placed, so its width already follows the count.
onMounted(() => { fit(); window.addEventListener('resize', resized) })
onBeforeUnmount(() => { cancelAnimationFrame(frame); window.removeEventListener('resize', resized) })
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
/* Blocks flow down a column and on to the next; none is cut in two. */
.menu-body { column-count: var(--cols, 1); column-gap: var(--col-gap); column-fill: balance; width: calc(var(--cols, 1) * var(--col-w) + (var(--cols, 1) - 1) * var(--col-gap)); }
.menu-body > :deep(*), .menu-body :deep(.display-panel > *) { break-inside: avoid; margin: 0 0 16px; }
/* In a window too short for whole blocks, the column list may break between its rows. */
.menu-body[data-split] :deep(.columns) { break-inside: auto; }
.menu-body[data-split] :deep(.columns .row), .menu-body[data-split] :deep(.columns .head), .menu-body[data-split] :deep(.columns .fine) { break-inside: avoid; }
.menu-body :deep(.display-panel) { display: contents; }
.menu-body :deep(.display-panel > .section) { margin-top: 0; padding-top: 0; border-top: 0; }
.menu-body > :deep(*:last-child), .menu-body :deep(.display-panel > :last-child) { margin-bottom: 0; }

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
