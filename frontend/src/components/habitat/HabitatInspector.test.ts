import { reactive, nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mountComponent } from '@/components/ai/testMount'
import { habitatFixture } from './__fixtures__/orchestration'
import HabitatInspector from './HabitatInspector.vue'
vi.mock('./habitatControls', async (original) => ({
  ...(await original<typeof import('./habitatControls')>()),
  loadAssignmentHistory: vi.fn().mockResolvedValue({ events: [], next: null }),
}))
vi.mock('vue-router', () => ({ RouterLink: { props: ['to'], template: '<a><slot /></a>' } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ canEdit: () => true }) }))
afterEach(() => {
  document.body.innerHTML = ''
})
const button = (el: HTMLElement, text: string) =>
  [...el.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === text)!
describe('Habitat inspector', () => {
  it('renders source evidence, gates unknown ownership, and focuses explicit destructive confirmation', async () => {
    const fixture = habitatFixture()
    const props = reactive({
      worker: fixture.fleet.workers[0],
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: true,
      authority: 'fixture:1',
    })
    const mounted = await mountComponent(HabitatInspector, props)
    expect(mounted.el.textContent).toContain('Assignment history')
    button(mounted.el, 'Refresh assignment history').click()
    await vi.waitFor(() => expect(mounted.el.textContent).toContain('No assignment changes'))
    expect(button(mounted.el, 'Stop').disabled).toBe(false)
    button(mounted.el, 'Stop').click()
    await nextTick()
    await nextTick()
    expect(document.activeElement?.textContent).toBe('Confirm stop')
    expect(mounted.el.querySelector('[aria-label="Confirm stop"]')).not.toBeNull()
    button(mounted.el, 'Cancel').click()
    await nextTick()
    expect(document.activeElement).toBe(button(mounted.el, 'Stop'))
    button(mounted.el, 'Stop').click()
    await nextTick()
    props.worker = fixture.fleet.workers[1]
    await nextTick()
    expect(button(mounted.el, 'Stop').disabled).toBe(true)
    expect(mounted.el.querySelector('[aria-label="Confirm stop"]')).toBeNull()
    expect(mounted.el.textContent).toContain('Ownership or live activity is not proven')
    await mounted.unmount()
  })
  it('does not publish suppressed progress or stale ETA and keeps plain empty evidence', async () => {
    const fixture = habitatFixture()
    fixture.fleet.workers[0].delivery_trust.progress_percent = 77
    fixture.fleet.workers[0].delivery_trust.progress_trusted = false
    const mounted = await mountComponent(HabitatInspector, {
      worker: fixture.fleet.workers[0],
      project: null,
      workers: fixture.fleet.workers,
      deliveries: [],
      fresh: false,
      authority: 'fixture:1',
    })
    expect(mounted.el.textContent).not.toContain('77%')
    expect(button(mounted.el, 'Message').disabled).toBe(true)
    expect(button(mounted.el, 'Restart / repair options')).not.toBeNull()
    await mounted.unmount()
  })
})
