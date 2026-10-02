<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { AccessError, type Agent, type AgentKeyCreated, agentScopeCeiling, createAgentKey, grantablePresetScopes, groupPermissions, keyHint, keyScopes, keyExpiryAfter, selectScopeGroup, lostPermission, matchesScope, MAX_KEY_SCOPES, type Permission, rotateAgentKey } from '../../lib/access'
import type { AgentKey } from '../../lib/settings'
import { can, myPermissions } from '../../lib/authz'
import { useAccess } from '../../stores/access'
import { agentLoginCommand } from '../../lib/agentLogin'
import { absoluteTime } from '../../lib/work'
import AppIcon from '../AppIcon.vue'
import AccessSheet from './AccessSheet.vue'
import ScopeCodeField from './ScopeCodeField.vue'
import ScopeDetails from './ScopeDetails.vue'
import ScopePresets from './ScopePresets.vue'
import KeyLifetime from './KeyLifetime.vue'
import { problem } from './accessText'

// A new key for an agent: what it may call (its scopes; none means nothing) and
// how long it works. It never does more than the agent's role allows. Its secret
// is shown once, here, to copy into the agent's configuration.
const props = defineProps<{ agent: Agent; rotateKey?: AgentKey; firstKey?: boolean; preferredPreset?: string }>()
const emit = defineEmits<{ close: []; created: [] }>()
const submitModifier = /Mac|iPhone|iPad/.test(navigator.platform) ? 'Cmd' : 'Ctrl'
const days = ref(90)
const busy = ref(false)
const error = ref('')
const created = ref<AgentKeyCreated | null>(null)
const access = useAccess()
const scopes = ref(new Set<string>(props.rotateKey?.scopes ?? []))
const rotationScopesChanged = ref(false)
const term = ref('')
// A scope can go on the key when I hold it and the agent's role allows it; the
// rest show, disabled, with the reason.
const mine = computed(() => myPermissions())
const currentAgent = computed(() => access.agent(props.agent.principal_id) ?? props.agent)
const ceiling = computed(() => agentScopeCeiling(currentAgent.value, access.roles, access.registry, !!props.rotateKey))
const codeCeiling = computed(() => agentScopeCeiling(currentAgent.value, access.roles, access.registry, true))
const held = computed(() => new Set([...mine.value].filter(k => !ceiling.value || ceiling.value.has(k))))
// Bulk actions respect an existing dedicated role too; only explicit scope
// choices may configure that role during creation.
const presetHeld = computed(() => new Set([...held.value].filter(k => codeCeiling.value?.has(k))))
// When my permissions or the agent's role shrink, scopes no longer allowed leave the selection.
watch([held, () => access.registry], () => { if (props.rotateKey && !rotationScopesChanged.value) return; const kept = grantablePresetScopes([...scopes.value], held.value, access.registry); if (kept.length !== scopes.value.size) scopes.value = new Set(kept) })
const why = (key: string) => !mine.value.has(key) ? 'you do not hold this' : `beyond ${props.agent.name}’s role${role.value ? ` (${role.value})` : ''}`
const available = computed(() => keyScopes(access.registry))
const groups = computed(() => groupPermissions(available.value.filter(p => matchesScope(p, term.value))))
const rotationScopes = computed(() => (props.rotateKey?.scopes ?? []).map(key => access.registry.find(p => p.key === key) ?? { key, group: 'Other', description: 'This scope is no longer in the permission registry.', risk: 'low', grantable_at: [], agent_grantable: false } as Permission).filter(p => matchesScope(p, term.value)))
const tried = ref(false)
function toggle(key: string) { if (busy.value || !held.value.has(key)) return; if (props.rotateKey) rotationScopesChanged.value = true; const next = new Set(scopes.value); if (next.has(key)) next.delete(key); else next.add(key); scopes.value = next }
function preset(keys: string[]) { if (busy.value || !allowed.value) return; if (props.rotateKey) rotationScopesChanged.value = true; scopes.value = new Set(grantablePresetScopes(keys, held.value, access.registry)) }
function selectGroup(keys: string[], all: boolean) {
  if (busy.value || !allowed.value) return
  if (props.rotateKey) rotationScopesChanged.value = true
  scopes.value = selectScopeGroup(scopes.value, keys, all, presetHeld.value, access.registry)
}
function applyCode(selected: Set<string>) { if (props.rotateKey) rotationScopesChanged.value = true; scopes.value = selected; term.value = '' }
function codeUnavailable(key: string): string | undefined {
  const permission = access.registry.find(p => p.key === key)
  if (!permission) return 'Unknown in this workspace'
  if (!permission.agent_grantable) return 'Unavailable to agent keys'
  if (codeCeiling.value && !codeCeiling.value.has(key)) return `beyond ${props.agent.name}’s role${role.value ? ` (${role.value})` : ''}`
  if (!held.value.has(key)) return why(key)
  return undefined
}
const allowed = computed(() => can('keys.manage'))
const scopeProblem = computed(() => !scopes.value.size ? 'Choose at least one thing it may do; a key without scopes can do nothing.'
  : scopes.value.size > MAX_KEY_SCOPES ? `A key holds at most ${MAX_KEY_SCOPES} scopes; clear ${scopes.value.size - MAX_KEY_SCOPES}.` : '')
const copied = ref(false)
const copyFallback = ref(false)
const tokenInput = ref<HTMLInputElement>()
const commandInput = ref<HTMLTextAreaElement>()
const commandCopied = ref(false)
const commandFallback = ref(false)
const loginCommand = agentLoginCommand(window.location.origin)
const role = computed(() => currentAgent.value.workspace_role?.name)
const rotationProblem = computed(() => props.rotateKey && !rotationScopesChanged.value && props.rotateKey.scopes.some(k => !held.value.has(k))
  ? 'The original scopes exceed what you or this agent’s role may grant. Rotation keeps those scopes, so it cannot continue.' : '')
async function create() {
  if (busy.value) return
  if (!allowed.value) { error.value = lostPermission('keys.manage'); return }
  tried.value = true
  if (rotationProblem.value || ((!props.rotateKey || rotationScopesChanged.value) && scopeProblem.value)) { document.getElementById('key-scopes')?.focus(); return }
  // Only scopes I may give go on the key.
  if ((!props.rotateKey || rotationScopesChanged.value) && grantablePresetScopes([...scopes.value], held.value, access.registry).length !== scopes.value.size) { scopes.value = new Set(grantablePresetScopes([...scopes.value], held.value, access.registry)); error.value = 'Some scopes are no longer yours to give and were cleared; check the list and create again.'; return }
  busy.value = true
  error.value = ''
  try {
    const expires = keyExpiryAfter(days.value)
    created.value = props.rotateKey ? await rotateAgentKey(props.rotateKey.id, expires, rotationScopesChanged.value ? [...scopes.value] : undefined) : await createAgentKey(props.agent, expires, [...scopes.value])
    emit('created')
    await nextTick()
    document.querySelector<HTMLElement>('.token-copy')?.focus()
  } catch (e) {
    error.value = e instanceof AccessError && e.status === 403 ? `The key was not created: it may only do what both you and ${props.agent.name}’s role may.` : problem(e, props.rotateKey ? 'Rotation could not be confirmed; reload the key list before trying again' : 'The key was not created')
  }
  finally { busy.value = false }
}
function selectKey(input: HTMLInputElement | undefined) {
  if (!input) return
  input.focus()
  input.setSelectionRange(0, input.value.length)
}
async function copy() {
  if (!created.value) return
  try { await navigator.clipboard.writeText(created.value.token); copied.value = true; copyFallback.value = false }
  catch { copied.value = false; copyFallback.value = true; selectKey(tokenInput.value) }
}
async function copyCommand() {
  try { await navigator.clipboard.writeText(loginCommand); commandCopied.value = true; commandFallback.value = false }
  catch { commandCopied.value = false; commandFallback.value = true; commandInput.value?.focus(); commandInput.value?.select() }
}
</script>

<template>
  <AccessSheet :title="created ? 'Key ready' : `${rotateKey ? 'Rotate key' : firstKey ? 'Create first key' : 'New key'} for ${agent.name}`" size="center" actions-first :submit-shortcut="!created" @submit="create" @close="busy || emit('close')">
    <div v-if="!created" class="body">
      <p v-if="rotateKey" class="note"><AppIcon name="refresh" :size="14" /><span>Rotate {{ keyHint(rotateKey.prefix) }}: create a replacement and revoke the old key immediately when you confirm. The same scopes are kept unless you change the selection. Copy the new key into {{ agent.name }}’s configuration to reconnect it.</span></p>
      <p v-else class="note"><AppIcon name="shield" :size="14" /><span>The key does only what you tick below, and never more than {{ agent.name }}’s role{{ role ? ` (${role})` : '' }} allows. Revoking it stops it at once.</span></p>
      <KeyLifetime v-model="days" :disabled="busy || !allowed" />
      <ScopeCodeField :unavailable="codeUnavailable" :disabled="busy || !allowed" @applied="applyCode" />
      <label class="search-field">
        <AppIcon name="search" :size="14" />
        <input v-model="term" class="field" type="search" placeholder="Find a scope by name, id or group" aria-label="Find a scope" autocomplete="off" spellcheck="false" />
      </label>
      <div v-if="rotateKey" class="rotation-scopes">
        <p class="label">{{ rotationScopesChanged ? 'Original scopes' : `${rotateKey.scopes.length} ${rotateKey.scopes.length === 1 ? 'scope' : 'scopes'} kept` }}</p>
        <p class="expiry-note">Search only filters the preview.{{ rotationScopesChanged ? '' : ' Rotation keeps every original scope unless you change the selection.' }}</p>
        <ScopeDetails v-for="scope in rotationScopes" :key="scope.key" :scope="scope" />
        <p v-if="!rotateKey.scopes.length" class="expiry-note">None; this key grants no access.</p>
        <p v-else-if="!rotationScopes.length" class="empty">No scope matches “{{ term }}”.</p>
        <p v-if="rotationProblem" class="field-error" role="alert">{{ rotationProblem }}</p>
      </div>
      <fieldset v-if="!rotateKey || rotationScopesChanged" id="key-scopes" class="scopes" tabindex="-1" :disabled="busy || !allowed" :aria-invalid="tried && !!scopeProblem" :aria-describedby="tried && scopeProblem ? 'key-scopes-error' : undefined">
        <legend class="label">What it may do <span class="count">{{ scopes.size }} chosen</span></legend>
        <ScopePresets :held="presetHeld" :registry="access.registry" :disabled="busy || !allowed" :preferred="preferredPreset" @apply="preset" />
        <button type="button" class="btn sm clear" :disabled="busy || !scopes.size" @click="preset([])">Clear</button>
        <div v-for="group in groups" :key="group.group" class="scope-group" role="group" :aria-label="group.group">
          <div class="group-h"><span>{{ group.group }}</span><span class="group-actions">
            <button type="button" class="btn sm ghost" :disabled="busy || !allowed || !group.items.some(p => presetHeld.has(p.key))" @click="selectGroup(group.items.map(p => p.key), true)">All</button>
            <button type="button" class="btn sm ghost" :disabled="busy || !allowed || !group.items.some(p => scopes.has(p.key))" @click="selectGroup(group.items.map(p => p.key), false)">None</button>
          </span></div>
          <label v-for="scope in group.items" :key="scope.key" class="scope-row" :class="{ off: !held.has(scope.key) }">
            <input type="checkbox" :checked="scopes.has(scope.key)" :disabled="busy || !allowed || !held.has(scope.key)" @change="toggle(scope.key)" />
            <ScopeDetails :scope="scope"><span v-if="!held.has(scope.key)" class="unavailable">{{ why(scope.key) }}</span></ScopeDetails>
          </label>
        </div>
        <p v-if="!groups.length" class="empty">No scope matches “{{ term }}”.</p>
        <p v-if="tried && scopeProblem" id="key-scopes-error" class="field-error" role="alert"><AppIcon name="alert" :size="12" />{{ scopeProblem }}</p>
      </fieldset>
      <p v-if="!allowed" class="set-note error" role="alert"><AppIcon name="shield" :size="14" />{{ lostPermission('keys.manage') }}</p>
      <p v-if="error" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
    </div>
    <div v-else class="body">
      <p class="once"><AppIcon name="info" :size="14" /><span>This key is shown only now; copy it before closing.</span></p>
      <div class="token">
        <input ref="tokenInput" class="field mono" readonly :value="created.token" aria-label="New agent key" @focus="selectKey($event.target as HTMLInputElement)" />
        <button type="button" class="btn token-copy" data-session-keep @click="copy"><AppIcon :name="copied ? 'check' : 'copy'" :size="14" />{{ copied ? 'Copied' : 'Copy key' }}</button>
      </div>
      <p class="expiry-note">{{ created.expires_at ? `Expires ${absoluteTime(created.expires_at)}.` : 'It never expires.' }}{{ rotateKey ? ' The old key is now revoked.' : '' }}</p>
      <p v-if="copied || copyFallback" class="copy-status" :class="{ 'sr-only': copied }" role="status">{{ copied ? 'Key copied.' : 'Clipboard unavailable. The key is selected; copy it with your keyboard or touch menu.' }}</p>
      <section class="cli-login" aria-labelledby="cli-login-title">
        <h3 id="cli-login-title">Use with the CLI</h3>
        <p class="expiry-note">Run this command, then paste the key at the hidden prompt.</p>
        <textarea ref="commandInput" class="field mono login-command" readonly :value="loginCommand" aria-label="CLI login command" rows="3" @focus="($event.target as HTMLTextAreaElement).select()" />
        <button type="button" class="btn sm" data-session-keep @click="copyCommand"><AppIcon :name="commandCopied ? 'check' : 'copy'" :size="14" />{{ commandCopied ? 'Command copied' : 'Copy command' }}</button>
        <p v-if="commandCopied || commandFallback" class="copy-status" :class="{ 'sr-only': commandCopied }" role="status">{{ commandCopied ? 'Command copied; paste the key only when prompted.' : 'Command selected; copy it with your keyboard or touch menu.' }}</p>
      </section>
    </div>
    <template #foot>
      <template v-if="!created">
        <button type="button" class="btn" :disabled="busy" @click="emit('close')">Cancel</button>
        <button type="button" class="btn primary" :disabled="busy || !allowed || !!rotationProblem" :data-tip="allowed ? undefined : lostPermission('keys.manage')" @click="create"><AppIcon name="key" :size="13" />{{ busy ? (rotateKey ? 'Rotating…' : 'Creating…') : (rotateKey ? 'Rotate key' : firstKey ? 'Create first key' : 'Create key') }}<kbd class="keycap" aria-hidden="true">{{ submitModifier }}<AppIcon name="enter" :size="12" /></kbd></button>
      </template>
      <button v-else type="button" class="btn primary" data-session-keep @click="emit('close')">Done</button>
    </template>
  </AccessSheet>
</template>

<style scoped>
.cli-login { display: grid; gap: 8px; padding-top: 4px; min-width: 0; }
.cli-login h3 { font-size: 14px; font-weight: 600; }
.cli-login .btn { justify-self: start; }
.login-command { height: auto; min-height: 88px; resize: none; overflow-wrap: anywhere; font-size: 12px; line-height: 1.6; }
.copy-status { font-size: 12.5px; color: var(--ink-2); line-height: 1.5; }
.expiry-note { font-size: 12.5px; line-height: 1.5; color: var(--ink-2); }
.rotation-scopes { display: grid; gap: 8px; }
.body { display: grid; gap: 14px; }
.note, .once { display: grid; grid-template-columns: 14px 1fr; gap: 8px; padding: 10px 12px; border-radius: 10px; background: var(--surface-2); font-size: 13px; line-height: 1.5; color: var(--ink-2); }
.note svg, .once svg { margin-top: 3px; color: var(--teal-ink); }
.label { padding: 0; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.token { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }
.scopes { display: grid; gap: 10px; margin: 0; padding: 0; border: 0; border-radius: 10px; }
.scopes:focus-visible { box-shadow: var(--focus-ring); }
.count { margin-left: 6px; letter-spacing: .04em; color: var(--ink-3); }
.clear { justify-self: start; }
.scope-group { display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; }
.group-h { display: flex; align-items: center; justify-content: space-between; gap: 8px; grid-column: 1 / -1; margin: 4px 0 2px; font: 600 10.5px/1.5 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.group-actions { display: flex; gap: 4px; }
.group-actions .btn { min-width: 44px; }
.scope-row { display: grid; grid-template-columns: 16px minmax(0, 1fr); align-items: start; gap: 8px; padding: 5px 0; cursor: pointer; }
.scope-row input { width: 16px; height: 16px; margin: 2px 0 0; accent-color: var(--teal); }
.unavailable { font-size: 11px; color: var(--ink-3); }
.scope-row.off { cursor: default; }
.empty { font-size: 13px; color: var(--ink-3); }
.field-error { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--danger); }
.token .field { font-size: 12.5px; }
@media (max-width: 600px) { .cli-login .btn { min-height: 44px; } .token { grid-template-columns: 1fr; } .token .btn { height: 44px; } .scope-row { min-height: 44px; align-items: center; } .scope-row input { margin: 0; } }
</style>
