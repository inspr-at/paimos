<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import type { Project } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { APIError } from '../../lib/api'
import { can, onAccessRevalidate } from '../../lib/authz'
import { createScope, scopeOwner } from '../../lib/identityScope'
import { DEADLINES, readProjectDeliverySettings, saveProjectDeliverySettings, type Deadline, type DeliverySettings, type SettingsSnapshot } from '../../lib/projectDeliverySettings'
import { useProjectSettingsOverlay } from '../../lib/projectSettings'
import { claimSettingsPopover } from '../../lib/settingsOverlays'
import AppIcon from '../AppIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import SettingsCard from './SettingsCard.vue'
import ProjectSettingsExplainer from './ProjectSettingsExplainer.vue'

const props = defineProps<{ project: Project }>(), session = useSession()
const allowed = computed(() => can('delivery.read', props.project.id)), manage = computed(() => can('delivery.manage', props.project.id))
const scope = createScope(() => scopeOwner(session.identity) ? `${scopeOwner(session.identity)}/${props.project.id}` : ''), reader = scope.lane()
const snapshot = shallowRef<SettingsSnapshot | null>(null), loading = ref(true), busy = ref(false), error = ref(''), feedback = ref('')
const conflict = ref(false)
const workflow = ref(''), deadlineValues = ref<Partial<Record<Deadline, string>>>({})
const addButton = ref<HTMLButtonElement>(), addForm = ref<HTMLFormElement>(), addOpen = ref(false), checkName = ref(''), checkError = ref('')
const register = useProjectSettingsOverlay()
let release: (() => void) | undefined, unregister: (() => void) | undefined
const checks = computed(() => snapshot.value?.effective.required_checks ?? [])
function own(key: 'required_workflow' | 'required_checks' | Deadline) { return snapshot.value ? (key === 'required_workflow' || key === 'required_checks' ? snapshot.value.overrides[key] !== undefined : snapshot.value.overrides.deadlines?.[key] !== undefined) : false }
function fill() { workflow.value = snapshot.value?.effective.required_workflow ?? ''; deadlineValues.value = Object.fromEntries(DEADLINES.map(([key]) => [key, String(snapshot.value?.effective.deadlines?.[key] ?? '')])) }
function load(acceptNewRevision = false) {
  if (!allowed.value || busy.value) return Promise.resolve()
  const id = props.project.id
  return reader.run(({ after, signal }) => after(readProjectDeliverySettings(id, signal), value => {
    const before = snapshot.value
    const dirtyWorkflow = !!before && workflow.value !== before.effective.required_workflow
    const dirtyDeadlines = DEADLINES.filter(([key]) => before && deadlineValues.value[key] !== String(before.effective.deadlines?.[key] ?? '')).map(([key]) => key)
    if (before && value.revision !== before.revision && (dirtyWorkflow || dirtyDeadlines.length) && !acceptNewRevision) {
      conflict.value = true; feedback.value = 'Delivery settings changed. Reload saved settings before saving this draft.'; return
    }
    snapshot.value = value; error.value = ''; conflict.value = false
    if (!before) fill()
    else {
      if (!dirtyWorkflow) workflow.value = value.effective.required_workflow ?? ''
      for (const [key] of DEADLINES) if (!dirtyDeadlines.includes(key)) deadlineValues.value[key] = String(value.effective.deadlines?.[key] ?? '')
      if (acceptNewRevision) feedback.value = 'Saved values reloaded; the draft is kept.'
    }
  }), { failed: cause => { error.value = cause instanceof Error ? cause.message : 'Checks and deadlines could not be loaded.' }, settled: () => { loading.value = false } })
}
function closeAdd(restore = true) { addOpen.value = false; release?.(); release = undefined; unregister?.(); unregister = undefined; if (restore) addButton.value?.focus({ preventScroll: true }) }
async function openAdd() {
  if (!manage.value || busy.value || checks.value.length >= 64) return
  if (addOpen.value) { closeAdd(); return }
  release = claimSettingsPopover(() => closeAdd(false)); checkName.value = ''; checkError.value = ''; addOpen.value = true
  unregister = register({ element: () => addForm.value, close: closeAdd, submit: addCheck })
  await nextTick(); addForm.value?.querySelector<HTMLInputElement>('input')?.focus({ preventScroll: true })
}
function save(overrides: DeliverySettings, success: () => void = () => {}) {
  if (!manage.value || busy.value || !snapshot.value) return Promise.resolve()
  const id = props.project.id, revision = snapshot.value.revision
  reader.cancel(); busy.value = true; feedback.value = ''; conflict.value = false
  return scope.run(({ after, signal }) => after(saveProjectDeliverySettings(id, overrides, revision, signal), value => {
    if (!manage.value) return
    snapshot.value = value; error.value = ''; feedback.value = 'Saved.'; success()
  }), { failed: cause => { feedback.value = cause instanceof Error ? cause.message : 'Nothing changed. Try again.'; conflict.value = cause instanceof APIError && cause.status === 412; if (addOpen.value) checkError.value = feedback.value }, settled: () => { busy.value = false } })
}
function saveWorkflow() {
  if (!snapshot.value || workflow.value === snapshot.value.effective.required_workflow) return
  const value = workflow.value.trim()
  if (!value || value.length > 200) { feedback.value = 'Enter a required workflow name, up to 200 characters.'; return }
  const overrides = structuredClone(snapshot.value.overrides); overrides.required_workflow = value
  void save(overrides, () => { workflow.value = snapshot.value?.effective.required_workflow ?? '' })
}
function saveDeadline(key: Deadline) {
  if (!snapshot.value || deadlineValues.value[key] === String(snapshot.value.effective.deadlines?.[key] ?? '')) return
  const text = deadlineValues.value[key] ?? '', value = Number(text)
  if (!/^\d{1,5}$/.test(text) || !Number.isInteger(value) || value > 10080) { feedback.value = 'Use whole minutes from 0 to 10080. Nothing changed.'; return }
  const overrides = structuredClone(snapshot.value.overrides); overrides.deadlines = { ...overrides.deadlines, [key]: value }
  void save(overrides, () => { deadlineValues.value[key] = String(snapshot.value?.effective.deadlines?.[key] ?? '') })
}
function resetValue(key: 'required_workflow' | 'required_checks' | Deadline) {
  if (!snapshot.value) return
  const overrides = structuredClone(snapshot.value.overrides)
  if (key === 'required_workflow' || key === 'required_checks') delete overrides[key]
  else if (overrides.deadlines) delete overrides.deadlines[key]
  void save(overrides, () => { if (key === 'required_workflow') workflow.value = snapshot.value?.effective.required_workflow ?? ''; else if (key !== 'required_checks') deadlineValues.value[key] = String(snapshot.value?.effective.deadlines?.[key] ?? '') })
}
function addCheck() {
  if (!snapshot.value || busy.value) return
  const value = checkName.value.trim()
  if (!value || value.length > 200 || checks.value.includes(value) || checks.value.length >= 64) { checkError.value = 'Enter a unique check name, up to 200 characters. At most 64 checks.'; return }
  const overrides = structuredClone(snapshot.value.overrides); overrides.required_checks = [...checks.value, value]
  void save(overrides, () => closeAdd())
}
function removeCheck(name: string) { if (!snapshot.value) return; const overrides = structuredClone(snapshot.value.overrides); overrides.required_checks = checks.value.filter(check => check !== name); void save(overrides) }
function reset() { scope.reset(); closeAdd(false); snapshot.value = null; loading.value = true; busy.value = false; error.value = ''; feedback.value = ''; conflict.value = false; workflow.value = ''; deadlineValues.value = {} }
watch(() => [props.project.id, scopeOwner(session.identity), allowed.value], () => { reset(); void load() }, { immediate: true, flush: 'sync' })
watch(manage, value => { if (!value) closeAdd(false) })
const stopAccess = onAccessRevalidate({ reset, refresh: () => { void load() } })
onBeforeUnmount(() => { stopAccess(); scope.dispose(); closeAdd(false) })
</script>

<template>
  <SettingsCard title="Checks and deadlines" icon="check" anchor="checks" variant="project">
    <template #lead>What a change must pass before it merges, and how long it may wait in each step before PAIMOS raises an alert. GitHub’s own branch rules stay as they are.</template>
    <template #aside><ProjectSettingsExplainer id="checks" :project-key="project.routeKey" /></template>
    <p v-if="!manage" class="lock-line"><AppIcon name="lock" :size="13" /><span>People who manage delivery for {{ project.routeKey }} change these.</span></p>
    <div v-if="loading" class="checks-sk" aria-busy="true" aria-label="Loading checks and deadlines"><span v-for="n in 7" :key="n" class="sk" /></div>
    <div v-else-if="error" class="read-error" role="alert"><p>{{ error }}</p><button type="button" class="btn sm" @click="load">Try again</button></div>
    <template v-else-if="snapshot">
      <div class="frows">
        <form class="frow" data-project-field @submit.prevent="saveWorkflow"><label class="fl" for="f-wf"><b>Required workflow</b><small>Its result decides whether a change may merge.</small></label><span class="fctl"><input v-if="manage" id="f-wf" v-model="workflow" class="field w-wf" maxlength="200" :disabled="busy" @blur="saveWorkflow" /><b v-else class="fv">{{ snapshot.effective.required_workflow }}</b><button v-if="own('required_workflow') && manage" type="button" class="link-btn quiet rst" :disabled="busy" @click="resetValue('required_workflow')">Reset</button><span v-else class="origin">{{ own('required_workflow') ? 'this project' : 'workspace default' }}</span></span></form>
        <div class="frow top"><span class="fl"><b>Required checks</b><small>Only these jobs decide; a run red only outside them counts as green.</small></span><span class="fctl"><span class="chips"><span v-for="name in checks" :key="name" class="chk"><AppIcon name="circle-check" :size="12" /><span>{{ name }}</span><button v-if="manage" type="button" class="chk-x" :aria-label="`Remove ${name}`" :disabled="busy" @click="removeCheck(name)"><AppIcon name="close" :size="11" /></button></span><span v-if="!checks.length" class="none-t">Only the workflow’s result counts</span><button v-if="manage" id="add-check" ref="addButton" type="button" class="add" aria-haspopup="dialog" :aria-expanded="addOpen" :disabled="busy || checks.length >= 64" @click="openAdd"><AppIcon name="plus" :size="13" />Add check</button></span><button v-if="own('required_checks') && manage" type="button" class="link-btn quiet rst" :disabled="busy" @click="resetValue('required_checks')">Reset</button><span v-else class="origin">{{ own('required_checks') ? 'this project' : 'workspace default' }}</span></span></div>
      </div>
      <div class="dl-head"><p class="eh">Alert when a change waits longer than</p><ProjectSettingsExplainer id="deadlines" :project-key="project.routeKey" /></div>
      <div class="dl"><form v-for="[key, label] in DEADLINES" :key="key" class="dl-r" data-project-field @submit.prevent="saveDeadline(key)"><label :for="`dl-${key}`">{{ label }}</label><span class="dl-v"><input v-if="manage" :id="`dl-${key}`" v-model="deadlineValues[key]" class="field n" inputmode="numeric" maxlength="5" :disabled="busy" @blur="saveDeadline(key)" /><b v-else>{{ snapshot.effective.deadlines?.[key] }}</b><span class="u">min</span><button v-if="own(key) && manage" type="button" class="link-btn quiet rst" :disabled="busy" @click="resetValue(key)">Reset</button><span v-else class="origin">{{ own(key) ? 'this project' : 'workspace default' }}</span></span></form></div>
      <p class="foot"><AppIcon name="info" :size="13" /><span>0 turns an alert off. Values marked “workspace default” follow the workspace; changing one sets it for this project only. <ProjectSettingsExplainer id="inherit" :project-key="project.routeKey" /></span></p>
      <p class="feedback" role="status" :aria-busy="busy">{{ feedback || (busy ? 'Saving…' : '') }}</p>
      <button v-if="conflict" type="button" class="link-btn" @pointerdown.prevent @click="load(true)">Reload saved settings; keep this draft</button>
    </template>
  </SettingsCard>
  <FloatingPanel v-if="addOpen" :anchor="addButton ?? null" :width="340" :tallest="280" label="Add a required check" @close="restore => closeAdd(restore)"><form id="check-form" ref="addForm" class="check-form" aria-label="Add a required check" @submit.prevent="addCheck"><label>Check name<input v-model="checkName" class="field" maxlength="200" autocomplete="off" :disabled="busy" /></label><p>Exactly as GitHub names it in the pull request’s checks.</p><p class="check-error" role="status">{{ checkError }}</p><div class="acts"><button type="button" class="btn" :disabled="busy" @click="closeAdd()">Cancel</button><button type="submit" class="btn primary" :disabled="busy">Add</button></div></form></FloatingPanel>
</template>

<style scoped>
.lock-line { display:flex; gap:7px; align-items:flex-start; margin-bottom:12px; font-size:12.5px; line-height:1.5; color:var(--ink-3); }.lock-line svg { flex:none; margin-top:2px; }.frows { border-top:1px solid var(--line); }.frow { display:grid; grid-template-columns:210px minmax(0,1fr); align-items:center; column-gap:24px; min-height:66px; padding:12px 2px; border-bottom:1px solid var(--line); }.frow:last-child { border-bottom:0; }.frow.top { align-items:start; }.fl { display:grid; gap:2px; min-width:0; }.fl b { font-size:13.5px; font-weight:600; color:var(--ink); }.fl small { font-size:12.5px; line-height:1.45; color:var(--ink-3); }.frow .field { height:36px; }.fctl { display:flex; flex-wrap:wrap; gap:8px 12px; align-items:center; min-width:0; }.fctl>.origin,.fctl>.rst { min-width:112px; min-height:18px; text-align:left; }.w-wf { max-width:220px; }.fv { font-size:13.5px; font-weight:500; }.origin { font-size:12px; color:var(--ink-3); white-space:nowrap; }.link-btn { border:0; padding:0; background:transparent; color:var(--ink-2); cursor:pointer; font-size:12px; }.chips { display:flex; flex-wrap:wrap; gap:6px; align-items:center; }.chk { display:inline-flex; gap:6px; align-items:center; min-height:28px; padding:0 4px 0 9px; border-radius:999px; background:var(--surface-raised); box-shadow:inset 0 0 0 1px var(--line-2); font:500 12px/1 var(--mono); color:var(--ink); overflow-wrap:anywhere; }.chk>svg { color:var(--teal-ink); flex:none; }.chk-x { display:grid; place-items:center; flex:none; width:22px; height:22px; border:0; padding:0; border-radius:50%; background:transparent; color:var(--ink-3); }.chk-x:hover { background:var(--row-hover); color:var(--ink); }.none-t { font-size:12.5px; color:var(--ink-3); margin-right:4px; }.add { display:inline-flex; align-items:center; gap:6px; min-height:30px; border:0; border-radius:10px; padding:0 10px 0 8px; background:transparent; color:var(--teal-ink); font-size:12.5px; font-weight:600; }.add:hover { background:var(--row-hover); }
.dl-head { display:flex; align-items:center; justify-content:space-between; gap:8px; margin:18px 0 8px; }.eh { font:500 10.5px/1.6 var(--mono); letter-spacing:.12em; text-transform:uppercase; color:var(--ink-3); }.dl { border-top:1px solid var(--line); }.dl-r { display:grid; grid-template-columns:minmax(0,1fr) auto; align-items:center; column-gap:16px; min-height:52px; padding:4px 2px; border-bottom:1px solid var(--line); }.dl-r label { font-size:13px; color:var(--ink); }.dl-v { display:inline-flex; align-items:center; gap:8px; }.dl-v .field.n { width:76px; height:34px; }.dl-v>b { min-width:40px; text-align:right; font-size:13.5px; }.u { width:26px; font-size:12.5px; color:var(--ink-3); }.dl-v .origin,.dl-v .rst { min-width:112px; text-align:left; }.foot { display:flex; align-items:flex-start; gap:8px; margin-top:12px; font-size:12.5px; line-height:1.5; color:var(--ink-3); }.foot>svg { flex:none; margin-top:2px; }.feedback { min-height:18px; margin-top:8px; font-size:12px; line-height:1.5; color:var(--ink-2); overflow-wrap:anywhere; }.read-error { display:grid; gap:12px; padding:16px; background:var(--surface-sunken); border-radius:12px; font-size:13px; }.read-error .btn { justify-self:start; }.checks-sk { display:grid; gap:14px; }.checks-sk .sk { height:38px; }.check-form { display:grid; gap:10px; }.check-form label { display:grid; gap:6px; font-size:13px; font-weight:600; }.check-form p { font-size:12px; line-height:1.5; color:var(--ink-2); }.check-error { min-height:18px; }.acts { display:flex; justify-content:flex-end; gap:8px; }
@container project-settings-body (max-width:600px) { .frow { grid-template-columns:minmax(0,1fr); row-gap:10px; }.dl-head { flex-wrap:wrap; }.dl-r { grid-template-columns:minmax(0,1fr); row-gap:4px; padding:8px 2px; }.dl-v { justify-self:end; }.dl-v .origin,.dl-v .rst { min-width:108px; } }
@media (pointer:coarse), (max-width:600px) { .field,.frow .field,.dl-v .field.n,.link-btn,.add,.btn,.check-form .field { min-height:44px; }.fctl>.origin,.fctl>.rst { min-height:44px; }.chk { min-height:44px; }.chk-x { width:44px; height:44px; }.chk { padding-right:0; } }
</style>
