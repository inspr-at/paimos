// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1210 G2/G4 (audit AEON-1218): the shared Agents projection. Risk: a question attaches to a
// lead by principal or to the wrong generation, workers bind by project instead of their real
// parent, a failed or cut-short read looks complete (in the store or in the views that read it),
// a read answered after a sign-in change is shown to the next person, named events flood the
// server with one reread each, or a reconnect refreshes the counts but leaves Next up behind.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { effectScope, ref } from 'vue'
import { addWake, asksBySession, emptyWake, eventNode, queueByProject, subtree } from '../src/lib/agentProjection'
import type { Question } from '../src/lib/decisionDeskApi'

type Handler = (...args: never[]) => unknown
const mocks = vi.hoisted(() => ({
  queue: (() => ({ items: [] })) as Handler, questions: (() => ({ items: [], has_more: false })) as Handler, sessions: (() => ({ items: [], next_cursor: null })) as Handler,
  calls: [] as string[], wake: null as null | ((event?: string, node?: string | null) => void),
  leads: { views: {} as Record<string, { caughtUp: boolean; decisions: unknown[] }>, loadMany: vi.fn(async () => {}), loadLead: vi.fn(async () => {}), load: vi.fn(async () => {}) },
  agents: { loaded: false, views: [] as unknown[], sessionsState: 'ready', sessionsError: '', sessionsStale: false, sessionsUpdatedAt: 1, admitSessions: (rows: unknown[]) => rows, loadAll: vi.fn(async () => {}) },
  work: { snapshots: {} as Record<string, unknown>, load: vi.fn(async (_project: string, _force?: boolean) => {}) },
  liveStatus: null as unknown as { state: string; dataStale: boolean; truncated: boolean; items: unknown[] },
  identity: null as unknown as { identity: { tenant: { id: string }; principal: { id: string } } | null },
  projects: null as unknown as { projects: { id: string; archived: boolean }[] },
}))
vi.mock('../src/lib/api', () => {
  class APIError extends Error { constructor(readonly status: number, message: string) { super(message) } }
  return { APIError, api: async () => new Response('{}') }
})
vi.mock('../src/lib/workQueue', () => ({ queueRequest: async () => { mocks.calls.push('queue'); return mocks.queue() } }))
vi.mock('../src/lib/decisionDeskApi', () => ({ MAX_QUESTION_PAGES: 10, readQuestions: async (query: { offset: number }) => { mocks.calls.push(`questions@${query.offset}`); return mocks.questions(query as never) } }))
vi.mock('../src/lib/agentRows', () => ({ listAllSessions: async (params: { project: string }) => { mocks.calls.push(`sessions:${params.project}`); return mocks.sessions() } }))
vi.mock('../src/lib/agents', () => ({ onProjectionWake: (listener: (event?: string, node?: string | null) => void) => { mocks.wake = listener; return () => { mocks.wake = null } } }))
vi.mock('../src/lib/usePolledData', () => ({ usePoller: () => ({ start() {}, stop() {} }) }))
vi.mock('../src/lib/authz', () => ({ onAccessRevalidate: () => () => {}, can: () => true }))
vi.mock('../src/stores/liveAgents', async () => {
  const { reactive } = await import('vue')
  mocks.liveStatus = reactive({ state: 'ready', dataStale: false, truncated: false, items: [] as unknown[] })
  return { useLiveAgents: () => ({ watch: () => () => {}, get items() { return mocks.liveStatus.items }, get state() { return mocks.liveStatus.state }, get dataStale() { return mocks.liveStatus.dataStale }, get truncated() { return mocks.liveStatus.truncated } }) }
})
vi.mock('../src/stores/projectLeads', () => ({ useProjectLeads: () => mocks.leads }))
vi.mock('../src/stores/agents', () => ({ useAgents: () => mocks.agents }))
vi.mock('../src/stores/workQueue', () => ({ useWorkQueue: () => mocks.work }))
vi.mock('../src/stores/projects', async () => {
  const { reactive } = await import('vue')
  mocks.projects = reactive({ projects: [] as { id: string; archived: boolean }[] })
  return { useProjects: () => mocks.projects }
})
vi.mock('../src/stores/session', async () => {
  const { reactive } = await import('vue')
  mocks.identity = reactive({ identity: { tenant: { id: 't' }, principal: { id: 'markus' } } }) as typeof mocks.identity
  return { useSession: () => mocks.identity }
})
import { useAgentProjection, QUEUE_ITEM_LIMIT, WAKE_BATCH_MS } from '../src/stores/agentProjection'
import { useAgentProjects } from '../src/components/agents/useAgentProjects'
import { useLeadSummary } from '../src/lib/useLeadSummary'

const S1 = '11111111-1111-4111-8111-111111111111', S2 = '22222222-2222-4222-8222-222222222222', W1 = '33333333-3333-4333-8333-333333333333'
const q = (id: string, fields: { session?: string; inputSession?: string; principal?: string; project?: string; state?: 'open' | 'answered' } = {}) => ({
  id, project_id: fields.project ?? 'p1', state: fields.state ?? 'open', revision: 1,
  input: { request_id: id, question: id, options: [], meanwhile: 'carries_on', ...(fields.inputSession ? { session_id: fields.inputSession } : {}) },
  askers: [{ id: `a-${id}`, principal_id: fields.principal ?? 'agent', reply_root_id: 'r', comment_node_id: 'c', input: { request_id: id, question: id, options: [], meanwhile: 'carries_on' }, ...(fields.session ? { session_id: fields.session } : {}) }],
}) as unknown as Question
const flush = () => new Promise(resolve => setTimeout(resolve, 0))
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }

describe('bindings', () => {
  it('bind a question to the exact session that asked, never by principal; one without a session binds to nobody', () => {
    const asks = asksBySession([q('by-session', { session: S1 }), q('by-input', { inputSession: S2 }), q('principal-only', { principal: 'lead-agent' }), q('answered', { session: S1, state: 'answered' })])
    expect(asks.get(S1)?.map(x => x.id)).toEqual(['by-session'])
    expect(asks.get(S2)?.map(x => x.id)).toEqual(['by-input'])
    expect([...asks.keys()]).toEqual([S1, S2])
  })
  it('bind workers by their real parent, at any depth, and survive a cycle', () => {
    const tree = [{ id: 'w1', parent: 'lead' }, { id: 'w2', parent: 'w1' }, { id: 'other', parent: 'another-lead' }, { id: 'x', parent: 'y' }, { id: 'y', parent: 'x' }, { id: 'lead', parent: null }]
    expect([...subtree('lead', tree)].sort()).toEqual(['w1', 'w2'])
    expect([...subtree('x', tree)].sort()).toEqual(['y'])
    expect(queueByProject([{ project_id: 'p1' }, { project_id: 'p1' }, { project_id: null }, { project_id: 'p2' }])).toEqual(new Map([['p1', 2], ['p2', 1]]))
  })
  it('map each named event to what it wakes; no event rereads everything', () => {
    const w = emptyWake()
    addWake(w, 'lead.start_requested', 'p1'); addWake(w, 'node.lead_decision_recorded', 'p2'); addWake(w, 'queue.moved', 'ticket'); addWake(w, 'question.asked', 'question'); addWake(w, 'harness.reparented', 'p1'); addWake(w, 'harness.heartbeat', 'p1')
    expect({ leads: [...w.leads], decisions: [...w.decisions], queue: w.queue, questions: w.questions, trees: [...w.trees], all: w.all }).toEqual({ leads: ['p1', 'p2'], decisions: ['p2'], queue: true, questions: true, trees: ['p1'], all: false })
    expect(addWake(emptyWake()).all).toBe(true)
    expect(eventNode(JSON.stringify({ node_id: 'p1', after: {} }))).toBe('p1')
    expect(eventNode('not json')).toBeNull()
    expect(eventNode(JSON.stringify({ node_id: 'x'.repeat(65) }))).toBeNull()
  })
})

describe('the store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    mocks.calls.length = 0
    mocks.queue = () => ({ items: [{ project_id: 'p1' }, { project_id: 'p1' }, { project_id: 'p2' }] })
    mocks.questions = () => ({ items: [q('q1', { session: S1 })], has_more: false })
    mocks.sessions = () => ({ items: [], next_cursor: null })
    mocks.leads.views = {}; mocks.leads.loadMany.mockClear(); mocks.leads.loadLead.mockClear(); mocks.leads.load.mockClear()
    Object.assign(mocks.agents, { loaded: false, views: [], sessionsState: 'ready', sessionsError: '', sessionsStale: false, sessionsUpdatedAt: 1 })
    Object.assign(mocks.liveStatus, { state: 'ready', dataStale: false, truncated: false })
    mocks.agents.loadAll.mockClear(); mocks.work.load.mockReset(); mocks.work.snapshots = {}; mocks.liveStatus.items = []
    mocks.identity.identity = { tenant: { id: 't' }, principal: { id: 'markus' } }
    mocks.projects.projects = [{ id: 'p1', archived: false }, { id: 'p2', archived: false }, { id: 'old', archived: true }]
  })
  afterEach(() => { vi.useRealTimers() })

  it('reads the queue, the open questions and every active project’s lead while the page shows them', async () => {
    const projection = useAgentProjection()
    const stop = projection.watchProjects({ all: true })
    await flush()
    expect(mocks.leads.loadMany).toHaveBeenCalledWith(['p1', 'p2'])
    expect([projection.queuedOf('p1'), projection.queuedOf('p2'), projection.queuedOf('p3')]).toEqual([2, 1, 0])
    expect(projection.asksOf(S1).map(x => x.id)).toEqual(['q1'])
    expect(projection.asksOf(S2)).toEqual([])
    stop()
  })

  it('reads every lead again once the projects list arrives after the page', async () => {
    mocks.projects.projects = []
    const projection = useAgentProjection()
    const stop = projection.watchProjects({ all: true })
    await flush()
    mocks.leads.loadMany.mockClear()
    mocks.projects.projects = [{ id: 'p1', archived: false }, { id: 'p3', archived: false }]
    await flush()
    expect(mocks.leads.loadMany).toHaveBeenCalledWith(['p1', 'p3'])
    stop()
  })

  it('reads open questions within five pages and says when more exist; a failed later page keeps the rest', async () => {
    const projection = useAgentProjection()
    mocks.questions = (query: { offset: number }) => ({ items: Array.from({ length: 100 }, (_, i) => q(`q-${query.offset + i}`, { session: S1 })), has_more: true })
    projection.watchProjects({ all: true })
    await flush()
    expect(mocks.calls.filter(c => c.startsWith('questions'))).toEqual(['questions@0', 'questions@100', 'questions@200', 'questions@300', 'questions@400'])
    expect(projection.questions).toMatchObject({ state: 'ready', partial: true })
    mocks.calls.length = 0
    mocks.questions = (query: { offset: number }) => { if (query.offset > 0) throw new Error('page failed'); return { items: [q('first', { session: S1 })], has_more: true } }
    await projection.refresh()
    expect(projection.asksOf(S1).map(x => x.id)).toEqual(['first'])
    expect(projection.questions.partial).toBe(true)
  })

  it('a failed read is said: queue counts become unknown, questions keep what was shown as incomplete', async () => {
    const projection = useAgentProjection()
    projection.watchProjects({ all: true })
    await flush()
    mocks.queue = () => { throw new Error('queue down') }
    mocks.questions = () => { throw new Error('desk down') }
    await projection.refresh()
    expect(projection.queuedOf('p1')).toBeNull()
    expect(projection.queue).toMatchObject({ state: 'error', error: 'queue down' })
    expect(projection.asksOf(S1).map(x => x.id)).toEqual(['q1'])
    expect(projection.questions).toMatchObject({ state: 'error', partial: true })
  })

  it('a read answered after a sign-in change is never shown to the next person', async () => {
    const projection = useAgentProjection()
    const slow = deferred<unknown>()
    mocks.queue = () => slow.promise
    projection.watchProjects({ all: true })
    await flush()
    mocks.identity.identity = { tenant: { id: 't' }, principal: { id: 'mia' } }
    slow.resolve({ items: [{ project_id: 'p1' }] })
    await flush()
    expect(projection.queuedOf('p1')).toBeNull()
    expect(projection.asksOf(S1)).toEqual([])
  })

  it('AEON-1212: overlapping opening refreshes await the same authoritative question read', async () => {
    const openingAnswer = deferred<unknown>(), laterAnswer = deferred<unknown>()
    let reads = 0
    mocks.questions = () => ++reads <= 2 ? openingAnswer.promise : laterAnswer.promise
    const projection = useAgentProjection()
    const stop = projection.watchProjects({ all: true })
    const opening = projection.refresh(), overlapping = projection.refresh()
    try {
      // An automatic read, the panel opening and a later poll overlap before
      // any answer lands. The opening cannot finish on a discarded result.
      openingAnswer.resolve({ items: [q('urgent', { session: S1 })], has_more: false })
      await opening
      expect(projection.asksOf(S1).map(item => item.id)).toEqual(['urgent'])
      expect(reads).toBe(1)
    } finally {
      laterAnswer.resolve({ items: [q('urgent', { session: S1 })], has_more: false })
      await overlapping
      stop()
    }
  })

  it('AEON-1212: failed or stale live reads cannot prove an empty worker inventory; truncated live reads are lower bounds', async () => {
    mocks.leads.views = { p1: { lead: { session_id: S1, state: 'working' }, caughtUp: false, decisions: [] } } as never
    mocks.sessions = () => ({ items: [{ id: W1, parent_harness_session_id: S1 }], next_cursor: null })
    mocks.liveStatus.items = [{ session_id: W1, project_id: 'p1', stopped_at: null, role: 'worker' }]
    const scope = effectScope()
    const summary = scope.run(() => useLeadSummary(ref('p1'), ref('P1')))!
    try {
      await useAgentProjection().refresh()
      expect(summary.workers.value.map(worker => worker.session_id)).toEqual([W1])
      expect([summary.workersKnown.value, summary.workersPartial.value]).toEqual([true, false])
      for (const state of ['idle', 'unavailable']) {
        mocks.liveStatus.items = []; mocks.liveStatus.state = state
        expect(summary.workersKnown.value).toBe(false)
        expect(summary.workers.value).toEqual([])
      }
      mocks.liveStatus.state = 'ready'; mocks.liveStatus.dataStale = true
      expect(summary.workersKnown.value).toBe(false)
      mocks.liveStatus.items = [{ session_id: W1, project_id: 'p1', stopped_at: null, role: 'worker' }]
      expect(summary.workers.value).toEqual([])
      mocks.liveStatus.dataStale = false; mocks.liveStatus.truncated = true
      expect(summary.workers.value.map(worker => worker.session_id)).toEqual([W1])
      expect([summary.workersKnown.value, summary.workersPartial.value]).toEqual([true, true])
      mocks.liveStatus.items = []
      expect([summary.workersKnown.value, summary.workersPartial.value]).toEqual([true, true])
    } finally { scope.stop() }
  })

  it('AEON-1212: a loaded Agents bundle does not prove that its session tree was read successfully', () => {
    const projection = useAgentProjection()
    mocks.agents.loaded = true
    Object.assign(mocks.agents, { sessionsState: 'error', sessionsError: 'sessions down', sessionsUpdatedAt: null })
    expect(projection.treeSource('p1')).toMatchObject({ state: 'error', error: 'sessions down' })
    expect(projection.workersOf('p1', S1)).toBeNull()
    Object.assign(mocks.agents, { sessionsState: 'ready', sessionsError: '', sessionsUpdatedAt: 1 })
    mocks.agents.views = [{ session: { id: W1, project_id: 'p1', parent_harness_session_id: S1 } }]
    expect([...projection.workersOf('p1', S1)!]).toEqual([W1])
    // The polling state retains ready after one failed refresh. Its error and
    // stale evidence still prevent a current, complete inventory claim.
    Object.assign(mocks.agents, { sessionsError: 'refresh failed', sessionsStale: true })
    expect(projection.treeSource('p1')).toMatchObject({ state: 'error', error: 'refresh failed' })
    expect(projection.workersOf('p1', S1)).toBeNull()
    Object.assign(mocks.agents, { sessionsError: '', sessionsStale: true })
    expect(projection.workersOf('p1', S1)).toBeNull()
  })

  it('batches named events into one reread of what they changed; a reconnect rereads everything', async () => {
    vi.useFakeTimers()
    const projection = useAgentProjection()
    projection.watchProjects({ all: true })
    await vi.advanceTimersByTimeAsync(0)
    mocks.calls.length = 0; mocks.leads.loadMany.mockClear()
    mocks.leads.views = { p2: { caughtUp: true, decisions: [] } }
    for (const event of ['queue.added', 'queue.moved', 'queue.picked_up']) mocks.wake!(event, 'ticket')
    mocks.wake!('lead.claim_checked', 'p1'); mocks.wake!('lead.removed', 'p1'); mocks.wake!('lead.start_requested', 'not-a-project')
    mocks.wake!('node.lead_decision_recorded', 'p2')
    expect(mocks.calls).toEqual([])
    await vi.advanceTimersByTimeAsync(WAKE_BATCH_MS)
    expect(mocks.calls).toEqual(['queue'])
    expect(mocks.leads.loadLead.mock.calls).toEqual([['p1']])
    expect(mocks.leads.load.mock.calls).toEqual([['p2']])
    expect(mocks.leads.loadMany).not.toHaveBeenCalled()
    mocks.calls.length = 0
    mocks.wake!('question.answered', 'question'); mocks.wake!('question.asked', 'question')
    await vi.advanceTimersByTimeAsync(WAKE_BATCH_MS)
    expect(mocks.calls).toEqual(['questions@0'])
    mocks.calls.length = 0
    mocks.wake!()
    await vi.advanceTimersByTimeAsync(WAKE_BATCH_MS)
    expect(mocks.calls.sort()).toEqual(['questions@0', 'queue'])
    expect(mocks.leads.loadMany).toHaveBeenCalledWith(['p1', 'p2'])
  })

  it('a reconnect or a poll rereads Next up with the counts: a missed queue event leaves neither behind', async () => {
    vi.useFakeTimers()
    // The server's queue; Next up (the project's own read) and the list's counts both come from it.
    let server = [{ project_id: 'p1', key: 'P1-1' }, { project_id: 'p1', key: 'P1-2' }]
    mocks.queue = () => ({ items: server })
    mocks.work.load.mockImplementation(async (project: string) => { mocks.work.snapshots[project] = { items: server.filter(item => item.project_id === project) } })
    const projection = useAgentProjection()
    projection.watchProjects({ all: true })
    projection.watchProjects({ project: 'p1' })
    await vi.advanceTimersByTimeAsync(0)
    const nextUp = () => (mocks.work.snapshots.p1 as { items: { key: string }[] }).items.map(item => item.key)
    expect([projection.queuedOf('p1'), nextUp()]).toEqual([2, ['P1-1', 'P1-2']])
    // The queue moves while the stream is down: no queue event arrives, only the reconnect.
    server = [{ project_id: 'p1', key: 'P1-9' }]
    mocks.wake!()
    await vi.advanceTimersByTimeAsync(WAKE_BATCH_MS)
    expect([projection.queuedOf('p1'), nextUp()]).toEqual([1, ['P1-9']])
    // The periodic reread does the same.
    server = []
    await projection.refresh()
    expect([projection.queuedOf('p1'), nextUp()]).toEqual([0, []])
  })

  it('the Agents list hears when the queue failed or was read in part, and when not every question was read', async () => {
    const projection = useAgentProjection()
    const scope = effectScope()
    const { view } = scope.run(() => useAgentProjects())!
    await flush()
    expect(view.value).toMatchObject({ queueError: '', queuePartial: false, asksPartial: false })
    expect(view.value.queued('p1')).toBe(2)
    // More queued items than one read keeps, and more open questions than five pages.
    mocks.queue = () => ({ items: Array.from({ length: QUEUE_ITEM_LIMIT + 1 }, () => ({ project_id: 'p1' })) })
    mocks.questions = (query: { offset: number }) => ({ items: Array.from({ length: 100 }, (_, i) => q(`q-${query.offset + i}`, { session: S1 })), has_more: true })
    await projection.refresh()
    expect(view.value).toMatchObject({ queueError: '', queuePartial: true, asksPartial: true })
    expect(view.value.queued('p1')).toBe(QUEUE_ITEM_LIMIT)
    // The queue fails: its counts are unknown and the list is told why, never shown nothing silently.
    mocks.queue = () => { throw new Error('queue down') }
    await projection.refresh()
    expect(view.value.queued('p1')).toBeNull()
    expect(view.value).toMatchObject({ queueError: 'queue down', queuePartial: false })
    scope.stop()
  })

  it('a lead’s workers from a session read cut short are a lower bound; a cut-short queue count never stands in for the project’s own', async () => {
    mocks.leads.views = { p1: { lead: { project_id: 'p1', state: 'working', session_id: S1, reason: '', revision: 1, generation: 1, process_active: true }, caughtUp: false, decisions: [] } } as never
    mocks.liveStatus.items = [{ session_id: W1, project_id: 'p1', stopped_at: null, role: 'worker' }]
    mocks.sessions = () => ({ items: [{ id: S1, parent_harness_session_id: null }, { id: W1, parent_harness_session_id: S1 }], next_cursor: 'more' })
    mocks.queue = () => ({ items: Array.from({ length: QUEUE_ITEM_LIMIT + 1 }, () => ({ project_id: 'p1' })) })
    mocks.work.snapshots = { p1: { items: [{ key: 'P1-1' }, { key: 'P1-2' }, { key: 'P1-3' }] } }
    const scope = effectScope()
    const summary = scope.run(() => useLeadSummary(ref('p1'), ref('P1')))!
    await flush()
    expect(summary.workers.value.map(w => w.session_id)).toEqual([W1])
    expect([summary.workersKnown.value, summary.workersPartial.value]).toEqual([true, true])
    expect(summary.queuedCount.value).toBe(3)
    // The whole tree read: the count is exact.
    mocks.sessions = () => ({ items: [{ id: S1, parent_harness_session_id: null }, { id: W1, parent_harness_session_id: S1 }], next_cursor: null })
    await useAgentProjection().refresh()
    expect(summary.workersPartial.value).toBe(false)
    scope.stop()
  })

  it('binds a lead band’s workers by parent from the project’s own session read when the Agents list is not loaded', async () => {
    const projection = useAgentProjection()
    expect(projection.workersOf('p1', S1)).toBeNull()
    mocks.sessions = () => ({ items: [{ id: W1, parent_harness_session_id: S1 }, { id: S2, parent_harness_session_id: null }, { id: 'w-other', parent_harness_session_id: S2 }], next_cursor: null })
    projection.watchProjects({ project: 'p1' })
    await flush()
    expect(mocks.calls).toContain('sessions:p1')
    expect([...projection.workersOf('p1', S1)!]).toEqual([W1])
    expect(projection.workersOf('p1', null)).toEqual(new Set())
    mocks.sessions = () => { throw new Error('sessions down') }
    await projection.refresh()
    expect(projection.workersOf('p1', S1)).toBeNull()
    expect(projection.treeSource('p1')).toMatchObject({ state: 'error' })
  })
})
