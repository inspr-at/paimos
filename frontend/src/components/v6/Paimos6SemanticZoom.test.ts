import { createApp, defineComponent, h, nextTick, ref } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

import Paimos6SourceRail from './Paimos6SourceRail.vue'
import Paimos6ZoomControl from './Paimos6ZoomControl.vue'
import Paimos6ZoomOverview from './Paimos6ZoomOverview.vue'

const mounted: Array<() => void> = []

afterEach(() => {
  while (mounted.length) mounted.pop()?.()
  vi.unstubAllGlobals()
})

function mount(render: () => ReturnType<typeof h>, width = 1200) {
  const root = document.createElement('div')
  root.style.width = `${width}px`
  document.body.append(root)
  const app = createApp(defineComponent({ setup: () => render }))
  app.mount(root)
  mounted.push(() => { app.unmount(); root.remove() })
  return root
}

describe('Paimos 6 semantic zoom controls and source truth (PAI-864)', () => {
  it('renders only Paimos active while dim sources are disabled, static, and non-networked', () => {
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)
    const root = mount(() => h(Paimos6SourceRail))
    const text = root.textContent ?? ''
    expect(text).toContain('Paimos · active source')
    expect(text).toContain('Pharos · dim source')
    expect(text).toContain('Janus · dim source')
    expect(text).not.toContain('Coming Soon')
    expect(root.querySelectorAll('[aria-disabled="true"]')).toHaveLength(2)
    expect(root.querySelector('a, button, input')).toBeNull()
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('accepts intermediate and far-out strings and continues zoom actions beyond 1000', async () => {
    const zoom = ref('25')
    const root = mount(() => h(Paimos6ZoomControl, {
      modelValue: zoom.value,
      band: zoom.value.length >= 4 ? 'far' : 'overview',
      'onUpdate:modelValue': (value: string) => { zoom.value = value },
    }))
    const input = root.querySelector<HTMLInputElement>('input')!
    expect(input.getAttribute('inputmode')).toBe('numeric')
    expect(input.getAttribute('maxlength')).toBe('64')

    input.value = '123456789012345678901234567890'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(zoom.value).toBe('123456789012345678901234567890')

    zoom.value = '1000'
    await nextTick()
    root.querySelector<HTMLButtonElement>('[aria-label="Zoom in by one"]')!.click()
    await nextTick()
    expect(zoom.value).toBe('1001')
  })

  it('rejects invalid drafts locally without producing float/scientific values', async () => {
    const zoom = ref('10')
    const root = mount(() => h(Paimos6ZoomControl, {
      modelValue: zoom.value,
      band: 'overview',
      'onUpdate:modelValue': (value: string) => { zoom.value = value },
    }))
    const input = root.querySelector<HTMLInputElement>('input')!
    input.value = '1e6'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await nextTick()
    expect(zoom.value).toBe('10')
    expect(input.value).toBe('10')
  })

  it.each([390, 1200])('keeps aggregate controls structurally bounded at %ipx', (width) => {
    const root = mount(() => h('div', [
      h(Paimos6ZoomControl, { modelValue: '1000', band: 'far' }),
      h(Paimos6ZoomOverview, {
        band: 'far',
        sampleLimit: 100,
        sampleTruncated: true,
        sampledSessions: 100,
        totals: {
          sessions: 1000,
          unread: 20,
          attention_sessions: 8,
          exception_messages: 6,
          action_requests: 2,
          exception_targets: 4,
          sampled_exception_targets: 4,
        },
      }),
    ]), width)
    const control = root.querySelector<HTMLElement>('.p6-zoom')!
    const overview = root.querySelector<HTMLElement>('.p6-overview')!
    expect(control.style.maxWidth).toBe('100%')
    expect(overview.style.maxWidth).toBe('100%')
    expect(root.querySelector('input')?.getAttribute('type')).toBe('text')
    expect(overview.textContent).toContain('Exception-first projection')
  })
})
