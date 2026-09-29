<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { createAgent, type Agent, type Role } from '../../lib/access'
import { can, myPermissions } from '../../lib/authz'
import { useAccess } from '../../stores/access'
import { useProjects } from '../../stores/projects'
import { useSession } from '../../stores/session'
import AccessSheet from './AccessSheet.vue'
import AppIcon from '../AppIcon.vue'
import { problem } from './accessText'

const emit = defineEmits<{ close: []; created: [agent: Agent] }>()
const access = useAccess()
const projects = useProjects()
const session = useSession()
const name = ref('')
const description = ref('')
const scope = ref<'workspace' | 'projects'>('workspace')
const roleId = ref(access.roles.find(r => r.key === 'viewer')?.id ?? '')
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
const effect = computed(() => scope.value === 'projects' ? 'Only the selected projects; no workspace access.' : role.value?.permissions.includes('nodes.read') ? 'All projects, including future ones.' : 'Workspace permissions only; this role cannot read projects.')
async function submit() {
  if (busy.value || !allowed.value) return
  tried.value = true
  if (!name.value.trim() || !validRole.value || scope.value === 'projects' && !selected.value.length) {
    await nextTick()
    document.querySelector<HTMLElement>('.new-agent-form [aria-invalid="true"]')?.focus()
    return
  }
  busy.value = true
  error.value = ''
  try {
    const agent = await createAgent({ name: name.value.trim(), description: description.value.trim(), ...(scope.value === 'workspace' ? { workspace_role_id: roleId.value } : { project_roles: selected.value.map(project_id => ({ project_id, role_id: roleId.value })) }) })
    emit('created', agent)
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
      <label for="agent-description">Description <span class="optional">optional</span></label>
      <textarea id="agent-description" v-model="description" class="field" rows="2" maxlength="1000" :disabled="busy" placeholder="What this agent does" />
      <label for="agent-role">Role <span class="optional">permission ceiling</span></label>
      <select id="agent-role" v-model="roleId" class="field" :disabled="busy" :aria-invalid="tried && !validRole">
        <option value="" disabled>Choose a role</option>
        <option v-for="r in roles" :key="r.id" :value="r.id" :disabled="!grantable(r)">{{ r.name }}{{ grantable(r) ? '' : ' — beyond your permissions' }}</option>
      </select>
      <p v-if="tried && !validRole" class="error">Choose a role you may grant.</p>
      <label for="agent-project-access">Project access</label>
      <select id="agent-project-access" v-model="scope" class="field" :disabled="busy" aria-describedby="agent-access-effect">
        <option value="workspace">Workspace role</option>
        <option value="projects">Selected projects only</option>
      </select>
      <p id="agent-access-effect" class="hint">{{ effect }}</p>
      <fieldset v-if="scope === 'projects'" class="projects" :aria-invalid="tried && !selected.length" tabindex="-1">
        <legend class="sr-only">Projects</legend>
        <p v-if="projects.loading" class="hint" role="status">Loading projects…</p>
        <p v-else-if="projects.error" class="error" role="alert">Projects could not be loaded. <button type="button" class="btn sm" @click="projects.load(true)">Try again</button></p>
        <p v-else-if="!availableProjects.length" class="hint">No projects available.</p>
        <label v-for="project in availableProjects" :key="project.id" class="project">
          <input v-model="selected" type="checkbox" :value="project.id" :disabled="busy || selected.length >= 50 && !selected.includes(project.id)" />
          <span :title="project.title">{{ project.title }}</span>
        </label>
        <p v-if="tried && !selected.length" class="error">Choose at least one project.</p>
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
.error { color: var(--danger); font-size: 13px; }
.projects { border: 1px solid var(--line); border-radius: 10px; margin: 2px 0 0; padding: 8px 12px; max-height: 220px; overflow-y: auto; }
.project { display: flex; align-items: center; gap: 10px; min-height: 36px; font-size: 13px; }
.project input { width: 16px; height: 16px; flex-shrink: 0; accent-color: var(--teal); }
.project span { overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
@media (max-width: 600px) { .field, .project { min-height: 44px; } }
</style>
