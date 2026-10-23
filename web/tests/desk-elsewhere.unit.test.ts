// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1495 risk: a sibling's answer (or one pretending to be it) puts a link to
// another site, more than first lines or a wrong count into this instance's band.
import { afterEach, describe, expect, it, vi } from 'vitest'

const harness = vi.hoisted(() => ({
  identity: null as { tenant: { id: string }; principal: { id: string; kind: string } } | null,
  api: vi.fn(),
}))
vi.mock('../src/stores/session', () => ({ useSession: () => harness }))
vi.mock('../src/lib/api', () => ({ api: harness.api }))
const { parseSummary, readSibling, ConnectionEnded } = await import('../src/lib/deskElsewhere')
afterEach(() => { harness.identity = null; harness.api.mockReset(); vi.unstubAllGlobals() })

const pma = { tag: 'pma', origin: 'https://pma.example' }
const line = (extra: Record<string, unknown> = {}) => ({ id: 'q1', kind: 'question', project_key: 'PORTAL', title: 'Deploy the sign-in flow?', asked_by: 'PORTAL', held: false, href: '/decision-desk?item=q:q1', context: 'private context', ...extra })
const body = (items: unknown[], extra: Record<string, unknown> = {}) => ({ instance: 'pma', items, counts: { open: items.length, held: 0 }, as_of: '2026-10-21T17:00:00Z', ...extra })

describe('parseSummary', () => {
  it('keeps first lines and resolves links inside the sibling only', () => {
    const summary = parseSummary(pma, body([line(), line({ id: 'q2', href: '//evil.example/x' }), line({ id: 'q3', href: 'https://evil.example/x' })], { upcoming: { label: 'Voice session' } }))
    expect(summary?.lines).toEqual([{ id: 'q1', kind: 'question', createdAt: '', projectKey: 'PORTAL', title: 'Deploy the sign-in flow?', askedBy: 'PORTAL', held: false, url: 'https://pma.example/decision-desk?item=q:q1' }])
    expect(summary?.open).toBe(3)
    expect(summary?.upcoming).toBe('Voice session')
  })
  it('refuses another instance, bad counts and unbounded lists', () => {
    expect(parseSummary(pma, body([line()], { instance: 'ppm' }))).toBeNull()
    expect(parseSummary(pma, body([line()], { counts: { open: -1, held: 0 } }))).toBeNull()
    expect(parseSummary(pma, { instance: 'pma', items: 'x', counts: { open: 0, held: 0 } })).toBeNull()
    const many = parseSummary(pma, body(Array.from({ length: 50 }, (_, i) => line({ id: `q${i}` }))))
    expect(many?.lines).toHaveLength(5)
    expect(parseSummary(pma, body([line({ title: 'x'.repeat(500) })]))?.lines[0]?.title).toHaveLength(200)
  })
})

describe('readSibling', () => {
  it('rejects a sibling body over 64000 bytes even when Content-Length is smaller', async () => {
    const payload = JSON.stringify(body([line({ title: 'Q'.repeat(100_000) })]))
    const bytes = new TextEncoder().encode(payload)
    expect(bytes.byteLength).toBeGreaterThan(100_000)
    let offset = 0, cancelled = false
    const chunk = 4096
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true, status: 200,
      headers: { get: (name: string) => name.toLowerCase() === 'content-length' ? '10' : null },
      body: {
        getReader: () => ({
          read: async () => {
            if (offset >= bytes.length) return { done: true, value: undefined }
            const end = Math.min(bytes.length, offset + chunk)
            const value = bytes.subarray(offset, end)
            offset = end
            return { done: false, value }
          },
          cancel: async () => { cancelled = true },
          releaseLock: () => undefined,
        }),
        cancel: async () => { cancelled = true },
      },
      // The old reader trusts this and returns a summary. The bound must not.
      json: async () => JSON.parse(payload),
    })))
    await expect(readSibling(pma, 'browser-only-test-grant')).rejects.toThrow(/too much/)
    expect(cancelled).toBe(true)
    expect(offset).toBeGreaterThan(64_000)
    expect(offset).toBeLessThanOrEqual(64_000 + chunk)
  })
})

// Risk: throttling discards a still-valid grant and its dated cached summary.
it('treats 429 as unreachable instead of ending the connection', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 429, headers: { 'Retry-After': '60' } })))
  const result = await readSibling(pma, 'browser-only-test-grant').catch(error => error)
  expect(result).toBeInstanceOf(Error)
  expect(result).not.toBeInstanceOf(ConnectionEnded)
  expect(result.message).toContain('429')
})

it('exchanges the history code in a credential-free POST and receives the bearer only in the body', async () => {
  const { exchangeInstance } = await import('../src/lib/instanceConnect')
  const token = `desk_${'a'.repeat(36)}_${'b'.repeat(36)}_${'c'.repeat(43)}`
  const fetch = vi.fn(async () => new Response(JSON.stringify({ token, instance: 'pma', account: 'Account', created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 90 * 86400_000).toISOString() })))
  vi.stubGlobal('fetch', fetch)
  const result = await exchangeInstance(pma, 'history-code', 'state', 'verifier')
  expect(result.token).toBe(token)
  expect(fetch).toHaveBeenCalledWith('https://pma.example/api/decision-desk/connections/exchange', expect.objectContaining({
    method: 'POST', credentials: 'omit', mode: 'cors', cache: 'no-store', redirect: 'error', referrerPolicy: 'no-referrer',
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code: 'history-code', state: 'state', code_verifier: 'verifier' }),
  }))
})
