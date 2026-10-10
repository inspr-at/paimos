// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { listMessages, type HarnessSession, type MessagePage, type ProjectMessage } from '../src/lib/agents'
import { useAgents } from '../src/stores/agents'

vi.mock('../src/lib/agents', async original => ({ ...await original<typeof import('../src/lib/agents')>(), listMessages: vi.fn() }))
vi.mock('../src/stores/projects', () => ({ useProjects: () => ({ byId: () => undefined }) }))
beforeEach(() => { setActivePinia(createPinia()); vi.mocked(listMessages).mockReset() })
const session = (id: string) => ({ id, project_id: 'project', agent_principal_id: 'shared-agent' }) as HarnessSession
const page = (...events: number[]): MessagePage => ({ items: events.map(event => ({ id: String(event), sent_event_id: event }) as ProjectMessage), next_after: 0, preamble: '' })

it('filters before limiting and keeps two sessions of one principal separate', async () => {
  const store = useAgents()
  vi.mocked(listMessages).mockResolvedValueOnce(page(3, 1, 2)).mockResolvedValueOnce(page(7))
  await store.refreshThread('project', 'first')
  await store.refreshThread('project', 'second')
  expect(listMessages).toHaveBeenNthCalledWith(1, 'project', { session: 'first', limit: 50 })
  expect(listMessages).toHaveBeenNthCalledWith(2, 'project', { session: 'second', limit: 50 })
  expect(store.thread(session('first')).map(m => m.id)).toEqual(['1', '2', '3'])
  expect(store.thread(session('second')).map(m => m.id)).toEqual(['7'])
})

it('a slow poll cannot overwrite the reply loaded after a send', async () => {
  const store = useAgents()
  let finish!: (value: MessagePage) => void
  vi.mocked(listMessages).mockImplementationOnce(() => new Promise(resolve => { finish = resolve })).mockResolvedValueOnce(page(2, 1))
  const stale = store.refreshThread('project', 'first')
  await store.refreshThread('project', 'first')
  finish(page(1)); await stale
  expect(store.thread(session('first')).map(m => m.id)).toEqual(['1', '2'])
})

// Risk: a burst of hints while a poll is outstanding either waits on that poll
// or starts one read per hint. One catch-up starts at once; the rest share it
// and leave a single trailing read. The slow page is not the thread that remains.
it('a burst during a slow poll starts one catch-up and one trailing read', async () => {
  const store = useAgents()
  let finish!: (value: MessagePage) => void
  vi.mocked(listMessages)
    .mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    .mockResolvedValueOnce(page(1))
    .mockResolvedValueOnce(page(3, 2, 1))
  const stale = store.refreshThread('project', 'first')
  const catchUp = store.refreshThread('project', 'first')
  void store.refreshThread('project', 'first')
  // The extra hint joins the catch-up. It does not start another read, and the
  // catch-up does not wait for the slow poll.
  expect(listMessages).toHaveBeenCalledTimes(2)
  finish(page(1))
  await catchUp
  await stale
  expect(listMessages).toHaveBeenCalledTimes(3)
  expect(store.thread(session('first')).map(m => m.id)).toEqual(['1', '2', '3'])
})

it('a denied session read clears its thread without changing another thread', async () => {
  const { APIError } = await import('../src/lib/api')
  const store = useAgents()
  vi.mocked(listMessages).mockResolvedValueOnce(page(1)).mockResolvedValueOnce(page(2))
  await store.refreshThread('project', 'first'); await store.refreshThread('project', 'second')
  vi.mocked(listMessages).mockRejectedValueOnce(new APIError(403, 'forbidden', {}))
  await store.refreshThread('project', 'first')
  expect(store.thread(session('first'))).toEqual([])
  expect(store.threadState(session('first'))).toBe('forbidden')
  expect(store.thread(session('second')).map(m => m.id)).toEqual(['2'])
  expect(store.threadState(session('second'))).toBe('ready')
})

it('an authentication reset drops both stored messages and an older response', async () => {
  const { resetPositions } = await import('../src/lib/position')
  const store = useAgents()
  let finish!: (value: MessagePage) => void
  let finishCatchUp!: (value: MessagePage) => void
  vi.mocked(listMessages)
    .mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    .mockImplementationOnce(() => new Promise(resolve => { finishCatchUp = resolve }))
    .mockResolvedValueOnce(page(2))
  const stale = store.refreshThread('project', 'first')
  const catchUp = store.refreshThread('project', 'first')
  resetPositions()
  expect(store.thread(session('first'))).toEqual([])
  await store.refreshThread('project', 'first')
  expect(listMessages).toHaveBeenCalledTimes(3)
  expect(store.thread(session('first')).map(m => m.id)).toEqual(['2'])
  finish(page(1)); finishCatchUp(page(1)); await Promise.all([stale, catchUp])
  expect(store.thread(session('first')).map(m => m.id)).toEqual(['2'])
})

// Risk: newest refreshes must retain paged history, and responses belonging
// to a previous account must never repopulate it.
it('AEON-976 retains earlier keyset pages across newest refreshes and drops paging after auth reset', async () => {
  const store = useAgents()
  vi.mocked(listMessages).mockResolvedValueOnce(page(...Array.from({ length: 50 }, (_, i) => 100 + i)))
  await store.refreshThread('project', 'first')
  expect(store.threadMore.first).toBe(true)
  vi.mocked(listMessages).mockResolvedValueOnce(page(99, 98))
  await store.earlierThread('project', 'first')
  expect(listMessages).toHaveBeenLastCalledWith('project', { session: 'first', limit: 50, after: 100 })
  expect(store.threadMore.first).toBe(false)
  vi.mocked(listMessages).mockResolvedValueOnce(page(150, 149))
  await store.refreshThread('project', 'first')
  expect(store.thread(session('first')).map(m => m.sent_event_id)).toEqual(Array.from({ length: 53 }, (_, i) => 98 + i))
  expect(store.threadMore.first).toBe(false)
  const { resetPositions } = await import('../src/lib/position')
  resetPositions()
  vi.mocked(listMessages).mockResolvedValueOnce(page(...Array.from({ length: 50 }, (_, i) => 200 + i)))
  await store.refreshThread('project', 'first')
  let finish!: (value: MessagePage) => void
  vi.mocked(listMessages).mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
  const older = store.earlierThread('project', 'first')
  resetPositions()
  finish(page(199, 198)); await older
  expect(store.thread(session('first'))).toEqual([])
  expect(store.threadMore.first).toBeUndefined()
})

// Risk: a reconnect or a hidden tab can accrue more than a newest page.
it('AEON-976 fills a multi-page reconnect gap without dropping loaded history', async () => {
  const store = useAgents()
  const events = (start: number, count: number) => Array.from({ length: count }, (_, i) => start + i)
  vi.mocked(listMessages).mockResolvedValueOnce(page(...events(1, 50)))
  await store.refreshThread('project', 'first')
  vi.mocked(listMessages)
    .mockResolvedValueOnce(page(...events(1, 50)))
    .mockResolvedValueOnce(page(...events(51, 50)))
    .mockResolvedValueOnce(page(...events(101, 25)))
  await store.refreshThread('project', 'first')
  expect(listMessages).toHaveBeenNthCalledWith(2, 'project', { session: 'first', limit: 50, newest_first: false, after: 0 })
  expect(listMessages).toHaveBeenNthCalledWith(3, 'project', { session: 'first', limit: 50, newest_first: false, after: 50 })
  expect(listMessages).toHaveBeenNthCalledWith(4, 'project', { session: 'first', limit: 50, newest_first: false, after: 100 })
  expect(store.thread(session('first')).map(m => m.sent_event_id)).toEqual(events(1, 125))
})

// Risk: a paging denial must not leave cached bodies visible until a poll.
it('AEON-976 clears only the denied history when project access is revoked during paging', async () => {
  const { APIError } = await import('../src/lib/api')
  const store = useAgents()
  vi.mocked(listMessages)
    .mockResolvedValueOnce(page(...Array.from({ length: 50 }, (_, i) => i + 1)))
    .mockResolvedValueOnce(page(100))
  await store.refreshThread('project', 'first'); await store.refreshThread('project', 'second')
  vi.mocked(listMessages).mockRejectedValueOnce(new APIError(403, 'forbidden', {}))
  await expect(store.earlierThread('project', 'first')).rejects.toMatchObject({ status: 403 })
  expect(store.thread(session('first'))).toEqual([])
  expect(store.threadState(session('first'))).toBe('forbidden')
  expect(store.threadMore.first).toBe(false)
  expect(store.thread(session('second')).map(m => m.id)).toEqual(['100'])
})
