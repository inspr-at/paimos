import { effectScope, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { loadPaimos6SessionZoom } from '@/v6/sessionHomeZoom'
import { loadOrchestration } from '@/services/orchestration'
import { fetchAgentModeSnapshot } from '@/services/agentMode'
import { ApiError } from '@/api/client'
import { habitatFixture } from '@/components/habitat/__fixtures__/orchestration'
import { useHabitat } from './useHabitat'
vi.mock('@/v6/sessionHomeZoom', () => ({
  loadPaimos6SessionZoom: vi.fn().mockRejectedValue(new Error('fixture unavailable')),
}))
vi.mock('@/services/orchestration', () => ({ loadOrchestration: vi.fn() }))
vi.mock('@/services/agentMode', () => ({ fetchAgentModeSnapshot: vi.fn() }))
function deferred<T>() {
  let resolve!: (value: T) => void
  return {
    promise: new Promise<T>((yes) => {
      resolve = yes
    }),
    resolve: (value: T) => resolve(value),
  }
}
afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})
describe('Habitat projection lifetime', () => {
  beforeEach(() => {
    vi.mocked(loadPaimos6SessionZoom).mockRejectedValue(new Error('fixture unavailable'))
  })
  it('synchronously erases rows/selection on authority changes and ignores superseded reads', async () => {
    const first = deferred<ReturnType<typeof habitatFixture>>()
    vi.mocked(loadOrchestration)
      .mockReturnValueOnce(first.promise)
      .mockResolvedValue(habitatFixture())
    vi.mocked(fetchAgentModeSnapshot).mockRejectedValue(new Error('offline'))
    const authority = ref('user-a:1')
    const project = ref<number | null>(null)
    const scope = effectScope()
    const subject = scope.run(() =>
      useHabitat({ authority, project, principal: ref(1), zoom: ref('10') }),
    )!
    authority.value = 'user-b:2'
    expect(subject.snapshot.value).toBeNull()
    await vi.waitFor(() => expect(subject.state.value).toBe('ready'))
    subject.selectedId.value = habitatFixture().fleet.workers[0].harness_session_id
    project.value = 1
    expect(subject.snapshot.value).toBeNull()
    expect(subject.selectedId.value).toBeNull()
    first.resolve(habitatFixture('1'))
    await vi.waitFor(() => expect(subject.state.value).toBe('ready'))
    expect(subject.snapshot.value?.fleet.zoom).toBe('10')
    scope.stop()
  })
  it('clears stale truth on failed refresh and exposes concealed authorization failures', async () => {
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture())
    vi.mocked(fetchAgentModeSnapshot).mockRejectedValue(new Error('offline'))
    const scope = effectScope()
    const subject = scope.run(() =>
      useHabitat({ authority: ref('a'), project: ref(null), principal: ref(1), zoom: ref('10') }),
    )!
    await vi.waitFor(() => expect(subject.state.value).toBe('ready'))
    subject.selectedId.value = habitatFixture().fleet.workers[0].harness_session_id
    vi.mocked(loadOrchestration).mockRejectedValueOnce(new ApiError(404, 'concealed'))
    await subject.refresh()
    expect(subject.state.value).toBe('unauthorized')
    expect(subject.snapshot.value).toBeNull()
    expect(subject.selectedWorker.value).toBeNull()
    scope.stop()
  })

  it('checks every included project within the bounded portfolio sample', async () => {
    const snapshot = habitatFixture('100')
    snapshot.project_coordination = Array.from({ length: 11 }, (_, index) => ({
      ...snapshot.project_coordination[0]!,
      project: {
        id: index + 1,
        key: `P${String(index + 1).padStart(2, '0')}`,
        name: `Project ${index + 1}`,
      },
    }))
    snapshot.coordination_bounds = {
      sample_limit: 100,
      total_projects: 11,
      sampled_projects: 11,
      omitted_projects: 0,
    }
    vi.mocked(loadOrchestration).mockResolvedValue(snapshot)
    vi.mocked(fetchAgentModeSnapshot).mockResolvedValue({ deliveries: [] } as never)
    vi.mocked(loadPaimos6SessionZoom).mockImplementation(
      async (projectId) =>
        ({
          totals: {
            sessions: projectId === 11 ? 1 : 0,
            unread: 0,
            attention_sessions: projectId === 11 ? 1 : 0,
            exception_messages: projectId === 11 ? 1 : 0,
            action_requests: projectId === 11 ? 1 : 0,
            exception_targets: projectId === 11 ? 1 : 0,
            sampled_exception_targets: projectId === 11 ? 1 : 0,
          },
        }) as never,
    )
    const scope = effectScope()
    const subject = scope.run(() =>
      useHabitat({ authority: ref('a'), project: ref(null), principal: ref(1), zoom: ref('100') }),
    )!

    await vi.waitFor(() => expect(subject.messageState.value).toBe('ready'))
    expect(loadPaimos6SessionZoom).toHaveBeenCalledTimes(11)
    expect(subject.messageAttention.value).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          projectId: 11,
          totals: expect.objectContaining({ action_requests: 1, exception_messages: 1 }),
        }),
      ]),
    )
    scope.stop()
  })
})
