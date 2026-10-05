<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { useWorkVocabulary } from '../../stores/workVocabulary'
import { WORK_ICONS, workLevel, type WorkVocabulary } from '../../lib/workVocabulary'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import SettingsCard from './SettingsCard.vue'
const session = useSession()
const vocabulary = useWorkVocabulary()
const draft = ref<WorkVocabulary>({ revision: 0, leaf: { name: '', icon: '' }, levels: [] })
const editable = computed(() => can('settings.manage'))
const loaded = ref(false), busy = ref(false), message = ref(''), error = ref(false)
const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
let generation = 0
onBeforeUnmount(() => { generation++ })
const preview = computed(() => [workLevel(draft.value, false, 1).name, workLevel(draft.value, false, 2).name, workLevel(draft.value, true, 1).name].join(' / '))
watch(() => `${session.identity?.tenant.id}:${session.identity?.principal.id}`, async () => {
  const run = ++generation
  loaded.value = false; busy.value = false; message.value = ''; error.value = false
  draft.value = { revision: 0, leaf: { name: '', icon: '' }, levels: [] }
  await load(run)
}, { immediate: true })
async function load(run = generation) {
  if (busy.value) return
  busy.value = true; message.value = ''; error.value = false
  try {
    const response = await api('/settings/work-vocabulary')
    if (!response.ok) throw new Error('Vocabulary could not be read.')
    const value = await response.json() as WorkVocabulary
    if (run !== generation) return
    draft.value = { ...value, levels: value.levels.length ? value.levels : [{ name: '', icon: '' }, { name: '', icon: '' }] }
    vocabulary.accept(value)
    loaded.value = true
  } catch (e) { if (run === generation) { error.value = true; message.value = e instanceof Error ? e.message : 'Vocabulary could not be read.' } }
  finally { if (run === generation) busy.value = false }
}
async function save() {
  if (!editable.value || !loaded.value || busy.value) return
  const run = generation, body = JSON.stringify(draft.value)
  busy.value = true; message.value = ''; error.value = false
  try {
    const response = await api('/settings/work-vocabulary', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body })
    const value = await response.json()
    if (run !== generation) return
    if (!response.ok) throw new Error(typeof value.error === 'string' ? value.error : 'Vocabulary was not saved.')
    draft.value = value; vocabulary.accept(value); message.value = 'Workspace names saved.'
  } catch (e) { if (run === generation) { error.value = true; message.value = e instanceof Error ? e.message : 'Vocabulary was not saved.' } }
  finally { if (run === generation) busy.value = false }
}
function keys(e: KeyboardEvent) {
  if (e.key === 'Enter' && (mac ? e.metaKey : e.ctrlKey)) { e.preventDefault(); void save() }
  if (e.key === 'Escape' && e.target instanceof HTMLElement && e.target.matches('input,select')) { e.preventDefault(); e.stopPropagation(); e.target.blur() }
}
</script>
<template>
  <SettingsCard title="Work vocabulary" icon="tree" anchor="work-vocabulary">
    <template #lead>Create a work item; nesting decides its name. Only work children make a parent. Names belong to the workspace, independently of appearance.</template>
    <div class="vocabulary" @keydown="keys">
      <div v-if="editable" class="actions" aria-label="Vocabulary actions">
        <button class="btn sm" type="button" :disabled="!loaded || busy || draft.levels.length >= 32" @click="draft.levels.push({ name: '', icon: '' })"><AppIcon name="plus" :size="14" />Add level</button>
        <button class="btn sm" type="button" :disabled="busy" @click="load()">Reload</button>
        <button class="btn sm primary" type="button" :disabled="!loaded || busy" @click="save">Save names <KeyCap k="mod" /><KeyCap k="enter" /></button>
      </div>
      <fieldset v-if="editable" :disabled="!loaded || busy">
        <div class="vocab-row"><label for="work-leaf-name">Leaf name</label><input id="work-leaf-name" v-model="draft.leaf.name" class="field" maxlength="60" placeholder="Ticket" /><select v-model="draft.leaf.icon" class="field" aria-label="Leaf icon"><option value="">Default icon</option><option v-for="icon in WORK_ICONS" :key="icon" :value="icon">{{ icon }}</option></select></div>
        <div v-for="(level, i) in draft.levels" :key="i" class="vocab-row"><label :for="`work-level-${i + 1}`">Parent level {{ i + 1 }}</label><input :id="`work-level-${i + 1}`" v-model="level.name" class="field" maxlength="60" :placeholder="workLevel({ ...draft, levels: [] }, false, i + 1).name" /><select v-model="level.icon" class="field" :aria-label="`Parent level ${i + 1} icon`"><option value="">Default icon</option><option v-for="icon in WORK_ICONS" :key="icon" :value="icon">{{ icon }}</option></select></div>
      </fieldset>
      <dl v-else class="read-only-levels">
        <div><dt>Leaf name</dt><dd>{{ workLevel(draft, true, 1).name }}</dd></div>
        <div v-for="(_, i) in draft.levels" :key="i"><dt>Parent level {{ i + 1 }}</dt><dd>{{ workLevel(draft, false, i + 1).name }}</dd></div>
      </dl>
      <p v-if="!editable" class="set-note">Only admins change these names. Everyone sees them.</p>
      <button v-if="error && !editable" type="button" class="btn sm" @click="load()">Try again</button>
      <p class="feedback" :class="{ error }" role="status">{{ message || 'Blank names and icons use stable defaults. Leaves always use the leaf name.' }}</p>
      <p class="preview">{{ preview }}</p>
    </div>
  </SettingsCard>
</template>
<style scoped>
.read-only-levels { display: grid; gap: 12px; margin: 14px 0; }.read-only-levels > div { display: flex; justify-content: space-between; gap: 16px; }.read-only-levels dt { color: var(--ink-2); }.read-only-levels dd { margin: 0; }
.actions { display: flex; flex-wrap: wrap; gap: 8px; }
.feedback { min-height: 3em; margin: 10px 0; color: var(--ink-2); font-size: 13px; overflow-wrap: anywhere; }
.feedback.error { color: var(--danger); }
fieldset { border: 0; padding: 0; margin: 14px 0 0; display: grid; gap: 12px; }
.vocab-row { display: grid; grid-template-columns: minmax(8em, 1fr) minmax(0, 2fr) minmax(7em, 1fr); gap: 10px; align-items: center; }
.vocab-row label { color: var(--ink-2); font-size: 13px; }
.vocab-row .field { min-width: 0; width: 100%; min-height: 36px; }
.preview { margin: 14px 0 0; color: var(--ink-3); font-size: 13px; overflow-wrap: anywhere; }
@media (max-width: 600px) { .vocab-row { grid-template-columns: minmax(0, 2fr) minmax(0, 1fr); }.vocab-row label { grid-column: 1 / -1; }.vocab-row .field { min-height: 44px; }.actions .btn { min-height: 44px; } }
</style>
