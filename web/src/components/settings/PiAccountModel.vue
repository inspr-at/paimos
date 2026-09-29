<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { setPiAccountModel, type AgentAccount } from '../../lib/agents'
import { validPiModel, piDataNote } from '../../lib/piModel'
const props = defineProps<{ account: AgentAccount; editable: boolean }>()
const emit = defineEmits<{ saved: [] }>()
const draft = ref(props.account.model ?? '')
const busy = ref(false)
const message = ref('')
const error = ref('')
const model = computed(() => draft.value.trim())
const changed = computed(() => model.value !== (props.account.model ?? ''))
const valid = computed(() => validPiModel(props.account.provider ?? '', model.value))
const note = computed(() => props.account.provider === 'openrouter' && (piDataNote(model.value) || (!changed.value && props.account.model_data_note)))
watch(() => props.account.model, (next, previous) => { if (draft.value === (previous ?? '')) draft.value = next ?? '' })
watch(draft, () => { message.value = ''; error.value = '' })
async function save() {
  if (busy.value || !props.editable || !changed.value || !valid.value) return
  busy.value = true; error.value = ''; message.value = ''
  try {
    const saved = await setPiAccountModel(props.account.id, model.value)
    draft.value = saved.model ?? model.value
    message.value = saved.model_status === 'unknown' ? 'Saved for new runs. Unknown slug — the public catalog could not confirm it.' : 'Saved for new runs.'
    emit('saved')
  } catch { error.value = 'Could not confirm the save. Reload accounts before trying again.' }
  finally { busy.value = false }
}
</script>
<template>
  <form v-if="account.provider" class="pi-model" @submit.prevent="save">
    <div class="model-row">
      <label :for="`pi-model-${account.id}`">Model <span>{{ account.provider === 'openrouter' ? 'OpenRouter' : account.provider }}</span></label>
      <div class="model-control">
        <input :id="`pi-model-${account.id}`" v-model="draft" class="field" :readonly="!editable" :disabled="busy" maxlength="116" spellcheck="false" autocomplete="off" :title="model" :aria-invalid="changed && !valid" :aria-describedby="`pi-model-help-${account.id}`" />
        <button v-if="editable && changed" class="btn sm" type="submit" :disabled="busy || !valid">{{ busy ? 'Saving…' : 'Save model' }}</button>
      </div>
    </div>
    <p :id="`pi-model-help-${account.id}`" class="model-help" role="status">{{ error || message || (changed && !valid ? 'Use vendor/model or vendor/model:variant.' : account.model_status === 'unknown' ? 'Unknown slug — the public catalog could not confirm it.' : 'Changes apply to new runs.') }}</p>
    <p v-if="note" class="data-note">Stealth and free models on OpenRouter may log prompts and outputs for the provider. Only send code you're fine sharing.</p>
    <p v-if="account.openrouter_credits" class="credits" :title="`Read ${account.openrouter_credits.observed_at}`">
      <span v-if="account.openrouter_credits.usage != null">${{ account.openrouter_credits.usage.toFixed(2) }} used</span>
      <span v-if="account.openrouter_credits.limit != null">${{ account.openrouter_credits.limit.toFixed(2) }} key limit</span>
      <span v-if="account.openrouter_credits.remaining != null">${{ account.openrouter_credits.remaining.toFixed(2) }} remaining</span>
    </p>
  </form>
</template>
<style scoped>
.pi-model { margin-top: 14px; max-width: 640px; }
.model-row { display: grid; grid-template-columns: 96px minmax(0, 1fr); align-items: center; gap: 12px; }
label { font-size: 12px; font-weight: 600; color: var(--ink); }
label span { display: block; margin-top: 3px; font-weight: 400; color: var(--ink-2); }
.model-control { display: flex; min-width: 0; gap: 8px; }
.field { min-width: 0; width: 100%; font-family: var(--mono); font-size: 12px; text-overflow: ellipsis; }
button { flex-shrink: 0; }
.model-help, .data-note, .credits { font-size: 12px; line-height: 1.5; color: var(--ink-2); margin-top: 7px; }
.credits { display: flex; flex-wrap: wrap; gap: 4px 14px; }
@media (max-width: 560px) { .model-row { grid-template-columns: minmax(0, 1fr); gap: 7px; } label span { display: inline; margin-left: 7px; } .model-control { flex-wrap: wrap; } }
</style>
