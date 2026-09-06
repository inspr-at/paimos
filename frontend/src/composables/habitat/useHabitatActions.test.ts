import { effectScope, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
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
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      id: '00000000-0000-4000-8000-000000000099',
      harness_session_id: worker.value.harness_session_id,
      kind: 'stop',
      state: 'pending',
    })
    subject.prepare('stop')
    expect(post).not.toHaveBeenCalled()
    await subject.submit()
    expect(post).toHaveBeenCalledWith(
      `/projects/1/harness-sessions/${worker.value.harness_session_id}/controls/stop`,
      {},
      expect.any(Object),
    )
    expect(subject.control.value?.state).toBe('pending')
    expect(subject.feedback.value).toContain('completion is not yet confirmed')
    scope.stop()
  })
  it('refuses a stale worker revision before a mutation', async () => {
    const { subject, scope, fixture } = setup()
    const post = vi.spyOn(api, 'post')
    subject.prepare('interrupt')
    const newer = structuredClone(fixture)
    newer.fleet.workers[0].revision++
    vi.mocked(loadOrchestration).mockResolvedValue(newer)
    await subject.submit()
    expect(post).not.toHaveBeenCalled()
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
