<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { APIError, assignProjectGroup, createNode, getKinds, getProjectGroups, type SharedProjectGroup, type WorkNode } from '../../lib/api'
import { can } from '../../lib/authz'
import { projectKeyProblem, projectSettingsPath, suggestProjectKey } from '../../lib/projectSettings'
import { projectRouteKey } from '../../lib/work'
import { useIdentityScope } from '../../lib/useIdentityScope'
import { toast } from '../../lib/toast'
import { useProjects } from '../../stores/projects'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import ProjectSettingsSheet from '../settings/ProjectSettingsSheet.vue'

const emit = defineEmits<{ close: [] }>()
const router = useRouter(), projects = useProjects(), scope = useIdentityScope(() => can('nodes.write'))
const name = ref(''), key = ref(''), touched = ref(false), group = ref('')
const kind = ref(''), groups = ref<SharedProjectGroup[]>([]), groupError = ref(''), loadError = ref('')
const busy = ref(false), conflict = ref(''), error = ref('')
const problem = computed(() => conflict.value || (name.value.trim() ? projectKeyProblem(key.value) : ''))
const valid = computed(() => !!scope.owner.value && !!kind.value && !!name.value.trim() && !projectKeyProblem(key.value) && !conflict.value && !busy.value)
const groupAllowed = computed(() => can('project_groups.read') && can('project_groups.write'))
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
watch(() => scope.owner.value, (owner, previous) => {
  if (previous && owner !== previous) { emit('close'); return }
  if (owner) load()
}, { immediate: true, flush: 'sync' })
function finish(node: WorkNode, groupFailed = false) {
  projects.addCreatedProject(node)
  toast(groupFailed ? 'Project created, but the group was not assigned. Choose it in General.' : 'Project created.', { tone: groupFailed ? 'error' : 'info' })
  void router.push({ path: projectSettingsPath(projectRouteKey(node.key, node.fields)), query: { new: node.id, ...(groupFailed ? { group_assignment_failed: '1' } : {}) } })
  emit('close')
}
function submit() {
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
</script>

<template>
  <ProjectSettingsSheet title="New project" description="Name it and give it a key; the rest lives in its Settings." @close="emit('close')" @submit="submit">
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
</template>

<style scoped>
.np { display: grid; gap: 18px; padding-top: 8px; }.np-f { display: grid; gap: 6px; min-width: 0; }.np-f > b { font-size: 13px; font-weight: 600; }.np-f > small { font-size: 12px; line-height: 1.5; color: var(--ink-3); overflow-wrap: anywhere; }#np-key-h { height: 4.5em; overflow: auto; }.np-f .field { width: 100%; height: 38px; min-width: 0; }.np-k { display: flex; align-items: center; gap: 12px; }.np-k .field { width: 11ch; max-width: 100%; text-transform: uppercase; letter-spacing: .04em; }.faint { color: var(--ink-3); font-weight: 400; }
.sh-note { display: flex; gap: 8px; padding: 10px 12px; border-radius: 10px; background: var(--surface-sunken); font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }.sh-note svg { flex: none; margin-top: 2px; }.np-f > small.bad, .error { color: var(--danger); }.error { font-size: 12.5px; overflow-wrap: anywhere; }
@media (pointer: coarse), (max-width: 600px) { .np-f .field { min-height: 44px; height: 44px; }.np-k .field { width: 100%; }.btn { min-height: 44px; } }
</style>
