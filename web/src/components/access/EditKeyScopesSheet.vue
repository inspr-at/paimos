<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { AccessError, changeAgentKeyScopes, getAgentKeyScopes, groupPermissions, keyHint, MAX_KEY_SCOPES, permissionLabel, type Agent } from '../../lib/access'
import { can } from '../../lib/authz'
import type { AgentKey } from '../../lib/settings'
import { useAccess } from '../../stores/access'
import AccessSheet from './AccessSheet.vue'
import AppIcon from '../AppIcon.vue'
import { problem } from './accessText'

const props = defineProps<{ agent: Agent; agentKey: AgentKey }>()
const emit = defineEmits<{ close: []; saved: [] }>()
const access = useAccess()
const original = ref<string[]>([])
const selected = ref(new Set<string>())
const grantable = ref(new Set<string>())
const loaded = ref(false)
const busy = ref(false)
const error = ref('')
const term = ref('')
const showUnavailable = ref(false)
const allowed = computed(() => can('keys.manage'))
const groups = computed(() => {
  const needle = term.value.trim().toLowerCase()
  // Keep obsolete or newly restricted scopes visible so they can be removed.
  const scopes = access.registry.filter(p => p.agent_grantable || original.value.includes(p.key))
  for (const key of original.value) if (!scopes.some(p => p.key === key)) scopes.push({ key, group: 'Other', description: key, risk: 'low', grantable_at: [], agent_grantable: false })
  return groupPermissions(scopes.filter(p => needle ? `${permissionLabel(p.key)} ${p.key} ${p.group}`.toLowerCase().includes(needle) : showUnavailable.value || grantable.value.has(p.key) || original.value.includes(p.key)))
})
const added = computed(() => [...selected.value].filter(k => !original.value.includes(k)))
const removed = computed(() => original.value.filter(k => !selected.value.has(k)))
const invalid = computed(() => [...selected.value].some(k => !grantable.value.has(k)))
const changed = computed(() => added.value.length + removed.value.length > 0)
async function load() {
  busy.value = true
  error.value = ''
  try {
    const view = await getAgentKeyScopes(props.agentKey.id)
    original.value = view.key.scopes
    selected.value = new Set(view.key.scopes)
    grantable.value = new Set(view.grantable_scopes)
    loaded.value = true
  } catch (e) { error.value = problem(e, 'The scopes could not be loaded') }
  finally { busy.value = false }
}
function toggle(key: string) {
  const next = new Set(selected.value)
  if (next.has(key)) next.delete(key)
  else if (grantable.value.has(key)) next.add(key)
  selected.value = next
}
async function save() {
  if (busy.value || !allowed.value || !changed.value || invalid.value || selected.value.size > MAX_KEY_SCOPES) return
  busy.value = true
  error.value = ''
  try {
    await changeAgentKeyScopes(props.agentKey.id, added.value, removed.value)
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
          <input v-model="term" class="field" type="search" aria-label="Find a scope" placeholder="Find a scope" data-autofocus autocomplete="off" />
        </label>
        <fieldset class="scopes" :disabled="busy || !allowed">
          <legend>{{ selected.size }} {{ selected.size === 1 ? 'scope' : 'scopes' }}</legend>
          <div v-for="group in groups" :key="group.group" class="scope-group" role="group" :aria-label="group.group">
            <p class="group-h">{{ group.group }}</p>
            <label v-for="scope in group.items" :key="scope.key" class="scope-row">
              <input type="checkbox" :checked="selected.has(scope.key)" :disabled="!grantable.has(scope.key) && !selected.has(scope.key)" @change="toggle(scope.key)" />
              <span class="scope-text"><span>{{ permissionLabel(scope.key) }}</span><span class="mono detail">{{ scope.key }}</span><span v-if="!grantable.has(scope.key)" class="detail">{{ selected.has(scope.key) ? 'Remove: outside current permissions' : 'Outside current permissions' }}</span></span>
            </label>
          </div>
          <p v-if="!groups.length" class="note">{{ term ? 'No matching scopes.' : 'No scopes are available within current permissions.' }}</p>
        </fieldset>
        <button v-if="!term" type="button" class="btn sm ghost scope-toggle" :aria-expanded="showUnavailable" @click="showUnavailable = !showUnavailable">{{ showUnavailable ? 'Hide unavailable scopes' : 'Show unavailable scopes' }}</button>
        <p v-if="!selected.size" class="note">This key will have no access.</p>
        <p v-else-if="invalid" class="note" role="alert">Remove scopes outside current permissions before saving.</p>
        <p v-if="selected.size > MAX_KEY_SCOPES" class="note" role="alert">Choose at most {{ MAX_KEY_SCOPES }} scopes.</p>
        <p v-if="changed" class="changes" aria-live="polite">{{ added.length }} added · {{ removed.length }} removed</p>
      </template>
      <p v-else-if="busy" class="note" role="status">Loading scopes…</p>
      <p v-if="!allowed" class="set-note error" role="alert">You no longer have permission to manage keys.</p>
      <div v-if="error" class="failure"><p class="set-note error" role="alert">{{ error }}</p><button type="button" class="btn sm" :disabled="busy" @click="load">Reload scopes</button></div>
    </div>
    <template #foot>
      <button type="button" class="btn" :disabled="busy" @click="emit('close')">Cancel</button>
      <button type="button" class="btn primary" :disabled="busy || !loaded || !allowed || !changed || invalid || selected.size > MAX_KEY_SCOPES" @click="save">{{ busy && loaded ? 'Saving…' : 'Save scopes' }}</button>
    </template>
  </AccessSheet>
</template>

<style scoped>
.body, .failure { display: grid; gap: 14px; min-width: 0; }
.note, .changes { font-size: 13px; line-height: 1.5; color: var(--ink-2); overflow-wrap: anywhere; }
.scopes { margin: 0; padding: 0; border: 0; min-width: 0; }
.scopes legend, .group-h { font: 500 10.5px/1.5 var(--mono); color: var(--ink-3); }
.scopes legend { margin-bottom: 8px; }
.scope-group { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 12px; }
.group-h { grid-column: 1 / -1; margin: 10px 0 4px; text-transform: uppercase; letter-spacing: .1em; }
.scope-row { display: grid; grid-template-columns: 16px minmax(0, 1fr); gap: 8px; padding: 7px 0; align-items: start; }
.scope-row input { width: 16px; height: 16px; margin: 2px 0 0; accent-color: var(--teal); }
.scope-text { display: grid; gap: 2px; font-size: 13px; line-height: 1.4; overflow-wrap: anywhere; }
.detail { font-size: 11px; color: var(--ink-3); }
.failure .btn, .scope-toggle { justify-self: start; }
@media (max-width: 600px) { .scope-group { grid-template-columns: minmax(0, 1fr); } .scope-row { min-height: 44px; align-items: center; } }
</style>
