<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { LAST_OWNER_REASON, effectLine, identityLine, isLastOwner, type Person, type ProjectRole } from '../../lib/access'
import { can, myPermissions } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { toast } from '../../lib/toast'
import { absoluteTime, relativeTime } from '../../lib/work'
import { useAccess } from '../../stores/access'
import { useProjects } from '../../stores/projects'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import ChoicePicker from '../settings/ChoicePicker.vue'
import AccessSheet from './AccessSheet.vue'
import RolePicker from './RolePicker.vue'
import StatusChip from './StatusChip.vue'
import { deactivatePoints, problem, undoing } from './accessText'

// One person: who they are, their workspace role and what it lets them do, the
// projects they are given and with which role, the classic identities linked to
// them, and whether they can sign in. Actions I may not take are not offered.
const props = defineProps<{ person: Person; focus?: string }>()
const emit = defineEmits<{ close: [] }>()
const access = useAccess()
const projects = useProjects()
const manage = computed(() => can('members.manage'))
const last = computed(() => isLastOwner(props.person))
const role = computed(() => props.person.workspace_role ? access.roleById.get(props.person.workspace_role.id) : undefined)
const first = computed(() => props.person.name.split(' ')[0])
const busy = ref(false)

// ---------- Workspace role ----------
const roleAnchor = ref<HTMLElement | null>(null)
// Why the server refused a role change, shown in the open picker.
const roleError = ref('')
async function chooseWorkspace(roleId: string | null) {
  const before = props.person.workspace_role?.id ?? null
  busy.value = true
  try {
    await access.setWorkspaceRole(props.person.principal_id, roleId)
    roleAnchor.value = null
    const name = access.roles.find(r => r.id === roleId)?.name
    toast(name ? `${props.person.name} is now ${name}` : `${props.person.name} has no workspace role now`, { action: { label: 'Undo', run: () => undoing(access.setWorkspaceRole(props.person.principal_id, before), `${props.person.name}’s role could not be put back`) } })
  } catch (e) { roleError.value = problem(e, `${props.person.name} keeps their role`) }
  finally { busy.value = false }
}

// ---------- Project access ----------
const projectRole = ref<{ project: { id: string; title: string }; current: string | null; anchor: HTMLElement } | null>(null)
watch([roleAnchor, projectRole], () => { roleError.value = '' })
const adding = ref<HTMLElement | null>(null)
const addChoices = computed(() => projects.projects.filter(p => !props.person.project_roles.some(r => r.project_id === p.id)).map(p => ({ value: p.id, label: p.title, hint: p.routeKey })))
function pickProject(projectId: string) {
  const anchor = adding.value
  adding.value = null
  const project = projects.byId(projectId)
  if (project && anchor) projectRole.value = { project: { id: project.id, title: project.title }, current: null, anchor }
}
async function chooseProject(roleId: string | null) {
  const target = projectRole.value
  if (!target || !roleId) return
  busy.value = true
  try {
    await access.setProjectRole(target.project.id, props.person.principal_id, roleId)
    projectRole.value = null
    toast(`${props.person.name} is ${access.roles.find(r => r.id === roleId)?.name ?? 'set'} on ${target.project.title}`)
  } catch (e) { roleError.value = problem(e, `The role on ${target.project.title} did not change`) }
  finally { busy.value = false }
}
async function removeProject(entry: ProjectRole) {
  const ws = props.person.workspace_role
  const ok = await confirmAction({
    title: `Remove ${props.person.name} from ${entry.project_title}?`,
    points: [
      `${first.value} loses the ${entry.role.name} role on ${entry.project_title}.`,
      ws ? `As ${ws.name} in the workspace, ${first.value} still has what that role gives on every project.` : `${first.value} no longer sees ${entry.project_title} at all.`,
      'Their work there stays, still shown as theirs.',
    ],
    confirmLabel: `Remove from ${entry.project_title}`, danger: true,
  })
  if (!ok) return
  try {
    await access.removeProjectMember(entry.project_id, props.person.principal_id)
    toast(`${props.person.name} is off ${entry.project_title}`, { action: { label: 'Undo', run: () => undoing(access.setProjectRole(entry.project_id, props.person.principal_id, entry.role.id), `${props.person.name} could not be put back on ${entry.project_title}`) } })
  } catch (e) { toast(problem(e, `${props.person.name} stays on ${entry.project_title}`), { tone: 'error' }) }
}

// ---------- Aliases and lifecycle ----------
async function unlink(alias: { principal_id: string; name: string }) {
  const ok = await confirmAction({ title: `Unlink ${alias.name} from ${props.person.name}?`, points: [`Its classic history shows as ${alias.name} again, not as ${first.value}.`, 'It goes back to the imported identities, where it can be linked again.'], confirmLabel: 'Unlink', danger: true })
  if (!ok) return
  try { await access.unlinkAlias(props.person.principal_id, alias.principal_id); toast(`${alias.name} is unlinked`, { action: { label: 'Undo', run: () => undoing(access.linkAlias(props.person.principal_id, alias.principal_id), `${alias.name} could not be linked again`) } }) }
  catch (e) { toast(problem(e, `${alias.name} stays linked`), { tone: 'error' }) }
}
async function deactivate() {
  const ok = await confirmAction({ title: `Deactivate ${props.person.name}?`, points: deactivatePoints(props.person.name), confirmLabel: `Deactivate ${first.value}`, danger: true })
  if (!ok) return
  try { await access.deactivate(props.person.principal_id); toast(`${props.person.name} is deactivated`, { action: { label: 'Reactivate', run: () => undoing(access.reactivate(props.person.principal_id), `${props.person.name} stays deactivated`) } }) }
  catch (e) { toast(problem(e, `${props.person.name} stays active`), { tone: 'error' }) }
}
async function reactivate() {
  try { await access.reactivate(props.person.principal_id); toast(`${props.person.name} can sign in again`) }
  catch (e) { toast(problem(e, `${props.person.name} stays deactivated`), { tone: 'error' }) }
}
const projectsSection = ref<HTMLElement>()
onMounted(async () => {
  void projects.load()
  if (props.focus === 'projects') { await nextTick(); projectsSection.value?.scrollIntoView({ block: 'start' }); projectsSection.value?.querySelector<HTMLElement>('button')?.focus() }
})
</script>

<template>
  <AccessSheet :title="person.name" :label="`${person.name}, access`" @close="emit('close')">
    <template #head>
      <div class="head">
        <Avatar :id="person.principal_id" :name="person.name" :size="44" />
        <div class="head-text">
          <h2 class="title">{{ person.name }}</h2>
          <p class="sub">{{ identityLine(person) }}<template v-if="person.identity === 'inspr_id' && person.email"> · INSPR ID</template></p>
          <p class="facts">
            <StatusChip :tone="person.status === 'active' ? 'ok' : 'off'" :label="person.status === 'active' ? 'Active' : 'Deactivated'" />
            <span class="last" :data-tip="person.last_active_at ? absoluteTime(person.last_active_at) : undefined">{{ person.last_active_at ? `Active ${relativeTime(person.last_active_at, { long: true })}` : 'Never signed in' }}</span>
          </p>
        </div>
      </div>
    </template>

    <section class="block" aria-labelledby="ws-role-h">
      <h3 id="ws-role-h" class="eyebrow">Workspace role</h3>
      <div class="role-line">
        <span class="role-name">{{ person.workspace_role?.name ?? 'Projects only' }}</span>
        <button v-if="manage && person.status === 'active'" type="button" class="btn sm" :aria-expanded="!!roleAnchor" aria-haspopup="dialog" @click="roleAnchor = roleAnchor ? null : ($event.currentTarget as HTMLElement)">Change role</button>
      </div>
      <p class="effect">{{ role ? effectLine(role.permissions, access.registry, 5) : 'No workspace role: only the projects below.' }}</p>
      <p v-if="last" class="note"><AppIcon name="shield" :size="14" /><span>{{ LAST_OWNER_REASON }}</span></p>
    </section>

    <section id="projects" ref="projectsSection" class="block" aria-labelledby="proj-h">
      <div class="block-head">
        <h3 id="proj-h" class="eyebrow">Projects</h3>
        <button v-if="manage && person.status === 'active' && addChoices.length" type="button" class="btn sm ghost" @click="adding = $event.currentTarget as HTMLElement"><AppIcon name="plus" :size="13" />Add to a project</button>
      </div>
      <ul v-if="person.project_roles.length" class="projects">
        <li v-for="entry in person.project_roles" :key="entry.project_id">
          <span class="key-badge">{{ entry.project_key }}</span>
          <RouterLink class="p-title" :to="`/settings/access/projects/${entry.project_id}`">{{ entry.project_title }}</RouterLink>
          <button v-if="manage" type="button" class="role-btn" :aria-label="`Role on ${entry.project_title}: ${entry.role.name}. Change`" aria-haspopup="dialog" @click="projectRole = { project: { id: entry.project_id, title: entry.project_title }, current: entry.role.id, anchor: $event.currentTarget as HTMLElement }">{{ entry.role.name }}<AppIcon name="chevron" :size="12" class="chev" /></button>
          <span v-else class="role-text">{{ entry.role.name }}</span>
          <button v-if="manage" type="button" class="icon-btn sm flat remove" :aria-label="`Remove ${person.name} from ${entry.project_title}`" data-tip="Remove from this project" @click="removeProject(entry)"><AppIcon name="close" :size="13" /></button>
        </li>
      </ul>
      <p class="effect">{{ person.workspace_role ? `Through ${person.workspace_role.name}, ${first} also has that role’s access on every project.` : person.project_roles.length ? `${first} sees only these projects.` : `${first} has no project yet.` }}</p>
    </section>

    <section v-if="person.aliases.length" class="block" aria-labelledby="aka-h">
      <h3 id="aka-h" class="eyebrow">Also known as</h3>
      <ul class="aliases">
        <li v-for="alias in person.aliases" :key="alias.principal_id">
          <span class="mono">{{ alias.name }}</span><span class="src">imported from classic</span>
          <button v-if="manage" type="button" class="btn sm ghost" :aria-label="`Unlink ${alias.name}`" @click="unlink(alias)">Unlink</button>
        </li>
      </ul>
    </section>

    <section v-if="manage" class="block" aria-labelledby="life-h">
      <h3 id="life-h" class="eyebrow">Sign-in</h3>
      <p v-if="person.status === 'active' && last" class="note"><AppIcon name="shield" :size="14" /><span>{{ first }} is the last active owner, so {{ first }} stays signed in and cannot be deactivated. Make another person an owner first.</span></p>
      <template v-else-if="person.status === 'active'">
        <p class="effect">Deactivating signs {{ first }} out everywhere and revokes their keys. Their work and history stay.</p>
        <button type="button" class="btn sm danger" @click="deactivate"><AppIcon name="stop" :size="13" />Deactivate {{ first }}</button>
      </template>
      <template v-else>
        <p class="effect">{{ first }} cannot sign in. Reactivating brings back their roles and projects as they were.</p>
        <button type="button" class="btn sm" @click="reactivate"><AppIcon name="refresh" :size="13" />Reactivate {{ first }}</button>
      </template>
    </section>

    <RolePicker
      v-if="roleAnchor" :anchor="roleAnchor" :subject="person.name" :roles="access.roles" :role-details="access.runtimeRoleDetails" :current="person.workspace_role?.id ?? null" :registry="access.registry"
      :mine="myPermissions()" scope="workspace" allow-none none-label="Projects only" :locked="last" :busy="busy" :can-apply="can('members.manage')" :error="roleError" @choose="chooseWorkspace" @close="roleAnchor = null"
    />
    <RolePicker
      v-if="projectRole" :anchor="projectRole.anchor" :subject="person.name" :place="projectRole.project.title" :roles="access.roles" :role-details="access.runtimeRoleDetails" :current="projectRole.current" :registry="access.registry"
      :mine="myPermissions(projectRole.project.id)" scope="project" :busy="busy" :can-apply="can('members.manage', projectRole.project.id)" :error="roleError" @choose="chooseProject" @close="projectRole = null"
    />
    <ChoicePicker v-if="adding" :anchor="adding" label="Add to project" :choices="addChoices" current="" placeholder="Find a project…" @choose="pickProject" @close="adding = null" />
  </AccessSheet>
</template>

<style scoped>
.head { display: flex; align-items: center; gap: 14px; }
.head-text { display: grid; gap: 2px; min-width: 0; }
.title { font: 600 18px/1.3 var(--font); color: var(--ink); overflow-wrap: anywhere; }
.sub { font-size: 13px; color: var(--ink-2); overflow-wrap: anywhere; }
.facts { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 4px; }
.last { font-size: 12.5px; color: var(--ink-3); }
.block { display: grid; gap: 8px; padding: 14px 0; border-top: 1px solid var(--line); }
.block:first-child { border-top: 0; }
.block-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 28px; }
.role-line { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.role-name { font-size: 15px; font-weight: 650; color: var(--ink); }
.effect { font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.note { display: grid; grid-template-columns: 14px 1fr; gap: 8px; padding: 9px 11px; border-radius: 10px; background: var(--surface-2); font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
.note svg { margin-top: 2px; color: var(--teal-ink); }
.projects, .aliases { display: grid; margin: 0; padding: 0; list-style: none; }
.projects li { display: grid; grid-template-columns: auto minmax(0, 1fr) auto auto; align-items: center; gap: 10px; min-height: 44px; border-bottom: 1px solid var(--line); }
.p-title { min-width: 0; color: var(--ink); font-size: 13.5px; font-weight: 600; text-decoration: none; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.p-title:hover { color: var(--teal-ink); }
.role-btn { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; font-weight: 600; }
@media (hover: hover) { .role-btn:hover { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); } }
.role-btn:focus-visible { box-shadow: var(--focus-ring); }
.role-text { font-size: 13px; font-weight: 600; }
.chev { color: var(--ink-3); }
.remove { color: var(--ink-3); }
.aliases li { display: flex; align-items: center; gap: 10px; min-height: 40px; }
.aliases .mono { font-size: 12.5px; }
.src { flex: 1; font-size: 12px; color: var(--ink-3); }
.btn.danger { justify-self: start; }
@media (max-width: 600px) { .projects li { min-height: 52px; } .role-btn { height: 40px; } .remove { width: 44px; height: 44px; } .block .btn { height: 44px; } }
</style>
