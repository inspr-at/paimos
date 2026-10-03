<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { beyond, deleteRole, lostPermission, projectRolesOf, type Role, workspaceRolesOf } from '../../lib/access'
import { can, myPermissions } from '../../lib/authz'
import { toast } from '../../lib/toast'
import { useAccess } from '../../stores/access'
import AppIcon from '../AppIcon.vue'
import AccessSheet from './AccessSheet.vue'
import { fieldOf, problem } from './accessText'

// Deleting a custom role. When people or agents hold it, they need another role
// first: I choose which, and the delete moves them all to it in one step.
const props = defineProps<{ role: Role }>()
const emit = defineEmits<{ close: []; deleted: [] }>()
const access = useAccess()
const inUse = computed(() => props.role.member_count > 0)
// Its holders may be bound in the workspace or on projects, so the replacement
// must be legal in both places.
const targets = computed(() => { const both = new Set(projectRolesOf(workspaceRolesOf(access.roles), access.registry).map(r => r.id)); return access.roles.filter(r => r.id !== props.role.id && both.has(r.id)) })
const allowed = (r: Role) => !beyond(r.permissions, myPermissions()).length
const reassign = ref(props.role.based_on && targets.value.some(r => r.id === props.role.based_on && allowed(r)) ? props.role.based_on : targets.value.find(allowed)?.id ?? '')
const error = ref('')
const busy = ref(false)
const holders = computed(() => props.role.member_count === 1 ? '1 person or agent holds' : `${props.role.member_count} people and agents hold`)
const targetName = computed(() => access.roleById.get(reassign.value)?.name ?? '')
const permitted = computed(() => can('roles.manage'))
// The chosen replacement must still be one I may give; if not, the first that is.
const reassignOk = computed(() => !inUse.value || targets.value.some(r => r.id === reassign.value && allowed(r)))
watch(reassignOk, ok => { if (!ok) reassign.value = targets.value.find(allowed)?.id ?? '' })
async function remove() {
  if (!permitted.value) { error.value = lostPermission('roles.manage'); return }
  if (inUse.value && !reassignOk.value) { error.value = reassign.value ? 'You can no longer give that role; choose another.' : 'Choose the role they get instead.'; return }
  busy.value = true
  try {
    await deleteRole(props.role.id, inUse.value ? reassign.value : undefined)
    toast(inUse.value ? `Deleted ${props.role.name}; its holders now have ${targetName.value}` : `Deleted ${props.role.name}`)
    // Leave the role's page first, then read the lists again.
    emit('deleted')
    void access.settle()
  } catch (e) {
    error.value = problem(e, 'The role was not deleted').replace(/^The role was not deleted: /, '')
    if (fieldOf(e) !== 'reassign_to') error.value = problem(e, 'The role was not deleted')
  } finally { busy.value = false }
}
</script>

<template>
  <AccessSheet :title="`Delete ${role.name}?`" size="center" @close="emit('close')">
    <div class="body">
      <ul class="points">
        <li><AppIcon name="info" :size="13" /><span>The role goes for good; built-in roles and other custom roles stay.</span></li>
        <li v-if="inUse"><AppIcon name="info" :size="13" /><span>{{ holders }} it, in the workspace or on projects. They all get the role you choose below, at once.</span></li>
        <li v-else><AppIcon name="info" :size="13" /><span>Nobody holds it, so nobody’s access changes.</span></li>
        <li><AppIcon name="info" :size="13" /><span>The access log keeps a record of the role and of every change.</span></li>
      </ul>
      <div v-if="inUse" class="field-row">
        <label class="label" for="reassign-role">Give its holders</label>
        <select id="reassign-role" v-model="reassign" class="field" data-autofocus :aria-invalid="!!error" :aria-describedby="error ? 'reassign-error' : undefined" @change="error = ''">
          <option v-for="r in targets" :key="r.id" :value="r.id" :disabled="!allowed(r)">{{ r.name }}{{ allowed(r) ? '' : ' (you cannot give this)' }}</option>
        </select>
      </div>
      <p v-if="!permitted" class="error" role="alert"><AppIcon name="shield" :size="12" />{{ lostPermission('roles.manage') }}</p>
      <p v-if="error" id="reassign-error" class="error" role="alert"><AppIcon name="alert" :size="12" />{{ error }}</p>
    </div>
    <template #foot>
      <button type="button" class="btn" @click="emit('close')">Keep the role</button>
      <button type="button" class="btn danger-solid" :disabled="busy || !permitted || !reassignOk" :data-tip="permitted ? undefined : lostPermission('roles.manage')" @click="remove">{{ inUse && targetName ? `Delete and give ${targetName}` : 'Delete role' }}</button>
    </template>
  </AccessSheet>
</template>

<style scoped>
.body { display: grid; gap: 14px; }
.points { display: grid; gap: 8px; margin: 0; padding: 0; list-style: none; }
.points li { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: 8px; font-size: 13px; line-height: 1.45; color: var(--ink-2); }
.points svg { margin-top: 3px; color: var(--ink-3); }
.field-row { display: grid; gap: 6px; }
.label { font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
select.field { appearance: auto; }
.error { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--danger); }
.danger-solid { color: var(--danger-on); border-color: transparent; background: var(--danger); box-shadow: 0 0 0 1px rgba(168, 66, 60, .5), 0 8px 18px -10px rgba(168, 66, 60, .7); }
.danger-solid:hover { filter: brightness(1.05); background: var(--danger); }
</style>
