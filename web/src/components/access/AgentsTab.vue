<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { brand } from '../../lib/brand'
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSession } from '../../stores/session'
import { CONNECTED_COMPUTER_REASON, agentDeactivatePoints, keyHint, revokeAgentKey, splitAgents, type Agent } from '../../lib/access'
import type { RowAction } from '../../lib/rowActions'
import { can, myPermissions } from '../../lib/authz'
import { confirmAction } from '../../lib/confirm'
import { keyState, listAgentKeys, type AgentKey } from '../../lib/settings'
import { toast } from '../../lib/toast'
import { relativeTime } from '../../lib/work'
import { useAccess } from '../../stores/access'
import AppIcon from '../AppIcon.vue'
import Avatar from '../Avatar.vue'
import RowMenu from '../business/RowMenu.vue'
import KeysTable from './KeysTable.vue'
import NewKeySheet from './NewKeySheet.vue'
import NewAgentSheet from './NewAgentSheet.vue'
import EditKeyScopesSheet from './EditKeyScopesSheet.vue'
import RolePicker from './RolePicker.vue'
import StatusChip from './StatusChip.vue'
import { problem, undoing } from './accessText'

// Agents: each with its role (what its keys can do at most) and its keys.
// Service principals run inside Aeon itself; they are shown as internal and are
// never offered keys. A person can deactivate an agent (its keys are revoked with
// it); deactivated agents rest in a folded group with a way back.
const access = useAccess()
const session = useSession()
const route = useRoute()
const router = useRouter()
const manageKeys = computed(() => session.identity?.principal.kind === 'person' && can('keys.manage'))
const creatingAgent = ref(false)
const firstKey = ref(false)
const firstKeyPreset = ref('')
watch([() => route.query.new, manageKeys], ([requested, allowed]) => { if (requested === '1' && allowed) creatingAgent.value = true }, { immediate: true })
function closeAgent() { creatingAgent.value = false; if (route.query.new) { const query = { ...route.query }; delete query.new; void router.replace({ query }) } }
async function agentCreated(agent: Agent, presetId: string) {
  closeAgent()
  // Let the old sheet return focus before the key sheet takes it.
  await nextTick()
  firstKey.value = true; firstKeyPreset.value = presetId; rotateKey.value = undefined; newKey.value = agent
  open.value = new Set(open.value).add(agent.principal_id)
  void access.load(true)
}
const manage = computed(() => can('members.manage'))
const keys = ref<AgentKey[] | null>(null)
const keysError = ref('')
const open = ref(new Set<string>())
const groups = computed(() => splitAgents(access.agents))
const working = computed(() => groups.value.working)
const deactivated = computed(() => groups.value.deactivated)
const deactivatedOpen = ref(false)
// Retiring an identity is person-only; the server refuses an agent key outright.
const retire = computed(() => session.identity?.principal.kind === 'person' && manage.value)
const service = computed(() => groups.value.service)
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
async function scopesSaved() { await Promise.all([loadKeys(), access.settle()]); toast('Key updated') }
const newKey = ref<Agent | null>(null)
const rotateKey = ref<AgentKey | undefined>()
function showKeySheet(agent: Agent, key?: AgentKey) { firstKey.value = agent.key_count === 0; firstKeyPreset.value = ''; rotateKey.value = key; newKey.value = agent }
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

// ---------- Deactivate / reactivate ----------
const menu = ref<{ agent: Agent; anchor: HTMLElement } | null>(null)
const root = ref<HTMLElement>()
const actions = computed<RowAction[]>(() => {
  const agent = menu.value?.agent
  return agent ? [{ id: 'deactivate', label: 'Deactivate…', icon: 'stop', group: 1, danger: true, reason: agent.connected_computer ? CONNECTED_COMPUTER_REASON : undefined }] : []
})
function openMenu(agent: Agent, anchor: HTMLElement) {
  if (menu.value?.agent.principal_id === agent.principal_id) { closeMenu(true); return }
  menu.value = { agent, anchor }
}
// Escape and a toggling click hand focus back to the trigger; a click elsewhere leaves it where the user put it.
function closeMenu(restoreFocus: boolean) {
  const anchor = menu.value?.anchor
  menu.value = null
  if (restoreFocus) anchor?.focus()
}
async function act(id: string) {
  const target = menu.value
  menu.value = null
  if (!target) return
  // The menu item that had focus leaves with the menu: the trigger takes focus first, so the
  // confirmation dialog opens from it and Cancel returns to it.
  target.anchor.focus()
  if (id === 'deactivate') await deactivate(target.agent)
}
// After a row leaves the working list its trigger is gone: focus the agent that took its place,
// else the Deactivated group, else the toolbar. Focus that is still somewhere in the tab stays put.
async function focusSurvivor(index: number) {
  await nextTick()
  const active = document.activeElement
  if (active && active !== document.body && root.value?.contains(active)) return
  const heir = working.value[Math.min(index, working.value.length - 1)]
  const target = (heir && root.value?.querySelector<HTMLElement>(`[data-actions-for="${heir.principal_id}"]`))
    ?? root.value?.querySelector<HTMLElement>('.fold')
    ?? root.value?.querySelector<HTMLElement>('.agent-toolbar .btn, .agent-toolbar a')
  target?.focus()
}
async function deactivate(agent: Agent) {
  const ok = await confirmAction({ title: `Deactivate ${agent.name}?`, points: agentDeactivatePoints(agent.key_count, !!agent.paired_computer), confirmLabel: agent.key_count ? 'Revoke keys and deactivate' : 'Deactivate', danger: true })
  if (!ok) return
  const index = working.value.findIndex(a => a.principal_id === agent.principal_id)
  try {
    await access.deactivate(agent.principal_id)
    void loadKeys()
    toast(`${agent.name} is deactivated`, { action: { label: 'Reactivate', run: () => void reactivate(agent) } })
    await focusSurvivor(index)
  } catch (e) { toast(problem(e, `${agent.name} stays active`), { tone: 'error' }) }
}
async function reactivate(agent: Agent) {
  try {
    await access.reactivate(agent.principal_id)
    // A computer never takes a key: it is connected by pairing it afresh.
    if (agent.paired_computer) toast(`${agent.name} is active again; connect the computer afresh`, { action: { label: 'Connect a computer', run: () => void router.push('/agents/register-agent') } })
    else toast(`${agent.name} is active again; add a new key to connect it`)
    await nextTick()
    const active = document.activeElement
    if (!active || active === document.body || !root.value?.contains(active)) root.value?.querySelector<HTMLElement>(`[data-actions-for="${agent.principal_id}"]`)?.focus()
  } catch (e) { toast(problem(e, `${agent.name} stays deactivated`), { tone: 'error' }) }
}
onMounted(loadKeys)
</script>

<template>
  <div ref="root" class="agents-tab">
    <div class="agent-toolbar" :class="{ 'empty-toolbar': !working.length }">
      <div class="lead-block">
        <p v-if="working.length" class="lead">Agent identities and keys for your CLIs and scripts.</p>
        <template v-else>
          <p class="lead"><RouterLink to="/agents/register-agent">Connect your machine (the agent daemon, no key to handle)</RouterLink></p>
          <p v-if="manageKeys" class="lead">New agent (a key for a CLI or script)</p>
        </template>
      </div>
      <button v-if="manageKeys" type="button" class="btn primary" @click="creatingAgent = true"><AppIcon name="plus" :size="14" />New agent</button>
    </div>
    <ul v-if="working.length" class="agents" :class="{ menus: retire }" aria-label="Agents">
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
          <button v-if="retire" type="button" class="icon-btn sm flat a-more" :data-actions-for="agent.principal_id" :aria-label="`Actions for ${agent.name}`" aria-haspopup="menu" :aria-expanded="menu?.agent.principal_id === agent.principal_id" @click="openMenu(agent, $event.currentTarget as HTMLElement)"><AppIcon name="more" :size="15" /></button>
        </div>
        <div v-if="open.has(agent.principal_id)" :id="`keys-${agent.principal_id}`" class="keys">
          <p v-if="keysError" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ keysError }}<button type="button" class="btn sm" @click="loadKeys">Try again</button></p>
          <div v-else-if="!keys" class="set-skeleton" role="status" aria-label="Loading keys"><span class="skeleton" /></div>
          <KeysTable v-else-if="keysOf(agent).length" :keys="keysOf(agent)" :revocable="manageKeys" :rotatable="manageKeys && !agent.paired_computer" :editable="manageKeys" @edit="editing = { agent, key: $event }" @revoke="revoke" @rotate="showKeySheet(agent, $event)" />
          <p v-else class="empty">{{ agent.paired_computer ? 'No key: a computer connects by pairing.' : 'No keys yet.' }}</p>
          <!-- A computer takes no key, even after its identity is reactivated: it is paired afresh. -->
          <RouterLink v-if="agent.paired_computer && !agent.connected_computer" class="btn sm" to="/agents/register-agent"><AppIcon name="plus" :size="13" />Connect a computer</RouterLink>
          <button v-else-if="!agent.paired_computer" type="button" class="btn sm" @click="showKeySheet(agent)"><AppIcon name="plus" :size="13" />{{ keysOf(agent).length ? 'New key' : 'Create first key' }}</button>
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

    <section v-if="deactivated.length" class="deactivated" aria-labelledby="deactivated-h">
      <h3 id="deactivated-h" class="fold-h">
        <button type="button" class="fold" :aria-expanded="deactivatedOpen" aria-controls="deactivated-rows" @click="deactivatedOpen = !deactivatedOpen">
          <AppIcon name="chevron" :size="12" class="chev" :class="{ open: deactivatedOpen }" />Deactivated<span class="count mono">{{ deactivated.length }}</span>
        </button>
      </h3>
      <ul v-if="deactivatedOpen" id="deactivated-rows" class="agents" aria-label="Deactivated agents">
        <li v-for="agent in deactivated" :key="agent.principal_id" class="agent off">
          <div class="row">
            <Avatar :id="agent.principal_id" :name="agent.name" kind="agent" :size="30" />
            <span class="a-text"><span class="a-name mono" :title="agent.name">{{ agent.name }}</span><span class="a-seen">{{ agent.last_seen_at ? `Seen ${relativeTime(agent.last_seen_at, { long: true })}` : 'Not seen yet' }}</span></span>
            <span class="a-role role-text">{{ agent.workspace_role?.name ?? (agent.project_roles?.length ? 'Project roles' : '') }}</span>
            <button v-if="retire" type="button" class="btn sm" :aria-label="`Reactivate ${agent.name}`" @click="reactivate(agent)"><AppIcon name="refresh" :size="13" />Reactivate</button>
          </div>
        </li>
      </ul>
    </section>

    <RowMenu v-if="menu" :anchor="menu.anchor" :items="actions" :label="`Actions for ${menu.agent.name}`" @select="act" @close="closeMenu" />
    <RolePicker
      v-if="picker" :anchor="picker.anchor" :subject="picker.agent.name" :roles="access.roles" :current="picker.agent.workspace_role?.id ?? null" :registry="access.registry"
      :mine="myPermissions()" scope="workspace" allow-none none-label="No role" :busy="busy" :can-apply="can('members.manage')" :error="roleError" @choose="chooseRole" @close="picker = null"
    />
    <EditKeyScopesSheet v-if="editing" :key="editing.key.id" :agent="editing.agent" :agent-key="editing.key" @close="editing = null" @saved="scopesSaved" />
    <NewAgentSheet v-if="creatingAgent" @close="closeAgent" @created="agentCreated" />
    <NewKeySheet v-if="newKey" :key="rotateKey?.id ?? newKey.principal_id" :first-key="firstKey" :preferred-preset="firstKeyPreset" :agent="newKey" :rotate-key="rotateKey" @close="newKey = null" @created="created" />
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
.menus .row { grid-template-columns: 30px minmax(0, 1fr) auto 150px 32px; }
.a-more { color: var(--ink-3); }
.off .row { grid-template-columns: 30px minmax(0, 1fr) auto auto; }
.off .a-name, .off .role-text { color: var(--ink-2); font-weight: 500; }
.off :deep(.avatar) { filter: grayscale(1); }
.deactivated { display: grid; gap: 2px; margin-top: 4px; }
.fold-h { margin: 0; font: inherit; }
.fold { display: inline-flex; align-items: center; gap: 8px; height: 34px; margin-left: -8px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font-size: 13px; font-weight: 600; }
.fold .count { margin-left: 2px; font-size: 11px; font-weight: 500; color: var(--ink-3); }
@media (hover: hover) { .fold:hover { background: var(--row-hover); color: var(--ink); } }
.fold:focus-visible { box-shadow: var(--focus-ring); }
.fold .chev { transform: rotate(-90deg); }
.fold .chev.open { transform: none; }
@media (prefers-reduced-motion: no-preference) { .fold .chev { transition: transform .15s ease; } }
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
  .row, .menus .row { grid-template-columns: 30px minmax(0, 1fr) auto; grid-template-areas: "av text keys" ". role more"; row-gap: 4px; padding: 8px 0; }
  .row > :first-child { grid-area: av; }
  .a-text { grid-area: text; }
  .a-role, .row > .status { grid-area: role; justify-self: start; }
  .keys-btn, .keys-count { grid-area: keys; }
  .a-more { grid-area: more; justify-self: end; width: 44px; height: 44px; }
  .off .row { grid-template-areas: "av text text" ". role act"; }
  .off .row > .btn { grid-area: act; justify-self: end; height: 44px; }
  .role-btn, .keys-btn { height: 44px; }
  .keys { padding-left: 0; }
  .keys .btn { height: 44px; }
}
</style>
