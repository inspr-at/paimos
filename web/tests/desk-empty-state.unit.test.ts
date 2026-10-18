// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1406 risk: an empty Open tab says "Nothing waits for you" while a source
// did not answer, so a person stops looking while something is waiting.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { deskEmptyState, deskUnansweredLine, type DeskProjection } from '../src/lib/decisionDesk'
import { loadDesk, nativeTierAdapter, tierSessionsFromRefresh } from '../src/lib/decisionDeskApi'
import type { HarnessSession } from '../src/lib/agents'

afterEach(() => vi.unstubAllGlobals())

const empty: DeskProjection = { items: [], counts: { open: 0, held: 0, chores: 0 }, has_more: false, as_of: '2026-10-16T08:00:00Z' }
// Every source answers with an empty page unless the test names a failing one.
function sources(failing: string[] = []) {
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    const url = new URL(path, 'https://test.invalid')
    if (failing.includes(url.pathname)) return Response.json({ error: 'Unavailable' }, { status: 503 })
    if (url.pathname === '/api/projects') return Response.json({ items: [{ id: 'p-pharos', key: 'PHAROS', title: 'Pharos' }] })
    if (url.pathname === '/api/approvals') return Response.json([])
    if (url.pathname === '/api/rules/doctrine/inbox') return Response.json({ items: [], pending: 0 })
    if (url.pathname === '/api/projects/p-pharos/messages') return Response.json({ items: [], next_after: 0 })
    if (['/api/decision-desk', '/api/key-trim-proposals', '/api/stepup-requests'].includes(url.pathname)) return Response.json({ items: [], has_more: false })
    throw new Error(`Unexpected read: ${path}`)
  }))
}
const choose = (read: { unanswered?: string[]; warnings: string[] }, over: Partial<Parameters<typeof deskEmptyState>[0]> = {}) =>
  deskEmptyState({ reading: false, open: 0, scoped: false, unanswered: (read.unanswered ?? []) as never, failed: false, ...over })

describe('Decision Desk empty state', () => {
  it('chooses reading, all clear, a silent source or the scope line, and nothing while rows are listed', () => {
    const base = { reading: false, open: 0, scoped: false, unanswered: [], failed: false }
    expect(deskEmptyState({ ...base, reading: true })).toBe('reading')
    expect(deskEmptyState({ ...base, reading: true, scoped: true })).toBe('reading')
    expect(deskEmptyState(base)).toBe('clear')
    expect(deskEmptyState({ ...base, unanswered: ['approvals'] })).toBe('incomplete')
    // A failure only the warning text names still blocks "all clear".
    expect(deskEmptyState({ ...base, failed: true })).toBe('incomplete')
    expect(deskEmptyState({ ...base, scoped: true, unanswered: ['approvals'] })).toBe('scoped')
    expect(deskEmptyState({ ...base, open: 1, unanswered: ['approvals'] })).toBeNull()
    expect(deskUnansweredLine(['approvals'])).toBe('Approvals didn’t answer, so this may be incomplete')
    expect(deskUnansweredLine(['approvals', 'stepups'])).toBe('Some sources didn’t answer, so this may be incomplete')
    expect(deskUnansweredLine(['approvals', 'desk'])).toBe('The desk didn’t answer, so this may be incomplete')
    expect(deskUnansweredLine([])).toBe('Some sources didn’t answer, so this may be incomplete')
  })

  it('names the source that did not answer, so an empty read is never all clear', async () => {
    sources()
    const answered = await loadDesk(undefined, {}, {}, empty)
    expect(answered.unanswered).toEqual([])
    expect(choose(answered)).toBe('clear')

    sources(['/api/approvals'])
    const silent = await loadDesk(undefined, {}, {}, empty)
    expect(silent.items.filter(item => !item.decided)).toEqual([])
    expect(silent.unanswered).toEqual(['approvals'])
    expect(choose(silent)).toBe('incomplete')
    expect(deskUnansweredLine(silent.unanswered!)).toBe('Approvals didn’t answer, so this may be incomplete')

    // The false empty AEON-1406 closes: the canonical desk counts two open rows but lists none.
    // Its warning is no source failure by text, so only the source flag keeps this from "all clear".
    sources()
    const partial = await loadDesk(undefined, {}, {}, { ...empty, counts: { open: 2, held: 0, chores: 0 } })
    expect(partial.items).toEqual([])
    expect(partial.warnings).toContain('The canonical desk is partial or changed while reading. Retry for current coverage.')
    expect(partial.unanswered).toEqual(['desk'])
    expect(choose(partial)).toBe('incomplete')

    sources()
    const missing = await loadDesk(undefined, {}, {}, null)
    expect(missing.unanswered).toEqual(['desk'])
    expect(choose(missing)).toBe('incomplete')
  })

  // Risk: refreshSessions answers {ok:false} and the desk still returns the empty
  // cache, so no tier read runs and the Open tab says all clear while discovery failed.
  it('keeps the empty desk incomplete when session discovery fails, until a successful retry', async () => {
    const cache: HarnessSession[] = []
    let outcome: { ok: true } | { ok: false; error: string } | { ok: false; dropped: true } = { ok: false, error: 'Session list unavailable' }
    expect(tierSessionsFromRefresh(outcome, cache)).toEqual({ ok: false, sessions: cache })
    expect(tierSessionsFromRefresh({ ok: false, dropped: true }, cache)).toEqual({ ok: false, sessions: cache })
    expect(tierSessionsFromRefresh({ ok: true }, cache)).toBe(cache)
    const adapter = nativeTierAdapter(async () => tierSessionsFromRefresh(outcome, cache), () => true)
    const view = readFileSync(new URL('../src/views/DecisionDeskView.vue', import.meta.url), 'utf8')
    expect(view).toContain('tierSessionsFromRefresh(await agents.refreshSessions(), agents.sessions)')

    sources()
    const missedRead = await adapter.read([])
    expect(missedRead.failed).toBe(true)
    expect(missedRead.items).toEqual([])
    expect(missedRead.warnings).toContain('Some tier requests could not be read. Open Agents to inspect their source.')
    const missed = await loadDesk(undefined, { tiers: adapter }, {}, empty)
    expect(missed.items.filter(item => !item.decided)).toEqual([])
    expect(missed.unanswered).toEqual(['tiers'])
    expect(choose(missed)).toBe('incomplete')
    expect(deskUnansweredLine(missed.unanswered!)).toBe('Tier requests didn’t answer, so this may be incomplete')

    outcome = { ok: true }
    sources()
    const settledRead = await adapter.read([])
    expect(settledRead).toEqual({ items: [], warnings: [] })
    expect(settledRead.failed).toBeUndefined()
    const settled = await loadDesk(undefined, { tiers: adapter }, {}, empty)
    expect(settled.unanswered).toEqual([])
    expect(choose(settled)).toBe('clear')
  })
})
