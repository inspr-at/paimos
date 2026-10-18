<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { APIError, assignProjectGroup, getNode, getProjectGroups, listNodes, undoGroupEvent, updateNode, type NodePatch, type SharedProjectGroup, type WorkNode } from '../../lib/api'
import { can, myProjectRole, myWorkspaceRole } from '../../lib/authz'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { toast } from '../../lib/toast'
import { compareRevision } from '../../lib/liveUpdates'
import { projectSettingsPath, useProjectSettingsOverlay } from '../../lib/projectSettings'
import { claimSettingsPopover } from '../../lib/settingsOverlays'
import { useProjects, type Project } from '../../stores/projects'
import { useProjectGroups } from '../../stores/projectGroups'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import SettingsCard from './SettingsCard.vue'
import ProjectSettingsExplainer from './ProjectSettingsExplainer.vue'

const props = defineProps<{ project: Project }>()
const route = useRoute(), router = useRouter(), projects = useProjects(), projectGroups = useProjectGroups()
const nextStep = computed(() => route.query.new === props.project.id)
function hideNext() { const { new: _new, group_assignment_failed: _failed, ...query } = route.query; void router.replace({ query }) }
const scope = useIdentityScope(() => can('nodes.read', props.project.id)), reads = scope.lane(), groupReads = scope.lane(), ticketReads = scope.lane()
const node = ref<WorkNode | null>(null), name = ref(props.project.title), description = ref('')
const ticketKey = ref(''), ticketError = ref('')
const groups = ref<SharedProjectGroup[]>([]), groupId = ref(''), groupsReady = ref(false), groupError = ref('')
const loading = ref(true), busy = ref(false), loadError = ref('')
const nameInput = ref<HTMLInputElement>()
const writable = computed(() => can('nodes.write', props.project.id))
const groupWritable = computed(() => can('project_groups.write') && groupsReady.value)
const admin = computed(() => writable.value && [myWorkspaceRole()?.key, myProjectRole(props.project.id)?.key].some(role => role === 'owner' || role === 'admin'))
const registerOverlay = useProjectSettingsOverlay()
type Confirmation = { kind: 'frozen' | 'archived' | 'deleted'; snapshot: WorkNode; anchor: HTMLElement }
const confirmation = ref<Confirmation | null>(null), confirmationContent = ref<HTMLElement>(), deleteDialog = ref<HTMLDialogElement>()
const typedKey = ref(''), actionError = ref('')
let unregister: (() => void) | undefined, release: (() => void) | undefined
const frozen = computed(() => props.project.frozen), archived = computed(() => props.project.archived)
const actionTitle = computed(() => `${confirmation.value?.kind === 'frozen' ? 'Freeze' : confirmation.value?.kind === 'archived' ? 'Archive' : 'Delete'} ${props.project.title}?`)
const actionCopy = computed(() => confirmation.value?.kind === 'frozen'
  ? 'Agents, routines and the delivery queue start nothing new. Running agents finish what they are doing. People can still read and edit tickets.'
  : 'Nothing new starts, and it moves to the Archived group, out of everyday lists and search. Running agents finish. Restore it any time.')
function fullDescription(value: WorkNode): string {
  const classic = value.fields?.classic
  return classic && typeof classic === 'object' && typeof (classic as Record<string, unknown>).description === 'string'
    ? (classic as { description: string }).description : value.body
}
function sayError(error: unknown, fallback: string): string {
  return error instanceof APIError && error.status === 412 ? 'The project changed. Reload it before saving; your draft stays here.' : error instanceof Error ? error.message : fallback
}
function apply(saved: WorkNode) {
  if (saved.id !== props.project.id) throw new Error('The response did not match this project. Reload before changing it again.')
  node.value = saved; projects.applyNode(saved)
}
function refreshGroups() {
  const target = props.project.id
  return getProjectGroups().then(result => ({ target, result }))
}
function load(preserveDraft = false) {
  loading.value = true; loadError.value = ''
  const target = props.project.id, before = node.value
  const keepName = preserveDraft && !!before && name.value !== before.title
  const keepDescription = preserveDraft && !!before && description.value !== fullDescription(before)
  void reads.run(({ after }) => after(getNode(target), value => {
    if (target !== props.project.id) return
    if (value.id !== target) throw new Error('The response did not match this project.')
    if (compareRevision(value.updated_at, props.project.nodeUpdatedAt) < 0) throw new Error('The project changed while it was being read. Try again.')
    node.value = value; projects.applyNode(value)
    if (!keepName) name.value = value.title
    if (!keepDescription) description.value = fullDescription(value)
  }), { failed: error => { loadError.value = sayError(error, 'The project could not be read.'); }, settled: () => { loading.value = false } })
  void groupReads.run(({ after }) => after(refreshGroups(), ({ target: readTarget, result }) => {
    if (readTarget !== props.project.id) return
    groups.value = result.items; groupId.value = result.items.find(group => group.project_ids.includes(readTarget))?.id ?? ''; groupsReady.value = true; groupError.value = ''
  }), { failed: () => { groupError.value = 'Shared groups could not be read. Nothing changed.'; groupsReady.value = false } })
  void ticketReads.run(({ after, signal }) => after(listNodes({ within: target, kind: ['work', 'ticket', 'task', 'epic'], sort: '-created_at,-key', limit: 1 }, { signal }), result => {
    if (target !== props.project.id) return
    ticketKey.value = result.items[0]?.key ?? `${props.project.routeKey}-1`; ticketError.value = ''
  }), { failed: () => { ticketKey.value = ''; ticketError.value = 'The ticket example could not be read.' } })
}
watch(() => scope.owner.value, () => {
  node.value = null; name.value = props.project.title; description.value = ''; ticketKey.value = ''; ticketError.value = ''; groups.value = []; groupId.value = ''; groupsReady.value = false; busy.value = false; close(false); load()
}, { immediate: true, flush: 'sync' })
watch(admin, allowed => { if (!allowed) close(false) })
// A disabled field cannot take focus. Wait for the read and write permission
// to settle before fulfilling the header's edit link.
watch([loading, writable, () => route.hash], () => {
  if (loading.value || !writable.value || route.hash !== '#f-name') return
  void nextTick(() => {
    if (!loading.value && writable.value && route.hash === '#f-name') nameInput.value?.focus({ preventScroll: true })
  })
}, { flush: 'post' })
function undoWrite(before: WorkNode, saved: WorkNode, patch: NodePatch, owner: string) {
  if (scope.owner.value !== owner || props.project.id !== before.id) return
  if (!writable.value || busy.value || node.value?.updated_at !== saved.updated_at) { toast('The project changed. Undo was not applied.', { tone: 'error' }); return }
  busy.value = true
  void scope.run(({ after }) => after(updateNode(before.id, patch, { ifUnmodifiedSince: saved.updated_at }), result => {
    apply(result)
    if (patch.title !== undefined) name.value = result.title
    if (patch.body !== undefined) description.value = fullDescription(result)
    toast('Change undone.')
  }), { failed: error => toast(sayError(error, 'Undo failed. Nothing changed.'), { tone: 'error' }), settled: () => { busy.value = false } })
}
function saveField(field: 'name' | 'description') {
  const snapshot = node.value
  if (!snapshot || !writable.value || busy.value || loading.value) return
  const value = field === 'name' ? name.value.trim() : description.value
  const old = field === 'name' ? snapshot.title : fullDescription(snapshot)
  if (value === old) return
  if (field === 'name' && !value) { toast('The project needs a name.', { tone: 'error' }); return }
  if (value.length > (field === 'name' ? 120 : 2000)) { toast(`${field === 'name' ? 'Name' : 'Description'} is too long. Nothing changed.`, { tone: 'error' }); return }
  let patch: NodePatch = field === 'name' ? { title: value } : { body: value }
  let inverse: NodePatch = field === 'name' ? { title: old } : { body: snapshot.body }
  // Imported projects still read their description from this field in the
  // shared header. Update it together with body, retaining every other field.
  if (field === 'description' && snapshot.fields?.classic && typeof snapshot.fields.classic === 'object') {
    patch = { ...patch, fields: { ...snapshot.fields, classic: { ...snapshot.fields.classic as Record<string, unknown>, description: value } } }
    inverse = { ...inverse, fields: snapshot.fields }
  }
  const owner = scope.owner.value
  busy.value = true; reads.cancel()
  void scope.run(({ after }) => after(updateNode(snapshot.id, patch, { ifUnmodifiedSince: snapshot.updated_at }), result => {
    apply(result)
    if (field === 'name') name.value = result.title
    else description.value = fullDescription(result)
    toast(`${field === 'name' ? 'Name' : 'Description'} saved.`, { action: { label: 'Undo', run: () => undoWrite(snapshot, result, inverse, owner) } })
  }), { failed: error => toast(sayError(error, 'The change could not be saved.'), { tone: 'error', action: error instanceof APIError && error.status === 412 ? { label: 'Reload project', run: () => load(true) } : undefined }), settled: () => { busy.value = false } })
}
function chooseGroup(event: Event) {
  const control = event.target as HTMLSelectElement, target = props.project.id, selected = control.value, before = groupId.value, owner = scope.owner.value
  control.value = before
  if (!groupWritable.value || busy.value || selected === before) return
  busy.value = true; groupReads.cancel()
  void scope.run(({ after }) => after(assignProjectGroup(selected || null, [target]), result => {
    groupId.value = selected; control.value = selected
    void projectGroups.load(true)
    toast('Group saved.', { action: result.event_id == null ? undefined : { label: 'Undo', run: () => {
      if (props.project.id !== target || scope.owner.value !== owner) return
      if (!groupWritable.value || busy.value || groupId.value !== selected) { toast('The group changed. Undo was not applied.', { tone: 'error' }); return }
      busy.value = true
      void scope.run(({ after }) => after(undoGroupEvent(result.event_id!), () => {
        groupId.value = before; void projectGroups.load(true); toast('Group change undone.')
      }), { failed: error => toast(sayError(error, 'The group change could not be undone.'), { tone: 'error' }), settled: () => { busy.value = false } })
    } } })
  }), { failed: error => toast(sayError(error, 'The group could not be saved.'), { tone: 'error' }), settled: () => { busy.value = false } })
}
function close(restore = true) {
  const anchor = confirmation.value?.anchor
  deleteDialog.value?.close(); confirmation.value = null; typedKey.value = ''; actionError.value = ''
  release?.(); release = undefined; unregister?.(); unregister = undefined
  if (restore && anchor?.isConnected) anchor.focus({ preventScroll: true })
}
async function show(kind: Confirmation['kind'], anchor: HTMLElement) {
  if (!admin.value || busy.value || !node.value) return
  close(false)
  confirmation.value = { kind, snapshot: node.value, anchor }
  release = claimSettingsPopover(() => close(false))
  unregister = registerOverlay({ element: () => kind === 'deleted' ? deleteDialog.value : confirmationContent.value, close, submit: confirm })
  await nextTick()
  if (confirmation.value?.kind === 'deleted') deleteDialog.value?.showModal()
}
function mutateState(snapshot: WorkNode, state: 'active' | 'frozen' | 'archived') {
  if (!admin.value || busy.value) return
  if (node.value?.updated_at !== snapshot.updated_at) { actionError.value = 'The project changed. Close this confirmation and try again.'; return }
  const owner = scope.owner.value
  busy.value = true; reads.cancel(); actionError.value = ''
  void scope.run(({ after }) => after(updateNode(snapshot.id, { state }, { ifUnmodifiedSince: snapshot.updated_at }), result => {
    apply(result); close()
    toast(state === 'frozen' ? 'Project frozen. Nothing new starts.' : state === 'archived' ? 'Project archived.' : 'Project restored. New work may start.', {
      action: { label: 'Undo', run: () => { if (!admin.value) return; undoWrite(snapshot, result, { state: snapshot.state }, owner) } },
    })
  }), { failed: error => { actionError.value = sayError(error, 'The project could not be changed.'); toast(actionError.value, { tone: 'error' }) }, settled: () => { busy.value = false } })
}
function confirm() {
  const pending = confirmation.value
  // A state patch does not remove a project or its tickets. Keep the approved
  // sheet, but refuse every submission until a revision-checked project
  // deletion operation exists, including keyboard and synthetic submissions.
  if (!pending || pending.kind === 'deleted') return
  mutateState(pending.snapshot, pending.kind)
}
function restore() { if (node.value) mutateState(node.value, 'active') }
onBeforeUnmount(() => close(false))
</script>

<template>
  <section v-if="nextStep" class="next" aria-label="Next step"><span class="next-ic"><AppIcon name="sparkle" :size="16" /></span><div class="next-t"><b>{{ project.title }} is ready.</b><p>Next: link its GitHub repository, so Delivery can count pull requests, CI runs and merges.</p><p v-if="route.query.group_assignment_failed === '1'" class="error">The selected group was not assigned. Choose it below.</p></div><RouterLink class="btn sm primary" :to="{ path: projectSettingsPath(project.routeKey, 'delivery'), query: { link: '1' }, hash: '#repository' }">Next: link a repository <AppIcon name="chevron-right" :size="13" /></RouterLink><button type="button" class="icon-btn sm" aria-label="Hide this step" @click="hideNext"><AppIcon name="close" :size="14" /></button></section>
  <SettingsCard title="Project" icon="folder" anchor="project-general" variant="project">
    <p v-if="!writable" class="ro-note"><AppIcon name="lock" :size="14" /><span>Project admins and maintainers change the name and description.</span></p>
    <div class="frows">
      <div class="frow"><label class="fl" for="f-name"><b id="name-label">Name</b><small id="name-help">Shown in the header, the Projects page and search.</small></label>
        <form v-if="writable" data-project-field tabindex="-1" @submit.prevent="saveField('name')"><input id="f-name" aria-labelledby="name-label" aria-describedby="name-help" ref="nameInput" v-model="name" class="field" maxlength="120" autocomplete="off" :disabled="loading || !node" :readonly="busy" :aria-busy="busy" @blur="saveField('name')"></form><p v-else class="fv">{{ project.title }}</p>
      </div>
      <div class="frow"><span class="fl"><b>Key</b><small>Set when the project was created. <ProjectSettingsExplainer id="key" :project-key="project.routeKey" /></small></span><div class="key-show"><span class="key-badge big">{{ project.routeKey }}</span><span class="key-say"><template v-if="ticketKey">Starts every ticket number: <b class="mono">{{ ticketKey }}</b>.</template><template v-else>{{ ticketError || 'Reading ticket example…' }}</template></span></div></div>
      <div class="frow top"><label class="fl" for="f-desc"><b id="description-label">Description</b><small id="description-help">Shown under the name in the header; Markdown works.</small></label><form v-if="writable" data-project-field tabindex="-1" @submit.prevent="saveField('description')"><textarea id="f-desc" aria-labelledby="description-label" aria-describedby="description-help" v-model="description" class="field ta" rows="3" maxlength="2000" placeholder="What this project is for, in a sentence or two" :disabled="loading || !node" :readonly="busy" :aria-busy="busy" @blur="saveField('description')" /></form><p v-else class="fv">{{ node ? fullDescription(node) || 'No description' : project.description || 'No description' }}</p></div>
      <div class="frow"><label class="fl" for="f-group"><b id="group-label">Group</b><small id="group-help">Shared groups arrange the Projects page for everyone.</small></label><select v-if="can('project_groups.write')" id="f-group" aria-labelledby="group-label" aria-describedby="group-help" class="field" :value="groupId" :disabled="!groupsReady || busy" @change="chooseGroup"><option value="">{{ !groupsReady ? 'Reading shared groups…' : 'No group' }}</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></select><p v-else class="fv">{{ groupsReady ? groups.find(group => group.id === groupId)?.name ?? 'No group' : groupError || 'Reading shared groups…' }}</p></div>
    </div>
    <div class="foot-lines">
      <p v-if="loadError" class="foot error" role="alert">{{ loadError }} <button type="button" class="link-btn" @click="load()">Try again</button></p>
      <p v-if="groupError" class="foot error" role="alert">{{ groupError }} <button type="button" class="link-btn" @click="load()">Try again</button></p>
      <p v-if="writable" class="foot"><AppIcon name="rollback" :size="13" /><span>Changes save when you leave a field. Enter saves the name; <KeyCap k="mod" /><KeyCap k="enter" /> saves a field. Undo sits in the message.</span></p>
      <p class="foot"><AppIcon name="external" :size="13" /><span>Kinds of work and vocabulary are the same in every project. <RouterLink class="link-btn quiet" to="/settings/kinds">Settings › Kinds of work <AppIcon name="chevron-right" :size="12" /></RouterLink></span></p>
    </div>
  </SettingsCard>
  <SettingsCard title="Lifecycle" icon="archive" anchor="lifecycle" variant="project">
    <template #lead>Where the project is in its life. Freeze and Archive can be undone any time.</template>
    <template #aside><ProjectSettingsExplainer id="lifecycle" :project-key="project.routeKey" /></template>
    <p v-if="!admin" class="ro-note"><AppIcon name="lock" :size="14" /><span>Project admins freeze, archive or delete a project.</span></p>
    <div class="life">
      <div class="life-r"><AppIcon name="snowflake" :size="16" /><span class="lbl"><b>{{ frozen ? 'Frozen' : 'Freeze' }}</b><small>Pauses the project: agents, routines and the delivery queue start nothing new. People can still read and edit tickets. Unfreeze any time.</small></span><button v-if="admin" type="button" class="btn sm" :disabled="busy || !node || archived" aria-haspopup="dialog" :aria-expanded="confirmation?.kind === 'frozen'" data-life="freeze" @click="frozen ? restore() : show('frozen', $event.currentTarget as HTMLElement)"><span class="stable-label"><span :class="{ inactive: frozen }">Freeze…</span><span :class="{ inactive: !frozen }">Unfreeze</span></span></button></div>
      <div class="life-r"><AppIcon name="archive" :size="16" /><span class="lbl"><b>{{ archived ? 'Archived' : 'Archive' }}</b><small>Does what Freeze does, and takes it out of everyday lists and search, into the Archived group. Everything stays readable; restore it any time.</small></span><button v-if="admin" type="button" class="btn sm" :disabled="busy || !node" aria-haspopup="dialog" :aria-expanded="confirmation?.kind === 'archived'" data-life="archive" @click="archived ? restore() : show('archived', $event.currentTarget as HTMLElement)"><span class="stable-label"><span :class="{ inactive: archived }">Archive…</span><span :class="{ inactive: !archived }">Restore</span></span></button></div>
      <div class="life-r"><AppIcon name="trash" :size="16" /><span class="lbl"><b>Delete</b><small>Project deletion is unavailable. Freeze or archive it instead.</small></span><button v-if="admin" type="button" class="btn sm danger" :disabled="busy || !node" data-life="delete" aria-haspopup="dialog" :aria-expanded="confirmation?.kind === 'deleted'" @click="show('deleted', $event.currentTarget as HTMLElement)">Delete…</button></div>
    </div>
  </SettingsCard>
  <FloatingPanel v-if="confirmation && confirmation.kind !== 'deleted'" :anchor="confirmation.anchor" :width="340" align="end" :label="actionTitle" cycle class="life-pop" @close="close($event)">
    <div ref="confirmationContent" class="cf" data-project-settings-overlay tabindex="-1"><h3>{{ actionTitle }}</h3><p>{{ actionCopy }}</p><div class="acts"><button type="button" class="btn sm" @click="close()">Cancel <KeyCap k="Esc" /></button><button type="button" class="btn sm primary" :disabled="busy" data-autofocus @click="confirm">{{ confirmation.kind === 'frozen' ? 'Freeze' : 'Archive' }}</button></div><p v-if="actionError" class="error" role="alert">{{ actionError }}</p></div>
  </FloatingPanel>
  <Teleport v-if="confirmation?.kind === 'deleted'" to="body">
    <dialog ref="deleteDialog" class="delete-sheet sheet" data-project-settings-overlay aria-labelledby="delete-project-title" @cancel.prevent="close()">
      <header class="sh-head"><h2 id="delete-project-title">{{ actionTitle }}</h2><button type="button" class="icon-btn sm" aria-label="Close delete sheet" @click="close()"><AppIcon name="close" :size="14" /></button></header>
      <div class="sh-body"><form id="delete-project-form" @submit.prevent="confirm"><p id="delete-unavailable" class="sh-lead" role="status">Project deletion is unavailable.</p><p class="sh-lead">To stop work for a while, <button type="button" class="link-btn" @click="show('frozen', confirmation!.anchor)">freeze</button> or <button type="button" class="link-btn" @click="show('archived', confirmation!.anchor)">archive</button> it.</p><label class="delete-key"><b>Type {{ project.routeKey }} to confirm</b><input id="del-key" v-model="typedKey" class="field mono" autocomplete="off" spellcheck="false" :maxlength="Math.max(project.routeKey.length + 8, 32)" autofocus></label></form><p v-if="actionError" class="error" role="alert">{{ actionError }}</p></div>
      <footer class="sh-foot"><button type="button" class="btn" @click="close()">Cancel <KeyCap k="Esc" /></button><button type="submit" form="delete-project-form" class="btn danger" disabled aria-describedby="delete-unavailable">Delete project <KeyCap k="mod" /><KeyCap k="enter" /></button></footer>
    </dialog>
  </Teleport>
</template>

<style scoped>
.next { display: grid; grid-template-columns: 34px minmax(0, 1fr) auto auto; align-items: center; column-gap: 14px; padding: 14px 12px 14px 16px; border-radius: var(--radius); background: color-mix(in srgb, var(--teal) 7%, var(--surface-raised)); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }.next-ic { display: grid; place-items: center; width: 34px; height: 34px; border-radius: 50%; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }.next-t { min-width: 0; }.next-t b { font-size: 14px; font-weight: 650; color: var(--ink); }.next-t p { margin-top: 2px; font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.frows, .life { border-top: 1px solid var(--line); }
.frow { display: grid; grid-template-columns: 210px minmax(0, 1fr); align-items: center; column-gap: 24px; min-height: 66px; padding: 12px 2px; border-bottom: 1px solid var(--line); }
.frow:last-child, .life-r:last-child { border-bottom: 0; }
.frow.top { align-items: start; }
.fl, .lbl { display: grid; gap: 2px; min-width: 0; }
.fl b, .lbl b { font-size: 13.5px; font-weight: 600; }
.fl small, .lbl small { font-size: 12.5px; line-height: 1.5; color: var(--ink-3); }
form { min-width: 0; } form:focus { outline: none; }
.frow .field { width: 100%; height: 36px; min-width: 0; }
.frow select.field { max-width: 260px; }
.field.ta { height: auto; min-height: 76px; padding: 8px 12px; line-height: 1.5; resize: none; font: inherit; font-size: 13.5px; }
.fv { font-size: 13.5px; color: var(--ink); font-weight: 500; white-space: pre-wrap; overflow-wrap: anywhere; }
.key-show { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; min-width: 0; }
.key-badge.big { height: 28px; font-size: 12.5px; }
.key-say { font-size: 13px; color: var(--ink-2); }.key-say b { color: var(--ink); font-weight: 600; font-size: 12.5px; }
.foot-lines { display: grid; gap: 8px; padding-top: 14px; }
.foot { display: flex; align-items: flex-start; gap: 8px; font-size: 12.5px; line-height: 1.5; color: var(--ink-3); }
.foot > svg { flex: none; margin-top: 2px; }
.link-btn { display: inline-flex; align-items: center; justify-content: center; gap: 2px; border: 0; padding: 0; background: none; color: var(--teal-ink); font-family: inherit; font-size: 12.5px; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; }
.link-btn.quiet { color: var(--ink-2); font-weight: 500; text-decoration-color: var(--line-2); }
.link-btn.quiet:hover { color: var(--teal-ink); text-decoration-color: currentColor; }
.ro-note { display: flex; align-items: center; gap: 8px; margin-bottom: 14px; padding: 8px 12px; border-radius: 10px; background: var(--surface-sunken); font-size: 12.5px; color: var(--ink-2); }
.ro-note > svg { flex: none; }
.life-r { display: grid; grid-template-columns: 28px minmax(0, 1fr) auto; align-items: center; column-gap: 12px; min-height: 70px; padding: 10px 2px; border-bottom: 1px solid var(--line); }
.life-r > svg { justify-self: center; color: var(--ink-3); }
.life-r .btn { min-width: 96px; }.stable-label { display: grid; }.stable-label span { grid-area: 1 / 1; }.stable-label .inactive { visibility: hidden; }
.cf { padding: 10px 10px 8px; }.cf:focus { outline: none; }.cf h3 { font-size: 14px; }.cf p { margin-top: 6px; font-size: 12.5px; line-height: 1.5; }
.acts { display: flex; justify-content: flex-end; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
.error { color: var(--danger); overflow-wrap: anywhere; }
.delete-sheet { position: fixed; inset: 0 0 0 auto; margin: 0; width: min(480px, 100vw); max-width: 100vw; height: 100dvh; max-height: 100dvh; padding: 0; border: 0; background: var(--surface-raised); color: var(--ink); box-shadow: var(--shadow-pop); }
.delete-sheet[open] { display: grid; grid-template-rows: auto minmax(0, 1fr) auto; }.delete-sheet::backdrop { background: var(--scrim); }
.sh-head { display: flex; align-items: flex-start; gap: 12px; padding: max(20px, env(safe-area-inset-top)) 16px 10px 26px; }.sh-head h2 { flex: 1; font: 600 18px/1.3 var(--font); overflow-wrap: anywhere; }
.sh-body { overflow: auto; min-width: 0; padding: 4px 26px 20px; }.sh-body form { display: grid; gap: 18px; padding-top: 8px; }.sh-lead { font-size: 13.5px; line-height: 1.6; color: var(--ink-2); }.sh-lead b { color: var(--ink); }
.delete-key { display: grid; gap: 6px; font-size: 13px; }.delete-key b { font-weight: 600; }.delete-key .field { width: 100%; height: 38px; }
.sh-foot { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; padding: 14px 20px max(14px, env(safe-area-inset-bottom)); border-top: 1px solid var(--line); }
@container project-settings-body (max-width: 600px) {
  .next { grid-template-columns: 34px minmax(0, 1fr) auto; row-gap: 10px; }.next > .btn { grid-column: 2; grid-row: 2; justify-self: start; }.next > .icon-btn { grid-column: 3; grid-row: 1; }
  .frow { grid-template-columns: minmax(0, 1fr); row-gap: 10px; }.frow select.field { max-width: none; }
  .life-r { grid-template-columns: 24px minmax(0, 1fr); row-gap: 10px; }.life-r > .btn { grid-column: 2; justify-self: start; }
}
@media (pointer: coarse), (max-width: 600px) {
  .field, .frow .field, .btn { min-height: 44px; }.frow .field:not(.ta) { height: 44px; }
  .link-btn { min-height: 44px; min-width: 44px; padding: 0 8px; margin: -12px -8px; }
  .sh-head .icon-btn { width: 44px; height: 44px; }.sh-foot .btn { flex: 1 0 auto; }
}
</style>
