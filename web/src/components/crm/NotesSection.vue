<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { api } from '../../lib/api'
import { applyNote, draftNote, errorText, statusOf, undoEvents, type Customer, type NoteProposal } from '../../lib/crm'
import { toast } from '../../lib/toast'
import { useCustomers } from '../../stores/customers'
import AppIcon from '../AppIcon.vue'
import BizIcon from '../business/BizIcon.vue'
import MarkdownBody from '../MarkdownBody.vue'
import MarkdownEditor from '../work/MarkdownEditor.vue'
import LineDiff from './LineDiff.vue'

// The team's notes on a customer, and rewriting them in two steps: a proposal is
// only a draft (kept on the server, nothing changes); an admin applies it
// separately, after seeing exactly what changes. Undo follows the apply.
const props = defineProps<{ customer: Customer; admin: boolean; locked?: boolean }>()
const emit = defineEmits<{ updated: [customer: Customer]; reload: [] }>()
const store = useCustomers()
const proposal = computed(() => store.proposals.get(props.customer.id))
// Anything that changes the customer (its revision) makes the proposal stale: the server refuses it then.
const stale = computed(() => !!proposal.value && proposal.value.base_revision !== props.customer.revision)
const notes = computed(() => props.customer.customer_notes ?? '')
const composing = ref(false)
const text = ref('')
const busy = ref(false)
const error = ref('')
const aiAvailable = ref(false)
const aiReason = ref('Checking AI note rewriting…')
const editor = ref<InstanceType<typeof MarkdownEditor>>()
const dialog = ref<HTMLDialogElement>()
const applyButton = ref<HTMLButtonElement>()
const section = ref<HTMLElement>()
let opener: HTMLElement | null = null

watch(() => [props.customer.id, props.customer.revision, props.admin], async () => {
  aiAvailable.value = false
  aiReason.value = 'Checking AI note rewriting…'
  if (!props.admin) return
  try {
    const response = await api(`/crm/organisations/${encodeURIComponent(props.customer.id)}/note-ai`)
    if (!response.ok) throw new Error('unavailable')
    const state = await response.json() as { enabled: boolean; reason?: string }
    aiAvailable.value = state.enabled
    aiReason.value = state.reason ?? ''
  } catch { aiReason.value = 'AI note rewriting is unavailable.' }
}, { immediate: true })

async function generateProposal() {
  if (busy.value || !aiAvailable.value) return
  busy.value = true; error.value = ''
  try {
    const response = await api(`/crm/organisations/${encodeURIComponent(props.customer.id)}/note-ai/generate`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: props.customer.revision }),
    }, 35_000)
    if (!response.ok) {
      const body = await response.json().catch(() => ({})) as { message?: string }
      throw new Error(body.message || 'AI note rewriting is unavailable. The notes were not changed.')
    }
    const draft = await response.json() as NoteProposal
    store.setProposal(props.customer.id, { ...draft, base_revision: props.customer.revision, base_text: notes.value })
    toast('AI proposal saved. Review the difference before applying it.')
  } catch (e) { error.value = errorText(e, 'AI note rewriting is unavailable.') }
  finally { busy.value = false }
}

async function compose(from = notes.value) {
  text.value = from; error.value = ''; composing.value = true
  await nextTick(); editor.value?.focus()
}
async function saveProposal() {
  if (busy.value) return
  if (!text.value.trim()) { error.value = 'A proposal needs some text.'; return }
  if (text.value === notes.value) { error.value = 'The proposal is the same as the notes.'; return }
  busy.value = true; error.value = ''
  try {
    const draft = await draftNote(props.customer.id, text.value, props.customer.revision)
    const next: NoteProposal = { ...draft, base_revision: props.customer.revision, base_text: notes.value }
    store.setProposal(props.customer.id, next)
    composing.value = false
    toast('Proposal saved. The notes stay as they are until it is applied.')
  } catch (e) {
    error.value = statusOf(e) === 409 ? 'The customer changed while you wrote this. Reload, then propose again; your text is still here.' : errorText(e, 'The proposal was not saved.')
    if (statusOf(e) === 409) emit('reload')
  } finally { busy.value = false }
}
function setAside() {
  store.setProposal(props.customer.id, null)
  toast('Proposal set aside. The notes are unchanged.')
}
// A stale proposal starts a new one from its own text.
function proposeAgain() {
  const from = proposal.value?.draft_text ?? notes.value
  store.setProposal(props.customer.id, null)
  void compose(from)
}
async function review() {
  opener = document.activeElement as HTMLElement
  error.value = ''
  dialog.value?.showModal()
  await nextTick(); applyButton.value?.focus()
}
function closeReview() { dialog.value?.close(); opener?.focus({ preventScroll: true }) }
async function apply() {
  const draft = proposal.value
  if (!draft || busy.value) return
  busy.value = true; error.value = ''
  try {
    const updated = await applyNote(props.customer.id, draft.id)
    store.setProposal(props.customer.id, null)
    dialog.value?.close()
    emit('updated', updated)
    void nextTick(() => section.value?.focus({ preventScroll: true }))
    toast('Notes rewritten.', {
      timeout: 8000,
      action: {
        label: 'Undo', run: () => {
          void undoEvents(updated.event_ids)
            .then(() => { emit('reload'); toast('The earlier notes are back.') })
            .catch(e => toast(errorText(e), { tone: 'error' }))
        },
      },
    })
  } catch (e) {
    error.value = statusOf(e) === 409 ? 'The customer changed since this proposal, so it was not applied. Propose again from the current notes.' : errorText(e, 'The proposal was not applied.')
    if (statusOf(e) === 409) emit('reload')
  } finally { busy.value = false }
}
function backdrop(event: MouseEvent) { if (event.target === dialog.value) closeReview() }
</script>

<template>
  <section ref="section" class="crm-card glass-card notes" aria-labelledby="notes-title" tabindex="-1">
    <header class="card-head">
      <span class="card-icon" aria-hidden="true"><BizIcon name="note" :size="15" /></span>
      <div class="card-titles">
        <h2 id="notes-title">Notes</h2>
        <p class="card-lead">For your team. Quotes never show them.</p>
      </div>
      <div v-if="admin && !locked && !composing && !proposal" class="note-actions">
        <button type="button" class="btn sm" @click="compose()"><AppIcon name="edit" :size="13" />Propose a rewrite</button>
        <button type="button" class="btn sm" :disabled="!aiAvailable || busy" :title="aiReason || undefined" :aria-describedby="!aiAvailable ? 'note-ai-reason' : undefined" @click="generateProposal"><AppIcon name="sparkle" :size="13" />{{ busy ? 'Generating…' : 'Suggest with AI' }}</button>
      </div>
    </header>

    <div v-if="!composing" class="notes-body">
      <MarkdownBody v-if="notes.trim()" :body="notes" />
      <p v-else class="empty-line">No notes yet.{{ admin ? ' Propose a first version; it applies once you confirm it.' : '' }}</p>
    </div>
    <p v-if="admin && !aiAvailable && !composing && !proposal" id="note-ai-reason" class="f-note">{{ aiReason }}</p>
    <p v-if="error && !composing" class="f-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>

    <div v-if="composing" class="composer">
      <MarkdownEditor ref="editor" v-model="text" label="Proposed notes" :saving="busy" save-label="Save proposal" :min-rows="6" @save="saveProposal" @cancel="composing = false" />
      <p class="f-note"><AppIcon name="info" :size="12" />A proposal is a draft. Nothing changes until an admin applies it and sees the difference first.</p>
      <p v-if="error" class="f-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
    </div>

    <div v-if="proposal && !composing" class="proposal" role="region" aria-labelledby="proposal-title">
      <header class="proposal-head">
        <h3 id="proposal-title">Proposed rewrite</h3>
        <span class="draft-chip">Draft, not applied</span>
      </header>
      <LineDiff :before="notes" :after="proposal.draft_text" label="Notes now against the proposal" />
      <p v-if="stale" class="f-note warn"><AppIcon name="alert" :size="12" />The customer changed since this proposal was made, so it can no longer be applied as it is. Propose again from the current notes.</p>
      <div class="proposal-actions">
        <button type="button" class="btn sm ghost" @click="setAside">Set aside</button>
        <span class="spacer" />
        <button v-if="stale && admin" type="button" class="btn sm" @click="proposeAgain">Propose again</button>
        <button v-else-if="admin" type="button" class="btn sm primary" @click="review"><AppIcon name="check" :size="13" />Review and apply…</button>
      </div>
    </div>

    <dialog ref="dialog" class="apply" aria-labelledby="apply-title" aria-describedby="apply-body" @cancel.prevent="closeReview" @click="backdrop">
      <div v-if="proposal" class="apply-card">
        <h2 id="apply-title">Apply this rewrite?</h2>
        <p id="apply-body">The notes of {{ customer.name }} are replaced by the proposal. You can undo it right after.</p>
        <LineDiff :before="notes" :after="proposal.draft_text" label="What changes" />
        <p v-if="error" class="f-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
        <div class="apply-actions">
          <button type="button" class="btn" @click="closeReview">Cancel</button>
          <button ref="applyButton" type="button" class="btn primary" :disabled="busy" @click="apply"><AppIcon name="check" :size="14" />{{ busy ? 'Applying…' : 'Apply rewrite' }}</button>
        </div>
      </div>
    </dialog>
  </section>
</template>

<style scoped>
.note-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.notes:focus-visible { box-shadow: var(--shadow), var(--focus-ring); }
.notes:focus { outline: none; }
.notes-body { font-size: 14px; }
/* Notes are written line by line; a single line break stays one. */
.notes-body :deep(p) { white-space: pre-line; }
.empty-line { font-size: 13.5px; color: var(--ink-3); }
.composer { display: grid; gap: 10px; }
.proposal { display: grid; gap: 10px; margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--line); }
.proposal-head { display: flex; align-items: center; gap: 10px; }
.proposal-head h3 { font-size: 14px; font-weight: 650; }
.draft-chip { display: inline-flex; align-items: center; height: 20px; padding: 0 8px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); font: 600 10px/1 var(--mono); letter-spacing: .06em; text-transform: uppercase; font-variant-ligatures: none; }
.proposal-actions { display: flex; align-items: center; gap: 8px; }
.spacer { flex: 1; }
.f-note.warn { color: var(--gold-ink); }
.f-note.warn svg { color: var(--gold-ink); }
.apply { width: min(var(--dialog-l), calc(100vw - 24px)); max-height: calc(100dvh - 24px); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.apply::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
.apply-card { display: grid; gap: 14px; max-height: calc(100dvh - 24px); overflow: auto; padding: 22px 24px 18px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
.apply-card h2 { font-size: 18px; }
.apply-card > p { font-size: 13.5px; color: var(--ink-2); }
.apply-actions { display: flex; justify-content: flex-end; gap: 8px; }
@media (max-width: 600px) {
  .note-actions .btn { height: 44px; }
  .apply-card { padding: 16px; }
  .apply-actions .btn, .proposal-actions .btn { height: 44px; }
  .proposal-actions { flex-wrap: wrap; }
}
</style>
