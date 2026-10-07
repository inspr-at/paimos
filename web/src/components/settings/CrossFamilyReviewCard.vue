<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import SettingsCard from './SettingsCard.vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import { getProjects, type ProjectSummary } from '../../lib/api'
import { can, onAccessChange } from '../../lib/authz'
import { createScope, scopeOwner } from '../../lib/identityScope'
import { deliveryLanguage } from '../../lib/delivery'
import { familyName, type ReviewFamily } from '../../lib/reviews'
import { readReviewPolicy, resetReviewPolicy, samePolicy, saveReviewPolicy, type ReviewPolicy, type ReviewPolicyMode, type ReviewPolicySettings } from '../../lib/reviewPolicy'
import { toast } from '../../lib/toast'
import { useSession } from '../../stores/session'
import { useProfile } from '../../stores/profile'

const props = defineProps<{ projectId?: string }>()
const session = useSession(), profile = useProfile()
const selected = ref(props.projectId ?? ''), projects = ref<ProjectSummary[]>([])
const de = computed(() => deliveryLanguage(profile.profile?.locale) === 'de')
const owner = () => scopeOwner(session.identity) ? `${scopeOwner(session.identity)}/${selected.value}` : ''
const scope = createScope(owner), reader = scope.lane(), projectScope = createScope(() => scopeOwner(session.identity))
const base = ref<ReviewPolicySettings | null>(null), mode = ref<ReviewPolicyMode | 'inherit'>('other_family'), families = ref<ReviewFamily[]>([])
const loading = ref(true), busy = ref(false), message = ref(''), problem = ref(false), projectsError = ref(false)
const readable = computed(() => can('reviewpolicy.read', selected.value || undefined))
const editable = computed(() => can('reviewpolicy.manage', selected.value || undefined))
const enabled = computed(() => editable.value && !!base.value && !loading.value && !busy.value)
const draft = computed<ReviewPolicy | null>(() => mode.value === 'inherit' ? null : { mode: mode.value, allowed_families: mode.value === 'allowlist' ? [...families.value] : [] })
const dirty = computed(() => !!base.value && !samePolicy(draft.value, base.value.policy ?? (selected.value ? null : base.value.effective)))
const valid = computed(() => mode.value !== 'allowlist' || families.value.length > 0)
const modes: ReviewPolicyMode[] = ['off', 'other_family', 'allowlist']
function label(mode: ReviewPolicyMode) { return (de.value ? { off:'Aus', other_family:'Andere Modellfamilie', allowlist:'Nur diese Familien' } : { off:'Off', other_family:'Another model family', allowlist:'Only these families' })[mode] }
function detail(mode: ReviewPolicyMode) { return (de.value ? { off:'Jede abgeschlossene Prüfung mit Ergebnis ok und gemeldetem Modell zählt, auch aus der Familie des Autors.', other_family:'Die Familie des Prüfers muss sich von der des Autors unterscheiden.', allowlist:'Eine andere Familie und eine der unten angekreuzten.' } : { off:'Any finished review with verdict ok and a reported model counts, even from the author’s own family.', other_family:'The reviewer’s family must differ from the author’s.', allowlist:'Another family, and one of those ticked below.' })[mode] }
const feedback = computed(() => message.value || (loading.value ? de.value ? 'Prüfregel wird geladen…' : 'Loading review rule…' : !valid.value ? de.value ? 'Mindestens eine Familie für „Nur diese Familien“ ankreuzen.' : 'Tick at least one family for “Only these families”.' : dirty.value ? `${de.value ? 'Noch nicht gespeichert' : 'Not saved yet'}: ${mode.value === 'inherit' ? de.value ? 'Voreinstellung des Arbeitsbereichs' : 'Workspace default' : label(mode.value)}${mode.value === 'allowlist' ? ` (${families.value.map(familyName).join(', ')})` : ''}.` : de.value ? 'Gespeicherte Regeln gelten ab dem nächsten Prüfstatus; bereits gemeldete Status bleiben, wie sie sind.' : 'Saved rules apply from the next review status; statuses already posted stay as they are.'))
const preview = computed(() => {
  const policy = draft.value ?? base.value?.workspace
  return policy?.mode === 'off' ? 'cross-family not required (policy off)' : policy?.mode === 'allowlist' ? `cross-family required · allowed: ${policy.allowed_families.join(', ') || '—'}` : 'cross-family required · reviewer family must differ from author'
})
function adopt(value: ReviewPolicySettings) { base.value = value; mode.value = selected.value && !value.policy ? 'inherit' : value.effective.mode; families.value = [...value.effective.allowed_families] }
function clear() { scope.reset(); base.value = null; busy.value = false; loading.value = true; mode.value = 'other_family'; families.value = []; message.value = ''; problem.value = false }
function load() {
  if (!readable.value || busy.value) { loading.value = false; return Promise.resolve() }
  loading.value = true; message.value = ''; problem.value = false
  const id = selected.value
  return reader.run(({ after, signal }) => after(readReviewPolicy(id, signal), value => { if (value.project_id === (id || null)) adopt(value) }), { failed: cause => { problem.value = true; message.value = cause instanceof Error ? cause.message : 'The review rule could not be loaded.' }, settled: () => { loading.value = false } })
}
function discard() { if (base.value && !busy.value) { adopt(base.value); message.value = ''; problem.value = false } }
function save() {
  if (!enabled.value || !dirty.value || !valid.value || !base.value) return Promise.resolve()
  return write(draft.value, false)
}
function write(policy: ReviewPolicy | null, reset: boolean, undo = false) {
  if (!enabled.value || !base.value || (!selected.value && policy === null)) return Promise.resolve()
  const id = selected.value, capturedOwner = owner(), before = base.value, old = before.policy && { mode: before.policy.mode, allowed_families: [...before.policy.allowed_families] }, revision = before.updated_at
  reader.cancel(); busy.value = true; message.value = ''; problem.value = false
  const body = policy && { mode: policy.mode, allowed_families: [...policy.allowed_families] }
  return scope.run(({ after, signal }) => after(body ? saveReviewPolicy(id, body, signal, revision) : resetReviewPolicy(id, signal, revision), value => {
    if (!editable.value || value.project_id !== (id || null)) return
    adopt(value); message.value = de.value ? `Gespeichert. Neue Prüfstatus folgen „${label(value.effective.mode)}“.` : `Saved. New review statuses follow “${label(value.effective.mode)}”.`
    if (reset && old && !undo) toast(de.value ? 'Dieses Projekt folgt wieder dem Arbeitsbereich.' : 'This project follows the workspace again.', { timeout:10_000, action:{ label:de.value ? 'Rückgängig' : 'Undo', run:() => {
      if (capturedOwner !== owner() || !editable.value || busy.value || dirty.value || !base.value || base.value.updated_at !== value.updated_at || base.value.policy !== null) return
      void write(old, false, true)
    } } })
  }), { failed:cause => { problem.value = true; message.value = cause instanceof Error ? cause.message : de.value ? 'Prüfregel wurde nicht gespeichert.' : 'The rule was not saved.' }, settled:() => { busy.value = false } })
}
function changed() { message.value = ''; problem.value = false }
function keys(event: KeyboardEvent) {
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)
  if (event.key === 'Enter' && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && !event.altKey) { event.preventDefault(); void save() }
  if (event.key === 'Escape' && event.target instanceof HTMLElement && event.target.matches('input,select')) { event.preventDefault(); event.stopPropagation(); event.target.blur() }
}
watch(() => props.projectId, id => { selected.value = id ?? '' })
watch([selected, () => scopeOwner(session.identity), readable], () => { clear(); void load() }, { immediate:true, flush:'sync' })
watch(() => scopeOwner(session.identity), () => {
  projectScope.reset(); projects.value = []; projectsError.value = false
  void projectScope.run(({ after }) => after(getProjects(), page => { projects.value = page.items }), { failed:() => { projectsError.value = true } })
}, { immediate:true, flush:'sync' })
const stopAccess = onAccessChange(change => { if (change === 'reset') { clear(); projectScope.reset(); projects.value = [] } else if (!dirty.value && !busy.value) void load() })
onBeforeUnmount(() => { scope.dispose(); projectScope.dispose(); stopAccess() })
</script>
<template>
  <SettingsCard :title="de ? 'Modellübergreifende Prüfung' : 'Cross-family review'" icon="shield" anchor="cross-family-review">
    <template #lead>{{ de ? 'Ob eine Änderung die Prüfung durch ein Modell einer anderen Familie braucht, bevor sie als geprüft gilt. PAIMOS liest die Familie des Autors aus den eigenen Laufaufzeichnungen, nie aus der Angabe eines Builders.' : 'Whether a change needs a review by a model of another family before it counts as reviewed. PAIMOS reads the author’s family from its own run records, never from what a builder claims.' }}</template>
    <template #aside><label class="scope-label" for="review-policy-scope">{{ de ? 'Gilt für' : 'Applies to' }}</label><select id="review-policy-scope" v-model="selected" class="field scope-select" :disabled="busy"><option value="">{{ de ? 'Gesamter Arbeitsbereich' : 'Whole workspace' }}</option><option v-for="project in projects" :key="project.id" :value="project.id">{{ project.key }} · {{ project.title }}</option></select></template>
    <div class="review-policy" :lang="de ? 'de' : 'en'" @keydown="keys">
      <div v-if="editable" class="actions">
        <button class="btn sm primary" type="button" :disabled="!enabled || !dirty || !valid" @click="save"><span class="save-label"><span>{{ busy ? de ? 'Wird gespeichert…' : 'Saving…' : de ? 'Regel speichern' : 'Save rule' }}</span><span aria-hidden="true" class="label-size">{{ de ? 'Wird gespeichert…' : 'Saving…' }}</span><span aria-hidden="true" class="label-size">{{ de ? 'Regel speichern' : 'Save rule' }}</span></span><KeyCap k="mod" /><KeyCap k="enter" /></button>
        <button class="btn sm" type="button" :disabled="!enabled || !dirty" @click="discard">{{ de ? 'Verwerfen' : 'Discard' }}</button>
        <button v-if="selected" class="btn sm ghost reset" type="button" :disabled="!enabled || !base?.policy" @click="write(null, true)">{{ de ? 'Auf Voreinstellung zurücksetzen' : 'Reset to workspace default' }}</button>
      </div>
      <p v-else class="permission"><AppIcon name="lock" :size="14" /><span>{{ de ? 'Zum Ändern wird „Prüfregel verwalten“ benötigt. Agenten können diese Regel lesen, aber nur mit ausdrücklicher Freigabe ändern.' : 'Changing this needs Manage review policy. Agents can read this rule but change it only with an explicit grant.' }}</span></p>
      <p v-if="selected" class="provenance">{{ base?.source === 'project' ? de ? 'Dieses Projekt hat eine eigene Regel.' : 'This project sets its own rule.' : de ? 'Dieses Projekt folgt dem Arbeitsbereich.' : 'This project follows the workspace.' }} <span v-if="base?.updated_at">{{ de ? 'Geändert am' : 'Changed on' }} {{ new Date(base.updated_at).toLocaleDateString(de ? 'de-AT' : 'en-GB') }}.</span></p>
      <fieldset class="rule-set" :disabled="!enabled" @change="changed"><legend>{{ de ? 'Prüfregel' : 'Review rule' }}</legend>
        <div class="modes">
          <label v-if="selected" class="option" :class="{ on: mode === 'inherit' }"><input v-model="mode" type="radio" name="review-policy-mode" value="inherit" /><span><b>{{ de ? 'Voreinstellung des Arbeitsbereichs verwenden' : 'Use the workspace default' }}</b><small>{{ de ? 'Derzeit' : 'Currently' }}: {{ label(base?.workspace.mode ?? 'other_family') }}</small></span></label>
          <label v-for="value in modes" :key="value" class="option" :class="{ on: mode === value }"><input v-model="mode" type="radio" name="review-policy-mode" :value="value" /><span><b>{{ label(value) }} <em v-if="value === 'other_family'">{{ de ? 'Voreinstellung' : 'Default' }}</em></b><small>{{ detail(value) }}</small></span></label>
        </div>
      </fieldset>
      <fieldset class="family-set" :disabled="!enabled || mode !== 'allowlist'" @change="changed"><legend>{{ de ? 'Zugelassene Prüferfamilien' : 'Reviewer families allowed' }}</legend><p class="hint">{{ de ? 'Gilt nur bei „Nur diese Familien“. Mindestens eine ankreuzen. Die Familie des Autors zählt nie.' : 'Used only with “Only these families”. Tick at least one. The author’s own family never counts.' }}</p><div class="families"><label v-for="family in base?.valid_families ?? []" :key="family" class="family"><input v-model="families" class="check-box" type="checkbox" :value="family" /><span>{{ familyName(family) }}<small>{{ family }}</small></span></label></div></fieldset>
      <p class="feedback" :class="{ problem: problem || !valid }" role="status">{{ feedback }}</p>
      <button v-if="problem && !busy" class="btn sm" type="button" @click="load">{{ de ? 'Erneut versuchen' : 'Try again' }}</button>
      <p v-if="projectsError" class="hint" role="status">{{ de ? 'Die Projektliste konnte nicht geladen werden.' : 'The project list could not be loaded.' }}</p>
      <p class="eyebrow">{{ de ? 'Was der Pull Request zeigt' : 'What the pull request shows' }}</p><p class="hint">{{ de ? 'Der Status aeon/review auf GitHub, immer auf Englisch.' : 'The aeon/review status on GitHub, always in English.' }}</p><div class="preview"><AppIcon name="shield" :size="13" /><code>{{ preview }}</code></div>
    </div>
  </SettingsCard>
</template>
<style scoped>
.review-policy { min-width:0; container:review-policy/inline-size; }.scope-label { white-space:nowrap; font-size:12px; color:var(--ink-3); }.scope-select { min-width:0; max-width:24em; width:100%; font-size:13px; }.actions { display:flex; flex-wrap:wrap; align-items:center; gap:8px; }.actions .btn { max-width:100%; }.reset { white-space:normal; height:auto; min-height:28px; text-align:left; }.save-label { display:grid; }.save-label>span { grid-area:1/1; }.label-size { visibility:hidden; pointer-events:none; }
.permission { display:grid; grid-template-columns:auto minmax(0,1fr); gap:8px; padding:10px 12px; border-radius:10px; background:var(--surface-2); font-size:13px; line-height:1.5; color:var(--ink-2); }.permission svg { margin-top:3px; }.provenance { min-height:1.5em; margin:10px 0 0; font-size:12.5px; color:var(--ink-3); }
fieldset { min-width:0; margin:14px 0 0; padding:14px 0 0; border:0; border-top:1px solid var(--line); }legend { float:left; width:100%; padding:0; font-size:13px; font-weight:600; color:var(--ink); }.modes { clear:both; display:grid; gap:2px; padding-top:8px; }.option { display:grid; grid-template-columns:16px minmax(0,1fr); gap:12px; align-items:start; height:72px; box-sizing:border-box; padding:10px 12px; border-radius:10px; cursor:pointer; }.option:hover { background:var(--row-hover); }.option.on { background:var(--row-selected); box-shadow:inset 0 0 0 1px var(--line-2); }.option input { width:16px; height:16px; margin:2px 0 0; accent-color:var(--primary); }.option b { font-size:13.5px; font-weight:600; color:var(--ink); }.option small { display:block; margin-top:2px; font-size:12px; line-height:1.45; color:var(--ink-2); }.option em { margin-left:8px; font:500 10px var(--mono); font-style:normal; text-transform:uppercase; letter-spacing:.1em; color:var(--ink-3); }fieldset:disabled .option,fieldset:disabled .family { cursor:default; }
.hint { clear:both; padding-top:4px; margin:0; font-size:12px; line-height:1.5; color:var(--ink-2); }.families { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:2px 8px; max-width:600px; padding-top:6px; min-height:96px; }.family { display:flex; align-items:center; gap:10px; min-width:0; height:46px; padding:4px 10px; box-sizing:border-box; border-radius:8px; font-size:13px; color:var(--ink); cursor:pointer; }.family:hover { background:var(--row-hover); }.family span { display:grid; min-width:0; }.family small { font:11px/1.3 var(--mono); color:var(--ink-3); overflow-wrap:anywhere; }.family-set:disabled .family { opacity:.5; }.feedback { min-height:3em; margin:10px 0; font-size:13px; line-height:1.5; color:var(--ink-2); overflow-wrap:anywhere; }.problem { color:var(--danger); }.eyebrow { margin:0; }.preview { display:grid; grid-template-columns:auto minmax(0,1fr); align-items:start; gap:8px; min-height:3em; margin-top:8px; color:var(--ink-2); }.preview code { font:12px/1.5 var(--mono); overflow-wrap:anywhere; }
@container review-policy (max-width:480px) { .option { height:108px; }.families { grid-template-columns:repeat(2,minmax(0,1fr)); min-height:144px; } }
@media (pointer:coarse) { .actions .btn { min-height:44px; }.actions kbd { display:none; }.scope-select { min-height:44px; } }
</style>
