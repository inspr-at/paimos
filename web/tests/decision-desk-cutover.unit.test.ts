// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { createSSRApp, reactive } from 'vue'
import { renderToString } from '@vue/server-renderer'
import AgentChores from '../src/components/agents/AgentChores.vue'
import type { AttachQueueRow } from '../src/lib/attachWatch'
import { useDecisionDesk } from '../src/stores/decisionDesk'
import { useSession } from '../src/stores/session'
import { loadDeskProjection, type DeskProjection } from '../src/lib/decisionDesk'

vi.mock('../src/stores/session', () => ({ useSession: vi.fn() }))
vi.mock('../src/lib/decisionDesk', async original => ({ ...await original<object>(), loadDeskProjection: vi.fn() }))
const page = (count: number): DeskProjection & { truncated: boolean } => ({ items: [], counts: { open: count, held: 0, chores: 0 }, has_more: false, as_of: '', truncated: false })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
const stores: ReturnType<typeof useDecisionDesk>[] = []
afterEach(() => { for (const store of stores.splice(0)) store.$dispose(); vi.clearAllMocks() })
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
  await desk.refresh(); expect(desk.count).toBeNull(); expect(desk.error).toBe('Access changed')
  await desk.refresh(); expect(desk.count).toBe(2); expect(desk.error).toBe('')
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
