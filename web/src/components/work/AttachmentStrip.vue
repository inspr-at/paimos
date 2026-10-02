<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { nextTick, reactive, ref } from 'vue'
import { contentUrl, fileKind, fileSize, hasThumbnail, isImage, type Attachment } from '../../lib/attachments'
import type { Upload } from '../../lib/useAttachments'
import AppIcon from '../AppIcon.vue'

// Attachments as a thumbnail strip under the properties, or as a gallery in the
// context column. Images open the viewer, other files are cards; drag to reorder
// (or Alt+arrows), captions in the gallery, delete with an undo toast.
const MIME = 'application/x-aeon-attachment'
const props = defineProps<{ items: Attachment[]; uploads: Upload[]; layout: 'strip' | 'gallery'; canWrite: boolean; loading: boolean; error: string }>()
const emit = defineEmits<{
  open: [id: string]; add: [files: File[]]; remove: [item: Attachment]; reorder: [id: string, to: number]
  caption: [item: Attachment, caption: string]; retry: [key: string]; cancel: [key: string]; reload: []
}>()
const picker = ref<HTMLInputElement>()
const dragging = ref<string | null>(null)
const over = ref<number | null>(null)
const editing = ref<string | null>(null)
const draft = ref('')
// Images whose picture could not be read (bytes not stored yet, or gone) show as a
// file card that says so, not as a broken image.
const unreadable = reactive(new Set<string>())

function pick() { picker.value?.click() }
function picked(event: Event) {
  const input = event.target as HTMLInputElement
  const files = [...(input.files ?? [])]
  input.value = ''
  if (files.length) emit('add', files)
}
function dragStart(event: DragEvent, item: Attachment) {
  if (!props.canWrite) return
  dragging.value = item.id
  event.dataTransfer?.setData(MIME, item.id)
  if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
}
function dragOver(event: DragEvent, index: number) {
  if (!dragging.value || !event.dataTransfer?.types.includes(MIME)) return
  event.preventDefault(); event.stopPropagation()
  const box = (event.currentTarget as HTMLElement).getBoundingClientRect()
  const after = props.layout === 'strip' ? event.clientX > box.left + box.width / 2 : event.clientY > box.top + box.height / 2 || event.clientX > box.left + box.width / 2
  over.value = index + (after ? 1 : 0)
}
function drop(event: DragEvent) {
  if (!dragging.value || over.value === null) return
  event.preventDefault(); event.stopPropagation()
  const from = props.items.findIndex(item => item.id === dragging.value)
  const to = over.value > from ? over.value - 1 : over.value
  emit('reorder', dragging.value, to)
  dragEnd()
}
function dragEnd() { dragging.value = null; over.value = null }
function keydown(event: KeyboardEvent, item: Attachment, index: number) {
  if (!props.canWrite) return
  const back = event.key === 'ArrowLeft' || event.key === 'ArrowUp', forward = event.key === 'ArrowRight' || event.key === 'ArrowDown'
  if (event.altKey && (back || forward)) {
    event.preventDefault()
    emit('reorder', item.id, index + (forward ? 1 : -1))
    void nextTick(() => document.querySelector<HTMLElement>(`[data-attachment-id="${item.id}"]`)?.focus())
  } else if (event.key === 'Delete' || event.key === 'Backspace') { event.preventDefault(); emit('remove', item) }
}
function startCaption(item: Attachment) { if (!props.canWrite) return; editing.value = item.id; draft.value = item.caption; requestAnimationFrame(() => document.querySelector<HTMLInputElement>(`[data-caption-for="${item.id}"]`)?.focus()) }
function saveCaption(item: Attachment) { if (editing.value !== item.id) return; editing.value = null; if (draft.value.trim() !== item.caption) emit('caption', item, draft.value.trim()) }
</script>

<template>
  <section class="attachments" :class="layout" aria-labelledby="attachments-title" @dragend="dragEnd">
    <header class="head">
      <h3 id="attachments-title" class="eyebrow">Attachments<span v-if="items.length + uploads.length" class="count mono">{{ items.length + uploads.length }}</span></h3>
      <button v-if="canWrite" type="button" class="add-btn" data-tip="Add files · or drop them, or paste a screenshot" @click="pick"><AppIcon name="paperclip" :size="13" />Add</button>
      <input ref="picker" type="file" multiple class="sr-only" tabindex="-1" aria-hidden="true" @change="picked" />
    </header>

    <p v-if="error" class="note error" role="alert"><AppIcon name="alert" :size="13" />{{ error }} <button type="button" class="link" @click="emit('reload')">Try again</button></p>
    <div v-else-if="loading && !items.length" class="list" aria-hidden="true"><span v-for="i in 3" :key="i" class="skeleton sk-thumb" /></div>
    <button v-else-if="!items.length && !uploads.length && canWrite" type="button" class="empty" @click="pick">
      <AppIcon name="image" :size="16" /><span>Drop screen designs or files here, or paste a screenshot</span>
    </button>
    <p v-else-if="!items.length && !uploads.length" class="note">No attachments.</p>

    <ol v-if="items.length || uploads.length" class="list" aria-label="Attachments">
      <li
        v-for="(item, index) in items" :key="item.id" class="item" :class="{ dragging: dragging === item.id, 'drop-before': over === index && dragging !== item.id, 'drop-after': over === index + 1 && index === items.length - 1 }"
        :draggable="canWrite ? 'true' : undefined" @dragstart="dragStart($event, item)" @dragover="dragOver($event, index)" @drop="drop"
      >
        <button
          type="button" class="tile" :class="{ file: !isImage(item) || unreadable.has(item.id) }" :data-attachment-id="item.id"
          :aria-label="`Open ${item.caption || item.name}${canWrite ? '. Alt and arrows move it, Delete removes it' : ''}`" @click="emit('open', item.id)" @keydown="keydown($event, item, index)"
        >
          <img v-if="hasThumbnail(item) && !unreadable.has(item.id)" :src="contentUrl(item.id, 'thumb')" :alt="item.caption || item.name" loading="lazy" decoding="async" draggable="false" @error="unreadable.add(item.id)" />
          <span v-else class="file-card"><span class="badge">{{ fileKind(item) }}</span><span class="file-name">{{ item.name }}</span><span class="file-size">{{ unreadable.has(item.id) ? 'No preview' : fileSize(item.size) }}</span></span>
        </button>
        <button v-if="canWrite" type="button" class="remove" :aria-label="`Delete ${item.name}`" data-tip="Delete · you can undo" @click="emit('remove', item)"><AppIcon name="close" :size="11" /></button>
        <template v-if="layout === 'gallery'">
          <input
            v-if="editing === item.id" v-model="draft" class="caption-input" :data-caption-for="item.id" aria-label="Caption" maxlength="300"
            @keydown.enter.prevent="saveCaption(item)" @keydown.esc.prevent.stop="editing = null" @blur="saveCaption(item)"
          />
          <button v-else type="button" class="caption" :class="{ unset: !item.caption }" :disabled="!canWrite && !item.caption" @click="startCaption(item)">{{ item.caption || (canWrite ? 'Add a caption' : item.name) }}</button>
        </template>
        <span v-else-if="item.caption" class="strip-caption">{{ item.caption }}</span>
      </li>
      <li v-for="upload in uploads" :key="upload.key" class="item uploading" :class="{ failed: upload.error }">
        <div class="tile" role="status" :aria-label="upload.error ? `${upload.name} did not upload: ${upload.error}` : `Uploading ${upload.name}, ${Math.round(upload.progress * 100)}%`">
          <img v-if="upload.preview" :src="upload.preview" alt="" />
          <span v-else class="file-card"><span class="badge">{{ upload.name.split('.').pop()?.toUpperCase().slice(0, 5) || 'FILE' }}</span><span class="file-name">{{ upload.name }}</span></span>
          <span class="progress" :style="{ '--p': upload.progress }"><i /></span>
          <span v-if="upload.error" class="failed-veil"><AppIcon name="alert" :size="14" /></span>
        </div>
        <span v-if="upload.error" class="upload-actions">
          <button type="button" class="link" @click="emit('retry', upload.key)">Retry</button>
          <button type="button" class="link" @click="emit('cancel', upload.key)">Remove</button>
        </span>
        <button v-else type="button" class="remove" :aria-label="`Cancel uploading ${upload.name}`" @click="emit('cancel', upload.key)"><AppIcon name="close" :size="11" /></button>
      </li>
    </ol>
  </section>
</template>

<style scoped>
.attachments { display: grid; gap: 8px; min-width: 0; }
.head { display: flex; align-items: center; gap: 8px; }
.head .eyebrow { display: inline-flex; align-items: center; gap: 8px; margin: 0; }
.count { display: inline-grid; place-items: center; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 10.5px; letter-spacing: 0; color: var(--ink-2); }
.add-btn { display: inline-flex; align-items: center; gap: 5px; height: 26px; margin-left: auto; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; box-shadow: inset 0 0 0 1px var(--line); color: var(--ink-2); font-size: 12px; font-weight: 600; }
.add-btn:hover { color: var(--teal-ink); box-shadow: inset 0 0 0 1px var(--glass-rim); background: var(--row-hover); }
.add-btn:focus-visible { box-shadow: var(--focus-ring); }
@media (max-width: 600px) { .add-btn { height: 44px; } }
.note { font-size: 12.5px; color: var(--ink-3); }
.note.error { display: flex; align-items: center; gap: 6px; color: var(--danger); }
.link { padding: 0; border: 0; background: none; color: var(--teal-ink); font-size: 12px; font-weight: 600; cursor: pointer; }
.link:hover { text-decoration: underline; }
.empty { display: flex; align-items: center; justify-content: center; gap: 8px; min-height: 64px; padding: 12px; border: 1.5px dashed var(--line-2); border-radius: 12px; background: transparent; color: var(--ink-3); font-size: 12.5px; }
.empty:hover { color: var(--teal-ink); border-color: var(--chip-teal-line); background: var(--row-hover); }
.empty:focus-visible { box-shadow: var(--focus-ring); }
.list { display: flex; gap: 10px; margin: 0; padding: 2px 2px 6px; list-style: none; overflow-x: auto; scrollbar-width: thin; }
.strip .list { min-height: 118px; }
.item { position: relative; flex-shrink: 0; display: grid; gap: 5px; }
.tile {
  position: relative; display: grid; place-items: center; width: 132px; height: 88px; padding: 0; border: 0; border-radius: 10px; overflow: hidden; cursor: zoom-in;
  background: var(--surface-sunken, var(--code-bg)); box-shadow: inset 0 0 0 1px var(--line), 0 1px 2px rgba(16, 35, 39, .08);
}
.tile.file { cursor: pointer; }
.tile img { width: 100%; height: 100%; object-fit: cover; object-position: top center; }
.tile:hover { box-shadow: inset 0 0 0 1px var(--chip-teal-line), 0 6px 16px -10px rgba(16, 35, 39, .45); }
.tile:focus-visible { box-shadow: var(--focus-ring); }
.item.dragging { opacity: .4; }
.item.drop-before::before, .item.drop-after::after { content: ''; position: absolute; top: 0; bottom: 18px; width: 3px; border-radius: 2px; background: var(--teal); }
.item.drop-before::before { left: -7px; } .item.drop-after::after { right: -7px; }
.item[draggable="true"] .tile { cursor: grab; }
.item[draggable="true"] .tile:active { cursor: grabbing; }
.file-card { display: grid; justify-items: center; gap: 3px; padding: 8px; text-align: center; }
.badge { display: grid; place-items: center; min-width: 34px; height: 22px; padding: 0 6px; border-radius: 6px; background: var(--chip-teal-bg); color: var(--teal-ink); font: 700 10.5px/1 var(--mono); }
.file-name { max-width: 116px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11.5px; color: var(--ink); }
.file-size { font-size: 10.5px; color: var(--ink-2); }
.remove {
  position: absolute; top: 5px; right: 5px; display: grid; place-items: center; width: 24px; height: 24px; padding: 0; border: 0; border-radius: 50%;
  background: rgba(16, 35, 39, .72); color: #fff; opacity: 0; transition: opacity .12s ease;
}
.item:hover .remove, .item:focus-within .remove { opacity: 1; }
.remove:focus-visible { opacity: 1; box-shadow: var(--focus-ring); }
.strip-caption { max-width: 132px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 11.5px; color: var(--ink-2); }
.progress { position: absolute; left: 8px; right: 8px; bottom: 8px; height: 4px; border-radius: 999px; background: rgba(255, 255, 255, .6); overflow: hidden; }
.progress i { display: block; height: 100%; width: calc(var(--p) * 100%); background: var(--teal); transition: width .15s ease; }
.uploading img { opacity: .55; }
.failed .tile { box-shadow: inset 0 0 0 1.5px var(--danger-line); }
.failed-veil { position: absolute; inset: 0; display: grid; place-items: center; background: rgba(178, 74, 68, .18); color: var(--danger); }
.upload-actions { display: flex; gap: 10px; }
.sk-thumb { width: 132px; height: 88px; border-radius: 10px; }
/* Gallery: the context column's two-up grid with captions. */
.gallery .list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px 10px; overflow: visible; }
.gallery .tile { width: 100%; height: auto; aspect-ratio: 16 / 10; }
.gallery .file-name { max-width: 100%; }
.caption, .caption-input { width: 100%; min-height: 24px; padding: 2px 4px; border: 0; border-radius: 6px; background: transparent; color: var(--ink-2); font-size: 12px; text-align: left; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.caption:hover:not(:disabled) { background: var(--row-hover); color: var(--ink); }
.caption.unset { color: var(--ink-3); }
.caption:focus-visible { box-shadow: var(--focus-ring); }
.caption-input { background: var(--field-bg); box-shadow: var(--focus-ring); color: var(--ink); }
.gallery .item.drop-before::before, .gallery .item.drop-after::after { bottom: 26px; }
@media (hover: none) { .remove { opacity: 1; } }
/* Phones: the strip becomes the two-up grid; no tile is cut at the edge. */
@media (max-width: 600px) {
  .list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px 10px; overflow: visible; }
  .tile, .sk-thumb { width: 100%; height: auto; aspect-ratio: 3 / 2; }
  .strip-caption, .file-name { max-width: 100%; white-space: normal; overflow-wrap: anywhere; }
}
</style>
