<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { createPolicyEditor, policyJSON, policyRequest } from '../../lib/policyEditor'
import { createScope } from '../../lib/identityScope'
import { can, onAccessChange, permissionsKnown } from '../../lib/authz'
import { choiceDisabled, compensatePreferences, preferenceScalars, readPreferences, selectorFor, writePreferences, type PreferenceLevelName, type PreferenceMutation, type PreferenceSnapshot, type Selector } from '../../lib/policyModels'
import PolicyEditorFrame from './PolicyEditorFrame.vue'
import PolicyEditorActions from './PolicyEditorActions.vue'
const props = defineProps<{ owner: string; person: boolean }>()
const level = ref<PreferenceLevelName>('person'), project = ref(''), kind = ref(''), mode = ref<'row' | 'scalars'>('row')
const projects = ref<{ id: string; title: string; key: string }[]>([]), projectError = ref('')
const scope = createScope(() => props.owner)
const editor = createPolicyEditor<PreferenceSnapshot, PreferenceMutation>(() => props.owner ? `${props.owner}/${level.value}/${project.value}/${kind.value}/${mode.value}` : '', {
  read: signal => readPreferences(level.value, project.value, signal), write: writePreferences, identity: value => `${value.document.person_id}/${value.level}/${value.project}`,
  writeKey: before => `${props.owner}/preferences/${before.document.person_id}/${before.level}/${before.project}`,
  compensate: compensatePreferences,
  saved: (result, undo, mutation) => {
    const runs = result.running_outside?.length ?? 0
    const note = runs ? ` ${runs} existing run${runs === 1 ? '' : 's'} outside the current provider requirement.` : ''
    return (undo ? mutation.unit === 'scalars' ? 'Preference restored. Existing runs keep their stricter requirement.' : 'Work-kind setting restored.' : 'Saved.') + note
  },
})
const { snapshot, draft, busy, loading, needsReload, message, undo, phase } = editor
const document = computed(() => snapshot.value?.document)
const currentLevel = computed(() => document.value?.levels[level.value]), view = computed(() => document.value?.views[level.value])
const currentKind = computed(() => document.value?.kinds.find(row => row.id === kind.value))
const effectiveRow = computed(() => view.value?.rows.find(row => row.kind_id === kind.value))
const lockedAbove = computed(() => !!effectiveRow.value?.locked_by && effectiveRow.value.locked_by !== level.value)
const editable = computed(() => props.person && !!document.value?.person_id && !!document.value.can[`edit_${level.value}`] && (mode.value !== 'row' || !!currentKind.value && currentKind.value.slug !== 'security' && !lockedAbove.value))
const rowDraft = computed(() => draft.value?.unit === 'row' ? draft.value.value : null)
const scalarDraft = computed(() => draft.value?.unit === 'scalars' ? draft.value.value : null)
const displayScalars = computed(() => scalarDraft.value ?? currentLevel.value)
const contextReady = computed(() => level.value !== 'project' || !!project.value && permissionsKnown(project.value) && can('nodes.read', project.value))
const opened = ref(false)
function cancel() { if (!busy.value) { editor.cancel(); opened.value = false } }
async function load() {
  opened.value = false; editor.reset()
  if (!contextReady.value) return
  await editor.load()
  // The initial row is chosen by an explicit picker; no watch can apply a
  // previous draft to a newly loaded kind/person.
}
watch(() => [props.owner, level.value, project.value, kind.value, mode.value, contextReady.value], () => { void load() }, { immediate: true, flush: 'sync' })
watch(() => props.owner, () => {
  scope.reset(); projects.value = []; projectError.value = ''; project.value = ''; kind.value = ''
  if (props.owner && props.person) void scope.run(({ after, signal }) => after(policyRequest('/projects', { signal }).then(r => policyJSON(r)), result => {
    if (!Array.isArray(result.items) || result.items.length > 500) throw new Error('Incomplete projects')
    projects.value = result.items
  }), { failed: () => { projectError.value = 'Visible projects could not be loaded. Default and You remain available.' } })
}, { immediate: true, flush: 'sync' })
const stopAccess = onAccessChange(() => { editor.reset(); scope.reset(); void load() })
onBeforeUnmount(() => { stopAccess(); editor.dispose(); scope.dispose() })
function selectKind(id: string) { kind.value = id }
function edit() {
  if (!editable.value || busy.value || !currentLevel.value) return
  opened.value = true
  if (mode.value === 'scalars') { editor.edit({ unit: 'scalars', value: preferenceScalars(currentLevel.value) }); return }
  if (!kind.value || !effectiveRow.value) return
  const own = currentLevel.value.rows.find(row => row.kind_id === kind.value)
  editor.edit({ unit: 'row', kind_id: kind.value, value: structuredClone(own ?? { kind_id: kind.value, normal: effectiveRow.value.normal.selector, complex: effectiveRow.value.complex.selector, locked: false }) })
}
function updateCell(bucket: 'normal' | 'complex', selector: Selector) {
  if (!rowDraft.value || busy.value) return
  draft.value = { unit: 'row', kind_id: kind.value, value: { ...rowDraft.value, [bucket]: selector } }
}
function cellMode(bucket: 'normal' | 'complex', value: string) {
  if (value === 'auto') { updateCell(bucket, { mode: 'auto' }); return }
  const choice = view.value?.choices.find(item => !choiceDisabled(item, currentKind.value?.slug ?? ''))
  if (choice && (value === 'latest' || value === 'pinned')) updateCell(bucket, selectorFor(choice, value))
}
function chosen(bucket: 'normal' | 'complex') {
  const selector = rowDraft.value?.[bucket]
  if (!selector || selector.mode === 'auto') return ''
  return view.value?.choices.find(choice => selector.mode === 'pinned' ? choice.profile.id === selector.profile_id : choice.profile.family === selector.family && choice.line === selector.line && choice.profile.effort === selector.effort && (!selector.harness || choice.profile.harness === selector.harness))?.profile.id ?? ''
}
function choose(bucket: 'normal' | 'complex', id: string) {
  const selector = rowDraft.value?.[bucket], choice = view.value?.choices.find(item => item.profile.id === id)
  if (!choice || !selector || selector.mode === 'auto' || choiceDisabled(choice, currentKind.value?.slug ?? '')) return
  updateCell(bucket, selectorFor(choice, selector.mode))
}
function rowLock(value: boolean) { if (rowDraft.value && !busy.value) draft.value = { unit: 'row', kind_id: kind.value, value: { ...rowDraft.value, locked: value } } }
function scalar(field: 'residency' | 'residency_locked' | 'prefs_locked', value: string | boolean) {
  if (!scalarDraft.value || busy.value) return
  draft.value = { unit: 'scalars', value: { ...scalarDraft.value, [field]: field === 'residency' ? value || null : value } }
}
function resetRow() {
  if (!editable.value || !currentLevel.value?.rows.some(row => row.kind_id === kind.value) || busy.value || mode.value !== 'row') return
  opened.value = true
  editor.edit({ unit: 'row', kind_id: kind.value, value: null })
}
function submit() { if (editable.value) void editor.submit() }
const resetLabel = computed(() => `Reset this work kind to ${level.value === 'project' ? 'your setting' : 'default'}`)
const displayedRow = (id: string) => {
  if (phase.value === 'saving' && draft.value?.unit === 'row' && draft.value.kind_id === id) return draft.value.value
  return currentLevel.value?.rows.find(row => row.kind_id === id)
}
</script>
<template>
  <div class="preferences-editor">
    <div class="preference-nav" data-testid="preference-navigation">
      <div class="levels" role="group" aria-label="Preference level"><button v-for="name in (['default','person','project'] as const)" :key="name" :aria-pressed="level === name" @click="level = name">{{ name === 'person' ? 'You' : name === 'default' ? 'Default' : 'Project' }}</button></div>
      <label>Project<select v-model="project" aria-label="Preference project" :disabled="level !== 'project'"><option value="">Choose a visible project</option><option v-for="item in projects" :key="item.id" :value="item.id">{{ item.key }} · {{ item.title }}</option></select></label>
      <label>Work kind<select :value="kind" aria-label="Preference work kind" @change="selectKind(($event.target as HTMLSelectElement).value)"><option value="">Choose a work kind</option><option v-for="item in document?.kinds" :key="item.id" :value="item.id">{{ item.label }}</option></select></label>
      <div class="modes" role="group" aria-label="Preference editor mode"><button :aria-pressed="mode === 'row'" @click="mode = 'row'">Models</button><button :aria-pressed="mode === 'scalars'" @click="mode = 'scalars'">Providers &amp; locks</button></div>
    </div>
    <PolicyEditorFrame :active="opened" title="Model preferences" @save="submit" @cancel="cancel" @undo="editor.submit(true)" @edit="edit">
      <template #actions="{ submitKey }"><PolicyEditorActions :editable="editable" :editing="!!draft" :closable="opened" :busy="busy || loading" :reload-required="needsReload" :undoable="!!undo" :submit-key="submitKey" @edit="edit" @save="submit" @cancel="cancel" @undo="editor.submit(true)" @reload="editor.load" /></template>
      <template #status>
        {{ message || (loading ? 'Loading model preferences…' : !contextReady ? 'Choose a visible project; project editing also needs workspace model visibility.' : lockedAbove && mode === 'row' ? `Model choices are locked by ${effectiveRow?.locked_by}.` : currentKind?.slug === 'security' && mode === 'row' ? 'Security review setup is unavailable here. Minimum checks cannot be replaced.' : !editable ? 'Choose a work kind. Changes require the owning source’s read and write permissions.' : 'Preferred models are advisory; minimum review checks still apply.') }}
        <span v-if="scalarDraft"> Existing runs keep stricter provider requirements. Looser choices below a lock are warnings, not permission changes.</span>
      </template>
      <div v-if="draft?.unit === 'row'" class="row-fields">
        <template v-if="rowDraft">
          <div v-for="bucket in (['normal','complex'] as const)" :key="bucket" class="bucket">
            <label>{{ bucket === 'normal' ? 'Normally' : 'If complex' }}<select :aria-label="`${bucket} preference mode`" :value="rowDraft[bucket].mode" :disabled="busy" @change="cellMode(bucket, ($event.target as HTMLSelectElement).value)"><option value="auto">Automatic</option><option value="latest" :disabled="!view?.choices.some(choice => !choiceDisabled(choice, currentKind?.slug ?? ''))">Follow latest</option><option value="pinned" :disabled="!view?.choices.some(choice => !choiceDisabled(choice, currentKind?.slug ?? ''))">Pin a version</option></select></label>
            <label>Model profile<select :aria-label="`${bucket} model profile`" :value="chosen(bucket)" :disabled="busy || rowDraft[bucket].mode === 'auto'" @change="choose(bucket, ($event.target as HTMLSelectElement).value)"><option value="">{{ rowDraft[bucket].mode === 'auto' ? 'Automatic routing' : 'Choose a qualifying profile' }}</option><option v-for="choice in view?.choices" :key="choice.profile.id" :value="choice.profile.id" :disabled="choiceDisabled(choice, currentKind?.slug ?? '')">{{ choice.profile.display_name || choice.profile.model }} · {{ choice.model_version }} · {{ choice.profile.harness }} · {{ choice.profile.effort }}{{ choice.residency_routes === 0 ? ' · no provider route' : choice.review_reason && currentKind?.slug === 'review' ? ` · ${choice.review_reason}` : '' }}</option></select></label>
          </div>
          <label class="check"><input type="checkbox" :checked="rowDraft.locked" :disabled="busy || level === 'project'" @change="rowLock(($event.target as HTMLInputElement).checked)" />Lock this work kind for narrower levels</label>
          <p>Locking an inherited row copies both current bucket selectors. Automatic is an explicit choice; Reset removes the stored row.</p>
        </template>
        <p v-else>Reset removes both buckets and this row’s lock. Providers, section locks and other work kinds stay stored. Save to confirm.</p>
      </div>
      <div v-else-if="mode === 'scalars' && displayScalars" class="scalar-fields">
        <label>Provider requirement<select aria-label="Provider requirement" :value="displayScalars.residency ?? ''" :disabled="!scalarDraft || busy" @change="scalar('residency', ($event.target as HTMLSelectElement).value)"><option value="">Inherit</option><option value="any">Any provider</option><option value="eu">EU-hosted only</option><option value="local">Local only</option></select></label>
        <label class="check"><input type="checkbox" :checked="displayScalars.residency_locked" :disabled="!scalarDraft || busy || level === 'project'" @change="scalar('residency_locked', ($event.target as HTMLInputElement).checked)" />Lock provider requirement</label>
        <label class="check"><input type="checkbox" :checked="displayScalars.prefs_locked" :disabled="!scalarDraft || busy || level === 'project'" @change="scalar('prefs_locked', ($event.target as HTMLInputElement).checked)" />Lock model preferences</label>
        <p>{{ view?.residency.qualifying_routes ?? 0 }} qualifying routes{{ view?.choices_truncated ? ' among the shown choices' : '' }}. <span v-if="!view?.residency.qualifying_routes">No qualifying route today; work will wait.</span></p>
        <p v-if="view?.residency.loosened_lock">Warning: this provider requirement is looser than a broader lock.</p>
      </div>
      <div v-else class="preview">
        <p v-if="effectiveRow"><strong>{{ currentKind?.label }}</strong> · source: {{ effectiveRow.set_by || 'Automatic' }}{{ effectiveRow.locked_by ? ` · locked by ${effectiveRow.locked_by}` : '' }}</p>
        <p v-if="effectiveRow">Normally: {{ effectiveRow.normal.label }} {{ effectiveRow.normal.today_version }}. If complex: {{ effectiveRow.complex.label }} {{ effectiveRow.complex.today_version }}.</p>
        <p v-if="effectiveRow?.warnings.length">{{ effectiveRow.warnings.join(' ') }}</p>
        <p>{{ view?.changes ?? 0 }} changes at this level. Choose Models for both complexity buckets, or Providers &amp; locks for level settings.</p>
      </div>
      <button class="btn reset-row" data-testid="preference-row-reset" :aria-disabled="mode !== 'row' || !editable || busy || !currentLevel?.rows.some(row => row.kind_id === kind)" @click="resetRow">{{ resetLabel }}</button>
      <p>Reset removes both buckets and the selected row’s lock. Hidden archived settings are preserved.</p>
      <p v-if="view?.choices_truncated">Model choices are incomplete; only the first 256 are shown.</p><p v-if="view?.resolution_truncated">The model preview is incomplete. Live routing remains authoritative.</p>
    </PolicyEditorFrame>
    <p v-if="projectError">{{ projectError }}</p>
    <div class="kind-list" aria-label="Work-kind preferences">
      <button v-for="row in view?.rows" :key="row.kind_id" class="kind-row" data-testid="preference-kind-row" :aria-pressed="kind === row.kind_id" @click="selectKind(row.kind_id)">
        <strong>{{ document?.kinds.find(item => item.id === row.kind_id)?.label }}</strong><span>{{ displayedRow(row.kind_id) ? 'Changed here' : 'Inherited' }}</span><span class="kind-summary">Normally: {{ displayedRow(row.kind_id)?.normal.mode ?? row.normal.selector.mode }} · If complex: {{ displayedRow(row.kind_id)?.complex.mode ?? row.complex.selector.mode }}</span>
      </button>
    </div>
  </div>
</template>
<style scoped>
.preference-nav { display: grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap: 12px; margin-top: 12px; }
.levels,.modes { display: grid; grid-template-columns: repeat(3,minmax(0,1fr)); gap: 4px; align-self:end; }
.modes { grid-template-columns: repeat(2,minmax(0,1fr)); }
.levels button,.modes button { height: 40px; font-size: 12px; border: 1px solid var(--line-2); background: transparent; color:var(--ink-2); padding:6px; }
button[aria-pressed="true"] { background:var(--row-selected); color:var(--ink); font-weight:650; }
label { display:grid; gap:5px; font-size:12px; color:var(--ink-2); min-width:0; }
select { width:100%; height:36px; min-width:0; box-sizing:border-box; padding:6px; border:1px solid var(--line-2); border-radius:3px; color:var(--ink); background:var(--surface); }
.bucket { display:grid; gap:10px; }
.row-fields,.scalar-fields { display:grid; grid-template-columns: repeat(2,minmax(0,1fr)); gap:12px; }
.check { display:flex; align-items:center; min-height:36px; }
.check input { flex:none; }
p { margin: 8px 0; font-size:12px; line-height:1.6; color:var(--ink-2); overflow-wrap:anywhere; }
.row-fields > p,.row-fields > .check { grid-column:1/-1; }
.reset-row { font-size:12px; min-height:36px; margin-top:10px; white-space:normal; text-align:left; }
button[aria-disabled="true"] { opacity:.5; cursor:default; }
.kind-list { border-top:1px solid var(--line); }
.kind-row { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:6px 12px; width:100%; height:96px; text-align:left; border:0; border-bottom:1px solid var(--line); background:transparent; color:var(--ink); padding:12px 0; }
.kind-row strong { font-size:13px; line-height:18px; overflow:auto; overflow-wrap:anywhere; max-height:36px; }
.kind-row span { font-size:12px; color:var(--ink-2); }
.kind-summary { grid-column:1/-1; }
@media (max-width:600px) { select,.levels button,.modes button { min-height:44px; } .scalar-fields { grid-template-columns:minmax(0,1fr); } }
</style>
