import { nextTick, reactive } from 'vue'
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
const combobox = (el: HTMLElement, label: string) =>
  [...el.querySelectorAll<HTMLSelectElement>('select')].find(
    (select) =>
      select.getAttribute('aria-label') === label ||
      select.closest('label')?.textContent?.trim().startsWith(label),
  )!

function ownedLifecycleFixture(phase: 'working' | 'stopped') {
  const snapshot = habitatFixture('100', 1)
  const coordinator = snapshot.fleet.workers[0]!
  const worker = snapshot.fleet.workers[1]!
  worker.management_mode = 'managed'
  worker.runtime_provenance_trust = 'managed_reporter'
  worker.machine_id = coordinator.machine_id
  worker.account_label = coordinator.account_label
  worker.dispatch_profile = structuredClone(coordinator.dispatch_profile)
  worker.phase = phase
  worker.liveness = {
    state: phase === 'stopped' ? 'dead' : 'idle',
    reason: phase === 'stopped' ? 'process_exited' : 'turn_complete',
    observed_at: '2026-09-06T12:00:00Z',
    source: 'agentd_reporter',
    reporter_age_seconds: 1,
  }
  worker.work_shape = 'ship'
  return { snapshot, coordinator, worker }
}

function mockTicketList() {
  vi.spyOn(api, 'getWithMeta').mockResolvedValue({
    data: {
      issues: [
        {
          id: 917,
          project_id: 1,
          issue_key: 'PAI-917',
          title: 'Fast iteration and runtime lifecycle',
        },
      ],
      total: 1,
      offset: 0,
      limit: 50,
      has_more: false,
    },
    status: 200,
    permissionsEpoch: '0',
    permissionsEpochGeneration: 0,
    etag: null,
    lastModified: null,
  })
}

function ownedRuntime(
  active: ReturnType<typeof ownedLifecycleFixture>,
  workspace: string,
  sessions = [
    {
      session_id: active.coordinator.harness_session_id,
      generation: id('33'),
      workspace_handle: workspace,
    },
    {
      session_id: active.worker.harness_session_id,
      generation: id('34'),
      workspace_handle: workspace,
    },
  ],
) {
  const profile = active.worker.dispatch_profile!
  return {
    id: id('31'),
    project_id: 1,
    generation: id('32'),
    machine_id: active.worker.machine_id!,
    account_label: active.worker.account_label,
    workspaces: [{ handle: workspace, identity: 'b'.repeat(64) }],
    profiles: [{ id: profile.id, version: profile.version }],
    sessions,
    expires_at: new Date(Date.now() + 120000).toISOString(),
  }
}

describe('Habitat browser lifecycle', () => {
  it('makes attach, listener repair and restart reviewable from exact owned evidence', async () => {
    vi.spyOn(api, 'getWithMeta').mockResolvedValue({
      data: {
        issues: [
          {
            id: 917,
            project_id: 1,
            issue_key: 'PAI-917',
            title: 'Fast iteration and runtime lifecycle',
          },
        ],
        total: 1,
        offset: 0,
        limit: 50,
        has_more: false,
      },
      status: 200,
      permissionsEpoch: '0',
      permissionsEpochGeneration: 0,
      etag: null,
      lastModified: null,
    })
    const active = ownedLifecycleFixture('working')
    const profile = active.worker.dispatch_profile!
    const workspace = id('30')
    const runtime = {
      id: id('31'),
      project_id: 1,
      generation: id('32'),
      machine_id: active.worker.machine_id!,
      account_label: active.worker.account_label,
      workspaces: [{ handle: workspace, identity: 'b'.repeat(64) }],
      profiles: [{ id: profile.id, version: profile.version }],
      sessions: [
        {
          session_id: active.coordinator.harness_session_id,
          generation: id('33'),
          workspace_handle: workspace,
        },
        {
          session_id: active.worker.harness_session_id,
          generation: id('34'),
          workspace_handle: workspace,
        },
      ],
      expires_at: new Date(Date.now() + 120000).toISOString(),
    }
    vi.mocked(loadOrchestration).mockResolvedValue(active.snapshot)
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([runtime])
    const mounted = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
      selectedSessionId: active.worker.harness_session_id,
    })

    await vi.waitFor(() => expect(combobox(mounted.el, 'Operation').value).toBe('reassign'))
    const operation = combobox(mounted.el, 'Operation')
    operation.value = 'attach'
    operation.dispatchEvent(new Event('change'))
    await nextTick()
    const worker = combobox(mounted.el, 'Lifecycle worker')
    worker.value = active.worker.harness_session_id
    worker.dispatchEvent(new Event('change'))
    await vi.waitFor(() => expect(combobox(mounted.el, 'Ticket').disabled).toBe(false))
    const ticket = combobox(mounted.el, 'Ticket')
    ticket.value = '917'
    ticket.dispatchEvent(new Event('change'))
    await nextTick()
    const parent = combobox(mounted.el, 'Parent worker')
    parent.value = active.coordinator.harness_session_id
    parent.dispatchEvent(new Event('change'))
    await nextTick()
    expect(button(mounted.el, 'Review attach request').disabled).toBe(false)

    const repairOperation = combobox(mounted.el, 'Operation')
    repairOperation.value = 'repair'
    repairOperation.dispatchEvent(new Event('change'))
    await nextTick()
    const layer = combobox(mounted.el, 'Repair layer')
    layer.value = 'listeners'
    layer.dispatchEvent(new Event('change'))
    await nextTick()
    expect(button(mounted.el, 'Review repair request').disabled).toBe(false)
    await mounted.unmount()

    const stopped = ownedLifecycleFixture('stopped')
    vi.mocked(loadOrchestration).mockResolvedValue(stopped.snapshot)
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([
      {
        ...runtime,
        sessions: [
          {
            session_id: stopped.worker.harness_session_id,
            generation: id('35'),
            workspace_handle: workspace,
          },
        ],
      },
    ])
    const restart = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
      selectedSessionId: stopped.worker.harness_session_id,
    })
    await vi.waitFor(() => expect(combobox(restart.el, 'Operation').value).toBe('restart'))
    await vi.waitFor(() =>
      expect(button(restart.el, 'Review restart request').disabled).toBe(false),
    )
    expect(combobox(restart.el, 'Lifecycle worker').value).toBe(stopped.worker.harness_session_id)
    await restart.unmount()
  })

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

  it('refreshes an expired advertisement without losing the selected draft', async () => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
    const snapshot = habitatFixture('100', 1)
    const profile = snapshot.fleet.workers[0]!.dispatch_profile!
    const workspace = id('41')
    const firstRuntime = {
      id: id('42'),
      project_id: 1,
      generation: id('43'),
      machine_id: 'fixture-machine',
      account_label: 'chatgpt' as const,
      workspaces: [{ handle: workspace, identity: 'a'.repeat(64) }],
      profiles: [{ id: profile.id, version: profile.version }],
      sessions: [],
      expires_at: new Date(Date.now() + 120000).toISOString(),
    }
    const refreshedRuntime = {
      ...firstRuntime,
      generation: id('44'),
      expires_at: new Date(Date.now() + 300000).toISOString(),
    }
    vi.mocked(loadOrchestration).mockResolvedValue(snapshot)
    vi.mocked(loadHabitatRuntimes)
      .mockResolvedValueOnce([firstRuntime])
      .mockResolvedValue([refreshedRuntime])
    const mounted = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
    })
    await vi.waitFor(() => expect(combobox(mounted.el, 'Runtime').value).toBe(''))
    const runtimeSelect = combobox(mounted.el, 'Runtime')
    runtimeSelect.value = firstRuntime.id
    runtimeSelect.dispatchEvent(new Event('change'))
    await nextTick()
    const workspaceSelect = combobox(mounted.el, 'Workspace')
    workspaceSelect.value = workspace
    workspaceSelect.dispatchEvent(new Event('change'))
    await nextTick()
    expect(button(mounted.el, 'Review start request').disabled).toBe(false)
    button(mounted.el, 'Review start request').click()
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Lifecycle request review"]')).not.toBeNull()

    await vi.advanceTimersByTimeAsync(130000)
    await nextTick()

    expect(loadHabitatRuntimes).toHaveBeenCalledTimes(2)
    expect(combobox(mounted.el, 'Runtime').value).toBe(firstRuntime.id)
    expect(combobox(mounted.el, 'Workspace').value).toBe(workspace)
    expect(mounted.el.textContent).not.toContain('This runtime advertisement expired')
    expect(mounted.el.querySelector('[aria-label="Lifecycle request review"]')).toBeNull()
    expect(button(mounted.el, 'Refresh runtime evidence')).toBeDefined()
    await mounted.unmount()
  })

  it('clears an unattempted review when authority changes and never submits its stale request', async () => {
    const props = reactive({
      projectId: 1,
      agent: 'coordinator',
      profile: habitatFixture().fleet.workers[0]!.dispatch_profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
    })
    const snapshot = habitatFixture('100', 1)
    const runtime = {
      id: id('51'),
      project_id: 1,
      generation: id('52'),
      machine_id: 'fixture-machine',
      account_label: 'chatgpt' as const,
      workspaces: [{ handle: id('53'), identity: 'b'.repeat(64) }],
      profiles: [{ id: props.profile!.id, version: props.profile!.version }],
      sessions: [],
      expires_at: new Date(Date.now() + 120000).toISOString(),
    }
    vi.mocked(loadOrchestration).mockResolvedValue(snapshot)
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([runtime])
    const mounted = await mountComponent(HabitatLifecycle, props)
    await vi.waitFor(() => expect(combobox(mounted.el, 'Runtime')).toBeDefined())
    const runtimeSelect = combobox(mounted.el, 'Runtime')
    runtimeSelect.value = runtime.id
    runtimeSelect.dispatchEvent(new Event('change'))
    await nextTick()
    const workspaceSelect = combobox(mounted.el, 'Workspace')
    workspaceSelect.value = runtime.workspaces[0]!.handle
    workspaceSelect.dispatchEvent(new Event('change'))
    await nextTick()
    button(mounted.el, 'Review start request').click()
    await nextTick()
    expect(mounted.el.textContent).toContain('Review start request')

    props.authority = 'human:2'
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Lifecycle request review"]')).toBeNull()
    expect(submitHabitatIntent).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it('keeps an exact review through an unrelated worker heartbeat refresh', async () => {
    const snapshot = habitatFixture('100', 1)
    const profile = snapshot.fleet.workers[0]!.dispatch_profile!
    const workspace = id('61')
    const props = reactive({
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
      workers: structuredClone(snapshot.fleet.workers),
    })
    const runtime = {
      id: id('62'),
      project_id: 1,
      generation: id('63'),
      machine_id: 'fixture-machine',
      account_label: 'chatgpt' as const,
      workspaces: [{ handle: workspace, identity: 'c'.repeat(64) }],
      profiles: [{ id: profile.id, version: profile.version }],
      sessions: [],
      expires_at: new Date(Date.now() + 120000).toISOString(),
    }
    vi.mocked(loadOrchestration).mockResolvedValue(snapshot)
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([runtime])
    const mounted = await mountComponent(HabitatLifecycle, props)
    await vi.waitFor(() => expect(combobox(mounted.el, 'Runtime')).toBeDefined())
    const runtimeSelect = combobox(mounted.el, 'Runtime')
    runtimeSelect.value = runtime.id
    runtimeSelect.dispatchEvent(new Event('change'))
    await nextTick()
    const workspaceSelect = combobox(mounted.el, 'Workspace')
    workspaceSelect.value = workspace
    workspaceSelect.dispatchEvent(new Event('change'))
    await nextTick()
    button(mounted.el, 'Review start request').click()
    await nextTick()
    const exactRequestBefore = mounted.el.querySelector('.habitat-facts')?.textContent
    expect(mounted.el.querySelector('[aria-label="Lifecycle request review"]')).not.toBeNull()

    props.workers[0]!.liveness.reason = 'background_heartbeat'
    await nextTick()
    await vi.waitFor(() => expect(loadHabitatRuntimes).toHaveBeenCalledTimes(2))

    expect(mounted.el.querySelector('[aria-label="Lifecycle request review"]')).not.toBeNull()
    expect(mounted.el.querySelector('.habitat-facts')?.textContent).toBe(exactRequestBefore)
    await mounted.unmount()
  })

  it.each(['reassign', 'attach', 'restart'] as const)(
    'preserves an edited %s draft when refreshed worker objects only report a heartbeat',
    async (operation) => {
      mockTicketList()
      const active = ownedLifecycleFixture(operation === 'restart' ? 'stopped' : 'working')
      const profile = active.worker.dispatch_profile!
      const workspace = id('71')
      const runtime = ownedRuntime(active, workspace)
      const props = reactive({
        projectId: 1,
        agent: 'coordinator',
        profile,
        authority: 'human:1',
        deployment: 'fixture',
        fresh: true,
        selectedSessionId: active.worker.harness_session_id,
        workers: structuredClone(active.snapshot.fleet.workers),
      })
      vi.mocked(loadOrchestration)
        .mockResolvedValueOnce(active.snapshot)
        .mockImplementation(() => Promise.resolve(structuredClone(active.snapshot)))
      vi.mocked(loadHabitatRuntimes).mockResolvedValue([runtime])

      const mounted = await mountComponent(HabitatLifecycle, props)
      await vi.waitFor(() => expect(combobox(mounted.el, 'Runtime').value).toBe(runtime.id))

      if (operation === 'attach') {
        const operationSelect = combobox(mounted.el, 'Operation')
        operationSelect.value = operation
        operationSelect.dispatchEvent(new Event('change'))
        await nextTick()
      } else await vi.waitFor(() => expect(combobox(mounted.el, 'Operation').value).toBe(operation))
      await vi.waitFor(() =>
        expect(combobox(mounted.el, 'Lifecycle worker').value).toBe(
          active.worker.harness_session_id,
        ),
      )
      await vi.waitFor(() => expect(combobox(mounted.el, 'Ticket').disabled).toBe(false))

      const ticket = combobox(mounted.el, 'Ticket')
      ticket.value = '917'
      ticket.dispatchEvent(new Event('change'))
      await nextTick()
      const shape = combobox(mounted.el, 'Work shape')
      shape.value = 'scout'
      shape.dispatchEvent(new Event('change'))
      await nextTick()
      const parent = combobox(mounted.el, 'Parent worker')
      parent.value = active.coordinator.harness_session_id
      parent.dispatchEvent(new Event('change'))
      await nextTick()

      props.workers[0]!.liveness.reason = 'background_heartbeat'
      await nextTick()
      await vi.waitFor(() => expect(loadOrchestration).toHaveBeenCalledTimes(2))

      expect(combobox(mounted.el, 'Ticket').value).toBe('917')
      expect(combobox(mounted.el, 'Work shape').value).toBe('scout')
      expect(combobox(mounted.el, 'Parent worker').value).toBe(
        active.coordinator.harness_session_id,
      )
      await mounted.unmount()
    },
  )

  it('initializes binding fields when the selected worker changes', async () => {
    mockTicketList()
    const active = ownedLifecycleFixture('working')
    const second = structuredClone(active.worker)
    second.harness_session_id = id('72')
    second.ticket = {
      id: 917,
      details_available: true,
      key: 'PAI-917',
      title: 'Fast iteration and runtime lifecycle',
    }
    second.parent_harness_session_id = null
    second.work_shape = 'scout'
    const snapshot = structuredClone(active.snapshot)
    snapshot.fleet.workers.push(second)
    const profile = active.worker.dispatch_profile!
    const workspace = id('73')
    const runtime = ownedRuntime(active, workspace, [
      {
        session_id: active.worker.harness_session_id,
        generation: id('74'),
        workspace_handle: workspace,
      },
      {
        session_id: second.harness_session_id,
        generation: id('75'),
        workspace_handle: workspace,
      },
    ])
    vi.mocked(loadOrchestration).mockResolvedValue(snapshot)
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([runtime])
    const mounted = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
    })
    await vi.waitFor(() => expect(combobox(mounted.el, 'Runtime')).toBeDefined())
    const runtimeSelect = combobox(mounted.el, 'Runtime')
    runtimeSelect.value = runtime.id
    runtimeSelect.dispatchEvent(new Event('change'))
    await nextTick()
    const operation = combobox(mounted.el, 'Operation')
    operation.value = 'reassign'
    operation.dispatchEvent(new Event('change'))
    await nextTick()
    const worker = combobox(mounted.el, 'Lifecycle worker')
    worker.value = second.harness_session_id
    worker.dispatchEvent(new Event('change'))
    await nextTick()
    await vi.waitFor(() => expect(combobox(mounted.el, 'Ticket').value).toBe('917'))
    expect(combobox(mounted.el, 'Work shape').value).toBe('scout')
    expect(combobox(mounted.el, 'Parent worker').value).toBe('')
    await mounted.unmount()
  })

  it('drops an unsubmitted review when the selected worker revision changes', async () => {
    mockTicketList()
    const active = ownedLifecycleFixture('working')
    const profile = active.worker.dispatch_profile!
    const workspace = id('76')
    const runtime = ownedRuntime(active, workspace)
    const props = reactive({
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
      selectedSessionId: active.worker.harness_session_id,
      workers: structuredClone(active.snapshot.fleet.workers),
    })
    const changed = structuredClone(active.snapshot)
    changed.fleet.workers[1]!.revision += 1
    vi.mocked(loadOrchestration)
      .mockResolvedValueOnce(active.snapshot)
      .mockResolvedValueOnce(changed)
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([runtime])
    const mounted = await mountComponent(HabitatLifecycle, props)
    await vi.waitFor(() => expect(combobox(mounted.el, 'Runtime').value).toBe(runtime.id))
    await vi.waitFor(() =>
      expect(combobox(mounted.el, 'Lifecycle worker').value).toBe(active.worker.harness_session_id),
    )
    button(mounted.el, 'Review reassign request').click()
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Lifecycle request review"]')).not.toBeNull()

    props.workers[0]!.liveness.reason = 'background_heartbeat'
    await nextTick()
    await vi.waitFor(() => expect(loadOrchestration).toHaveBeenCalledTimes(2))

    await vi.waitFor(() =>
      expect(mounted.el.querySelector('[aria-label="Lifecycle request review"]')).toBeNull(),
    )
    expect(button(mounted.el, 'Review reassign request')).toBeDefined()
    expect(submitHabitatIntent).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it('lets a human pick among two advertised accounts and invalidates review when the choice changes', async () => {
    mockTicketList()
    const profile = habitatFixture().fleet.workers[0]!.dispatch_profile!
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture('100', 1))
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([
      {
        id: id('51'),
        project_id: 1,
        generation: id('52'),
        machine_id: 'fixture-machine',
        schema_version: 4,
        account_scopes: [
          {
            account_label: 'chatgpt',
            accounts: [
              { key: 'coordinator', label: 'Coordinator' },
              { key: 'personal', label: 'Personal' },
            ],
            profiles: [{ id: profile.id, version: profile.version }],
            attachment_revision: 7,
            account_availability: 'available' as const,
          },
        ],
        workspaces: [{ handle: id('53'), identity: 'a'.repeat(64) }],
        sessions: [],
        expires_at: new Date(Date.now() + 120000).toISOString(),
      },
    ])
    const mounted = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
    })
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('fixture-machine'))
    combobox(mounted.el, 'Runtime').value = id('51')
    combobox(mounted.el, 'Runtime').dispatchEvent(new Event('change'))
    await nextTick()
    const account = combobox(mounted.el, 'Named account')
    expect(account.value).toBe('')
    expect(button(mounted.el, 'Review start request').disabled).toBe(true)
    account.value = 'chatgpt\0coordinator'
    account.dispatchEvent(new Event('change'))
    await nextTick()
    combobox(mounted.el, 'Workspace').value = id('53')
    combobox(mounted.el, 'Workspace').dispatchEvent(new Event('change'))
    await nextTick()
    expect(button(mounted.el, 'Review start request').disabled).toBe(false)
    button(mounted.el, 'Review start request').click()
    await nextTick()
    expect(mounted.el.textContent).toContain('coordinator')
    expect(mounted.el.textContent).toContain('Attachment revision')
    expect(mounted.el.textContent).toContain('7')
    account.value = 'chatgpt\0personal'
    account.dispatchEvent(new Event('change'))
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Lifecycle request review"]')).toBeNull()
    expect(submitHabitatIntent).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it('invalidates review when the chosen class+account tuple changes on a v3 runtime', async () => {
    mockTicketList()
    const profile = habitatFixture().fleet.workers[0]!.dispatch_profile!
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture('100', 1))
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([
      {
        id: id('61'),
        project_id: 1,
        generation: id('62'),
        machine_id: 'fixture-machine',
        schema_version: 3,
        workspaces: [{ handle: id('63'), identity: 'a'.repeat(64) }],
        account_scopes: [
          {
            account_label: 'chatgpt',
            accounts: [{ key: 'codex-work', label: 'Work' }],
            profiles: [{ id: profile.id, version: profile.version }],
          },
          {
            account_label: 'api_key',
            accounts: [{ key: 'codex-api', label: 'API' }],
            profiles: [{ id: profile.id, version: profile.version }],
          },
        ],
        sessions: [],
        expires_at: new Date(Date.now() + 120000).toISOString(),
      },
    ])
    const mounted = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
    })
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('fixture-machine'))
    combobox(mounted.el, 'Runtime').value = id('61')
    combobox(mounted.el, 'Runtime').dispatchEvent(new Event('change'))
    await nextTick()
    const account = combobox(mounted.el, 'Named account')
    account.value = 'chatgpt\0codex-work'
    account.dispatchEvent(new Event('change'))
    await nextTick()
    combobox(mounted.el, 'Workspace').value = id('63')
    combobox(mounted.el, 'Workspace').dispatchEvent(new Event('change'))
    await nextTick()
    button(mounted.el, 'Review start request').click()
    await nextTick()
    expect(mounted.el.textContent).toContain('Work')
    account.value = 'api_key\0codex-api'
    account.dispatchEvent(new Event('change'))
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Lifecycle request review"]')).toBeNull()
    expect(submitHabitatIntent).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it('shows committed empty account availability without offering a launch choice', async () => {
    mockTicketList()
    const profile = habitatFixture().fleet.workers[0]!.dispatch_profile!
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture('100', 1))
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([
      {
        id: id('91'),
        project_id: 1,
        generation: id('92'),
        machine_id: 'fixture-machine',
        schema_version: 4,
        workspaces: [{ handle: id('93'), identity: 'a'.repeat(64) }],
        account_scopes: [
          {
            account_label: 'chatgpt',
            profiles: [{ id: profile.id, version: profile.version }],
            attachment_revision: 9,
            account_availability: 'unavailable',
          },
        ],
        sessions: [],
        expires_at: new Date(Date.now() + 120000).toISOString(),
      },
    ])
    const mounted = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
    })
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('fixture-machine'))
    combobox(mounted.el, 'Runtime').value = id('91')
    combobox(mounted.el, 'Runtime').dispatchEvent(new Event('change'))
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Named account"]')).toBeNull()
    expect(mounted.el.textContent).toContain('No named account is attached')
    expect(button(mounted.el, 'Review start request').disabled).toBe(true)
    expect(submitHabitatIntent).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it('repairs a unique v2 class without a named key and refuses empty mixed-class v3 repair', async () => {
    mockTicketList()
    const profile = habitatFixture().fleet.workers[0]!.dispatch_profile!
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture('100', 1))
    const v2 = {
      id: id('71'),
      project_id: 1,
      generation: id('72'),
      machine_id: 'fixture-machine',
      account_label: 'chatgpt',
      schema_version: 2 as const,
      accounts: [
        { key: 'coordinator', label: 'Coordinator' },
        { key: 'personal', label: 'Personal' },
      ],
      workspaces: [{ handle: id('73'), identity: 'a'.repeat(64) }],
      profiles: [{ id: profile.id, version: profile.version }],
      sessions: [],
      expires_at: new Date(Date.now() + 120000).toISOString(),
    }
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([v2])
    const unique = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
    })
    await vi.waitFor(() => expect(unique.el.textContent).toContain('fixture-machine'))
    combobox(unique.el, 'Runtime').value = id('71')
    combobox(unique.el, 'Runtime').dispatchEvent(new Event('change'))
    await nextTick()
    combobox(unique.el, 'Operation').value = 'repair'
    combobox(unique.el, 'Operation').dispatchEvent(new Event('change'))
    await nextTick()
    expect(button(unique.el, 'Review repair request').disabled).toBe(false)
    expect(() => button(unique.el, 'Review repair request').click()).not.toThrow()
    await nextTick()
    expect(unique.el.querySelector('[aria-label="Lifecycle request review"]')).not.toBeNull()
    expect(unique.el.textContent).toContain('chatgpt')
    expect(submitHabitatIntent).not.toHaveBeenCalled()
    await unique.unmount()

    const v3 = {
      id: id('81'),
      project_id: 1,
      generation: id('82'),
      machine_id: 'fixture-machine',
      schema_version: 3 as const,
      workspaces: [{ handle: id('83'), identity: 'a'.repeat(64) }],
      account_scopes: [
        {
          account_label: 'chatgpt',
          accounts: [{ key: 'codex-work', label: 'Work' }],
          profiles: [{ id: profile.id, version: profile.version }],
        },
        {
          account_label: 'cursor_context',
          accounts: [{ key: 'cursor-op', label: 'Cursor' }],
          profiles: [{ id: 'cursor-composer', version: '1' }],
        },
      ],
      sessions: [],
      expires_at: new Date(Date.now() + 120000).toISOString(),
    }
    vi.mocked(loadHabitatRuntimes).mockResolvedValue([v3])
    const mixed = await mountComponent(HabitatLifecycle, {
      projectId: 1,
      agent: 'coordinator',
      profile,
      authority: 'human:1',
      deployment: 'fixture',
      fresh: true,
    })
    await vi.waitFor(() => expect(mixed.el.textContent).toContain('fixture-machine'))
    combobox(mixed.el, 'Runtime').value = id('81')
    combobox(mixed.el, 'Runtime').dispatchEvent(new Event('change'))
    await nextTick()
    combobox(mixed.el, 'Operation').value = 'repair'
    combobox(mixed.el, 'Operation').dispatchEvent(new Event('change'))
    await nextTick()
    expect(button(mixed.el, 'Review repair request').disabled).toBe(true)
    expect(mixed.el.textContent).toContain('Choose one advertised account')
    expect(() => button(mixed.el, 'Review repair request').click()).not.toThrow()
    await nextTick()
    expect(mixed.el.querySelector('[aria-label="Lifecycle request review"]')).toBeNull()
    const account = combobox(mixed.el, 'Named account')
    account.value = 'chatgpt\0codex-work'
    account.dispatchEvent(new Event('change'))
    await nextTick()
    expect(button(mixed.el, 'Review repair request').disabled).toBe(false)
    expect(() => button(mixed.el, 'Review repair request').click()).not.toThrow()
    await nextTick()
    expect(mixed.el.querySelector('[aria-label="Lifecycle request review"]')).not.toBeNull()
    expect(mixed.el.textContent).toContain('Work')
    expect(submitHabitatIntent).not.toHaveBeenCalled()
    await mixed.unmount()
  })
})
