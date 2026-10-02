// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { answerFor, arrivals, CUSTOM_ANSWER, draftFor, macPlatform, newRound, outcomeUnavailable, roundCounts, submitModifier, type DeskDraft } from '../src/lib/decisionDesk'
import { commitDesk, emptySources, loadDesk, loadMoreQuestions, readQuestions, questionItem, approvalItem, actionItem, nativeTierAdapter, ruleItem, type Question } from '../src/lib/decisionDeskApi'

const question = (): Question => ({ id: 'q1', project_id: 'project', revision: 1, state: 'open', suggested_outcome: 'once', suggestion_reason: 'ticket_default',
  input: { request_id: 'request', question: 'Which index?', options: [{ id: 'a', title: 'Partial', description: 'Smaller', answer: 'Use the partial index.' }], meanwhile: 'parked' }, askers: [], pending: [], created_at: '', updated_at: '' })
afterEach(() => vi.unstubAllGlobals())
it('keeps Open offsets and answered cursors in separate request shapes', async () => {
  const fetch = vi.fn(async (_path: string) => Response.json({ items: [], has_more: false }))
  vi.stubGlobal('fetch', fetch)
  await readQuestions({ state: 'open', offset: 200 })
  await readQuestions({ state: 'answered', cursor: 'page/two+three' })
  expect(fetch.mock.calls.map(call => new URL(String(call[0]), 'https://test.invalid').search)).toEqual([
    '?state=open&limit=100&offset=200', '?state=answered&order=desc&limit=100&cursor=page%2Ftwo%2Bthree',
  ])
})
it('appends one answered page, dedupes corrections and refreshes from newest', async () => {
  const history = Array.from({ length: 205 }, (_, i) => ({ ...question(), id: `history-${i}`, state: 'answered' as const }))
  const reads: URL[] = []
  let newest = history
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    const url = new URL(path, 'https://test.invalid'); reads.push(url)
    if (url.pathname === '/api/decision-desk') {
      if (url.searchParams.get('state') === 'open') return Response.json({ items: [question()], has_more: false })
      const start = Number(url.searchParams.get('cursor') ?? 0), end = start + 100
      return Response.json({ items: newest.slice(start, end), has_more: end < newest.length, ...(end < newest.length ? { next_cursor: String(end) } : {}) })
    }
    return Response.json(url.pathname === '/api/approvals' ? [] : { items: [] })
  }))
  const first = await loadDesk()
  reads.length = 0
  const second = await loadMoreQuestions(first, 'answered')
  expect(reads.map(url => url.searchParams.get('cursor'))).toEqual(['100'])
  expect(second.items.filter(item => item.decided)).toHaveLength(200)
  expect(first.items.filter(item => item.decided)).toHaveLength(100)
  expect(first.sources.questions.size).toBe(101)
  // An overlapping revision replaces its previous projection without duplicating it.
  newest = [...history.slice(0, 200), { ...history[0]!, revision: 3 }, ...history.slice(200)]
  const third = await loadMoreQuestions(second, 'answered')
  expect(third.items.filter(item => item.decided)).toHaveLength(205)
  expect(third.sources.questions.get('q:history-0')!.revision).toBe(3)
  expect(third.hasMore.answered).toBe(false)
  const count = reads.length
  expect(await loadMoreQuestions(third, 'answered')).toBe(third)
  expect(reads).toHaveLength(count)
  // A fresh decision above the retained cursor is picked up by a full refresh.
  newest = [{ ...question(), id: 'fresh', state: 'answered' }, ...history]
  reads.length = 0
  const refreshed = await loadDesk({ open: 1, answered: 3 })
  expect(refreshed.items.find(item => item.decided)!.id).toBe('q:fresh')
  expect(reads.filter(url => url.searchParams.get('state') === 'answered').map(url => url.searchParams.get('cursor'))).toEqual([null, '100', '200'])
})
it('continues Open offsets without rereading answered or protected sources', async () => {
  const reads: URL[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    const url = new URL(path, 'https://test.invalid'); reads.push(url)
    if (url.pathname === '/api/decision-desk') return Response.json({ items: [{ ...question(), id: url.searchParams.get('offset') ?? 'answered' }], has_more: url.searchParams.get('state') === 'open' })
    return Response.json(url.pathname === '/api/approvals' ? [] : { items: [] })
  }))
  let read = await loadDesk()
  reads.length = 0
  for (let i = 1; i < 10; i++) read = await loadMoreQuestions(read, 'open')
  expect(reads.map(url => url.searchParams.get('offset'))).toEqual(['100', '200', '300', '400', '500', '600', '700', '800', '900'])
  expect(read.hasMore.open).toBe(true)
  expect(read.questionPositions.open!.pages).toBe(10)
  expect(await loadMoreQuestions(read, 'open')).toBe(read)
  expect(reads).toHaveLength(9)
})
it('rejects any cursor cycle and leaves the previous page available for retry', async () => {
  let cursor: string | undefined = 'two'
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    const url = new URL(path, 'https://test.invalid')
    if (url.pathname === '/api/decision-desk') return Response.json({ items: [question()], has_more: url.searchParams.get('state') === 'answered', next_cursor: cursor })
    return Response.json(url.pathname === '/api/approvals' ? [] : { items: [] })
  }))
  const first = await loadDesk()
  cursor = 'three'
  const second = await loadMoreQuestions(first, 'answered')
  for (const invalid of ['two', 'three', 'x'.repeat(513), undefined]) {
    cursor = invalid
    await expect(loadMoreQuestions(second, 'answered')).rejects.toThrow('cursor is unavailable')
    expect(second.questionPositions.answered!.pages).toBe(2)
    expect(second.questionPositions.answered!.nextCursor).toBe('three')
  }
  cursor = 'four'
  const third = await loadMoreQuestions(second, 'answered')
  expect(third.questionPositions.answered!.pages).toBe(3)
})
it('caps answered continuation at ten pages and rejects oversized pages before merging', async () => {
  let oversized = false, answeredReads = 0
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    const url = new URL(path, 'https://test.invalid')
    if (url.pathname === '/api/decision-desk') {
      if (url.searchParams.get('state') === 'open') return Response.json({ items: [], has_more: false })
      answeredReads++
      return Response.json({ items: Array.from({ length: oversized ? 101 : 100 }, (_, i) => ({ ...question(), id: `${answeredReads}-${i}`, state: 'answered' })), has_more: true, next_cursor: String(answeredReads) })
    }
    return Response.json(url.pathname === '/api/approvals' ? [] : { items: [] })
  }))
  let read = await loadDesk()
  oversized = true
  await expect(loadMoreQuestions(read, 'answered')).rejects.toThrow('exceeded its limit')
  expect(read.sources.questions.size).toBe(100)
  expect(read.questionPositions.answered!.pages).toBe(1)
  oversized = false
  for (let i = 1; i < 10; i++) read = await loadMoreQuestions(read, 'answered')
  expect(read.sources.questions.size).toBe(1000)
  expect(read.hasMore.answered).toBe(true)
  expect(await loadMoreQuestions(read, 'answered')).toBe(read)
  expect(answeredReads).toBe(11) // Ten accepted pages plus the rejected oversized response.
})
it('reads newest answered pages with bounded cursors while Open keeps its offset', async () => {
  const history = Array.from({ length: 205 }, (_, i) => ({ ...question(), id: `history-${205-i}`, state: 'answered' as const }))
  const reads: URL[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    const url = new URL(path, 'https://test.invalid'); reads.push(url)
    if (url.pathname === '/api/decision-desk') {
      if (url.searchParams.get('state') === 'open') return Response.json({ items: [question()], has_more: false })
      expect(url.searchParams.get('order')).toBe('desc')
      expect(url.searchParams.has('offset')).toBe(false)
      const cursor = url.searchParams.get('cursor'), start = cursor === 'page/two' ? 100 : cursor === 'page/three' ? 200 : 0
      return Response.json({ items: history.slice(start, start + 100), has_more: start < 200, ...(start < 200 ? { next_cursor: start === 0 ? 'page/two' : 'page/three' } : {}) })
    }
    return Response.json(url.pathname === '/api/approvals' ? [] : { items: [] })
  }))
  const result = await loadDesk({ open: 1, answered: 3 })
  expect(result.items.filter(item => item.decided).map(item => item.id)).toEqual(history.map(question => `q:${question.id}`))
  expect(result.sources.questions.size).toBe(206)
  expect(result.hasMore).toEqual({ open: false, answered: false })
  expect(reads.filter(url => url.searchParams.get('state') === 'answered')).toHaveLength(3)
  expect(reads.find(url => url.searchParams.get('state') === 'open')!.searchParams.get('offset')).toBe('0')
})
it('fails answered reads honestly when a required continuation cursor is missing', async () => {
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    const url = new URL(path, 'https://test.invalid')
    if (url.pathname === '/api/decision-desk') return Response.json({ items: [], has_more: url.searchParams.get('state') === 'answered' })
    return Response.json(url.pathname === '/api/approvals' ? [] : { items: [] })
  }))
  const result = await loadDesk({ open: 1, answered: 2 })
  expect(result.warnings).toContain('Questions could not be read. They may be inaccessible; this is not an empty desk.')
})
it('rejects oversized source pages before collecting question records', async () => {
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    const url = new URL(path, 'https://test.invalid')
    if (url.pathname === '/api/decision-desk') return Response.json({ items: Array.from({ length: 101 }, (_, i) => ({ ...question(), id: `q${i}` })), has_more: false })
    return Response.json(url.pathname === '/api/approvals' ? [] : { items: [] })
  }))
  const result = await loadDesk()
  expect(result.items).toEqual([])
  expect(result.sources.questions.size).toBe(0)
  expect(result.warnings).toContain('Questions could not be read. They may be inaccessible; this is not an empty desk.')
})
it('freezes round IDs while arrivals and decided/skipped counts remain distinct', () => {
  const one = questionItem(question(), 'Aeon'), two = { ...one, id: 'q:two' }, three = { ...one, id: 'q:new' }
  const round = newRound([one, two])
  expect(arrivals([three, two, one], round).map(item => item.id)).toEqual(['q:new'])
  expect(round).toEqual(['q:q1', 'q:two'])
  expect(roundCounts([{ ...one, decided: true }, two], round, new Set(['q:q1', 'q:two']))).toEqual({ decided: 1, skipped: 1, open: 0 })
})
it('preserves custom answers on correction and separates the P.S. from the answer', () => {
  const item = questionItem(question(), 'Aeon'), draft = { ...draftFor(item), optionId: CUSTOM_ANSWER, answer: '  Keep it tenant-scoped. ', reason: 'Separate note' }
  expect(answerFor(item, draft)).toBe('Keep it tenant-scoped.')
  const decided = { ...item, decided: true, optionId: CUSTOM_ANSWER, answer: 'Earlier answer', reason: 'Earlier reason' }
  expect(draftFor(decided)).toMatchObject({ optionId: CUSTOM_ANSWER, answer: 'Earlier answer', reason: 'Earlier reason', dirty: false })
})
it('leaves publishing and every protected action outside ordinary Always answers', () => {
  const item = questionItem(question(), 'Aeon')
  for (const outcome of ['always', 'requirement', 'doctrine'] as const) expect(outcomeUnavailable(item, outcome)).toContain('not available')
  for (const kind of ['approval', 'action', 'rule', 'tier'] as const) expect(outcomeUnavailable({ ...item, kind }, 'once')).toContain('protected')
})
it('reports dispatched and failed delivery honestly, retaining reuse provenance', () => {
  const q = question(); q.revision = 2; q.state = 'answered'
  q.pending = [{ id: 'p', revision: 2, kind: 'inbox', state: 'delivered', deliver_after: '' }]
  expect(questionItem(q, 'Aeon').delivery).toContain('receiver confirmation')
  q.pending[0]!.receipt_state = 'failed'
  expect(questionItem(q, 'Aeon').delivery).toContain('failed')
  q.askers = [{ id: 'asker', principal_id: 'agent', reply_root_id: 'root', comment_node_id: 'node', input: q.input, from_record: { label: 'From the record', decision_id: 'decision', revision: 3 } }]
  expect(questionItem(q, 'Aeon').fromRecord).toBe('From the record · revision 3')
})
it('uses only the platform submission modifier and leaves mixed/native combinations alone', () => {
  expect(macPlatform('MacIntel')).toBe(true); expect(macPlatform('Linux x86_64')).toBe(false)
  const base = { metaKey: true, ctrlKey: false, altKey: false, shiftKey: false }
  expect(submitModifier(base, true)).toBe(true); expect(submitModifier(base, false)).toBe(false)
  expect(submitModifier({ ...base, metaKey: false, ctrlKey: true }, false)).toBe(true)
  expect(submitModifier({ ...base, shiftKey: true }, true)).toBe(false)
})
it('captures question ID/revision in the write and propagates permission/expiry failures', async () => {
  const q = question(), sources = emptySources(), item = questionItem(q, 'Aeon'), draft = draftFor(item)
  sources.questions.set(item.id, q)
  const fetch = vi.fn(async (_url: string, init: RequestInit) => {
    expect(JSON.parse(init.body as string)).toMatchObject({ expected_revision: 1, request_id: 'operation', option_id: 'a', outcome: 'once' })
    return new Response(JSON.stringify({ error: 'Access revoked' }), { status: 403 })
  }); vi.stubGlobal('fetch', fetch)
  await expect(commitDesk(item, draft, sources, 'operation', false)).rejects.toThrow('Access revoked')
  expect(fetch.mock.calls[0]?.[0]).toBe('/api/questions/q1/decision')
  sources.questions.set(item.id, { ...q, revision: 2 })
  await expect(commitDesk(item, draft, sources, 'operation', false)).rejects.toThrow('changed')
  expect(fetch).toHaveBeenCalledTimes(1)
})
it('never submits a phone approval or a tier request through the question endpoint', async () => {
  const item = { ...questionItem(question(), 'Aeon'), id: 'a:approval', kind: 'approval' as const }, sources = emptySources()
  sources.approvals.set(item.id, { id: 'approval', agent_principal_id: 'agent', scope: 'nodes.read', resource_kind: 'tenant', rationale: '', proposed_at: '', expires_at: '', decision: null })
  const fetch = vi.fn(async () => new Response(JSON.stringify({ error: 'Unavailable' }), { status: 404 })); vi.stubGlobal('fetch', fetch)
  const draft: DeskDraft = { optionId: 'approved', answer: '', reason: '', outcome: 'once', dirty: true }
  await expect(commitDesk(item, draft, sources, 'operation', true)).rejects.toThrow('Phone verification')
  await expect(commitDesk({ ...item, kind: 'tier' }, draft, sources, 'operation', false)).rejects.toThrow('protected tier')
  expect(fetch).toHaveBeenCalledTimes(1)
  expect(fetch.mock.calls[0]?.[0]).toBe('/api/phone-approvals/approval/approval')
})
it('does not turn denied source reads into a claim that the desk is empty', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'Forbidden' }), { status: 403 })))
  const result = await loadDesk()
  expect(result.items).toEqual([])
  expect(result.warnings).toContain('Questions could not be read. They may be inaccessible; this is not an empty desk.')
})

it('does not preselect a grant, even when the protected source recommends it', () => {
  const item = { ...questionItem(question(), 'Aeon'), kind: 'approval' as const, recommended: 'a' }
  expect(draftFor(item).optionId).toBe('')
  expect(draftFor({ ...item, kind: 'tier' }).optionId).toBe('')
})
it('files expired undecided approvals in history', () => {
  const item = approvalItem({ id: 'a', agent_principal_id: 'agent', scope: 'nodes.read', resource_kind: 'node', rationale: '', expires_at: '2000-01-01T00:00:00Z', proposed_at: '', decision: null })
  expect(item.decided).toBe(true)
  expect(item.answer).toBe('Expired without a decision')
})
it('sends a held reply to the principal UUID even without a sender session or target', async () => {
  const message = { id: 'held', sender_principal_id: 'd2c76909-751c-4408-82bf-d8e7a2495509', recipient_principal_id: 'person', to: 'person', body: '', sent_event_id: 1, is_action_request: true, expects_reply: true, delivery_level: 'simple' as const, status: 'held' as const, reply_obligation: 'open' as const }
  const item = actionItem(message, 'project', 'Aeon'), sources = emptySources(); sources.actions.set(item.id, message)
  const fetch = vi.fn(async (_url: string, init: RequestInit) => {
    if (_url.includes('/message-targets')) return new Response('[]')
    expect(JSON.parse(init.body as string)).toMatchObject({ to: message.sender_principal_id, reply_to: message.id })
    return new Response(JSON.stringify({ status: 'accepted' }))
  }); vi.stubGlobal('fetch', fetch)
  await expect(commitDesk(item, { ...draftFor(item), answer: 'Continue.' }, sources, 'operation', false)).resolves.toMatchObject({ decided: true })
  expect(fetch).toHaveBeenCalledTimes(1)
})
it('skips tier reads for sessions that do not advertise service_tier_v1', async () => {
  const fetch = vi.fn(); vi.stubGlobal('fetch', fetch)
  const adapter = nativeTierAdapter(async () => [{ id: 's', advertised_capabilities: ['managed_control_v1'] }] as never, () => true)
  expect(await adapter.read()).toEqual({ items: [], warnings: [] })
  expect(fetch).not.toHaveBeenCalled()
})
it('formats delivery deadlines and keeps a rule proposal PR link', () => {
  const q = question(); q.revision = 2; q.state = 'answered'
  q.pending = [{ id: 'p', revision: 2, kind: 'inbox', state: 'pending', deliver_after: '2026-10-02T08:00:00Z' }]
  expect(questionItem(q, 'Aeon').delivery).not.toContain('2026-10-02T08:00:00Z')
  expect(ruleItem({ id: 'r', pr_url: 'https://github.com/inspr-at/inspr-modules/pull/123' } as never)).toHaveProperty('prUrl', 'https://github.com/inspr-at/inspr-modules/pull/123')
})
it('warns when the approvals read reaches its 200-row cap', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => new Response(JSON.stringify(url.includes('/approvals?') ? Array.from({ length: 200 }, (_, at) => ({ id: `a-${at}`, resource_kind: 'tenant', proposed_at: '', expires_at: '', decision: null })) : { items: [], has_more: false }))))
  const result = await loadDesk()
  expect(result.warnings).toContain('Approvals may be incomplete: the source limit is 200 requests.')
})
