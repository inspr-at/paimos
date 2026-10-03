// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { draftFor } from '../src/lib/decisionDesk'
import { commitDesk, decisionPermission, emptySources, keyTrimItem, loadDesk, loadMoreQuestions, type Question } from '../src/lib/decisionDeskApi'
import { decideKeyTrim, readKeyTrims, type KeyTrimProposal } from '../src/lib/keyTrim'

export function trimFixture(): KeyTrimProposal {
  return { id: 'proposal', key_id: 'key', key_name: 'worker', owner_id: 'agent', owner_name: 'Worker owner', previous_scopes: ['nodes.read', 'nodes.write'], snapshot_digest: 'a'.repeat(64), candidate_scopes: ['nodes.read'], candidate_digest: 'b'.repeat(64), evidence: { summary: 'Reviewed ticket workload.', observed_at: '2026-10-02T08:00:00Z', risks: [{ scope: 'nodes.write', evidence: 'No recorded use; earlier use unknown.', risk_if_dropped: 'Creating and editing tickets fails.' }] }, usage: [{ scope: 'nodes.write', last_used_at: null }], created_by: 'coordinator', created_at: '2026-10-03T08:00:00Z', expires_at: new Date(Date.now() + 60_000).toISOString(), state: 'pending', revision: 1, applied_at: null, restore_until: null }
}
afterEach(() => vi.unstubAllGlobals())
it('preserves key trim cursors and an in-flight approval when continuing questions', async () => {
  const proposal = trimFixture()
  const question: Question = { id: 'question-1', project_id: 'project', revision: 1, state: 'open', suggested_outcome: 'once', suggestion_reason: 'ticket_default',
    input: { request_id: 'question-request', question: 'Which index?', options: [], meanwhile: 'parked' }, askers: [], pending: [], created_at: '', updated_at: '' }
  let release!: (response: Response) => void
  const fetch = vi.fn((path: string, init?: RequestInit): Promise<Response> => {
    const url = new URL(path, 'https://test.invalid')
    if (init?.method === 'POST') return Promise.resolve(Response.json({ ...proposal, revision: 2, state: 'applied' }))
    if (url.pathname === '/api/decision-desk') {
      if (url.searchParams.get('state') === 'answered') return Promise.resolve(Response.json({ items: [], has_more: false }))
      if (url.searchParams.get('offset') === '100') return new Promise(resolve => { release = resolve })
      return Promise.resolve(Response.json({ items: [question], has_more: true }))
    }
    if (url.pathname === '/api/key-trim-proposals') return Promise.resolve(Response.json(url.searchParams.get('state') === 'pending'
      ? { items: [proposal], has_more: true, next_cursor: proposal.id } : { items: [], has_more: false }))
    return Promise.resolve(Response.json(url.pathname === '/api/approvals' ? [] : { items: [] }))
  })
  vi.stubGlobal('fetch', fetch)
  const first = await loadDesk(), item = first.items.find(item => item.kind === 'key_trim')!
  let current = first
  const pending = loadMoreQuestions(first, 'open', () => current)
  const applied = await commitDesk(item, { ...draftFor(item), optionId: 'approve' }, first.sources, 'approve-request', false)
  current = { ...first, items: first.items.map(row => row.id === applied.id ? applied : row) }
  release(Response.json({ items: [{ ...question, id: 'question-2' }], has_more: false }))
  const result = await pending
  expect(result.items.map(item => item.id)).toEqual(['q:question-1', 'q:question-2', item.id])
  expect(result.items.at(-1)).toEqual(applied)
  expect(result.sources.keyTrims.get(item.id)?.state).toBe('applied')
  expect(result.nextKeyTrims).toEqual({ pending: proposal.id })
  expect(fetch.mock.calls.filter(([path, init]) => path.startsWith('/api/key-trim-proposals') && init?.method !== 'POST')).toHaveLength(2)
})
it('routes approve, decline and restore through protected native CAS endpoints', async () => {
  for (const decision of ['approve', 'decline', 'restore'] as const) {
    const proposal = trimFixture()
    if (decision === 'decline') proposal.blocked_reason = 'A dropped scope was used recently.'
    if (decision === 'restore') { proposal.state = 'applied'; proposal.revision = 2; proposal.restore_until = new Date(Date.now() + 60_000).toISOString() }
    const fetch = vi.fn(async (_url: string, init: RequestInit) => {
      expect(JSON.parse(init.body as string)).toEqual({ request_id: 'request', expected_digest: decision === 'restore' ? proposal.candidate_digest : proposal.snapshot_digest, ...(decision !== 'restore' && { decision }) })
      return new Response(JSON.stringify({ ...proposal, revision: proposal.revision + 1, state: decision === 'approve' ? 'applied' : decision === 'restore' ? 'restored' : 'declined' }))
    }); vi.stubGlobal('fetch', fetch)
    const item = keyTrimItem(proposal), sources = emptySources(); sources.keyTrims.set(item.id, proposal)
    const result = await commitDesk(item, { ...draftFor(item), optionId: decision }, sources, 'request', true)
    expect(fetch.mock.calls[0]![0]).toBe(`/api/key-trim-proposals/proposal/${decision === 'restore' ? 'restore' : 'decision'}`)
    expect(result.decided).toBe(true)
  }
})
it('retains frozen source identity and rejects changed records and unconfirmed responses', async () => {
  const proposal = trimFixture(), item = keyTrimItem(proposal), sources = emptySources(); sources.keyTrims.set(item.id, { ...proposal, revision: 2 })
  const fetch = vi.fn(async () => new Response(JSON.stringify({ ...proposal, id: 'other', state: 'applied', revision: 2 }))); vi.stubGlobal('fetch', fetch)
  await expect(commitDesk(item, { ...draftFor(item), optionId: 'approve' }, sources, 'request', false)).rejects.toThrow('source changed')
  expect(fetch).not.toHaveBeenCalled()
  await expect(decideKeyTrim(proposal, 'approve', 'request')).rejects.toThrow('could not be confirmed')
})
it('shows source evidence without assuming unknown scopes are unused and requires manage rights', () => {
  const item = keyTrimItem(trimFixture())
  expect(item.keyTrim?.evidence.risks[0]?.risk_if_dropped).toContain('tickets fails')
  expect(draftFor(item).optionId).toBe('')
  expect(decisionPermission(item, emptySources(), permission => permission === 'approvals.decide')).toBe(false)
  expect(decisionPermission(item, emptySources(), permission => permission === 'keys.manage')).toBe(true)
})
it('propagates recent-use and CAS refusal without reporting success', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'A dropped scope was recently used.' }), { status: 409 })))
  await expect(decideKeyTrim(trimFixture(), 'approve', 'request')).rejects.toThrow('recently used')
})
it('exposes bounded continuation independently for pending and decided proposals', async () => {
  const fetch = vi.fn(async (url: string) => {
    const state = new URL(url, 'https://test.invalid').searchParams.get('state')!
    return new Response(JSON.stringify({ items: Array.from({ length: 100 }, (_, i) => ({ ...trimFixture(), id: `${state}-${i}` })), has_more: true, next_cursor: `${state}-99` }))
  }); vi.stubGlobal('fetch', fetch)
  const page = await readKeyTrims()
  expect(page.items).toHaveLength(200)
  expect(page.next).toEqual({ pending: 'pending-99', decided: 'decided-99' })
  expect(page.warnings).toEqual(['More pending key trim proposals are available. Use Next key trims in that view.', 'More decided key trim proposals are available. Use Next key trims in that view.'])
  expect(fetch).toHaveBeenCalledTimes(2)
})
it.each(['approve', 'restore'] as const)('can %s proposals beyond the first page through the native CAS endpoint', async decision => {
  const proposal = trimFixture(), state = decision === 'approve' ? 'pending' : 'decided'
  if (decision === 'restore') { proposal.state = 'applied'; proposal.revision = 2; proposal.restore_until = new Date(Date.now() + 60_000).toISOString() }
  const fetch = vi.fn(async (url: string, init?: RequestInit) => {
    const params = new URL(url, 'https://test.invalid').searchParams
    if (init?.method === 'POST') return new Response(JSON.stringify({ ...proposal, revision: proposal.revision + 1, state: decision === 'approve' ? 'applied' : 'restored' }))
    if (params.get('state') !== state) return new Response(JSON.stringify({ items: [], has_more: false }))
    if (!params.has('cursor')) return new Response(JSON.stringify({ items: Array.from({ length: 100 }, (_, i) => ({ ...proposal, id: `earlier-${i}` })), has_more: true, next_cursor: 'earlier-99' }))
    expect(params.get('cursor')).toBe('earlier-99')
    return new Response(JSON.stringify({ items: [proposal], has_more: false }))
  }); vi.stubGlobal('fetch', fetch)
  const first = await readKeyTrims()
  expect(first.items.some(row => row.id === proposal.id)).toBe(false)
  const next = await readKeyTrims(first.next)
  expect(next.items).toHaveLength(1)
  expect(next.items).toEqual([proposal])
  expect(next.next).toEqual({})
  const item = keyTrimItem(next.items[0]!), sources = emptySources(); sources.keyTrims.set(item.id, proposal)
  const result = await commitDesk(item, { ...draftFor(item), optionId: decision }, sources, 'request', false)
  expect(result.keyTrim?.state).toBe(decision === 'approve' ? 'applied' : 'restored')
  expect(fetch.mock.calls.at(-1)?.[0]).toBe(`/api/key-trim-proposals/proposal/${decision === 'restore' ? 'restore' : 'decision'}`)
})

it('rejects missing, repeated and mismatched continuation and oversized pages', async () => {
  for (const next_cursor of [undefined, 'old', 'wrong']) {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ items: [{ ...trimFixture(), id: 'last' }], has_more: true, next_cursor }))))
    await expect(readKeyTrims({ pending: 'old' })).rejects.toThrow('continuation is invalid')
  }
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ items: Array.from({ length: 101 }, trimFixture), has_more: false }))))
  await expect(readKeyTrims()).rejects.toThrow('page is invalid')
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { headers: { 'Content-Length': String(9 * 1024 * 1024) } })))
  await expect(readKeyTrims()).rejects.toThrow('too large')
})
