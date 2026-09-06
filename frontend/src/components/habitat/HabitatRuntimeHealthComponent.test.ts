import { reactive, nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountComponent } from '@/components/ai/testMount'
import { loadRuntimeHealth, type RuntimeHealthPage } from './habitatRuntimeHealth'
import HabitatRuntimeHealth from './HabitatRuntimeHealth.vue'
vi.mock('./habitatRuntimeHealth', () => ({ loadRuntimeHealth: vi.fn() }))
afterEach(() => {
  vi.clearAllMocks()
  document.body.innerHTML = ''
})
const page = (): RuntimeHealthPage => ({
  schema_version: 1,
  observed_at: new Date().toISOString(),
  runtimes: [
    {
      runtime_id: '00000000-0000-4000-8000-000000000001',
      runtime_generation: '00000000-0000-4000-8000-000000000002',
      machine_id: 'fixture-machine',
      status: 'fresh',
      expires_at: new Date(Date.now() + 120000).toISOString(),
      layers: (['reporter', 'primary', 'fallback', 'attention'] as const).map((layer) => ({
        layer,
        state: 'healthy',
        status: 'fresh',
        reason: 'recovered',
        failure_count: 0,
        updated_at: new Date().toISOString(),
      })),
    },
  ],
})
describe('Connection health presentation', () => {
  it('withdraws healthy claims when the workspace snapshot is stale or the source report is old', async () => {
    vi.mocked(loadRuntimeHealth).mockResolvedValue(page())
    const props = reactive({ projectIds: [1], authority: 'human:1', fresh: true })
    const mounted = await mountComponent(HabitatRuntimeHealth, props)
    await vi.waitFor(() =>
      expect(mounted.el.textContent).toContain('Reported connections are responding'),
    )
    props.fresh = false
    await nextTick()
    expect(mounted.el.textContent).toContain('Connection status is unconfirmed')
    expect(mounted.el.textContent).not.toContain('are responding')
    props.fresh = true
    const old = page()
    old.observed_at = new Date(Date.now() - 60000).toISOString()
    vi.mocked(loadRuntimeHealth).mockResolvedValue(old)
    props.authority = 'human:1:revision2'
    await vi.waitFor(() => expect(loadRuntimeHealth).toHaveBeenCalledTimes(2))
    await nextTick()
    expect(mounted.el.textContent).toContain('Connection status is unconfirmed')
    await mounted.unmount()
  })
  it('drops late responses after an authority change and never calls missing reports healthy', async () => {
    let resolve!: (page: RuntimeHealthPage) => void
    vi.mocked(loadRuntimeHealth).mockImplementationOnce(
      () =>
        new Promise((done) => {
          resolve = done
        }),
    )
    const props = reactive({ projectIds: [1], authority: 'human:1', fresh: true })
    const mounted = await mountComponent(HabitatRuntimeHealth, props)
    vi.mocked(loadRuntimeHealth).mockResolvedValue({
      schema_version: 1,
      observed_at: new Date().toISOString(),
      runtimes: [],
    })
    props.authority = 'human:2'
    await vi.waitFor(() =>
      expect(mounted.el.textContent).toContain('No runtime reports in this view'),
    )
    resolve(page())
    await nextTick()
    await nextTick()
    expect(mounted.el.textContent).not.toContain('fixture-machine')
    expect(mounted.el.textContent).not.toContain('are responding')
    await mounted.unmount()
  })
})
