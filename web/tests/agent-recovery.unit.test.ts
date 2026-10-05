// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { effectScope, reactive, nextTick } from 'vue'
import type { HarnessSession } from '../src/lib/agents'
const mocks = vi.hoisted(() => ({ request: vi.fn(), read: vi.fn(), toast: vi.fn() }))
const identity = reactive({ principal: { id: 'person-1', kind: 'person' } })
vi.mock('../src/stores/session', () => ({ useSession: () => ({ identity }) }))
vi.mock('../src/lib/authz', () => ({ can: () => true }))
vi.mock('../src/lib/agents', () => ({ requestAgentRecovery: mocks.request, readAgentRecovery: mocks.read }))
vi.mock('../src/lib/toast', () => ({ toast: mocks.toast }))
import { useAgentRecovery } from '../src/lib/agentRecovery'

const session = { id: 'session-1', project_id: 'project-1', archived_at: null, agent_recovery: { session_id: 'session-1', observed_revision: 'a'.repeat(64), cause: 'heartbeat_overdue', detail: 'Heartbeat overdue', action: 'restart' } } as HarnessSession
let scope = effectScope()
const setup = () => scope.run(useAgentRecovery)!
beforeEach(() => {
  scope = effectScope(); identity.principal.id = 'person-1'; vi.clearAllMocks(); vi.useFakeTimers()
  mocks.request.mockImplementation(async (_project, id, body) => ({ id: body.request_id, session_id: id, state: 'pending' }))
  mocks.read.mockImplementation(async (_project, id, request) => ({ id: request, session_id: id, state: 'claimed', outcome: null }))
})
afterEach(() => { scope.stop(); vi.useRealTimers() })

test('accepted recovery stays pending until verified result and never calls queued work restarted', async () => {
  const recovery = setup()
  await recovery.request(session, 'Worker')
  expect(recovery.busy[session.id]).toBe(true)
  expect(mocks.request.mock.calls[0]?.slice(0, 2)).toEqual(['project-1', 'session-1'])
  expect(mocks.request.mock.calls[0]?.[2]).toMatchObject({ expected_revision: 'a'.repeat(64), action: 'restart' })
  expect(mocks.toast.mock.calls.at(-1)?.[0]).toContain('requested. Waiting')
  mocks.read.mockImplementation(async (_project, id, request) => ({ id: request, session_id: id, state: 'completed', outcome: 'continuation_queued' }))
  await vi.advanceTimersByTimeAsync(1000)
  expect(mocks.toast.mock.calls.at(-1)?.[0]).toContain('Continuation queued for normal dispatch')
  expect(recovery.busy[session.id]).toBeUndefined()
})

test('rejected or expired recovery surfaces failure and stops polling', async () => {
  const recovery = setup()
  mocks.read.mockImplementation(async (_project, id, request) => ({ id: request, session_id: id, state: 'expired', outcome: 'unconfirmed' }))
  await recovery.request(session, 'Worker')
  await Promise.resolve()
  expect(mocks.toast.mock.calls.at(-1)?.[0]).toContain('outcome is unconfirmed')
  expect(mocks.toast.mock.calls.at(-1)?.[1]).toMatchObject({ tone: 'error' })
  const reads = mocks.read.mock.calls.length
  await vi.advanceTimersByTimeAsync(120000)
  expect(mocks.read).toHaveBeenCalledTimes(reads)
  expect(recovery.busy[session.id]).toBeUndefined()
})

test('person changes discard a late accepted request and clear owned state', async () => {
  const recovery = setup()
  let finish!: (value: unknown) => void
  mocks.request.mockImplementation((_project, id, body) => new Promise(resolve => { finish = () => resolve({ id: body.request_id, session_id: id, state: 'pending' }) }))
  const pending = recovery.request(session, 'Worker')
  identity.principal.id = 'person-2'; await nextTick(); finish({})
  await pending
  expect(mocks.toast).not.toHaveBeenCalled()
  expect(mocks.read).not.toHaveBeenCalled()
  expect(recovery.busy[session.id]).toBeUndefined()
})

test('disposal and wrong-session responses cannot produce a recovery success', async () => {
  const recovery = setup()
  mocks.read.mockResolvedValue({ id: 'wrong-request', session_id: 'session-2', state: 'completed', outcome: 'reconnected' })
  await recovery.request(session, 'Worker'); await Promise.resolve()
  expect(mocks.toast.mock.calls.at(-1)?.[0]).toContain('did not match this session')
  expect(mocks.toast.mock.calls.at(-1)?.[1]).toMatchObject({ tone: 'error' })
  const reads = mocks.read.mock.calls.length
  scope.stop(); await vi.advanceTimersByTimeAsync(10000)
  expect(mocks.read).toHaveBeenCalledTimes(reads)
})


test('switching the visible session discards a late recovery result', async () => {
  const visible = reactive({ id: session.id })
  const recovery = scope.run(() => useAgentRecovery(() => visible.id))!
  let finish!: () => void
  mocks.request.mockImplementation((_project, id, body) => new Promise(resolve => { finish = () => resolve({ id: body.request_id, session_id: id, state: 'pending' }) }))
  const pending = recovery.request(session, 'Worker')
  visible.id = 'session-2'; await nextTick(); finish()
  await pending
  expect(mocks.read).not.toHaveBeenCalled()
  expect(mocks.toast).not.toHaveBeenCalled()
  expect(recovery.busy[session.id]).toBeUndefined()
})
