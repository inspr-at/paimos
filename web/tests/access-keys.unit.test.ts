// SPDX-License-Identifier: AGPL-3.0-only
// AEON-785: the footer and the Agents tab share one key cache. Settling access
// after a revoke must refresh it, and a late answer must not put the old key back.
import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import type { AgentKey } from '../src/lib/settings'
import type { Members } from '../src/lib/access'

const session = vi.hoisted(() => ({ requiresSignIn: false, identity: { tenant: { id: 't1' }, principal: { id: 'p1', kind: 'person' as const } } }))
vi.mock('../src/stores/session', () => ({ useSession: () => session }))
vi.mock('../src/lib/settings', async original => ({ ...await original<typeof import('../src/lib/settings')>(), listAgentKeys: vi.fn() }))
vi.mock('../src/lib/access', async original => ({
  ...await original<typeof import('../src/lib/access')>(),
  getRegistry: vi.fn(),
  getRoles: vi.fn(),
  getMembers: vi.fn(),
}))
vi.mock('../src/lib/authz', async original => ({ ...await original<typeof import('../src/lib/authz')>(), accessChanged: vi.fn() }))

import { listAgentKeys } from '../src/lib/settings'
import { getMembers, getRegistry, getRoles } from '../src/lib/access'
import { accessChanged } from '../src/lib/authz'
import { useAccess } from '../src/stores/access'

const key = (revoked: string | null): AgentKey => ({
  id: 'k1', principal_id: 'agent', name: 'cli', prefix: 'aeon', scopes: [],
  created_at: '2026-10-01T00:00:00Z', expires_at: null, last_used_at: null, revoked_at: revoked,
})
const members = { people: [], agents: [], invites: [], imported: [], owner_count: 1 } as Members

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  session.requiresSignIn = false
  vi.mocked(getRegistry).mockResolvedValue([])
  vi.mocked(getRoles).mockResolvedValue([])
  vi.mocked(getMembers).mockResolvedValue(members)
  vi.mocked(accessChanged).mockResolvedValue()
})

it('refreshes the shared key cache when access settles after a key change', async () => {
  vi.mocked(listAgentKeys).mockResolvedValue([key(null)])
  const store = useAccess()
  await store.loadKeys()
  expect(store.keys?.map(item => item.revoked_at)).toEqual([null])
  vi.mocked(listAgentKeys).mockResolvedValue([key('2026-10-06T00:00:00Z')])
  await store.settle()
  expect(store.keys?.map(item => item.revoked_at)).toEqual(['2026-10-06T00:00:00Z'])
})

it('drops a stale key read and keeps the last keys when the newest read fails', async () => {
  vi.mocked(listAgentKeys).mockResolvedValue([key(null)])
  const store = useAccess()
  await store.loadKeys()
  let finishStale!: (keys: AgentKey[]) => void
  vi.mocked(listAgentKeys).mockReturnValueOnce(new Promise(resolve => { finishStale = resolve }))
  const stale = store.loadKeys()
  vi.mocked(listAgentKeys).mockRejectedValueOnce(new Error('The agent keys could not be loaded.'))
  await expect(store.loadKeys()).rejects.toThrow('The agent keys could not be loaded.')
  expect(store.keys?.map(item => item.revoked_at)).toEqual([null])
  finishStale([key('2026-10-06T00:00:00Z')])
  await stale
  expect(store.keys?.map(item => item.revoked_at)).toEqual([null])
})
