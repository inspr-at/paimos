// SPDX-License-Identifier: AGPL-3.0-only
// AEON-449: the agents store merges by the server's event-log position. Each case
// runs the real store, lib and api() against a fake server with an event log: a
// read is held, a write or a newer read happens, and the held answer is released
// late. The old answer must never rewind what a newer one or a write established.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { api } from '../src/lib/api'
import { resolveMessage, undoRemoval, type AgentRun, type HarnessSession, type ProjectMessage } from '../src/lib/agents'
import { POSITION_HEADER, resetPositions } from '../src/lib/position'
import { useAgents } from '../src/stores/agents'

vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))

const PROJECT = 'p1'
let position: number
let sessions: HarnessSession[]
let runs: AgentRun[]
let messages: ProjectMessage[]
let requests: string[]
// A GET whose path matches answers with what the server had when it was asked,
// released later: it names the position it had then, like the real header.
let hold: RegExp | null
// One that matches this is processed late instead: the server reads when the answer
// is released, so a request that started earlier can answer with a newer snapshot.
let late: RegExp | null
// One that matches this has an event commit while the server reads it: the answer is
// the newer snapshot and names no position, the way the real middleware answers.
let interrupt: { pattern: RegExp; change: () => void } | null
let current: (session: HarnessSession) => boolean
const held: (() => void)[] = []

const session = (fields: Partial<HarnessSession>) => ({
  id: 's1', project_id: PROJECT, agent_principal_id: 'agent', harness: 'codex', management_mode: 'unmanaged', role: 'worker', phase: 'working', activity: 'busy',
  revision: 1, activity_sequence: 1, advertised_capabilities: [], heartbeat_at: new Date().toISOString(), created_at: '2026-01-01T00:00:00Z', stopped_at: null, run_id: null, ticket_node_id: null,
  work_order_id: null, parent_harness_session_id: null, stop_reason: null, host: 'h', work_shape: 'unknown', ...fields,
}) as HarnessSession
const run = (status: AgentRun['status']) => ({ id: 'r1', agent_principal_id: 'agent', status }) as AgentRun
const message = (fields: Partial<ProjectMessage> = {}) => ({ id: 'm1', sent_event_id: 1, is_action_request: true, status: 'held', ...fields }) as ProjectMessage

function respond(body: unknown, at?: number, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...(at === undefined ? {} : { [POSITION_HEADER]: String(at) }) } })
}
// Every change the server accepts appends one event.
const commit = (change: () => void) => { change(); position++ }

async function serve(input: string, init: RequestInit = {}): Promise<Response> {
  const url = new URL(input, 'http://aeon.test')
  const path = url.pathname.replace(/^\/api/, '')
  const method = init.method ?? 'GET'
  requests.push(`${method} ${path}${url.search}`)
  if (method === 'GET') {
    const key = `${path}${url.search}`
    if (late?.test(key)) return new Promise(resolve => { held.push(() => resolve(respond(structuredClone(read(path, url.searchParams)), position))) })
    if (interrupt?.pattern.test(key)) {
      const { change } = interrupt
      interrupt = null
      change()
      return respond(structuredClone(read(path, url.searchParams)))
    }
    const at = position
    const snapshot = structuredClone(read(path, url.searchParams))
    const answer = () => respond(snapshot, at)
    if (hold?.test(key)) return new Promise(resolve => { held.push(() => resolve(answer())) })
    return answer()
  }
  const archive = /^\/projects\/[^/]+\/harness-sessions\/([^/]+)\/archive$/.exec(path)
  if (archive) {
    commit(() => { sessions = sessions.map(s => s.id === archive[1] ? { ...s, archived_at: '2026-09-30T00:00:00Z' } : s) })
    return respond({}, position)
  }
  if (/^\/events\/\d+\/undo$/.test(path)) {
    commit(() => { sessions = sessions.map(s => ({ ...s, archived_at: null })) })
    return respond({ after: sessions[0] }, position)
  }
  if (/\/messages\/[^/]+\/resolution$/.test(path)) {
    commit(() => { messages = messages.map(m => ({ ...m, human_resolution_outcome: 'resolved' as const })) })
    return respond({ message_id: 'm1', decision: 'resolved', created_at: '2026-09-30T00:00:00Z' }, position)
  }
  return respond({}, position)
}
function read(path: string, query: URLSearchParams): unknown {
  if (path === '/harness-sessions') {
    const ticket = query.get('ticket')
    const items = ticket ? sessions.filter(s => s.ticket_node_id === ticket) : query.get('view') === 'all' ? sessions : sessions.filter(current)
    return { items, next_cursor: null }
  }
  if (path === '/runs') return { items: query.get('agent') && query.get('agent') !== 'agent' ? [] : runs, next_cursor: null }
  if (path === `/projects/${PROJECT}/messages`) return { items: messages, next_after: 0 }
  if (path.endsWith('/message-targets')) return []
  if (path === '/approvals' || path === '/agent-accounts' || path === '/models') return []
  return {}
}
const release = () => { hold = null; late = null; for (const go of held.splice(0)) go() }
const settle = async () => { for (let i = 0; i < 6; i++) await new Promise(resolve => setTimeout(resolve)) }
const reads = (pattern: RegExp) => requests.filter(r => r.startsWith('GET') && pattern.test(r)).length

beforeEach(() => {
  setActivePinia(createPinia())
  resetPositions()
  position = 10
  sessions = [session({})]
  runs = [run('running')]
  messages = [message()]
  requests = []
  hold = null
  late = null
  interrupt = null
  current = s => !s.archived_at
  held.length = 0
  vi.stubGlobal('fetch', vi.fn(serve))
})
afterEach(() => { vi.unstubAllGlobals() })

it('undoing a removal is not reversed by a history read held across it', async () => {
  // Removed more than 24 hours ago: only the history read still carries it.
  sessions = [session({ archived_at: '2026-09-01T00:00:00Z', stopped_at: '2026-09-01T00:00:00Z', phase: 'stopped' })]
  const agents = useAgents()
  hold = /view=all/
  const history = agents.loadHistory()
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null

  // Undo, then apply its answer the way sessionRemoval does.
  agents.recordRemoval(await undoRemoval(7))
  await settle()
  release()
  await history
  await settle()

  expect(reads(/view=all/)).toBe(2)
  expect(agents.historyState).toBe('ready')
  const restored = agents.historyViews.find(v => v.session.id === 's1')
  expect(restored).toBeDefined()
  expect(restored!.session.archived_at).toBeNull()
})

it('a thread read from before a resolution does not null the resolution', async () => {
  const agents = useAgents()
  const s = session({})
  await agents.refreshThread(PROJECT, s.id)
  expect(agents.thread(s)[0]?.human_resolution_outcome).toBeUndefined()

  hold = /session=/
  const thread = agents.refreshThread(PROJECT, s.id)
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  await resolveMessage(PROJECT, 'm1', 'resolved', 'done')
  release()
  await thread

  expect(reads(/messages.*session=/)).toBe(3)
  expect(agents.thread(s)[0]?.human_resolution_outcome).toBe('resolved')
})

it('the global run list and a per-agent list that arrive out of order never rewind a run', async () => {
  const agents = useAgents()
  await agents.refreshAgentRuns('agent')
  expect(agents.runs.r1?.status).toBe('running')

  // The per-agent read is asked while the run still runs, and answers last.
  hold = /runs\?.*agent=/
  const perAgent = agents.refreshAgentRuns('agent')
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  // The run completes without a write of this tab; the global read sees it.
  commit(() => { runs = [run('completed')] })
  await agents.refreshAgentRuns('other')
  await Promise.all([agents.loadAll()])
  expect(agents.runs.r1?.status).toBe('completed')
  release()
  await perAgent

  expect(agents.runs.r1?.status).toBe('completed')
  expect(agents.recentRuns('agent').map(r => r.status)).toEqual(['completed'])
})

it('a write that never calls afterWrite still keeps a held list read from undoing it', async () => {
  const agents = useAgents()
  await agents.refreshSessions()
  expect(agents.views).toHaveLength(1)

  hold = /harness-sessions\?/
  const list = agents.refreshSessions()
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  // A component writes through api() and does not tell the store.
  const response = await api(`/projects/${PROJECT}/harness-sessions/s1/archive`, { method: 'POST' })
  expect(response.ok).toBe(true)
  release()
  await list
  await settle()

  expect(reads(/harness-sessions\?.*view=current/)).toBe(3)
  expect(agents.views).toHaveLength(0)
  expect(agents.sessions).toEqual([])
})

it('answers without a position are ordered by their start, as before', async () => {
  const agents = useAgents()
  vi.stubGlobal('fetch', vi.fn(async (input: string, init: RequestInit = {}) => {
    const response = await serve(input, init)
    response.headers.delete(POSITION_HEADER)
    return new Response(await response.text(), { status: response.status, headers: { 'Content-Type': 'application/json' } })
  }))
  await agents.refreshSessions()
  hold = /harness-sessions\?/
  const list = agents.refreshSessions()
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  commit(() => { sessions = [] })
  void agents.afterWrite()
  await settle()
  release()
  await list
  await settle()
  expect(agents.views).toHaveLength(0)
})

// Review of AEON-449, P1: the header named the newest event before the handler ran, so two
// answers stamped 10 could hold data from before and after event 11, and the stale one
// rewound a run. The server now names a position only for a read no event interrupted.
it('a read an event interrupted names no position and is judged by when it started, in either arrival order', async () => {
  const agents = useAgents()
  await agents.refreshAgentRuns('agent')
  expect(agents.runs.r1?.status).toBe('running')

  // The per-agent read starts first, reads before event 11 and answers last, at exactly 10.
  hold = /runs\?.*agent=/
  const perAgent = agents.refreshAgentRuns('agent')
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  // The global read starts later and an event commits while it runs: it includes event 11, names no position.
  interrupt = { pattern: /runs\?limit=200/, change: () => commit(() => { runs = [run('completed')] }) }
  await agents.loadAll()
  // A position-less answer that started later replaces the exact one it cannot be compared with.
  expect(agents.runs.r1?.status).toBe('completed')

  release()
  await perAgent
  // The exact answer started earlier than the row it meets: it cannot outrank it.
  expect(agents.runs.r1?.status).toBe('completed')
  expect(agents.recentRuns('agent').map(r => r.status)).toEqual(['completed'])
})

it('an exact answer that started later replaces a row an interrupted read brought earlier', async () => {
  const agents = useAgents()
  interrupt = { pattern: /runs\?limit=200/, change: () => commit(() => { runs = [run('completed')] }) }
  await agents.loadAll()
  expect(agents.runs.r1?.status).toBe('completed')
  commit(() => { runs = [run('failed')] })
  await agents.refreshAgentRuns('agent')
  expect(agents.runs.r1?.status).toBe('failed')
})

// Review of AEON-449, P1: History had its own ledger and the current list always won
// over it, so a current-list answer held across a restore brought the archived row back.
it('a current-list answer held across a restore does not bring back the row History read restored', async () => {
  sessions = [session({ archived_at: '2026-09-29T00:00:00Z', stopped_at: '2026-09-29T00:00:00Z', phase: 'stopped', revision: 1 })]
  current = () => true
  const agents = useAgents()
  await agents.refreshSessions()
  expect(agents.removedViews.map(v => v.session.id)).toEqual(['s1'])

  // The current list is read while the session is still removed (revision 1, position 10), and answers last.
  hold = /view=current/
  const list = agents.refreshSessions()
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  // Undo: revision 2 at position 11, which History reads.
  commit(() => { sessions = sessions.map(s => ({ ...s, archived_at: null, revision: 2 })) })
  await agents.loadHistory()
  expect(agents.historyViews.find(v => v.session.id === 's1')?.session.revision).toBe(2)

  release()
  await list
  await settle()

  const back = agents.historyViews.find(v => v.session.id === 's1')
  expect(back?.session.archived_at).toBeNull()
  expect(back?.session.revision).toBe(2)
  expect(agents.removedViews).toHaveLength(0)
  expect(agents.views.map(v => v.session.id)).toEqual(['s1'])
  expect(agents.views[0]?.session.revision).toBe(2)
})

it('the other way round, a History answer held across a removal does not bring the row back', async () => {
  current = () => true
  const agents = useAgents()
  await agents.refreshSessions()
  hold = /view=all/
  const history = agents.loadHistory()
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  commit(() => { sessions = sessions.map(s => ({ ...s, archived_at: '2026-09-30T00:00:00Z', revision: 2 })) })
  await agents.refreshSessions()
  expect(agents.removedViews.map(v => v.session.revision)).toEqual([2])

  release()
  await history
  await settle()
  expect(agents.removedViews.map(v => v.session.revision)).toEqual([2])
  expect(agents.views).toHaveLength(0)
})

// Review of AEON-449, P2: the order the requests started in was compared before the
// server's position, so an earlier-started answer at 11 was dropped after a later one at 10.
it('an earlier-started read that the server answered later, at a higher position, is applied', async () => {
  sessions = [session({ ticket_node_id: null })]
  const agents = useAgents()
  // The ticket read starts first and the server processes it late, after event 11.
  late = /ticket=n1/
  const ticket = agents.ensureTicket('n1')
  await vi.waitFor(() => expect(held).toHaveLength(1))
  // The list starts later and is answered at 10, before the event.
  await agents.refreshSessions()
  expect(agents.sessions.map(s => s.id)).toEqual(['s1'])
  commit(() => { sessions = [...sessions, session({ id: 's2', ticket_node_id: 'n1' })] })
  release()
  await ticket

  expect(agents.sessions.map(s => s.id).sort()).toEqual(['s1', 's2'])
})
