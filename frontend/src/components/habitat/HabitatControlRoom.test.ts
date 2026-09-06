import { reactive, nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountComponent } from '@/components/ai/testMount'
import { loadOrchestration } from '@/services/orchestration'
import { fetchAgentModeSnapshot, type AgentModeSnapshot } from '@/services/agentMode'
import { habitatFixture } from './__fixtures__/orchestration'
const context = vi.hoisted(() => ({
  route: { query: { view: 'workers' } as Record<string, string> },
  replace: vi.fn(),
}))
vi.mock('vue-router', () => ({
  useRoute: () => context.route,
  useRouter: () => ({ replace: context.replace }),
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { id: 1, role: 'member', status: 'active' },
    allProjects: true,
    accessibleProjects: new Map(),
    canEdit: () => true,
  }),
}))
vi.mock('@/services/orchestration', () => ({ loadOrchestration: vi.fn() }))
vi.mock('@/services/agentMode', () => ({ fetchAgentModeSnapshot: vi.fn() }))
vi.mock('@/v6/sessionHomeZoom', () => ({
  loadPaimos6SessionZoom: vi.fn().mockResolvedValue({
    totals: { sessions: 0, exception_messages: 0, action_requests: 0, attention_sessions: 0 },
  }),
}))
import HabitatControlRoom from './HabitatControlRoom.vue'
afterEach(() => {
  vi.clearAllMocks()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})
describe('Habitat production composition', () => {
  it('makes a compact inspector modal and restores its worker trigger on Escape', async () => {
    vi.stubGlobal(
      'matchMedia',
      vi.fn(() => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() })),
    )
    context.route = reactive({ query: { view: 'workers' } })
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture())
    vi.mocked(fetchAgentModeSnapshot).mockResolvedValue({
      deliveries: [],
      aggregates: null,
    } as unknown as AgentModeSnapshot)
    const mounted = await mountComponent(HabitatControlRoom)
    await vi.waitFor(() =>
      expect(mounted.el.querySelector('.habitat-worker-select')).not.toBeNull(),
    )
    const trigger = mounted.el.querySelector<HTMLButtonElement>('.habitat-worker-select')!
    trigger.focus()
    trigger.click()
    await nextTick()
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Inspector"]')?.getAttribute('aria-modal')).toBe(
      'true',
    )
    expect(mounted.el.querySelector<HTMLElement>('.habitat-stage')?.inert).toBe(true)
    const close = mounted.el.querySelector<HTMLButtonElement>('[aria-label="Close inspector"]')!
    expect(document.activeElement).toBe(close)
    close.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await nextTick()
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Inspector"]')).toBeNull()
    expect(mounted.el.querySelector<HTMLElement>('.habitat-stage')?.inert).toBe(false)
    expect(document.activeElement).toBe(trigger)
    await mounted.unmount()
  })
  it('keeps explicit hierarchy and selected button focus through an ordinary live update', async () => {
    context.route = reactive({ query: { view: 'workers' } })
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture())
    vi.mocked(fetchAgentModeSnapshot).mockResolvedValue({
      deliveries: [],
      aggregates: null,
    } as unknown as AgentModeSnapshot)
    const mounted = await mountComponent(HabitatControlRoom)
    await vi.waitFor(() => expect(mounted.el.querySelector('[data-worker-id]')).not.toBeNull())
    expect(mounted.el.querySelectorAll('[data-worker-id]')).toHaveLength(1)
    mounted.el
      .querySelector<HTMLButtonElement>('[aria-label="Expand coordinator descendants"]')!
      .click()
    await nextTick()
    expect(mounted.el.querySelectorAll('[data-worker-id]')).toHaveLength(2)
    const select = mounted.el.querySelector<HTMLButtonElement>('.habitat-worker-select')!
    select.click()
    select.focus()
    await nextTick()
    expect(mounted.el.querySelector('[aria-label="Inspector"]')?.textContent).toContain(
      'coordinator',
    )
    const refresh = [...mounted.el.querySelectorAll<HTMLButtonElement>('button')].find(
      (b) => b.textContent?.trim() === 'Refresh',
    )!
    refresh.click()
    await vi.waitFor(() => expect(loadOrchestration).toHaveBeenCalledTimes(2))
    await nextTick()
    expect(document.activeElement).toBe(select)
    await mounted.unmount()
  })
  it('offers recovery for an empty worker tree and never invents an idle presence', async () => {
    context.route = reactive({ query: { view: 'workers' } })
    vi.mocked(loadOrchestration).mockResolvedValue(habitatFixture('10', null, true))
    vi.mocked(fetchAgentModeSnapshot).mockResolvedValue({
      deliveries: [],
      aggregates: null,
    } as unknown as AgentModeSnapshot)
    const mounted = await mountComponent(HabitatControlRoom)
    await vi.waitFor(() =>
      expect(mounted.el.textContent).toContain('No worker generations in this sample'),
    )
    expect(mounted.el.querySelectorAll('[data-worker-id]')).toHaveLength(0)
    expect(mounted.el.textContent).toContain('Set up a worker')
    expect(mounted.el.textContent).not.toContain('Idle')
    await mounted.unmount()
  })
})
