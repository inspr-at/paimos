<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { ref } from 'vue'
import RulesDialog from './RulesDialog.vue'
import BizIcon from '../business/BizIcon.vue'
import { doctrineMessage, proposeDoctrineChange, submitDoctrineInbox, DoctrineError, type DoctrineSource, type DoctrineFile, type DoctrineRule, type DoctrineProposal, type DoctrineInboxItem } from '../../lib/doctrine'
// draft: "edit then propose" for a doctrine inbox proposal (AEON-444). The
// person's text replaces the agent's before it becomes a pull request.
const props = defineProps<{ source: DoctrineSource; file: DoctrineFile; rule: DoctrineRule; draft?: DoctrineInboxItem }>()
const emit = defineEmits<{ close: []; saved: [proposal: DoctrineProposal] }>()
const sourceText = ref(props.draft?.proposed ?? props.rule.source)
const en = ref(props.draft?.tldr?.en ?? props.rule.tldr?.en ?? '')
const de = ref(props.draft?.tldr?.de ?? props.rule.tldr?.de ?? '')
const explanation = ref(props.draft?.why ?? '')
const busy = ref(false)
const error = ref('')
const frozen = ref(false)
const requestId = crypto.randomUUID()
async function submit() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  frozen.value = true
  try {
    emit('saved', props.draft
      ? await submitDoctrineInbox(props.draft.id, { source: sourceText.value, tldr: { en: en.value, de: de.value }, why: explanation.value, rule_sha256: props.rule.sha256 })
      : await proposeDoctrineChange({ request_id: requestId, source_id: props.source.id, path: props.file.path, rule_key: props.rule.key, rule_sha256: props.rule.sha256, source: sourceText.value, tldr: { en: en.value, de: de.value }, explanation: explanation.value }))
  } catch (cause) {
    error.value = doctrineMessage(cause)
    // A lost response can have created the PR. Keep its exact input and UUID
    // for recovery; validation/leak failures happen before any publication.
    if (cause instanceof DoctrineError && ['invalid_request', 'credential_text', 'public_identity', 'stale_rule', 'stale_source', 'invalid_sidecar', 'unsupported_repository', 'app_unavailable', 'forbidden', 'not_pending', 'request_conflict'].includes(cause.code)) frozen.value = false
  } finally { busy.value = false }
}
</script>
<template>
  <RulesDialog :title="draft ? 'Edit, then propose' : 'Propose change'" :lede="draft ? `Your edits replace ${draft.proposer || 'the proposer'}’s text in a pull request to ${source.repository}.` : `A pull request in ${source.repository}; a person approves after checks and review.`" size="wide" :busy="busy" @close="emit('close')">
    <form id="doctrine-proposal-form" class="form" @submit.prevent="submit">
      <label>Rule<textarea v-model="sourceText" class="field mono" rows="5" required :readonly="frozen" data-autofocus /></label>
      <label>TL;DR · English<input v-model="en" class="field" maxlength="300" required :readonly="frozen"></label>
      <details><summary><BizIcon name="chevron-right" :size="14" />German TL;DR</summary><label class="translation">TL;DR · German<input v-model="de" class="field" maxlength="300" :readonly="frozen"></label></details>
      <label>Why this change<textarea v-model="explanation" class="field" rows="3" maxlength="2000" required :readonly="frozen" /></label>
      <p v-if="source.visibility === 'public'" class="hint">Public, identity-free doctrine only; private details are blocked before publication.</p>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <p v-if="frozen && !busy" class="hint">Retry keeps the same request so an interrupted response cannot create a second PR.</p>
    </form>
    <template #footer>
      <button type="button" class="btn ghost" :disabled="busy" @click="emit('close')">Cancel</button>
      <button type="submit" form="doctrine-proposal-form" class="btn primary submit-action" :disabled="busy || !en.trim() || !explanation.trim() || !sourceText.trim()"><span class="submit-label" data-size-label="Create pull request"><span>{{ busy ? 'Creating PR…' : frozen ? 'Retry proposal' : 'Create pull request' }}</span></span></button>
    </template>
  </RulesDialog>
</template>
<style scoped>
.form, label { display: flex; flex-direction: column; gap: 7px; min-width: 0; }
.form { gap: 15px; }
.submit-label { display: grid; justify-items: center; }
/* Reserve the longest state label without fixing a pixel width or moving Cancel. */
.submit-label::before { content: attr(data-size-label); visibility: hidden; grid-area: 1 / 1; }
.submit-label > span { grid-area: 1 / 1; }
label { color: var(--ink-2); font-size: 13px; font-weight: 600; }
.field { box-sizing: border-box; width: 100%; min-width: 0; padding: 10px 12px; border: 1px solid var(--line); border-radius: 9px; background: var(--surface-2); color: var(--ink); font: inherit; font-weight: 400; line-height: 1.5; resize: vertical; }
textarea.field { height: auto; min-height: 90px; }
textarea[data-autofocus] { min-height: 140px; }
.field:focus-visible { outline: none; box-shadow: var(--focus-ring); }
.mono { font-family: var(--mono); font-size: 12px; }
.hint, summary { margin: 0; color: var(--ink-3); font-size: 12px; line-height: 1.5; }
summary { display: flex; align-items: center; gap: 6px; list-style: none; cursor: pointer; }
summary::-webkit-details-marker { display: none; }
details[open] summary :deep(svg) { transform: rotate(90deg); }
.translation { padding-top: 8px; }
.error { margin: 0; color: var(--ink); padding: 12px; border-radius: 9px; background: var(--surface-2); font-size: 13px; line-height: 1.5; }
</style>
