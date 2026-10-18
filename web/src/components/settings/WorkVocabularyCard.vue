<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { api } from '../../lib/api'
import { can } from '../../lib/authz'
import { useSession } from '../../stores/session'
import { useWorkVocabulary } from '../../stores/workVocabulary'
import { vocabularyChain, vocabularyRows, workIconChoice, type WorkVocabulary } from '../../lib/workVocabulary'
import AppIcon from '../AppIcon.vue'
import IconChoice from './IconChoice.vue'
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
// The last levels read or saved here. Agent names share this revision: when they
// save, the levels come back unchanged and this draft adopts the new revision.
let base: WorkVocabulary | null = null
const sameLevels = (a: WorkVocabulary, b: WorkVocabulary) => JSON.stringify([a.leaf, a.levels]) === JSON.stringify([b.leaf, b.levels])
watch(() => vocabulary.value, next => {
  if (base && loaded.value && next.revision > draft.value.revision && sameLevels(next, base)) { base = next; draft.value.revision = next.revision }
})
// Top level first, the leaf last: rows and the preview follow the nesting.
const rows = computed(() => vocabularyRows(draft.value))
const chain = computed(() => vocabularyChain(draft.value))
watch(() => `${session.identity?.tenant.id}:${session.identity?.principal.id}`, async () => {
  const run = ++generation
  loaded.value = false; busy.value = false; message.value = ''; error.value = false
  draft.value = { revision: 0, leaf: { name: '', icon: '' }, levels: [] }; base = null
  await load(run)
}, { immediate: true })
async function load(run = generation) {
  if (busy.value) return
  busy.value = true; message.value = ''; error.value = false
  try {
    const response = await api('/settings/work-vocabulary')
    if (!response.ok) throw new Error('Vocabulary could not be read.')
    const read = await response.json() as WorkVocabulary
    if (run !== generation) return
    // A read overtaken by an Agent names save shows that newer revision instead.
    const value = vocabulary.accept(read)
    draft.value = { revision: value.revision, leaf: { ...value.leaf }, levels: value.levels.length ? value.levels.map(level => ({ ...level })) : [{ name: '', icon: '' }, { name: '', icon: '' }] }
    base = value
    loaded.value = true
  } catch (e) { if (run === generation) { error.value = true; message.value = e instanceof Error ? e.message : 'Vocabulary could not be read.' } }
  finally { if (run === generation) busy.value = false }
}
async function save() {
  if (!editable.value || !loaded.value || busy.value) return
  // Without lead the server keeps the agent names (AEON-791).
  const run = generation, body = JSON.stringify({ revision: draft.value.revision, leaf: draft.value.leaf, levels: draft.value.levels })
  busy.value = true; message.value = ''; error.value = false
  try {
    const response = await api('/settings/work-vocabulary', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body })
    const value = await response.json()
    if (run !== generation) return
    if (!response.ok) throw new Error(typeof value.error === 'string' ? value.error : 'Vocabulary was not saved.')
    const fresh = vocabulary.accept(value as WorkVocabulary)
    draft.value = { revision: fresh.revision, leaf: { ...fresh.leaf }, levels: fresh.levels.map(level => ({ ...level })) }
    base = fresh; message.value = 'Workspace names saved.'
  } catch (e) { if (run === generation) { error.value = true; message.value = e instanceof Error ? e.message : 'Vocabulary was not saved.' } }
  finally { if (run === generation) busy.value = false }
}
function keys(e: KeyboardEvent) {
  if (e.key === 'Enter' && (mac ? e.metaKey : e.ctrlKey)) { e.preventDefault(); void save() }
  if (e.key === 'Escape' && e.target instanceof HTMLElement && e.target.matches('input')) { e.preventDefault(); e.stopPropagation(); e.target.blur() }
}
</script>
<template>
  <SettingsCard title="Work vocabulary" icon="tree" anchor="work-vocabulary">
    <template #lead>Create a work item; nesting decides its name. Only work children make a parent. Levels are listed as they nest, top level first, the leaf last. Names belong to the workspace, independently of appearance.</template>
    <div class="vocabulary" @keydown="keys">
      <div v-if="editable" class="actions" aria-label="Vocabulary actions">
        <button class="btn sm" type="button" :disabled="!loaded || busy || draft.levels.length >= 32" @click="draft.levels.push({ name: '', icon: '' })"><AppIcon name="plus" :size="14" />Add level</button>
        <button class="btn sm" type="button" :disabled="busy" @click="load()">Reload</button>
        <button class="btn sm primary" type="button" :disabled="!loaded || busy" @click="save">Save names <KeyCap k="mod" /><KeyCap k="enter" /></button>
      </div>
      <fieldset v-if="editable" :disabled="!loaded || busy">
        <div v-for="row in rows" :key="row.key" class="vocab-row" :style="{ '--depth': Math.min(row.depth, 4) }">
          <label :for="row.leaf ? 'work-leaf-name' : `work-level-${row.index + 1}`">{{ row.label }}</label>
          <input :id="row.leaf ? 'work-leaf-name' : `work-level-${row.index + 1}`" v-model="row.level.name" class="field" maxlength="60" :placeholder="row.placeholder" />
          <IconChoice v-model="row.level.icon" :label="row.leaf ? 'Leaf icon' : `${row.label} icon`" :fallback="row.fallback" />
        </div>
      </fieldset>
      <dl v-else class="read-only-levels">
        <div v-for="(row, i) in rows" :key="row.key" :style="{ '--depth': Math.min(row.depth, 4) }"><dt>{{ row.label }}</dt><dd><AppIcon :name="workIconChoice(chain[i].icon, row.fallback)" :size="14" /><span>{{ chain[i].name }}</span></dd></div>
      </dl>
      <p v-if="!editable" class="set-note">Only admins change these names. Everyone sees them.</p>
      <button v-if="error && !editable" type="button" class="btn sm" @click="load()">Try again</button>
      <p class="feedback" :class="{ error }" role="status">{{ message || 'Blank names and icons use stable defaults. Leaves always use the leaf name.' }}</p>
      <p class="preview"><template v-for="(step, i) in chain" :key="i"><span v-if="i"> / </span><span><AppIcon :name="workIconChoice(step.icon, 'layers')" :size="13" />{{ step.name }}</span></template></p>
    </div>
  </SettingsCard>
</template>
<style scoped>
.read-only-levels { display: grid; gap: 12px; margin: 14px 0; min-width: 0; }
.read-only-levels > div { display: flex; justify-content: space-between; gap: 16px; min-width: 0; }
.read-only-levels dt { color: var(--ink-2); flex-shrink: 0; padding-left: calc(var(--depth) * 12px); }
.read-only-levels dd { display: flex; align-items: flex-start; gap: 6px; margin: 0; min-width: 0; }
.read-only-levels dd svg { flex-shrink: 0; margin-top: 3px; color: var(--ink-3); }
.read-only-levels dd span { min-width: 0; overflow-wrap: anywhere; }
.actions { display: flex; flex-wrap: wrap; gap: 8px; }
.feedback { min-height: 3em; margin: 10px 0; color: var(--ink-2); font-size: 13px; overflow-wrap: anywhere; }
.feedback.error { color: var(--danger); }
fieldset { border: 0; padding: 0; margin: 14px 0 0; display: grid; gap: 12px; }
/* Each level sits one light step in from the one above it; only the label moves, so names and icons stay in two columns. */
.vocab-row { display: grid; grid-template-columns: minmax(11em, 1fr) minmax(0, 2fr) minmax(9em, 1fr); gap: 10px; align-items: center; }
.vocab-row label { padding-left: calc(var(--depth) * 12px); color: var(--ink-2); font-size: 13px; overflow-wrap: anywhere; }
.vocab-row .field { min-width: 0; width: 100%; min-height: 36px; }
.preview { margin: 14px 0 0; color: var(--ink-3); font-size: 13px; overflow-wrap: anywhere; }
.preview svg { display: inline-block; margin-right: 5px; vertical-align: -2px; }
@media (max-width: 600px) {
  .vocab-row { grid-template-columns: minmax(0, 1fr) minmax(9.5em, 11em); }
  .vocab-row label { grid-column: 1 / -1; padding-left: calc(var(--depth) * 10px); }
  .vocab-row .field { min-height: 44px; }
  .actions .btn { min-height: 44px; }
}
</style>
