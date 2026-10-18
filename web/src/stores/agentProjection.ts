// SPDX-License-Identifier: AGPL-3.0-only
// One session-bound projection of the projects' leads, their queued work, the open questions and
// the session tree (AEON-1210 G2/G4, audit AEON-1218). The Agents tree, the lead band and panel and
// the later Agents packages read the same bindings here, so counts and priorities never disagree.
// Every read is bounded, belongs to one person (a sign-in change drops it) and reports its own
// failure or partial result; named server events wake a batched reread of what they changed.
import { defineStore } from 'pinia'
import { computed, onScopeDispose, reactive, ref, watch } from 'vue'
import { APIError } from '../lib/api'
import { onAccessRevalidate } from '../lib/authz'
import { listAllSessions } from '../lib/agentRows'
import { onProjectionWake } from '../lib/agents'
import { addWake, asksBySession, emptyWake, queueByProject, subtree, type Wake } from '../lib/agentProjection'
import { readQuestions, MAX_QUESTION_PAGES, type Question } from '../lib/decisionDeskApi'
import { usePoller } from '../lib/usePolledData'
import { queueRequest, type QueueWireSnapshot } from '../lib/workQueue'
import { useAgents } from './agents'
import { useProjectLeads } from './projectLeads'
import { useProjects } from './projects'
import { useSession } from './session'
import { useWorkQueue } from './workQueue'

export type SourceState = 'idle' | 'loading' | 'ready' | 'error' | 'forbidden'
export interface Source { state: SourceState; error: string; partial: boolean; stale?: boolean }
/** Open questions read per refresh: at most this many pages of 100. More are reported as partial. */
export const QUESTION_PAGE_LIMIT = Math.min(5, MAX_QUESTION_PAGES)
/** Queued items kept from one queue read. */
export const QUEUE_ITEM_LIMIT = 2000
/** Sessions read for a project's tree where the Agents list is not loaded. */
export const TREE_LIMIT = 200
export const WAKE_BATCH_MS = 400
export const PROJECTION_POLL_MS = 30_000

const failure = (e: unknown, fallback: string): Pick<Source, 'state' | 'error'> => e instanceof APIError && e.status === 403
  ? { state: 'forbidden', error: 'You may not see this.' }
  : { state: 'error', error: e instanceof Error && e.message ? e.message : fallback }

export const useAgentProjection = defineStore('agentProjection', () => {
  const session = useSession(), leads = useProjectLeads(), projects = useProjects(), agents = useAgents(), workQueue = useWorkQueue()
  const owner = computed(() => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : '')
  const queue = reactive<Source & { counts: Map<string, number> }>({ state: 'idle', error: '', partial: false, counts: new Map() })
  const questions = reactive<Source & { items: Question[] }>({ state: 'idle', error: '', partial: false, items: [] })
  // Parent links of a project's sessions, for pages where the Agents list is not loaded.
  const trees = reactive<Record<string, Source & { parents: Map<string, string | null> }>>({})
  let epoch = 0, queueRead = 0, questionRead = 0
  const treeReads = new Map<string, number>()
  const flights = new Map<string, { again: boolean; promise: Promise<void> }>()
  let pending: Wake = emptyWake(), timer: ReturnType<typeof setTimeout> | undefined
  function reset() {
    epoch++
    Object.assign(queue, { state: 'idle', error: '', partial: false, counts: new Map() })
    Object.assign(questions, { state: 'idle', error: '', partial: false, items: [] })
    for (const id of Object.keys(trees)) delete trees[id]
    flights.clear()
    pending = emptyWake(); clearTimeout(timer); timer = undefined
  }
  watch(owner, reset, { flush: 'sync' })

  // ---------- Reads ----------
  // An opening joins the source's current read instead of invalidating it.
  // A live change during that read requests one catch-up; every waiter stays
  // attached until the catch-up is applied. Reset detaches the previous owner.
  function joinRead(key: string, read: () => Promise<void>, changed = false): Promise<void> {
    const pending = flights.get(key)
    if (pending) { if (changed) pending.again = true; return pending.promise }
    const turn = epoch, flight = { again: false, promise: Promise.resolve() }
    flights.set(key, flight)
    flight.promise = (async () => {
      do { flight.again = false; await read() } while (flight.again && turn === epoch)
    })().finally(() => { if (flights.get(key) === flight) flights.delete(key) })
    return flight.promise
  }
  const readQueue = (changed = false) => joinRead('queue', readQueueOnce, changed)
  const readOpenQuestions = (changed = false) => joinRead('questions', readOpenQuestionsOnce, changed)
  const readTree = (projectId: string, changed = false) => joinRead(`tree:${projectId}`, () => readTreeOnce(projectId), changed)
  async function readQueueOnce() {
    const turn = epoch, mine = ++queueRead
    if (queue.state === 'idle') queue.state = 'loading'
    try {
      const raw = await queueRequest<QueueWireSnapshot>('/queue')
      if (turn !== epoch || mine !== queueRead) return
      const items = raw.items.slice(0, QUEUE_ITEM_LIMIT)
      Object.assign(queue, { state: 'ready', error: '', partial: raw.items.length > QUEUE_ITEM_LIMIT, counts: queueByProject(items) })
    } catch (e) {
      // Counts that cannot be read are unknown, never the last ones shown.
      if (turn === epoch && mine === queueRead) Object.assign(queue, { ...failure(e, 'The queue can’t be read right now.'), partial: false, counts: new Map() })
    }
  }
  async function readOpenQuestionsOnce() {
    const turn = epoch, mine = ++questionRead
    if (questions.state === 'idle') questions.state = 'loading'
    const items = new Map<string, Question>()
    let partial = false
    try {
      for (let page = 0; page < QUESTION_PAGE_LIMIT; page++) {
        let result
        try { result = await readQuestions({ state: 'open', offset: page * 100 }) } catch (e) { if (page === 0) throw e; partial = true; break }
        if (result.items.length > 100) { partial = true; break }
        for (const question of result.items) items.set(question.id, question)
        if (!result.has_more) break
        if (page === QUESTION_PAGE_LIMIT - 1) partial = true
      }
      if (turn !== epoch || mine !== questionRead) return
      Object.assign(questions, { state: 'ready', error: '', partial, items: [...items.values()] })
    } catch (e) {
      // A failed read keeps the questions last shown and says the list is incomplete; a refusal shows none.
      if (turn !== epoch || mine !== questionRead) return
      const refused = failure(e, 'Open questions can’t be read right now.')
      Object.assign(questions, { ...refused, partial: refused.state !== 'forbidden', items: refused.state === 'forbidden' ? [] : questions.items })
    }
  }
  async function readTreeOnce(projectId: string) {
    const turn = epoch, mine = (treeReads.get(projectId) ?? 0) + 1
    treeReads.set(projectId, mine)
    trees[projectId] ??= { state: 'loading', error: '', partial: false, parents: new Map() }
    try {
      const page = await listAllSessions({ project: projectId, view: 'current', limit: TREE_LIMIT })
      if (turn !== epoch || treeReads.get(projectId) !== mine) return
      // The read passes the session ledger like every other copy (AEON-449).
      const rows = agents.admitSessions(page.items)
      trees[projectId] = { state: 'ready', error: '', partial: !!page.next_cursor, parents: new Map(rows.map(s => [s.id, s.parent_harness_session_id ?? null])) }
    } catch (e) {
      if (turn === epoch && treeReads.get(projectId) === mine) trees[projectId] = { ...failure(e, 'The project’s sessions can’t be read right now.'), partial: false, parents: new Map() }
    }
  }
  const activeProjects = () => projects.projects.filter(p => !p.archived).map(p => p.id)

  // ---------- Who reads: the Agents page reads every project, a lead band or panel its own ----------
  const watchers = new Map<symbol, { all: boolean; project?: string }>()
  const allWatchers = ref(0)
  const watchedProjects = () => [...new Set([...watchers.values()].flatMap(w => w.project ? [w.project] : []))]
  const everyProject = () => [...watchers.values()].some(w => w.all)
  function refresh(changed = false) {
    if (!owner.value || !watchers.size) return Promise.resolve()
    const reads: Promise<unknown>[] = [readQueue(changed), readOpenQuestions(changed)]
    if (everyProject()) reads.push(leads.loadMany(activeProjects()))
    for (const id of watchedProjects()) {
      if (!agents.loaded) reads.push(readTree(id, changed))
      // Next up reads the project's own queue: a reconnect or a poll rereads it with the counts,
      // so a missed queue event never leaves the panel behind the list (AEON-1210 G4).
      reads.push(workQueue.load(id, true))
    }
    return Promise.all(reads)
  }
  const poller = usePoller(refresh, PROJECTION_POLL_MS)
  /** Starts the shared reads while a view shows them; the returned function stops this view's part. */
  function watchProjects(scope: { all: true } | { project: string }) {
    const key = Symbol('projection')
    const first = !watchers.size
    const all = 'all' in scope
    watchers.set(key, all ? { all: true } : { all: false, project: scope.project })
    if (all) allWatchers.value++
    if (first) poller.start()
    void refresh()
    return () => { if (!watchers.delete(key)) return; if (all) allWatchers.value--; if (!watchers.size) poller.stop() }
  }
  // The projects list may arrive after the page: every active project's lead is read once it is known.
  watch(() => allWatchers.value > 0 && owner.value ? activeProjects().join(',') : '', (ids, before) => { if (ids && ids !== before) void leads.loadMany(ids.split(',')) })

  // ---------- Live: batched wakes from the named events (lib/agents.ts registers them centrally) ----------
  function wake(event?: string, node?: string | null) {
    if (!watchers.size) return
    addWake(pending, event, node)
    timer ??= setTimeout(flush, WAKE_BATCH_MS)
  }
  function flush() {
    timer = undefined
    const w = pending; pending = emptyWake()
    if (!owner.value || !watchers.size) return
    if (w.all) { void refresh(true); if (agents.loaded) void agents.loadAll(); return }
    const known = new Set(projects.projects.map(p => p.id))
    if (w.anyLead && everyProject()) void leads.loadMany(activeProjects())
    for (const id of w.leads) {
      if (!known.has(id)) continue
      // A decision continues the history a panel already read; otherwise the lead state alone.
      const view = leads.views[id]
      if (w.decisions.has(id) && view && (view.caughtUp || view.decisions.length)) void leads.load(id)
      else void leads.loadLead(id)
    }
    if (w.queue) {
      void readQueue(true)
      for (const id of Object.keys(workQueue.snapshots)) void workQueue.load(id, true)
    }
    if (w.questions) void readOpenQuestions(true)
    if (w.trees.size) {
      if (agents.loaded) void agents.loadAll()
      for (const id of w.trees) if (trees[id]) void readTree(id, true)
    }
  }
  const stopWakes = onProjectionWake(wake)
  const stopAccess = onAccessRevalidate({ reset, refresh: () => { void refresh() } })
  onScopeDispose(() => { stopWakes(); stopAccess(); poller.stop(); clearTimeout(timer) })

  // ---------- Bindings everyone reads ----------
  const asks = computed(() => asksBySession(questions.items))
  /** Open questions asked by exactly this session. */
  const asksOf = (sessionId: string | null | undefined): Question[] => sessionId ? asks.value.get(sessionId) ?? [] : []
  /** Queued work items of a project; null while the queue cannot be read. */
  const queuedOf = (projectId: string | null | undefined): number | null => !projectId || queue.state !== 'ready' ? null : queue.counts.get(projectId) ?? 0
  /** The sessions under a lead session, by real parent identity; null while the tree is unknown. */
  function workersOf(projectId: string, leadSessionId: string | null | undefined): Set<string> | null {
    if (!leadSessionId) return new Set()
    const source = treeSource(projectId)
    if (source.state !== 'ready' || source.stale) return null
    if (agents.loaded) return subtree(leadSessionId, agents.views.filter(v => v.session.project_id === projectId).map(v => ({ id: v.session.id, parent: v.session.parent_harness_session_id })))
    const tree = trees[projectId]!
    return subtree(leadSessionId, [...tree.parents].map(([id, parent]) => ({ id, parent })))
  }
  const treeSource = (projectId: string): Source => agents.loaded ? {
    state: agents.sessionsState === 'ready' && agents.sessionsError ? 'error' : agents.sessionsState,
    error: agents.sessionsError, partial: false, stale: agents.sessionsStale || agents.sessionsUpdatedAt === null,
  } : trees[projectId] ?? { state: 'idle', error: '', partial: false }
  return { queue, questions, trees, watchProjects, refresh, wake, flush, asksOf, queuedOf, workersOf, treeSource }
})
