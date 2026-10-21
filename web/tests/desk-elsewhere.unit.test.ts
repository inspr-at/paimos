// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1495 risk: a sibling's answer (or one pretending to be it) puts a link to
// another site, more than first lines or a wrong count into this instance's band.
import { describe, expect, it, vi } from 'vitest'

vi.mock('../src/stores/session', () => ({ useSession: () => ({ identity: null }) }))
vi.mock('../src/lib/api', () => ({ api: vi.fn() }))
const { parseSummary } = await import('../src/lib/deskElsewhere')

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
