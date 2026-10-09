// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { reactive } from 'vue'
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
