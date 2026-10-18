// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { getDraft } from '../src/lib/quotes/api'
import { getQuote } from '../src/lib/quotes/lifecycle'
import { loadRecovery, type RecoveryDraft } from '../src/lib/quoteRecovery'
import { acquireQuote, closeAllQuotes } from '../src/lib/quoteWorkspace'
import type { QuoteDocumentData } from '../src/lib/quotes/types'

vi.mock('../src/lib/quotes/api', () => ({ getDraft: vi.fn(), saveDraft: vi.fn() }))
vi.mock('../src/lib/quotes/lifecycle', () => ({ getQuote: vi.fn(), getVersion: vi.fn() }))
vi.mock('../src/lib/quoteRecovery', async importOriginal => ({
  ...await importOriginal<typeof import('../src/lib/quoteRecovery')>(),
  loadRecovery: vi.fn(), clearRecovery: vi.fn().mockResolvedValue(undefined),
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
    addEventListener: () => {}, removeEventListener: () => {},
  })
  vi.stubGlobal('document', { addEventListener: () => {}, removeEventListener: () => {} })
  vi.mocked(getDraft).mockResolvedValue({ draft_revision: 1, quote_revision: 1, base_version: 0, document: document(),
    document_sha256: 'fixture', schema_version: 1, minimum_writer_version: 1, updated_at: '2026-10-02T00:00:00Z', updated_by_principal_id: 'person' })
  vi.mocked(getQuote).mockResolvedValue({ id: 'quote', state: 'draft', current_version: 0, revision: 1 } as Awaited<ReturnType<typeof getQuote>>)
})
afterEach(() => { closeAllQuotes(); vi.unstubAllGlobals(); vi.useRealTimers(); vi.clearAllMocks() })

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
