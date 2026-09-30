// SPDX-License-Identifier: AGPL-3.0-only
// AEON-449: the agents store keeps every session and run by the row's own revision
// (row_version), with the server's event-log position ordering the reads of one
// collection. Each case runs the real store, lib and api() against a fake server with
// an event log and per-row versions: a read or a write is held, a newer answer lands,
// and the held answer is released late. The old answer must never rewind what a newer
// one or a write established, however it reached the page.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { api } from '../src/lib/api'
import { resolveMessage, type ProjectMessage } from '../src/lib/agents'
import { createRun, getRun, getSession, listAllSessions, listRuns, removeSession, runNowOnce, undoRemoval, type AgentRunRow, type HarnessSessionRow } from '../src/lib/agentRows'
import { POSITION_HEADER, resetPositions, tick } from '../src/lib/position'
import { wrapRow } from '../src/lib/wire'
import { useAgents } from '../src/stores/agents'

vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined, load: async () => {} }) }))

const PROJECT = 'p1'
let position: number
let sessions: HarnessSessionRow[]
let runs: AgentRunRow[]
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
let current: (session: HarnessSessionRow) => boolean
// A write whose path matches is processed at once, but its answer is released later, with
// the position the tenant has reached by then: the watermark the real middleware reads
// after the commit, which can be newer than the body.
let holdWrite: RegExp | null
const held: (() => void)[] = []

const session = (fields: Partial<HarnessSessionRow>) => ({
  id: 's1', project_id: PROJECT, agent_principal_id: 'agent', harness: 'codex', management_mode: 'unmanaged', role: 'worker', phase: 'working', activity: 'busy',
  revision: 1, row_version: 1, activity_sequence: 1, advertised_capabilities: [], heartbeat_at: new Date().toISOString(), created_at: '2026-01-01T00:00:00Z', stopped_at: null, run_id: null, ticket_node_id: null,
  work_order_id: null, parent_harness_session_id: null, stop_reason: null, host: 'h', work_shape: 'unknown', ...fields,
}) as HarnessSessionRow
const run = (status: AgentRunRow['status'], rowVersion = 1) => ({ id: 'r1', agent_principal_id: 'agent', status, row_version: rowVersion }) as AgentRunRow
// A row as the server changes it: every change raises the row's own version.
const changed = <T extends { row_version?: number }>(row: T, fields: Partial<T>): T => ({ ...row, ...fields, row_version: (row.row_version ?? 0) + 1 })
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
    commit(() => { sessions = sessions.map(s => s.id === archive[1] ? changed(s, { archived_at: '2026-09-30T00:00:00Z' }) : s) })
    return respond({}, position)
  }
  if (/^\/events\/\d+\/undo$/.test(path)) {
    commit(() => { sessions = sessions.map(s => changed(s, { archived_at: null })) })
    return respond({ after: sessions[0] }, position)
  }
  const removal = /^\/projects\/[^/]+\/harness-sessions\/([^/]+)\/remove$/.exec(path)
  if (removal) {
    commit(() => { sessions = sessions.map(s => s.id === removal[1] ? changed(s, { archived_at: '2026-09-30T00:00:00Z' }) : s) })
    // A bare row, as a mutation answers: no project, ticket or agent summaries.
    const { project: _project, ticket: _ticket, agent: _agent, ...bare } = structuredClone(sessions.find(s => s.id === removal[1])!)
    return respond({ session: bare, message: 'Record removed; process not stopped by removal.', processes_signalled: false, process_state: 'unknown', event_id: 7, undoable: true }, position)
  }
  const override = /^\/runs\/([^/]+)\/capacity-override$/.exec(path)
  if (override) {
    commit(() => { runs = runs.map(r => r.id === override[1] ? changed(r, { capacity_override: 'now' as const }) : r) })
    const body = structuredClone(runs.find(r => r.id === override[1]))
    if (holdWrite?.test(path)) return new Promise(resolve => { held.push(() => resolve(respond(body, position))) })
    return respond(body, position)
  }
  const launch = /^\/work-orders\/([^/]+)\/runs$/.exec(path)
  if (launch) {
    commit(() => { runs = [...runs, { ...run('queued'), id: 'launched', work_order_id: launch[1] }] })
    const body = structuredClone(runs.find(r => r.id === 'launched'))
    if (holdWrite?.test(path)) return new Promise(resolve => { held.push(() => resolve(respond(body, position))) })
    return respond(body, position)
  }
  if (/\/messages\/[^/]+\/resolution$/.test(path)) {
    commit(() => { messages = messages.map(m => ({ ...m, human_resolution_outcome: 'resolved' as const })) })
    return respond({ message_id: 'm1', decision: 'resolved', created_at: '2026-09-30T00:00:00Z' }, position)
  }
  return respond({}, position)
}
function read(path: string, query: URLSearchParams): unknown {
  const detail = new RegExp(`^/projects/${PROJECT}/harness-sessions/([^/]+)$`).exec(path)
  if (detail) return { ...sessions.find(s => s.id === detail[1]), metadata_history: [{ field: 'model', previous_value: null, value: 'gpt', at: '2026-01-01T00:00:00Z' }] }
  const single = /^\/runs\/([^/]+)$/.exec(path)
  if (single) return runs.find(r => r.id === single[1])
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
const release = () => { hold = null; late = null; holdWrite = null; for (const go of held.splice(0)) go() }
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
  holdWrite = null
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
  agents.recordSession(await undoRemoval(7))
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
  commit(() => { runs = [run('completed', 2)] })
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
  interrupt = { pattern: /runs\?limit=200/, change: () => commit(() => { runs = [run('completed', 2)] }) }
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
  interrupt = { pattern: /runs\?limit=200/, change: () => commit(() => { runs = [run('completed', 2)] }) }
  await agents.loadAll()
  expect(agents.runs.r1?.status).toBe('completed')
  commit(() => { runs = [run('failed', 3)] })
  await agents.refreshAgentRuns('agent')
  expect(agents.runs.r1?.status).toBe('failed')
})

// Review of AEON-449, P1: History had its own ledger and the current list always won
// over it, so a current-list answer held across a restore brought the archived row back.
it('a current-list answer held across a restore does not bring back the row History read restored', async () => {
  sessions = [session({ archived_at: '2026-09-29T00:00:00Z', stopped_at: '2026-09-29T00:00:00Z', phase: 'stopped', row_version: 1 })]
  current = () => true
  const agents = useAgents()
  await agents.refreshSessions()
  expect(agents.removedViews.map(v => v.session.id)).toEqual(['s1'])

  // The current list is read while the session is still removed (row_version 1, position 10), and answers last.
  hold = /view=current/
  const list = agents.refreshSessions()
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  // Undo: row_version 2 at position 11, which History reads.
  commit(() => { sessions = sessions.map(s => changed(s, { archived_at: null })) })
  await agents.loadHistory()
  expect(agents.historyViews.find(v => v.session.id === 's1')?.session.row_version).toBe(2)

  release()
  await list
  await settle()

  const back = agents.historyViews.find(v => v.session.id === 's1')
  expect(back?.session.archived_at).toBeNull()
  expect(back?.session.row_version).toBe(2)
  expect(agents.removedViews).toHaveLength(0)
  expect(agents.views.map(v => v.session.id)).toEqual(['s1'])
  expect(agents.views[0]?.session.row_version).toBe(2)
})

it('the other way round, a History answer held across a removal does not bring the row back', async () => {
  current = () => true
  const agents = useAgents()
  await agents.refreshSessions()
  hold = /view=all/
  const history = agents.loadHistory()
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  commit(() => { sessions = sessions.map(s => changed(s, { archived_at: '2026-09-30T00:00:00Z' })) })
  await agents.refreshSessions()
  expect(agents.removedViews.map(v => v.session.row_version)).toEqual([2])

  release()
  await history
  await settle()
  expect(agents.removedViews.map(v => v.session.row_version)).toEqual([2])
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

it('a ticket view held across a newer copy History read does not rewind the row', async () => {
  sessions = [session({ ticket_node_id: 'n1', row_version: 1 })]
  const agents = useAgents()
  await agents.refreshSessions()

  hold = /ticket=n1/
  const ticket = agents.ensureTicket('n1')
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  // The session stops (row_version 2, position 11); History reads it.
  commit(() => { sessions = sessions.map(s => changed(s, { phase: 'stopped' as const, stopped_at: '2026-09-30T00:00:00Z' })) })
  await agents.loadHistory()
  expect(agents.sessions[0]?.row_version).toBe(2)

  release()
  await ticket
  expect(agents.sessions.map(s => [s.id, s.row_version, s.phase])).toEqual([['s1', 2, 'stopped']])
})

it('a removal result that arrives after the undo that followed it does not bring the removal back', async () => {
  const agents = useAgents()
  await agents.refreshSessions()
  commit(() => { sessions = [session({ row_version: 3 })] })
  // The undo's answer, then the answer to the earlier removal arrives late: a write result names no position.
  const removalStarted = tick()
  agents.recordSession(wrapRow(session({ row_version: 3 }), undefined, tick()))
  agents.recordSession(wrapRow(session({ archived_at: '2026-09-30T00:00:00Z', row_version: 2 }), undefined, removalStarted))
  expect(agents.removedViews).toHaveLength(0)
  expect(agents.views.map(v => v.session.row_version)).toEqual([3])
  await settle()
  expect(agents.views.map(v => v.session.row_version)).toEqual([3])
})

// Review of AEON-449 round 2, P1: a mutation response carried a watermark read after its
// commit, newer than its own body. A capacity override that answered `queued` from event
// 10 got position 12 once a daemon claimed the run, and overwrote a `running` read at
// position 12 through the request-order tie-break, because it started after that read.
it('a capacity override answered after a daemon claimed the run does not put the run back to queued', async () => {
  runs = [run('queued', 2)]
  const agents = useAgents()
  await agents.refreshAgentRuns('agent')
  // The list read starts first, and the server processes it late.
  late = /runs\?limit=200/
  const listing = agents.loadAll()
  await vi.waitFor(() => expect(held).toHaveLength(1))
  // The override starts after it and is processed at once: version 3 (queued, override set) at position 11.
  holdWrite = /capacity-override/
  const override = runNowOnce('r1')
  await vi.waitFor(() => expect(held).toHaveLength(2))
  // A daemon claims the run: version 4 at position 12. The list is processed now and sees it.
  commit(() => { runs = [run('running', 4)] })
  release()
  await listing
  expect(agents.runs.r1?.status).toBe('running')
  // The override's answer arrives last, with the watermark 12: newer than its own body.
  agents.admitRun(await override)
  expect(agents.runs.r1?.status).toBe('running')
  expect(agents.runs.r1?.row_version).toBe(4)
})

// Review of AEON-449 round 2, P1: the launch result was returned unstamped and a
// delayed answer rewound a running run to queued.
it('a launch answered after a newer read of the run does not rewind it', async () => {
  runs = []
  const agents = useAgents()
  late = /runs\?.*agent=/
  const listing = agents.refreshAgentRuns('agent')
  await vi.waitFor(() => expect(held).toHaveLength(1))
  holdWrite = /work-orders\/wo1\/runs/
  const launch = createRun('wo1', { agent_principal_id: 'agent', model_profile_id: 'm1' })
  await vi.waitFor(() => expect(held).toHaveLength(2))
  // The daemon claims the launched run; the read that started before the launch is processed now.
  commit(() => { runs = runs.map(r => changed({ ...r, status: 'running' as const }, {})) })
  release()
  await listing
  expect(agents.runs.launched?.status).toBe('running')
  expect(agents.admitRun(await launch).status).toBe('running')
  expect(agents.runs.launched?.status).toBe('running')
  expect(agents.recentRuns('agent').map(r => r.status)).toEqual(['running'])
})

it('a launch that nothing has read yet is the first row of its run, and a later read replaces it', async () => {
  runs = []
  const agents = useAgents()
  const first = agents.admitRun(await createRun('wo1', { agent_principal_id: 'agent', model_profile_id: 'm1' }))
  expect(first.status).toBe('queued')
  expect(agents.runs.launched?.status).toBe('queued')
  commit(() => { runs = runs.map(r => changed({ ...r, status: 'running' as const }, {})) })
  await agents.refreshAgentRuns('agent')
  expect(agents.runs.launched?.status).toBe('running')
})

// Review of AEON-449 round 2, P2: the dialog kept a raw copy of the run it read and showed
// it even when the ledger had refused it.
it('a run read on its own and answered after a newer read is refused and the page keeps the newer row', async () => {
  runs = [run('queued', 2)]
  const agents = useAgents()
  await agents.refreshAgentRuns('agent')
  hold = /\/runs\/r1$/
  const single = getRun('r1')
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  commit(() => { runs = [run('running', 3)] })
  await agents.loadAll()
  release()
  // The standing row the dialog shows is the ledger's, not the answer it asked for.
  expect(agents.admitRun(await single).status).toBe('running')
  expect(agents.runs.r1?.status).toBe('running')
})

it('the sessions of an agent read by a dialog and answered late do not rewind a session or join the list', async () => {
  sessions = [session({ run_id: 'r1', management_mode: 'managed' })]
  const agents = useAgents()
  await agents.refreshSessions()
  hold = /harness-sessions\?.*agent=/
  const perAgent = listAllSessions({ agent: 'agent', limit: 200 })
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  commit(() => { sessions = [changed(sessions[0], { phase: 'stopped' as const, stopped_at: '2026-09-30T00:00:00Z' })] })
  await agents.refreshSessions()
  release()
  const standing = agents.admitSessions((await perAgent).items)
  expect(standing.map(s => [s.phase, s.row_version])).toEqual([['stopped', 2]])
  expect(agents.sessionById('s1')?.phase).toBe('stopped')
  expect(agents.sessions.map(s => s.phase)).toEqual(['stopped'])
})

// Review of AEON-449 round 2, P2: the panel stored an unstamped detail and preferred it over the
// current session, so a detail held across a newer list row showed the older one.
it('a session detail answered after a newer list row does not replace it, and its history stays across list rows', async () => {
  const agents = useAgents()
  await agents.refreshSessions()
  hold = /harness-sessions\/s1$/
  const detail = agents.loadSessionDetail(PROJECT, 's1')
  await vi.waitFor(() => expect(held).toHaveLength(1))
  hold = null
  commit(() => { sessions = [changed(sessions[0], { phase: 'yielded' as const })] })
  await agents.refreshSessions()
  expect(agents.sessionById('s1')?.phase).toBe('yielded')
  release()
  await detail
  expect(agents.sessionById('s1')?.phase).toBe('yielded')
  expect(agents.views[0]?.session.row_version).toBe(2)

  // A detail that is current brings the history only it carries; the next list row keeps it.
  await agents.loadSessionDetail(PROJECT, 's1')
  expect(agents.views[0]?.session.metadata_history).toHaveLength(1)
  commit(() => { sessions = [changed(sessions[0], { activity: 'idle' as const })] })
  await agents.refreshSessions()
  expect(agents.views[0]?.session.row_version).toBe(3)
  expect(agents.views[0]?.session.metadata_history).toHaveLength(1)
})

it('the answer to a removal keeps the summaries of the list row it replaces', async () => {
  sessions = [session({ project: { id: PROJECT, key: 'P', title: 'Project' }, agent: { id: 'agent', name: 'camy' } })]
  const agents = useAgents()
  await agents.refreshSessions()
  const removed = agents.recordSession((await removeSession({ id: 's1', project_id: PROJECT }, 'Clean up')).session)
  expect(removed.archived_at).toBe('2026-09-30T00:00:00Z')
  expect(removed.project?.title).toBe('Project')
  expect(removed.agent?.name).toBe('camy')
  expect(agents.removedViews.map(v => v.session.id)).toEqual(['s1'])
})
