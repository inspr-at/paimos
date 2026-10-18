<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onUpdated, ref } from 'vue'
import { SORT_FIELDS, SORT_LABELS, type SortField, type SortKey } from '../../lib/work'
import AppIcon from '../AppIcon.vue'

// Multi-key sort by keyboard and pointer: the same keys as Shift-clicking column
// headers. The first key decides, the next ones break ties.
const props = defineProps<{ sort: SortKey[]; stable?: boolean }>()
const emit = defineEmits<{ change: [keys: SortKey[]] }>()
const free = computed(() => SORT_FIELDS.filter(field => field !== 'paid' && !props.sort.some(key => key.field === field)))
const FIRST_DIRECTION: Partial<Record<SortField, boolean>> = { updated_at: true, created_at: true, priority: false }
function set(index: number, patch: Partial<SortKey>) { emit('change', props.sort.map((key, i) => i === index ? { ...key, ...patch } : key)) }
function remove(index: number) { emit('change', props.sort.filter((_, i) => i !== index)) }
// The URL answers a change a moment later; focus goes to the key once it is drawn.
let focusRow: number | null = null
onUpdated(() => {
  if (focusRow === null) return
  const select = root.value?.querySelector<HTMLElement>(`[data-sort-row="${focusRow}"] select`)
  if (select) { select.focus(); focusRow = null }
})
function add() {
  const field = free.value[0]
  if (!field) return
  focusRow = props.sort.length
  emit('change', [...props.sort, { field, desc: FIRST_DIRECTION[field] ?? false }])
}
function move(index: number, step: -1 | 1) {
  const to = index + step
  if (to < 0 || to >= props.sort.length) return
  const next = [...props.sort]
  ;[next[index], next[to]] = [next[to], next[index]]
  focusRow = to
  emit('change', next)
}
const root = ref<HTMLElement>()
function keydown(event: KeyboardEvent, index: number) {
  if (event.altKey && (event.key === 'ArrowUp' || event.key === 'ArrowDown')) { event.preventDefault(); move(index, event.key === 'ArrowUp' ? -1 : 1) }
}
</script>

<template>
  <div ref="root" class="sort-editor" :class="{ stable }">
    <div class="head">
      <p class="eyebrow">Sort</p>
      <button v-if="sort.length" type="button" class="reset" data-tip="Newest updated first" @click="emit('change', [])">Default</button>
    </div>
    <ol v-if="sort.length" class="keys" aria-label="Sort keys">
      <li v-for="(key, index) in sort" :key="key.field" class="key-row" :data-sort-row="index">
        <span class="rank mono" aria-hidden="true">{{ index + 1 }}</span>
        <select
          class="field sort-field" :value="key.field" :aria-label="`Sort key ${index + 1}`" aria-describedby="sort-move-hint"
          @change="set(index, { field: ($event.target as HTMLSelectElement).value as SortField })" @keydown="keydown($event, index)"
        >
          <option v-for="field in [key.field, ...free]" :key="field" :value="field">{{ SORT_LABELS[field] }}</option>
        </select>
        <button
          type="button" class="icon-btn sm flat dir" :aria-label="`${SORT_LABELS[key.field]}: ${key.desc ? 'descending' : 'ascending'}. Reverse`"
          :data-tip="key.desc ? 'Descending' : 'Ascending'" @click="set(index, { desc: !key.desc })"
        ><AppIcon :name="key.desc ? 'arrow-down' : 'arrow-up'" :size="13" /></button>
        <button type="button" class="icon-btn sm flat" :aria-label="`Remove ${SORT_LABELS[key.field]} from the sort`" data-tip="Remove" @click="remove(index)"><AppIcon name="close" :size="12" /></button>
      </li>
    </ol>
    <p v-else class="default">Newest updated first</p>
    <p id="sort-move-hint" class="sr-only">Alt and the arrow keys move the sort key.</p>
    <button v-if="free.length" type="button" class="add" @click="add"><AppIcon name="plus" :size="13" />{{ stable ? 'Add sort key' : sort.length ? 'Then by' : 'Sort by' }}</button>
  </div>
</template>

<style scoped>
.sort-editor { display: grid; gap: 6px; }
.head { display: flex; align-items: center; justify-content: space-between; }
.head .eyebrow { margin: 0; }
.reset { height: 24px; padding: 0 8px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; }
.reset:hover { background: var(--row-hover); }
.reset:focus-visible { box-shadow: var(--focus-ring); }
.keys { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.key-row { display: flex; align-items: center; gap: 4px; }
.rank { width: 16px; font-size: 11px; color: var(--ink-3); text-align: center; }
.sort-field { flex: 1; min-width: 0; height: 30px; padding: 0 8px; font-size: 13px; }
.icon-btn { width: 28px; height: 28px; color: var(--ink-2); }
.dir { color: var(--teal-ink); }
.default { padding: 2px 2px 0 20px; font-size: 12.5px; color: var(--ink-3); }
.add { display: inline-flex; align-items: center; gap: 6px; justify-self: start; height: 28px; margin-left: 14px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; color: var(--teal-ink); font-size: 12.5px; font-weight: 600; }
.add:hover { background: var(--row-hover); }
.add:focus-visible { box-shadow: var(--focus-ring); }
/* Actions precede the variable key list, so adding keys never moves Add.
   The head stays as tall as Default (24px): that action appears with the first key. */
.stable .head { order: -2; min-height: 24px; }
.stable .add { order: -1; }
</style>
