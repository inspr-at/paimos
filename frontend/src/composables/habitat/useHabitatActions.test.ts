import { effectScope, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '@/api/client'
import { loadOrchestration } from '@/services/orchestration'
import { habitatFixture } from '@/components/habitat/__fixtures__/orchestration'
import { useHabitatActions } from './useHabitatActions'
vi.mock('@/services/orchestration', () => ({ loadOrchestration: vi.fn() }))
afterEach(() => vi.restoreAllMocks())
function setup() {
  const fixture = habitatFixture('100', 1)
  const worker = ref(fixture.fleet.workers[0])
  const authority = ref('human:1')
  const scope = effectScope()
  const subject = scope.run(() =>
    useHabitatActions({ worker, authority, editable: ref(true), fresh: ref(true) }),
  )!
  vi.mocked(loadOrchestration).mockResolvedValue(fixture)
  return { subject, scope, worker, authority, fixture }
}
describe('Habitat owned action boundaries', () => {
  it('requires explicit confirmation, requests a real scoped control, and waits for outcome evidence', async () => {
    const { subject, scope, worker } = setup()
    const post = vi
      .spyOn(api, 'post')
      .mockRejectedValue(new ApiError(409, 'stale worker'))
      .mockResolvedValue({
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
    subject.prepare('stop')
    expect(post).not.toHaveBeenCalled()
    await subject.submit()
    expect(post).toHaveBeenCalledWith(
      `/projects/1/harness-sessions/${worker.value.harness_session_id}/controls/v1/stop`,
      { expected_revision: worker.value.revision, request_key: expect.any(String) },
      expect.any(Object),
    )
    expect(subject.control.value?.state).toBe('pending')
    expect(subject.feedback.value).toContain('completion is not yet confirmed')
    scope.stop()
  })
  it('sends the selected revision to the atomic control endpoint and rejects a conflict', async () => {
    const { subject, scope, fixture } = setup()
    const post = vi.spyOn(api, 'post').mockRejectedValue(new ApiError(409, 'stale worker'))
    subject.prepare('interrupt')
    const newer = structuredClone(fixture)
    newer.fleet.workers[0].revision++
    vi.mocked(loadOrchestration).mockResolvedValue(newer)
    await subject.submit()
    expect(post).toHaveBeenCalledWith(
      expect.stringContaining('/controls/v1/interrupt'),
      expect.objectContaining({ expected_revision: fixture.fleet.workers[0].revision }),
      expect.any(Object),
    )
    expect(subject.control.value).toBeNull()
    expect(subject.feedback.value).toContain('revision or recipient changed')
    scope.stop()
  })
  it('clears drafts and in-flight acknowledgements across an authority switch', async () => {
    const { subject, scope, authority } = setup()
    let resolve!: (value: unknown) => void
    vi.spyOn(api, 'post').mockImplementation(
      () =>
        new Promise((yes) => {
          resolve = yes as typeof resolve
        }),
    )
    subject.prepare('simple')
    subject.draft.value = 'Fixture draft'
    const operation = subject.submit()
    await vi.waitFor(() => expect(resolve).toBeTypeOf('function'))
    authority.value = 'human:2'
    expect(subject.draft.value).toBe('')
    resolve({ message_id: 'fixture-message', delivered: true })
    await operation
    expect(subject.feedback.value).toBe('')
    expect(subject.control.value).toBeNull()
    scope.stop()
  })
})
