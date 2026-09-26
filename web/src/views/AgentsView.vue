<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { can } from '../lib/authz'
import { message, subscribeAgents, type AgentAccount, type Approval, type SessionControl } from '../lib/agents'
import { canDecideApproval as allowedToDecide, controlBlocked, decidedApprovals, type Resource } from '../lib/agentState'
import { confirmAction } from '../lib/confirm'
import { toast } from '../lib/toast'
import { useAgents, type HeldRequest, type SessionView } from '../stores/agents'
import { useProjects } from '../stores/projects'
import { useSession } from '../stores/session'
import AppIcon from '../components/AppIcon.vue'
import AccountsCard from '../components/agents/AccountsCard.vue'
import ApprovalQueue from '../components/agents/ApprovalQueue.vue'
import SessionList from '../components/agents/SessionList.vue'
import SessionPanel from '../components/agents/SessionPanel.vue'
import LiveNow from '../components/agents/LiveNow.vue'
import StartAgentDialog from '../components/agents/StartAgentDialog.vue'
import RunQueue from '../components/agents/RunQueue.vue'

// Markus's desk for agents: what waits on him first, then every live session grouped
// by state, with accounts and pacing beside them. A session opens in the docked panel.
const agents = useAgents()
const projects = useProjects()
const session = useSession()
const route = useRoute()
const router = useRouter()
const cursor = ref('')
const live = ref(false)
const stale = computed(() => agents.sessionsState === 'error' || (agents.sessionsUpdatedAt !== null && agents.now - agents.sessionsUpdatedAt > 45_000))
const updatedTime = computed(() => agents.sessionsUpdatedAt === null ? '' : new Date(agents.sessionsUpdatedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }))
const queue = ref<InstanceType<typeof ApprovalQueue>>()
const startDialog = ref<InstanceType<typeof StartAgentDialog>>()
const canStart = computed(() => session.identity?.principal.kind === 'person' && can('work_orders.write') && can('run.create'))

const sessionId = computed(() => typeof route.params.sessionId === 'string' ? route.params.sessionId : '')
const selected = computed(() => agents.views.find(v => v.session.id === sessionId.value))
const writable = computed(() => can('harness.control'))
const canResolve = computed(() => session.identity?.principal.kind === 'person' && can('inbox.manage'))
const canRevoke = computed(() => session.identity?.principal.kind === 'person' && can('approvals.revoke'))
const canDecide = computed(() => session.identity?.principal.kind === 'person' && (can('approvals.decide') || canResolve.value))
const canDecideApproval = (approval: Approval) => session.identity?.principal.kind === 'person' && allowedToDecide(approval, can)
const history = computed(() => decidedApprovals(agents.approvals, agents.now))
const counts = computed(() => ({ working: agents.grouped.working.length, idle: agents.grouped.idle.length }))
const summary = computed(() => {
  const parts: string[] = []
  if (agents.needsCount) parts.push(`${agents.needsCount} ${agents.needsCount === 1 ? 'needs' : 'need'} you`)
  if (agents.sessionsState === 'ready' && agents.loaded) parts.push(...(agents.sessions.length ? [`${counts.value.working} working`, `${counts.value.idle} idle`] : ['No agent connected yet']))
  return parts.join(' · ') || (agents.loaded ? 'Nothing waits on you' : '')
})

// ---------- Resources and people ----------
function resource(approval: Approval): Resource {
  if (approval.resource_kind === 'tenant') return { label: 'the whole workspace' }
  if (approval.resource_kind === 'run') {
    // A run is shown by the ticket its session works on.
    const run = approval.run_id ?? approval.resource_id
    const ticket = agents.views.find(v => v.session.run_id === run && v.ticket)?.ticket
    return ticket ? { label: ticket.title, key: ticket.key, title: ticket.title, href: ticket.href } : { label: 'its current run' }
  }
  const id = approval.resource_id ?? ''
  const project = projects.byId(id)
  if (project) return { label: project.title, key: project.routeKey, title: project.title, href: `/p/${encodeURIComponent(project.routeKey)}` }
  const node = agents.nodes[id]
  if (!node) return { label: 'a ticket' }
  const owner = projects.byRouteKey(node.key.split('-')[0] ?? '')
  return { label: node.title, key: node.key, title: node.title, href: owner ? `/p/${encodeURIComponent(owner.routeKey)}/${encodeURIComponent(node.key)}` : undefined }
}
function openAgent(principalId: string) {
  const views = agents.byAgent(principalId)
  const target = views.find(v => v.status.group !== 'stopped') ?? views[0]
  if (target) openSession(target.session.id)
  else toast('This agent has no session listed right now.')
}

// ---------- Actions ----------
async function decide(approval: Approval, decision: 'approved' | 'denied', reason: string) {
  await agents.decide(approval, decision, reason)
  toast(`${decision === 'approved' ? 'Approved' : 'Denied'}: ${agents.askerName(approval.agent_principal_id, approval.agent_name).name} was told.`)
  await nextTick()
  const next = agents.pending[0]
  cursor.value = next ? `a:${next.id}` : ''
  if (next) focusRow(cursor.value)
}
async function resolveHeld(request: HeldRequest, decision: 'resolved' | 'dismissed', note: string) {
  await agents.resolve(request, decision, note)
  toast(`${decision === 'resolved' ? 'Resolved' : 'Dismissed'}: the request from ${agents.askerName(request.sender_principal_id).name} is answered.`)
  await nextTick()
  const next = agents.pending[0] ?? null
  const nextHeld = agents.held[0] ?? null
  cursor.value = next ? `a:${next.id}` : nextHeld ? `m:${nextHeld.id}` : ''
  if (cursor.value) focusRow(cursor.value)
}
const controlBlock = (view: SessionView, kind: SessionControl['kind']) => controlBlocked(view.session, kind, view.name, writable.value, agents.controls[view.session.id])
async function control(view: SessionView, kind: SessionControl['kind']) {
  if (kind === 'stop') {
    const ok = await confirmAction({
      title: `Stop ${view.name}?`, danger: true, confirmLabel: 'Stop session',
      body: `${view.harness} ends this session after its current step. It cannot be resumed; the next session starts fresh.`,
    })
    if (!ok) return
  }
  try {
    await agents.control(view, kind)
    toast(kind === 'stop' ? `Stop sent to ${view.name}.` : `Interrupt sent to ${view.name}.`)
  } catch (e) { toast(message(e), { tone: 'error' }) }
}
async function setAccount(account: AgentAccount, state: AgentAccount['state']) {
  if (state === 'draining') {
    const ok = await confirmAction({ title: `Drain ${account.label}?`, body: 'Running work finishes; no new runs start on this account until you resume it.', confirmLabel: 'Drain account' })
    if (!ok) return
  }
  await agents.setAccount(account, state)
}
function review(approval: Approval) {
  if (window.innerWidth < 1100) void closePanel()
  cursor.value = `a:${approval.id}`
  void nextTick(() => focusRow(cursor.value))
}

// ---------- Panel ----------
// Like the ticket panel: opening from the list adds one history entry, switching
// sessions replaces it, and closing goes back to the list entry instead of adding one.
let openedFromList = false
function openSession(id: string) {
  cursor.value = `s:${id}`
  if (sessionId.value) { void router.replace({ path: `/agents/${id}`, query: route.query }); return }
  openedFromList = true
  void router.push({ path: `/agents/${id}`, query: route.query })
}
// Focus returns to the session's row once the panel is gone.
async function closePanel() {
  const id = sessionId.value
  if (id) cursor.value = `s:${id}`
  const back = openedFromList && typeof window.history.state?.back === 'string' && window.history.state.back.startsWith('/agents')
  openedFromList = false
  if (back) {
    const closed = new Promise<void>(resolve => { const stop = watch(sessionId, value => { if (!value) { stop(); resolve() } }); setTimeout(() => { stop(); resolve() }, 1000) })
    router.back()
    await closed
  }
  else await router.replace({ path: '/agents', query: route.query })
  await nextTick()
  if (id) focusRow(cursor.value)
}

// ---------- Keyboard: j/k move, a approve, d deny, Enter opens, Esc closes ----------
function rows() { return [...document.querySelectorAll<HTMLElement>('.agents-page [data-row]')] }
function focusRow(id: string) {
  const el = document.querySelector<HTMLElement>(`.agents-page [data-row="${CSS.escape(id)}"]`)
  el?.focus({ preventScroll: true })
  el?.scrollIntoView({ block: 'nearest' })
}
function move(step: number) {
  const list = rows()
  if (!list.length) return
  const index = list.findIndex(el => el.dataset.row === cursor.value)
  const next = list[index === -1 ? (step > 0 ? 0 : list.length - 1) : Math.max(0, Math.min(list.length - 1, index + step))]
  cursor.value = next.dataset.row ?? ''
  focusRow(cursor.value)
  // With the panel open, the panel follows the cursor through sessions.
  if (sessionId.value && cursor.value.startsWith('s:')) void router.replace({ path: `/agents/${cursor.value.slice(2)}`, query: route.query })
}
function typing(target: EventTarget | null) {
  return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
}
function keydown(event: KeyboardEvent) {
  if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey) return
  if (document.querySelector('dialog[open], .floating') || typing(event.target)) return
  const [kind, id] = [cursor.value.slice(0, 1), cursor.value.slice(2)]
  switch (event.key) {
    case 'j': case 'ArrowDown': event.preventDefault(); move(1); break
    case 'k': case 'ArrowUp': event.preventDefault(); move(-1); break
    case 'a': case 'd':
      if (kind === 'a') { event.preventDefault(); void queue.value?.begin(id, event.key === 'a' ? 'approve' : 'deny') }
      else if (kind === 'm') { event.preventDefault(); void queue.value?.begin(id, event.key === 'a' ? 'resolve' : 'dismiss') }
      break
    case 'Enter': case 'o':
      if ((event.target as HTMLElement).closest('a, button')) return
      if (kind === 's') { event.preventDefault(); openSession(id) }
      else if (kind === 'm') { event.preventDefault(); const held = agents.held.find(m => m.id === id); if (held) openAgent(held.sender_principal_id) }
      break
    case 'Escape':
      if (sessionId.value) { event.preventDefault(); void closePanel() }
      break
  }
}

// ---------- Live ----------
let stop: (() => void) | undefined
let poll: ReturnType<typeof setInterval> | undefined
let clock: ReturnType<typeof setInterval> | undefined
let debounce: ReturnType<typeof setTimeout> | undefined
function changed() {
  if (document.visibilityState === 'hidden' || debounce) return
  // A fixed batch window cannot be starved by a stream of new worker events.
  debounce = setTimeout(() => { debounce = undefined; void agents.loadAll() }, 400)
}
function visibilityChanged() {
  if (document.visibilityState !== 'visible') return
  clearTimeout(debounce); debounce = undefined
  agents.tick()
  void agents.loadAll()
}
onMounted(() => {
  void agents.loadAll()
  stop = subscribeAgents(changed, value => { live.value = value })
  poll = setInterval(() => { if (document.visibilityState !== 'hidden') void agents.loadAll() }, 20_000)
  clock = setInterval(() => agents.tick(), 1000)
  window.addEventListener('keydown', keydown)
  document.addEventListener('visibilitychange', visibilityChanged)
})
onBeforeUnmount(() => {
  stop?.(); clearInterval(poll); clearInterval(clock); clearTimeout(debounce)
  window.removeEventListener('keydown', keydown)
  document.removeEventListener('visibilitychange', visibilityChanged)
})
watch(sessionId, id => { if (id) cursor.value = `s:${id}` }, { immediate: true })
</script>

<template>
  <section class="agents-page" :class="{ 'panel-open': !!sessionId }" aria-labelledby="agents-title">
    <header class="page-head">
      <div class="head-main">
        <p class="eyebrow">{{ session.identity?.tenant.name ?? 'Workspace' }}</p>
        <h1 id="agents-title">Agents</h1>
        <p class="summary"><span v-if="summary">{{ summary }}</span><span v-else class="skeleton summary-skeleton" /></p>
      </div>
      <div class="head-side">
        <button v-if="canStart" type="button" class="btn primary start-agent" @click="startDialog?.open()"><AppIcon name="plus" :size="15" />Start agent</button>
        <RouterLink v-if="can('keys.manage')" class="context-link" to="/settings/access/agents">Agent keys<AppIcon name="arrow" :size="13" /></RouterLink>
        <div class="freshness">
          <p class="live" :class="{ on: live && !stale }" :data-tip="live ? 'Connected to live updates' : 'Refreshing every 20 seconds'">
            <span class="live-mark" aria-hidden="true" />{{ stale ? 'Update delayed' : live ? 'Live' : 'Polling' }}
          </p>
          <span class="last-updated" role="status">
            <template v-if="agents.sessionsUpdatedAt !== null">Updated <time :datetime="new Date(agents.sessionsUpdatedAt).toISOString()">{{ updatedTime }}</time></template>
            <template v-else>Waiting for first update</template>
          </span>
        </div>
      </div>
    </header>

    <!-- The first load swaps the loading layout for the real one in one step, so the
         page does not jump as each read lands. -->
    <div :key="agents.loaded ? 'ready' : 'loading'" class="layout">
      <div class="main-col">
        <LiveNow :views="agents.views" :now="agents.now" :selected="sessionId" :loaded="agents.loaded" :can-start="canStart" @open="id => id ? openSession(id) : startDialog?.open()" />
        <ApprovalQueue
          v-if="!agents.loaded || agents.needsCount || history.length"
          ref="queue" :pending="agents.pending" :held="agents.held" :history="history" :now="agents.now" :loaded="agents.loaded"
          :cursor="cursor" :can-decide="canDecide" :can-decide-approval="canDecideApproval" :can-resolve="canResolve" :can-revoke="canRevoke" :asker="agents.askerName" :resource="resource" :decide="decide" :revoke="agents.revoke" :resolve="resolveHeld"
          @focus-row="id => cursor = id" @open-agent="openAgent"
        />
        <p v-if="agents.approvalsState === 'error'" class="inline-error" role="alert"><AppIcon name="alert" :size="14" />Permission requests could not be loaded: {{ agents.approvalsError }} <button type="button" class="btn sm" @click="agents.refreshApprovals()">Try again</button></p>
        <SessionList
          v-if="agents.loaded"
          :groups="agents.grouped" :now="agents.now" :cursor="cursor" :selected="sessionId" :state="agents.sessionsState" :error="agents.sessionsError"
          :loaded="agents.loaded" :controls="agents.controls" :can-control="writable" :can-start="canStart"
          @open="openSession" @control="control" @focus-row="id => cursor = id" @retry="agents.loadAll()" @start="startDialog?.open()"
        />
        <RunQueue v-if="agents.loaded" />
        <p v-if="agents.loaded && (agents.sessions.length || agents.pending.length)" class="hint" aria-hidden="true">
          <kbd class="keycap">j</kbd><kbd class="keycap">k</kbd> move · <kbd class="keycap"><AppIcon name="enter" /></kbd> open · <kbd class="keycap">a</kbd> approve · <kbd class="keycap">d</kbd> deny
        </p>
      </div>
      <aside v-if="agents.loaded" class="side-col" aria-label="Accounts">
        <AccountsCard :accounts="agents.accounts" :state="agents.accountsState" :now="agents.now" :admin="agents.accountsState === 'ready'" :set="setAccount" />
      </aside>
    </div>

    <SessionPanel
      v-if="sessionId && agents.loaded" :view="selected" :loading="false" :now="agents.now" :can-write="writable" :control-block="controlBlock"
      @close="closePanel" @control="control" @review="review"
    />
    <StartAgentDialog ref="startDialog" />
  </section>
</template>

<style scoped>
.agents-page { width: 100%; margin: 0; padding: 22px var(--gutter) 24px; }
.page-head { display: flex; align-items: flex-end; justify-content: space-between; gap: 24px; margin-bottom: 20px; }
.page-head h1 { margin-top: 6px; }
.head-side { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.start-agent { min-height: 44px; }
/* In-context link to the matching settings. */
.context-link { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 10px; border-radius: 999px; color: var(--teal-ink); font-size: 13px; font-weight: 600; white-space: nowrap; }
@media (max-width: 600px) {
  .page-head { flex-direction: column; align-items: stretch; gap: 8px; }
  .head-side { justify-content: space-between; margin: 0 -10px 0 0; }
  .context-link { height: 44px; margin-left: -10px; }
}
@media (hover: hover) { .context-link:hover { background: var(--row-hover); } }
.context-link:focus-visible { box-shadow: var(--focus-ring); }
.summary { margin-top: 6px; min-height: 20px; font-size: 13.5px; color: var(--ink-2); }
.summary-skeleton { display: inline-block; width: 220px; }
.freshness { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 6px 10px; }
.last-updated { color: var(--ink-2); font-size: 12px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.live { display: inline-flex; align-items: center; gap: 8px; height: 28px; padding: 0 12px; border-radius: 999px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); font-size: 12px; color: var(--ink-2); }
.live-mark { width: 7px; height: 7px; border-radius: 50%; background: var(--st-backlog); }
.live.on .live-mark { background: var(--ok); box-shadow: 0 0 0 3px rgba(47, 122, 90, .16); }
.layout { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: 20px; align-items: start; container: agents-layout / inline-size; }
.main-col { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; min-width: 0; }
.side-col { position: sticky; top: 16px; display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; min-width: 0; }
.inline-error { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; padding: 10px 14px; border-radius: 12px; background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); font-size: 13px; color: var(--danger); }
.hint { display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 5px; padding: 4px 0; font-size: 12px; color: var(--ink-3); }
.hint .keycap + .keycap { margin-left: 2px; }
/* Wide screens dock the session panel: the page reflows beside it. */
@media (min-width: 1100px) {
  .agents-page.panel-open { margin: 0; padding-right: calc(var(--panel-w) + 22px); }
}
/* Beside the panel the page is narrow: accounts move below the sessions. */
.agents-page.panel-open .layout { grid-template-columns: minmax(0, 1fr); }
.agents-page.panel-open .side-col { position: static; }
@media (max-width: 1080px) { .layout { grid-template-columns: minmax(0, 1fr); } .side-col { position: static; } }
@media (max-width: 720px) {
  .agents-page { padding: 16px 12px 20px; }
  .page-head { align-items: flex-start; }
  .hint { display: none; }
}
</style>
