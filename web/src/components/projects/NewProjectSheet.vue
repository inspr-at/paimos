<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { APIError, assignProjectGroup, createNode, getKinds, getProjectGroups, type SharedProjectGroup, type WorkNode } from '../../lib/api'
import { getMembers } from '../../lib/access'
import { can } from '../../lib/authz'
import { createFromDraft, draftLanguage, dropDraft, getImportDraft, handedOverAt, IMPORT_COPY, targetDateText, type HeldDraft, type ImportDraft, type OwnerOption } from '../../lib/importDraft'
import { projectKeyProblem, projectSettingsPath, suggestProjectKey } from '../../lib/projectSettings'
import { projectRouteKey } from '../../lib/work'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { toast } from '../../lib/toast'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import ProjectSettingsSheet from '../settings/ProjectSettingsSheet.vue'
import ImportComesAlong from './ImportComesAlong.vue'
import ImportOpenPoints from './ImportOpenPoints.vue'
import OwnerPicker from './OwnerPicker.vue'
import TargetDateField from './TargetDateField.vue'

// draft given (even null): the project comes from a conversation handed over
// from START (AEON-1441). The same sheet then asks only what START cannot
// know: the key, the owner, an optional target date and which open points
// become tickets. null means nothing is held: the one gone message.
const props = defineProps<{ draft?: HeldDraft | null }>()
const emit = defineEmits<{ close: [] }>()
const fromStart = props.draft !== undefined
const router = useRouter(), projects = useProjects(), session = useSession()
// A person without project rights still opens a handoff and is told so; the server decides.
const scope = useIdentityScope(fromStart ? () => true : () => can('nodes.write'))
const name = ref(''), key = ref(''), touched = ref(false), group = ref('')
const kind = ref(''), groups = ref<SharedProjectGroup[]>([]), groupError = ref(''), loadError = ref('')
const busy = ref(false), conflict = ref(''), error = ref('')
const problem = computed(() => conflict.value || (name.value.trim() && projectKeyProblem(key.value) ? (fromStart ? (key.value ? t.value.keyShape : t.value.keyEmpty) : projectKeyProblem(key.value)) : ''))
const valid = computed(() => !!scope.owner.value && !!kind.value && !!name.value.trim() && !projectKeyProblem(key.value) && !conflict.value && !busy.value)
const groupAllowed = computed(() => can('project_groups.read') && can('project_groups.write'))

// From START: the draft, its language and what the person chooses.
type Phase = 'loading' | 'ready' | 'gone' | 'denied' | 'unreadable'
const phase = ref<Phase>(fromStart ? (props.draft ? 'loading' : 'gone') : 'ready')
const lang = ref(draftLanguage(props.draft ?? null))
const t = computed(() => IMPORT_COPY[lang.value])
const draft = ref<ImportDraft | null>(null)
const owner = ref(''), targetDate = ref(''), chosen = ref<number[]>([]), people = ref<OwnerOption[]>([])
const ownerPicker = ref<{ close: () => boolean }>()
const me = computed(() => session.identity?.principal.id ?? '')
const loading = computed(() => phase.value === 'loading')
const startValid = computed(() => phase.value === 'ready' && !!scope.owner.value && !!name.value.trim() && !projectKeyProblem(key.value) && !conflict.value && !!owner.value && !busy.value)
const title = computed(() => fromStart ? t.value.title : 'New project')

function changeName() { if (!touched.value) key.value = suggestProjectKey(name.value); conflict.value = ''; error.value = '' }
function changeKey() { touched.value = true; key.value = key.value.toUpperCase(); conflict.value = ''; error.value = '' }
function load() {
  loadError.value = ''
  void scope.run(({ after }) => after(getKinds(), result => {
    kind.value = result.items.find(item => item.slug === 'project')?.id ?? ''
    if (!kind.value) throw new Error('The workspace has no project kind. Nothing was created.')
  }), { failed: cause => { loadError.value = cause instanceof Error ? cause.message : 'Project setup could not be read.' } })
  if (groupAllowed.value) void scope.run(({ after }) => after(getProjectGroups(), result => { groups.value = result.items; groupError.value = '' }), { failed: () => { groupError.value = 'Groups could not be read. Create without a group, or try again.' } })
}
// People only; without the right to read members the owner is the person themselves.
function loadPeople() {
  const self = session.identity ? [{ id: session.identity.principal.id, name: session.identity.principal.name }] : []
  people.value = self
  if (!can('members.read')) return
  void scope.run(({ after }) => after(getMembers(), members => {
    const others = members.people.filter(person => person.status === 'active' && person.principal_id !== me.value)
      .map(person => ({ id: person.principal_id, name: person.name })).sort((a, b) => a.name.localeCompare(b.name))
    people.value = [...self, ...others]
  }), { failed: () => { people.value = self } })
}
function loadDraft() {
  const held = props.draft
  if (!held) { phase.value = 'gone'; return }
  phase.value = 'loading'
  void scope.run(({ after, signal }) => after(getImportDraft(held, signal), result => {
    lang.value = result.language
    // The person's own create already committed: open the project it made.
    if (result.status === 'created' && result.project) { finishStart(result.project, result.tickets ?? 0, false); return }
    draft.value = result
    name.value = result.title; key.value = suggestProjectKey(result.title); touched.value = false
    owner.value = me.value; targetDate.value = ''
    chosen.value = result.open_points.map((_, index) => index)
    phase.value = 'ready'
  }), { failed: cause => {
    phase.value = cause instanceof APIError && cause.status === 404 ? 'gone' : cause instanceof APIError && cause.status === 403 ? 'denied' : 'unreadable'
  } })
}
watch(() => scope.owner.value, (current, previous) => {
  // Another person or workspace: nothing of this draft stays on screen or in the tab.
  if (previous && current !== previous) { if (fromStart) dropDraft(); emit('close'); return }
  if (!current) return
  if (!fromStart) { load(); return }
  loadPeople(); loadDraft()
}, { immediate: true, flush: 'sync' })

function finish(node: WorkNode, groupFailed = false) {
  projects.addCreatedProject(node)
  toast(groupFailed ? 'Project created, but the group was not assigned. Choose it in General.' : 'Project created.', { tone: groupFailed ? 'error' : 'info' })
  void router.push({ path: projectSettingsPath(projectRouteKey(node.key, node.fields)), query: { new: node.id, ...(groupFailed ? { group_assignment_failed: '1' } : {}) } })
  emit('close')
}
function finishStart(node: WorkNode, tickets: number, announce = true) {
  dropDraft()
  projects.addCreatedProject(node)
  if (announce) toast(t.value.created(tickets))
  void router.push(`/p/${encodeURIComponent(projectRouteKey(node.key, node.fields))}/tickets`)
  emit('close')
}
function submit() {
  if (fromStart) { submitStart(); return }
  if (!valid.value) return
  const snapshot = { name: name.value.trim(), key: key.value, group: groupAllowed.value ? group.value : '', kind: kind.value }
  busy.value = true; error.value = ''; conflict.value = ''
  let created: WorkNode | null = null
  void scope.run(({ after }) => after(createNode({ kind_id: snapshot.kind, title: snapshot.name, body: '', state: 'active', fields: { project_key: snapshot.key }, key_prefix: snapshot.key }), node => {
    if (!node.id || node.kind_id !== snapshot.kind || projectRouteKey(node.key, node.fields) !== snapshot.key) throw new Error('The server returned a different project. Reload Projects before trying again.')
    created = node
    if (!snapshot.group) { finish(node); return }
    if (!can('project_groups.write')) { finish(node, true); return }
    return after(assignProjectGroup(snapshot.group, [node.id]), () => finish(node))
  }), {
    failed: cause => {
      if (created) { finish(created, true); return }
      if (cause instanceof APIError && cause.status === 409) conflict.value = cause.message
      else error.value = cause instanceof Error ? cause.message : 'The project could not be created. Check Projects before retrying.'
    }, settled: () => { busy.value = false },
  })
}
// The choice is captured when the person acts; the answer is used only while
// they and this draft are still the ones on screen (the scope drops it otherwise).
function submitStart() {
  const held = props.draft
  if (!held || !startValid.value) return
  const snapshot = { name: name.value.trim(), key: key.value, owner_id: owner.value, target_date: targetDate.value || null, open_points: [...chosen.value] }
  busy.value = true; error.value = ''; conflict.value = ''
  void scope.run(({ after }) => after(createFromDraft(held, snapshot), result => {
    if (projectRouteKey(result.project.key, result.project.fields) !== snapshot.key && !result.replayed) throw new Error(t.value.failed)
    finishStart(result.project, result.tickets)
  }), {
    failed: cause => {
      // The sheet speaks the conversation's language, so server wording stays out of it.
      if (cause instanceof APIError && cause.status === 404) { phase.value = 'gone'; return }
      if (cause instanceof APIError && cause.status === 409) { conflict.value = t.value.keyTaken; return }
      error.value = cause instanceof APIError && cause.status === 403 ? t.value.denied : t.value.failed
      void nextTick(() => document.getElementById('np-error')?.scrollIntoView({ block: 'nearest' }))
    }, settled: () => { busy.value = false },
  })
}
function close() { if (fromStart) dropDraft(); emit('close') }
const closeInner = () => ownerPicker.value?.close() ?? false
</script>

<template>
  <ProjectSettingsSheet v-if="!fromStart" title="New project" description="Name it and give it a key; the rest lives in its Settings." @close="emit('close')" @submit="submit">
    <form id="np-form" class="np" novalidate @submit.prevent="submit">
      <label class="np-f"><b>Name</b><input id="np-name" v-model="name" class="field" autocomplete="off" maxlength="500" :disabled="busy" autofocus @input="changeName"></label>
      <label class="np-f"><b>Key</b><span class="np-k"><input id="np-key" v-model="key" class="field mono" maxlength="10" autocomplete="off" spellcheck="false" aria-describedby="np-key-h" :aria-invalid="!!problem" :disabled="busy" @input="changeKey"></span><small id="np-key-h" :class="{ bad: problem }">{{ problem || (key ? `Starts every ticket number: ${key}-1, ${key}-2. Suggested from the name; change it now if you like.` : 'Starts every ticket number. Suggested from the name; change it now if you like.') }}</small></label>
      <label class="np-f"><b>Group <span class="faint">· optional</span></b><select id="np-group" v-model="group" class="field" :disabled="busy || !groupAllowed || !!groupError"><option value="">No group</option><option v-for="item in groups" :key="item.id" :value="item.id">{{ item.name }}</option></select><small v-if="!groupAllowed">People with shared-group permissions choose a group.</small><small v-else-if="groupError" class="bad">{{ groupError }}</small></label>
      <p class="sh-note"><AppIcon name="info" :size="13" /><span>Choose the key with care: changing it comes in a later release. After creating it, its Settings open on General with the next step: link a repository.</span></p>
      <p v-if="loadError" class="error" role="alert">{{ loadError }} <button type="button" class="btn sm" @click="load">Try again</button></p>
      <p v-if="error || conflict" class="error" role="alert">{{ error || conflict }}</p>
    </form>
    <template #actions><button type="button" class="btn" @click="emit('close')">Cancel <KeyCap k="Esc" /></button><button id="np-go" type="submit" form="np-form" class="btn primary" :disabled="!valid" :aria-busy="busy">Create project <KeyCap k="mod" /><KeyCap k="enter" /></button></template>
  </ProjectSettingsSheet>

  <ProjectSettingsSheet v-else :title="title" :close-label="t.close" :lang="lang" :escape="closeInner" @close="close" @submit="submit">
    <template #description>
      <p v-if="phase === 'loading' || phase === 'ready'" class="np-from" data-m="from"><template v-if="loading"><span class="sk-row"><span class="skeleton" style="width: 44%" /></span><span class="sk-row"><span class="skeleton" style="width: 64%" /></span></template><template v-else-if="draft">{{ t.from(draft.source?.host ?? 'START', handedOverAt(lang, draft.handed_over_at)) }}</template></p>
    </template>
    <div v-if="phase === 'gone' || phase === 'denied' || phase === 'unreadable'" class="state" role="status">
      <AppIcon :name="phase === 'gone' ? 'clock' : 'info'" :size="20" />
      <h3>{{ phase === 'gone' ? t.gone : phase === 'denied' ? t.denied : t.readFailed }}</h3>
      <p>{{ phase === 'denied' ? t.deniedHint : t.again }}</p>
      <button v-if="phase === 'unreadable'" type="button" class="btn sm" @click="loadDraft">{{ t.retry }}</button>
    </div>
    <form v-else id="np-form" class="np" :class="{ locked: busy }" novalidate @submit.prevent="submit">
      <h3 class="np-sec">{{ t.create }}</h3>
      <label class="np-f"><b>{{ t.name }}</b>
        <span v-if="loading" class="field sk-field"><span class="sk-row"><span class="skeleton" style="width: 64%" /></span></span>
        <input v-else id="np-name" v-model="name" class="field" autocomplete="off" maxlength="500" :disabled="busy" @input="changeName">
      </label>
      <label class="np-f"><b>{{ t.key }}</b><span class="np-k">
        <span v-if="loading" class="field sk-field np-key-sk"><span class="sk-row"><span class="skeleton" style="width: 46%" /></span></span>
        <input v-else id="np-key" v-model="key" class="field mono" maxlength="10" autocomplete="off" spellcheck="false" aria-describedby="np-key-h" :aria-invalid="!!problem" :disabled="busy" @input="changeKey">
      </span><small id="np-key-h" class="key-h" :class="{ bad: problem }">{{ problem || t.keyHelp(loading ? '' : key) }}</small></label>
      <div class="np-row">
        <OwnerPicker ref="ownerPicker" v-model="owner" :people="loading ? people.slice(0, 1) : people" :me="me" :label="t.owner" :you="t.you" :find="t.find" :nobody="t.nobody" :disabled="loading || busy" />
        <TargetDateField v-model="targetDate" :label="t.date" :optional="t.optional" :empty="t.noDate" :clear="t.clearDate" :text="value => targetDateText(lang, value)" :disabled="loading || busy" />
      </div>
      <ImportOpenPoints v-if="loading || (draft?.open_points.length ?? 0) > 0" v-model:chosen="chosen" :points="draft?.open_points ?? []" :loading="loading" :disabled="busy" :label="t.tickets" :all="t.all" :count="t.count" />
      <ImportComesAlong :draft="draft" :loading="loading" :t="t" />
      <p v-if="error" id="np-error" class="error" role="alert">{{ error }}</p>
    </form>
    <template #actions>
      <button v-if="phase === 'gone' || phase === 'denied' || phase === 'unreadable'" id="np-projects" type="button" class="btn primary" @click="close">{{ t.toProjects }}</button>
      <template v-else>
        <button type="button" class="btn" @click="close">{{ t.cancel }} <KeyCap k="Esc" /></button>
        <button id="np-go" type="submit" form="np-form" class="btn primary" :disabled="!startValid" :aria-busy="busy"><span class="btn-label"><span :aria-hidden="busy || undefined">{{ t.go }}</span><span :aria-hidden="!busy || undefined">{{ t.creating }}</span></span><KeyCap k="mod" /><KeyCap k="enter" /></button>
      </template>
    </template>
  </ProjectSettingsSheet>
</template>

<style scoped>
.np { display: grid; gap: 18px; padding-top: 8px; }.np-f { display: grid; gap: 6px; min-width: 0; }.np-f > b { font-size: 13px; font-weight: 600; }.np-f > small { font-size: 12px; line-height: 1.5; color: var(--ink-3); overflow-wrap: anywhere; }#np-key-h { height: 4.5em; overflow: auto; }.np-f .field { width: 100%; height: 38px; min-width: 0; }.np-k { display: flex; align-items: center; gap: 12px; }.np-k .field { width: 170px; max-width: 100%; text-transform: uppercase; letter-spacing: .04em; }.faint { color: var(--ink-3); font-weight: 400; }
.sh-note { display: flex; gap: 8px; padding: 10px 12px; border-radius: 10px; background: var(--surface-sunken); font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }.sh-note svg { flex: none; margin-top: 2px; }.np-f > small.bad, .error { color: var(--danger); }.error { font-size: 12.5px; overflow-wrap: anywhere; }
/* From START (approved design AEON-1441). */
.np-from { min-height: 2lh; text-wrap: balance; }
.np-sec { font: 600 15px/1.35 var(--font); letter-spacing: -.005em; color: var(--ink); }
.np-row { position: relative; display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; }
.np .field:disabled { opacity: 1; color: var(--ink-2); background: var(--surface-2); box-shadow: 0 0 0 1px var(--line); }
.sk-row { display: flex; align-items: center; height: 1lh; }
.sk-field { display: flex; align-items: center; padding: 0 12px; background: var(--surface-2); box-shadow: 0 0 0 1px var(--line); }
.np-key-sk { width: 170px; max-width: 100%; }
.state { display: grid; justify-items: center; align-content: center; gap: 8px; min-height: 100%; padding: 24px 12px 12%; text-align: center; }
.state > svg { color: var(--teal); margin-bottom: 4px; }
.state h3 { font-size: 17px; font-weight: 600; color: var(--ink); text-wrap: balance; }
.state p { font-size: 13.5px; text-wrap: pretty; }
.state .btn { margin-top: 8px; }
.btn.primary[aria-busy="true"] { cursor: progress; }
@media (pointer: coarse), (max-width: 600px) { .np-f .field { min-height: 44px; height: 44px; }.np-k .field, .np-key-sk { width: 100%; }.btn { min-height: 44px; } }
/* Phones have no keyboard to hint at. */
@media (max-width: 600px) { .keycap { display: none; } }
</style>
