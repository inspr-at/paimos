// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1406 risk: an empty Open tab says "Nothing waits for you" while a source
// did not answer, so a person stops looking while something is waiting.
// AEON-1631 risk: the badge counts two items while a failed desk read shows the
// all-clear art, and nothing reads the desk again until the next refresh.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { DESK_RETRY_MS, deskAutoRetry, deskEmptyState, deskFailedWords, deskUnansweredLine, type DeskProjection, type DeskRetryPhase } from '../src/lib/decisionDesk'
import { loadDesk, nativeTierAdapter, tierSessionsFromRefresh } from '../src/lib/decisionDeskApi'
import type { HarnessSession } from '../src/lib/agents'

afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

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
    // Design B stays for the minor sources only.
    expect(deskEmptyState({ ...base, unanswered: ['tiers'] })).toBe('incomplete')
    expect(deskEmptyState({ ...base, unanswered: ['projects', 'lessons'] })).toBe('incomplete')
    // A failure only the warning text names still blocks "all clear".
    expect(deskEmptyState({ ...base, failed: true })).toBe('incomplete')
    expect(deskEmptyState({ ...base, scoped: true, unanswered: ['tiers'] })).toBe('scoped')
    expect(deskEmptyState({ ...base, open: 1, unanswered: ['approvals'] })).toBeNull()
    expect(deskUnansweredLine(['approvals'])).toBe('Approvals didn’t answer, so this may be incomplete')
    expect(deskUnansweredLine(['approvals', 'stepups'])).toBe('Some sources didn’t answer, so this may be incomplete')
    expect(deskUnansweredLine(['approvals', 'desk'])).toBe('The desk didn’t answer, so this may be incomplete')
    expect(deskUnansweredLine([])).toBe('Some sources didn’t answer, so this may be incomplete')
  })

  it('never calls the desk clear when the desk, its questions or its approvals did not answer, or the badge counts items', () => {
    const base = { reading: false, open: 0, scoped: false, unanswered: [], failed: false }
    for (const source of ['desk', 'questions', 'approvals'] as const) {
      expect(deskEmptyState({ ...base, unanswered: [source] }), source).toBe('failed')
      expect(deskEmptyState({ ...base, unanswered: ['tiers', source], failed: true }), `${source} with a minor one`).toBe('failed')
      // A narrowed view cannot say nothing waits in it either.
      expect(deskEmptyState({ ...base, scoped: true, unanswered: [source] }), `${source} scoped`).toBe('failed')
    }
    // Badge two, list empty, every source answered: still not clear, and while a read runs it is reading.
    expect(deskEmptyState({ ...base, waiting: 2 })).toBe('failed')
    expect(deskEmptyState({ ...base, waiting: 2, checking: true })).toBe('reading')
    expect(deskEmptyState({ ...base, waiting: 2, unanswered: ['lessons'] })).toBe('failed')
    expect(deskEmptyState({ ...base, waiting: 0 })).toBe('clear')
    expect(deskEmptyState({ ...base, waiting: null })).toBe('clear')
    // The badge counts the whole desk; a narrowed view keeps its own line.
    expect(deskEmptyState({ ...base, scoped: true, waiting: 2 })).toBe('scoped')
    expect(deskEmptyState({ ...base, open: 2, waiting: 2, unanswered: ['desk'] })).toBeNull()
  })

  it('says the count the badge knows, in English and impersonal German', () => {
    expect(deskFailedWords(2).sentence).toBe('2 items are waiting, but the list couldn’t be loaded.')
    expect(deskFailedWords(1).sentence).toBe('1 item is waiting, but the list couldn’t be loaded.')
    expect(deskFailedWords(null).sentence).toBe('The desk couldn’t be loaded.')
    expect(deskFailedWords(0).sentence).toBe('The desk couldn’t be loaded.')
    expect(deskFailedWords(2, true).sentence).toBe('2 Einträge warten, aber die Liste konnte nicht geladen werden.')
    expect(deskFailedWords(1, true).sentence).toBe('1 Eintrag wartet, aber die Liste konnte nicht geladen werden.')
    expect(deskFailedWords(undefined, true).sentence).toBe('Der Desk konnte nicht geladen werden.')
    const en = deskFailedWords(2), de = deskFailedWords(2, true)
    expect([en.retrying, en.exhausted, en.retry, en.stale('19:47')]).toEqual(['Retrying automatically', 'Still not loading. Your items are safe; try again later.', 'Try again', 'Showing the list from 19:47'])
    expect([de.retrying, de.exhausted, de.retry, de.stale('19:47')]).toEqual(['Wird automatisch erneut versucht', 'Lädt weiterhin nicht. Die Einträge bleiben erhalten; später erneut versuchen.', 'Erneut versuchen', 'Liste von 19:47'])
  })

  // Risk: retries stack (one per failure or poll) or never stop; a navigation leaves one running.
  it('retries a failed desk 3, 10 and 30 s apart, one at a time, then hands over to the normal refresh', async () => {
    vi.useFakeTimers()
    expect(DESK_RETRY_MS).toEqual([3_000, 10_000, 30_000])
    let failing = true, reads = 0
    const phases: DeskRetryPhase[] = []
    const retry = deskAutoRetry(async () => { reads++ }, () => failing, phase => phases.push(phase))
    retry.start()
    // A second failure while one retry waits schedules nothing more.
    retry.start()
    expect(phases.at(-1)).toBe('retrying')
    await vi.advanceTimersByTimeAsync(2_999); expect(reads).toBe(0)
    await vi.advanceTimersByTimeAsync(1); expect(reads).toBe(1)
    await vi.advanceTimersByTimeAsync(9_999); expect(reads).toBe(1)
    await vi.advanceTimersByTimeAsync(1); expect(reads).toBe(2)
    await vi.advanceTimersByTimeAsync(29_999); expect(reads).toBe(2)
    await vi.advanceTimersByTimeAsync(1); expect(reads).toBe(3)
    expect(phases.at(-1)).toBe('exhausted')
    expect(retry.pending()).toBe(false)
    await vi.advanceTimersByTimeAsync(600_000); expect(reads).toBe(3)

    // A person's Try again starts the schedule over; a good read ends it.
    retry.start()
    await vi.advanceTimersByTimeAsync(3_000); expect(reads).toBe(4)
    failing = false
    await vi.advanceTimersByTimeAsync(10_000); expect(reads).toBe(5)
    expect(phases.at(-1)).toBe('idle')
    expect(retry.pending()).toBe(false)

    // Leaving the desk cancels the waiting retry, and a read already under way schedules nothing after it.
    failing = true
    retry.start(); retry.stop()
    await vi.advanceTimersByTimeAsync(60_000); expect(reads).toBe(5)
    let release!: () => void
    const slow = deskAutoRetry(() => { reads++; return new Promise<void>(resolve => { release = resolve }) }, () => failing, () => {})
    slow.start()
    await vi.advanceTimersByTimeAsync(3_000); expect(reads).toBe(6)
    slow.stop(); release()
    await vi.advanceTimersByTimeAsync(60_000); expect(reads).toBe(6)
    expect(slow.pending()).toBe(false)
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
    expect(choose(silent)).toBe('failed')
    expect(deskUnansweredLine(silent.unanswered!)).toBe('Approvals didn’t answer, so this may be incomplete')

    // A minor source keeps design B: the picture with a quiet line.
    sources(['/api/key-trim-proposals'])
    const trims = await loadDesk(undefined, {}, {}, empty)
    expect(trims.unanswered).toEqual(['keyTrims'])
    expect(choose(trims)).toBe('incomplete')
    expect(deskUnansweredLine(trims.unanswered!)).toBe('Key trims didn’t answer, so this may be incomplete')

    // The false empty AEON-1406 closes: the canonical desk counts two open rows but lists none.
    // Its warning is no source failure by text, so only the source flag keeps this from "all clear".
    sources()
    const partial = await loadDesk(undefined, {}, {}, { ...empty, counts: { open: 2, held: 0, chores: 0 } })
    expect(partial.items).toEqual([])
    expect(partial.warnings).toContain('The canonical desk is partial or changed while reading. Retry for current coverage.')
    expect(partial.unanswered).toEqual(['desk'])
    expect(choose(partial)).toBe('failed')
    expect(choose(partial, { waiting: 2 })).toBe('failed')

    sources()
    const missing = await loadDesk(undefined, {}, {}, null)
    expect(missing.unanswered).toEqual(['desk'])
    expect(choose(missing)).toBe('failed')
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
