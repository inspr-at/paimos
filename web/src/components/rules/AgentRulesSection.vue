<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import RuleColumn, { type ColumnSet } from './RuleColumn.vue'
import RuleDetail from './RuleDetail.vue'
import { accountName, getProjects, listNodes, type ProjectSummary } from '../../lib/api'
import { can } from '../../lib/authz'
import { getMembers } from '../../lib/access'
import { toast } from '../../lib/toast'
import { useSession } from '../../stores/session'
import {
  HARNESS_LABEL, HARNESSES, LAYER_LABEL, LAYERS, ROLE_LABEL, ROLES, RULES_BUDGET, RulesError,
  applyEnabled, blankRule, calendarVersion, createLayer, createSet, diffRules, duplicateRule, getSet, validVersion,
  groupState, heldIdentities, isUuid, layerInColumn, listLayers, listSets, listVersions, mergeQuery,
  mergeRules, normalizeRule, publishBlock, publishSet, resetAvailability, restoreSet, rulesEqual,
  rulesMessage, saveDraft, scopeFor, scopeRank, touchRule, validateDraft, writeBlock,
  type AgentMode, type AgentRule, type Caller, type HarnessName, type LayerName, type MergedRules,
  type RoleName, type RuleContext, type RuleLayer, type RulePatch, type RuleSet, type RuleSnapshot,
} from '../../lib/rules'

interface Working {
  remote: RuleSet
  name: string
  rules: AgentRule[]
  versions: RuleSnapshot[] | null
  versionId: string
  collapsed: boolean
}
interface Bundle { layer: RuleLayer; sets: Working[] }

const session = useSession()
const model = ref<Bundle[]>([])
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const filter = ref('')
const projectId = ref('')
const personId = ref('')
const role = ref<RoleName>('builder')
const harness = ref<HarnessName>('cursor')
const agentId = ref('')
const taskId = ref('')
const mode = ref<AgentMode>('role')
const projects = ref<ProjectSummary[]>([])
const people = ref<{ id: string; name: string }[]>([])
const agents = ref<{ id: string; name: string }[]>([])
const tasks = ref<{ id: string; title: string }[]>([])
const contextNote = ref('')
const selection = ref<{ setId: string; identity: string } | null>(null)
const previewOpen = ref(false)
const publishOpen = ref(false)
const restoreOpen = ref(false)
const removeOpen = ref(false)
const publishSetId = ref('')
const merged = ref<MergedRules | null>(null)
const projectNote = ref('')

const context = computed<RuleContext>(() => ({
  projectId: projectId.value, personId: personId.value, agentId: agentId.value,
  role: role.value, harness: harness.value, taskId: taskId.value,
}))
const caller = computed<Caller | null>(() => {
  const identity = session.identity
  if (!identity) return null
  return { id: identity.principal.id, kind: identity.principal.kind === 'agent' ? 'agent' : 'person', allows: (permission, project) => can(permission, project) }
})
const personOptions = computed(() => {
  const list = [...people.value]
  const identity = session.identity
  if (identity && !list.some(person => person.id === identity.principal.id)) list.unshift({ id: identity.principal.id, name: accountName(identity) })
  return list
})
const tenantName = computed(() => session.identity?.tenant.name ?? 'Workspace')

watch(() => session.identity?.principal.id, id => { if (id && !personId.value) personId.value = id }, { immediate: true })

function workingOf(setId: string): { bundle: Bundle; set: Working } | null {
  for (const bundle of model.value) {
    const set = bundle.sets.find(item => item.remote.id === setId)
    if (set) return { bundle, set }
  }
  return null
}
function displayRules(set: Working): AgentRule[] {
  if (!set.versionId || !set.versions) return set.rules
  return set.versions.find(version => version.version === set.versionId)?.rules ?? set.rules
}
function dirty(set: Working) {
  return !set.versionId && (set.name !== set.remote.name || !rulesEqual(set.rules, set.remote.rules))
}
const dirtyCount = computed(() => model.value.reduce((count, bundle) => count + bundle.sets.filter(dirty).length, 0))
function columnRank(column: LayerName) {
  if (column === 'agent') return mode.value === 'task' ? 5 : mode.value === 'named' ? 4 : 3
  return scopeRank({ layer: column })
}
function heldFor(column: LayerName) {
  const flat = model.value.flatMap(bundle => bundle.sets.map(set => ({ scope: bundle.layer.scope, rules: displayRules(set) })))
  return heldIdentities(flat, context.value, columnRank(column))
}
function bundlesFor(column: LayerName) {
  return model.value.filter(bundle => layerInColumn(bundle.layer.scope, column, context.value, mode.value))
}
function intendedScope(column: LayerName) {
  return scopeFor(column, context.value, mode.value)
}
function columnSets(column: LayerName): ColumnSet[] {
  return bundlesFor(column).flatMap(bundle => bundle.sets.map(set => ({
    id: set.remote.id,
    name: set.versionId ? (set.versions?.find(version => version.version === set.versionId)?.name ?? set.name) : set.name,
    dirty: dirty(set),
    readOnly: !!set.versionId,
    collapsed: set.collapsed,
    versionId: set.versionId,
    versions: set.versions?.map(version => ({ version: version.version })) ?? null,
    rules: displayRules(set),
    baseline: set.remote.rules,
    writable: !writeBlock(caller.value, bundle.layer.scope),
    writeReason: writeBlock(caller.value, bundle.layer.scope),
  })))
}
const columns = computed(() => LAYERS.map((column, index) => {
  const bundles = bundlesFor(column)
  const scope = intendedScope(column)
  const block = 'scope' in scope ? writeBlock(caller.value, scope.scope) : scope.error
  return {
    column, number: index + 1, title: LAYER_LABEL[column], hint: hint(column),
    sets: columnSets(column), held: [...heldFor(column)],
    hasLayer: bundles.length > 0,
    canAdd: bundles.length > 0 && !writeBlock(caller.value, bundles[0]!.layer.scope),
    createLabel: `Add ${LAYER_LABEL[column].toLowerCase()} rules`,
    createReason: bundles.length > 0 ? null : block,
  }
}))
function hint(column: LayerName) {
  if (column === 'company') return tenantName.value
  if (column === 'project') return projects.value.find(project => project.id === projectId.value)?.title ?? 'Choose a project'
  if (column === 'person') return personOptions.value.find(person => person.id === personId.value)?.name ?? 'Choose a person'
  if (mode.value === 'role') return `Role · ${ROLE_LABEL[role.value]}`
  if (mode.value === 'named') return agents.value.find(agent => agent.id === agentId.value)?.name ?? 'Choose an agent'
  return tasks.value.find(task => task.id === taskId.value)?.title ?? 'Choose a task'
}
const selected = computed(() => {
  if (!selection.value) return null
  const located = workingOf(selection.value.setId)
  if (!located) return null
  const rule = displayRules(located.set).find(item => item.identity === selection.value?.identity)
  if (!rule) return null
  return { ...located, rule }
})
const selectedLock = computed(() => {
  const current = selected.value
  if (!current) return ''
  const column = current.bundle.layer.scope.layer
  if (heldFor(column).has(current.rule.identity)) return 'A higher layer locks this rule, so switching it here does not turn it off.'
  if (current.rule.strength === 'locked') return 'Locked rules stay on and do not expire.'
  return ''
})
const deleteReason = computed(() => {
  const current = selected.value
  if (!current) return null
  if (current.set.versionId) return 'Published versions are read-only.'
  if (current.rule.strength === 'locked') return 'Locked rules stay in the set.'
  return writeBlock(caller.value, current.bundle.layer.scope)
})
const resetState = computed(() => {
  const rule = selected.value?.rule
  if (!rule) return { show: false, available: false, reason: '' }
  const answer = resetAvailability(rule)
  return answer.available ? { show: true, available: true, reason: 'Original wording is available.' } : { show: answer.show, available: false, reason: answer.reason }
})
const publishChoices = computed(() => model.value.flatMap(bundle => bundle.sets.map(set => ({
  id: set.remote.id, name: set.name, layer: LAYER_LABEL[bundle.layer.scope.layer],
  reason: publishBlock(caller.value, bundle.layer.scope), dirty: dirty(set),
}))))
const publishTarget = computed(() => publishChoices.value.find(choice => choice.id === publishSetId.value) ?? null)
const publishChanges = computed(() => {
  const located = publishSetId.value ? workingOf(publishSetId.value) : null
  if (!located) return []
  const published = located.set.versions?.find(version => version.version === located.set.remote.published_version)
  return diffRules(published?.rules ?? [], located.set.remote.rules)
})
const meter = computed(() => merged.value ? Math.min(100, merged.value.byte_size / RULES_BUDGET * 100) : 0)

function toWorking(remote: RuleSet): Working {
  return { remote, name: remote.name, rules: remote.rules.map(normalizeRule), versions: null, versionId: '', collapsed: false }
}
async function load() {
  loading.value = true
  error.value = ''
  try {
    const { layers } = await listLayers()
    const bundles = await Promise.all(layers.map(async layer => {
      const listed = await listSets(layer.id)
      const sets = await Promise.all(listed.sets.map(set => getSet(set.id)))
      return { layer, sets: sets.map(toWorking) }
    }))
    model.value = bundles
  } catch (cause) {
    error.value = rulesMessage(cause)
  } finally {
    loading.value = false
  }
}
async function loadContext() {
  try {
    const page = await getProjects(false)
    projects.value = page.items
    if (!projectId.value && page.items[0]) projectId.value = page.items[0].id
  } catch { projectNote.value = 'Projects could not be loaded.' }
  if (!can('members.read')) {
    contextNote.value = 'Other people and named agents are listed only with permission to see members.'
    return
  }
  try {
    const members = await getMembers()
    people.value = members.people.filter(person => person.status === 'active').map(person => ({ id: person.principal_id, name: person.name }))
    agents.value = members.agents.map(agent => ({ id: agent.principal_id, name: agent.name }))
  } catch { contextNote.value = 'People and agents could not be loaded.' }
}
watch([mode, projectId], async () => {
  tasks.value = []
  if (mode.value !== 'task' || !projectId.value) return
  try {
    const page = await listNodes({ within: projectId.value, kind: ['task'], limit: 50 })
    tasks.value = page.items.map(item => ({ id: item.id, title: item.title }))
  } catch { contextNote.value = 'Tasks could not be loaded.' }
})

function editable(column: LayerName) {
  return bundlesFor(column).flatMap(bundle => writeBlock(caller.value, bundle.layer.scope) ? [] : bundle.sets.filter(set => !set.versionId))
}
function toggleLayer(column: LayerName) {
  const sets = editable(column)
  const held = heldFor(column)
  const enabled = groupState(sets.flatMap(set => set.rules), held) !== 'on'
  for (const set of sets) set.rules = applyEnabled(set.rules, enabled, held)
}
function toggleSet(setId: string) {
  const located = workingOf(setId)
  if (!located || located.set.versionId || writeBlock(caller.value, located.bundle.layer.scope)) return
  const held = heldFor(located.bundle.layer.scope.layer)
  located.set.rules = applyEnabled(located.set.rules, groupState(located.set.rules, held) !== 'on', held)
}
function toggleRule(setId: string, identity: string) {
  const located = workingOf(setId)
  if (!located || located.set.versionId || writeBlock(caller.value, located.bundle.layer.scope)) return
  const held = heldFor(located.bundle.layer.scope.layer)
  located.set.rules = located.set.rules.map(rule => rule.identity === identity && canFlipRule(rule, held) ? touchRule(rule, { enabled: !rule.enabled }) : rule)
}
function canFlipRule(rule: AgentRule, held: Set<string>) {
  return rule.strength !== 'locked' && !held.has(rule.identity)
}
function changeRule(patch: RulePatch) {
  const current = selected.value
  if (!current || current.set.versionId || writeBlock(caller.value, current.bundle.layer.scope)) return
  const next = touchRule(current.rule, patch)
  if (next.identity !== current.rule.identity && current.set.rules.some(rule => rule.identity === next.identity)) {
    error.value = 'That identity is already used in this set.'
    return
  }
  current.set.rules = current.set.rules.map(rule => rule.identity === current.rule.identity ? next : rule)
  selection.value = { setId: current.set.remote.id, identity: next.identity }
}
function addRule(setId: string) {
  const located = workingOf(setId)
  if (!located || writeBlock(caller.value, located.bundle.layer.scope)) return
  const rule = blankRule(located.set.rules.map(item => item.identity))
  located.set.rules = [...located.set.rules, rule]
  located.set.collapsed = false
  selection.value = { setId, identity: rule.identity }
}
function duplicate() {
  const current = selected.value
  if (!current || current.set.versionId) return
  const copy = duplicateRule(current.rule, current.set.rules.map(rule => rule.identity))
  current.set.rules = [...current.set.rules, copy]
  selection.value = { setId: current.set.remote.id, identity: copy.identity }
}
function removeRule() {
  const current = selected.value
  if (!current || deleteReason.value) return
  current.set.rules = current.set.rules.filter(rule => rule.identity !== current.rule.identity)
  selection.value = null
  removeOpen.value = false
}
async function addSet(column: LayerName, name: string) {
  const bundle = bundlesFor(column)[0]
  if (!bundle || writeBlock(caller.value, bundle.layer.scope)) return
  const issue = validateDraft(name, [])
  if (issue) { error.value = issue; return }
  try {
    const created = await createSet(bundle.layer.id, name)
    const full = await getSet(created.id)
    bundle.sets = [...bundle.sets, toWorking(full)]
    notice.value = `Added “${name}”.`
  } catch (cause) { error.value = rulesMessage(cause) }
}
async function createColumn(column: LayerName) {
  const scope = intendedScope(column)
  if ('error' in scope) { error.value = scope.error; return }
  const block = writeBlock(caller.value, scope.scope)
  if (block) { error.value = block; return }
  try {
    const layer = await createLayer(scope.scope)
    model.value = [...model.value, { layer, sets: [] }]
    notice.value = `${LAYER_LABEL[column]} rules can take a set now.`
  } catch (cause) { error.value = rulesMessage(cause) }
}
function rename(setId: string, name: string) {
  const located = workingOf(setId)
  if (!located || located.set.versionId) return
  located.set.name = name
}
function collapse(setId: string) {
  const located = workingOf(setId)
  if (located) located.set.collapsed = !located.set.collapsed
}
async function history(setId: string) {
  const located = workingOf(setId)
  if (!located || located.set.versions) return
  try { located.set.versions = (await listVersions(setId)).versions }
  catch (cause) { error.value = rulesMessage(cause) }
}
function showVersion(setId: string, version: string) {
  const located = workingOf(setId)
  if (!located) return
  located.set.versionId = version
}
function discard() {
  for (const bundle of model.value) for (const set of bundle.sets) {
    set.name = set.remote.name
    set.rules = set.remote.rules.map(normalizeRule)
  }
  notice.value = 'Unsaved edits were discarded.'
}
async function save() {
  saving.value = true
  error.value = ''
  try {
    for (const bundle of model.value) for (const set of bundle.sets) {
      if (!dirty(set)) continue
      const issue = validateDraft(set.name, set.rules)
      if (issue) { error.value = issue; return }
      try {
        const saved = await saveDraft(set.remote.id, { expected_revision: set.remote.revision, name: set.name, rules: set.rules })
        set.remote = saved
        set.name = saved.name
        set.rules = saved.rules.map(normalizeRule)
      } catch (cause) {
        if (cause instanceof RulesError && cause.code === 'revision_conflict') {
          try { set.remote = await getSet(set.remote.id) } catch { /* keep the previous revision and the draft */ }
          error.value = rulesMessage(cause)
          return
        }
        throw cause
      }
    }
    notice.value = 'Draft saved.'
    toast('Draft saved')
  } catch (cause) { error.value = rulesMessage(cause) }
  finally { saving.value = false }
}
async function openPreview() {
  const query = mergeQuery(context.value)
  if ('error' in query) { error.value = query.error; return }
  try {
    merged.value = await mergeRules(query.query)
    previewOpen.value = true
    error.value = ''
  } catch (cause) { error.value = rulesMessage(cause) }
}
function openPublish() {
  const choice = publishChoices.value.find(item => item.id === selected.value?.set.remote.id && !item.reason) ?? publishChoices.value.find(item => !item.reason)
  if (!choice) return
  publishSetId.value = choice.id
  publishOpen.value = true
  void history(choice.id)
}
async function confirmPublish() {
  const located = workingOf(publishSetId.value)
  if (!located) return
  const block = publishBlock(caller.value, located.bundle.layer.scope)
  if (block) { error.value = block; return }
  if (dirty(located.set)) { error.value = 'Save the draft before publishing. Publish uses the saved draft.'; return }
  const version = calendarVersion()
  if (!validVersion(version)) { error.value = 'Could not build a version.'; return }
  saving.value = true
  try {
    await publishSet(located.set.remote.id, { expected_revision: located.set.remote.revision, version })
    const fresh = await getSet(located.set.remote.id)
    located.set.remote = fresh
    located.set.name = fresh.name
    located.set.rules = fresh.rules.map(normalizeRule)
    located.set.versions = null
    located.set.versionId = ''
    publishOpen.value = false
    notice.value = `Published ${version}.`
    toast('Rules published')
  } catch (cause) {
    if (cause instanceof RulesError && cause.code === 'revision_conflict') {
      try { located.set.remote = await getSet(located.set.remote.id) } catch { /* draft stays */ }
    }
    error.value = rulesMessage(cause)
  } finally { saving.value = false }
}
function openRestore() {
  const current = selected.value
  if (!current?.set.versionId) return
  restoreOpen.value = true
}
async function confirmRestore() {
  const current = selected.value
  if (!current?.set.versionId) return
  const block = publishBlock(caller.value, current.bundle.layer.scope)
  if (block) { error.value = block; return }
  if (dirty(current.set)) { error.value = 'Save or discard the draft before restoring.'; return }
  const newVersion = calendarVersion()
  saving.value = true
  try {
    await restoreSet(current.set.remote.id, { expected_revision: current.set.remote.revision, version: current.set.versionId, new_version: newVersion })
    const fresh = await getSet(current.set.remote.id)
    current.set.remote = fresh
    current.set.name = fresh.name
    current.set.rules = fresh.rules.map(normalizeRule)
    current.set.versionId = ''
    current.set.versions = null
    restoreOpen.value = false
    notice.value = `Restored as ${newVersion}.`
  } catch (cause) { error.value = rulesMessage(cause) }
  finally { saving.value = false }
}
function onKey(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  if (previewOpen.value || publishOpen.value || restoreOpen.value || removeOpen.value) {
    previewOpen.value = publishOpen.value = restoreOpen.value = removeOpen.value = false
    return
  }
  selection.value = null
}
onMounted(() => {
  window.addEventListener('keydown', onKey)
  void load()
  void loadContext()
})
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <section class="rules-page" aria-labelledby="agent-rules-title">
    <header class="rules-head">
      <div>
        <h2 id="agent-rules-title">Agent rules</h2>
        <p class="lede">Company wins, then the project, then the person, then the agent. A locked rule stays on in lower layers.</p>
      </div>
      <div class="actions">
        <button v-if="dirtyCount" type="button" class="btn sm ghost" @click="discard">Discard</button>
        <button type="button" class="btn sm" :disabled="!dirtyCount || saving" @click="save">Save</button>
        <button type="button" class="btn sm" @click="openPreview"><AppIcon name="book" :size="14" />Preview</button>
        <button type="button" class="btn sm primary" :disabled="!publishChoices.some(choice => !choice.reason) || saving" @click="openPublish"><AppIcon name="upload" :size="14" />Publish</button>
      </div>
    </header>
    <p v-if="!publishChoices.some(choice => !choice.reason) && !loading" class="hint">{{ publishChoices[0]?.reason ?? 'Nothing to publish yet.' }}</p>
    <p v-if="dirtyCount" class="chip teal draft-count">{{ dirtyCount }} unsaved {{ dirtyCount === 1 ? 'draft' : 'drafts' }}</p>

    <div class="context" role="group" aria-label="Who the preview is for">
      <p class="tenant"><span>Workspace</span><strong>{{ tenantName }}</strong></p>
      <label>Project
        <select v-model="projectId" class="field"><option v-for="project in projects" :key="project.id" :value="project.id">{{ project.title }}</option></select>
      </label>
      <label>Person
        <select v-model="personId" class="field"><option v-for="person in personOptions" :key="person.id" :value="person.id">{{ person.name }}</option></select>
      </label>
      <label>Role
        <select v-model="role" class="field"><option v-for="item in ROLES" :key="item" :value="item">{{ ROLE_LABEL[item] }}</option></select>
      </label>
      <label>Harness
        <select v-model="harness" class="field"><option v-for="item in HARNESSES" :key="item" :value="item">{{ HARNESS_LABEL[item] }}</option></select>
      </label>
      <label>Agent scope
        <select v-model="mode" class="field">
          <option value="role">Role</option>
          <option value="named">Named agent</option>
          <option value="task">Task</option>
        </select>
      </label>
      <label v-if="mode !== 'role'">Named agent
        <select v-model="agentId" class="field"><option value="">Choose</option><option v-for="agent in agents" :key="agent.id" :value="agent.id">{{ agent.name }}</option></select>
      </label>
      <label v-if="mode === 'task'">Task
        <select v-model="taskId" class="field"><option value="">Choose</option><option v-for="task in tasks" :key="task.id" :value="task.id">{{ task.title }}</option></select>
      </label>
      <label class="filter">Filter
        <input v-model="filter" class="field" type="search" placeholder="Filter rules">
      </label>
    </div>
    <p v-if="projectNote || contextNote" class="hint">{{ projectNote || contextNote }}</p>
    <p v-if="projectId && !isUuid(projectId)" class="hint">This project cannot be used for a preview until it has a workspace id.</p>

    <div class="meter" :class="{ measured: !!merged }">
      <span>{{ merged ? `${merged.byte_size} / ${RULES_BUDGET} bytes` : 'Open preview to measure the merged file.' }}</span>
      <span class="bar" aria-hidden="true"><i :style="{ width: `${meter}%` }"></i></span>
    </div>
    <p v-if="notice" class="hint" role="status">{{ notice }}</p>
    <p v-if="error" class="set-note error" role="alert"><AppIcon name="alert" :size="14" /><span>{{ error }}</span></p>
    <div v-if="loading" class="waiting" role="status" aria-label="Loading rules"><span class="skeleton"></span><span class="skeleton"></span></div>

    <div v-else class="board" :class="{ sided: !!selected }">
      <div class="layers">
        <RuleColumn
          v-for="column in columns" :key="column.column" :number="column.number" :title="column.title" :hint="column.hint"
          :sets="column.sets" :held="column.held" :filter="filter" :selected-id="selection ? `${selection.setId}:${selection.identity}` : ''"
          :create-label="column.createLabel" :create-reason="column.createReason" :has-layer="column.hasLayer" :can-add="column.canAdd"
          @toggle-layer="toggleLayer(column.column)" @toggle-set="toggleSet" @toggle-rule="toggleRule" @select="(setId, identity) => selection = { setId, identity }"
          @add-rule="addRule" @add-set="addSet(column.column, $event)" @create="createColumn(column.column)" @rename="rename" @collapse="collapse"
          @history="history" @version="showVersion"
        />
      </div>
      <div v-if="selected" class="detail-host">
        <RuleDetail
          :layer="LAYER_LABEL[selected.bundle.layer.scope.layer]" :set-name="selected.set.name" :rule="selected.rule"
          :read-only="!!selected.set.versionId || !!writeBlock(caller, selected.bundle.layer.scope)" :lock-note="selectedLock"
          :reset="resetState" :delete-reason="deleteReason" @close="selection = null" @change="changeRule" @duplicate="duplicate"
          @remove="removeOpen = true" @reset="error = resetState.reason"
        />
        <p v-if="selected.set.versionId" class="restore-row">
          <button type="button" class="btn sm" :disabled="!!publishBlock(caller, selected.bundle.layer.scope)" @click="openRestore">Restore as a new version</button>
          <span v-if="publishBlock(caller, selected.bundle.layer.scope)" class="hint">{{ publishBlock(caller, selected.bundle.layer.scope) }}</span>
        </p>
      </div>
    </div>

    <div v-if="previewOpen && merged" class="scrim" @click.self="previewOpen = false">
      <aside class="drawer" role="dialog" aria-modal="true" aria-label="Merged rules">
        <header class="drawer-head"><h2>Merged rules</h2><button type="button" class="icon-btn sm flat" aria-label="Close preview" @click="previewOpen = false"><AppIcon name="close" :size="16" /></button></header>
        <p class="hint">{{ merged.byte_size }} bytes of {{ RULES_BUDGET }}. Version {{ merged.version || 'floor only' }}.{{ dirtyCount ? ' Unsaved drafts are not in this preview.' : '' }}</p>
        <p v-if="merged.floor" class="hint">Safety floor is included.</p>
        <pre class="merge">{{ merged.body }}</pre>
      </aside>
    </div>

    <div v-if="publishOpen" class="scrim" @click.self="publishOpen = false">
      <aside class="drawer" role="dialog" aria-modal="true" aria-label="Publish">
        <header class="drawer-head"><h2>Publish</h2><button type="button" class="icon-btn sm flat" aria-label="Close publish" @click="publishOpen = false"><AppIcon name="close" :size="16" /></button></header>
        <label class="fld">Set
          <select v-model="publishSetId" class="field" @change="history(publishSetId)">
            <option v-for="choice in publishChoices" :key="choice.id" :value="choice.id" :disabled="!!choice.reason">{{ choice.layer }} · {{ choice.name }}{{ choice.reason ? ` — ${choice.reason}` : '' }}</option>
          </select>
        </label>
        <p class="hint">A new version is assigned when you confirm. The previous version stays as it was.</p>
        <p v-if="publishTarget?.dirty" class="hint">Save the draft first. Publish uses the saved draft, not unsaved edits.</p>
        <ul class="changes">
          <li v-if="!publishChanges.length">No difference from the published version.</li>
          <li v-for="change in publishChanges" :key="change.kind + change.label"><span class="kind">{{ change.kind }}</span> {{ change.label }}</li>
        </ul>
        <label class="fld">Note
          <textarea class="field" rows="2" disabled placeholder="Not stored"></textarea>
        </label>
        <p class="hint">The publish request has no note, so a note cannot be saved.</p>
        <button type="button" class="btn primary" :disabled="saving || !!publishTarget?.reason || !!publishTarget?.dirty" @click="confirmPublish">Publish</button>
      </aside>
    </div>

    <div v-if="restoreOpen && selected?.set.versionId" class="scrim" @click.self="restoreOpen = false">
      <aside class="drawer" role="dialog" aria-modal="true" aria-label="Restore version">
        <header class="drawer-head"><h2>Restore</h2><button type="button" class="icon-btn sm flat" aria-label="Close restore" @click="restoreOpen = false"><AppIcon name="close" :size="16" /></button></header>
        <p>Publish the rules from {{ selected.set.versionId }} as a new version. The old version is not rewritten.</p>
        <button type="button" class="btn primary" :disabled="saving" @click="confirmRestore">Restore</button>
      </aside>
    </div>

    <div v-if="removeOpen" class="scrim" @click.self="removeOpen = false">
      <aside class="drawer" role="dialog" aria-modal="true" aria-label="Remove rule">
        <header class="drawer-head"><h2>Remove rule</h2><button type="button" class="icon-btn sm flat" aria-label="Close remove" @click="removeOpen = false"><AppIcon name="close" :size="16" /></button></header>
        <p>The rule leaves this draft. It is removed when you save.</p>
        <button type="button" class="btn danger" @click="removeRule">Remove</button>
      </aside>
    </div>
  </section>
</template>

<style scoped>
.rules-page { display: flex; flex-direction: column; gap: 12px; min-width: 0; max-width: 100%; }
.rules-page :deep(button:disabled) { opacity: 1; color: var(--ink-2); background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); }
.rules-head { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 12px; align-items: flex-start; }
.rules-head h2 { margin: 0; font-size: 20px; }
.lede, .hint, .tenant span { color: var(--ink-2); font-size: 13px; }
.lede { margin: 4px 0 0; max-width: 62ch; }
.actions { display: flex; flex-wrap: wrap; gap: 8px; }
.draft-count { align-self: flex-start; letter-spacing: 0; text-transform: none; }
.context { display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 8px; min-width: 0; }
.context label, .fld { display: grid; gap: 4px; min-width: 0; color: var(--ink-2); font-size: 12px; font-weight: 650; }
.tenant { display: grid; gap: 4px; margin: 0; }
.tenant strong { font-size: 14px; color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.meter { display: flex; align-items: center; gap: 10px; color: var(--ink-2); font-size: 12px; }
.meter .bar { width: min(160px, 40vw); }
.waiting { display: grid; gap: 8px; }
.waiting .skeleton { height: 88px; border-radius: 12px; }
.board { display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; min-width: 0; }
.board.sided { grid-template-columns: minmax(0, 1fr) minmax(280px, 380px); align-items: start; }
.layers { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; min-width: 0; }
.detail-host { display: flex; flex-direction: column; min-width: 0; max-height: calc(100vh - 120px); position: sticky; top: 12px; border-radius: 12px; overflow: hidden; background: var(--surface); box-shadow: 0 0 0 1px var(--line); }
.restore-row { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin: 0; padding: 0 14px 12px; }
.scrim { position: fixed; inset: 0; z-index: 40; display: grid; justify-items: end; background: var(--scrim); }
.drawer { width: min(520px, 100%); height: 100%; overflow: auto; padding: 16px; background: var(--surface); box-shadow: var(--shadow-pop); display: flex; flex-direction: column; gap: 12px; }
.drawer-head { display: flex; align-items: center; gap: 8px; }
.drawer-head h2 { margin: 0; font-size: 16px; }
.drawer-head .icon-btn { margin-left: auto; }
.merge { margin: 0; padding: 12px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; border-radius: 10px; background: var(--surface-2); font: 12.5px/1.5 var(--mono); }
.changes { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.kind { display: inline-block; min-width: 4.5em; color: var(--ink-3); font-size: 11px; font-weight: 700; text-transform: uppercase; }
.fld textarea.field { height: auto; padding: 8px 12px; }
@media (max-width: 1100px) {
  .layers { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .board.sided { grid-template-columns: minmax(0, 1fr); }
  .detail-host { position: fixed; inset: 0; z-index: 30; max-height: none; border-radius: 0; background: var(--scrim); display: grid; justify-items: end; }
}
@media (max-width: 700px) {
  .layers { grid-template-columns: minmax(0, 1fr); }
}
</style>
