import { nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountComponent } from '@/components/ai/testMount'
import { habitatFixture } from './__fixtures__/orchestration'
import {
  loadHabitatRuntimes,
  submitHabitatIntent,
  loadHabitatIntent,
  type HabitatIntent,
} from './habitatLifecycle'
import HabitatLifecycle from './HabitatLifecycle.vue'
import { api } from '@/api/client'
import { writeIntentRecovery } from './habitatRecovery'
import { loadOrchestration } from '@/services/orchestration'
vi.mock('@/services/orchestration', () => ({ loadOrchestration: vi.fn() }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 1 }, isSuperAdmin: true, impersonation: null }),
}))
vi.mock('./habitatLifecycle', async (original) => ({
  ...(await original<typeof import('./habitatLifecycle')>()),
  loadHabitatRuntimes: vi.fn(),
  submitHabitatIntent: vi.fn(),
  loadHabitatIntent: vi.fn(),
  cancelHabitatIntent: vi.fn(),
}))
afterEach(() => {
  vi.restoreAllMocks()
  vi.clearAllMocks()
  document.body.innerHTML = ''
  sessionStorage.clear()
})
const id = (n: string) => `00000000-0000-4000-8000-${n.padStart(12, '0')}`
const button = (el: HTMLElement, text: string) =>
  [...el.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === text)!
describe('Habitat browser lifecycle', () => {
  it('requires review and retains an exact ambiguous request key before showing a failed outcome', async () => {
    vi.spyOn(api, 'getWithMeta').mockResolvedValue({
      data: { issues: [], has_more: false },
      status: 200,
      permissionsEpoch: '1',
      permissionsEpochGeneration: 0,
      etag: null,
      lastModified: null,
    })
    const profile = habitatFixture().fleet.workers[0].dispatch_profile!
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture('100', 1))
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([
      {
        id: id('1'),
        project_id: 1,
        generation: id('2'),
        machine_id: 'fixture-machine',
        account_label: 'chatgpt',
        workspaces: [{ handle: id('3'), identity: 'a'.repeat(64) }],
        profiles: [{ id: profile.id, version: profile.version }],
        sessions: [],
        expires_at: new Date(Date.now() + 120000).toISOString(),
      },
    ])
    vi.mocked(submitHabitatIntent).mockRejectedValueOnce(new Error('lost acknowledgement'))
    const mounted = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
    })
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('fixture-machine'))
    let selects = mounted.el.querySelectorAll<HTMLSelectElement>('select')
    selects[0].value = id('1')
    selects[0].dispatchEvent(new Event('change'))
    await nextTick()
    selects = mounted.el.querySelectorAll<HTMLSelectElement>('select')
    selects[2].value = id('3')
    selects[2].dispatchEvent(new Event('change'))
    await nextTick()
    button(mounted.el, 'Review start request').click()
    await nextTick()
    expect(submitHabitatIntent).not.toHaveBeenCalled()
    button(mounted.el, 'Cancel review').click()
    await nextTick()
    expect(document.activeElement).toBe(button(mounted.el, 'Review start request'))
    button(mounted.el, 'Review start request').click()
    await nextTick()
    button(mounted.el, 'Confirm exact request').click()
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('No outcome confirmed'))
    expect(button(mounted.el, 'Cancel review')).toBeUndefined()
    const request = vi.mocked(submitHabitatIntent).mock.calls[0][1]
    const pending: HabitatIntent = {
      id: id('4'),
      projectId: 1,
      state: 'requested',
      revision: 1,
      reason: '',
      createdAt: '2026-09-06T12:00:00Z',
      updatedAt: '2026-09-06T12:00:00Z',
      expiresAt: '2026-09-06T12:02:00Z',
      newGeneration: id('5'),
      resultSessionId: null,
    }
    vi.mocked(submitHabitatIntent).mockResolvedValueOnce(pending)
    button(mounted.el, 'Confirm exact request').click()
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('Request recorded'))
    expect(vi.mocked(submitHabitatIntent).mock.calls[1][1]).toEqual(request)
    expect(mounted.el.textContent).not.toContain('Runtime completion recorded')
    vi.mocked(loadHabitatIntent).mockResolvedValueOnce({
      ...pending,
      state: 'failed',
      revision: 2,
      reason: 'unsupported',
    })
    button(mounted.el, 'Refresh intent evidence').click()
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('The intent failed'))
    button(mounted.el, 'Finish reviewing this result').click()
    await vi.waitFor(() => expect(loadHabitatRuntimes).toHaveBeenCalledTimes(2))
    await vi.waitFor(() =>
      expect(document.activeElement).toBe(button(mounted.el, 'Review start request')),
    )
    await mounted.unmount()
  })
  it('re-reads a saved receipt after navigation without repeating its mutation', async () => {
    vi.spyOn(api, 'getWithMeta').mockResolvedValue({
      data: { issues: [] },
      status: 200,
      permissionsEpoch: '1',
      permissionsEpochGeneration: 0,
      etag: null,
      lastModified: null,
    })
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture('100', 1))
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([])
    const request = {
      request_key: id('11'),
      operation: 'start' as const,
      runtime_id: id('12'),
      runtime_generation: id('13'),
      account_label: 'chatgpt',
      ttl_seconds: 120,
      workspace_handle: id('14'),
      agent_name: 'coordinator',
      dispatch_profile_id: 'fixture-profile',
      dispatch_profile_version: '1',
      ticket_id: null,
      work_shape: 'unknown' as const,
      role: 'coordinator' as const,
      parent_harness_session_id: null,
    }
    expect(
      writeIntentRecovery(
        { origin: location.origin, instance: 'fixture', principalId: 1, projectId: 1 },
        { intentId: id('15'), request, savedAt: Date.now() },
      ),
    ).toBe(true)
    vi.mocked(loadHabitatIntent).mockResolvedValue({
      id: id('15'),
      projectId: 1,
      state: 'requested',
      revision: 1,
      reason: '',
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 120000).toISOString(),
      newGeneration: id('16'),
      resultSessionId: null,
    })
    const mounted = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile: habitatFixture().fleet.workers[0].dispatch_profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
    })
    await vi.waitFor(() =>
      expect(loadHabitatIntent).toHaveBeenCalledWith(1, id('15'), request, expect.any(AbortSignal)),
    )
    expect(submitHabitatIntent).not.toHaveBeenCalled()
    expect(mounted.el.textContent).toContain('Request status refreshed')
    expect(mounted.el.textContent).not.toContain('Runtime completion recorded')
    await mounted.unmount()
  })
})
