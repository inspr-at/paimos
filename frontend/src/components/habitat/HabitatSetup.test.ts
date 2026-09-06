import { nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError } from '@/api/client'
import { mountComponent } from '@/components/ai/testMount'
import HabitatSetup from './HabitatSetup.vue'
import { habitatFixture } from './__fixtures__/orchestration'
vi.mock('./habitatLifecycle', () => ({ loadHabitatRuntimes: vi.fn().mockResolvedValue([]) }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: { project: '1', view: 'assign' } }),
  useRouter: () => ({ replace: vi.fn() }),
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSuperAdmin: true }) }))
afterEach(() => {
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})
const findButton = (el: HTMLElement, text: string) =>
  [...el.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === text)!
describe('Habitat root setup', () => {
  it('performs real root CAS only after confirmation and refuses stale revision success', async () => {
    const fixture = habitatFixture()
    vi.spyOn(api, 'get').mockImplementation(async (path) => {
      if (path === '/health')
        return {
          agent_bus_identity_enforced: true,
          agent_bus_instance: 'fixture',
          deployment_instance: 'fixture',
        } as never
      if (path.includes('execution-options'))
        return { dispatch_profiles: [fixture.fleet.workers[0].dispatch_profile] } as never
      if (path.includes('/agents')) return [{ project_id: 1, name: 'coordinator' }] as never
      return { schema_version: 1, revision: 0, orchestrator: null, updated_at: null } as never
    })
    const put = vi.spyOn(api, 'put').mockRejectedValue(new ApiError(409, 'revision_conflict'))
    const mounted = await mountComponent(HabitatSetup, {
      projects: fixture.project_coordination,
      projectId: 1,
      authority: 'a',
      root: fixture.instance_root,
    })
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('coordinator'))
    const selects = [...mounted.el.querySelectorAll<HTMLSelectElement>('select')]
    selects[1].value = 'coordinator'
    selects[1].dispatchEvent(new Event('change'))
    const label = mounted.el.querySelector<HTMLInputElement>(
      'input[placeholder="Human-readable orchestrator name"]',
    )!
    label.value = 'Root'
    label.dispatchEvent(new Event('input'))
    await nextTick()
    findButton(mounted.el, 'Review root binding').click()
    await nextTick()
    expect(put).not.toHaveBeenCalled()
    findButton(mounted.el, 'Confirm root binding').click()
    await vi.waitFor(() =>
      expect(put).toHaveBeenCalledWith(
        '/orchestrator/v1/config',
        {
          expected_revision: 0,
          orchestrator: { project_id: 1, key: 'coordinator', display_label: 'Root' },
        },
        expect.any(Object),
      ),
    )
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('root binding changed'))
    expect(mounted.el.textContent).not.toContain('Root identity saved')
    expect(mounted.el.textContent).toContain('Guided CLI fallback')
    await mounted.unmount()
  })
})
