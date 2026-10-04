// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia } from 'pinia'
import { ref } from 'vue'
import { resetPositions } from '../src/lib/position'
import { clearPermissions, revokePermissions } from '../src/lib/authz'
import { defaultSchedule } from '../src/lib/capacity'
import { pairingView, pairingEnrollment } from './agent-pairing-fixtures'

const mocks = vi.hoisted(() => ({ capacity: vi.fn(), schedules: vi.fn(), computers: vi.fn() }))
vi.mock('../src/stores/agents', () => ({ useAgents: () => ({ accounts: [], now: Date.now(), onWrite: () => () => {} }) }))
vi.mock('../src/lib/preferences', () => ({ usePreference: () => ({ value: ref('free'), save: vi.fn() }) }))
vi.mock('../src/lib/capacity', async importOriginal => ({ ...await importOriginal<typeof import('../src/lib/capacity')>(), listCapacity: mocks.capacity, listSchedules: mocks.schedules }))
vi.mock('../src/lib/agentPairing', async importOriginal => ({ ...await importOriginal<typeof import('../src/lib/agentPairing')>(), listPairingComputers: mocks.computers }))
import { useCapacity } from '../src/stores/capacity'

let pinia: ReturnType<typeof createPinia>
const inventory = [pairingView({ computer_id: '33333333-3333-4333-8333-333333333333', computer_state: 'connected', enrollments: [pairingEnrollment()] })]
const schedule = { scope: 'user', schedule: { ...defaultSchedule('Europe/Vienna'), reserve: 'fixed', reserve_percent: 25 } }
beforeEach(() => {
  vi.resetAllMocks()
  pinia = createPinia(); setActivePinia(pinia)
  mocks.capacity.mockResolvedValue([{ account_id: 'old', schedule: schedule.schedule }])
  mocks.schedules.mockResolvedValue([schedule])
  mocks.computers.mockResolvedValue(inventory)
})
afterEach(() => disposePinia(pinia))

for (const reset of [resetPositions, clearPermissions, revokePermissions]) {
  it(`clears person-owned snapshots and load state before a failed read after ${reset.name}`, async () => {
    const store = useCapacity()
    await store.load()
    expect(store.computers).toHaveLength(1)
    expect(store.accountLines.size).toBe(1)
    expect(store.byAccount.size).toBe(1)
    expect(store.reserveConfirmed).toBe(true)
    reset()
    expect(store.computers).toEqual([])
    expect(store.accountLines.size).toBe(0)
    expect(store.byAccount.size).toBe(0)
    expect(store.reserveConfirmed).toBe(false)
    expect([store.loaded, store.schedulesLoaded, store.computersLoaded]).toEqual([false, false, false])
    expect([store.state, store.computersState]).toEqual(['idle', 'idle'])
    for (const read of Object.values(mocks)) read.mockRejectedValue(new Error('read refused'))
    await store.load()
    expect(store.computers).toEqual([])
    expect(store.accountLines.size).toBe(0)
    expect(store.byAccount.size).toBe(0)
    expect([store.state, store.computersState]).toEqual(['error', 'error'])
    expect([store.loaded, store.schedulesLoaded, store.computersLoaded]).toEqual([false, false, false])
  })

  it(`drops successful reads started before ${reset.name} even after a new failed refresh`, async () => {
    const store = useCapacity()
    const releases: (() => void)[] = []
    for (const [read, value] of [[mocks.capacity, [{ account_id: 'old', schedule: schedule.schedule }]], [mocks.schedules, [schedule]], [mocks.computers, inventory]] as const) {
      read.mockImplementationOnce(() => new Promise(resolve => releases.push(() => resolve(value))))
    }
    const old = store.load()
    expect(releases).toHaveLength(3)
    reset()
    for (const read of Object.values(mocks)) read.mockRejectedValue(new Error('new identity refused'))
    const fresh = store.load()
    for (const release of releases) release()
    await Promise.all([old, fresh])
    expect(store.computers).toEqual([])
    expect(store.byAccount.size).toBe(0)
    expect(store.reserveConfirmed).toBe(false)
    expect([store.state, store.computersState]).toEqual(['error', 'error'])
  })

}

it('keeps a snapshot for a transient failure within the same identity', async () => {
  const store = useCapacity()
  await store.load()
  mocks.computers.mockRejectedValue(new Error('offline'))
  await store.load()
  expect(store.computers).toEqual(inventory)
  expect(store.computersStale).toBe(true)
})
