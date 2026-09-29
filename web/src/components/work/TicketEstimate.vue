<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import type { ListItem } from '../../lib/api'
import { estimateDisplay, parseEstimate } from '../../lib/estimates'
import type { SaveResult } from '../../lib/useTicket'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ item: ListItem; editable: boolean; save?: (hours: number | null) => Promise<SaveResult> }>()
const view = computed(() => estimateDisplay(props.item))
const canEdit = computed(() => props.editable && props.item.kind_slug !== 'epic' && !!props.save)
const editing = ref(false), saving = ref(false), draft = ref(''), error = ref('')
const input = ref<HTMLInputElement | null>(null)
const control = ref<HTMLButtonElement | null>(null)
const errorId = useId()
async function closeEdit() { editing.value = false; await nextTick(); control.value?.focus() }
watch(() => props.item.id, () => { editing.value = false; error.value = '' })
async function edit() {
  draft.value = view.value.hours === null ? '' : String(view.value.hours)
  error.value = ''; editing.value = true
  await nextTick(); input.value?.focus(); input.value?.select()
}
async function submit() {
  if (saving.value || !canEdit.value) return
  const hours = draft.value.trim() ? parseEstimate(draft.value) : null
  if (draft.value.trim() && hours === null) { error.value = 'Use 2h, 30m or 1.5; up to 200 hours.'; return }
  const id = props.item.id
  saving.value = true; error.value = ''
  try {
    const result = await props.save!(hours)
    if (props.item.id !== id) return
    if (result === 'ok') await closeEdit()
    else error.value = result === 'conflict' ? 'Changed elsewhere. Review your estimate and save again.' : 'Estimate was not saved. Try again.'
  } finally { saving.value = false }
}
</script>

<template>
  <div v-if="canEdit || view.text || item.estimate?.open_children" class="prop estimate-prop">
    <dt>Estimate</dt>
    <dd>
      <form v-if="editing" class="estimate-editor" @submit.prevent="submit" @keydown.esc.stop.prevent="closeEdit">
        <label class="estimate-label">Agent hours<input ref="input" v-model="draft" aria-label="Estimate in agent hours" placeholder="2h or 30m" :disabled="saving" :aria-invalid="!!error" :aria-describedby="error ? errorId : undefined" autocomplete="off" @input="error = ''" /></label>
        <div class="estimate-actions"><button class="save-estimate" type="submit" :disabled="saving">{{ saving ? 'Saving…' : 'Save' }}</button><button type="button" :disabled="saving" @click="closeEdit">Cancel</button></div>
        <p v-if="error" :id="errorId" role="alert">{{ error }}</p>
      </form>
      <button v-else-if="canEdit" ref="control" type="button" class="prop-btn" :class="{ 'estimate-draft': view.draft }" :data-tip="view.tip" :aria-label="view.text ? `Estimate: ${view.text}. Edit estimate` : 'Add estimate'" @click="edit">
        <span class="inline-label">Estimate</span><span v-if="view.text" class="mono">{{ view.text }}</span><span v-else>Add</span><span v-if="view.draft" class="estimate-mark">est.</span><AppIcon name="chevron" :size="12" class="chev" />
      </button>
      <span v-else class="prop-static" :class="{ 'estimate-draft': view.draft }" :data-tip="view.tip"><span class="inline-label">Estimate</span><span class="mono">{{ view.text || '—' }}</span><span v-if="view.draft" class="estimate-mark">est.</span></span>
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
.estimate-draft { color: var(--ink-3); }
.estimate-mark { font-size: 10px; }
.estimate-editor { display: grid; gap: 8px; max-width: 260px; padding: 8px 0; }
.estimate-label { display: grid; gap: 6px; font-size: 11px; color: var(--ink-2); }
input { width: 100%; min-width: 0; box-sizing: border-box; padding: 8px 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--surface); color: var(--ink); font: 13px var(--mono); }
input:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.estimate-actions { display: flex; gap: 6px; }
.estimate-actions button { padding: 6px 12px; border: 1px solid var(--line); border-radius: 8px; background: transparent; color: var(--ink-2); font-size: 12px; cursor: pointer; }
.estimate-actions .save-estimate { background: var(--row-selected); color: var(--ink); }
button:focus-visible { outline: none; box-shadow: var(--focus-ring); }
p { margin: 0; color: var(--ink-2); font-size: 12px; line-height: 1.4; }
</style>
