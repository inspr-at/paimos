<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts" generic="T extends string = ColumnId">
import { computed, nextTick, ref, useId } from 'vue'
import { COLUMN_BY_ID, PINNED, type ColumnId } from '../../lib/columns'
import AppIcon from '../AppIcon.vue'

// Show, hide and reorder list columns: drag a row, or Alt+Up/Down on it. The
// pinned columns always lead. Reset goes back to the list's own choice
// ("Automatic" columns that follow the width, for tickets). Other lists (the
// Projects list) pass their own labels, pinned columns and wording.
const props = withDefaults(defineProps<{
  order: T[]; visible: T[]; customised: boolean
  notes?: Partial<Record<string, string>>
  labels?: Partial<Record<string, string>>; pinned?: T[]; resetLabel?: string; resetTip?: string; note?: string | null
}>(), { labels: undefined, pinned: undefined, resetLabel: 'Automatic', resetTip: 'Columns follow the width again', note: 'Drag a header edge to resize a column; double-click it to fit.' })
const emit = defineEmits<{ change: [order: T[], visible: T[]]; reset: [] }>()
const pins = computed<T[]>(() => props.pinned ?? (PINNED as unknown as T[]))
const label = (id: T) => props.labels?.[id] ?? COLUMN_BY_ID.get(id as unknown as ColumnId)?.label ?? id
const free = computed(() => props.order.filter(id => !pins.value.includes(id)))
const shown = computed(() => new Set(props.visible))
// Plain strings: a generic ref does not unwrap cleanly in the template.
const dragging = ref<string | null>(null)
const over = ref<string | null>(null)
const hint = `move-hint-${useId()}`

function toggle(id: T) {
  const next = shown.value.has(id) ? props.visible.filter(x => x !== id) : [...props.visible, id]
  emit('change', props.order, next.filter(x => !pins.value.includes(x)))
}
function step(id: T, delta: -1 | 1) {
  const list = [...free.value]
  const index = list.indexOf(id), to = index + delta
  if (index === -1 || to < 0 || to >= list.length) return
  ;[list[index], list[to]] = [list[to]!, list[index]!]
  emit('change', [...pins.value, ...list], props.visible.filter(x => !pins.value.includes(x)))
  // Moving the row in the DOM drops focus; put it back as soon as the list re-renders.
  void nextTick(() => document.querySelector<HTMLElement>(`[data-column-row="${id}"]`)?.focus())
}
function keydown(event: KeyboardEvent, id: T) {
  if (event.altKey && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) { event.preventDefault(); step(id, event.key === 'ArrowUp' ? -1 : 1) }
}
function drop(target: T) {
  const from = dragging.value as T | null
  dragging.value = null; over.value = null
  if (!from || from === target) return
  const list = free.value.filter(x => x !== from)
  list.splice(list.indexOf(target), 0, from)
  emit('change', [...pins.value, ...list], props.visible.filter(x => !pins.value.includes(x)))
}
</script>

<template>
  <div class="columns">
    <div class="head">
      <p class="eyebrow">Columns</p>
      <button v-if="customised" type="button" class="reset" :data-tip="resetTip" @click="emit('reset')">{{ resetLabel }}</button>
      <span v-else class="auto-note">{{ resetLabel }}</span>
    </div>
    <ul class="list" aria-label="Columns">
      <li v-for="id in pins" :key="id" class="row pinned">
        <input type="checkbox" class="check" checked disabled :aria-label="`${label(id)} (always shown)`" />
        <span class="name">{{ label(id) }}</span>
        <span class="pin-note">Always</span>
      </li>
      <li
        v-for="(id, index) in free" :key="id" class="row" :class="{ off: !shown.has(id), dragging: dragging === id, over: over === id, noted: !!notes?.[id] }" draggable="true"
        @dragstart="dragging = id" @dragend="dragging = null; over = null" @dragover.prevent="over = id" @dragleave="over = over === id ? null : over" @drop.prevent="drop(id)"
      >
        <label class="row-label">
          <input
            type="checkbox" class="check" :checked="shown.has(id)" :data-column-row="id" :aria-describedby="notes?.[id] ? `${hint} ${hint}-${id}` : hint"
            @change="toggle(id)" @keydown="keydown($event, id)"
          />
          <span class="name">{{ label(id) }}</span>
        </label>
        <span class="moves">
          <button type="button" class="icon-btn sm flat" :aria-label="`Move ${label(id)} up`" :disabled="index === 0" tabindex="-1" @click="step(id, -1)"><AppIcon name="chevron-up" :size="13" /></button>
          <button type="button" class="icon-btn sm flat" :aria-label="`Move ${label(id)} down`" :disabled="index === free.length - 1" tabindex="-1" @click="step(id, 1)"><AppIcon name="chevron" :size="13" /></button>
        </span>
        <span class="grip" aria-hidden="true" />
        <p v-if="notes?.[id]" :id="`${hint}-${id}`" class="col-note">{{ notes[id] }}</p>
      </li>
    </ul>
    <p :id="hint" class="sr-only">Alt and the arrow keys move the column.</p>
    <p v-if="note" class="fine">{{ note }}</p>
  </div>
</template>

<style scoped>
.columns { display: grid; gap: 6px; }
.head { display: flex; align-items: center; justify-content: space-between; }
.head .eyebrow { margin: 0; }
.reset { height: 24px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; }
.reset:hover { background: var(--row-hover); }
.reset:focus-visible { box-shadow: var(--focus-ring); }
.auto-note { font-size: 11.5px; color: var(--ink-3); }
.list { margin: 0; padding: 0; list-style: none; display: grid; gap: 1px; }
.row { display: flex; align-items: center; gap: 6px; height: 32px; padding: 0 4px 0 8px; border-radius: 8px; font-size: 13px; color: var(--ink); user-select: none; }
.row:hover { background: var(--row-hover); }
.row:focus-within { background: var(--row-selected); }
.row.off .name { color: var(--ink-2); }
.row.dragging { opacity: .45; }
/* Where a dragged column lands: a caret in the gap above the row. */
.row { position: relative; }
.row.over::before { content: ''; position: absolute; left: 8px; right: 8px; top: -2px; height: 2px; border-radius: 2px; background: var(--teal); pointer-events: none; }
.row.pinned { gap: 9px; color: var(--ink-2); }
.row.pinned:hover { background: transparent; }
.row-label { display: flex; align-items: center; gap: 9px; flex: 1; min-width: 0; height: 100%; cursor: pointer; }
.check { width: 16px; height: 16px; margin: 0; accent-color: var(--teal); }
.check:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
.name { flex: 1; min-width: 0; }
.pin-note { font-size: 11.5px; color: var(--ink-3); padding-right: 8px; }
.moves { display: inline-flex; opacity: 0; }
.row:hover .moves, .row:focus-within .moves { opacity: 1; }
.moves .icon-btn { width: 24px; height: 24px; }
.grip { flex-shrink: 0; width: 10px; height: 14px; margin: 0 4px 0 2px; cursor: grab; background: radial-gradient(circle, var(--ink-3) 1.2px, transparent 1.5px) 0 0 / 5px 5px; }
.fine { margin-top: 2px; font-size: 11.5px; color: var(--ink-3); }
.row.noted { flex-wrap: wrap; height: auto; min-height: 32px; padding-top: 6px; padding-bottom: 6px; }
.row.noted .row-label { height: 20px; }
.col-note { flex-basis: 100%; margin: 1px 0 0 25px; font-size: 11.5px; line-height: 1.35; color: var(--ink-3); }
</style>
