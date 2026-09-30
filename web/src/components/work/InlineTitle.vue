<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import type { SaveResult } from '../../lib/useTicket'
import AppIcon from '../AppIcon.vue'

// The ticket title: click or press e to edit; Enter saves, Escape cancels.
// A conflict keeps the draft and shows the title someone else saved.
const props = defineProps<{ value: string; editable: boolean; save: (value: string) => Promise<SaveResult>; large?: boolean }>()
const editing = ref(false)
const draft = ref('')
const saving = ref(false)
const conflict = ref(false)
const area = ref<HTMLTextAreaElement>()
const dirty = computed(() => editing.value && draft.value.trim() !== props.value)

function grow() { const el = area.value; if (el) { el.style.height = 'auto'; el.style.height = `${el.scrollHeight}px` } }
async function start() {
  if (!props.editable || editing.value) return
  draft.value = props.value; conflict.value = false; editing.value = true
  await nextTick(); grow(); area.value?.focus(); area.value?.select()
}
async function commit() {
  const title = draft.value.replace(/\s+/g, ' ').trim()
  if (!title) { area.value?.focus(); return }
  if (title === props.value && !conflict.value) { editing.value = false; return }
  saving.value = true
  const result = await props.save(title)
  saving.value = false
  if (result === 'ok') { editing.value = false; conflict.value = false }
  else if (result === 'conflict') { conflict.value = true; await nextTick(); area.value?.focus() }
}
function cancel() { editing.value = false; conflict.value = false }
function keydown(event: KeyboardEvent) {
  if (event.key === 'Enter') { event.preventDefault(); void commit() }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); cancel() }
}
function blur() { if (!saving.value && !conflict.value) { if (dirty.value) void commit(); else editing.value = false } }
defineExpose({ start, isDirty: () => dirty.value, editing })
</script>

<template>
  <div class="inline-title" :class="{ large }">
    <template v-if="editing">
      <textarea ref="area" v-model="draft" class="title-input" rows="1" aria-label="Title" :disabled="saving" @input="grow" @keydown="keydown" @blur="blur" />
      <div v-if="conflict" class="conflict" role="alert">
        <AppIcon name="alert" :size="14" />
        <p>Changed elsewhere to <strong>“{{ value }}”</strong>. <span class="keys"><kbd class="keycap"><AppIcon name="enter" /></kbd> saves yours · <kbd class="keycap">esc</kbd> keeps theirs</span></p>
      </div>
      <p v-else class="hint"><kbd class="keycap"><AppIcon name="enter" /></kbd> save · <kbd class="keycap">esc</kbd> cancel</p>
    </template>
    <h2 v-else class="title-text" :class="{ editable }" :tabindex="editable ? 0 : undefined" :data-tip="editable ? 'Click or press e to edit' : undefined" @click="start" @keydown.enter.prevent="start">{{ value }}</h2>
  </div>
</template>

<style scoped>
.title-text, .title-input { margin: 0; font: 500 21px/1.3 var(--serif); letter-spacing: -.012em; color: var(--ink); overflow-wrap: anywhere; }
.large .title-text, .large .title-input { font-size: 28px; font-weight: 400; line-height: 1.2; letter-spacing: -.02em; }
.title-text.editable { cursor: text; border-radius: 8px; margin: -3px -8px; padding: 3px 8px; }
@media (hover: hover) { .title-text.editable:hover { background: var(--row-hover); } }
.title-text:focus-visible { box-shadow: var(--focus-ring); }
@media (max-width: 720px) {
  .title-text, .title-input { font-size: 19px; line-height: 1.28; }
  .large .title-text, .large .title-input { font-size: 23px; }
}
.title-input { display: block; width: calc(100% + 16px); margin: -5px -8px; padding: 4px 7px; border: 1px solid var(--glass-edge); border-radius: 10px; resize: none; overflow: hidden; background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); }
.title-input:focus { box-shadow: var(--focus-ring); }
.hint { display: flex; align-items: center; flex-wrap: wrap; gap: 4px; margin-top: 10px; font-size: 12px; color: var(--ink-3); }
.conflict { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: 8px; margin-top: 12px; padding: 9px 12px; border-radius: 10px; background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.conflict > svg { margin-top: 2px; color: var(--danger); }
.conflict p { font-size: 12.5px; line-height: 1.5; color: var(--ink); }
.conflict strong { font-weight: 600; }
.conflict .keys { display: inline-flex; align-items: center; gap: 3px; color: var(--ink-3); white-space: nowrap; }
</style>
