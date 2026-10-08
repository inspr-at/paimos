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
  expect(listMessages).toHaveBeenNthCalledWith(1, 'project', { session: 'first', limit: 200 })
  expect(listMessages).toHaveBeenNthCalledWith(2, 'project', { session: 'second', limit: 200 })
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
