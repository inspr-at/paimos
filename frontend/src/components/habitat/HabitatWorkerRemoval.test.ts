import { nextTick, reactive } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import { mountComponent } from '@/components/ai/testMount'
import { habitatFixture } from './__fixtures__/orchestration'
import { writeIntentRecovery } from './habitatRecovery'
import type { HabitatExistingRequest } from './habitatLifecycle'
import HabitatInspector from './HabitatInspector.vue'

vi.mock('./habitatControls', async (original) => ({
  ...(await original<typeof import('./habitatControls')>()),
  loadAssignmentHistory: vi.fn().mockResolvedValue({ events: [], next: null }),
}))
vi.mock('@/composables/useMicTranscript', () => ({
  useMicTranscript: () => ({
    state: { value: 'idle' },
    level: { value: 0 },
    errorMessage: { value: null },
    isActive: { value: false },
    micSupported: () => true,
    start: vi.fn(),
    finish: vi.fn(),
    stop: vi.fn(),
  }),
}))
vi.mock('@/services/agentModeVoice', () => ({ transcribeAgentModeAudio: vi.fn() }))
vi.mock('@/services/orchestration', () => ({ loadOrchestration: vi.fn() }))
vi.mock('vue-router', () => ({ RouterLink: { props: ['to'], template: '<a><slot /></a>' } }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ canEdit: () => true, user: { id: 1 } }),
}))

afterEach(() => {
  vi.restoreAllMocks()
  sessionStorage.clear()
  document.body.innerHTML = ''
})

const button = (el: HTMLElement, text: string) =>
  [...el.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === text)!
const id = (n: string) => `00000000-0000-4000-8000-${n.padStart(12, '0')}`

function readyFixture() {
  const fixture = habitatFixture('100', 1)
  const worker = fixture.fleet.workers[0]
  worker.capabilities.stop = true
  return { fixture, worker }
}

describe('Habitat worker removal dialog', () => {
  it('contains forward and reverse Tab navigation with one tabbable radio per group', async () => {
    const { fixture, worker } = readyFixture()
    const mounted = await mountComponent(HabitatInspector, {
      worker,
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    const opener = button(mounted.el, 'Remove worker')
    opener.focus()
    opener.click()
    await nextTick()
    await nextTick()

    const layer = document.querySelector('[data-testid="habitat-worker-removal"]') as HTMLElement
    const dialog = layer.querySelector<HTMLElement>('[role="dialog"]')!
    const radios = [...dialog.querySelectorAll<HTMLInputElement>('input[type="radio"]')]
    const finish = radios.find((input) => input.value === 'finish_then_remove')!
    const stop = radios.find((input) => input.value === 'stop_now')!
    const cancel = button(dialog, 'Cancel')
    expect(document.activeElement).toBe(dialog)

    finish.focus()
    dialog.dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'Tab',
        shiftKey: true,
        bubbles: true,
        cancelable: true,
      }),
    )
    expect(document.activeElement).toBe(cancel)

    cancel.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }),
    )
    expect(document.activeElement).toBe(finish)

    stop.click()
    await nextTick()
    cancel.focus()
    cancel.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }),
    )
    expect(document.activeElement).toBe(stop)
    await mounted.unmount()
  })

  it('restores the exact remove entry after Escape and Cancel without late unmount focus', async () => {
    const { fixture, worker } = readyFixture()
    const mounted = await mountComponent(HabitatInspector, {
      worker,
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    const opener = button(mounted.el, 'Remove worker')
    opener.focus()
    opener.click()
    await nextTick()
    await nextTick()
    let dialog = document.querySelector<HTMLElement>('[data-testid="habitat-worker-removal"]')!
    dialog.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )
    await vi.waitFor(() => expect(document.activeElement).toBe(opener))
    expect(document.querySelector('[data-testid="habitat-worker-removal"]')).toBeNull()

    opener.click()
    await nextTick()
    await nextTick()
    dialog = document.querySelector<HTMLElement>('[data-testid="habitat-worker-removal"]')!
    const stop = [...dialog.querySelectorAll<HTMLInputElement>('input[type="radio"]')].find(
      (input) => input.value === 'stop_now',
    )!
    stop.click()
    button(dialog, 'Cancel').click()
    await vi.waitFor(() => expect(document.activeElement).toBe(opener))

    const sentinel = document.createElement('button')
    document.body.appendChild(sentinel)
    opener.click()
    sentinel.focus()
    await mounted.unmount()
    await nextTick()
    expect(document.activeElement).toBe(sentinel)
    sentinel.remove()
  })

  it('refuses Escape while a removal submission is busy', async () => {
    const { fixture, worker } = readyFixture()
    let resolvePost!: (value: unknown) => void
    vi.spyOn(api, 'post').mockReturnValue(new Promise((resolve) => (resolvePost = resolve)))
    const mounted = await mountComponent(HabitatInspector, {
      worker,
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    const opener = button(mounted.el, 'Remove worker')
    opener.focus()
    opener.click()
    await nextTick()
    const dialog = document.querySelector<HTMLElement>('[data-testid="habitat-worker-removal"]')!
    ;[...dialog.querySelectorAll<HTMLInputElement>('input[type="radio"]')]
      .find((input) => input.value === 'stop_now')!
      .click()
    await nextTick()
    button(dialog, 'Confirm removal').click()
    await nextTick()
    expect(button(dialog, 'Cancel').disabled).toBe(true)
    dialog.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )
    expect(document.querySelector('[data-testid="habitat-worker-removal"]')).toBe(dialog)

    resolvePost({
      schema_version: 1,
      state: 'requested',
      control: {
        sequence: 1,
        requested_at: new Date().toISOString(),
        requested_by_user_id: 1,
        id: id('99'),
        harness_session_id: worker.harness_session_id,
        kind: 'stop',
        state: 'pending',
      },
    })
    await vi.waitFor(() =>
      expect(document.querySelector('[data-testid="habitat-worker-removal"]')).toBeNull(),
    )
    await mounted.unmount()
  })

  it('shows finish and stop while keeping handoff unavailable, and cancel does not mutate', async () => {
    const { fixture, worker } = readyFixture()
    const patch = vi.spyOn(api, 'patch')
    const post = vi.spyOn(api, 'post')
    const mounted = await mountComponent(HabitatInspector, {
      worker,
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    button(mounted.el, 'Remove worker').click()
    await nextTick()
    const dialog = document.querySelector('[data-testid="habitat-worker-removal"]') as HTMLElement
    const finish = [...dialog.querySelectorAll<HTMLInputElement>('input[type="radio"]')].find(
      (input) => input.value === 'finish_then_remove',
    )
    const handoff = [...dialog.querySelectorAll<HTMLInputElement>('input[type="radio"]')].find(
      (input) => input.value === 'handoff',
    )
    const draft = [...dialog.querySelectorAll<HTMLInputElement>('input[type="radio"]')].find(
      (input) => input.value === 'draft_only',
    )
    expect(finish?.disabled).toBe(false)
    expect(handoff?.disabled).toBe(true)
    expect(draft?.disabled).toBe(true)
    expect(dialog.textContent).toContain('retire-after-work')
    expect(dialog.textContent).toContain('successor acceptance')
    button(dialog, 'Cancel').click()
    await nextTick()
    expect(patch).not.toHaveBeenCalled()
    expect(post).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it('submits durable finish-current-work retirement through the browser control route', async () => {
    const { fixture, worker } = readyFixture()
    const retirementId = id('98')
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      schema_version: 1,
      retirement: {
        id: retirementId,
        project_id: 1,
        harness_session_id: worker.harness_session_id,
        correlation_id: retirementId,
        kind: 'retire_after_work',
        requested_revision: worker.revision,
        state: 'requested',
        requested_at: new Date().toISOString(),
        owned_stop_receipt: false,
        stopped_generation_proof: false,
      },
    })
    const mounted = await mountComponent(HabitatInspector, {
      worker,
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    button(mounted.el, 'Remove worker').click()
    await nextTick()
    const dialog = document.querySelector('[data-testid="habitat-worker-removal"]') as HTMLElement
    ;[...dialog.querySelectorAll<HTMLLabelElement>('label')]
      .find((label) => label.textContent?.includes('Finish current work, then remove'))!
      .click()
    await nextTick()
    button(dialog, 'Finish, then remove').click()
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith(
      `/projects/1/harness-sessions/${worker.harness_session_id}/controls/v1/retire-after-work`,
      { expected_revision: worker.revision, request_key: expect.any(String) },
      expect.any(Object),
    )
    await mounted.unmount()
  })

  it('discards only a local lifecycle draft without API calls', async () => {
    const { fixture, worker } = readyFixture()
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
      session_id: worker.harness_session_id,
      session_generation: id('5'),
      expected_revision: worker.revision,
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
    const mounted = await mountComponent(HabitatInspector, {
      worker,
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    button(mounted.el, 'Remove worker').click()
    await nextTick()
    const dialog = document.querySelector('[data-testid="habitat-worker-removal"]') as HTMLElement
    ;[...dialog.querySelectorAll<HTMLLabelElement>('label')]
      .find((label) => label.textContent?.includes('Discard local lifecycle review draft'))!
      .click()
    await nextTick()
    button(dialog, 'Discard draft only').click()
    await vi.waitFor(() => expect(sessionStorage.length).toBe(0))
    expect(patch).not.toHaveBeenCalled()
    expect(post).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it('submits stop-now through the real control receipt path', async () => {
    const { fixture, worker } = readyFixture()
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      schema_version: 1,
      state: 'requested',
      control: {
        sequence: 1,
        requested_at: new Date().toISOString(),
        requested_by_user_id: 1,
        id: '00000000-0000-4000-8000-000000000099',
        harness_session_id: worker.harness_session_id,
        kind: 'stop',
        state: 'pending',
      },
    })
    const mounted = await mountComponent(HabitatInspector, {
      worker,
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    button(mounted.el, 'Remove worker').click()
    await nextTick()
    const dialog = document.querySelector('[data-testid="habitat-worker-removal"]') as HTMLElement
    ;[...dialog.querySelectorAll<HTMLLabelElement>('label')]
      .find((label) => label.textContent?.includes('Stop now and preserve history'))!
      .click()
    await nextTick()
    button(dialog, 'Confirm removal').click()
    await vi.waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith(
      `/projects/1/harness-sessions/${worker.harness_session_id}/controls/v1/stop`,
      { expected_revision: worker.revision, request_key: expect.any(String) },
      expect.any(Object),
    )
    await mounted.unmount()
  })

  it('clears stale confirmation when revision changes before submit', async () => {
    const { fixture, worker } = readyFixture()
    const props = reactive({
      worker,
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    const post = vi.spyOn(api, 'post')
    const mounted = await mountComponent(HabitatInspector, props)
    button(mounted.el, 'Remove worker').click()
    await nextTick()
    const dialog = document.querySelector('[data-testid="habitat-worker-removal"]') as HTMLElement
    ;[...dialog.querySelectorAll<HTMLLabelElement>('label')]
      .find((label) => label.textContent?.includes('Stop now and preserve history'))!
      .click()
    await nextTick()
    props.worker = { ...props.worker, revision: props.worker.revision + 1 }
    await nextTick()
    expect(button(dialog, 'Confirm removal').disabled).toBe(true)
    expect(dialog.textContent).toContain('Worker evidence changed')
    expect(post).not.toHaveBeenCalled()
    await mounted.unmount()
  })
})
