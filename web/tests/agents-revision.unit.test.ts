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
const held: (() => void)[] = []

const session = (fields: Partial<HarnessSession>) => ({
  id: 's1', project_id: PROJECT, agent_principal_id: 'agent', harness: 'codex', management_mode: 'unmanaged', role: 'worker', phase: 'working', activity: 'busy',
  revision: 1, activity_sequence: 1, advertised_capabilities: [], heartbeat_at: new Date().toISOString(), created_at: '2026-01-01T00:00:00Z', stopped_at: null, run_id: null, ticket_node_id: null,
  work_order_id: null, parent_harness_session_id: null, stop_reason: null, host: 'h', work_shape: 'unknown', ...fields,
}) as HarnessSession
const run = (status: AgentRun['status']) => ({ id: 'r1', agent_principal_id: 'agent', status }) as AgentRun
const message = (fields: Partial<ProjectMessage> = {}) => ({ id: 'm1', sent_event_id: 1, is_action_request: true, status: 'held', ...fields }) as ProjectMessage

function respond(body: unknown, at: number, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', [POSITION_HEADER]: String(at) } })
}
// Every change the server accepts appends one event.
const commit = (change: () => void) => { change(); position++ }

async function serve(input: string, init: RequestInit = {}): Promise<Response> {
  const url = new URL(input, 'http://aeon.test')
  const path = url.pathname.replace(/^\/api/, '')
  const method = init.method ?? 'GET'
  requests.push(`${method} ${path}${url.search}`)
  if (method === 'GET') {
    const at = position
    const snapshot = structuredClone(read(path, url.searchParams))
    const answer = () => respond(snapshot, at)
    if (hold?.test(`${path}${url.search}`)) return new Promise(resolve => { held.push(() => resolve(answer())) })
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
    const items = query.get('view') === 'all' ? sessions : sessions.filter(s => !s.archived_at)
    return { items, next_cursor: null }
  }
  if (path === '/runs') return { items: query.get('agent') && query.get('agent') !== 'agent' ? [] : runs, next_cursor: null }
  if (path === `/projects/${PROJECT}/messages`) return { items: messages, next_after: 0 }
  if (path.endsWith('/message-targets')) return []
  if (path === '/approvals' || path === '/agent-accounts' || path === '/models') return []
  return {}
}
const release = () => { hold = null; for (const go of held.splice(0)) go() }
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
