// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { deferred, flush, setupSource } from './record-source'
import * as identity from '../src/lib/identityScope'
const first = { account_id: 'owned-a', posture: 'balanced', source: 'person', floor_percent: 20, own_floor_percent: 20, revision: 3, binding_revision: 2, can_set_posture: true, can_set_floor: false, boost_percent: 0, boost_until: null }
const scopes: { stop: () => void }[] = []
function setup(options: { read?: ReturnType<typeof vi.fn>; write?: ReturnType<typeof vi.fn>; kind?: string } = {}) {
  vi.stubGlobal('document', { documentElement: { lang: 'en' } })
  const session = reactive({ identity: { tenant: { id: 'tenant' }, principal: { id: 'owner', kind: options.kind ?? 'person' } }, authenticationCurrent: () => true })
  const read = options.read ?? vi.fn(async () => ({ accounts: [{ account_id: first.account_id, usage_policy: first }, { account_id: 'peer', usage_policy: { ...first, account_id: 'peer', can_set_posture: false } }], has_more: false }))
  const write = options.write ?? vi.fn(async () => ({ accounts: [{ ...first, revision: 4, boost_percent: 20, boost_until: '2026-10-08T21:59:00Z' }] }))
  const view = setupSource('components/agents/BoostToday.vue', {}, {
    'vue-router': { useRoute: () => ({ query: {} }) }, '../../stores/session': { useSession: () => session },
    '../../lib/authz': { can: () => true, onAccessChange: () => () => {} }, '../../lib/identityScope': identity,
    '../../lib/accountUsage': { getUsageOverview: read, putAccountBoost: write },
    '../../lib/usePolledData': { usePoller: () => ({ start() {}, stop() {} }) },
  })
  scopes.push(view)
  return { ...view, session, read, write }
}
afterEach(() => { for (const scope of scopes.splice(0)) scope.stop(); vi.unstubAllGlobals(); vi.useRealTimers() })
it('writes only owned accounts with captured revisions and expires the confirmed selection at the local cutoff', async () => {
  vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout'] }); vi.setSystemTime('2026-10-08T12:00:00Z')
  const view = setup(); await flush()
  expect(view.state.policies.value).toEqual([first]); expect(view.state.selected.value).toBe(0)
  view.state.save(20); await flush()
  expect(view.write.mock.calls[0]![0]).toEqual([first]); expect(view.write.mock.calls[0]![1]).toBe(20)
  expect(view.state.selected.value).toBe(20); expect(view.emitted).toEqual([['changed']])
  vi.advanceTimersByTime(Date.parse('2026-10-08T21:59:00Z') - Date.now())
  expect(view.state.selected.value).toBe(0)
})
it('keeps the last confirmed selection on failure and blocks retry until a complete reload', async () => {
  const view = setup({ write: vi.fn().mockRejectedValue(new Error('failed')) }); await flush()
  view.state.save(30); await flush()
  expect(view.state.selected.value).toBe(0); expect(view.state.ready.value).toBe(false)
  expect(view.state.error.value).toContain('Could not confirm'); expect(view.emitted).toEqual([])
  view.state.save(10); await flush(); expect(view.write).toHaveBeenCalledTimes(1)
  await view.state.load(true); expect(view.state.ready.value).toBe(true)
})
it('drops a held save on identity change and exposes no boost controls to agents or non-owners', async () => {
  const held = deferred<any>(), view = setup({ write: vi.fn(() => held.promise) }); await flush()
  view.state.save(20)
  view.read.mockResolvedValue({ accounts: [{ account_id: 'peer', usage_policy: { ...first, can_set_posture: false } }], has_more: false })
  view.session.identity.principal.id = 'peer'; await flush()
  held.resolve({ accounts: [{ ...first, revision: 4, boost_percent: 20, boost_until: '2026-10-08T21:59:00Z' }] }); await flush()
  expect(view.state.visible.value).toBe(false); expect(view.state.policies.value).toEqual([]); expect(view.emitted).toEqual([])
  const agent = setup({ kind: 'agent' }); await flush(); expect(agent.read).not.toHaveBeenCalled(); expect(agent.state.visible.value).toBe(false)
})
it('shows Boost today when every owned account is withheld and saves without account ids', async () => {
  vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout'] }); vi.setSystemTime('2026-10-08T12:00:00Z')
  const view = setup({
    read: vi.fn(async () => ({ accounts: [{ account_id: 'hidden-a', boost_withheld: true, details_redacted: true }, { account_id: first.account_id, usage_policy: first }], has_more: false })),
    write: vi.fn(async () => ({ accounts: [{ ...first, revision: 4, boost_percent: 20, boost_until: '2026-10-08T21:59:00Z' }], withheld_count: 1, withheld_boost_until: '2026-10-08T21:59:00Z' })),
  })
  await flush()
  view.state.save(20); await flush()
  expect(view.write.mock.calls[0]![0]).toEqual([first])
  const hidden = setup({
    read: vi.fn(async () => ({ accounts: [{ account_id: 'hidden-a', boost_withheld: true, details_redacted: true }], has_more: false })),
    write: vi.fn(async () => ({ accounts: [], withheld_count: 1, withheld_boost_until: '2026-10-08T21:59:00Z' })),
  })
  await flush()
  expect(hidden.state.visible.value).toBe(true)
  expect(hidden.state.policies.value).toEqual([])
  expect(hidden.state.selected.value).toBe(null)
  hidden.state.save(20); await flush()
  expect(hidden.write.mock.calls[0]![0]).toEqual([])
  expect(hidden.state.selected.value).toBe(20)
  expect(hidden.emitted).toEqual([['changed']])
})
it('rejects incomplete overview pagination instead of boosting a partial account list', async () => {
  const view = setup({ read: vi.fn(async () => ({ accounts: [{ account_id: first.account_id, usage_policy: first }], has_more: true })) }); await flush()
  expect(view.state.ready.value).toBe(false); expect(view.state.policies.value).toEqual([])
  view.state.save(20); expect(view.write).not.toHaveBeenCalled(); expect(view.state.error.value).toContain('Could not read')
})
