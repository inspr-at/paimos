<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { brand } from '../../lib/brand'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSession } from '../../stores/session'
import { keyHint, revokeAgentKey, type Agent } from '../../lib/access'
import { can, myPermissions } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { keyState, listAgentKeys, type AgentKey } from '../../lib/settings'
import { toast } from '../../lib/toast'
import { relativeTime } from '../../lib/work'
import { useAccess } from '../../stores/access'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import KeysTable from './KeysTable.vue'
import NewKeySheet from './NewKeySheet.vue'
import NewAgentSheet from './NewAgentSheet.vue'
import EditKeyScopesSheet from './EditKeyScopesSheet.vue'
import RolePicker from './RolePicker.vue'
import StatusChip from './StatusChip.vue'
import { problem, undoing } from './accessText'

// Agents: each with its role (what its keys can do at most) and its keys.
// Service principals run inside Aeon itself; they are shown as internal and are
// never offered keys.
const access = useAccess()
const session = useSession()
const route = useRoute()
const router = useRouter()
const manageKeys = computed(() => session.identity?.principal.kind === 'person' && can('keys.manage'))
const creatingAgent = ref(false)
const firstKey = ref(false)
watch([() => route.query.new, manageKeys], ([requested, allowed]) => { if (requested === '1' && allowed) creatingAgent.value = true }, { immediate: true })
function closeAgent() { creatingAgent.value = false; if (route.query.new) { const query = { ...route.query }; delete query.new; void router.replace({ query }) } }
async function agentCreated(agent: Agent) {
  closeAgent()
  // Let the old sheet return focus before the key sheet takes it.
  await nextTick()
  firstKey.value = true; rotateKey.value = undefined; newKey.value = agent
  open.value = new Set(open.value).add(agent.principal_id)
  void access.load(true)
}
const manage = computed(() => can('members.manage'))
const keys = ref<AgentKey[] | null>(null)
const keysError = ref('')
const open = ref(new Set<string>())
const working = computed(() => access.agents.filter(a => !a.service))
const service = computed(() => access.agents.filter(a => a.service))
const keysOf = (agent: Agent) => (keys.value ?? []).filter(k => k.principal_id === agent.principal_id).sort((a, b) => Number(keyState(a) !== 'active') - Number(keyState(b) !== 'active') || Date.parse(b.created_at) - Date.parse(a.created_at))
async function loadKeys() {
  if (!manageKeys.value) return
  keysError.value = ''
  try { keys.value = await listAgentKeys() } catch { keysError.value = 'The agent keys could not be loaded.' }
}
function toggle(agent: Agent) { const next = new Set(open.value); if (next.has(agent.principal_id)) next.delete(agent.principal_id); else next.add(agent.principal_id); open.value = next }
async function revoke(key: AgentKey) {
  const ok = await confirmAction({ title: `Revoke the key ${keyHint(key.prefix)}?`, points: [`Anything using it is refused from now on; ${key.name} keeps its other keys.`, 'A revoked key cannot be turned back on. Create a new one instead.'], confirmLabel: 'Revoke key', danger: true })
  if (!ok) return
  try { await revokeAgentKey(key.id); await Promise.all([loadKeys(), access.load(true)]); toast(`The key ${keyHint(key.prefix)} is revoked`) }
  catch (e) { toast(problem(e, 'The key stays active'), { tone: 'error' }) }
}
const editing = ref<{ agent: Agent; key: AgentKey } | null>(null)
async function scopesSaved() { await loadKeys(); toast('Key scopes updated') }
const newKey = ref<Agent | null>(null)
const rotateKey = ref<AgentKey | undefined>()
function showKeySheet(agent: Agent, key?: AgentKey) { firstKey.value = agent.key_count === 0; rotateKey.value = key; newKey.value = agent }
async function created() { await Promise.all([loadKeys(), access.load(true)]); if (newKey.value) open.value = new Set(open.value).add(newKey.value.principal_id) }
const picker = ref<{ agent: Agent; anchor: HTMLElement } | null>(null)
const busy = ref(false)
// Why the server refused a role change, shown in the open picker.
const roleError = ref('')
watch(picker, () => { roleError.value = '' })
async function chooseRole(roleId: string | null) {
  const target = picker.value
  if (!target) return
  busy.value = true
  const before = target.agent.workspace_role?.id ?? null
  try {
    await access.setWorkspaceRole(target.agent.principal_id, roleId)
    picker.value = null
    toast(`${target.agent.name} is now ${access.roleById.get(roleId ?? '')?.name ?? 'without a workspace role'}; its keys follow`, { action: { label: 'Undo', run: () => undoing(access.setWorkspaceRole(target.agent.principal_id, before), `${target.agent.name}’s role could not be put back`) } })
  } catch (e) { roleError.value = problem(e, `${target.agent.name} keeps its role`) }
  finally { busy.value = false }
}
onMounted(loadKeys)
</script>

<template>
  <div class="agents-tab">
    <div class="agent-toolbar" :class="{ 'empty-toolbar': !working.length }">
      <div class="lead-block">
        <p v-if="working.length" class="lead">Agent identities and keys for your CLIs and scripts.</p>
        <template v-else>
          <p class="lead"><RouterLink to="/agents/register-agent">Connect a computer (the agent daemon, no key to handle)</RouterLink></p>
          <p v-if="manageKeys" class="lead">New agent (a key for a CLI or script)</p>
        </template>
      </div>
      <button v-if="manageKeys" type="button" class="btn primary" @click="creatingAgent = true"><AppIcon name="plus" :size="14" />New agent</button>
    </div>
    <ul v-if="working.length" class="agents" aria-label="Agents">
      <li v-for="agent in working" :key="agent.principal_id" class="agent">
        <div class="row">
          <Avatar :id="agent.principal_id" :name="agent.name" kind="agent" :size="30" />
          <span class="a-text"><span class="a-name mono" :title="agent.name">{{ agent.name }}</span><span v-if="agent.description" class="a-description" :title="agent.description">{{ agent.description }}</span><span class="a-seen">{{ agent.last_seen_at ? `Seen ${relativeTime(agent.last_seen_at, { long: true })}` : 'Not seen yet' }}</span></span>
          <span class="a-role">
            <button v-if="manage" type="button" class="role-btn" aria-haspopup="dialog" :aria-label="`Role of ${agent.name}: ${agent.workspace_role?.name ?? 'none'}. Change`" @click="picker = { agent, anchor: $event.currentTarget as HTMLElement }">{{ agent.workspace_role?.name ?? (agent.project_roles?.length ? 'Project roles' : 'No role') }}<AppIcon name="chevron" :size="12" class="chev" /></button>
            <span v-else class="role-text">{{ agent.workspace_role?.name ?? (agent.project_roles?.length ? 'Project roles' : 'No role') }}</span>
          </span>
          <button v-if="manageKeys" type="button" class="keys-btn" :aria-expanded="open.has(agent.principal_id)" :aria-controls="`keys-${agent.principal_id}`" @click="toggle(agent)">
            <AppIcon name="key" :size="13" />{{ agent.key_count === 1 ? '1 active key' : `${agent.key_count} active keys` }}<AppIcon name="chevron" :size="12" class="chev" :class="{ open: open.has(agent.principal_id) }" />
          </button>
          <span v-else class="keys-count">{{ agent.key_count === 1 ? '1 active key' : `${agent.key_count} active keys` }}</span>
        </div>
        <div v-if="open.has(agent.principal_id)" :id="`keys-${agent.principal_id}`" class="keys">
          <p v-if="keysError" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ keysError }}<button type="button" class="btn sm" @click="loadKeys">Try again</button></p>
          <div v-else-if="!keys" class="set-skeleton" role="status" aria-label="Loading keys"><span class="skeleton" /></div>
          <KeysTable v-else-if="keysOf(agent).length" :keys="keysOf(agent)" :revocable="manageKeys" :rotatable="manageKeys" :editable="manageKeys" @edit="editing = { agent, key: $event }" @revoke="revoke" @rotate="showKeySheet(agent, $event)" />
          <p v-else class="empty">No keys yet.</p>
          <button type="button" class="btn sm" @click="showKeySheet(agent)"><AppIcon name="plus" :size="13" />{{ keysOf(agent).length ? 'New key' : 'Create first key' }}</button>
        </div>
      </li>
    </ul>


    <section v-if="service.length" class="internal" aria-labelledby="internal-h">
      <h3 id="internal-h" class="group-h">Internal <span class="count mono">{{ service.length }}</span></h3>
      <p class="lead">These run inside {{ brand.short_name }} itself (imports, bootstrap, the public quote page). They act for the workspace, never with a key.</p>
      <ul class="agents">
        <li v-for="agent in service" :key="agent.principal_id" class="agent service">
          <div class="row">
            <Avatar :id="agent.principal_id" :name="agent.name" kind="agent" :size="30" />
            <span class="a-text"><span class="a-name">{{ agent.name }}</span><span class="a-seen">{{ agent.last_seen_at ? `Active ${relativeTime(agent.last_seen_at, { long: true })}` : '' }}</span></span>
            <StatusChip tone="info" label="Internal" />
            <span class="keys-count" data-tip="Service principals are never given keys">No keys</span>
          </div>
        </li>
      </ul>
    </section>

    <RolePicker
      v-if="picker" :anchor="picker.anchor" :subject="picker.agent.name" :roles="access.roles" :current="picker.agent.workspace_role?.id ?? null" :registry="access.registry"
      :mine="myPermissions()" scope="workspace" allow-none none-label="No role" :busy="busy" :can-apply="can('members.manage')" :error="roleError" @choose="chooseRole" @close="picker = null"
    />
    <EditKeyScopesSheet v-if="editing" :agent="editing.agent" :agent-key="editing.key" @close="editing = null" @saved="scopesSaved" />
    <NewAgentSheet v-if="creatingAgent" @close="closeAgent" @created="agentCreated" />
    <NewKeySheet v-if="newKey" :first-key="firstKey" :agent="newKey" :rotate-key="rotateKey" @close="newKey = null" @created="created" />
  </div>
</template>

<style scoped>
.agents-tab { display: grid; grid-template-columns: minmax(0, 1fr); gap: 12px; }
.agent-toolbar { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 12px; }
.agent-toolbar .btn { flex-shrink: 0; }
.empty-toolbar { padding: 8px 0; align-items: flex-end; }
.lead-block { display: grid; gap: 2px; min-width: 0; flex: 1 1 16rem; }
.lead { font-size: 13px; line-height: 1.5; color: var(--ink-2); overflow-wrap: break-word; }
.lead a { color: var(--teal-ink); text-decoration: underline; text-underline-offset: 3px; }
.agents { display: grid; margin: 0; padding: 0; list-style: none; }
.agent { border-bottom: 1px solid var(--line); }
.row { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto 150px; align-items: center; gap: 12px; min-height: 58px; }
.a-text { display: grid; gap: 1px; min-width: 0; }
.a-name { font-size: 13.5px; font-weight: 600; color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.a-description { font-size: 12px; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.a-name.mono { font-size: 12.5px; }
.a-seen { font-size: 12px; color: var(--ink-3); }
.a-role { display: flex; align-items: center; }
.role-btn svg, .keys-btn svg { display: block; flex-shrink: 0; }
.role-btn, .keys-btn { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; line-height: 20px; font-weight: 600; white-space: nowrap; }
.keys-btn { justify-self: end; font-weight: 500; color: var(--ink-2); }
.keys-btn svg:first-child { color: var(--ink-3); }
@media (hover: hover) { .role-btn:hover, .keys-btn:hover { background: var(--surface-raised); box-shadow: inset 0 0 0 1px var(--line-2); } }
.role-btn:focus-visible, .keys-btn:focus-visible { box-shadow: var(--focus-ring); }
.role-text { font-size: 13px; font-weight: 600; }
.chev { color: var(--ink-3); }
.chev.open { transform: rotate(180deg); }
.keys-count { justify-self: end; font-size: 12.5px; color: var(--ink-3); }
.keys { display: grid; gap: 10px; justify-items: start; padding: 2px 0 14px 42px; }
.keys > :first-child { width: 100%; }
.empty { font-size: 13px; color: var(--ink-3); }
.internal { display: grid; gap: 6px; margin-top: 8px; }
.group-h { display: flex; align-items: baseline; gap: 6px; margin: 0; font: 600 10.5px/1.5 var(--mono); letter-spacing: .14em; text-transform: uppercase; color: var(--ink-3); }
.count { letter-spacing: 0; }
@media (max-width: 600px) {
  .agent-toolbar .btn { min-height: 44px; }
  .row { grid-template-columns: 30px minmax(0, 1fr) auto; grid-template-areas: "av text keys" ". role role"; row-gap: 4px; padding: 8px 0; }
  .row > :first-child { grid-area: av; }
  .a-text { grid-area: text; }
  .a-role, .row > .status { grid-area: role; justify-self: start; }
  .keys-btn, .keys-count { grid-area: keys; }
  .role-btn, .keys-btn { height: 44px; }
  .keys { padding-left: 0; }
  .keys .btn { height: 44px; }
}
</style>
