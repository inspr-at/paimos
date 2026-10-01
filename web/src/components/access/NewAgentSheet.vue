<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { agentDescription, createAgent, effectLine, KEY_SCOPE_PRESETS, keyScopes, neededToGive, suggestAgentRole, type Agent, type Role } from '../../lib/access'
import { can, myPermissions } from '../../lib/authz'
import { useAccess } from '../../stores/access'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import AccessSheet from './AccessSheet.vue'
import AppIcon from '../AppIcon.vue'
import { problem } from './accessText'

const emit = defineEmits<{ close: []; created: [agent: Agent, presetId: string] }>()
const access = useAccess()
const projects = useProjects()
const session = useSession()
const name = ref('')
const presetId = ref('')
const preset = computed(() => KEY_SCOPE_PRESETS.find(p => p.id === presetId.value))
const descriptionEdited = ref(false)
const emptyConfirmed = ref(false)
const scope = ref<'workspace' | 'projects'>('workspace')
const roleId = ref('')
const roleEdited = ref(false)
const selected = ref<string[]>([])
const busy = ref(false)
const error = ref('')
const tried = ref(false)
const allowed = computed(() => session.identity?.principal.kind === 'person' && can('keys.manage'))
const roles = computed(() => access.roles.filter(r => !(r.builtin && ['owner', 'customer', ...(scope.value === 'workspace' ? ['guest'] : [])].includes(r.key))))
const grantable = (role: Role) => role.permissions.every(k => myPermissions().has(k))
const role = computed(() => roles.value.find(r => r.id === roleId.value))
const validRole = computed(() => !!role.value && grantable(role.value))
const availableProjects = computed(() => projects.projects.filter(p => !p.archived))
const suggested = computed(() => suggestAgentRole(roles.value, preset.value?.scopes ?? [], scope.value === 'projects' ? 'project' : 'workspace', access.registry, myPermissions()))
const roleEffect = (r: Role) => effectLine(neededToGive(r, scope.value === 'projects' ? 'project' : 'workspace', access.registry).filter(key => keyScopes(access.registry).some(p => p.key === key)), access.registry, 3)
const prefill = computed(() => agentDescription(preset.value?.label ?? '', scope.value, selected.value.map(id => projects.projects.find(p => p.id === id)?.routeKey ?? id)))
const description = ref(prefill.value)
watch(prefill, value => { if (!descriptionEdited.value) description.value = value })
watch(description, () => { emptyConfirmed.value = false })
watch([suggested, roles, presetId], () => {
  if (roleEdited.value) return
  roleId.value = preset.value ? suggested.value?.id ?? '' : roles.value.find(r => r.builtin && r.key === 'viewer' && grantable(r))?.id ?? ''
}, { immediate: true })
function choosePreset() {
  roleEdited.value = false
  if (presetId.value === 'ticket-worker') scope.value = 'projects'
}
const effect = computed(() => scope.value === 'projects' ? 'Only the selected projects; no workspace access.' : role.value?.permissions.includes('nodes.read') ? 'All projects, including future ones.' : 'Workspace permissions only; this role cannot read projects.')
async function submit() {
  if (busy.value || !allowed.value) return
  tried.value = true
  if (!name.value.trim() || !validRole.value || !description.value.trim() && !emptyConfirmed.value || scope.value === 'projects' && !selected.value.length) {
    await nextTick()
    document.querySelector<HTMLElement>('.new-agent-form [aria-invalid="true"]')?.focus()
    return
  }
  busy.value = true
  error.value = ''
  try {
    const agent = await createAgent({ name: name.value.trim(), description: description.value.trim(), ...(scope.value === 'workspace' ? { workspace_role_id: roleId.value } : { project_roles: selected.value.map(project_id => ({ project_id, role_id: roleId.value })) }) })
    emit('created', agent, presetId.value)
  } catch (e) { error.value = problem(e, 'The agent was not created') }
  finally { busy.value = false }
}
onMounted(() => { void projects.load() })
</script>

<template>
  <AccessSheet title="New agent" size="center" @close="busy || emit('close')">
    <form id="new-agent-form" class="new-agent-form" @submit.prevent="submit">
      <p class="hint">Create an identity for a CLI or script, then choose its first key’s scopes.</p>
      <label for="agent-name">Name</label>
      <input id="agent-name" v-model="name" class="field" maxlength="200" autocomplete="off" spellcheck="false" placeholder="e.g. release-helper" data-autofocus :disabled="busy" :aria-invalid="tried && !name.trim()" :aria-describedby="tried && !name.trim() ? 'agent-name-error' : undefined" />
      <p v-if="tried && !name.trim()" id="agent-name-error" class="error">Enter a name for this agent.</p>
      <label for="agent-preset">Purpose preset</label>
      <select id="agent-preset" v-model="presetId" class="field" :disabled="busy" @change="choosePreset">
        <option value="">Custom CLI or script</option>
        <option v-for="p in KEY_SCOPE_PRESETS" :key="p.id" :value="p.id">{{ p.label }}</option>
      </select>
      <div v-if="preset" class="preset-preview">
        <p class="hint">{{ preset.description }} Preview these scopes; apply them in the first key sheet.</p>
        <p class="mono preset-scopes">{{ preset.scopes.join(', ') }}</p>
      </div>
      <label for="agent-description">Description <span class="optional">optional</span></label>
      <textarea id="agent-description" v-model="description" class="field" rows="2" maxlength="1000" :disabled="busy" placeholder="What this agent does" @input="descriptionEdited = true" />
      <label v-if="!description.trim()" class="empty-confirm">
        <input v-model="emptyConfirmed" type="checkbox" :disabled="busy" :aria-invalid="tried && !emptyConfirmed" :aria-describedby="tried && !emptyConfirmed ? 'agent-description-error' : undefined" />
        Create without a description
      </label>
      <p v-if="tried && !description.trim() && !emptyConfirmed" id="agent-description-error" class="error">Add a description or confirm creating without one.</p>
      <label for="agent-role">Role <span class="optional">permission ceiling</span></label>
      <select id="agent-role" v-model="roleId" class="field" :disabled="busy" :aria-invalid="tried && !validRole" :aria-describedby="tried && !validRole ? 'agent-role-error' : 'agent-role-effect'" @change="roleEdited = true">
        <option value="" disabled>Choose a role</option>
        <option v-for="r in roles" :key="r.id" :value="r.id" :disabled="!grantable(r)">{{ r.name }} — {{ roleEffect(r) }}{{ grantable(r) ? '' : ' Beyond your permissions.' }}</option>
      </select>
      <p id="agent-role-effect" class="hint">{{ role ? roleEffect(role) : 'Choose the role that covers the work this agent needs to do.' }}</p>
      <p v-if="preset" class="hint" aria-live="polite">
        <template v-if="suggested">{{ suggested.name }} covers {{ preset.label }} with the fewest permissions you may grant{{ scope === 'projects' ? ' on a project' : '' }}.
          <button v-if="roleId !== suggested.id" type="button" class="btn sm" :disabled="busy" @click="roleId = suggested.id; roleEdited = true">Use {{ suggested.name }}</button>
        </template>
        <template v-else>No non-admin role you may grant covers all of {{ preset.label }}{{ scope === 'projects' ? ' on a project' : '' }}. Choose a role explicitly or ask for a narrower custom role.</template>
      </p>
      <p v-if="tried && !validRole" id="agent-role-error" class="error">Choose a role you may grant.</p>
      <label for="agent-project-access">Project access</label>
      <select id="agent-project-access" v-model="scope" class="field" :disabled="busy" aria-describedby="agent-access-effect">
        <option value="workspace">Workspace role</option>
        <option value="projects">Selected projects only</option>
      </select>
      <p id="agent-access-effect" class="hint">{{ effect }}</p>
      <fieldset v-if="scope === 'projects'" class="projects" :aria-invalid="tried && !selected.length" :aria-describedby="tried && !selected.length ? 'agent-projects-error' : undefined" tabindex="-1">
        <legend class="sr-only">Projects</legend>
        <p v-if="projects.loading" class="hint" role="status">Loading projects…</p>
        <p v-else-if="projects.error" class="error" role="alert">Projects could not be loaded. <button type="button" class="btn sm" @click="projects.load(true)">Try again</button></p>
        <p v-else-if="!availableProjects.length" class="hint">No projects available.</p>
        <label v-for="project in availableProjects" :key="project.id" class="project">
          <input v-model="selected" type="checkbox" :value="project.id" :disabled="busy || selected.length >= 50 && !selected.includes(project.id)" />
          <span :title="project.title">{{ project.title }}</span>
        </label>
        <p v-if="tried && !selected.length" id="agent-projects-error" class="error">Choose at least one project.</p>
      </fieldset>
      <p v-if="!allowed" class="error" role="alert">Only a person with Manage agent keys may create an agent.</p>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
    </form>
    <template #foot>
      <button type="button" class="btn" :disabled="busy" @click="emit('close')">Cancel</button>
      <button type="submit" form="new-agent-form" class="btn primary" :disabled="busy || !allowed"><AppIcon name="plus" :size="14" />{{ busy ? 'Creating…' : 'Create agent' }}</button>
    </template>
  </AccessSheet>
</template>

<style scoped>
.new-agent-form { display: grid; gap: 8px; }
.new-agent-form > label { margin-top: 8px; font-size: 13px; font-weight: 600; }
.hint { color: var(--ink-2); font-size: 13px; line-height: 1.5; }
.optional { margin-left: 4px; color: var(--ink-3); font-size: 12px; font-weight: 400; }
textarea.field { resize: vertical; min-height: 64px; }
.preset-preview { display: grid; gap: 6px; padding: 10px 12px; border: 1px solid var(--line); border-radius: 10px; }
.preset-scopes { font-size: 11px; color: var(--ink-3); overflow-wrap: anywhere; line-height: 1.5; }
.new-agent-form > .empty-confirm { display: flex; align-items: center; gap: 8px; font-weight: 400; min-height: 36px; }
.empty-confirm input { accent-color: var(--teal); }
.error { color: var(--danger); font-size: 13px; }
.projects { border: 1px solid var(--line); border-radius: 10px; margin: 2px 0 0; padding: 8px 12px; max-height: 220px; overflow-y: auto; }
.project { display: flex; align-items: center; gap: 10px; min-height: 36px; font-size: 13px; }
.project input { width: 16px; height: 16px; flex-shrink: 0; accent-color: var(--teal); }
.project span { overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
@media (max-width: 600px) { .field, .project { min-height: 44px; } }
</style>
