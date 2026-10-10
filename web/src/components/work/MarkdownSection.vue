<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { SaveResult } from '../../lib/useTicket'
import { confirmAction } from '../../lib/confirm'
import AppIcon from '../AppIcon.vue'
import MarkdownBody from '../MarkdownBody.vue'
import MarkdownEditor from './MarkdownEditor.vue'

// One Markdown section of a ticket (Description, Acceptance criteria, Notes):
// rendered by default, edited in place, conflicts keep the draft.
const props = defineProps<{ recordId?: string; title: string; value: string; editable: boolean; save: (value: string) => Promise<SaveResult>; emptyText?: string; attachmentId?: (file: File) => Promise<string | null> }>()
const emit = defineEmits<{ openAttachment: [id: string] }>()
const editing = ref(false)
const draft = ref('')
const saving = ref(false)
const conflict = ref(false)
const editor = ref<InstanceType<typeof MarkdownEditor>>()
let generation = 0
let draftSave = props.save
function discard() { generation++; editing.value = false; draft.value = ''; saving.value = false; conflict.value = false }
watch(() => props.recordId, discard, { flush: 'sync' })
const dirty = computed(() => editing.value && draft.value !== props.value)
watch(() => props.value, () => { if (!editing.value) conflict.value = false })

async function start() {
  if (!props.editable) return
  draftSave = props.save
  draft.value = props.value; conflict.value = false; editing.value = true
  await nextTick(); editor.value?.focus()
}
async function commit() {
  if (saving.value) return
  if (draft.value === props.value && !conflict.value) { editing.value = false; return }
  const request = generation
  saving.value = true
  const result = await draftSave(draft.value)
  if (request !== generation) return
  saving.value = false
  if (result === 'ok') { editing.value = false; conflict.value = false }
  else if (result === 'conflict') conflict.value = true
}
async function cancel() {
  if (dirty.value && !(await confirmAction({ title: `Discard your ${props.title.toLowerCase()} changes?`, body: 'Your edits have not been saved.', confirmLabel: 'Discard', danger: true }))) { editor.value?.focus(); return }
  discard()
}
defineExpose({ start, isDirty: () => dirty.value, editing, discard })
</script>

<template>
  <section class="md-section" :class="{ editing }" :aria-label="title">
    <header class="section-head">
      <h3 class="eyebrow">{{ title }}</h3>
      <button v-if="editable && !editing && value.trim()" type="button" class="icon-btn sm flat edit-btn" :aria-label="`Edit ${title.toLowerCase()}`" :data-tip="`Edit ${title.toLowerCase()}`" @click="start"><AppIcon name="edit" :size="13" /></button>
    </header>
    <template v-if="editing">
      <div v-if="conflict" class="conflict" role="alert">
        <AppIcon name="alert" :size="14" />
        <div>
          <p><strong>Changed elsewhere while you were editing.</strong> Your draft is kept below; saving again replaces the newer version.</p>
          <details><summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Show the newer version</summary><MarkdownBody :body="value || '*Empty*'" /></details>
        </div>
      </div>
      <MarkdownEditor ref="editor" v-model="draft" :label="title" :saving="saving" :attachment-id="attachmentId" :save-label="conflict ? 'Save anyway' : 'Save'" @save="commit" @cancel="cancel" />
    </template>
    <div v-else-if="value.trim()" class="section-body" :class="{ clickable: editable }" @dblclick="start">
      <MarkdownBody :body="value" @open-attachment="id => emit('openAttachment', id)" />
    </div>
    <button v-else-if="editable" type="button" class="empty-add" @click="start"><AppIcon name="plus" :size="13" />{{ emptyText ?? `Add ${title.toLowerCase()}` }}</button>
  </section>
</template>

<style scoped>
.md-section + .md-section { margin-top: 22px; }
.section-head { display: flex; align-items: center; justify-content: space-between; min-height: 26px; margin-bottom: 6px; }
.section-head .eyebrow { margin: 0; font-size: 10.5px; font-weight: 500; }
.edit-btn { width: 26px; height: 26px; color: var(--ink-3); opacity: 0; }
.md-section:hover .edit-btn, .edit-btn:focus-visible { opacity: 1; }
@media (hover: none) { .edit-btn { opacity: 1; } }
.section-body { border-radius: 8px; }
.empty-add { display: inline-flex; align-items: center; gap: 6px; height: 30px; padding: 0 10px; margin-left: -10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-3); font-size: 13px; }
.empty-add:hover { color: var(--teal-ink); background: var(--row-hover); }
.empty-add:focus-visible { box-shadow: var(--focus-ring); }
.conflict { display: flex; gap: 10px; margin-bottom: 10px; padding: 10px 12px; border-radius: 10px; background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); color: var(--ink); font-size: 13px; }
.conflict > svg { flex-shrink: 0; margin-top: 2px; color: var(--danger); }
.conflict p { color: var(--ink); }
.conflict summary { margin-top: 6px; cursor: pointer; color: var(--teal-ink); font-size: 12.5px; }
.conflict details :deep(.markdown-body) { margin-top: 8px; font-size: 13px; }
</style>
