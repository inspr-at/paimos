// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createSSRApp, reactive } from 'vue'
import { renderToString } from '@vue/server-renderer'
import AgentChores from '../src/components/agents/AgentChores.vue'
import type { AttachQueueRow } from '../src/lib/attachWatch'
import { useDecisionDesk } from '../src/stores/decisionDesk'
import { useSession } from '../src/stores/session'
import { deskRefreshNotice, loadDeskProjection, retainDecidedLessons, type DeskItem, type DeskProjection } from '../src/lib/decisionDesk'
import { decisionPermission, emptySources } from '../src/lib/decisionDeskApi'
import { onProjectionWake } from '../src/lib/agents'
import { APIError } from '../src/lib/api'
import type { MethodLearning } from '../src/lib/knowledge'

vi.mock('../src/stores/session', () => ({ useSession: vi.fn() }))
vi.mock('../src/lib/decisionDesk', async original => ({ ...await original<object>(), loadDeskProjection: vi.fn() }))
vi.mock('../src/lib/agents', async original => ({ ...await original<object>(), onProjectionWake: vi.fn(() => () => {}) }))
const page = (count: number): DeskProjection & { truncated: boolean } => ({ items: [], counts: { open: count, held: 0, chores: 0 }, has_more: false, as_of: '', truncated: false })
function deferred<T>() { let resolve!: (value: T) => void, reject!: (cause: unknown) => void; const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail }); return { promise, resolve, reject } }
const stores: ReturnType<typeof useDecisionDesk>[] = []
afterEach(() => { for (const store of stores.splice(0)) store.$dispose(); vi.clearAllMocks(); vi.useRealTimers() })
function setup() {
  setActivePinia(createPinia())
  const session = reactive({ identity: null as null | { tenant: { id: string }; principal: { id: string; kind: string } }, authenticationCurrent: () => true })
  vi.mocked(useSession).mockReturnValue(session as never)
  const desk = useDecisionDesk(); stores.push(desk)
  return { session, desk }
}
it('drops a previous person read even if transport ignores abort, and resets the count synchronously', async () => {
  const previous = deferred<ReturnType<typeof page>>(), next = deferred<ReturnType<typeof page>>()
  vi.mocked(loadDeskProjection).mockReturnValueOnce(previous.promise).mockReturnValueOnce(next.promise)
  const { session, desk } = setup()
  session.identity = { tenant: { id: 'tenant' }, principal: { id: 'first', kind: 'person' } }
  const oldRead = desk.refresh()
  session.identity = { tenant: { id: 'tenant' }, principal: { id: 'second', kind: 'person' } }
  const newRead = desk.refresh()
  expect(desk.count).toBeNull()
  previous.resolve(page(77)); await oldRead
  expect(desk.count).toBeNull()
  next.resolve(page(3)); await newRead
  expect(desk.count).toBe(3)
  expect(loadDeskProjection).toHaveBeenCalledTimes(2)
})
it('reports an unknown count on failed refresh and retries without claiming an empty desk', async () => {
  vi.mocked(loadDeskProjection).mockResolvedValueOnce(page(8)).mockRejectedValueOnce(new Error('Access changed')).mockResolvedValueOnce(page(2))
  const { session, desk } = setup()
  session.identity = { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }
  await desk.refresh(); expect(desk.count).toBe(8)
  // AEON-1477 retains transient failures; a permission reset still loses the
  // old authority and must never present its count as a fresh empty desk.
  desk.invalidate()
  await desk.refresh(); expect(desk.count).toBeNull(); expect(desk.error).toBe('Access changed')
  await desk.refresh(); expect(desk.count).toBe(2); expect(desk.error).toBe('')
})

it('AEON-1477 retains the last good projection through 429, 500 and abort, resetting failures only on success', async () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-17T13:58:00Z'))
  const row: DeskProjection['items'][number] = { id: 'pending-1477', kind: 'question', revision: 1, title: 'Which index?', held: true, created_at: '2026-10-17T13:00:00Z', href: '/decision-desk?item=q:pending-1477', source: '/api/questions/pending-1477' }
  const good = { ...page(1), items: [row] }, aborted = deferred<typeof good>()
  vi.mocked(loadDeskProjection).mockResolvedValueOnce(good)
  const { session, desk } = setup()
  session.identity = { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }
  await desk.refresh()
  const shown = desk.projection, at = desk.refreshedAt
  for (const status of [429, 500]) {
    vi.mocked(loadDeskProjection).mockRejectedValueOnce(new Error(`Decision Desk could not be read (${status}).`))
    await desk.refresh()
    expect(desk.projection).toBe(shown); expect(desk.projection?.items).toEqual([row]); expect(desk.count).toBe(1); expect(desk.error).toBe('')
    expect(desk.failures).toBe(status === 429 ? 1 : 2); expect(desk.refreshedAt).toBe(at)
  }
  const since = desk.failedSince
  vi.mocked(loadDeskProjection).mockRejectedValueOnce(new DOMException('Cancelled', 'AbortError'))
  await desk.refresh()
  expect(desk.failures).toBe(2); expect(desk.failedSince).toBe(since); expect(desk.projection).toBe(shown); expect(desk.error).toBe('')
  vi.mocked(loadDeskProjection).mockRejectedValueOnce(new APIError(499, 'Client cancelled'))
  await desk.refresh()
  expect(desk.failures).toBe(2); expect(desk.failedSince).toBe(since); expect(desk.projection).toBe(shown); expect(desk.error).toBe('')
  vi.mocked(loadDeskProjection).mockReturnValueOnce(aborted.promise)
  const pending = desk.refresh()
  const signal = vi.mocked(loadDeskProjection).mock.lastCall![0]!
  desk.cancelRefresh()
  expect(signal.aborted).toBe(true)
  aborted.reject(new Error('Transport reports a generic cancel'))
  expect(await pending).toBe(false)
  expect(desk.failures).toBe(2); expect(desk.projection).toBe(shown)
  vi.mocked(loadDeskProjection).mockResolvedValueOnce(page(3))
  expect(await desk.refresh()).toBe(true)
  expect(desk.count).toBe(3); expect(desk.failures).toBe(0); expect(desk.failedSince).toBeNull()
})

it('AEON-1477 coalesces a two-second burst of twenty live events into one trailing read, and Try again reads at once', async () => {
  vi.useFakeTimers()
  const trailing = deferred<ReturnType<typeof page>>()
  vi.mocked(loadDeskProjection).mockResolvedValueOnce(page(1)).mockReturnValueOnce(trailing.promise).mockResolvedValue(page(3))
  const { session, desk } = setup()
  // Opening the Desk (a new person) reads at once, outside any event window.
  session.identity = { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }
  expect(loadDeskProjection).toHaveBeenCalledTimes(1)
  await vi.advanceTimersByTimeAsync(0); expect(desk.count).toBe(1)
  const wake = vi.mocked(onProjectionWake).mock.lastCall![0]
  for (let event = 0; event < 20; event++) { wake('question.asked'); await vi.advanceTimersByTimeAsync(100) }
  await vi.advanceTimersByTimeAsync(999)
  expect(loadDeskProjection).toHaveBeenCalledTimes(1)
  await vi.advanceTimersByTimeAsync(1)
  expect(loadDeskProjection).toHaveBeenCalledTimes(2)
  // An event while the trailing read runs is not lost: it opens the next window.
  wake('question.asked')
  trailing.resolve(page(2)); await vi.advanceTimersByTimeAsync(0); expect(desk.count).toBe(2)
  await vi.advanceTimersByTimeAsync(2999)
  expect(loadDeskProjection).toHaveBeenCalledTimes(2)
  await vi.advanceTimersByTimeAsync(1)
  expect(loadDeskProjection).toHaveBeenCalledTimes(3)
  await vi.advanceTimersByTimeAsync(10_000)
  expect(loadDeskProjection).toHaveBeenCalledTimes(3)
  // Try again does not wait for the window; its read covers the pending event.
  wake('question.asked'); await vi.advanceTimersByTimeAsync(500)
  expect(await desk.refresh()).toBe(true)
  expect(loadDeskProjection).toHaveBeenCalledTimes(4)
  await vi.advanceTimersByTimeAsync(10_000)
  expect(loadDeskProjection).toHaveBeenCalledTimes(4)
  // A delayed live wake cannot repopulate person-owned data after reset.
  wake('question.asked'); desk.invalidate(); await vi.advanceTimersByTimeAsync(3000)
  expect(loadDeskProjection).toHaveBeenCalledTimes(4); expect(desk.projection).toBeNull()
})

it('AEON-1477 keeps a cancelled first load quiet and a genuine first failure unknown', async () => {
  vi.mocked(loadDeskProjection).mockRejectedValueOnce(new DOMException('Cancelled', 'AbortError'))
  const { session, desk } = setup()
  session.identity = { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } }
  await desk.refresh()
  expect(desk.count).toBeNull(); expect(desk.error).toBe(''); expect(desk.failures).toBe(0)
  vi.mocked(loadDeskProjection).mockRejectedValueOnce(new Error('Decision Desk could not be read (500).'))
  await desk.refresh()
  expect(desk.count).toBeNull(); expect(desk.error).toContain('(500)'); expect(desk.failures).toBe(1)
})

it('AEON-1477 keeps a decided lesson learning so Undo can re-check it after the pending list drops it', () => {
  const learning: MethodLearning = {
    id: 'n-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', source: 'ticket', node_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', key: 'AEON-1', title: 'Recovery failed during release',
    text: 'The recovery check was missing.', href: '/p/AEON/AEON-1', author: null, at: '2026-10-14T08:00:00Z',
    recommendation: { decision: 'accept', lesson: 'Rehearse recovery before releasing.', knowledge_id: 'k-aeon-deploy', by: { id: 'scout', name: 'Scout' }, at: '2026-10-14T08:30:00Z', event_id: 700, prepared: true, stale: false, target_missing: false },
  }
  const decided: DeskItem = {
    id: 'l:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', kind: 'lesson', title: learning.recommendation!.lesson!, projectId: 'p-aeon', projectName: 'Paimos Aeon',
    context: learning.recommendation!.source_summary ?? '', findings: '', meanwhile: '', destination: 'Changelog', choices: [], why: '', outcome: 'once', suggestion: 'Accept or Dismiss the prepared lesson.',
    revision: 700, createdAt: learning.at, held: false, decided: true, undoEvent: 800, lesson: { learning, outdated: false },
  }
  const stillThere: DeskItem = { ...decided, id: 'l:still-there' }
  const open: DeskItem = { ...decided, id: 'l:still-open', decided: false, undoEvent: undefined }
  const next: DeskItem[] = [stillThere]
  const lessons = new Map<string, MethodLearning>()
  retainDecidedLessons([open, decided, stillThere], next, lessons)
  expect(next.map(item => item.id)).toEqual([stillThere.id, decided.id])
  expect(lessons.get(decided.id)).toBe(learning)
  expect(lessons.has(open.id)).toBe(false)
  expect(lessons.has(stillThere.id)).toBe(false)
  const sources = emptySources()
  expect(decisionPermission(decided, sources, () => true)).toBe(false)
  sources.lessons.set(decided.id, lessons.get(decided.id)!)
  expect(decisionPermission(decided, sources, () => true)).toBe(true)
})

it('AEON-1477 shows the hint after two failures and escalates strictly after sixty seconds or at five failures', () => {
  const good = Date.parse('2026-10-17T13:58:00Z'), since = good + 1000
  expect(deskRefreshNotice(1, since, good, since + 90_000)).toBeNull()
  expect(deskRefreshNotice(2, since, null, since + 90_000)).toBeNull()
  expect(deskRefreshNotice(2, since, good, since + 60_000)).toBe('quiet')
  expect(deskRefreshNotice(2, since, good, since + 60_001)).toBe('escalated')
  expect(deskRefreshNotice(4, since, good, since)).toBe('quiet')
  expect(deskRefreshNotice(5, since, good, since)).toBe('escalated')
  expect(deskRefreshNotice(0, null, good, since + 90_000)).toBeNull()
})

// Risk: the connection adapter must retain consent scope, expiry and receipts.
it('connection chores preserve consent scope, exact expiry and readable history', async () => {
  vi.mocked(useSession).mockReturnValue({ identity: null } as never)
  const row: AttachQueueRow = { outcome: 'waiting', what: 'Codex on the review Mac', detail: 'Wants to watch the conversation', left: 'Expires in 2m', soon: false, ticket: { key: 'AEON-569', title: 'Decision Desk cutover' },
    review: { request_id: 'connection-source', request_digest: 'a'.repeat(64), state: 'pending', expires_at: '2026-10-09T12:02:00Z', snapshot: { computer_id: 'computer', project_id: 'project', ticket_id: 'ticket', host: 'the review Mac', harness: 'codex', transcript: '/fixture/session.jsonl', file_id: 'fixture', process: { pid: 1234, uid: 501, started: '2026-10-09T12:00:00Z', executable: '/fixture/codex', cwd: '/fixture' } } } }
  const render = (connection: AttachQueueRow) => renderToString(createSSRApp(AgentChores, { signins: [], attaches: [connection], history: [{ ...row, outcome: 'declined' }] }))
  const conversation = await render(row)
  expect(conversation).toContain('and share its conversation')
  expect(conversation).toContain('AEON-569')
  expect(conversation).toContain('Decision Desk cutover')
  expect(conversation).toContain('Your terminal on the review Mac waits')
  expect(conversation).toContain('datetime="2026-10-09T12:02:00Z"')
  expect(conversation).toContain('Expires in 2m')
  expect(conversation).toContain('Connection history')
  expect(conversation).toContain('Declined')
  const statusOnly = await render({ ...row, review: { ...row.review, snapshot: { ...row.review.snapshot, mode: 'lease' } } })
  expect(statusOnly).toContain('Status only')
  expect(statusOnly).not.toContain('share its conversation')
})
