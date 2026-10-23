// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { getDraft, saveDraft } from '../src/lib/quotes/api'
import { APIError } from '../src/lib/api'
import { QuoteSession } from '../src/lib/quoteSession'
import { mergeQuote } from '../src/lib/quoteMerge'
import { deferred } from './record-source'
import { getQuote } from '../src/lib/quotes/lifecycle'
import { loadRecovery, type RecoveryDraft } from '../src/lib/quoteRecovery'
import { acquireQuote, closeAllQuotes } from '../src/lib/quoteWorkspace'
import type { QuoteDocumentData } from '../src/lib/quotes/types'

vi.mock('../src/lib/quotes/api', () => ({ getDraft: vi.fn(), saveDraft: vi.fn() }))
vi.mock('../src/lib/quotes/lifecycle', () => ({ getQuote: vi.fn(), getVersion: vi.fn() }))
vi.mock('../src/lib/quoteRecovery', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/quoteRecovery')>(),
  loadRecovery: vi.fn(), clearRecovery: vi.fn().mockResolvedValue(undefined), saveRecovery: vi.fn().mockResolvedValue(true),
}))
vi.mock('../src/stores/session', () => ({ useSession: () => ({ identity: null }) }))
vi.mock('../src/lib/quotePresence', () => ({ QuotePresence: class { async start() {} stop() {} } }))

const document = (): QuoteDocumentData => ({
  schema_version: 1, minimum_writer_version: 1, title: 'Saved quote', subtitle: '', project_ref: '',
  offer_date: '2026-10-02', valid_until: '2026-11-02', currency: 'EUR',
  sender: {}, recipient: {}, legal: {}, layout: {}, sections: [], positions: [], net_total_cents: 0,
})
beforeEach(() => {
  vi.useFakeTimers()
  vi.stubGlobal('BroadcastChannel', undefined)
  vi.stubGlobal('sessionStorage', { getItem: () => 'tab', setItem: () => {} })
  vi.stubGlobal('window', {
    setTimeout: (...args: Parameters<typeof setTimeout>) => setTimeout(...args),
    clearTimeout: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
    addEventListener: vi.fn(), removeEventListener: vi.fn(),
  })
  vi.stubGlobal('document', { addEventListener: () => {}, removeEventListener: () => {} })
  vi.mocked(getDraft).mockResolvedValue({ draft_revision: 1, quote_revision: 1, base_version: 0, document: document(),
    document_sha256: 'fixture', schema_version: 1, minimum_writer_version: 1, updated_at: '2026-10-02T00:00:00Z', updated_by_principal_id: 'person' })
  vi.mocked(getQuote).mockResolvedValue({ id: 'quote', state: 'draft', current_version: 0, revision: 1 } as Awaited<ReturnType<typeof getQuote>>)
  vi.mocked(loadRecovery).mockResolvedValue(null)
})
const sessions: QuoteSession[] = []
const session = () => {
  const next = new QuoteSession({ tenantId: 'tenant', principalId: 'person', quoteId: 'quote', clientSessionId: 'tab' })
  sessions.push(next)
  return next
}
afterEach(() => { closeAllQuotes(); sessions.splice(0).forEach(value => value.dispose()); vi.unstubAllGlobals(); vi.useRealTimers(); vi.clearAllMocks() })

for (const kind of ['discarded', 'unchanged', 'wrong-version', 'matching'] as const) {
  it(`waits for a deferred ${kind} recovery decision even after the old 400ms window`, async () => {
    let begin!: () => void
    const reading = new Promise<void>(resolve => { begin = resolve })
    let finish!: (copy: RecoveryDraft | null) => void
    vi.mocked(loadRecovery).mockImplementation(() => { begin(); return new Promise(resolve => { finish = resolve }) })
    const quote = acquireQuote({ tenantId: 'tenant', principalId: 'person' }, 'quote')
    let ready = false
    void quote.ready.then(() => { ready = true })
    await reading
    expect(quote.view.value?.working?.title).toBe('Saved quote')
    expect(quote.recoveryReady.value).toBe(false)
    await vi.advanceTimersByTimeAsync(1000)
    expect(ready).toBe(false)
    expect(quote.recoveryReady.value).toBe(false)
    const copy: RecoveryDraft | null = kind === 'discarded' ? null : {
      tenantId: 'tenant', principalId: 'person', quoteId: 'quote', sessionId: 'tab',
      baseRevision: 1, baseVersion: kind === 'wrong-version' ? 3 : 0,
      base: document(), mine: { ...document(), title: kind === 'unchanged' ? 'Saved quote' : 'Local work' },
      savedAt: Date.now(), schemaVersion: 1,
    }
    finish(copy)
    await quote.ready
    expect(quote.recoveryReady.value).toBe(true)
    expect(quote.recovery.value).toEqual(kind === 'matching' ? copy : null)
    expect(quote.view.value?.working?.title).toBe('Saved quote')
  })
}

it('AEON-1682: loading refuses writes and a held reload cannot replace an edit made after it began', async () => {
  const current = session()
  current.edit({ ...document(), title: 'Must not save during loading' })
  await current.save()
  expect(saveDraft).not.toHaveBeenCalled()
  await current.open()
  const gate = deferred<Awaited<ReturnType<typeof getDraft>>>()
  vi.mocked(getDraft).mockReturnValueOnce(gate.promise)
  const reloading = current.reload()
  expect(getDraft).toHaveBeenCalledTimes(2)
  current.edit({ ...document(), title: 'Local edit after reload began' })
  gate.resolve({ ...(await vi.mocked(getDraft).mock.results[0]!.value), document: { ...document(), title: 'Older network result' } })
  await expect(reloading).resolves.toBe('stale')
  expect(current.view.working!.title).toBe('Local edit after reload began')
  expect(current.view.local).toBe('dirty')
  expect(saveDraft).not.toHaveBeenCalled()
})

it('AEON-1682: revision watch backs off on the injected clock and focus rechecks without waiting', async () => {
  const current = session()
  await current.open()
  const draft = await vi.mocked(getDraft).mock.results[0]!.value
  vi.mocked(getDraft).mockRejectedValueOnce(new Error('offline')).mockRejectedValueOnce(new Error('offline'))
    .mockRejectedValueOnce(new Error('offline')).mockResolvedValue({ ...draft, draft_revision: 2 })
  await vi.advanceTimersByTimeAsync(4000)
  expect(getDraft).toHaveBeenCalledTimes(2)
  expect(current.view.remote).toBe('unavailable')
  await vi.advanceTimersByTimeAsync(7999)
  expect(getDraft).toHaveBeenCalledTimes(2)
  await vi.advanceTimersByTimeAsync(1)
  expect(getDraft).toHaveBeenCalledTimes(3)
  await vi.advanceTimersByTimeAsync(15999)
  expect(getDraft).toHaveBeenCalledTimes(3)
  await vi.advanceTimersByTimeAsync(1)
  expect(getDraft).toHaveBeenCalledTimes(4)
  const focus = vi.mocked(window.addEventListener).mock.calls.find(call => call[0] === 'focus')![1] as () => void
  focus()
  await Promise.resolve(); await Promise.resolve()
  expect(getDraft).toHaveBeenCalledTimes(5)
  expect(current.view.remote).toBe('newer')
  expect(Reflect.get(current, 'checkDelay')).toBe(4000)
})

it('AEON-1682: merge review preserves text overlaps, deleted anchors and competing order', () => {
  const base = document()
  base.sections = [{ id: 'section-1', heading: 'Scope', body: '', nodes: [{ id: 'node-1', kind: 'paragraph', text: 'Base wording' }] }]
  const mine = structuredClone(base), theirs = structuredClone(base)
  mine.sections[0]!.nodes[0]!.text = 'My wording'; theirs.sections[0]!.nodes[0]!.text = 'Their wording'
  const overlap = mergeQuote(base, mine, theirs)
  expect(overlap.conflicts.map(conflict => conflict.path)).toContain('$.sections[section-1].nodes[node-1].text')
  const chosen = mergeQuote(base, mine, theirs, { [overlap.conflicts[0]!.path]: 'theirs' })
  expect(chosen.document!.sections[0]!.nodes[0]!.text).toBe('Their wording')
  mine.sections = []
  expect(mergeQuote(base, mine, theirs).conflicts.map(conflict => conflict.reason)).toContain('deletion versus edit or competing addition')
  base.sections.push({ ...structuredClone(base.sections[0]!), id: 'section-2', nodes: [] }, { ...structuredClone(base.sections[0]!), id: 'section-3', nodes: [] })
  mine.sections = [base.sections[1]!, base.sections[0]!, base.sections[2]!]
  theirs.sections = [base.sections[0]!, base.sections[2]!, base.sections[1]!]
  expect(mergeQuote(base, mine, theirs).conflicts.map(conflict => conflict.reason)).toContain('competing reorder')
})

it('AEON-1682: refused saves preserve work and distinguish corrected mutations from uncertain same-id retry', async () => {
  for (const [status, kind] of [[400, 'invalid'], [403, 'forbidden'], [503, 'server']] as const) {
    const current = session()
    await current.open()
    current.edit({ ...document(), title: `Unsaved ${kind}` })
    const body = { errors: { 'document.title': 'review this field' } }
    vi.mocked(saveDraft).mockRejectedValueOnce(new APIError(status, kind, body))
    await current.save()
    const first = vi.mocked(saveDraft).mock.calls.at(-1)!
    expect(first.slice(0, 2)).toEqual(['quote', 1])
    expect(current.view).toMatchObject({ local: 'failed', baseRevision: 1, working: { title: `Unsaved ${kind}` }, failure: { kind, status, body } })
    expect(current.view.pendingMutationId).toBe(kind === 'server' ? first[4] : null)
    vi.mocked(saveDraft).mockImplementationOnce(async (_quote, revision, document, _session, mutation) => ({
      mutation_id: mutation, acknowledged_revision: revision + 1, acknowledged_quote_revision: 1,
      current_revision: revision + 1, current_quote_revision: 1, replayed: false, document,
    }))
    if (kind !== 'server') current.edit({ ...document(), title: `Corrected ${kind}` })
    await current.save()
    const next = vi.mocked(saveDraft).mock.calls.at(-1)!
    if (kind === 'server') expect(next[4]).toBe(first[4])
    else expect(next[4]).not.toBe(first[4])
    expect(current.view.local).toBe('clean')
    expect(current.view.working!.title).toBe(kind === 'server' ? `Unsaved ${kind}` : `Corrected ${kind}`)
    current.dispose()
  }
})
