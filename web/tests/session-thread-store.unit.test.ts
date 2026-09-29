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
