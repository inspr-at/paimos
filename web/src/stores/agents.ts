// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref, shallowRef, triggerRef } from 'vue'
import { APIError, getNode } from '../lib/api'
import {
  decideApproval, getControl, listAccounts, listApprovals, listMessages, listModels, listTargets, requestManagedControl,
  requestControl, resolveMessage, revokeApproval, sendMessage, setAccountState, archiveAccount,
  type AgentAccount, type AgentRun, type Approval, type HarnessSession, type ModelProfile, type Paged, type ProjectMessage, type SessionControl,
} from '../lib/agents'
import { cancelRun, getSession, listAllSessions, listRuns, type AgentRunRow, type HarnessSessionRow } from '../lib/agentRows'
import { openRow, type Wire } from '../lib/wire'
import { agentName, byStart, byStopped, harnessLabel, heldRequests, mergeSessionEvidence, needsYou, pendingApprovals, runModel, sessionStatus, type SessionStatus } from '../lib/agentState'
import { advanceActivity, type ActivityEvidence } from '../lib/liveAgents'
import { toast } from '../lib/toast'
import { managedControlSession } from '../lib/managedControl'
import { usePolledData } from '../lib/usePolledData'
import { useProjects } from './projects'
import { useAgentAppearance } from '../lib/agentAppearance'
import { carry, createReadOrder, lowestPosition, onReset, positionOf, readOrdered, stampAt, tick as requestTick, type ReadOrder } from '../lib/position'
import type { Admitted, DeepReadonly, Ledger } from '../lib/ledger'

// No exported factory or ledger instance: every component admits through this store.
function freezeRow<T>(row: T): DeepReadonly<T> {
  const seen = new WeakSet<object>()
  const freeze = (value: unknown) => {
    if (!value || typeof value !== 'object' || seen.has(value)) return
    seen.add(value)
    for (const child of Object.values(value)) freeze(child)
    Object.freeze(value)
  }
  freeze(row)
  return row as DeepReadonly<T>
}

interface Entry<T> { row: Admitted<T>; revision?: number; position?: number; start: number; landed: number }

interface LedgerOptions<T> {
  // Builds the row to keep from the one held and the one admitted, for evidence the
  // newer answer may omit (a bare mutation result has no list summaries). The default
  // keeps the admitted row as it is.
  combine?: (held: Admitted<T> | undefined, incoming: T) => DeepReadonly<T>
}

function createLedger<T extends { id: string }>(options: LedgerOptions<T> = {}) {
  const entries = new Map<string, Entry<T>>()
  const listeners = new Set<(ids: Set<string>) => void>()

  function admits(held: Entry<T>, incoming: Omit<Entry<T>, 'row'>) {
    if (held.revision !== undefined && incoming.revision === undefined) return false
    // Asked after the held row landed: it cannot be older, so a lower answer means the log moved back.
    const backwards = incoming.start > held.landed
    if (incoming.revision !== undefined && held.revision !== undefined && incoming.revision !== held.revision) {
      return incoming.revision > held.revision || backwards
    }
    if (incoming.position !== undefined && held.position !== undefined && incoming.position !== held.position) {
      return incoming.position > held.position || backwards
    }
    return incoming.start >= held.start
  }

  return {
    // Judges each wire row against the copy held and returns, in order, the row that
    // stands for it: the admitted one, or the row held when the incoming one is older.
    // Listeners hear which ids changed.
    merge(wired: Wire<T>[]): Admitted<T>[] {
      const landed = requestTick()
      const changed = new Set<string>()
      const standing = wired.map(wire => {
        const { row, stamp } = openRow(wire)
        const held = entries.get(row.id)
        const incoming = { revision: stamp.rowVersion, position: stamp.position, start: stamp.start, landed }
        if (held && !admits(held, incoming)) return held.row
        const admitted = freezeRow(options.combine ? options.combine(held?.row, row) : row) as Admitted<T>
        entries.set(row.id, { ...incoming, row: admitted })
        changed.add(row.id)
        return admitted
      })
      if (changed.size) for (const listen of listeners) listen(changed)
      return standing
    },
    get: (id: string): Admitted<T> | undefined => entries.get(id)?.row,
    // Called after a merge replaced rows, with their ids: a view that copied a row follows it.
    subscribe(listen: (ids: Set<string>) => void) {
      listeners.add(listen)
      return () => { listeners.delete(listen) }
    },
    clear() { entries.clear() },
  }
}

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

// A page's rows are judged by their ledger as the page arrives, and the page holds the
// rows that stand: a row an older answer would have rewound is the one held.
function mergePage<R extends { id: string }>(page: Paged<Wire<R>>, ledger: Ledger<R>): Paged<Admitted<R>> {
  return carry({ items: ledger.merge(page.items), next_cursor: page.next_cursor }, page)
}
// Pages overlap when the list moves while it is read: each session stands once.
const once = <R extends { id: string }>(rows: R[]): R[] => [...new Map(rows.map(row => [row.id, row])).values()]

export const useAgents = defineStore('agents', () => {
  const projects = useProjects()
  const { choice: statePreferences, ready: preferencesReady } = useAgentAppearance()
  const now = ref(Date.now())
  // One ledger for every session this page holds, whichever read or write brought
  // it: the current list, History, a ticket's sessions, a detail and the answers to
  // its own writes. Every row reaches it as a Wire row from lib/agentRows.ts, and only
  // what it admits can be shown or kept: the session's own row_version decides which
  // copy is newer, then the position of the list snapshot it came from, then the order
  // the requests started in.
  const sessionLedger = createLedger<HarnessSessionRow>({ combine: mergeSessionEvidence })
  // The same for runs: the global list, every per-agent list, a run read on its own and
  // the answers to its own writes (a launch included).
  const runLedger = createLedger<AgentRunRow>()
  // Every session the ledger holds, for a view that follows one by id (a dialog, a panel).
  const standingSessions = shallowRef(new Map<string, HarnessSession>())
  sessionLedger.subscribe(ids => {
    for (const id of ids) standingSessions.value.set(id, sessionLedger.get(id)!)
    triggerRef(standingSessions)
  })
  onReset(() => { sessionLedger.clear(); runLedger.clear(); standingSessions.value = new Map(); runs.value = {} })
  // The session list and the ticket-scoped reads share one order for what the
  // list holds: the server's position decides, and a tie goes to the read that started later.
  const sessionsOrder = createReadOrder()
  const initialSessions: HarnessSession[] = []
  const sessionsRead = usePolledData(async (): Promise<Wire<HarnessSessionRow>[]> => {
    await preferencesReady
    const out: Wire<HarnessSessionRow>[] = []
    const cursors = new Set<string>()
    const positions: (number | undefined)[] = []
    let cursor: string | undefined
    do {
      const result = await listAllSessions({ cursor, view: 'current' })
      positions.push(positionOf(result))
      out.push(...result.items)
      cursor = result.next_cursor ?? undefined
      if (cursor && cursors.has(cursor)) throw new Error('Session pagination did not advance. Please retry.')
      if (cursor) cursors.add(cursor)
    } while (cursor)
    // The list as a whole includes what its oldest page did.
    return stampAt(out, { position: lowestPosition(positions) })
  }, initialSessions, items => {
    activityEvidence.value = new Map(items.map(item => [item.id, advanceActivity(activityEvidence.value.get(item.id), item)]))
    now.value = Math.max(now.value, Date.now())
  }, { order: sessionsOrder, adopt: rows => once(sessionLedger.merge(rows)) })
  const sessions = sessionsRead.data
  const activityEvidence = ref(new Map<string, ActivityEvidence>())
  const eventPulseFor = (sessionId: string) => activityEvidence.value.get(sessionId)?.pulse ?? 0
  const sessionsState = computed(() => sessionsRead.status.value.state)
  const sessionsError = computed(() => sessionsRead.status.value.error)
  const sessionsUpdatedAt = computed(() => sessionsRead.status.value.updatedAt)
  const sessionsStale = sessionsRead.stale
  let loadFlight: Promise<void> | undefined
  const approvalsRead = usePolledData(listApprovals, [] as Approval[], undefined, { order: createReadOrder() })
  const approvals = approvalsRead.data
  const approvalsState = computed(() => approvalsRead.status.value.state)
  const approvalsError = computed(() => approvalsRead.status.value.error)
  const approvalsHardError = computed(() => approvalsRead.status.value.state === 'error')
  const accountsRead = usePolledData(listAccounts, [] as AgentAccount[], undefined, { order: createReadOrder() })
  const accounts = accountsRead.data
  const accountsState = computed(() => accountsRead.status.value.state)
  const accountsUpdatedAt = computed(() => accountsRead.status.value.updatedAt)
  const messagingState = ref<Availability>('idle')
  const pendingHeld = ref<Record<string, ProjectMessage[]>>({})
  const threads = ref<Record<string, ProjectMessage[]>>({})
  const addresses = ref<Record<string, string>>({})
  const runs = shallowRef<Record<string, AgentRun>>({})
  const agentRuns = ref<Record<string, string[]>>({})
  const nodes = ref<Record<string, NodeRef>>({})
  const modelsRead = usePolledData(listModels, [] as ModelProfile[], undefined, { order: createReadOrder() })
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
  // The global run list and every per-agent list overlap: the ledger keeps the
  // newest copy of each run, so no older answer rewinds a run another read already
  // moved forward. `runs` follows whatever the ledger admits.
  runLedger.subscribe(ids => {
    const next = { ...runs.value }
    for (const id of ids) next[id] = runLedger.get(id)!
    runs.value = next
  })
  const initialRuns: Paged<AgentRun> = { items: [], next_cursor: null }
  const runsRead = usePolledData(() => listRuns({ limit: 200 }), initialRuns, undefined, { order: createReadOrder(), adopt: page => mergePage(page, runLedger) })
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
  const messagingOrder = createReadOrder()
  async function refreshMessaging(force = false, retried = false): Promise<void> {
    if (!force && Date.now() - messagingAt < 30_000) return
    messagingAt = Date.now()
    const ticket = messagingOrder.begin()
    const ids = sessionProjects()
    if (!ids.length) { pendingHeld.value = {}; if (messagingState.value === 'idle') messagingState.value = 'ready'; return }
    const held: Record<string, ProjectMessage[]> = {}
    const names: Record<string, string> = { ...addresses.value }
    const positions: (number | undefined)[] = []
    let failure: unknown = null
    await all(ids, async id => {
      try {
        const [page, targets] = await Promise.all([listMessages(id, { pending: true }), listTargets(id).catch(() => [])])
        held[id] = page.items
        positions.push(positionOf(page))
        for (const target of targets) if (target.enabled && target.role !== 'simple_fallback') names[target.principal_id] = target.address
      } catch (e) { failure ??= e }
    })
    // A project that did not answer leaves no position to judge the rest by.
    const verdict = messagingOrder.land(ticket, failure ? undefined : lowestPosition(positions))
    if (verdict === 'older') return
    if (verdict === 'stale') return retried ? undefined : refreshMessaging(true, true)
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
    const ticket = sessionsOrder.begin()
    try {
      const page = await listAllSessions({ ticket: nodeId, limit: 50 })
      const verdict = sessionsOrder.land(ticket, positionOf(page))
      // A read that predates a write is not the ticket's answer: read again next time.
      if (verdict === 'stale') ticketLoadedAt.delete(nodeId)
      if (verdict !== 'apply') return
      // Each row is judged against the copy the ledger holds, then the list follows the ledger.
      const items = sessionLedger.merge(page.items)
      const ids = new Set(items.map(s => s.id))
      sessions.value = [...sessions.value.filter(s => !ids.has(s.id)), ...items]
    } catch { /* the ticket panel simply shows no sessions */ }
  }
  // Scope before limiting: a busy project must not crowd a session's replies out.
  // One order per thread and per agent: an older answer never replaces a newer one,
  // and one that predates a write of this tab is read again.
  const threadOrders = new Map<string, ReadOrder>()
  const agentRunOrders = new Map<string, ReadOrder>()
  const orderFor = (orders: Map<string, ReadOrder>, key: string) => {
    let order = orders.get(key)
    if (!order) orders.set(key, order = createReadOrder())
    return order
  }
  async function refreshThread(projectId: string, sessionId: string) {
    try {
      await readOrdered(orderFor(threadOrders, sessionId), () => listMessages(projectId, { session: sessionId, limit: 200 }), page => {
        threads.value = { ...threads.value, [sessionId]: page.items }
        learnAddresses(page.items)
        if (messagingState.value !== 'ready') messagingState.value = 'ready'
      })
    } catch (e) { if (messagingState.value !== 'ready') messagingState.value = availability(e) }
  }
  async function refreshAgentRuns(principalId: string) {
    try {
      await readOrdered(orderFor(agentRunOrders, principalId), () => listRuns({ agent: principalId, limit: 10 }), read => {
        const page = mergePage(read, runLedger)
        agentRuns.value = { ...agentRuns.value, [principalId]: page.items.map(run => run.id) }
      })
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
  const historyOrder = createReadOrder()
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
        // A read held across a write of this tab (a removal, its undo) is read again, never applied.
        const applied = await readOrdered(historyOrder, () => listAllSessions({ cursor, view: 'all' }), result => {
          // The ledger holds the newest copy of every session, whichever read brought it.
          const page = sessionLedger.merge(result.items).filter(item => item.stopped_at || item.archived_at)
          if (cursor) {
            const seen = new Set(historySessions.value.map(item => item.id))
            historySessions.value = [...historySessions.value, ...page.filter(item => !seen.has(item.id))]
          } else historySessions.value = page
          historyCursor.value = result.next_cursor ?? null
        })
        historyState.value = applied ? 'ready' : 'error'
      } catch { historyState.value = 'error' } finally { historyFlight = undefined }
    })()
    return historyFlight
  }
  const historyMore = computed(() => !!historyCursor.value)
  // A list that copied a row follows the ledger when a newer copy is admitted, so the
  // current list and History never show two versions of one session.
  sessionLedger.subscribe(ids => {
    const follow = (list: HarnessSession[]) => list.some(item => ids.has(item.id)) ? list.map(item => ids.has(item.id) ? sessionLedger.get(item.id) ?? item : item) : list
    sessions.value = follow(sessions.value)
    historySessions.value = follow(historySessions.value)
  })
  // Ended sessions the list carries and History read, each once, as the ledger holds them.
  const historyViews = computed(() => {
    const byId = new Map(historySessions.value.map(item => [item.id, item]))
    for (const item of sessions.value) if (item.stopped_at || item.archived_at || byId.has(item.id)) byId.set(item.id, item)
    const ended = (item: HarnessSession) => Date.parse(item.archived_at ?? item.stopped_at ?? item.created_at)
    return [...byId.values()].filter(item => item.stopped_at || item.archived_at).sort((a, b) => ended(b) - ended(a)).map(viewOf)
  })
  const grouped = computed(() => {
    const out: Record<SessionStatus['group'], SessionView[]> = { problem: [], unresponsive: [], needs: [], awaiting: [], throttled: [], working: [], idle: [], stopped: [] }
    for (const view of views.value) out[view.status.group].push(view)
    // Start order for live sessions, latest stop first for ended ones (AEON-468).
    for (const [group, list] of Object.entries(out)) list.sort((a, b) => (group === 'stopped' ? byStopped : byStart)(a.session, b.session))
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
  // Every write raises the floor in api(), so a read the server ordered by position
  // that started before it is read again or dropped by its data. Superseding is for
  // an answer without a position (an older server), which only the order it started in can judge.
  function afterWrite(apply?: () => void): Promise<void> {
    invalidatePolls()
    for (const order of [sessionsOrder, messagingOrder, ...threadOrders.values(), ...agentRunOrders.values()]) order.supersede()
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

  // The answer to a write that returns a session (a removal, an undo, a move) is a
  // bare row: the list summaries it lacks stay with the copy held (mergeSessionEvidence).
  // The ledger keeps it only when no read has brought a newer copy, and the lists that
  // hold the session follow it.
  function recordSession(written: Wire<HarnessSessionRow>): HarnessSession {
    let row!: HarnessSession
    // afterWrite applies its change before it returns, so the row that stands is known here.
    void afterWrite(() => {
      [row] = sessionLedger.merge([written])
      if (!sessions.value.some(s => s.id === row.id)) sessions.value = [...sessions.value, row]
    })
    return row
  }
  // A session read on its own: the detail the panel shows, with the history only it
  // carries. The ledger judges it like any other copy; every list that holds the
  // session follows, and a view that shows it reads the standing row by id.
  async function loadSessionDetail(projectId: string, sessionId: string, signal?: AbortSignal) {
    sessionLedger.merge([await getSession(projectId, sessionId, signal)])
  }
  // Rows that came with an answer of their own (a dialog's per-agent list): judged by the
  // ledger, never added to a list. Returns what stands for each.
  const admitSessions = (rows: Wire<HarnessSessionRow>[]) => sessionLedger.merge(rows)
  const sessionById = (id: string): HarnessSession | undefined => standingSessions.value.get(id)
  // A run read or written outside the lists (launch, run now, a single read): the ledger
  // keeps the newest copy and `runs` follows it.
  const admitRun = (run: Wire<AgentRunRow>): AgentRun => runLedger.merge([run])[0]
  const admitRuns = (rows: Wire<AgentRunRow>[]): AgentRun[] => runLedger.merge(rows)

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
    // A message can change what the lists show (held requests, reply obligations); the open thread is read first.
    void afterWrite()
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
    void afterWrite(() => { runLedger.merge([cancelled]) })
  }
  function tick() { now.value = Math.max(now.value, Date.now()) }
  // Bumped by delivery events (AEON-280); an open chat re-reads its message status.
  const deliveryPulse = ref(0)
  function deliveryChanged() { deliveryPulse.value++ }

  return {
    now, sessions, sessionsState, sessionsError, sessionsUpdatedAt, sessionsStale, refreshStale, approvals, approvalsState, approvalsError, approvalsHardError, accounts, accountsState, accountsUpdatedAt, messagingState, runs, nodes, controls, models, eventPulseFor,
    loading, loaded, pending, held, needsCount, views, removedViews, historyViews, historyState, historyMore, loadHistory, loadOlderHistory, recordSession, loadSessionDetail, admitSessions, sessionById, admitRun, admitRuns, grouped,
    loadAll, loadNeeds, ensureTicket, refreshApprovals, refreshAccounts, refreshSessions, refreshThread, refreshAgentRuns, tick, deliveryPulse, deliveryChanged,
    viewOf, byAgent, forTicket, recentRuns, askerName, thread, addressOf, decide, revoke, resolve, control, send, setAccount, removeAccount, cancelQueuedRun,
    invalidatePolls, afterWrite, onWrite,
  }
})
