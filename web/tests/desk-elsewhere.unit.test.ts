// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1495 risk: a sibling's answer (or one pretending to be it) puts a link to
// another site, more than first lines or a wrong count into this instance's band.
import { effectScope, type EffectScope } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

const harness = vi.hoisted(() => ({
  identity: null as { tenant: { id: string }; principal: { id: string; kind: string } } | null,
  api: vi.fn(),
}))
vi.mock('../src/stores/session', () => ({ useSession: () => harness }))
vi.mock('../src/lib/api', () => ({ api: harness.api }))
const { parseSummary, readSibling, useDeskElsewhere } = await import('../src/lib/deskElsewhere')
const scopes: EffectScope[] = []
afterEach(() => { scopes.splice(0).forEach(scope => scope.stop()); harness.identity = null; harness.api.mockReset(); vi.unstubAllGlobals() })

const pma = { tag: 'pma', origin: 'https://pma.example' }
const line = (extra: Record<string, unknown> = {}) => ({ id: 'q1', kind: 'question', project_key: 'PORTAL', title: 'Deploy the sign-in flow?', asked_by: 'PORTAL', held: false, href: '/decision-desk?item=q:q1', context: 'private context', ...extra })
const body = (items: unknown[], extra: Record<string, unknown> = {}) => ({ instance: 'pma', items, counts: { open: items.length, held: 0 }, as_of: '2026-10-21T17:00:00Z', ...extra })

describe('parseSummary', () => {
  it('keeps first lines and resolves links inside the sibling only', () => {
    const summary = parseSummary(pma, body([line(), line({ id: 'q2', href: '//evil.example/x' }), line({ id: 'q3', href: 'https://evil.example/x' })], { upcoming: { label: 'Voice session' } }))
    expect(summary?.lines).toEqual([{ id: 'q1', projectKey: 'PORTAL', title: 'Deploy the sign-in flow?', askedBy: 'PORTAL', held: false, url: 'https://pma.example/decision-desk?item=q:q1' }])
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
    await expect(readSibling(pma)).rejects.toThrow(/too much/)
    expect(cancelled).toBe(true)
    expect(offset).toBeGreaterThan(64_000)
    expect(offset).toBeLessThanOrEqual(64_000 + chunk)
  })
})

describe('useDeskElsewhere', () => {
  const person = () => { harness.identity = { tenant: { id: 't1' }, principal: { id: 'p1', kind: 'person' } } }
  const instances = (siblings: { tag: string; origin: string }[]) => new Response(JSON.stringify({ here: 'ppm', siblings }), { status: 200 })
  const summary = (items: ReturnType<typeof line>[]) => new Response(JSON.stringify(body(items)), { status: 200 })
  async function mounted() {
    const scope = effectScope()
    scopes.push(scope)
    const desk = scope.run(() => useDeskElsewhere({ refreshEvery: 0 }))!
    await vi.waitFor(() => expect(desk.settled.value).toBe(true))
    return desk
  }

  it('keeps a failed instance list unknown and reads it again on refresh', async () => {
    person()
    harness.api.mockRejectedValueOnce(new Error('down'))
    const desk = await mounted()
    expect(desk.discovery.value).toBe('unknown')
    expect(desk.reads.value).toEqual([])
    harness.api.mockResolvedValueOnce(instances([{ tag: 'pma', origin: 'https://pma.example' }]))
    vi.stubGlobal('fetch', vi.fn(async () => summary([line()])))
    desk.refresh()
    await vi.waitFor(() => expect(desk.discovery.value).toBe('known'))
    expect(harness.api).toHaveBeenCalledTimes(2)
    expect(desk.reads.value.map(read => read.tag)).toEqual(['pma'])
    expect(desk.reads.value[0]?.state).toBe('ok')
  })

  it('treats a confirmed empty sibling list as known', async () => {
    person()
    harness.api.mockResolvedValue(instances([]))
    const desk = await mounted()
    expect(desk.discovery.value).toBe('known')
    expect(desk.reads.value).toEqual([])
    desk.refresh()
    await new Promise(resolve => setTimeout(resolve, 20))
    expect(harness.api).toHaveBeenCalledTimes(1)
    expect(desk.discovery.value).toBe('known')
  })

  it('keeps the shown line count when a later sibling refresh fails', async () => {
    person()
    harness.api.mockResolvedValue(instances([{ tag: 'pma', origin: 'https://pma.example' }]))
    let down = false
    vi.stubGlobal('fetch', vi.fn(async () => down ? new Response('no', { status: 401 }) : summary([line(), line({ id: 'q2', title: 'Second question' })])))
    const desk = await mounted()
    expect(desk.reads.value[0]).toMatchObject({ state: 'ok', shown: 2 })
    down = true
    desk.refresh()
    await vi.waitFor(() => expect(desk.reads.value[0]?.state).toBe('error'))
    expect(desk.reads.value[0]?.shown).toBe(2)
    expect(desk.discovery.value).toBe('known')
  })
})
