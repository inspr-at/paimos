<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script lang="ts">
export interface InvitePrefill { email: string; workspaceRoleId: string | null; projectRoles: { project_id: string; role_id: string }[] }
</script>
<script setup lang="ts">
import { brand } from '../../lib/brand'
import { computed, nextTick, onMounted, ref } from 'vue'
import { beyond, defaultProjectRole, defaultWorkspaceRole, effectLine, EXPIRY_DAYS, type InviteCreated, lostPermission, permissionLabel, projectRolesOf, validEmail, workspaceRolesOf } from '../../lib/access'
import { can, myPermissions } from '../../lib/authz'
import { absoluteTime } from '../../lib/work'
import { useAccess } from '../../stores/access'
import { useProjects } from '../../stores/projects'
import AppIcon from '../AppIcon.vue'
import BizIcon from '../business/BizIcon.vue'
import AccessSheet from './AccessSheet.vue'
import { fieldOf, problem } from './accessText'

// Invite someone by email with a workspace role, project roles, or both. Aeon
// supplies a one-time join link; the configured provider may separately send
// a sign-in setup email for a newly created account.
const props = defineProps<{ prefill?: InvitePrefill | null }>()
const emit = defineEmits<{ close: [] }>()
const access = useAccess()
const projects = useProjects()
const email = ref(props.prefill?.email ?? '')
const displayName = ref('')
const provisionAccount = ref(false)
const provisioner = computed(() => access.members?.provisioner?.name ?? null)
const mine = computed(() => myPermissions())
const grantable = (roleId: string) => !beyond(access.roleById.get(roleId)?.permissions ?? [], mine.value).length
const projectRoles = computed(() => projectRolesOf(access.roles, access.registry))
// Guest is a project role; it is offered on projects only.
const workspaceRoles = computed(() => workspaceRolesOf(access.roles))
const okWorkspace = (id: string) => workspaceRoles.value.some(r => r.id === id) && grantable(id)
// The invite API checks every permission of a project role against the
// inviter's workspace permissions, unlike a project binding, so the form does
// the same and never offers a role the server would refuse.
const okProject = (id: string) => projectRoles.value.some(r => r.id === id) && grantable(id)
// A default or an "Invite again" prefill keeps only roles I may give there.
const start = props.prefill ? props.prefill.workspaceRoleId ?? '' : defaultWorkspaceRole(access.roles)
const workspaceRole = ref<string>(start && okWorkspace(start) ? start : '')
const projectRows = ref<{ project_id: string; role_id: string }[]>(props.prefill?.projectRoles.filter(r => okProject(r.role_id)).map(r => ({ ...r })) ?? [])
const days = ref<number>(14)
const errors = ref<Record<string, string>>({})
const saving = ref(false)
const result = ref<InviteCreated | null>(null)
const retrying = ref(false)
const copied = ref(false)
const whyNot = (roleId: string) => { const missing = beyond(access.roleById.get(roleId)?.permissions ?? [], mine.value); return missing.length ? `needs ${missing.slice(0, 2).map(permissionLabel).join(', ')}${missing.length > 2 ? ' and more' : ''}, which you do not hold` : '' }
const effect = computed(() => { const role = access.roleById.get(workspaceRole.value); return role ? effectLine(role.permissions, access.registry) : 'No workspace access: only the projects below.' })
const freeProjects = (row: { project_id: string }) => projects.projects.filter(p => p.id === row.project_id || !projectRows.value.some(r => r.project_id === p.id))
// The server takes at most 50 projects on one invite.
const MAX_PROJECTS = 50
function addProject() {
  if (projectRows.value.length >= MAX_PROJECTS) return
  const next = projects.projects.find(p => !projectRows.value.some(r => r.project_id === p.id))
  if (!next) return
  projectRows.value.push({ project_id: next.id, role_id: defaultProjectRole(projectRoles.value) })
  void nextTick(() => document.querySelector<HTMLSelectElement>(`[data-project-row="${projectRows.value.length - 1}"]`)?.focus())
}
function validate(): boolean {
  const out: Record<string, string> = {}
  if (!validEmail(email.value)) out.email = email.value.trim() ? 'Enter an email address like name@example.com.' : 'Enter the email address they sign in with.'
  if (provisionAccount.value && !displayName.value.trim()) out.display_name = 'Enter their name for the sign-in account.'
  if (!workspaceRole.value && !projectRows.value.length) out.access = 'Give a workspace role or at least one project, or they could not see anything.'
  else if (workspaceRole.value && !okWorkspace(workspaceRole.value)) out.access = 'Choose a workspace role you can give.'
  else if (projectRows.value.some(r => !okProject(r.role_id))) out.access = 'Choose a project role you can give for every project.'
  errors.value = out
  return !Object.keys(out).length
}
const allowed = computed(() => can('members.manage'))
async function submit() {
  if (!allowed.value) return
  if (saving.value || !validate()) { void nextTick(() => document.querySelector<HTMLElement>('.invite-form [aria-invalid="true"]')?.focus()); return }
  saving.value = true
  try {
    result.value = await access.invite({ email: email.value.trim(), ...(workspaceRole.value ? { workspace_role_id: workspaceRole.value } : {}), ...(projectRows.value.length ? { project_roles: projectRows.value } : {}), expires_in_days: days.value, ...(provisionAccount.value ? { provision_account: true, display_name: displayName.value.trim() } : {}) })
    await nextTick()
    document.querySelector<HTMLElement>('.join-copy')?.focus()
  } catch (e) {
    const field = fieldOf(e)
    const key = field === 'workspace_role_id' || field === 'project_roles' ? 'access' : field === 'expires_in_days' ? 'days' : field === 'email' ? 'email' : field === 'display_name' ? 'display_name' : 'form'
    errors.value = { [key]: problem(e, 'The invite was not created').replace(/^The invite was not created: /, '') }
  } finally { saving.value = false }
}
async function copy() {
  if (!result.value) return
  try { await navigator.clipboard.writeText(result.value.join_url); copied.value = true } catch { copied.value = false }
}
async function retryProvision() {
  if (!result.value || retrying.value || !allowed.value) return
  retrying.value = true
  try { result.value.account = await access.retryInviteProvision(result.value.invite.id) }
  catch { result.value.account = { status: 'failed', reason: 'The sign-in account could not be set up. Ask an administrator.' } }
  finally { retrying.value = false }
}
function another() { result.value = null; copied.value = false; email.value = ''; displayName.value = ''; provisionAccount.value = false; errors.value = {}; void nextTick(() => document.getElementById('invite-email')?.focus()) }
onMounted(() => { void projects.load() })
</script>

<template>
  <AccessSheet :title="result ? 'Invite ready' : 'Invite people'" size="center" @close="saving || emit('close')">
    <form v-if="!result" class="invite-form" novalidate @submit.prevent="submit">
      <p class="intro"><BizIcon name="mail" :size="14" /><span>{{ brand.short_name }} never sends email. You get a link to send yourself. If you create a sign-in account, the provider sends a separate setup email.</span></p>
      <div class="field-row">
        <label class="label" for="invite-email">Email</label>
        <input id="invite-email" v-model="email" class="field" type="email" autocomplete="off" spellcheck="false" placeholder="name@example.com" data-autofocus :aria-invalid="!!errors.email" :aria-describedby="errors.email ? 'email-error' : undefined" @input="errors.email = ''" />
        <span v-if="errors.email" id="email-error" class="error"><AppIcon name="alert" :size="12" />{{ errors.email }}</span>
      </div>
      <div v-if="provisioner" class="field-row">
        <label class="provision-choice"><input v-model="provisionAccount" type="checkbox" /> Also create their sign-in account ({{ provisioner }})</label>
        <span class="hint">{{ provisioner }} emails them a link to set up sign-in.</span>
      </div>
      <div v-if="provisionAccount" class="field-row">
        <label class="label" for="invite-display-name">Their name</label>
        <input id="invite-display-name" v-model="displayName" class="field" autocomplete="off" maxlength="200" :aria-invalid="!!errors.display_name" :aria-describedby="errors.display_name ? 'display-name-error' : undefined" @input="errors.display_name = ''" />
        <span v-if="errors.display_name" id="display-name-error" class="error"><AppIcon name="alert" :size="12" />{{ errors.display_name }}</span>
      </div>
      <div class="field-row">
        <label class="label" for="invite-role">Workspace role</label>
        <select id="invite-role" v-model="workspaceRole" class="field" :aria-invalid="!!errors.access" aria-describedby="ws-effect" @change="errors.access = ''">
          <option value="">None: only the projects below</option>
          <option v-for="role in workspaceRoles" :key="role.id" :value="role.id" :disabled="!grantable(role.id)">{{ role.name }}{{ grantable(role.id) ? '' : ` (${whyNot(role.id)})` }}</option>
        </select>
        <span id="ws-effect" class="hint">{{ effect }}</span>
      </div>
      <fieldset class="field-row projects">
        <legend class="label">Projects</legend>
        <p v-if="!projectRows.length" class="hint">Add projects for someone who should work on only some of them, such as a guest.</p>
        <div v-for="(row, index) in projectRows" :key="index" class="project-row">
          <select v-model="row.project_id" class="field" :aria-label="`Project ${index + 1}`" :data-project-row="index">
            <option v-for="p in freeProjects(row)" :key="p.id" :value="p.id">{{ p.title }}</option>
          </select>
          <select v-model="row.role_id" class="field" :aria-label="`Role on project ${index + 1}`">
            <option v-for="role in projectRoles" :key="role.id" :value="role.id" :disabled="!grantable(role.id)">{{ role.name }}{{ grantable(role.id) ? '' : ` (${whyNot(role.id)})` }}</option>
          </select>
          <button type="button" class="icon-btn sm flat" :aria-label="`Remove project ${index + 1}`" @click="projectRows.splice(index, 1)"><AppIcon name="close" :size="13" /></button>
        </div>
        <p v-if="projectRows.length >= MAX_PROJECTS" class="cap">An invite can name at most {{ MAX_PROJECTS }} projects. Give a workspace role instead, or send a second invite for the rest.</p>
        <button v-else-if="projectRows.length < projects.projects.length" type="button" class="btn sm ghost add" @click="addProject"><AppIcon name="plus" :size="13" />Add a project</button>
        <span v-if="errors.access" class="error" role="alert"><AppIcon name="alert" :size="12" />{{ errors.access }}</span>
      </fieldset>
      <div class="field-row short">
        <label class="label" for="invite-days">Link works for</label>
        <select id="invite-days" v-model.number="days" class="field" :aria-invalid="!!errors.days" :aria-describedby="errors.days ? 'days-error' : undefined">
          <option v-for="d in EXPIRY_DAYS" :key="d" :value="d">{{ d }} days</option>
        </select>
        <span v-if="errors.days" id="days-error" class="error"><AppIcon name="alert" :size="12" />{{ errors.days }}</span>
      </div>
      <p v-if="errors.form" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ errors.form }}</p>
    </form>
    <div v-else class="result">
      <p class="done"><AppIcon name="check" :size="16" /><span>The invite for <b>{{ result.invite.email }}</b> is ready.</span></p>
      <p class="once"><AppIcon name="info" :size="14" /><span>This link is shown only now. Copy it and send it yourself: {{ brand.short_name }} sends no email. It works once, for {{ result.invite.email }}, until {{ absoluteTime(result.invite.expires_at) }}.</span></p>
      <p v-if="result.account?.status === 'invited'" class="hint">{{ provisioner }} has emailed them a sign-in setup link.</p>
      <p v-else-if="result.account?.status === 'exists'" class="hint">They already have a sign-in account with this email.</p>
      <div v-else-if="result.account?.status === 'failed'" class="account-failed" role="alert">
        <span>{{ result.account.reason }}</span>
        <button type="button" class="btn sm" :disabled="retrying || !allowed" @click="retryProvision">{{ retrying ? 'Retrying…' : 'Retry account setup' }}</button>
      </div>
      <div class="join">
        <input class="field mono join-url" readonly :value="result.join_url" aria-label="Join link" @focus="($event.target as HTMLInputElement).select()" />
        <button type="button" class="btn primary join-copy" data-session-keep @click="copy"><AppIcon :name="copied ? 'check' : 'copy'" :size="14" />{{ copied ? 'Copied' : 'Copy link' }}</button>
      </div>
      <p class="hint">{{ result.account?.status === 'invited' ? 'Once they finish sign-in setup and use the join link, they appear under People with the roles you chose.' : result.account?.status === 'exists' ? 'After they sign in with this address and use the join link, they appear under People with the roles you chose.' : 'They need a sign-in account with this email before they can use the join link.' }}</p>
    </div>
    <template #foot>
      <template v-if="!result">
        <button type="button" class="btn primary" :disabled="saving || !allowed" @click="submit"><AppIcon name="send" :size="13" /><span class="btn-label"><span>{{ saving ? 'Creating…' : 'Create invite link' }}</span><span aria-hidden="true">Create invite link</span></span></button>
        <button type="button" class="btn" :disabled="saving" @click="emit('close')">Cancel</button>
        <p v-if="!allowed" class="lost" role="alert">{{ lostPermission('members.manage') }}</p>
      </template>
      <template v-else>
        <button type="button" class="btn primary" data-session-keep @click="emit('close')">Done</button>
        <button type="button" class="btn" @click="another">Invite someone else</button>
      </template>
    </template>
  </AccessSheet>
</template>

<style scoped>
.invite-form, .result { display: grid; gap: 14px; }
.intro, .once { display: grid; grid-template-columns: 14px 1fr; gap: 8px; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.intro svg, .once svg { margin-top: 3px; color: var(--teal-ink); }
.field-row { display: grid; gap: 6px; margin: 0; padding: 0; border: 0; min-width: 0; }
.field-row.short select { max-width: 200px; }
.provision-choice { display: flex; align-items: center; gap: 9px; font-size: 13px; color: var(--ink); }
.provision-choice input { width: 16px; height: 16px; accent-color: var(--teal-ink); }
.account-failed { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; color: var(--danger); font-size: 12.5px; }
.label { font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; padding: 0; }
select.field { appearance: auto; padding-right: 8px; }
.hint { font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
.error { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--danger); }
.projects { gap: 8px; }
.project-row { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr) auto; gap: 8px; align-items: center; }
.add { justify-self: start; }
.done { display: flex; align-items: center; gap: 8px; font-size: 14px; color: var(--ink); }
.done svg { color: var(--ok); }
.join { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }
.join-url { font-size: 12.5px; }
@media (max-width: 600px) {
  .project-row { grid-template-columns: minmax(0, 1fr) auto; }
  .project-row select:nth-child(2) { grid-column: 1; }
  .project-row .icon-btn { grid-row: 1 / span 2; grid-column: 2; width: 44px; height: 44px; }
  .join { grid-template-columns: minmax(0, 1fr); }
  .join .btn { height: 44px; }
  .add { height: 44px; }
}
.lost { flex: 1 1 100%; margin: 0; font-size: 12.5px; line-height: 1.45; color: var(--danger); }
.cap { font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
</style>
