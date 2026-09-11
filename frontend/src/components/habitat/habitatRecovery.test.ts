import { afterEach, describe, expect, it } from 'vitest'
import { ssHabitatIntentKey } from '@/constants/storage'
import {
  findLifecycleDraftForSession,
  readIntentRecovery,
  writeIntentRecovery,
} from './habitatRecovery'
import type { HabitatExistingRequest, HabitatStartRequest } from './habitatLifecycle'
const id = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const scope = {
  origin: 'https://fixture.local',
  instance: 'test-instance',
  principalId: 1,
  projectId: 1,
}
const request: HabitatStartRequest = {
  request_key: id(1),
  operation: 'start',
  runtime_id: id(2),
  runtime_generation: id(3),
  account_label: 'chatgpt',
  ttl_seconds: 120,
  workspace_handle: id(4),
  agent_name: 'fixture',
  dispatch_profile_id: 'fixture-profile',
  dispatch_profile_version: '1',
  ticket_id: null,
  work_shape: 'unknown',
  role: 'worker',
  parent_harness_session_id: null,
}
const key = ssHabitatIntentKey(scope.origin, scope.instance, scope.principalId, scope.projectId)
afterEach(() => sessionStorage.clear())
describe('Non-authoritative lifecycle recovery', () => {
  it('preserves the exact review only within its principal, origin, instance and project', () => {
    const review = { intentId: id(5), request, savedAt: Date.now() }
    expect(writeIntentRecovery(scope, review)).toBe(true)
    expect(readIntentRecovery(scope)).toEqual(review)
    for (const changed of [
      { principalId: 2 },
      { origin: 'https://other.local' },
      { instance: 'another-instance' },
      { projectId: 2 },
    ])
      expect(readIntentRecovery({ ...scope, ...changed })).toBeNull()
    expect(readIntentRecovery(scope)).toEqual(review)
  })
  it('clears expired, scope-tampered and extra-field recovery instead of treating it as evidence', () => {
    const review = { intentId: id(5), request, savedAt: Date.now() }
    for (const change of [
      { savedAt: Date.now() - 25 * 60 * 60 * 1000 },
      { savedAt: Date.now() + 60_000 },
      { scope: { ...scope, principalId: 2 } },
      { request: { ...request, private_field: 'fixture-canary' } },
    ]) {
      expect(writeIntentRecovery(scope, review)).toBe(true)
      sessionStorage.setItem(
        key,
        JSON.stringify({ ...JSON.parse(sessionStorage.getItem(key)!), ...change }),
      )
      expect(readIntentRecovery(scope)).toBeNull()
      expect(sessionStorage.getItem(key)).toBeNull()
    }
  })
  it('finds only unsubmitted lifecycle drafts bound to one worker session', () => {
    const sessionId = id(9)
    const draft: HabitatExistingRequest = {
      request_key: id(10),
      operation: 'reassign',
      runtime_id: id(2),
      runtime_generation: id(3),
      account_label: 'chatgpt',
      ttl_seconds: 120,
      workspace_handle: id(4),
      agent_name: 'fixture',
      dispatch_profile_id: 'fixture-profile',
      dispatch_profile_version: '1',
      ticket_id: 917,
      work_shape: 'ship',
      role: 'worker',
      parent_harness_session_id: null,
      session_id: sessionId,
      session_generation: id(11),
      expected_revision: 2,
    }
    expect(
      writeIntentRecovery(scope, { intentId: null, request: draft, savedAt: Date.now() }),
    ).toBe(true)
    expect(findLifecycleDraftForSession(1, 1, sessionId)?.recovery.request).toEqual(draft)
    expect(findLifecycleDraftForSession(1, 1, id(12))).toBeNull()
    expect(
      writeIntentRecovery(scope, { intentId: id(5), request: draft, savedAt: Date.now() }),
    ).toBe(true)
    expect(findLifecycleDraftForSession(1, 1, sessionId)).toBeNull()
  })
})
