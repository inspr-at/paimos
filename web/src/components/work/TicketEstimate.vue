<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import type { ListItem } from '../../lib/api'
import { estimateControlLabel, estimateDisplay, parseEstimate } from '../../lib/estimates'
import type { SaveResult } from '../../lib/useTicket'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import { fieldTarget, macPlatform, submitModifier } from '../../lib/decisionDesk'

const props = defineProps<{ item: ListItem; editable: boolean; save?: (hours: number | null) => Promise<SaveResult> }>()
const view = computed(() => estimateDisplay(props.item))
const editLabel = computed(() => estimateControlLabel(props.item))
const canEdit = computed(() => props.editable && !!props.save)
const editing = ref(false), saving = ref(false), draft = ref(''), error = ref('')
const input = ref<HTMLInputElement | null>(null)
const control = ref<HTMLButtonElement | null>(null)
const errorId = useId()
const mac = macPlatform(navigator.platform)
let generation = 0
function keys(event: KeyboardEvent) {
 if (event.isComposing || event.repeat) return
 if (event.key === 'Escape') {
  event.preventDefault(); event.stopPropagation()
  if (fieldTarget(event.target)) (event.target as HTMLElement).blur()
  else void closeEdit()
 } else if (event.key === 'Enter' && fieldTarget(event.target)) {
  if (submitModifier(event, mac)) { event.preventDefault(); event.stopPropagation(); void submit() }
  else if (!event.metaKey && !event.ctrlKey && !event.altKey) event.preventDefault()
 }
}
async function closeEdit() { editing.value = false; await nextTick(); control.value?.focus() }
watch(() => props.item.id, () => { generation++; editing.value = false; saving.value = false; error.value = '' })
async function edit() {
  const hours = props.item.estimate?.is_parent ? props.item.estimate.planned_hours : view.value.hours
  draft.value = hours == null ? '' : String(hours)
  error.value = ''; editing.value = true
  await nextTick(); input.value?.focus(); input.value?.select()
}
async function submit() {
  if (saving.value || !canEdit.value) return
  const hours = draft.value.trim() ? parseEstimate(draft.value) : null
  if (draft.value.trim() && hours === null) { error.value = 'Use 2h, 30m or 1.5; up to 200 hours.'; return }
  const id = props.item.id, request = generation
  saving.value = true; error.value = ''
  try {
    const result = await props.save!(hours)
    if (props.item.id !== id || request !== generation) return
    if (result === 'ok') await closeEdit()
    else error.value = result === 'conflict' ? 'Changed elsewhere. Review your estimate and save again.' : 'Estimate was not saved. Try again.'
  } catch { if (props.item.id === id && request === generation) error.value = 'Estimate was not saved. Try again.' } finally { if (request === generation) saving.value = false }
}
</script>

<template>
  <div v-if="canEdit || view.text || item.estimate?.open_children" class="prop estimate-prop">
    <dt>{{ item.estimate?.is_parent ? 'Parent estimate' : 'Estimate' }}</dt>
    <dd>
      <form v-if="editing" class="estimate-editor" @submit.prevent="submit" @keydown.capture="keys">
        <div class="estimate-actions"><button class="save-estimate" type="submit" aria-label="Save" :aria-keyshortcuts="mac ? 'Meta+Enter' : 'Control+Enter'" :disabled="saving">{{ saving ? 'Saving…' : 'Save' }} <KeyCap k="mod" /><KeyCap k="enter" /></button><button type="button" aria-label="Cancel" :disabled="saving" @click="closeEdit">Cancel <kbd>Esc</kbd></button></div>
        <label class="estimate-label">{{ item.estimate?.is_parent ? 'Planned agent hours' : 'Agent hours' }}<input ref="input" v-model="draft" aria-label="Estimate in agent hours" placeholder="2h or 30m" :disabled="saving" :aria-invalid="!!error" :aria-describedby="error ? errorId : undefined" autocomplete="off" @input="error = ''" /></label>
        <p v-if="error" :id="errorId" role="alert">{{ error }}</p>
      </form>
      <button v-else-if="canEdit" ref="control" type="button" class="prop-btn" :class="{ 'estimate-draft': view.draft, 'parent-estimate': item.estimate?.is_parent }" :data-tip="view.tip" :aria-label="editLabel" @click="edit">
        <span class="inline-label">Estimate</span><span v-if="view.text" class="mono">{{ view.text }}</span><span v-else>Add</span><span v-if="view.draft" class="estimate-mark">est.</span><AppIcon name="chevron" :size="12" class="chev" />
      </button>
      <span v-else class="prop-static" :class="{ 'estimate-draft': view.draft, 'parent-estimate': item.estimate?.is_parent }" :data-tip="view.tip"><span class="inline-label">Estimate</span><span class="mono">{{ view.text || '—' }}</span><span v-if="view.draft" class="estimate-mark">est.</span></span>
      <span v-if="item.estimate?.is_parent" class="estimate-coverage">{{ item.estimate.estimated_leaves ?? 0 }} of {{ item.estimate.leaf_count ?? 0 }} leaves estimated</span>
    </dd>
  </div>
</template>

<style scoped>
.prop-btn, .prop-static { display: inline-flex; align-items: center; gap: 7px; max-width: 100%; min-height: 28px; padding: 0 9px; border: 0; border-radius: 999px; font-size: 12.5px; color: var(--ink); white-space: nowrap; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); }
.prop-btn { cursor: pointer; }
.prop-btn:hover { background: var(--row-hover); }
.column .prop-btn, .column .prop-static { margin-left: -9px; background: transparent; box-shadow: none; }
.column .inline-label { display: none; }
.inline-label { font: 500 9.5px var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); }
.mono { font: 11.5px var(--mono); font-variant-numeric: tabular-nums; }
.chev { color: var(--ink-3); }
@media (max-width: 720px) { .prop-btn, .prop-static { min-height: 34px; } }
.estimate-coverage { display: block; color: var(--ink-3); font-size: 11px; margin-top: 4px; }
.parent-estimate { white-space: normal; flex-wrap: wrap; border-radius: 0; background: transparent; box-shadow: none; text-align: left; }
.estimate-draft { color: var(--ink-3); }
.estimate-mark { font-size: 10px; }
.estimate-editor { display: grid; gap: 8px; inline-size: clamp(15rem, 30vw, 20rem); max-inline-size: 100%; padding: 8px 0; }
.estimate-label { display: grid; gap: 6px; font-size: 11px; color: var(--ink-2); }
input { width: 100%; min-width: 0; box-sizing: border-box; padding: 8px 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface); color: var(--ink); font: 13px var(--mono); }
input:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.estimate-actions { display: flex; gap: 6px; }
.estimate-actions button { padding: 6px 12px; border: 1px solid var(--line); border-radius: 8px; background: transparent; color: var(--ink-2); font-size: 12px; cursor: pointer; }
.estimate-actions kbd { font: 10px var(--mono); color: var(--ink-3); }
.estimate-actions .save-estimate { inline-size: 8rem; flex: none; display: inline-flex; align-items: center; justify-content: center; gap: 4px; background: var(--row-selected); color: var(--ink); }
button:focus-visible { outline: none; box-shadow: var(--focus-ring); }
@media (pointer: coarse) { .prop-btn, .estimate-actions button, input { min-height: 44px; } }
p { margin: 0; color: var(--ink-2); font-size: 12px; line-height: 1.4; }
</style>
