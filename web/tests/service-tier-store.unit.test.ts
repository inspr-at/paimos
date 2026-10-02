// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { createPinia, disposePinia, getActivePinia, setActivePinia } from 'pinia'
import { useServiceTiers } from '../src/stores/serviceTiers'
import { changeServiceTier, readServiceTier } from '../src/lib/agentRows'
import type { HarnessSession } from '../src/lib/agents'
import type { TierState } from '../src/lib/serviceTier'
import { toast } from '../src/lib/toast'

const auth = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'person', kind: 'person' } } })
const ownership = { daemon_id: 'fixture', generation: 'generation', process_id: 'process', root_pid: 10, group_id: 10, started_at: '2026-10-02T00:00:00Z' }
const s = { id: 's', project_id: 'p', model: 'fixture', harness: 'codex', phase: 'working', management_mode: 'managed', run_id: 'run', advertised_capabilities: ['stop', 'service_tier_v1'], service_tier: 'default', service_tier_revision: 3, process_ownership: ownership, process_observed_at: new Date().toISOString() } as HarnessSession
const fresh = (): TierState => ({ session_id: 's', revision: 3, active_tier: 'default', pending: null, read_only: false, reports: [], requests: [] })
vi.mock('../src/stores/session', () => ({ useSession: () => auth }))
const agents = reactive({ sessions: [s], sessionById: () => s, refreshSessions: vi.fn().mockResolvedValue(undefined) })
vi.mock('../src/stores/agents', () => ({ useAgents: () => agents }))
vi.mock('../src/lib/authz', () => ({ can: () => true }))
vi.mock('../src/lib/toast', () => ({ toast: vi.fn() }))
vi.mock('../src/lib/agentRows', () => ({ changeServiceTier: vi.fn(), readServiceTier: vi.fn(), decideServiceTier: vi.fn(), askServiceTier: vi.fn() }))
let doc: EventTarget & { visibilityState: string }
beforeEach(() => {
  setActivePinia(createPinia()); auth.identity.principal.id = 'person'; vi.clearAllMocks(); vi.useFakeTimers()
  s.service_tier_revision = 3; s.process_observed_at = new Date().toISOString()
  doc = Object.assign(new EventTarget(), { visibilityState: 'visible' })
  vi.stubGlobal('document', doc)
})
afterEach(() => { disposePinia(getActivePinia()!); vi.useRealTimers(); vi.unstubAllGlobals() })

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

async function queue(store: ReturnType<typeof useServiceTiers>, withUndo = true) {
  vi.mocked(changeServiceTier).mockImplementationOnce(async (_project, _session, body) => ({ ...fresh(), revision: 4, pending: { id: body.request_id, session_id: 's', kind: 'tier', value: 'fast', state: 'pending', outcome: null, reason: null } }))
  expect(await store.change(s, 'Fixture', 'fast', { withUndo })).toBe(true)
  return store.state(s)
}
// The same behavioral assertions run against e2d47dcf, before page leases
// existed: that store polls globally and fails the lifecycle assertions below.
const watchPage = (store: ReturnType<typeof useServiceTiers>) => store.watchPage?.() ?? (() => {})

for (const reason of ['vendor_rejected', 'control_expired', 'authorization_revoked', null]) it(`reports a cleared pending change (${reason ?? 'older server'}) and removes Undo`, async () => {
  const store = useServiceTiers(); watchPage(store)
  const pending = await queue(store)
  vi.mocked(readServiceTier).mockResolvedValueOnce({ ...fresh(), revision: 5, ...(reason ? { last_change: { ...pending.pending!, state: 'completed', outcome: 'rejected', reason } } : {}) })
  await vi.advanceTimersByTimeAsync(1200)
  expect(store.undo).toBeNull()
  expect(store.errors.s).toContain('Change to Fast was not applied')
  if (reason) expect(store.errors.s).toContain(reason.replaceAll('_', ' '))
  expect(toast).toHaveBeenCalledWith(expect.stringContaining('Change to Fast was not applied'), { tone: 'error' })
})
it('approval without an Undo receipt still reports rejection', async () => {
  const store = useServiceTiers(); watchPage(store)
  await queue(store, false)
  vi.mocked(readServiceTier).mockResolvedValueOnce({ ...fresh(), revision: 5 })
  await vi.advanceTimersByTimeAsync(1200)
  expect(store.errors.s).toContain('was not applied')
})
it('successful pending Undo does not report its intentional cancellation as a rejection', async () => {
  const store = useServiceTiers()
  const pending = await queue(store)
  vi.mocked(readServiceTier).mockResolvedValueOnce(pending)
  vi.mocked(changeServiceTier).mockResolvedValueOnce({ ...fresh(), revision: 5 })
  await store.undoChange()
  expect(store.undo).toBeNull(); expect(toast).not.toHaveBeenCalled()
})
it('overlapping reads share their result rather than returning undefined to a picker', async () => {
  const store = useServiceTiers()
  let finish!: (s: TierState) => void
  const barrier = new Promise<TierState>(resolve => { finish = resolve })
  vi.mocked(readServiceTier).mockReturnValue(barrier)
  const picker = store.load(s), panel = store.load(s)
  finish(fresh())
  expect(await picker).toEqual(fresh()); expect(await panel).toEqual(fresh())
  expect(readServiceTier).toHaveBeenCalledTimes(1)
})
it('an overlapping read cannot falsely refuse Undo', async () => {
  const store = useServiceTiers(); const pending = await queue(store)
  let finish!: (s: TierState) => void
  const barrier = new Promise<TierState>(resolve => { finish = resolve })
  vi.mocked(readServiceTier).mockReturnValue(barrier)
  const undo = store.undoChange(), panel = store.load(s)
  vi.mocked(changeServiceTier).mockResolvedValueOnce({ ...fresh(), revision: 5 })
  finish(pending); await Promise.all([undo, panel])
  expect(changeServiceTier).toHaveBeenCalledTimes(2)
  expect(toast).not.toHaveBeenCalled(); expect(store.undo).toBeNull()
})
it('backs off to 30 seconds and stops automatic reads after two minutes', async () => {
  const store = useServiceTiers(); watchPage(store)
  const pending = await queue(store)
  const started = Date.now(), times: number[] = []
  vi.mocked(readServiceTier).mockImplementation(async () => { times.push(Date.now() - started); return pending })
  await vi.advanceTimersByTimeAsync(1200); expect(readServiceTier).toHaveBeenCalledTimes(1)
  await vi.advanceTimersByTimeAsync(1200); expect(readServiceTier).toHaveBeenCalledTimes(1)
  await vi.advanceTimersByTimeAsync(1200); expect(readServiceTier).toHaveBeenCalledTimes(2)
  await vi.advanceTimersByTimeAsync(116_400)
  expect(times).toEqual([1200, 3600, 8400, 18_000, 37_200, 67_200, 97_200])
  expect(store.errors.s).toContain('still pending')
  await vi.advanceTimersByTimeAsync(86_400_000)
  expect(times).toEqual([1200, 3600, 8400, 18_000, 37_200, 67_200, 97_200])
})
it('hidden tabs and navigation stop reads; becoming visible resumes within the time limit', async () => {
  const store = useServiceTiers(); const stop = watchPage(store)
  const pending = await queue(store)
  vi.mocked(readServiceTier).mockResolvedValue(pending)
  doc.visibilityState = 'hidden'; doc.dispatchEvent(new Event('visibilitychange'))
  await vi.advanceTimersByTimeAsync(10_000); expect(readServiceTier).not.toHaveBeenCalled()
  doc.visibilityState = 'visible'; doc.dispatchEvent(new Event('visibilitychange'))
  await vi.advanceTimersByTimeAsync(0); expect(readServiceTier).toHaveBeenCalledTimes(1)
  stop(); await vi.advanceTimersByTimeAsync(60_000)
  expect(readServiceTier).toHaveBeenCalledTimes(1)
})
it('a completion live event reports a late rejection without needing a tier revision bump', async () => {
  const store = useServiceTiers(); watchPage(store)
  const pending = await queue(store)
  vi.mocked(readServiceTier).mockResolvedValue(pending)
  await vi.advanceTimersByTimeAsync(120_000)
  vi.mocked(readServiceTier).mockResolvedValue({ ...fresh(), revision: 4, last_change: { ...pending.pending!, state: 'completed', outcome: 'rejected', reason: 'authorization_revoked' } })
  store.reconcile?.(); await vi.advanceTimersByTimeAsync(0)
  expect(store.undo).toBeNull(); expect(store.errors.s).toContain('authorization revoked')
})
it('live tier revisions confirm a change after the automatic read window ends', async () => {
  const store = useServiceTiers(); watchPage(store)
  const pending = await queue(store)
  vi.mocked(readServiceTier).mockResolvedValue(pending)
  await vi.advanceTimersByTimeAsync(120_000)
  vi.mocked(readServiceTier).mockResolvedValue({ ...fresh(), revision: 5, active_tier: 'fast' })
  agents.sessions[0]!.service_tier_revision = 5
  await vi.advanceTimersByTimeAsync(0)
  expect(store.state(s).active_tier).toBe('fast')
})
