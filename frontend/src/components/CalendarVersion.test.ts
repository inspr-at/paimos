import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive, type App } from 'vue'
import display from '@/vendor/calendar-version-display/display.json'
import CalendarVersion from './CalendarVersion.vue'
import * as renderer from '@/vendor/calendar-version-display/version.js'

type Props = {
  version: string
  scheme?: string
  prefix?: string
  mode?: 'pretty' | 'reduced'
  brand?: string
  interactive?: boolean
}
const apps: App[] = []
async function mount(input: Props) {
  const root = document.createElement('div')
  document.body.append(root)
  const props = reactive(input)
  const app = createApp({ render: () => h(CalendarVersion, props) })
  app.mount(root)
  apps.push(app)
  await nextTick()
  return { app, props, root, label: root.querySelector<HTMLElement>('.calendar-version-label')! }
}

function clipboard() {
  return vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
}

describe('CalendarVersion shared adapter', () => {
  afterEach(() => {
    for (const app of apps.splice(0)) app.unmount()
    document.body.replaceChildren()
    vi.restoreAllMocks()
  })

  it('uses Pretty by default while preserving canonical machine metadata and product brand', async () => {
    const render = vi.spyOn(renderer, 'renderVersion')
    const { label } = await mount({
      version: '260909151030.0.0',
      scheme: 'inspr-calendar-v2',
      brand: '#123456',
    })
    expect(label.querySelector('.separator')).not.toBeNull()
    expect(label.dataset.version).toBe('v260909151030.0.0')
    expect(label.dataset.canonical).toBe('260909151030.0.0')
    expect(label.querySelector<HTMLElement>('.yy')!.style.opacity).toBe(String(display.weights.yy))
    expect(render).toHaveBeenCalledWith(
      label,
      'v260909151030.0.0',
      'inspr-calendar-v2',
      expect.objectContaining({ brand: '#123456' }),
    )
    expect(label.getAttribute('aria-label')).toContain('Copy version')
  })

  it('copies the exact canonical version by mouse and keyboard, without decorative prefix', async () => {
    const copy = clipboard()
    const { label } = await mount({ version: '260909151030.0.0', scheme: 'inspr-calendar-v2' })
    label.click()
    await nextTick()
    expect(copy).toHaveBeenLastCalledWith('260909151030.0.0')
    label.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await nextTick()
    expect(copy).toHaveBeenCalledTimes(2)
    await vi.waitFor(() =>
      expect(label.querySelector('[role="status"]')!.textContent).toBe('Copied'),
    )
  })

  it('keeps SemVer available and preserves canonical copy in that mode', async () => {
    const copy = clipboard()
    const { root, label } = await mount({
      version: '260909151030.0.0',
      scheme: 'inspr-calendar-v2',
    })
    root.querySelector<HTMLButtonElement>('[aria-label="Show SemVer"]')!.click()
    await nextTick()
    expect(label.querySelector('.separator')).toBeNull()
    expect(label.textContent).toBe('v260909151030.0.0')
    label.click()
    expect(copy).toHaveBeenLastCalledWith('260909151030.0.0')
    root.querySelector<HTMLButtonElement>('[aria-label="Show Pretty version"]')!.click()
    await nextTick()
    expect(label.querySelector('.separator')).not.toBeNull()
  })

  it('disposes the old controller on prop changes and unmount', async () => {
    const copy = clipboard()
    const { app, props, label } = await mount({
      version: '260909151030.0.0',
      scheme: 'inspr-calendar-v2',
    })
    props.version = '260910151030.0.0'
    await nextTick()
    label.click()
    expect(copy).toHaveBeenCalledTimes(1)
    expect(copy).toHaveBeenLastCalledWith('260910151030.0.0')
    app.unmount()
    apps.splice(apps.indexOf(app), 1)
    label.click()
    expect(copy).toHaveBeenCalledTimes(1)
  })

  it('leaves legacy, wrong-scheme, invalid dates and arbitrary prefixes plain', async () => {
    for (const input of [
      { version: '5.21.0', scheme: 'legacy' },
      { version: '260909151030.0.0', scheme: 'inspr-calendar-v1' },
      { version: '260229151030.0.0', scheme: 'inspr-calendar-v2' },
      { version: '260431151030.0.0', scheme: 'inspr-calendar-v2' },
      { version: '260909151030.0.0', scheme: 'inspr-calendar-v2', prefix: 'release-' },
    ]) {
      const { label, root } = await mount(input)
      expect(label.textContent).toBe(`${input.prefix ?? 'v'}${input.version}`)
      expect(label.getAttribute('role')).toBeNull()
      expect(root.querySelector('button')).toBeNull()
    }
  })

  it('supports empty prefixes, real leap dates and non-interactive surrounding controls', async () => {
    const { label, root } = await mount({
      version: '280229151030.0.0',
      scheme: 'inspr-calendar-v2',
      prefix: '',
      interactive: false,
    })
    expect(label.querySelector('.yy')!.textContent).toBe('28')
    expect(label.querySelector('.v')).toBeNull()
    expect(label.getAttribute('role')).toBe('img')
    expect(label.hasAttribute('tabindex')).toBe(false)
    expect(root.querySelector('button')).toBeNull()
  })

  it('defaults the scheme to the release record rather than guessing from the string', async () => {
    const { label } = await mount({ version: '260909151030.0.0' })
    expect(Boolean(label.querySelector('.yy'))).toBe(__APP_VERSION_SCHEME__ === 'inspr-calendar-v2')
  })
})
