<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { AccessError, getProjectMembers, type ProjectMember } from '../../lib/access'
import { can, myPermissions } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { useAccess } from '../../stores/access'
import type { Project } from '../../stores/projects'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import ChoicePicker from '../settings/ChoicePicker.vue'
import RolePicker from './RolePicker.vue'
import { problem, undoing } from './accessText'

// Who can work on one project, and through what: a role on this project, their
// workspace role (which reaches every project), or both. Project roles can be
// given, changed and taken away here; workspace roles change under People.
const props = defineProps<{ project: Project }>()
const access = useAccess()
const rows = ref<ProjectMember[] | null>(null)
const error = ref('')
const manage = computed(() => can('members.manage', props.project.id))
type Filter = 'all' | 'project' | 'workspace'
const filter = ref<Filter>('all')
async function load() {
  error.value = ''
  try { rows.value = await getProjectMembers(props.project.id) } catch (e) { error.value = problem(e, 'The people on this project could not be loaded') }
}
// One row per person or agent (contract v2 #4): the project role when there is
// one, and the workspace role for context.
const people = computed(() => {
  const out = new Map<string, { principal_id: string; name: string; kind: 'person' | 'agent'; avatar_url: string | null; project: ProjectMember['role'] | null; workspace: ProjectMember['role'] | null }>()
  for (const row of rows.value ?? []) {
    const entry = out.get(row.principal_id) ?? { principal_id: row.principal_id, name: row.name, kind: row.kind, avatar_url: row.avatar_url, project: null, workspace: row.workspace_role }
    if (row.via === 'project') entry.project = row.role; else entry.workspace = row.workspace_role ?? row.role
    out.set(row.principal_id, entry)
  }
  return [...out.values()].sort((a, b) => Number(!a.project) - Number(!b.project) || Number(a.kind === 'agent') - Number(b.kind === 'agent') || a.name.localeCompare(b.name))
})
const shown = computed(() => people.value.filter(p => filter.value === 'all' || (filter.value === 'project' ? !!p.project : !p.project)))
const counts = computed(() => ({ all: people.value.length, project: people.value.filter(p => p.project).length, workspace: people.value.filter(p => !p.project).length }))
type Entry = (typeof people.value)[number]

// ---------- Changing ----------
const picker = ref<{ id: string; name: string; current: string | null; anchor: HTMLElement } | null>(null)
const adding = ref<HTMLElement | null>(null)
const busy = ref(false)
// Why the server refused a role change, shown in the open picker.
const roleError = ref('')
watch(picker, () => { roleError.value = '' })
const addChoices = computed(() => [
  ...access.people.filter(p => p.status === 'active' && !people.value.some(e => e.principal_id === p.principal_id && e.project)).map(p => ({ value: p.principal_id, label: p.name, detail: p.workspace_role ? `${p.workspace_role.name} in the workspace` : 'No workspace role' })),
  ...access.agents.filter(a => !a.service && !people.value.some(e => e.principal_id === a.principal_id && e.project)).map(a => ({ value: a.principal_id, label: a.name, detail: 'Agent' })),
])
function pickPerson(id: string) {
  const anchor = adding.value
  adding.value = null
  const name = access.person(id)?.name ?? access.agent(id)?.name ?? ''
  if (anchor) picker.value = { id, name, current: null, anchor }
}
async function choose(roleId: string | null) {
  const target = picker.value
  if (!target || !roleId) return
  const before = target.current
  busy.value = true
  try {
    await access.setProjectRole(props.project.id, target.id, roleId)
    picker.value = null
    await load()
    const name = access.roleById.get(roleId)?.name ?? 'the role'
    toast(before ? `${target.name} is ${name} on ${props.project.title}` : `${target.name} can work on ${props.project.title} as ${name}`, {
      action: { label: 'Undo', run: () => undoing((before ? access.setProjectRole(props.project.id, target.id, before) : access.removeProjectMember(props.project.id, target.id)).then(load), `${target.name}’s access could not be put back`) },
    })
  } catch (e) { roleError.value = problem(e, `${target.name}’s access did not change`) }
  finally { busy.value = false }
}
async function remove(entry: Entry) {
  if (!entry.project) return
  const first = entry.name.split(' ')[0]
  const ok = await confirmAction({
    title: `Remove ${entry.name} from ${props.project.title}?`,
    points: [
      `${first} loses the ${entry.project.name} role on ${props.project.title}.`,
      entry.workspace ? `As ${entry.workspace.name} in the workspace, ${first} still reaches this project with what that role gives.` : `${first} no longer sees ${props.project.title}.`,
      'Their work here stays, still shown as theirs.',
    ],
    confirmLabel: 'Remove from project', danger: true,
  })
  if (!ok) return
  const role = entry.project.id
  try {
    await access.removeProjectMember(props.project.id, entry.principal_id)
    await load()
    toast(`${entry.name} is off ${props.project.title}`, { action: { label: 'Undo', run: () => undoing(access.setProjectRole(props.project.id, entry.principal_id, role).then(load), `${entry.name} could not be put back on ${props.project.title}`) } })
  } catch (e) {
    toast(problem(e, `${entry.name} stays on ${props.project.title}`), { tone: 'error' })
    // 404 or via_workspace: what is shown is out of date.
    if (e instanceof AccessError && (e.status === 404 || e.status === 409)) void load()
  }
}
onMounted(load)
watch(() => props.project.id, load)
</script>

<template>
  <div class="project-access">
    <RouterLink class="back" to="/settings/access/projects"><AppIcon name="arrow-left" :size="13" />All projects</RouterLink>
    <header class="head">
      <span class="key-badge big">{{ project.routeKey }}</span>
      <div class="head-text">
        <h3>{{ project.title }}</h3>
        <p>Who can work on this project. A project role adds to someone’s workspace role here; it never takes anything away.</p>
      </div>
      <button v-if="manage && rows" type="button" class="btn primary sm" @click="adding = $event.currentTarget as HTMLElement"><AppIcon name="plus" :size="13" />Add someone</button>
    </header>
    <div class="seg" role="radiogroup" aria-label="People to show">
      <button type="button" role="radio" :aria-checked="filter === 'all'" @click="filter = 'all'">Everyone<span class="n mono">{{ counts.all }}</span></button>
      <button type="button" role="radio" :aria-checked="filter === 'project'" @click="filter = 'project'">Project role<span class="n mono">{{ counts.project }}</span></button>
      <button type="button" role="radio" :aria-checked="filter === 'workspace'" @click="filter = 'workspace'" data-tip="Their workspace role reaches this project">Workspace<span class="n mono">{{ counts.workspace }}</span></button>
    </div>
    <div v-if="!rows && !error" class="set-skeleton" role="status" aria-label="Loading project access"><span class="skeleton" /><span class="skeleton" /><span class="skeleton" /></div>
    <p v-else-if="error" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}<button type="button" class="btn sm" @click="load">Try again</button></p>
    <ul v-else class="members" :aria-label="`People on ${project.title}`">
      <li v-for="entry in shown" :key="entry.principal_id" class="member">
        <Avatar :id="entry.principal_id" :name="entry.name" :kind="entry.kind" :size="30" />
        <span class="m-text">
          <RouterLink v-if="entry.kind === 'person'" class="m-name" :to="`/settings/access/people/${entry.principal_id}`">{{ entry.name }}</RouterLink>
          <span v-else class="m-name mono">{{ entry.name }}</span>
          <span class="via">
            <template v-if="entry.project && entry.workspace">{{ entry.project.name }} here · {{ entry.workspace.name }} in the workspace</template>
            <template v-else-if="entry.project">{{ entry.project.name }} here · no workspace role</template>
            <template v-else>{{ entry.workspace?.name }} in the workspace, which reaches every project</template>
          </span>
        </span>
        <span class="m-role">
          <button v-if="entry.project && manage" type="button" class="role-btn" aria-haspopup="dialog" :aria-label="`Role of ${entry.name} on ${project.title}: ${entry.project.name}. Change`" @click="picker = { id: entry.principal_id, name: entry.name, current: entry.project.id, anchor: $event.currentTarget as HTMLElement }">{{ entry.project.name }}<AppIcon name="chevron" :size="12" class="chev" /></button>
          <span v-else-if="entry.project" class="tag project">{{ entry.project.name }}</span>
          <span v-else class="tag" data-tip="Change workspace roles under People">Workspace</span>
        </span>
        <span class="m-act">
          <button v-if="entry.project && manage" type="button" class="icon-btn sm flat" :aria-label="`Remove ${entry.name} from ${project.title}`" data-tip="Remove the project role" @click="remove(entry)"><AppIcon name="close" :size="13" /></button>
        </span>
      </li>
    </ul>
    <p v-if="rows && !shown.length" class="empty">{{ filter === 'project' ? 'No one has a role on this project yet.' : 'No one reaches this project through the workspace.' }}</p>
    <ChoicePicker v-if="adding" :anchor="adding" :label="`Add to ${project.title}`" :choices="addChoices" current="" placeholder="Find a person or agent…" @choose="pickPerson" @close="adding = null" />
    <RolePicker
      v-if="picker" :anchor="picker.anchor" :subject="picker.name" :place="project.title" :roles="access.roles" :role-details="access.runtimeRoleDetails" :current="picker.current" :registry="access.registry"
      :mine="myPermissions(project.id)" scope="project" :busy="busy" :can-apply="can('members.manage', project.id)" :error="roleError" @choose="choose" @close="picker = null"
    />
  </div>
</template>

<style scoped>
.project-access { display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; }
.back { display: inline-flex; align-items: center; gap: 6px; justify-self: start; height: 28px; margin-left: -8px; padding: 0 8px; border-radius: 8px; color: var(--ink-2); font-size: 13px; text-decoration: none; }
.back:hover { color: var(--teal-ink); background: var(--row-hover); }
.back:focus-visible { box-shadow: var(--focus-ring); }
.head { display: flex; align-items: flex-start; gap: 12px; }
.key-badge.big { height: 26px; padding: 0 10px; font-size: 12px; }
.head-text { flex: 1; display: grid; gap: 2px; min-width: 0; }
h3 { font: 600 17px/1.3 var(--font); color: var(--ink); }
.head-text p { font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.seg { justify-self: start; }
.n { margin-left: 6px; font-size: 11px; color: var(--ink-3); }
.members { display: grid; margin: 0; padding: 0; list-style: none; }
.member { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto 32px; align-items: center; gap: 12px; min-height: 56px; border-bottom: 1px solid var(--line); }
.m-text { display: grid; gap: 1px; min-width: 0; }
.m-name { color: var(--ink); font-size: 13.5px; font-weight: 600; text-decoration: none; overflow-wrap: anywhere; }
a.m-name:hover { color: var(--teal-ink); }
.m-name.mono { font-size: 12.5px; }
.via { font-size: 12.5px; color: var(--ink-2); }
.role-btn { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; font-weight: 600; }
@media (hover: hover) { .role-btn:hover { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); } }
.role-btn:focus-visible { box-shadow: var(--focus-ring); }
.chev { color: var(--ink-3); }
.tag { display: inline-flex; align-items: center; height: 22px; padding: 0 9px; border-radius: 999px; box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-2); font-size: 12px; }
.tag.project { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font-weight: 600; }
.m-act { display: flex; justify-content: flex-end; }
.empty { padding: 12px 0; font-size: 13px; color: var(--ink-3); }
@media (max-width: 760px) {
  .head { flex-wrap: wrap; }
  .head .btn { width: 100%; justify-content: center; }
  .seg { width: 100%; overflow-x: auto; scrollbar-width: none; }
  .seg button { flex-shrink: 0; white-space: nowrap; }
}
@media (max-width: 600px) {
  .member { grid-template-columns: 30px minmax(0, 1fr) 44px; grid-template-areas: "av text act" ". role role"; row-gap: 4px; padding: 8px 0; }
  .member > :first-child { grid-area: av; }
  .m-text { grid-area: text; }
  .m-role { grid-area: role; }
  .m-act { grid-area: act; }
  .m-act .icon-btn { width: 44px; height: 44px; }
  .role-btn { height: 44px; margin-left: -8px; }
  .back { height: 44px; }
  /* The name reads as a line, but takes a full-height tap. */
  .m-text { position: relative; }
  a.m-name { display: inline-flex; align-items: center; min-height: 44px; margin: -12px 0; }
  .head .btn, .seg button { height: 44px; }
}
</style>
