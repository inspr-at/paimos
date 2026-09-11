import { afterEach, describe, expect, it } from 'vitest'
import { createApp, h } from 'vue'

import display from '@/brand/calendar-version-display.json'
import CalendarVersion from './CalendarVersion.vue'

function mount(props: { version: string; scheme?: string; prefix?: string }) {
  document.body.innerHTML = '<div id="root"></div>'
  const app = createApp({ render: () => h(CalendarVersion, props) })
  app.mount('#root')
  return app
}

describe('CalendarVersion', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('bundles the pinned doctrine data', () => {
    expect(display.schema).toBe('inspr.calendar-version-display.v1')
    expect(display.scheme).toBe('inspr-calendar-v2')
    expect(display.design_revision).toBe(3)
    expect(display.weights.yy).toBe(1)
    expect(display.weights.mm).toBe(0.8)
    expect(display.weights.dd).toBe(1)
    expect(display.weights.hh).toBe(0.6)
    expect(display.weights.mi).toBe(0.4)
    expect(display.weights.ss).toBe(0.2)
    expect(display.weights.tail).toBe(0.2)
    expect(display.tint.segments).toEqual(['yy', 'mm', 'dd'])
    expect(display.tint.default).toBe('#d69b31')
    expect(display.tint.mix).toBe(0.8)
    expect(display.tint.space).toBe('oklab')
  })

  it('weights a calendar v2 coordinate from the data and keeps the canonical text', () => {
    const app = mount({ version: '260909151030.0.0', scheme: 'inspr-calendar-v2' })
    const span = document.querySelector<HTMLSpanElement>('span.cv2')!
    expect(span).not.toBeNull()
    expect(span.textContent).toBe('v260909151030.0.0')
    expect(span.dataset.version).toBe('v260909151030.0.0')
    expect(span.querySelector('b.yy')!.textContent).toBe('26')
    expect(span.querySelector('b.hh')!.textContent).toBe('15')
    expect(span.querySelector('b.tail')!.textContent).toBe('.0.0')
    for (const [segment, weight] of Object.entries(display.weights)) {
      const property = (display.css.properties as Record<string, string>)[segment]
      expect(span.style.getPropertyValue(property)).toBe(String(weight))
    }
    expect(span.style.getPropertyValue('--cv2-mix')).toBe(`${Math.round(display.tint.mix * 100)}%`)
    expect(span.style.getPropertyValue('--cv2-tint')).toBe(display.tint.default)
    expect(span.querySelectorAll('b.tinted').length).toBe(display.tint.segments.length)
    app.unmount()
  })

  it('renders plain text for other schemes and never decides by shape', () => {
    for (const [version, scheme] of [
      ['26.09.04', 'inspr-calendar-v1'],
      ['5.21.0', 'legacy'],
      ['260909151030.0.0', 'inspr-calendar-v1'],
      ['26.09.04', 'inspr-calendar-v2'],
    ] as const) {
      const app = mount({ version, scheme })
      expect(document.querySelector('span.cv2')).toBeNull()
      expect(document.getElementById('root')!.textContent).toBe(`v${version}`)
      app.unmount()
    }
  })

  it('defaults the scheme to the build-time release record', () => {
    expect(typeof __APP_VERSION_SCHEME__).toBe('string')
    const app = mount({ version: '260909151030.0.0' })
    expect(document.querySelector('span.cv2') !== null).toBe(
      __APP_VERSION_SCHEME__ === 'inspr-calendar-v2',
    )
    app.unmount()
  })
})
