// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { draftFor, outcomeUnavailable, type DeskProjection } from '../src/lib/decisionDesk'
import { commitDesk, emptySources, loadDesk, nativeTierAdapter, questionItem, type Question } from '../src/lib/decisionDeskApi'
import type { HarnessSession } from '../src/lib/agents'

const question = (): Question => ({ id: 'question', project_id: 'project', revision: 1, state: 'open', suggested_outcome: 'always', suggestion_reason: 'project_default',
  input: { request_id: 'ask', question: 'Which storage?', meanwhile: 'parked', options: [{ id: 'local', title: 'Local', description: '', answer: 'Use local storage.' }],
    ticket_id: 'ticket', doctrine: { source_id: 'source', path: 'docs/rules', rule_key: 'storage', rule_sha256: 'a'.repeat(64) } },
  outcomes: ['once', 'always', 'requirement', 'doctrine'].map(outcome => ({ outcome, available: true, why: '', ...(outcome === 'doctrine' ? { mapping_present: true } : {}) })) as Question['outcomes'],
  askers: [], pending: [], created_at: '', updated_at: '' })
afterEach(() => vi.unstubAllGlobals())

it('integrates the server default and each publishing outcome, then corrects the exact revision with its doctrine mapping', async () => {
  const q = question(), sources = emptySources()
  sources.questions.set('q:question', q)
  const writes: { path: string; body: Record<string, unknown> }[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init: RequestInit) => {
    const body = JSON.parse(init.body as string); writes.push({ path, body })
    const current = sources.questions.get('q:question')!
    return new Response(JSON.stringify({ ...current, revision: current.revision + 1, state: 'answered', answer: { id: `answer-${current.revision + 1}`, revision: current.revision + 1, answer: 'Use local storage.', option_id: 'local', outcome: body.outcome, decided_by: 'person', created_at: '', deliver_after: '' } }))
  }))
  let item = questionItem(q, 'Aeon')
  expect(draftFor(item).outcome).toBe('always')
  for (const outcome of ['always', 'requirement', 'doctrine', 'once'] as const) {
    expect(outcomeUnavailable(item, outcome)).toBe('')
    item = await commitDesk(item, { ...draftFor(item), outcome, dirty: true }, sources, `operation-${outcome}`, false)
    expect(item.outcome).toBe(outcome)
  }
  expect(writes.map(write => write.path)).toEqual(Array(4).fill('/api/questions/question/decision'))
  expect(writes.map(write => write.body.expected_revision)).toEqual([1, 2, 3, 4])
  expect(writes[2]!.body.doctrine).toEqual(q.input.doctrine)
})
it('uses server permission reasons and refuses doctrine without a confirmed mapping', () => {
  const q = question()
  q.outcomes!.find(row => row.outcome === 'always')!.available = false
  q.outcomes!.find(row => row.outcome === 'always')!.why = 'This outcome requires knowledge.write permission.'
  const item = questionItem(q, 'Aeon')
  expect(outcomeUnavailable(item, 'always')).toBe('This outcome requires knowledge.write permission.')
  expect(outcomeUnavailable(questionItem({ ...q, input: { ...q.input, doctrine: undefined } }, 'Aeon'), 'doctrine')).toContain('mapping')
})
it('retains outcome failure, retryability, exact effect references and required review independently of a handed-off answer', () => {
  const q = question(); q.revision = 3; q.state = 'answered'
  q.answer = { id: 'answer', revision: 3, outcome: 'requirement', answer: 'Use local storage.', decided_by: 'person', created_at: '', deliver_after: '', replaces: 'previous-answer' }
  q.pending = [
    { id: 'old', revision: 2, kind: 'outcome', state: 'delivered', deliver_after: '', effect_ref: 'old-effect' },
    { id: 'inbox', revision: 3, kind: 'inbox', state: 'delivered', receipt_state: 'handed_off', deliver_after: '' },
    { id: 'effect', revision: 3, kind: 'outcome', state: 'failed', deliver_after: '', effect_ref: 'criterion/ticket/answer', error_code: 'ticket_revision_conflict', error_message: 'The ticket changed during grace; no criterion was changed.', effect_data: { retryable: false, ticket_id: 'ticket', knowledge_id: 'knowledge', doctrine_id: 'doctrine', review_required: [{ kind: 'criterion', ref: 'ticket', why: 'Earlier criterion was edited; person review is required.' }] }, doctrine_state: 'in_review' },
  ]
  const item = questionItem(q, 'Aeon')
  expect(item.delivery).toContain('Handed to the agent.')
  expect(item.outcomeEffects).toHaveLength(1)
  expect(item.outcomeEffects![0]).toMatchObject({ id: 'effect', state: 'failed', error_code: 'ticket_revision_conflict', error_message: q.pending[2]!.error_message, effect_ref: 'criterion/ticket/answer', effect_data: q.pending[2]!.effect_data, doctrine_state: 'in_review' })
  // Successful outcomes retain provenance too, including the retryable failure
  // the worker will retry automatically; neither can be called delivered yet.
  q.pending[2]!.effect_data!.retryable = true
  expect(questionItem(q, 'Aeon').outcomeEffects![0]!.effect_data!.retryable).toBe(true)
  q.pending[2]!.state = 'delivered'
  expect(questionItem(q, 'Aeon').outcomeEffects![0]!.state).toBe('delivered')
})

const session = (id: string): HarnessSession => ({ id, project_id: 'project', advertised_capabilities: ['service_tier_v1'], process_ownership: { daemon_id: 'daemon', generation: 'generation', process_id: `process-${id}`, root_pid: 123, group_id: 123, started_at: '2026-10-03T10:00:00Z' } }) as HarnessSession
const projection = (id: string, sessionId: string): DeskProjection => ({ items: [{ id, kind: 'tier_request', project_id: 'project', title: 'Tier request', revision: 7, created_at: '', held: false, can_decide: true, href: `/decision-desk?item=t:${id}`, source: `/api/projects/project/harness-sessions/${sessionId}/tier` }], counts: { open: 1, held: 0, chores: 0 }, has_more: false, as_of: '' })
function tierFetch(pendingSession: string, pendingID: string) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const match = url.match(/harness-sessions\/([^/]+)\/tier/)
    if (!match) return new Response(JSON.stringify(url.startsWith('/api/approvals?') ? [] : { items: [], has_more: false, pending: 0 }))
    const sessionID = match[1]!
    const requests = sessionID === pendingSession ? [{ id: pendingID, session_id: sessionID, tier: 'fast', reason: 'Unblock this run', state: init?.method === 'POST' ? 'approved' : 'pending', created_at: '' }]
      : Array.from({ length: 50 }, (_, n) => ({ id: `${sessionID}-history-${n}`, session_id: sessionID, tier: 'fast', reason: 'History', state: 'declined', created_at: '' }))
    return new Response(JSON.stringify({ session_id: sessionID, revision: 7, read_only: false, requests }))
  })
}
it('hydrates pending tier requests independently of two full sessions of history and can decide their exact native request', async () => {
  const sessions = [session('history-1'), session('history-2'), session('pending')]
  const fetch = tierFetch('pending', 'request'); vi.stubGlobal('fetch', fetch)
  const adapter = nativeTierAdapter(async () => sessions, () => true)
  const result = await loadDesk(undefined, { tiers: adapter }, projection('request', 'pending'))
  const item = result.items.find(item => item.id === 't:request')!
  expect(item.unavailable).toBeUndefined()
  expect(item.context).toBe('Unblock this run')
  const answer = await commitDesk(item, { ...draftFor(item), optionId: 'approve' }, result.sources, 'operation', false, { tiers: adapter })
  expect(answer.decided).toBe(true)
  const write = fetch.mock.calls.find(([, init]) => init?.method === 'POST')!
  expect(write[0]).toBe('/api/projects/project/harness-sessions/pending/tier/requests/request/decision')
  expect(JSON.parse(write[1]!.body as string)).toMatchObject({ expected_revision: 7, expected_ownership: sessions[2]!.process_ownership })
})
it('hydrates a canonical direct-linked tier session beyond the bulk session cap through the admitted detail read', async () => {
  const sessions = Array.from({ length: 31 }, (_, n) => session(`session-${n}`)), fetch = tierFetch('session-30', 'linked-request')
  vi.stubGlobal('fetch', fetch)
  const detail = vi.fn(async (project: string, id: string) => { expect(project).toBe('project'); return sessions.find(row => row.id === id)! })
  const adapter = nativeTierAdapter(async () => sessions.slice(0, 30), () => true, detail)
  const result = await loadDesk(undefined, { tiers: adapter }, projection('linked-request', 'session-30'))
  expect(detail).toHaveBeenCalledWith('project', 'session-30')
  expect(result.items.find(item => item.id === 't:linked-request')).toMatchObject({ context: 'Unblock this run', revision: 7 })
  expect(result.items.find(item => item.id === 't:linked-request')!.unavailable).toBeUndefined()
})

it('bounds streamed tier response bytes before decode and leaves the source explicitly unavailable', async () => {
  let cancelled = false
  vi.stubGlobal('fetch', vi.fn(async () => new Response(new ReadableStream({
    start(controller) { controller.enqueue(new Uint8Array(2 * 1024 * 1024 + 1)); controller.enqueue(new Uint8Array([0])); controller.close() },
    cancel() { cancelled = true },
  }))))
  const adapter = nativeTierAdapter(async () => [session('pending')], () => true)
  const read = await adapter.read(projection('request', 'pending').items)
  expect(cancelled).toBe(true)
  expect(read.items).toEqual([])
  expect(read.warnings).toContain('Some tier requests could not be read. Open Agents to inspect their source.')
})
