// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1406 risk: an empty Open tab says "Nothing waits for you" while a source
// did not answer, so a person stops looking while something is waiting.
// AEON-1631 risk: the badge counts two items while a failed desk read shows the
// all-clear art, and nothing reads the desk again until the next refresh.
// AEON-1700 risk: the sync line claims a check, a countdown or a live stream that
// did not happen, or announces every tick to a screen reader.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { DESK_RETRY_MS, deskAutoRetry, deskEmptyState, deskFailedWords, deskUnansweredLine, type DeskProjection, type DeskRetryPhase } from '../src/lib/decisionDesk'
import { loadDesk, nativeTierAdapter, tierSessionsFromRefresh } from '../src/lib/decisionDeskApi'
import { deskClock, deskClockMoved, deskSyncAnnouncement, deskSyncLine, deskSyncMotion, type DeskSyncInput, type DeskSyncLine } from '../src/lib/deskSync'
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
    expect([en.retrying, en.exhausted, en.retry]).toEqual(['Retrying automatically', 'Still not loading. Your items are safe; try again later.', 'Try again'])
    expect([de.retrying, de.exhausted, de.retry]).toEqual(['Wird automatisch erneut versucht', 'Lädt weiterhin nicht. Die Einträge bleiben erhalten; später erneut versuchen.', 'Erneut versuchen'])

    // AEON-1700 §3/§7: the sync line's words for every state on one frozen clock. Every instant derives from
    // `at`; clock times come from the same formatter as the line, so the zone of the runner never matters.
    const at = Date.UTC(2026, 9, 23, 13, 58, 0)
    const idle: DeskSyncInput = { now: at + 12_000, checkedAt: at, busy: false, change: false, failures: 0, notice: null, stale: false, retry: 'idle',
      online: true, visible: true, live: true, pollAt: at + 30_000, retryAt: null }
    const words = (line: DeskSyncLine, short = false) => line.parts.map(part => 'n' in part ? `${part.n}${part.unit ?? ''}` : short ? part.short ?? part.text : part.text).join('').replace(/\u00a0/g, ' ')
    const line = (over: Partial<DeskSyncInput> = {}, lang: 'en' | 'de' = 'en') => deskSyncLine({ ...idle, ...over }, lang)
    const shown = deskClock(at)
    expect(words(line())).toBe('Live · checked 12 s ago · next check in 18 s')
    expect(words(line(), true), '≤ 520: no "Live ·", "next in"').toBe('checked 12 s ago · next in 18 s')
    expect(words(line({}, 'de'))).toBe('Live · vor 12 s geprüft · nächste Prüfung in 18 s')
    expect(line()).toMatchObject({ state: 'normal', icon: 'dot', live: true, warn: false, retry: false, tip: `Last check ${deskClock(at, true)} · next ${deskClock(at + 30_000, true)}` })
    expect(line().sentence).toBe(`Live. Last check ${deskClock(at, true)}. Next check ${deskClock(at + 30_000, true)}.`)
    expect(words(line({ now: at + 3_000 }))).toBe('Live · checked just now · next check in 27 s')
    expect(words(line({ now: at + 3_000 }, 'de'))).toBe('Live · gerade geprüft · nächste Prüfung in 27 s')
    expect(words(line({ now: at + 3 * 60_000, pollAt: at + 3 * 60_000 + 9_000 }))).toBe('Live · checked 3 min ago · next check in 9 s')
    expect(words(line({ now: at + 59 * 60_000 + 59_000, pollAt: at + 60 * 60_000 }))).toBe('Live · checked 59 min ago · next check in 1 s')
    expect(words(line({ now: at + 2 * 3600_000, pollAt: at + 2 * 3600_000 + 30_000 }))).toBe(`Live · checked at ${shown} · next check in 30 s`)
    expect(words(line({ now: at + 2 * 3600_000, pollAt: at + 2 * 3600_000 + 30_000 }, 'de'))).toBe(`Live · um ${shown} geprüft · nächste Prüfung in 30 s`)
    // Disconnected: "Live" and its dot drop. Hidden: no countdown is invented while polls pause.
    expect(line({ live: false })).toMatchObject({ icon: null, live: false })
    expect(words(line({ live: false }))).toBe('checked 12 s ago · next check in 18 s')
    expect(words(line({ visible: false }))).toBe('Live · checked 12 s ago')
    expect(words(line({ pollAt: null }))).toBe('Live · checked 12 s ago')
    // The checked count reserves "59" on its own. The countdown keeps its unit in the same slot, so the box can reserve "59 s".
    expect(line().parts.filter(part => 'n' in part)).toEqual([{ n: 12 }, { n: 18, unit: ' s' }])
    expect(line({ busy: true })).toMatchObject({ state: 'checking', icon: 'refresh', spin: true })
    expect(words(line({ busy: true }))).toBe('Checking now…')
    expect(words(line({ busy: true }, 'de'))).toBe('Wird geprüft…')
    expect(line({ busy: true, change: true })).toMatchObject({ state: 'change', icon: 'refresh', spin: true })
    expect(words(line({ busy: true, change: true }))).toBe('Change arrived · updating…')
    expect(words(line({ busy: true, change: true }, 'de'))).toBe('Änderung eingetroffen · wird aktualisiert…')
    expect(line({ failures: 1 })).toMatchObject({ state: 'retrying', icon: 'refresh', spin: false, warn: false })
    expect(words(line({ failures: 1 }))).toBe('Checking again…')
    expect(words(line({ failures: 1 }, 'de'))).toBe('Erneuter Versuch…')
    const quiet = { failures: 2, notice: 'quiet', now: at + 20_000 } as const
    expect(line(quiet)).toMatchObject({ state: 'quiet', health: 'quiet', icon: 'alert', warn: true, retry: false })
    expect(line(quiet).parts.filter(part => 'n' in part)).toEqual([{ n: 10, unit: ' s' }])
    expect(words(line(quiet))).toBe(`Couldn’t check · trying again in 10 s · showing ${shown}`)
    expect(words(line(quiet, 'de'))).toBe(`Prüfung fehlgeschlagen · neuer Versuch in 10 s · Stand ${shown}`)
    expect(words(line(quiet), true), '≤ 520').toBe(`Couldn’t check · retry in 10 s · showing ${shown}`)
    // A pending automatic retry comes first when it is due before the poll.
    expect(words(line({ ...quiet, retryAt: at + 23_000 }))).toBe(`Couldn’t check · trying again in 3 s · showing ${shown}`)
    // AEON-1631's kept rows are the same quiet failure while retries run, and escalate once they are spent.
    expect(words(line({ stale: true, retry: 'retrying', retryAt: at + 15_000 }))).toBe(`Couldn’t check · trying again in 3 s · showing ${shown}`)
    expect(line({ stale: true, retry: 'exhausted' }).state).toBe('escalated')
    const escalated = { failures: 5, notice: 'escalated' } as const
    expect(line(escalated)).toMatchObject({ state: 'escalated', icon: 'alert', warn: true, retry: true })
    expect(words(line(escalated))).toBe(`Not updated since ${shown}`)
    expect(words(line(escalated, 'de'))).toBe(`Seit ${shown} nicht aktualisiert`)
    // Try again stays where it is through a later read: escalation wins over checking.
    expect(line({ ...escalated, busy: true }).state).toBe('escalated')
    expect(line({ online: false })).toMatchObject({ state: 'offline', icon: 'cloud-off', warn: false, retry: false })
    expect(words(line({ online: false }))).toBe(`Offline · showing ${shown} · checks resume when you’re back`)
    expect(words(line({ online: false }, 'de'))).toBe(`Offline · Stand ${shown} · Prüfung startet wieder, sobald online`)
    expect(words(line({ online: false }), true), '≤ 520').toBe(`Offline · showing ${shown} · resumes when online`)
    expect(line({ checkedAt: null })).toMatchObject({ state: 'never', icon: 'refresh', spin: false, tip: '' })
    expect(words(line({ checkedAt: null }))).toBe('Checking now…')
    expect(words(line({ checkedAt: null }, 'de'))).toBe('Wird geprüft…')
    // Capture freezes one instant and sets reduced motion. The same instant twice is the same settled sentence.
    const frozen = line({ now: at, checkedAt: at, pollAt: at + 30_000 })
    expect(words(frozen)).toBe(words(line({ now: at, checkedAt: at, pollAt: at + 30_000 })))
    expect(words(frozen)).toBe('Live · checked just now · next check in 30 s')
    expect(frozen.spin, 'a settled line does not turn').toBe(false)
    expect(words(frozen)).not.toBe('Checking now…')
    expect(deskSyncMotion({ reducedMotion: true, spin: true })).toEqual({ spin: false, pulse: false })
    expect(deskSyncMotion({ reducedMotion: false, spin: true })).toEqual({ spin: true, pulse: true })
    expect(deskClockMoved(at, at), 'a frozen clock does not make the poll due').toBe(false)
    expect(deskClockMoved(at, at + 30_000)).toBe(true)
    // §5: failures, escalation and recovery speak once per transition; nothing else does.
    expect(deskSyncAnnouncement('ok', 'ok', shown)).toBeNull()
    expect(deskSyncAnnouncement('ok', 'quiet', shown)).toBe('Couldn’t check the Desk. Trying again.')
    expect(deskSyncAnnouncement('quiet', 'escalated', shown)).toBe(`The Desk hasn’t updated since ${shown}. Try again is available.`)
    expect(deskSyncAnnouncement('escalated', 'quiet', shown)).toBeNull()
    expect(deskSyncAnnouncement('escalated', 'ok', shown)).toBe('The Desk is up to date again.')
    expect(deskSyncAnnouncement('quiet', 'ok', shown, 'de')).toBe('Der Desk ist wieder aktuell.')
  })

  // Risk: retries stack (one per failure or poll) or never stop; a navigation leaves one running.
  it('retries a failed desk 3, 10 and 30 s apart, one at a time, then hands over to the normal refresh', async () => {
    vi.useFakeTimers()
    expect(DESK_RETRY_MS).toEqual([3_000, 10_000, 30_000])
    let failing = true, reads = 0
    const phases: DeskRetryPhase[] = [], dues: (number | undefined)[] = []
    const retry = deskAutoRetry(async () => { reads++ }, () => failing, (phase, dueAt) => { phases.push(phase); dues.push(dueAt) })
    const startedAt = Date.now()
    retry.start()
    // A second failure while one retry waits schedules nothing more.
    retry.start()
    expect(phases.at(-1)).toBe('retrying')
    // AEON-1700: each scheduled retry names its own timer's due time; the sync line counts down to it.
    expect(dues.at(-1)).toBe(startedAt + 3_000)
    await vi.advanceTimersByTimeAsync(2_999); expect(reads).toBe(0)
    await vi.advanceTimersByTimeAsync(1); expect(reads).toBe(1)
    expect(dues.at(-1)).toBe(startedAt + 3_000 + 10_000)
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
