import { reactive, nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountComponent } from '@/components/ai/testMount'
import { habitatFixture } from './__fixtures__/orchestration'
import HabitatHome from './HabitatHome.vue'
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  RouterLink: { props: ['to'], template: '<a><slot /></a>' },
}))
vi.mock('./HabitatRuntimeHealth.vue', () => ({ default: { template: '<div />' } }))
afterEach(() => {
  document.body.innerHTML = ''
})
describe('Home presents actual next steps', () => {
  it('distinguishes missing identity, missing run, and uncertain coordinator without inventing workers', async () => {
    const props = reactive({
      snapshot: habitatFixture('10', null, true),
      deliveries: [],
      messages: [],
      messageState: 'ready' as const,
      deliveryState: 'ready' as const,
      fresh: true,
      authority: 'fixture:1',
    })
    const mounted = await mountComponent(HabitatHome, props)
    expect(mounted.el.textContent).toContain('Set up coordinator')
    expect(mounted.el.textContent).toContain('No workers yet')
    expect(mounted.el.querySelector('.habitat-roster-row')).toBeNull()
    expect(mounted.el.querySelector('.habitat-welcome-art')?.getAttribute('aria-hidden')).toBe(
      'true',
    )
    props.snapshot.instance_root.configured_identity =
      habitatFixture().instance_root.configured_identity
    props.snapshot.instance_root.active_generation.state = 'unset'
    props.snapshot.instance_root.active_generation.reason = 'no_active_root_generation'
    await nextTick()
    expect(mounted.el.textContent).toContain('Start coordinator')
    expect(mounted.el.textContent).not.toContain('Set up a worker')
    props.snapshot.instance_root.active_generation.state = 'unknown'
    props.snapshot.instance_root.active_generation.reason = 'root_generation_unknown'
    await nextTick()
    expect(mounted.el.textContent).toContain('Review coordinator')
    expect(mounted.el.textContent).not.toContain('connected')
    await mounted.unmount()
  })
  it('labels unavailable attention and retained dead workers without claiming an all-clear or active work', async () => {
    const snapshot = habitatFixture()
    snapshot.fleet.workers[0].liveness.state = 'dead'
    const mounted = await mountComponent(HabitatHome, {
      snapshot,
      deliveries: [],
      messages: [{ projectId: 1, totals: null }],
      messageState: 'ready',
      deliveryState: 'unavailable',
      fresh: true,
      authority: 'fixture:1',
    })
    expect(mounted.el.textContent).toContain('Some attention is unknown')
    expect(mounted.el.textContent).toContain('Your workers')
    expect(mounted.el.textContent).toContain('Disconnected')
    expect(mounted.el.textContent).not.toContain('Nothing needs your attention')
    expect(mounted.el.textContent).not.toContain('At work')
    await mounted.unmount()
  })
})
