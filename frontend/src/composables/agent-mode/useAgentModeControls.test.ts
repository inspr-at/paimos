/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref } from 'vue'

import { makeFixtureSnapshot } from '@/services/agentModeFixtures'
import { normalizeWireSnapshot } from '@/services/agentModeTransport'
import type { ControlCommand, ControlGrant } from '@/services/agentModeControls'
import { useAgentModeControls } from './useAgentModeControls'

const rows = normalizeWireSnapshot(makeFixtureSnapshot(10), 0).deliveries
const GRANT_ID = '11111111-1111-4111-8111-111111111111'
const COMMAND_ID = '22222222-2222-4222-8222-222222222222'

function memoryStorage(seed: Record<string, string> = {}): Storage {
  const values = new Map(Object.entries(seed))
  return {
    get length() { return values.size },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => { values.delete(key) },
    setItem: (key, value) => { values.set(key, String(value)) },
  }
}

function grant(targets: ControlGrant['targets'], revision = 1): ControlGrant {
  return {
    grantId: GRANT_ID, revision, deliveryKey: rows[0].id, issueKey: rows[0].issueKey,
    actions: [...new Set(targets.map((target) => target.action))], targets,
    expiresAt: '2027-08-21T20:00:00Z',
  }
}

function command(status: ControlCommand['status'] = 'pending_confirmation', revision = 1): ControlCommand {
  return {
    commandId: COMMAND_ID, statusRevision: revision, action: 'run.cancel.running', status,
    challengeTemplate: 'run_cancel_running', expiresAt: '2027-08-21T20:00:00Z',
    display: { issueKey: rows[0].issueKey, deliveryKey: rows[0].id, runId: 42 },
    outcome: status === 'applied' ? 'applied' : null, reason: null,
  }
}

function fixture(options: { storage?: Storage; stored?: boolean } = {}) {
  const delivery = ref(rows[0])
  const userId = ref<number | null>(7)
  const online = ref(true)
  const degraded = ref(false)
  const authorityAvailable = ref(true)
  const authorityVersion = ref(1)
  const storage = options.storage ?? memoryStorage(options.stored
    ? { [`paimos:agent-mode:control-command:7:${rows[0].id}`]: COMMAND_ID }
    : {})
  const issueGrant = vi.fn(async () => grant([{ action: 'run.cancel.running', runId: 42 }]))
  const getCommand = vi.fn(async () => command())
  const createCommand = vi.fn(async () => command())
  const transitionCommand = vi.fn(async (_id, operation: 'confirm' | 'withdraw') => (
    command(operation === 'confirm' ? 'accepted' : 'rejected', 2)
  ))
  let key = 4
  const scope = effectScope()
  const controls = scope.run(() => useAgentModeControls({
    delivery, userId, online, degraded, authorityAvailable, authorityVersion,
    dependencies: {
      storage, issueGrant, getCommand, createCommand, transitionCommand,
      newIdempotencyKey: () => `${key++}`.padStart(8, '0') + '-0000-4000-8000-000000000000',
      pollBaseMs: 100_000,
    },
  }))!
  return { scope, controls, delivery, userId, online, degraded, authorityAvailable, authorityVersion, storage, issueGrant, getCommand, createCommand, transitionCommand }
}

async function flush() {
  for (let index = 0; index < 12; index += 1) await Promise.resolve()
}

const scopes: ReturnType<typeof effectScope>[] = []
afterEach(() => {
  for (const scope of scopes.splice(0)) scope.stop()
  vi.useRealTimers()
})

describe('useAgentModeControls', () => {
  it('reloads only the opaque user+delivery command id and never auto-confirms', async () => {
    const f = fixture({ stored: true }); scopes.push(f.scope)
    await f.controls.initialize()
    expect(f.getCommand).toHaveBeenCalledWith(COMMAND_ID, expect.any(AbortSignal))
    expect(f.issueGrant).not.toHaveBeenCalled()
    expect(f.transitionCommand).not.toHaveBeenCalled()
    expect(f.controls.boundCommand.value?.command.commandId).toBe(COMMAND_ID)
  })

  it('derives a restored binding only from server display labels and preserves it across selection changes', async () => {
    const f = fixture({ stored: true }); scopes.push(f.scope)
    f.getCommand.mockResolvedValueOnce({
      ...command('accepted', 2),
      display: { issueKey: 'SAFE-1', deliveryKey: rows[0].id, runId: 42 },
    })
    await f.controls.initialize()
    expect(f.controls.boundCommand.value).toMatchObject({
      deliveryKey: rows[0].id,
      issueKey: 'SAFE-1',
      command: { display: { issueKey: 'SAFE-1', deliveryKey: rows[0].id } },
    })
    f.delivery.value = rows[1]
    await flush()
    expect(f.controls.boundCommand.value?.issueKey).toBe('SAFE-1')
    expect(f.transitionCommand).not.toHaveBeenCalled()
  })

  it('rejects a restored command whose server delivery label mismatches its storage scope', async () => {
    const f = fixture({ stored: true }); scopes.push(f.scope)
    f.getCommand.mockResolvedValueOnce({
      ...command(),
      display: { issueKey: 'SAFE-1', deliveryKey: rows[1].id, runId: 42 },
    })
    await f.controls.initialize()
    expect(f.controls.boundCommand.value).toBeNull()
    expect(f.controls.state.value).toBe('error')
  })

  it('atomically replaces targets and rejects an old captured descriptor', async () => {
    const f = fixture(); scopes.push(f.scope)
    f.issueGrant
      .mockResolvedValueOnce(grant([{ action: 'run.cancel.running', runId: 42 }], 1))
      .mockResolvedValueOnce(grant([{ action: 'run.cancel.running', runId: 99 }], 2))
    await f.controls.initialize()
    const old = f.controls.targets.value[0]
    await f.controls.initialize()
    expect(f.controls.targets.value).toEqual([{ action: 'run.cancel.running', runId: 99 }])
    expect(await f.controls.activate({ target: old as { action: 'run.cancel.running'; runId: number } })).toBe(false)
    expect(f.createCommand).not.toHaveBeenCalled()
  })

  it('removes advertised targets exactly when the grant expires', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-21T19:00:00Z'))
    const f = fixture(); scopes.push(f.scope)
    f.issueGrant.mockResolvedValueOnce({
      ...grant([{ action: 'run.cancel.running', runId: 42 }]),
      expiresAt: '2026-08-21T19:00:01Z',
    })
    await f.controls.initialize()
    expect(f.controls.targets.value).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(f.controls.targets.value).toEqual([])
    expect(f.controls.state.value).toBe('conflict')
  })

  it('removes stale capability while offline and reloads a fresh target set on reconnect', async () => {
    const f = fixture(); scopes.push(f.scope)
    f.issueGrant
      .mockResolvedValueOnce(grant([{ action: 'run.cancel.running', runId: 42 }], 1))
      .mockResolvedValueOnce(grant([{ action: 'run.cancel.running', runId: 99 }], 2))
    await f.controls.initialize()
    f.online.value = false
    expect(f.controls.targets.value).toEqual([])
    expect(f.controls.controlAvailable.value).toBe(false)
    f.online.value = true
    await flush()
    expect(f.controls.targets.value).toEqual([{ action: 'run.cancel.running', runId: 99 }])
    expect(f.issueGrant).toHaveBeenCalledTimes(2)
  })

  it('invalidates pre-command authority before loading its replacement targets', async () => {
    const f = fixture(); scopes.push(f.scope)
    f.issueGrant
      .mockResolvedValueOnce(grant([{ action: 'run.cancel.running', runId: 42 }], 1))
      .mockResolvedValueOnce(grant([{ action: 'run.cancel.running', runId: 77 }], 2))
    await f.controls.initialize()
    f.authorityVersion.value += 1
    expect(f.controls.targets.value).toEqual([])
    await flush()
    expect(f.controls.targets.value).toEqual([{ action: 'run.cancel.running', runId: 77 }])
  })

  it('single-flights creation and confirms only an exact command revision', async () => {
    const f = fixture(); scopes.push(f.scope)
    await f.controls.initialize()
    const target = f.controls.targets.value[0] as { action: 'run.cancel.running'; runId: number }
    const first = f.controls.activate({ target })
    const second = f.controls.activate({ target })
    expect(await first).toBe(true)
    expect(await second).toBe(false)
    expect(f.createCommand).toHaveBeenCalledOnce()
    expect(await f.controls.confirmExact(COMMAND_ID, 99)).toBe(false)
    expect(await f.controls.confirmExact(COMMAND_ID, 1)).toBe(true)
    expect(f.transitionCommand).toHaveBeenCalledOnce()
    expect(f.controls.boundCommand.value?.command.status).toBe('accepted')
  })

  it('withdraws a pending command against its original binding before changing selection', async () => {
    const f = fixture(); scopes.push(f.scope)
    await f.controls.initialize()
    const target = f.controls.targets.value[0] as { action: 'run.cancel.running'; runId: number }
    await f.controls.activate({ target })
    f.delivery.value = rows[1]
    await flush()
    expect(f.transitionCommand).toHaveBeenCalledWith(
      COMMAND_ID, 'withdraw', 1, expect.any(String), expect.any(AbortSignal),
    )
    expect(f.controls.boundCommand.value).toBeNull()
  })

  it('retains the old safe binding when withdrawal races an accepted command', async () => {
    const f = fixture(); scopes.push(f.scope)
    await f.controls.initialize()
    const target = f.controls.targets.value[0] as { action: 'run.cancel.running'; runId: number }
    await f.controls.activate({ target })
    f.transitionCommand.mockResolvedValueOnce(command('accepted', 2))
    f.delivery.value = rows[1]
    await flush()
    expect(f.controls.boundCommand.value).toMatchObject({
      deliveryKey: rows[0].id,
      issueKey: rows[0].issueKey,
      command: { status: 'accepted' },
    })
  })

  it('does not restore an old pending projection when the principal changes during withdrawal', async () => {
    const f = fixture(); scopes.push(f.scope)
    await f.controls.initialize()
    const target = f.controls.targets.value[0] as { action: 'run.cancel.running'; runId: number }
    await f.controls.activate({ target })
    let resolveWithdrawal!: (value: ControlCommand) => void
    f.transitionCommand.mockImplementationOnce(() => new Promise((resolve) => { resolveWithdrawal = resolve }))
    f.delivery.value = rows[1]
    await flush()
    f.userId.value = 8
    resolveWithdrawal(command('accepted', 2))
    await flush()
    expect(f.controls.boundCommand.value).toBeNull()
    expect(f.storage.getItem(`paimos:agent-mode:control-command:7:${rows[0].id}`)).toBeNull()
    expect(f.storage.getItem(`paimos:agent-mode:control-command:8:${rows[0].id}`)).toBeNull()
  })

  it('fails closed for malformed storage and isolates values by effective user', async () => {
    const key = `paimos:agent-mode:control-command:7:${rows[0].id}`
    const storage = memoryStorage({ [key]: 'not-a-command-id' })
    const f = fixture({ storage }); scopes.push(f.scope)
    await f.controls.initialize()
    expect(f.controls.storageAvailable.value).toBe(false)
    expect(f.issueGrant).not.toHaveBeenCalled()
    expect(storage.getItem(key)).toBeNull()
    expect(storage.getItem(`paimos:agent-mode:control-command:8:${rows[0].id}`)).toBeNull()
  })

  it('clears only the previous effective-user storage key on a principal switch', async () => {
    const oldKey = `paimos:agent-mode:control-command:7:${rows[0].id}`
    const newKey = `paimos:agent-mode:control-command:8:${rows[0].id}`
    const storage = memoryStorage({ [oldKey]: COMMAND_ID, [newKey]: '33333333-3333-4333-8333-333333333333' })
    const f = fixture({ storage, stored: true }); scopes.push(f.scope)
    f.getCommand
      .mockResolvedValueOnce(command())
      .mockResolvedValueOnce({ ...command(), commandId: '33333333-3333-4333-8333-333333333333' })
    await f.controls.initialize()
    f.userId.value = 8
    await flush()
    expect(storage.getItem(oldKey)).toBeNull()
    expect(storage.getItem(newKey)).toBe('33333333-3333-4333-8333-333333333333')
  })

  it('keeps accepted truth while offline and never withdraws it on selection drift', async () => {
    const f = fixture(); scopes.push(f.scope)
    f.createCommand.mockResolvedValueOnce(command('accepted', 2))
    await f.controls.initialize()
    const target = f.controls.targets.value[0] as { action: 'run.cancel.running'; runId: number }
    await f.controls.activate({ target })
    f.online.value = false
    f.delivery.value = rows[1]
    await flush()
    expect(f.controls.boundCommand.value?.issueKey).toBe(rows[0].issueKey)
    expect(f.controls.boundCommand.value?.command.status).toBe('accepted')
    expect(f.controls.state.value).toBe('offline')
    expect(f.transitionCommand).not.toHaveBeenCalled()
  })
})
