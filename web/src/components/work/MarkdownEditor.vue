<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import MarkdownBody from '../MarkdownBody.vue'

// A plain Markdown textarea with a live preview toggle. Cmd/Ctrl+Enter saves,
// Escape cancels (asking first when there are changes).
// bare: part of a larger form (no own Save/Cancel; an empty value may be saved).
// split: write and preview side by side (the full-page writing layout).
const props = withDefaults(defineProps<{ modelValue: string; label: string; saving?: boolean; saveLabel?: string; placeholder?: string; minRows?: number; compact?: boolean; bare?: boolean; split?: boolean; attachmentId?: (file: File) => Promise<string | null> }>(), { saveLabel: 'Save', placeholder: 'Write in Markdown…', minRows: 5 })
const emit = defineEmits<{ 'update:modelValue': [value: string]; save: []; cancel: [] }>()
const area = ref<HTMLTextAreaElement>()
const mirror = ref<HTMLTextAreaElement>()
const preview = ref(false)
const empty = computed(() => !props.modelValue.trim())

// The box fits its text without ever collapsing (AEON-1532): a hidden twin of
// the same width and type measures the text, so the live box changes height
// once per edit, straight to its new size. Split: the box sets the row's
// minimum and stretches to the preview, so both columns share one height and
// the page scrolls, never the box. Otherwise the box stops at 560 px (or the
// height it was dragged to) and scrolls inside.
const CAP = 560
interface Change { height: number; floor: number; longer: boolean }
// The page's scroller (the app's main column), or null when nothing scrolls.
function scroller(el: HTMLElement) {
  for (let node = el.parentElement; node; node = node.parentElement) {
    const overflow = getComputedStyle(node).overflowY
    if ((overflow === 'auto' || overflow === 'scroll') && node.scrollHeight > node.clientHeight) return node
  }
  const root = document.scrollingElement
  return root && root.scrollHeight > root.clientHeight ? root : null
}
// Before the text changes: while text is added nothing shrinks; when text is
// removed, the box shrinks only as far as its scroller can absorb without
// moving the page (the rest stays as space below the text).
function hold(longer: boolean): Change | undefined {
  const el = area.value
  if (!el?.offsetWidth) return undefined
  const box = scroller(el)
  const room = box && box.scrollTop >= 1 ? Math.max(0, Math.floor(box.scrollHeight - box.clientHeight - box.scrollTop)) : Infinity
  return { height: el.offsetHeight, floor: el.offsetHeight - room, longer }
}
function fit(change?: Change) {
  const el = area.value, twin = mirror.value
  if (!el || !twin || !el.offsetWidth) return
  twin.value = el.value || el.placeholder
  // + 1: scrollHeight is rounded, and the box must never be a fraction short.
  let height = Math.max(twin.scrollHeight + twin.offsetHeight - twin.clientHeight + 1, props.minRows * 21 + 18)
  if (change) height = Math.max(height, change.longer ? change.height : change.floor)
  if (props.split) { el.style.height = ''; el.style.setProperty('--md-fit', `${height}px`) }
  else { el.style.removeProperty('--md-fit'); el.style.height = `${Math.min(height, Math.max(CAP, change?.height ?? 0))}px` }
}
function input(event: Event) {
  const value = (event.target as HTMLTextAreaElement).value
  const change = hold(value.length >= props.modelValue.length)
  emit('update:modelValue', value)
  fit(change)
}
// Text set from outside (insert, upload, reset) fits once the box shows it.
let pending: Change | undefined
watch(() => props.modelValue, (value, old) => { if (area.value && area.value.value !== value) pending = hold(value.length >= old.length) }, { flush: 'pre' })
watch(() => props.modelValue, () => { if (pending) { fit(pending); pending = undefined } }, { flush: 'post' })
watch([() => props.split, () => props.minRows], () => fit(), { flush: 'post' })
function keydown(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); if (!empty.value || props.bare) emit('save') }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); emit('cancel') }
}
async function showWrite() { preview.value = false; await nextTick(); fit(); area.value?.focus() }
function focus() { area.value?.focus(); const length = area.value?.value.length ?? 0; area.value?.setSelectionRange(length, length) }
// Insert text at the caret (or replace the selection), as typing would.
function insert(text: string) {
  const el = area.value
  if (!el) { emit('update:modelValue', props.modelValue + text); return }
  const start = el.selectionStart, end = el.selectionEnd
  const next = props.modelValue.slice(0, start) + text + props.modelValue.slice(end)
  emit('update:modelValue', next)
  void nextTick(() => { el.focus(); el.setSelectionRange(start + text.length, start + text.length) })
}
// Pasted or dropped images become attachments referenced from the text.
async function addFiles(files: File[]) {
  if (!props.attachmentId || !files.length) return
  for (const file of files) {
    const placeholder = `![Uploading ${file.name || 'image'}…]()`
    insert(placeholder)
    const id = await props.attachmentId(file)
    const text = id ? `![${(file.name || 'Screenshot').replace(/[[\]]/g, '')}](attachment:${id})` : ''
    emit('update:modelValue', props.modelValue.replace(placeholder, text))
  }
}
function paste(event: ClipboardEvent) {
  const files = [...(event.clipboardData?.files ?? [])]
  if (!files.length || !props.attachmentId) return
  event.preventDefault(); event.stopPropagation()
  void addFiles(files)
}
function drop(event: DragEvent) {
  const files = [...(event.dataTransfer?.files ?? [])]
  if (!files.length || !props.attachmentId) return
  event.preventDefault(); event.stopPropagation()
  void addFiles(files)
}
// A new width (a window resize, or the box's own scrollbar coming or going
// after an edit) or a late font rewraps the text: fit again, shrinking only by
// the slack below, as a delete does, so the scroller never clamps.
let width = 0
const resize = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(([entry]) => {
  if (entry && entry.contentRect.width !== width) { width = entry.contentRect.width; fit(hold(false)) }
})
const refit = () => fit(hold(false))
onMounted(() => {
  fit()
  if (area.value) resize?.observe(area.value)
  document.fonts?.addEventListener('loadingdone', refit)
})
onBeforeUnmount(() => { resize?.disconnect(); document.fonts?.removeEventListener('loadingdone', refit) })
defineExpose({ focus, insert })
</script>

<template>
  <div class="md-editor" :class="{ compact, split }" @keydown="keydown">
    <div v-if="!split" class="editor-head">
      <div class="seg" role="radiogroup" :aria-label="`${label} mode`">
        <button type="button" role="radio" :aria-checked="!preview" @click="showWrite">Write</button>
        <button type="button" role="radio" :aria-checked="preview" @click="preview = true">Preview</button>
      </div>
      <span class="md-hint"><AppIcon name="edit" :size="12" />Markdown</span>
    </div>
    <div v-show="!preview || split" class="md-write">
      <textarea
        ref="area" class="md-area" :value="modelValue" :aria-label="`${label}, Markdown`" :placeholder="placeholder" :disabled="saving" spellcheck="true" @input="input" @paste="paste" @drop="drop"
      />
      <textarea ref="mirror" class="md-mirror" aria-hidden="true" tabindex="-1" readonly spellcheck="false" />
    </div>
    <div v-if="preview || split" class="md-preview" tabindex="0" :aria-label="`${label} preview`">
      <MarkdownBody v-if="!empty" :body="modelValue" />
      <p v-else class="nothing">Nothing to preview yet.</p>
    </div>
    <div v-if="!bare" class="editor-foot">
      <span class="keys"><KeyCap k="mod" /><KeyCap k="enter" /> save · <kbd class="keycap">esc</kbd> cancel</span>
      <span class="spacer" />
      <button type="button" class="btn sm" :disabled="saving" @click="emit('cancel')">Cancel</button>
      <button type="button" class="btn sm on" :disabled="saving || empty" @click="emit('save')">{{ saving ? 'Saving…' : saveLabel }}</button>
    </div>
  </div>
</template>

<style scoped>
.md-editor { display: grid; gap: 8px; }
.editor-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.editor-head .seg button { height: 24px; padding: 0 10px; font-size: 12px; }
.md-hint { display: inline-flex; align-items: center; gap: 5px; font-size: 11.5px; color: var(--ink-3); }
.md-write { position: relative; display: grid; min-width: 0; }
/* The twin shares everything that wraps text, so it measures what the box shows. */
.md-area, .md-mirror {
  width: 100%; padding: 10px 12px; border: 1px solid var(--glass-edge); border-radius: var(--radius-s);
  font: 13px/1.6 var(--mono); font-variant-ligatures: none; font-feature-settings: "liga" 0, "calt" 0;
}
.md-area { min-height: 90px; resize: vertical; background: var(--field-bg); color: var(--ink); box-shadow: var(--field-inset), 0 0 0 1px var(--line); }
.md-mirror { position: absolute; top: 0; left: 0; height: 0; min-height: 0; overflow: hidden; visibility: hidden; pointer-events: none; resize: none; }
.md-area:focus { box-shadow: var(--focus-ring); }
.md-preview { min-height: 90px; padding: 10px 12px; border-radius: var(--radius-s); background: var(--surface-sunken); box-shadow: inset 0 0 0 1px var(--line); }
.nothing { font-size: 13px; color: var(--ink-3); }
.editor-foot { display: flex; align-items: center; gap: 8px; }
.keys { display: inline-flex; align-items: center; gap: 3px; font-size: 11.5px; color: var(--ink-3); }
.spacer { flex: 1; }
.compact .md-area { min-height: 64px; }
/* Writing layout: the source left, the rendered text right, one row as tall as
   the longer of the two (at least --md-min), so the page scrolls them as one. */
.split { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 16px; align-items: stretch; }
.split .md-area { min-height: max(var(--md-min, 220px), var(--md-fit, 0px)); overflow: hidden; resize: none; }
/* Typing changes the preview: it never anchors the page's scroll position. */
.split .md-preview { min-height: var(--md-min, 220px); max-height: none; overflow: auto; overflow-anchor: none; background: transparent; box-shadow: inset 0 0 0 1px var(--line); }
@media (hover: none) { .keys { display: none; } }
</style>
