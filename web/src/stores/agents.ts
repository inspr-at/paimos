// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { APIError, getNode } from '../lib/api'
import {
  decideApproval, getControl, listAccounts, listAllSessions, listApprovals, listMessages, listModels, listRuns, listTargets, message,
  requestControl, resolveMessage, revokeApproval, sendMessage, setAccountState,
  type AgentAccount, type AgentRun, type Approval, type HarnessSession, type ModelProfile, type ProjectMessage, type SessionControl,
} from '../lib/agents'
import { agentName, groupSessions, harnessLabel, heldRequests, needsYou, pendingApprovals, runModel, sessionStatus, type SessionStatus } from '../lib/agentState'
import { advanceActivity, type ActivityEvidence } from '../lib/liveAgents'
import { toast } from '../lib/toast'
import { useProjects } from './projects'

// forbidden: not for this person; error: the read failed (any other status).
export type Availability = 'idle' | 'ready' | 'forbidden' | 'error'
export interface NodeRef { id: string; key: string; title: string }
export interface SessionView {
  session: HarnessSession; status: SessionStatus; name: string; harness: string; account: string; model: string
  run?: AgentRun; projectKey: string; projectTitle: string; ticket: (NodeRef & { href: string }) | null
}
export type HeldRequest = ProjectMessage & { projectId: string }

const FAN_OUT = 6
const availability = (e: unknown): Availability => e instanceof APIError && e.status === 403 ? 'forbidden' : 'error'
const describe = (e: unknown) => e instanceof APIError ? `The server answered “${e.message}” (${e.status}).` : message(e)
async function all<T>(items: T[], work: (item: T) => Promise<void>) {
  let next = 0
  const worker = async () => { while (next < items.length) { const item = items[next++]; try { await work(item) } catch { /* optional detail */ } } }
  await Promise.all(Array.from({ length: Math.min(FAN_OUT, items.length) }, worker))
}

export const useAgents = defineStore('agents', () => {
  const projects = useProjects()
  const now = ref(Date.now())
  const sessions = ref<HarnessSession[]>([])
  const activityEvidence = ref(new Map<string, ActivityEvidence>())
  const eventPulseFor = (sessionId: string) => activityEvidence.value.get(sessionId)?.pulse ?? 0
  const sessionsState = ref<Availability>('idle')
  const sessionsError = ref('')
  const sessionsUpdatedAt = ref<number | null>(null)
  let sessionsFlight: Promise<void> | undefined
  let sessionsAgain = false
  let loadFlight: Promise<void> | undefined
  let loadAgain = false
  const approvals = ref<Approval[]>([])
  const approvalsState = ref<Availability>('idle')
  const approvalsError = ref('')
  const accounts = ref<AgentAccount[]>([])
  const accountsState = ref<Availability>('idle')
  const messagingState = ref<Availability>('idle')
  const pendingHeld = ref<Record<string, ProjectMessage[]>>({})
  const threads = ref<Record<string, ProjectMessage[]>>({})
  const addresses = ref<Record<string, string>>({})
  const runs = ref<Record<string, AgentRun>>({})
  const agentRuns = ref<Record<string, string[]>>({})
  const nodes = ref<Record<string, NodeRef>>({})
  const models = ref<ModelProfile[]>([])
  const controls = ref<Record<string, SessionControl>>({})
  const loading = ref(false)
  const loaded = ref(false)
  const ticketLoadedAt = new Map<string, number>()
  let needsAt = 0
  let messagingAt = 0

  // Messaging stays per project; only projects where agents have sessions are asked.
  const sessionProjects = () => [...new Set(sessions.value.map(s => s.project_id))]
  function learnAddresses(items: ProjectMessage[]) {
    const names = { ...addresses.value }
    let changed = false
    for (const item of items) if (item.to && !names[item.recipient_principal_id]) { names[item.recipient_principal_id] = item.to; changed = true }
    if (changed) addresses.value = names
  }
  function mergeRuns(list: AgentRun[]) {
    if (list.length) runs.value = { ...runs.value, ...Object.fromEntries(list.map(run => [run.id, run])) }
  }

  // ---------- Reads ----------
  async function refreshApprovals() {
    try { approvals.value = await listApprovals(); approvalsState.value = 'ready'; approvalsError.value = '' }
    catch (e) { approvalsState.value = availability(e); approvalsError.value = message(e) }
  }
  async function refreshAccounts() {
    try { accounts.value = await listAccounts(); accountsState.value = 'ready' }
    catch (e) { accountsState.value = availability(e) }
  }
  async function refreshModels() { if (!models.value.length) try { models.value = await listModels() } catch { /* model names fall back to the run's */ } }
  // Tenant-wide, newest first, with project and ticket summaries.
  function refreshSessions(): Promise<void> {
    if (sessionsFlight) { sessionsAgain = true; return sessionsFlight }
    sessionsFlight = (async () => {
      do {
        sessionsAgain = false
        try {
          const out = new Map<string, HarnessSession>()
          const cursors = new Set<string>()
          let cursor: string | undefined
          do {
            const result = await listAllSessions({ cursor })
            for (const item of result.items) out.set(item.id, item)
            cursor = result.next_cursor ?? undefined
            if (cursor && cursors.has(cursor)) throw new Error('Session pagination did not advance. Please retry.')
            if (cursor) cursors.add(cursor)
          } while (cursor)
          activityEvidence.value = new Map([...out.values()].map(item => [item.id, advanceActivity(activityEvidence.value.get(item.id), item)]))
          sessions.value = [...out.values()]
          sessionsUpdatedAt.value = Date.now()
          now.value = Date.now()
          sessionsState.value = 'ready'; sessionsError.value = ''
        } catch (e) { sessionsState.value = availability(e); sessionsError.value = describe(e) }
      } while (sessionsAgain)
    })().finally(() => { sessionsFlight = undefined })
    return sessionsFlight
  }
  // The newest runs cover the rows' account, model and telemetry in one read.
  async function refreshRuns() {
    try { mergeRuns((await listRuns({ limit: 200 })).items) } catch { /* rows fall back to "not reported" */ }
  }
  // Held action requests still waiting on a person, and message addresses for names.
  async function refreshMessaging(force = false) {
    if (!force && Date.now() - messagingAt < 30_000) return
    messagingAt = Date.now()
    const ids = sessionProjects()
    if (!ids.length) { pendingHeld.value = {}; if (messagingState.value === 'idle') messagingState.value = 'ready'; return }
    const held: Record<string, ProjectMessage[]> = {}
    const names: Record<string, string> = { ...addresses.value }
    let failure: unknown = null
    await all(ids, async id => {
      try {
        const [page, targets] = await Promise.all([listMessages(id, { pending: true }), listTargets(id).catch(() => [])])
        held[id] = page.items
        for (const target of targets) if (target.enabled && target.role !== 'simple_fallback') names[target.principal_id] = target.address
      } catch (e) { failure ??= e }
    })
    if (failure && !Object.keys(held).length) { messagingState.value = availability(failure); return }
    messagingState.value = 'ready'
    pendingHeld.value = held
    addresses.value = names
    learnAddresses(Object.values(held).flat())
  }
  async function resourceNodes() {
    const ids = [...new Set(approvals.value.filter(a => a.resource_kind === 'node' && a.resource_id && !nodes.value[a.resource_id] && !projects.byId(a.resource_id!)).map(a => a.resource_id!))]
    await all(ids, async id => { const node = await getNode(id); nodes.value = { ...nodes.value, [id]: { id, key: node.key, title: node.title } } })
  }

  function loadAll(): Promise<void> {
    // A slow optional detail read must never hold up a new session wake.
    const sessionRead = refreshSessions()
    if (loadFlight) { loadAgain = true; return Promise.all([loadFlight, sessionRead]).then(() => {}) }
    loading.value = true
    loadFlight = (async () => {
      do {
        loadAgain = false
        // Sessions do not wait for optional project or account metadata.
        await Promise.all([projects.load(), refreshApprovals(), sessionRead, refreshRuns(), refreshAccounts(), refreshModels()])
        await Promise.all([refreshMessaging(), resourceNodes()])
        loaded.value = true
        needsAt = Date.now()
      } while (loadAgain)
    })().finally(() => { loadFlight = undefined; loading.value = false; now.value = Date.now() })
    return loadFlight
  }
  // The header badge: approvals and held action requests, at most every 30 seconds.
  async function loadNeeds(force = false) {
    if (!force && Date.now() - needsAt < 30_000) return
    needsAt = Date.now()
    await Promise.all([projects.load(), refreshApprovals(), refreshSessions()])
    await refreshMessaging(force)
    now.value = Date.now()
  }
  // Sessions bound to one ticket, for the ticket panel; cached for 20 seconds.
  async function ensureTicket(nodeId: string) {
    if (Date.now() - (ticketLoadedAt.get(nodeId) ?? 0) < 20_000) return
    ticketLoadedAt.set(nodeId, Date.now())
    try {
      const { items } = await listAllSessions({ ticket: nodeId, limit: 50 })
      const ids = new Set(items.map(s => s.id))
      sessions.value = [...sessions.value.filter(s => !ids.has(s.id)), ...items]
    } catch { /* the ticket panel simply shows no sessions */ }
  }
  // One project's messages, newest first; the panel shows the agent's side of it.
  async function refreshThread(projectId: string) {
    try {
      const page = await listMessages(projectId, { limit: 200 })
      threads.value = { ...threads.value, [projectId]: page.items }
      learnAddresses(page.items)
      if (messagingState.value !== 'ready') messagingState.value = 'ready'
    } catch (e) { if (messagingState.value !== 'ready') messagingState.value = availability(e) }
  }
  async function refreshAgentRuns(principalId: string) {
    try {
      const { items } = await listRuns({ agent: principalId, limit: 10 })
      mergeRuns(items)
      agentRuns.value = { ...agentRuns.value, [principalId]: items.map(run => run.id) }
    } catch { /* the panel says no runs were reported */ }
  }

  // ---------- Derived ----------
  const pending = computed(() => pendingApprovals(approvals.value, now.value))
  const held = computed<HeldRequest[]>(() => Object.entries(pendingHeld.value).flatMap(([projectId, list]) => heldRequests(list).map(m => ({ ...m, projectId }))))
  const needsCount = computed(() => pending.value.length + held.value.length)
  const accountById = computed(() => new Map(accounts.value.map(a => [a.id, a])))
  const modelById = computed(() => new Map(models.value.map(m => [m.id, m])))
  function viewOf(session: HarnessSession): SessionView {
    const run = session.run_id ? runs.value[session.run_id] : undefined
    const project = projects.byId(session.project_id)
    const routeKey = project?.routeKey ?? session.project?.key ?? ''
    const node = session.ticket ?? (session.ticket_node_id ? nodes.value[session.ticket_node_id] : undefined)
    return {
      session, status: sessionStatus(session, now.value, needsYou(session, pending.value, held.value)), name: agentName(session, addresses.value), harness: harnessLabel(session.harness),
      account: run?.account_id ? accountById.value.get(run.account_id)?.label ?? '' : '',
      model: (run?.model_profile_id ? modelById.value.get(run.model_profile_id)?.slug : '') || runModel(run),
      run, projectKey: routeKey, projectTitle: project?.title ?? session.project?.title ?? '',
      ticket: node && routeKey ? { id: node.id, key: node.key, title: node.title, href: `/p/${encodeURIComponent(routeKey)}/${encodeURIComponent(node.key)}` } : null,
    }
  }
  const views = computed(() => sessions.value.map(viewOf))
  const grouped = computed(() => {
    const out: Record<SessionStatus['group'], SessionView[]> = { needs: [], working: [], idle: [], stopped: [] }
    const buckets = groupSessions(sessions.value, now.value, s => needsYou(s, pending.value, held.value))
    const byId = new Map(views.value.map(v => [v.session.id, v]))
    for (const group of ['needs', 'working', 'idle', 'stopped'] as const) out[group] = buckets[group].map(entry => byId.get(entry.session.id)!).filter(Boolean)
    return out
  })
  const byAgent = (principalId: string) => views.value.filter(v => v.session.agent_principal_id === principalId)
  const forTicket = (nodeId: string) => views.value.filter(v => v.session.ticket_node_id === nodeId && v.status.group !== 'stopped')
  const recentRuns = (principalId: string) => (agentRuns.value[principalId] ?? []).map(id => runs.value[id]).filter(Boolean)
  // Who asks: a live session, then a message address, then the caller's fallback
  // (the approval's agent_name), then a short id.
  function askerName(principalId: string, fallbackName?: string | null) {
    const session = sessions.value.find(s => s.agent_principal_id === principalId)
    if (session) return { name: agentName(session, addresses.value), harness: harnessLabel(session.harness), sessionId: session.id }
    const address = addresses.value[principalId]
    if (address) { const [harness, name] = address.split(':'); return { name, harness: harnessLabel(harness), sessionId: '' } }
    const fallback = fallbackName?.trim()
    if (fallback) return { name: fallback, harness: '', sessionId: '' }
    return { name: `Agent ${principalId.slice(0, 8)}`, harness: '', sessionId: '' }
  }
  // Oldest first for reading; the server returns the newest 200.
  const thread = (session: HarnessSession) => (threads.value[session.project_id] ?? [])
    .filter(m => m.sender_principal_id === session.agent_principal_id || m.recipient_principal_id === session.agent_principal_id)
    .slice().reverse()
  const addressOf = (principalId: string) => addresses.value[principalId] ?? ''

  // ---------- Writes ----------
  async function decide(approval: Approval, decision: 'approved' | 'denied', reason: string) {
    const updated = await decideApproval(approval.id, decision, reason)
    approvals.value = approvals.value.map(a => a.id === approval.id ? { ...a, ...updated, decision } : a)
  }
  async function revoke(approval: Approval) {
    await revokeApproval(approval.id)
  }
  // Resolving records a person's answer; the held message itself is never released.
  async function resolve(request: HeldRequest, decision: 'resolved' | 'dismissed', note: string) {
    await resolveMessage(request.projectId, request.id, decision, note)
    pendingHeld.value = { ...pendingHeld.value, [request.projectId]: (pendingHeld.value[request.projectId] ?? []).filter(m => m.id !== request.id) }
  }
  async function control(view: SessionView, kind: SessionControl['kind']) {
    const { session } = view
    const issued = await requestControl(session.project_id, session.id, kind)
    controls.value = { ...controls.value, [session.id]: issued }
    void follow(session, issued, view.name)
    return issued
  }
  // Controls are claimed and completed by the agent; follow for up to a minute.
  async function follow(session: HarnessSession, issued: SessionControl, name: string) {
    for (let i = 0; i < 30; i++) {
      await new Promise(resolve => setTimeout(resolve, 2000))
      let current: SessionControl
      try { current = await getControl(session.project_id, session.id, issued.id) } catch { return }
      controls.value = { ...controls.value, [session.id]: current }
      if (current.state === 'completed') {
        const what = current.kind === 'stop' ? 'stop' : 'interrupt'
        if (current.outcome === 'applied') toast(`${name} applied the ${what}.`)
        else toast(`${name} refused the ${what}${current.reason ? `: ${current.reason}` : '.'}`, { tone: 'error' })
        void refreshSessions()
        return
      }
    }
  }
  async function send(session: HarnessSession, to: string, body: string, level: 'simple' | 'steer', replyTo?: string) {
    await sendMessage(session.project_id, { to, body, idempotency_key: crypto.randomUUID(), expects_reply: false, is_action_request: false, delivery_level: level, ...(replyTo ? { reply_to: replyTo } : {}) })
    await refreshThread(session.project_id)
  }
  async function setAccount(account: AgentAccount, state: AgentAccount['state']) {
    const updated = await setAccountState(account.id, state)
    accounts.value = accounts.value.map(a => a.id === account.id ? { ...a, ...updated } : a)
  }
  function tick() { now.value = Date.now() }

  return {
    now, sessions, sessionsState, sessionsError, sessionsUpdatedAt, approvals, approvalsState, approvalsError, accounts, accountsState, messagingState, runs, nodes, controls, eventPulseFor,
    loading, loaded, pending, held, needsCount, views, grouped,
    loadAll, loadNeeds, ensureTicket, refreshApprovals, refreshSessions, refreshThread, refreshAgentRuns, tick,
    viewOf, byAgent, forTicket, recentRuns, askerName, thread, addressOf, decide, revoke, resolve, control, send, setAccount,
    recordRun: (run: AgentRun) => mergeRuns([run]),
  }
})
