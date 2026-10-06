<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
// AEON-791: the word people read for a project's lead. Inside PAIMOS it is
// always the lead; the names ride on the work vocabulary (one revision).
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import { leadWords, pluralWord } from '../../lib/lead'
import type { LeadNames, WorkVocabulary } from '../../lib/workVocabulary'
import { useSession } from '../../stores/session'
import { useWorkVocabulary } from '../../stores/workVocabulary'
import KeyCap from '../KeyCap.vue'
import SettingsCard from './SettingsCard.vue'
const session = useSession()
const vocabulary = useWorkVocabulary()
const editable = computed(() => can('settings.manage'))
const SUGGESTIONS = ['Supervisor', 'Coordinator', 'Conductor']
const draft = ref<LeadNames>({ singular: '', plural: '' })
const saved = computed<LeadNames>(() => vocabulary.value.lead ?? { singular: '', plural: '' })
const busy = ref(false), message = ref(''), error = ref(false)
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
const words = computed(() => leadWords(draft.value.singular, draft.value.plural))
const current = computed(() => leadWords(saved.value.singular, saved.value.plural))
const dirty = computed(() => words.value.S !== current.value.S || words.value.P !== current.value.P)
const pluralHint = computed(() => draft.value.singular.trim() ? pluralWord(draft.value.singular.trim()) : 'Leads')
const preview = computed(() => [
  { where: 'Project', text: `AEON ${words.value.l} · Working · Start ${words.value.l}` },
  { where: 'Ticket', text: `Queued, waiting for a ${words.value.l}` },
  { where: 'Agents page', text: `${words.value.P} · One per project` },
])
const feedback = computed(() => message.value
  || (vocabulary.error && !vocabulary.loaded ? vocabulary.error : '')
  || (dirty.value ? `Not saved yet: “${words.value.S}” and “${words.value.P}”.` : 'Blank names use “Lead” and “Leads”.'))
let generation = 0
onBeforeUnmount(() => { generation++ })
// The draft follows the saved names until the person edits it; a new identity starts clean.
watch(saved, (next, prior) => {
  if (!prior || (draft.value.singular === prior.singular && draft.value.plural === prior.plural)) draft.value = { ...next }
}, { immediate: true })
watch(() => `${session.identity?.tenant.id}:${session.identity?.principal.id}`, () => {
  generation++; busy.value = false; message.value = ''; error.value = false
  draft.value = { ...saved.value }
  void vocabulary.load()
}, { immediate: true })
async function reload() {
  if (busy.value) return
  const run = ++generation
  busy.value = true; message.value = ''; error.value = false
  try {
    const response = await api('/settings/work-vocabulary')
    if (!response.ok) throw new Error('Agent names could not be read.')
    const value = await response.json() as WorkVocabulary
    if (run !== generation) return
    vocabulary.accept(value); draft.value = { ...saved.value }
  } catch (e) { if (run === generation) { error.value = true; message.value = e instanceof Error ? e.message : 'Agent names could not be read.' } }
  finally { if (run === generation) busy.value = false }
}
async function save() {
  if (!editable.value || !vocabulary.loaded || busy.value) return
  const run = ++generation, base = vocabulary.value
  // Only the names change; levels go back exactly as last read, under the same revision.
  const lead = { singular: draft.value.singular.trim(), plural: draft.value.plural.trim() }
  const body = JSON.stringify({ revision: base.revision, leaf: base.leaf, levels: base.levels, lead })
  busy.value = true; message.value = ''; error.value = false
  try {
    const response = await api('/settings/work-vocabulary', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body })
    const value = await response.json().catch(() => ({}))
    if (run !== generation) return
    if (!response.ok) throw new Error(typeof value.error === 'string' ? value.error : 'Agent names were not saved.')
    vocabulary.accept(value as WorkVocabulary); draft.value = { ...saved.value }; message.value = 'Agent names saved.'
  } catch (e) { if (run === generation) { error.value = true; message.value = e instanceof Error ? e.message : 'Agent names were not saved.' } }
  finally { if (run === generation) busy.value = false }
}
function suggest(word: string) {
  draft.value = { singular: word, plural: pluralWord(word) }; message.value = ''; error.value = false
}
function keys(e: KeyboardEvent) {
  if (e.key === 'Enter' && (mac ? e.metaKey : e.ctrlKey)) { e.preventDefault(); void save() }
  if (e.key === 'Escape' && e.target instanceof HTMLElement && e.target.matches('input')) { e.preventDefault(); e.stopPropagation(); e.target.blur() }
}
</script>
<template>
  <SettingsCard title="Agent names" icon="agent" anchor="agent-names">
    <template #lead>What people call the agent that runs a project. Inside PAIMOS it is always the lead; only the word people read changes.</template>
    <div class="names" @keydown="keys">
      <div v-if="editable" class="actions" aria-label="Agent name actions">
        <button class="btn sm" type="button" :disabled="busy" @click="reload">Reload</button>
        <button class="btn sm primary" type="button" :disabled="!vocabulary.loaded || busy" @click="save">Save names <KeyCap k="mod" /><KeyCap k="enter" /></button>
      </div>
      <fieldset v-if="editable" :disabled="!vocabulary.loaded || busy" @input="message = ''; error = false">
        <div class="names-row">
          <span class="lbl">Project agent<small>One per project</small></span>
          <input v-model="draft.singular" class="field" maxlength="40" placeholder="Lead" aria-label="Name, one" autocomplete="off" />
          <input v-model="draft.plural" class="field" maxlength="40" :placeholder="pluralHint" aria-label="Name, several" autocomplete="off" />
        </div>
        <p class="suggest">Others use <button v-for="word in SUGGESTIONS" :key="word" type="button" class="btn sm ghost" @click="suggest(word)">{{ word }}</button></p>
      </fieldset>
      <dl v-else class="read-only">
        <div><dt>One project agent</dt><dd>{{ current.S }}</dd></div>
        <div><dt>Several</dt><dd>{{ current.P }}</dd></div>
      </dl>
      <p v-if="!editable" class="set-note">Only admins change these names. Everyone sees them.</p>
      <button v-if="(error || vocabulary.error) && !editable" type="button" class="btn sm" @click="reload">Try again</button>
      <p class="feedback" :class="{ error: error || (!!vocabulary.error && !vocabulary.loaded), dirty: dirty && !message }" role="status">{{ feedback }}</p>
      <p class="eyebrow">Where people read it</p>
      <ul class="preview" aria-label="Where people read it">
        <li v-for="row in preview" :key="row.where"><span>{{ row.where }}</span><span>{{ row.text }}</span></li>
      </ul>
    </div>
  </SettingsCard>
</template>
<style scoped>
.actions { display: flex; flex-wrap: wrap; gap: 8px; }
fieldset { border: 0; padding: 0; margin: 14px 0 0; min-width: 0; }
.names-row { display: grid; grid-template-columns: minmax(8em, 1fr) minmax(0, 1fr) minmax(0, 1fr); gap: 6px 10px; align-items: center; padding: 8px 0; border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); }
.lbl { font-size: 13px; font-weight: 600; color: var(--ink); }
.lbl small { display: block; font-size: 12px; font-weight: 400; color: var(--ink-3); }
.names-row .field { min-width: 0; width: 100%; min-height: 36px; }
.suggest { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 6px; margin: 10px 0 0; font-size: 12.5px; color: var(--ink-3); }
.read-only { display: grid; gap: 12px; margin: 14px 0; min-width: 0; }
.read-only > div { display: flex; justify-content: space-between; gap: 16px; min-width: 0; }
.read-only dt { color: var(--ink-2); flex-shrink: 0; }
.read-only dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
.feedback { min-height: 3em; margin: 10px 0; color: var(--ink-2); font-size: 13px; overflow-wrap: anywhere; }
.feedback.dirty { color: var(--warn-ink); }
.feedback.error { color: var(--danger); }
.eyebrow { margin: 0; }
.preview { display: grid; gap: 6px; margin: 8px 0 0; padding: 0; list-style: none; }
.preview li { display: grid; grid-template-columns: minmax(8em, 150px) minmax(0, 1fr); gap: 12px; font-size: 13px; color: var(--ink); overflow-wrap: anywhere; }
.preview li span:first-child { color: var(--ink-3); font-size: 12.5px; }
@media (max-width: 600px) {
  .names-row { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
  .lbl { grid-column: 1 / -1; }
  .names-row .field { min-height: 44px; }
  .actions .btn, .suggest .btn { min-height: 44px; }
  .preview li { grid-template-columns: minmax(0, 1fr); gap: 0; }
}
</style>
