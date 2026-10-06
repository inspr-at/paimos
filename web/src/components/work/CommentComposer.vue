<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import KeyCap from '../KeyCap.vue'
import PersonAvatar from './PersonAvatar.vue'

// Write a comment: Markdown, Cmd/Ctrl+Enter posts, c focuses it from the panel.
const props = defineProps<{ recordId?: string; me: string; meId?: string | null; post: (body: string) => Promise<boolean>; disabled?: boolean }>()
const draft = defineModel<string>({ default: '' })
const busy = ref(false)
const focused = ref(false)
let generation = 0
function discard() { generation++; draft.value = ''; busy.value = false; focused.value = false }
watch(() => props.recordId, discard, { flush: 'sync' })
const area = ref<HTMLTextAreaElement>()
const open = computed(() => focused.value || !!draft.value)
function grow() { const el = area.value; if (el) { el.style.height = 'auto'; el.style.height = `${Math.min(el.scrollHeight + 2, 240)}px` } }
async function submit() {
  if (!draft.value.trim() || busy.value) return
  const request = generation
  busy.value = true
  const ok = await props.post(draft.value)
  if (request !== generation) return
  busy.value = false
  if (ok) { draft.value = ''; await nextTick(); grow() }
}
function keydown(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); void submit() }
  else if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); area.value?.blur() }
}
async function focus() { area.value?.focus(); await nextTick(); grow() }
defineExpose({ focus, isDirty: () => !!draft.value.trim(), discard })
</script>

<template>
  <div class="composer" :class="{ open }">
    <PersonAvatar :id="meId" :name="me" :size="26" class="me" />
    <div class="composer-box">
      <textarea
        ref="area" v-model="draft" class="composer-area" rows="1" :placeholder="disabled ? 'You can read this ticket but not comment' : 'Add a comment… Markdown works'"
        aria-label="Add a comment" aria-keyshortcuts="c" :disabled="disabled || busy" @input="grow" @keydown="keydown" @focus="focused = true; grow()" @blur="focused = false"
      />
      <div v-if="open" class="composer-foot">
        <span class="keys"><KeyCap k="mod" /><KeyCap k="enter" /> to send</span>
        <button type="button" class="btn sm on" :disabled="busy || !draft.trim()" @mousedown.prevent @click="submit">{{ busy ? 'Sending…' : 'Comment' }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.composer { display: flex; align-items: flex-start; gap: 10px; }
.me { margin-top: 5px; }
.composer-box { flex: 1; min-width: 0; border-radius: 12px; border: 1px solid var(--glass-edge); background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); }
.composer.open .composer-box { box-shadow: var(--focus-ring); }
.composer-area { display: block; width: 100%; min-height: 36px; padding: 8px 12px; border: 0; background: transparent; color: var(--ink); resize: none; font: 13.5px/1.5 var(--font); }
.composer-area:focus { box-shadow: none; }
@media (max-width: 600px) { .composer-area { min-height: 44px; } }
.composer-area::placeholder { color: var(--ink-3); }
.composer-foot { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 0 6px 6px 12px; }
.keys { display: inline-flex; align-items: center; gap: 3px; font-size: 11.5px; color: var(--ink-3); }
@media (hover: none) { .keys { visibility: hidden; } }
</style>
