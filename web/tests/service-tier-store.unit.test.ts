// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import { useServiceTiers } from '../src/stores/serviceTiers'
import { changeServiceTier, readServiceTier } from '../src/lib/agentRows'
import type { HarnessSession } from '../src/lib/agents'
import type { TierState } from '../src/lib/serviceTier'

const auth = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } })
const ownership = { daemon_id: 'fixture', generation: 'generation', process_id: 'process', root_pid: 10, group_id: 10, started_at: '2026-10-02T00:00:00Z' }
const s = { id: 's', project_id: 'p', model: 'fixture', harness: 'codex', phase: 'working', management_mode: 'managed', run_id: 'run', advertised_capabilities: ['stop', 'service_tier_v1'], service_tier: 'default', service_tier_revision: 3, process_ownership: ownership, process_observed_at: new Date().toISOString() } as HarnessSession
const fresh = (): TierState => ({ session_id: 's', revision: 3, active_tier: 'default', pending: null, read_only: false, reports: [], requests: [] })
vi.mock('../src/stores/session', () => ({ useSession: () => auth }))
vi.mock('../src/stores/agents', () => ({ useAgents: () => ({ sessionById: () => s, refreshSessions: vi.fn().mockResolvedValue(undefined) }) }))
vi.mock('../src/lib/authz', () => ({ can: () => true }))
vi.mock('../src/lib/toast', () => ({ toast: vi.fn() }))
vi.mock('../src/lib/agentRows', () => ({ changeServiceTier: vi.fn(), readServiceTier: vi.fn(), decideServiceTier: vi.fn(), askServiceTier: vi.fn() }))
beforeEach(() => { setActivePinia(createPinia()); auth.identity.principal.id = 'person'; vi.clearAllMocks(); vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers() })

it('drops a late answer after identity changes and clears per-person Undo and drafts', async () => {
  const store = useServiceTiers()
  let finish!: (s: TierState) => void
  vi.mocked(readServiceTier).mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  const read = store.load(s)
  auth.identity.principal.id = 'another-person'
  finish(fresh()); await read
  expect(store.states).toEqual({}); expect(store.dialog).toBeNull(); expect(store.undo).toBeNull()
})
it('an older read cannot replace the accepted revision of a write', async () => {
  const store = useServiceTiers()
  let finish!: (s: TierState) => void
  vi.mocked(readServiceTier).mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  const read = store.load(s)
  vi.mocked(changeServiceTier).mockImplementationOnce(async (_project, _session, body) => ({ ...fresh(), revision: 4, pending: { id: body.request_id, session_id: 's', kind: 'tier', value: 'fast', state: 'pending', outcome: null, reason: null } }))
  expect(await store.change(s, 'Fixture', 'fast', { withUndo: true })).toBe(true)
  finish(fresh()); await read
  expect(store.state(s).revision).toBe(4)
  expect(store.undo).toMatchObject({ session: 's', project: 'p', from: 'default', to: 'fast', revision: 4, ownership })
  expect(vi.mocked(changeServiceTier).mock.calls[0]![2]).toMatchObject({ tier: 'fast', expected_revision: 3, expected_ownership: ownership })
})
it('a failed write never creates a success toast or Undo receipt', async () => {
  const store = useServiceTiers()
  vi.mocked(changeServiceTier).mockRejectedValueOnce(new Error('offline'))
  vi.mocked(readServiceTier).mockResolvedValueOnce(fresh())
  expect(await store.change(s, 'Fixture', 'fast', { withUndo: true })).toBe(false)
  expect(store.undo).toBeNull(); expect(store.errors.s).toBe('offline')
})
it('a mutation response after an identity change cannot restore state or Undo', async () => {
  const store = useServiceTiers()
  let finish!: (s: TierState) => void
  vi.mocked(changeServiceTier).mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  const write = store.change(s, 'Fixture', 'fast', { withUndo: true })
  auth.identity.principal.id = 'another-person'
  finish({ ...fresh(), revision: 4 }); await write
  expect(store.states).toEqual({}); expect(store.undo).toBeNull(); expect(store.busy).toEqual({})
})

it('an unknown write result blocks another mutation until a successful read', async () => {
  const store = useServiceTiers()
  vi.mocked(changeServiceTier).mockRejectedValueOnce(new Error('response lost'))
  vi.mocked(readServiceTier).mockRejectedValueOnce(new Error('offline'))
  expect(await store.change(s, 'Fixture', 'fast')).toBe(false)
  expect(store.unavailable(s)).toContain('previous tier outcome is unknown')
  expect(await store.change(s, 'Fixture', 'fast')).toBe(false)
  expect(changeServiceTier).toHaveBeenCalledTimes(1)
  vi.mocked(readServiceTier).mockResolvedValueOnce(fresh())
  await store.load(s)
  expect(store.unavailable(s)).toBe('')
})
it('Undo refuses another switch instead of overwriting it', async () => {
  const store = useServiceTiers()
  vi.mocked(changeServiceTier).mockImplementationOnce(async (_project, _session, body) => ({ ...fresh(), revision: 4, pending: { id: body.request_id, session_id: 's', kind: 'tier', value: 'fast', state: 'pending', outcome: null, reason: null } }))
  await store.change(s, 'Fixture', 'fast', { withUndo: true })
  vi.mocked(readServiceTier).mockResolvedValueOnce({ ...fresh(), revision: 6, active_tier: 'fastest' })
  await store.undoChange()
  expect(changeServiceTier).toHaveBeenCalledTimes(1)
})
