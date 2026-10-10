// SPDX-License-Identifier: AGPL-3.0-only
// AEON-741: the project lead store against the real lead reads. Writes outrank
// earlier reads, totals span generations and trimming, the lead keeps asking
// after its session stops, and unread questions are never silently dropped.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { effectScope, ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import type { LeadDecision, ProjectLead } from '../src/lib/lead'

type Handler = (path: string, init?: RequestInit) => unknown | Promise<unknown>
const http = vi.hoisted(() => ({ handler: (() => ({})) as Handler, paths: [] as string[], admitted: [] as unknown[] }))
vi.mock('../src/lib/api', () => {
  class APIError extends Error { constructor(readonly status: number, message: string, readonly body: Record<string, unknown> = {}) { super(message) } }
  const api = async (path: string, init?: RequestInit) => {
    http.paths.push(`${init?.method ?? 'GET'} ${path}`)
    return new Response(JSON.stringify(await http.handler(path, init)), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  return { api, APIError, getNode: async (id: string) => ({ key: id.toUpperCase(), title: id }) }
})
vi.mock('../src/lib/agentRows', async importOriginal => ({ ...await importOriginal<object>(), getSession: async (project: string, session: string) => {
  http.paths.push(`GET /projects/${project}/harness-sessions/${session}`)
  return http.handler(`/projects/${project}/harness-sessions/${session}`)
} }))
vi.mock('../src/stores/agents', () => ({ useAgents: () => ({ admitSessions: (rows: unknown[]) => { http.admitted.push(...rows); return rows } }) }))
vi.mock('../src/lib/authz', () => ({ onAccessChange: () => () => {}, can: () => true }))
vi.mock('../src/stores/liveAgents', () => ({ useLiveAgents: () => ({ items: [], watch: () => () => {} }) }))
vi.mock('../src/stores/workQueue', () => ({ useWorkQueue: () => ({ snapshots: {} }) }))
import { useProjectLeads } from '../src/stores/projectLeads'
import { useLeadSummary } from '../src/lib/useLeadSummary'
import { resetPositions } from '../src/lib/position'

const NOW = '2026-10-06T12:00:00Z'
const P = 'p1', S1 = 's1', S2 = 's2', AGENT = 'agent-lead'
const lead = (patch: Partial<ProjectLead> = {}): ProjectLead => ({ project_id: P, revision: 4, generation: 2, session_id: S1, state: 'working', reason: '', process_active: true, ...patch })
const decision = (id: number, stage: LeadDecision['request']['stage'], outcome: LeadDecision['outcome'], ticket: string, session = S1, at = NOW): LeadDecision => ({
  event_id: id, project_id: P, session_id: session, recorded_at: at, outcome, reason_codes: [], gate_freshness: [], results: [],
  request: { stage, outcome, reason_codes: ['gates_ready'], attempt: 1, ticket_node_id: ticket },
})
const question = (id: string, asker = AGENT) => ({ id, project_id: P, state: 'open', input: { question: id, options: [] }, askers: [{ principal_id: asker }] })
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
const settle = () => new Promise(resolve => setTimeout(resolve, 0))
function routes(over: Partial<Record<'lead' | 'pause' | 'decisions' | 'questions' | 'session', Handler>> = {}): Handler {
  return (path, init) => {
    const url = new URL(path, 'http://x')
    if (url.pathname === `/projects/${P}/lead/pause`) return over.pause?.(path, init) ?? lead({ state: 'paused', revision: 5 })
    if (url.pathname === `/projects/${P}/lead`) return over.lead?.(path, init) ?? lead()
    if (url.pathname === `/projects/${P}/lead-settings`) return { revision: 1, details_redacted: false, automatic_launch_enabled: false, owner_person_id: null }
    if (url.pathname === `/projects/${P}/lead-decisions`) return over.decisions?.(path, init) ?? { items: [], next_after: null }
    if (url.pathname === `/projects/${P}/questions`) return over.questions?.(path, init) ?? { items: [], has_more: false }
    if (url.pathname.startsWith(`/projects/${P}/harness-sessions/`)) return over.session?.(path, init) ?? { agent_principal_id: AGENT }
    throw new Error(`unexpected ${path}`)
  }
}
function summary() {
  const scope = effectScope()
  const result = scope.run(() => useLeadSummary(ref(P), ref('PHAROS')))!
  return { ...result, stop: () => scope.stop() }
}
beforeEach(() => { vi.useFakeTimers({ toFake: ['Date'] }); vi.setSystemTime(new Date(NOW)); setActivePinia(createPinia()); http.paths.length = 0; http.admitted.length = 0; http.handler = routes(); vi.spyOn(console, 'warn').mockImplementation(() => {}) })
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

it('AEON-1038: launch-off or unreadable policy refuses Start without a POST', async () => {
  const leads = useProjectLeads()
  for (const automatic_launch_enabled of [false, undefined]) {
    http.handler = routes({ lead: () => lead({ state: 'none', session_id: null, automatic_launch_enabled }) })
    await leads.loadLead(P)
    await expect(leads.start(P)).rejects.toThrow(automatic_launch_enabled === false ? /not enabled/ : /could not be read/)
  }
  expect(http.paths.filter(path => path.startsWith('POST '))).toEqual([])
})

it('a read that began before Pause cannot put the older lead back, and a fresh read follows', async () => {
  const leads = useProjectLeads()
  await leads.loadLead(P)
  const older = deferred<ProjectLead>()
  let reads = 0
  http.handler = routes({ lead: () => ++reads === 1 ? older.promise : lead({ state: 'paused', revision: 5 }) })
  const reading = leads.loadLead(P)
  expect(await leads.pause(P)).toMatchObject({ state: 'paused', revision: 5 })
  older.resolve(lead({ state: 'working', revision: 4 }))
  await reading; await settle()
  expect(leads.view(P).lead).toMatchObject({ state: 'paused', revision: 5 })
  expect(reads).toBe(2)
})

it('a newer read never yields to an older revision answered later', async () => {
  const leads = useProjectLeads()
  http.handler = routes({ lead: () => lead({ state: 'paused', revision: 6 }) })
  await leads.loadLead(P)
  http.handler = routes({ lead: () => lead({ state: 'working', revision: 5 }) })
  await leads.loadLead(P)
  expect(leads.view(P).lead).toMatchObject({ state: 'paused', revision: 6 })
})

it('AEON-1040: removal sends the confirmed revision and older reads cannot resurrect the lead', async () => {
  const leads = useProjectLeads()
  const unstarted = lead({ state: 'paused', session_id: null, generation: 0, process_active: false })
  const none = lead({ state: 'none', revision: 0, session_id: null, generation: 0, process_active: false })
  http.handler = routes({ lead: () => unstarted })
  await leads.loadLead(P)
  await expect(leads.remove(P, 3)).rejects.toThrow('The lead changed')
  expect(http.paths.filter(path => path.startsWith('DELETE '))).toEqual([])
  const older = deferred<ProjectLead>()
  let reads = 0
  http.handler = routes({ lead: (_path, init) => {
    if (init?.method === 'DELETE') {
      expect(JSON.parse(String(init.body))).toEqual({ expected_revision: 4 })
      return none
    }
    return ++reads === 1 ? older.promise : none
  } })
  const reading = leads.loadLead(P)
  expect(await leads.remove(P, 4)).toMatchObject({ state: 'none', revision: 0 })
  older.resolve(unstarted)
  await reading; await settle()
  expect(leads.view(P).lead).toMatchObject({ state: 'none', revision: 0 })
  expect(http.paths.filter(path => path.startsWith('DELETE '))).toEqual([`DELETE /projects/${P}/lead`])
})

it('AEON-1040: a fresh external removal clears the card and outranks an older overlapping read', async () => {
  const leads = useProjectLeads()
  await leads.loadLead(P)
  const older = deferred<ProjectLead>()
  http.handler = routes({ lead: () => older.promise })
  const reading = leads.loadLead(P)
  http.handler = routes({ lead: () => lead({ state: 'none', revision: 0, session_id: null, generation: 0, process_active: false }) })
  await leads.loadLead(P)
  older.resolve(lead())
  await reading
  expect(leads.view(P).lead).toMatchObject({ state: 'none', revision: 0 })
})

it('gate and merged totals cover every generation and survive trimming the retained history', async () => {
  const leads = useProjectLeads()
  const history = [decision(1, 'review', 'requested', 'n-gate'), decision(2, 'release_handoff', 'handoff', 'n-merged'),
    ...Array.from({ length: 2100 }, (_, i) => decision(10 + i, 'queue', 'selected', `n-q${i}`))]
  http.handler = routes({ decisions: path => {
    const url = new URL(path, 'http://x'), after = Number(url.searchParams.get('after'))
    expect(url.searchParams.has('session_id')).toBe(false)
    const page = history.filter(d => d.event_id > after).slice(0, 200)
    return { items: page, next_after: page.length === 200 ? page.at(-1)!.event_id : null }
  } })
  const lines = summary()
  await leads.load(P); await leads.load(P); await leads.load(P)
  expect(leads.view(P).caughtUp).toBe(true)
  expect(leads.view(P).decisions.length).toBeLessThan(history.length)
  expect(lines.gateIds.value).toEqual(['n-gate']); expect(lines.mergedIds.value).toEqual(['n-merged'])
  // The lead restarts as a new generation: its history continues, nothing resets.
  history.push(decision(5000, 'admission', 'selected', 'n-new', S2))
  http.handler = routes({ lead: () => lead({ session_id: S2, generation: 3, revision: 8 }), decisions: path => {
    const after = Number(new URL(path, 'http://x').searchParams.get('after'))
    return { items: history.filter(d => d.event_id > after), next_after: null }
  } })
  await leads.load(P)
  expect(leads.view(P).decisions.at(-1)?.event_id).toBe(5000)
  expect(lines.gateIds.value).toEqual(['n-gate']); expect(lines.mergedIds.value).toEqual(['n-merged'])
  lines.stop()
})

it('an unanswered question stays with the lead after its session stopped', async () => {
  const leads = useProjectLeads()
  http.handler = routes({ lead: () => lead({ state: 'paused', process_active: false, revision: 6 }), questions: () => ({ items: [question('q-lead'), question('q-other', 'someone')], has_more: false }) })
  const lines = summary()
  await leads.load(P)
  expect(lines.leadSession.value).toBeNull()
  expect(lines.questions.value.map(q => q.id)).toEqual(['q-lead'])
  lines.stop()
})

it('open questions are paged within a bound and a remainder is reported, never hidden', async () => {
  const leads = useProjectLeads()
  const offsets: number[] = []
  http.handler = routes({ questions: path => {
    const offset = Number(new URL(path, 'http://x').searchParams.get('offset')); offsets.push(offset)
    return { items: Array.from({ length: 100 }, (_, i) => question(`q-${offset + i}`, offset === 300 && i === 7 ? AGENT : 'someone')), has_more: true }
  } })
  const lines = summary()
  await leads.load(P)
  expect(offsets).toEqual([0, 100, 200, 300, 400])
  expect(lines.questions.value.map(q => q.id)).toEqual(['q-307'])
  expect(lines.questionsPartial.value).toBe(true)
  lines.stop()
})

it('a session read answered after a reset is not admitted for the next person', async () => {
  const leads = useProjectLeads()
  const session = deferred<unknown>()
  http.handler = routes({ session: () => session.promise })
  const loading = leads.load(P)
  await vi.waitFor(() => expect(http.paths).toContain(`GET /projects/${P}/harness-sessions/${S1}`))
  resetPositions()
  session.resolve({ agent_principal_id: AGENT })
  await loading; await settle()
  expect(http.admitted).toEqual([])
  expect(leads.view(P).principal).toBeNull()
})

it('a failed later question page keeps the questions read and says the rest are unread', async () => {
  const leads = useProjectLeads()
  http.handler = routes({ questions: path => {
    const offset = Number(new URL(path, 'http://x').searchParams.get('offset'))
    if (offset > 0) throw new Error('page failed')
    return { items: [question('q-lead'), ...Array.from({ length: 99 }, (_, i) => question(`q-${i}`, 'someone'))], has_more: true }
  } })
  const lines = summary()
  await leads.load(P)
  expect(lines.questions.value.map(q => q.id)).toEqual(['q-lead'])
  expect(lines.questionsPartial.value).toBe(true)
  lines.stop()
})

it('a failed question read keeps the last questions shown and reports them incomplete', async () => {
  const leads = useProjectLeads()
  http.handler = routes({ questions: () => ({ items: [question('q-lead')], has_more: false }) })
  const lines = summary()
  await leads.load(P)
  expect(lines.questionsPartial.value).toBe(false)
  http.handler = routes({ questions: () => { throw new Error('read failed') } })
  await leads.load(P)
  expect(lines.questions.value.map(q => q.id)).toEqual(['q-lead'])
  expect(lines.questionsPartial.value).toBe(true)
  lines.stop()
})
