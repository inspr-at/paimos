<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { AccessError, agentScopeCeiling, changeAgentKeyScopes, getAgentKeyScopes, grantablePresetScopes, groupPermissions, keyHint, matchesScope, MAX_KEY_SCOPES, type Agent, type Role } from '../../lib/access'
import { can, myPermissions } from '../../lib/authz'
import type { AgentKey } from '../../lib/settings'
import { useAccess } from '../../stores/access'
import AccessSheet from './AccessSheet.vue'
import AppIcon from '../AppIcon.vue'
import ScopeDetails from './ScopeDetails.vue'
import ScopePresets from './ScopePresets.vue'
import { problem } from './accessText'

const props = defineProps<{ agent: Agent; agentKey: AgentKey }>()
const emit = defineEmits<{ close: []; saved: [] }>()
const access = useAccess()
const original = ref<string[]>([])
const selected = ref(new Set<string>())
const grantable = ref(new Set<string>())
const roleGrantable = ref(new Set<string>())
const agentRole = ref<Role | null>(null)
const mayExtend = (key: string) => can('roles.manage') && myPermissions().has(key) && roleGrantable.value.has(key)
const loaded = ref(false)
const busy = ref(false)
const error = ref('')
const term = ref('')
const showUnavailable = ref(false)
const allowed = computed(() => can('keys.manage'))
// Presets never extend a role. Explicit checkbox changes still use the existing
// server-authorized role-extension confirmation below.
const ceiling = computed(() => agentScopeCeiling(access.agent(props.agent.principal_id) ?? props.agent, access.roles, access.registry))
const presetHeld = computed(() => new Set([...grantable.value].filter(key => myPermissions().has(key) && (!ceiling.value || ceiling.value.has(key)))))
const groups = computed(() => {
  // Keep obsolete or newly restricted scopes visible so they can be removed.
  const scopes = access.registry.filter(p => p.agent_grantable || original.value.includes(p.key))
  for (const key of original.value) if (!scopes.some(p => p.key === key)) scopes.push({ key, group: 'Other', description: key, risk: 'low', grantable_at: [], agent_grantable: false })
  return groupPermissions(scopes.filter(p => matchesScope(p, term.value) && (term.value.trim() || showUnavailable.value || grantable.value.has(p.key) || mayExtend(p.key) || original.value.includes(p.key))))
})
const added = computed(() => [...selected.value].filter(k => !original.value.includes(k)))
const removed = computed(() => original.value.filter(k => !selected.value.has(k)))
const roleAdded = computed(() => [...selected.value].filter(k => !grantable.value.has(k) && mayExtend(k)))
const invalid = computed(() => [...selected.value].some(k => !grantable.value.has(k) && !mayExtend(k)))
const changed = computed(() => added.value.length + removed.value.length + roleAdded.value.length > 0)
async function load() {
  busy.value = true
  error.value = ''
  try {
    const view = await getAgentKeyScopes(props.agentKey.id)
    original.value = view.key.scopes
    selected.value = new Set(view.key.scopes)
    grantable.value = new Set(view.grantable_scopes)
    agentRole.value = view.agent_role ?? access.roleById.get(props.agent.workspace_role?.id ?? '') ?? null
    roleGrantable.value = new Set(view.role_grantable_scopes ?? [])
    loaded.value = true
  } catch (e) { error.value = problem(e, 'The scopes could not be loaded') }
  finally { busy.value = false }
}
function toggle(key: string) {
  const next = new Set(selected.value)
  if (next.has(key)) next.delete(key)
  else if (grantable.value.has(key) || mayExtend(key)) next.add(key)
  selected.value = next
}
function preset(keys: string[]) {
  if (!busy.value && allowed.value) selected.value = new Set(grantablePresetScopes(keys, presetHeld.value, access.registry))
}
function unavailableReason(key: string) {
  if (!access.registry.some(p => p.key === key && p.agent_grantable)) return 'Unavailable to agent keys'
  if (!myPermissions().has(key)) return 'Not in your permissions'
  if (agentRole.value && !agentRole.value.permissions.includes(key)) return `Not in this agent's role (${agentRole.value.name})`
  if (!agentRole.value) return "Not in this agent's project roles"
  return "Not in the key creator's current permissions"
}
async function save() {
  if (busy.value || !allowed.value || !changed.value || invalid.value || selected.value.size > MAX_KEY_SCOPES) return
  busy.value = true
  error.value = ''
  try {
    await changeAgentKeyScopes(props.agentKey.id, [...new Set([...added.value, ...roleAdded.value])], removed.value, roleAdded.value.length && agentRole.value ? { role_id: agentRole.value.id, add: roleAdded.value } : undefined)
    emit('saved')
    emit('close')
  } catch (e) {
    error.value = e instanceof AccessError && e.status === 403 ? 'Access changed; reload the scopes and check what you may grant.' : problem(e, 'The scopes were not saved')
  } finally { busy.value = false }
}
onMounted(load)
</script>

<template>
  <AccessSheet :title="`Edit scopes for ${agent.name}`" size="center" @close="busy || emit('close')">
    <div class="body" :aria-busy="busy">
      <p class="note">Changes to <span class="mono">{{ keyHint(agentKey.prefix) }}</span> apply immediately with the same key.</p>
      <template v-if="loaded">
        <label class="search-field">
          <AppIcon name="search" :size="14" />
          <input v-model="term" class="field" type="search" aria-label="Find a scope" placeholder="Find a scope by name, id or group" data-autofocus autocomplete="off" />
        </label>
        <ScopePresets :held="presetHeld" :registry="access.registry" :disabled="busy || !allowed" @apply="preset" />
        <fieldset class="scopes" :disabled="busy || !allowed">
          <legend>{{ selected.size }} {{ selected.size === 1 ? 'scope' : 'scopes' }}</legend>
          <div v-for="group in groups" :key="group.group" class="scope-group" role="group" :aria-label="group.group">
            <p class="group-h">{{ group.group }}</p>
            <label v-for="scope in group.items" :key="scope.key" class="scope-row">
              <input type="checkbox" :checked="selected.has(scope.key)" :disabled="!grantable.has(scope.key) && !mayExtend(scope.key) && !selected.has(scope.key)" @change="toggle(scope.key)" />
              <ScopeDetails :scope="scope"><span v-if="!grantable.has(scope.key)" class="detail">{{ unavailableReason(scope.key) }}</span></ScopeDetails>
            </label>
          </div>
          <p v-if="!groups.length" class="note">{{ term ? 'No matching scopes.' : 'No scopes are available for this key.' }}</p>
        </fieldset>
        <button v-if="!term" type="button" class="btn sm ghost scope-toggle" :aria-expanded="showUnavailable" @click="showUnavailable = !showUnavailable">{{ showUnavailable ? 'Hide unavailable scopes' : 'Show unavailable scopes' }}</button>
        <p v-if="!selected.size" class="note">This key will have no access.</p>
        <p v-else-if="invalid" class="note" role="alert">Remove unavailable scopes before saving.</p>
        <p v-if="selected.size > MAX_KEY_SCOPES" class="note" role="alert">Choose at most {{ MAX_KEY_SCOPES }} scopes.</p>
        <p v-if="changed" class="changes" aria-live="polite">{{ added.length }} added · {{ removed.length }} removed</p>
      </template>
      <p v-else-if="busy" class="note" role="status">Loading scopes…</p>
      <p v-if="!allowed" class="set-note error" role="alert">You no longer have permission to manage keys.</p>
      <div v-if="error" class="failure"><p class="set-note error" role="alert">{{ error }}</p><button type="button" class="btn sm" :disabled="busy" @click="load">Reload scopes</button></div>
    </div>
    <template #foot>
      <div class="foot">
        <section v-if="roleAdded.length && agentRole" class="role-confirm" aria-label="Confirm role changes" aria-live="polite">
          <h3>Also add to the agent's role?</h3>
          <p>Add <span class="mono">{{ roleAdded.join(', ') }}</span> to <strong>{{ agentRole.name }}</strong> and save this key's scopes together.</p>
          <p v-if="agentRole.member_count > 1">This role is shared by {{ agentRole.member_count }} people and agents. All of them receive the role permission; other keys keep their current scopes.</p>
          <p v-else>The role permission is available to this agent's other keys within their existing scopes.</p>
        </section>
        <div class="actions">
          <button type="button" class="btn" :disabled="busy" @click="emit('close')">Cancel</button>
          <button type="button" class="btn primary" :disabled="busy || !loaded || !allowed || !changed || invalid || selected.size > MAX_KEY_SCOPES" @click="save">{{ busy && loaded ? 'Saving…' : roleAdded.length ? 'Add to role and save scopes' : 'Save scopes' }}</button>
        </div>
      </div>
    </template>
  </AccessSheet>
</template>

<style scoped>
.body, .failure { display: grid; gap: 14px; min-width: 0; }
.note, .changes { font-size: 13px; line-height: 1.5; color: var(--ink-2); overflow-wrap: anywhere; }
.scopes { margin: 0; padding: 0; border: 0; min-width: 0; }
.scopes legend, .group-h { font: 500 10.5px/1.5 var(--mono); color: var(--ink-3); }
.scopes legend { margin-bottom: 8px; }
.scope-group { display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; }
.group-h { grid-column: 1 / -1; margin: 10px 0 4px; text-transform: uppercase; letter-spacing: .1em; }
.scope-row { display: grid; grid-template-columns: 16px minmax(0, 1fr); gap: 8px; padding: 7px 0; align-items: start; }
.scope-row input { width: 16px; height: 16px; margin: 2px 0 0; accent-color: var(--teal); }
.detail { font-size: 11px; color: var(--ink-3); }
.foot { display: grid; gap: 12px; width: 100%; min-width: 0; }
.actions { display: flex; justify-content: end; gap: 8px; }
.role-confirm { display: grid; gap: 8px; padding: 12px; border: 1px solid var(--line); border-radius: 10px; background: var(--surface-raised); font-size: 13px; line-height: 1.5; overflow-wrap: anywhere; }
.role-confirm h3 { font-size: 13px; font-weight: 600; }
.failure .btn, .scope-toggle { justify-self: start; }
@media (max-width: 600px) { .scope-group { grid-template-columns: minmax(0, 1fr); } .scope-row { min-height: 44px; align-items: center; } }
</style>
