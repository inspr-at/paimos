// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { APIError, getNode } from '../lib/api'
import {
  decideApproval, getControl, listAccounts, listAllSessions, listApprovals, listMessages, listModels, listRuns, listTargets, requestManagedControl,
  requestControl, resolveMessage, revokeApproval, sendMessage, setAccountState, archiveAccount, cancelRun,
  type AgentAccount, type AgentRun, type Approval, type HarnessSession, type ModelProfile, type ProjectMessage, type SessionControl,
} from '../lib/agents'
import { agentName, harnessLabel, heldRequests, mergeSessionEvidence, needsYou, pendingApprovals, runModel, sessionStatus, type SessionStatus } from '../lib/agentState'
import { advanceActivity, type ActivityEvidence } from '../lib/liveAgents'
import { toast } from '../lib/toast'
import { managedControlSession } from '../lib/managedControl'
import { usePolledData } from '../lib/usePolledData'
import { useProjects } from './projects'
import { useAgentAppearance } from '../lib/agentAppearance'

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
async function all<T>(items: T[], work: (item: T) => Promise<void>) {
  let next = 0
  const worker = async () => { while (next < items.length) { const item = items[next++]; try { await work(item) } catch { /* optional detail */ } } }
  await Promise.all(Array.from({ length: Math.min(FAN_OUT, items.length) }, worker))
}

export const useAgents = defineStore('agents', () => {
  const projects = useProjects()
  const { choice: statePreferences, ready: preferencesReady } = useAgentAppearance()
  const now = ref(Date.now())
  let sessionReadOrder = 0
  let appliedSessionRead = 0
  const sessionsRead = usePolledData<HarnessSession[]>(async (): Promise<HarnessSession[]> => {
    const order = ++sessionReadOrder
    await preferencesReady
    const out = new Map<string, HarnessSession>()
    const cursors = new Set<string>()
    let cursor: string | undefined
    do {
      const result = await listAllSessions({ cursor, view: 'current' })
      for (const item of result.items) out.set(item.id, item)
      cursor = result.next_cursor ?? undefined
      if (cursor && cursors.has(cursor)) throw new Error('Session pagination did not advance. Please retry.')
      if (cursor) cursors.add(cursor)
    } while (cursor)
    // A newer ticket-scoped read may have completed while pagination was open.
    const previous = new Map(sessions.value.map(item => [item.id, item]))
    if (order < appliedSessionRead) return sessions.value
    appliedSessionRead = order
    return [...out.values()].map(item => mergeSessionEvidence(previous.get(item.id), item))
  }, [] as HarnessSession[], items => {
    activityEvidence.value = new Map(items.map(item => [item.id, advanceActivity(activityEvidence.value.get(item.id), item)]))
    now.value = Math.max(now.value, Date.now())
  })
  const sessions = sessionsRead.data
  const activityEvidence = ref(new Map<string, ActivityEvidence>())
  const eventPulseFor = (sessionId: string) => activityEvidence.value.get(sessionId)?.pulse ?? 0
  const sessionsState = computed(() => sessionsRead.status.value.state)
  const sessionsError = computed(() => sessionsRead.status.value.error)
  const sessionsUpdatedAt = computed(() => sessionsRead.status.value.updatedAt)
  const sessionsStale = sessionsRead.stale
  let loadFlight: Promise<void> | undefined
  const approvalsRead = usePolledData(listApprovals, [] as Approval[])
  const approvals = approvalsRead.data
  const approvalsState = computed(() => approvalsRead.status.value.state)
  const approvalsError = computed(() => approvalsRead.status.value.error)
  const approvalsHardError = computed(() => approvalsRead.status.value.state === 'error')
  const accountsRead = usePolledData(listAccounts, [] as AgentAccount[])
  const accounts = accountsRead.data
  const accountsState = computed(() => accountsRead.status.value.state)
  const accountsUpdatedAt = computed(() => accountsRead.status.value.updatedAt)
  const messagingState = ref<Availability>('idle')
  const pendingHeld = ref<Record<string, ProjectMessage[]>>({})
  const threads = ref<Record<string, ProjectMessage[]>>({})
  const addresses = ref<Record<string, string>>({})
  const runs = ref<Record<string, AgentRun>>({})
  const agentRuns = ref<Record<string, string[]>>({})
  const nodes = ref<Record<string, NodeRef>>({})
  const modelsRead = usePolledData(listModels, [] as ModelProfile[])
  const models = modelsRead.data
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
  // Bumped by every accepted write; a per-agent read started before one is dropped.
  let runWrites = 0
  function mergeRuns(list: AgentRun[]) {
    if (list.length) runs.value = { ...runs.value, ...Object.fromEntries(list.map(run => [run.id, run])) }
  }
  const runsRead = usePolledData(() => listRuns({ limit: 200 }), { items: [] as AgentRun[], next_cursor: null as string | null }, page => mergeRuns(page.items))
  const refreshStale = computed(() => sessionsRead.stale.value || approvalsRead.stale.value || accountsRead.stale.value || modelsRead.stale.value || runsRead.stale.value)

  // ---------- Reads ----------
  const refreshApprovals = approvalsRead.refresh
  const refreshAccounts = accountsRead.refresh
  const refreshModels = modelsRead.refresh
  // Tenant-wide, newest first, with project and ticket summaries.
  const refreshSessions = sessionsRead.refresh
  // The newest runs cover the rows' account, model and telemetry in one read.
  const refreshRuns = runsRead.refresh
  // Held action requests still waiting on a person, and message addresses for names.
  let messagingTurn = 0
  async function refreshMessaging(force = false) {
    if (!force && Date.now() - messagingAt < 30_000) return
    messagingAt = Date.now()
    const turn = ++messagingTurn
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
    if (turn !== messagingTurn) return
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
    if (loadFlight) return loadFlight
    loading.value = true
    loadFlight = (async () => {
        // Sessions do not wait for optional project or account metadata.
        await Promise.all([projects.load(), refreshApprovals(), sessionRead, refreshRuns(), refreshAccounts(), refreshModels()])
        await Promise.all([refreshMessaging(), resourceNodes()])
        loaded.value = true
        needsAt = Date.now()
    })().finally(() => { loadFlight = undefined; loading.value = false; now.value = Math.max(now.value, Date.now()) })
    return loadFlight
  }
  // The header badge: approvals and held action requests, at most every 30 seconds.
  async function loadNeeds(force = false) {
    if (!force && Date.now() - needsAt < 30_000) return
    needsAt = Date.now()
    await Promise.all([projects.load(), refreshApprovals(), refreshSessions()])
    await refreshMessaging(force)
    now.value = Math.max(now.value, Date.now())
  }
  // Sessions bound to one ticket, for the ticket panel; cached for 20 seconds.
  async function ensureTicket(nodeId: string) {
    if (Date.now() - (ticketLoadedAt.get(nodeId) ?? 0) < 20_000) return
    ticketLoadedAt.set(nodeId, Date.now())
    const order = ++sessionReadOrder
    try {
      const { items } = await listAllSessions({ ticket: nodeId, limit: 50 })
      if (order < appliedSessionRead) return
      appliedSessionRead = order
      const ids = new Set(items.map(s => s.id))
      const previous = new Map(sessions.value.map(item => [item.id, item]))
      sessions.value = [...sessions.value.filter(s => !ids.has(s.id)), ...items.map(item => mergeSessionEvidence(previous.get(item.id), item))]
    } catch { /* the ticket panel simply shows no sessions */ }
  }
  // Scope before limiting: a busy project must not crowd a session's replies out.
  const threadReads = new Map<string, number>()
  async function refreshThread(projectId: string, sessionId: string) {
    const read = (threadReads.get(sessionId) ?? 0) + 1
    threadReads.set(sessionId, read)
    try {
      const page = await listMessages(projectId, { session: sessionId, limit: 200 })
      if (threadReads.get(sessionId) !== read) return
      threads.value = { ...threads.value, [sessionId]: page.items }
      learnAddresses(page.items)
      if (messagingState.value !== 'ready') messagingState.value = 'ready'
    } catch (e) { if (messagingState.value !== 'ready') messagingState.value = availability(e) }
  }
  async function refreshAgentRuns(principalId: string) {
    const started = runWrites
    try {
      const { items } = await listRuns({ agent: principalId, limit: 10 })
      if (started !== runWrites) return
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
      session, status: sessionStatus(session, now.value, needsYou(session, pending.value, held.value), statePreferences.value, run), name: agentName(session, addresses.value), harness: harnessLabel(session.harness),
      account: run?.account_id ? accountById.value.get(run.account_id)?.label ?? '' : '',
      model: (run?.model_profile_id ? modelById.value.get(run.model_profile_id)?.slug : '') || runModel(run),
      run, projectKey: routeKey, projectTitle: project?.title ?? session.project?.title ?? '',
      ticket: node && routeKey ? { id: node.id, key: node.key, title: node.title, href: `/p/${encodeURIComponent(routeKey)}/${encodeURIComponent(node.key)}` } : null,
    }
  }
  const views = computed(() => sessions.value.filter(s => !s.archived_at).map(viewOf))
  const removedViews = computed(() => sessions.value.filter(s => s.archived_at).map(viewOf))
  // History (AEON-291): the list reads only sessions that ended in the last 24
  // hours. Every ended or removed generation is read on demand, newest first.
  const historySessions = ref<HarnessSession[]>([])
  const historyState = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
  // The next page's cursor: History reads one page at a time and "Show older"
  // continues until the server has no more.
  const historyCursor = ref<string | null>(null)
  let historyFlight: Promise<void> | undefined
  function loadHistory(force = false) {
    if (historyFlight) return historyFlight
    if (historyState.value === 'ready' && !force) return Promise.resolve()
    return readHistory(undefined)
  }
  function loadOlderHistory() {
    if (historyFlight) return historyFlight
    if (!historyCursor.value) return Promise.resolve()
    return readHistory(historyCursor.value)
  }
  function readHistory(cursor: string | undefined) {
    historyState.value = 'loading'
    historyFlight = (async () => {
      try {
        const result = await listAllSessions({ cursor, view: 'all' })
        const page = result.items.filter(item => item.stopped_at || item.archived_at)
        if (cursor) {
          const seen = new Set(historySessions.value.map(item => item.id))
          historySessions.value = [...historySessions.value, ...page.filter(item => !seen.has(item.id))]
        } else historySessions.value = page
        historyCursor.value = result.next_cursor ?? null
        historyState.value = 'ready'
      } catch { historyState.value = 'error' } finally { historyFlight = undefined }
    })()
    return historyFlight
  }
  const historyMore = computed(() => !!historyCursor.value)
  const historyViews = computed(() => {
    const byId = new Map(historySessions.value.map(item => [item.id, item]))
    for (const item of sessions.value) if (item.stopped_at || item.archived_at || byId.has(item.id)) byId.set(item.id, item)
    const ended = (item: HarnessSession) => Date.parse(item.archived_at ?? item.stopped_at ?? item.created_at)
    return [...byId.values()].filter(item => item.stopped_at || item.archived_at).sort((a, b) => ended(b) - ended(a)).map(viewOf)
  })
  const grouped = computed(() => {
    const out: Record<SessionStatus['group'], SessionView[]> = { problem: [], unresponsive: [], needs: [], awaiting: [], throttled: [], working: [], idle: [], stopped: [] }
    for (const view of views.value) out[view.status.group].push(view)
    for (const [group, list] of Object.entries(out)) {
      const at = (v: SessionView) => Date.parse((group === 'stopped' ? v.session.stopped_at : v.session.heartbeat_at) ?? v.session.created_at)
      list.sort((a, b) => at(b) - at(a))
    }
    return out
  })
  const byAgent = (principalId: string) => views.value.filter(v => v.session.agent_principal_id === principalId)
  const forTicket = (nodeId: string) => views.value.filter(v => v.session.ticket_node_id === nodeId && v.status.group !== 'stopped')
  const recentRuns = (principalId: string) => (agentRuns.value[principalId] ?? []).map(id => runs.value[id]).filter(Boolean)
  // Requests name their principal, never a guessed session of that principal.
  function askerName(principalId: string, fallbackName?: string | null) {
    const session = sessions.value.find(s => s.agent_principal_id === principalId)
    if (session?.agent?.name) return { name: session.agent.name, harness: '', sessionId: '' }
    const address = addresses.value[principalId]
    if (address) { const [harness, name] = address.split(':'); return { name, harness: harnessLabel(harness), sessionId: '' } }
    const fallback = fallbackName?.trim()
    if (fallback) return { name: fallback, harness: '', sessionId: '' }
    return { name: `Agent ${principalId.slice(0, 8)}`, harness: '', sessionId: '' }
  }
  // Oldest first for reading; the server returns the newest 200.
  const thread = (session: HarnessSession) => (threads.value[session.id] ?? [])
    .slice().sort((a, b) => a.sent_event_id - b.sent_event_id)
  const addressOf = (principalId: string) => addresses.value[principalId] ?? ''

  // ---------- One rule for every write (AEON-402) ----------
  // A write on /agents can change what any of its reads return: Remove account
  // cancels runs, Remove computer takes account bindings along. So an accepted
  // write drops every read already in flight, here and in the stores and lists
  // that registered with onWrite, applies its own result, then reads each once more.
  interface WriteReader { invalidate: () => void; refresh: () => Promise<unknown> }
  const writeReaders = new Set<WriteReader>()
  function onWrite(reader: WriteReader) {
    writeReaders.add(reader)
    return () => { writeReaders.delete(reader) }
  }
  function invalidatePolls() { sessionsRead.invalidate(); approvalsRead.invalidate(); accountsRead.invalidate(); modelsRead.invalidate(); runsRead.invalidate() }
  let rereadAfterWrite: Promise<void> | undefined
  function afterWrite(apply?: () => void): Promise<void> {
    invalidatePolls()
    appliedSessionRead = ++sessionReadOrder
    runWrites++
    messagingTurn++
    for (const reader of writeReaders) reader.invalidate()
    apply?.()
    // Writes in one burst (a bulk removal) share one re-read.
    rereadAfterWrite ??= new Promise<void>(resolve => setTimeout(resolve)).then(async () => {
      rereadAfterWrite = undefined
      await Promise.allSettled([refreshSessions(), refreshApprovals(), refreshAccounts(), refreshModels(), refreshRuns(), refreshMessaging(true), ...[...writeReaders].map(reader => reader.refresh())])
      now.value = Math.max(now.value, Date.now())
    })
    return rereadAfterWrite
  }

  // Preserve summaries locally because the mutation response is a bare session.
  function recordRemoval(removed: HarnessSession) {
    void afterWrite(() => {
      const known = sessions.value.some(s => s.id === removed.id)
      sessions.value = known ? sessions.value.map(s => s.id === removed.id ? { ...s, ...removed } : s) : [...sessions.value, removed]
      historySessions.value = historySessions.value.map(s => s.id === removed.id ? { ...s, ...removed } : s)
    })
  }

  // ---------- Writes ----------
  async function decide(approval: Approval, decision: 'approved' | 'denied', reason: string) {
    const updated = await decideApproval(approval.id, decision, reason)
    void afterWrite(() => { approvals.value = approvals.value.map(a => a.id === approval.id ? { ...a, ...updated, decision } : a) })
  }
  async function revoke(approval: Approval) {
    await revokeApproval(approval.id)
    void afterWrite()
  }
  // Resolving records a person's answer; the held message itself is never released.
  async function resolve(request: HeldRequest, decision: 'resolved' | 'dismissed', note: string) {
    await resolveMessage(request.projectId, request.id, decision, note)
    void afterWrite(() => { pendingHeld.value = { ...pendingHeld.value, [request.projectId]: (pendingHeld.value[request.projectId] ?? []).filter(m => m.id !== request.id) } })
  }
  async function control(view: SessionView, kind: SessionControl['kind']) {
    const { session } = view
    // managed_control_v1 sessions refuse the legacy route; use the ownership-aware one.
    const issued = managedControlSession(session) ? await requestManagedControl(session, kind) : await requestControl(session.project_id, session.id, kind)
    void afterWrite(() => { controls.value = { ...controls.value, [session.id]: issued } })
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
        else if (current.reason === 'graceful_stop_timeout') toast(`${name} did not exit after normal stop. Open Recover to review a force stop.`, { tone: 'error' })
        else toast(`${name} refused the ${what}${current.reason ? `: ${current.reason}` : '.'}`, { tone: 'error' })
        void refreshSessions()
        return
      }
    }
  }
  // `to` is the registered harness address when one exists, otherwise the principal id.
  // recipient_session_id is always the open generation and is never dropped.
  async function send(session: HarnessSession, to: string, body: string, level: 'simple' | 'steer', replyTo?: string) {
    await sendMessage(session.project_id, { to, body, recipient_session_id: session.id, idempotency_key: crypto.randomUUID(), expects_reply: false, is_action_request: false, delivery_level: level, ...(replyTo ? { reply_to: replyTo } : {}) })
    await refreshThread(session.project_id, session.id)
  }
  async function setAccount(account: AgentAccount, state: AgentAccount['state']) {
    const updated = await setAccountState(account.id, state)
    void afterWrite(() => { accounts.value = accounts.value.map(a => a.id === account.id ? { ...a, ...updated } : a) })
  }
  // Remove leaves the list at once; its runs and history stay on the server (AEON-402).
  async function removeAccount(account: Pick<AgentAccount, 'id'>) {
    await archiveAccount(account.id)
    void afterWrite(() => { accounts.value = accounts.value.filter(a => a.id !== account.id) })
  }
  async function cancelQueuedRun(run: Pick<AgentRun, 'id'>) {
    const cancelled = await cancelRun(run.id)
    void afterWrite(() => mergeRuns([cancelled]))
  }
  function tick() { now.value = Math.max(now.value, Date.now()) }
  // Bumped by delivery events (AEON-280); an open chat re-reads its message status.
  const deliveryPulse = ref(0)
  function deliveryChanged() { deliveryPulse.value++ }

  return {
    now, sessions, sessionsState, sessionsError, sessionsUpdatedAt, sessionsStale, refreshStale, approvals, approvalsState, approvalsError, approvalsHardError, accounts, accountsState, accountsUpdatedAt, messagingState, runs, nodes, controls, models, eventPulseFor,
    loading, loaded, pending, held, needsCount, views, removedViews, historyViews, historyState, historyMore, loadHistory, loadOlderHistory, recordRemoval, grouped,
    loadAll, loadNeeds, ensureTicket, refreshApprovals, refreshAccounts, refreshSessions, refreshThread, refreshAgentRuns, tick, deliveryPulse, deliveryChanged,
    viewOf, byAgent, forTicket, recentRuns, askerName, thread, addressOf, decide, revoke, resolve, control, send, setAccount, removeAccount, cancelQueuedRun,
    invalidatePolls, afterWrite, onWrite,
    recordRun: (run: AgentRun) => mergeRuns([run]),
  }
})
