import { effectScope, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '@/api/client'
import { habitatFixture } from '@/components/habitat/__fixtures__/orchestration'
import type { HabitatExistingRequest } from '@/components/habitat/habitatLifecycle'
import { writeIntentRecovery } from '@/components/habitat/habitatRecovery'
import { useHabitatRemoval } from './useHabitatRemoval'

const id = (n: string) => `00000000-0000-4000-8000-${n.padStart(12, '0')}`

function setup() {
  const fixture = habitatFixture('100', 1)
  const worker = ref(fixture.fleet.workers[0])
  worker.value.capabilities.stop = true
  const authority = ref('human:1')
  const principalId = ref(1)
  const scope = effectScope()
  const subject = scope.run(() =>
    useHabitatRemoval({
      worker,
      authority,
      editable: ref(true),
      fresh: ref(true),
      principalId,
    }),
  )!
  return { subject, scope, worker, authority, principalId, fixture }
}

const scopes: ReturnType<typeof effectScope>[] = []
afterEach(() => {
  for (const scope of scopes.splice(0)) scope.stop()
  vi.restoreAllMocks()
  sessionStorage.clear()
})

describe('useHabitatRemoval', () => {
  it('clears only a local lifecycle draft without calling any API', async () => {
    const { subject, scope, worker } = setup()
    scopes.push(scope)
    const draft: HabitatExistingRequest = {
      request_key: id('1'),
      operation: 'reassign',
      runtime_id: id('2'),
      runtime_generation: id('3'),
      account_label: 'chatgpt',
      ttl_seconds: 120,
      workspace_handle: id('4'),
      agent_name: 'fixture',
      dispatch_profile_id: 'fixture-profile',
      dispatch_profile_version: '1',
      ticket_id: 917,
      work_shape: 'ship',
      role: 'worker',
      parent_harness_session_id: null,
      session_id: worker.value.harness_session_id,
      session_generation: id('5'),
      expected_revision: worker.value.revision,
    }
    expect(
      writeIntentRecovery(
        {
          origin: 'https://fixture.local',
          instance: 'test-instance',
          principalId: 1,
          projectId: 1,
        },
        { intentId: null, request: draft, savedAt: Date.now() },
      ),
    ).toBe(true)
    const patch = vi.spyOn(api, 'patch')
    const post = vi.spyOn(api, 'post')
    subject.begin()
    expect(subject.draftOnlyAvailable.value).toBe(true)
    subject.choose('draft_only')
    await subject.submit()
    expect(patch).not.toHaveBeenCalled()
    expect(post).not.toHaveBeenCalled()
    expect(subject.draftOnlyAvailable.value).toBe(false)
    expect(subject.feedback.value).toContain('No API was called')
  })

  it('does not offer draft-only removal without a local lifecycle draft', () => {
    const { subject, scope } = setup()
    scopes.push(scope)
    subject.begin()
    expect(subject.draftOnlyAvailable.value).toBe(false)
    subject.choose('draft_only')
    expect(subject.mode.value).toBeNull()
  })

  it('stop-now removal requests a scoped control once and records pending evidence', async () => {
    const { subject, scope, worker } = setup()
    scopes.push(scope)
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      schema_version: 1,
      state: 'requested',
      control: {
        sequence: 1,
        requested_at: new Date().toISOString(),
        requested_by_user_id: 1,
        id: '00000000-0000-4000-8000-000000000099',
        harness_session_id: worker.value.harness_session_id,
        kind: 'stop',
        state: 'pending',
      },
    })
    subject.begin()
    subject.choose('stop_now')
    await subject.submit()
    expect(post).toHaveBeenCalledTimes(1)
    expect(subject.control.value?.state).toBe('pending')
    expect(subject.feedback.value).toContain('does not prove local reaping')
  })

  it('blocks duplicate confirmation while a removal request is in flight', async () => {
    const { subject, scope, worker } = setup()
    scopes.push(scope)
    let resolve!: (value: unknown) => void
    const post = vi.spyOn(api, 'post').mockImplementation(
      () =>
        new Promise((yes) => {
          resolve = yes
        }),
    )
    subject.begin()
    subject.choose('stop_now')
    const first = subject.submit()
    await vi.waitFor(() => expect(resolve).toBeTypeOf('function'))
    await subject.submit()
    expect(post).toHaveBeenCalledTimes(1)
    resolve({
      schema_version: 1,
      state: 'requested',
      control: {
        sequence: 1,
        requested_at: new Date().toISOString(),
        requested_by_user_id: 1,
        id: '00000000-0000-4000-8000-000000000099',
        harness_session_id: worker.value.harness_session_id,
        kind: 'stop',
        state: 'pending',
      },
    })
    await first
  })

  it('submits finish-then-remove while leaving handoff explicitly unsupported', async () => {
    const { subject, scope, worker } = setup()
    scopes.push(scope)
    const retirementId = id('97')
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      schema_version: 1,
      retirement: {
        id: retirementId,
        project_id: 1,
        harness_session_id: worker.value.harness_session_id,
        correlation_id: retirementId,
        kind: 'retire_after_work',
        requested_revision: worker.value.revision,
        state: 'requested',
        requested_at: new Date().toISOString(),
        owned_stop_receipt: false,
        stopped_generation_proof: false,
      },
    })
    const patch = vi.spyOn(api, 'patch')
    subject.begin()
    expect(subject.finishThenRemoveAvailable.value).toBe(true)
    expect(subject.handoffAvailable.value).toBe(false)
    subject.choose('finish_then_remove')
    await subject.submit()
    expect(post).toHaveBeenCalledWith(
      `/projects/1/harness-sessions/${worker.value.harness_session_id}/controls/v1/retire-after-work`,
      { expected_revision: worker.value.revision, request_key: expect.any(String) },
      expect.any(Object),
    )
    expect(subject.control.value?.state).toBe('requested')
    expect(subject.feedback.value).toContain('New work is fenced')
    subject.begin()
    subject.choose('handoff')
    expect(subject.mode.value).toBeNull()
    await subject.submit()
    expect(post).toHaveBeenCalledTimes(1)
    expect(patch).not.toHaveBeenCalled()
  })

  it('shows failed and completed durable retirement evidence without inventing success', async () => {
    const { subject, scope, worker } = setup()
    scopes.push(scope)
    const retirementId = id('95')
    const requestedAt = new Date().toISOString()
    vi.spyOn(api, 'post').mockResolvedValue({
      schema_version: 1,
      retirement: {
        id: retirementId,
        project_id: 1,
        harness_session_id: worker.value.harness_session_id,
        correlation_id: retirementId,
        kind: 'retire_after_work',
        requested_revision: worker.value.revision,
        state: 'requested',
        requested_at: requestedAt,
        owned_stop_receipt: false,
        stopped_generation_proof: false,
      },
    })
    const result = (state: 'outcome_unknown' | 'completed') => ({
      data: {
        id: retirementId,
        project_id: 1,
        harness_session_id: worker.value.harness_session_id,
        correlation_id: retirementId,
        kind: 'retire_after_work',
        requested_revision: worker.value.revision,
        state,
        reason: state === 'completed' ? 'applied' : 'outcome_unknown',
        requested_at: requestedAt,
        claimed_at: requestedAt,
        ...(state === 'completed' ? { stopping_at: requestedAt } : {}),
        completed_at: requestedAt,
        owned_stop_receipt: state === 'completed',
        stopped_generation_proof: state === 'completed',
      },
      status: 200,
      permissionsEpoch: '1',
      permissionsEpochGeneration: 0,
      etag: null,
      lastModified: null,
    })
    const get = vi
      .spyOn(api, 'getWithMeta')
      .mockResolvedValueOnce(result('outcome_unknown'))
      .mockResolvedValueOnce(result('completed'))
    subject.begin()
    subject.choose('finish_then_remove')
    await subject.submit()
    await subject.checkControl()
    expect(subject.control.value?.state).toBe('outcome_unknown')
    expect(subject.feedback.value).toContain('did not establish')
    await subject.checkControl()
    expect(subject.control.value?.state).toBe('completed')
    expect(subject.feedback.value).toContain('owned stop')
    expect(get).toHaveBeenCalledTimes(2)
  })

  it('refuses stop when capability is not advertised', async () => {
    const { subject, scope, worker } = setup()
    scopes.push(scope)
    worker.value.capabilities.stop = false
    const post = vi.spyOn(api, 'post')
    subject.begin()
    subject.choose('stop_now')
    expect(subject.mode.value).toBeNull()
    await subject.submit()
    expect(post).not.toHaveBeenCalled()
  })

  it('refuses stale stop confirmation after revision changes and cancel leaves state untouched', async () => {
    const { subject, scope, worker } = setup()
    scopes.push(scope)
    const post = vi.spyOn(api, 'post').mockRejectedValue(new ApiError(409, 'stale'))
    subject.begin()
    subject.choose('stop_now')
    worker.value = { ...worker.value, revision: worker.value.revision + 1 }
    await subject.submit()
    expect(post).toHaveBeenCalled()
    expect(subject.control.value).toBeNull()
    expect(subject.feedback.value).toContain('revision')
    subject.begin()
    subject.choose('stop_now')
    subject.cancel()
    expect(subject.open.value).toBe(false)
    expect(post).toHaveBeenCalledTimes(1)
  })

  it('refreshes terminal control receipts without equating request acceptance with reaping', async () => {
    const { subject, scope, worker } = setup()
    scopes.push(scope)
    vi.spyOn(api, 'post').mockResolvedValue({
      schema_version: 1,
      state: 'requested',
      control: {
        sequence: 1,
        requested_at: new Date().toISOString(),
        requested_by_user_id: 1,
        id: '00000000-0000-4000-8000-000000000096',
        harness_session_id: worker.value.harness_session_id,
        kind: 'stop',
        state: 'pending',
      },
    })
    const get = vi.spyOn(api, 'getWithMeta').mockResolvedValue({
      data: {
        id: '00000000-0000-4000-8000-000000000096',
        project_id: 1,
        correlation_id: '00000000-0000-4000-8000-000000000096',
        harness_session_id: worker.value.harness_session_id,
        sequence: 1,
        kind: 'stop',
        state: 'applied',
        outcome: 'applied',
        reason: 'applied',
        requested_at: new Date().toISOString(),
        claimed_at: new Date().toISOString(),
        completed_at: new Date().toISOString(),
      },
      status: 200,
      permissionsEpoch: '1',
      permissionsEpochGeneration: 0,
      etag: null,
      lastModified: null,
    })
    subject.begin()
    subject.choose('stop_now')
    await subject.submit()
    await subject.checkControl()
    expect(get).toHaveBeenCalled()
    expect(subject.control.value?.outcome).toBe('applied')
    expect(subject.feedback.value).toContain('evidence refreshed')
  })
})
