import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import { mountComponent } from '@/components/ai/testMount'
import Paimos6PreviewView from './Paimos6PreviewView.vue'

describe('Paimos6PreviewView (PAI-854)', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
  })

  it('renders an honest fixture home with distinct attention and nullable selection', async () => {
    const mounted = await mountComponent(Paimos6PreviewView)
    const text = mounted.el.textContent ?? ''

    expect(mounted.el.querySelector('main[aria-labelledby="p6-title"]')).not.toBeNull()
    expect(mounted.el.querySelectorAll('.p6-session-card')).toHaveLength(3)
    expect(mounted.el.querySelectorAll('.p6-session-card.needs-attention')).toHaveLength(1)
    expect(mounted.el.querySelector('.p6-session-card.is-selected')).toBeNull()
    expect(text).toContain('No selection · preview target Paimos')
    expect(text).toContain('Fixture only')
    expect(text).toContain('not relabelled 5.x issues, deliveries, runs, or harness sessions')
    expect(text).toContain('Live session seam unavailable')
    expect(text).toContain('responsive mobile-web stub—not a native client')
    expect(text).not.toMatch(/coming soon/i)

    const attentionCard = mounted.el.querySelector<HTMLElement>('.p6-session-card.needs-attention')!
    attentionCard.querySelector<HTMLButtonElement>('.p6-card-select')!.click()
    await nextTick()
    expect(attentionCard.classList.contains('is-selected')).toBe(true)
    expect(attentionCard.classList.contains('needs-attention')).toBe(true)
    expect(mounted.el.textContent).toContain('Selected agent target · claude:jan')

    attentionCard.querySelector<HTMLButtonElement>('.p6-card-select')!.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }),
    )
    await nextTick()
    expect(mounted.el.querySelector('.p6-session-card.is-selected')).toBeNull()
    expect(mounted.el.textContent).toContain('No selection · preview target Paimos')
    await mounted.unmount()
  })

  it('makes capability truth visible and keeps unmanaged direct controls disabled', async () => {
    const mounted = await mountComponent(Paimos6PreviewView)
    const unmanaged = [...mounted.el.querySelectorAll<HTMLElement>('.p6-session-card')].find((card) =>
      card.textContent?.includes('Unmanaged CLI'),
    )!
    const directControls = [...unmanaged.querySelectorAll<HTMLButtonElement>('.p6-card-actions button')].slice(0, 3)

    expect(unmanaged.textContent).toContain('Paimos does not own this process')
    expect(unmanaged.textContent).toContain('Paimos-steer nudge')
    expect(directControls.every((button) => button.disabled)).toBe(true)
    const nudge = unmanaged.querySelector<HTMLButtonElement>('.p6-paimos-nudge')!
    expect(nudge.disabled).toBe(false)
    nudge.click()
    await nextTick()
    expect(mounted.el.querySelector('[role="status"]')?.textContent).toContain('local fixture no-op')
    expect(mounted.el.querySelector('[role="status"]')?.textContent).toContain('No request was sent')
    await mounted.unmount()
  })

  it('opens Amy first, documents both mic gestures, and performs local-only interactions', async () => {
    const fetchSpy = vi.fn()
    vi.stubGlobal('fetch', fetchSpy)
    const mounted = await mountComponent(Paimos6PreviewView)

    mounted.el.querySelector<HTMLButtonElement>('[aria-label="Open the talk-first door"]')!.click()
    await nextTick()
    const door = mounted.el.querySelector<HTMLElement>('.p6-talk-door')!
    expect(door).not.toBeNull()
    expect(mounted.el.querySelector('main')?.hasAttribute('inert')).toBe(true)
    const mic = door.querySelector<HTMLButtonElement>('.p6-mic')!
    await vi.waitFor(() => expect(document.activeElement).toBe(mic))
    expect(door.textContent!.indexOf('Amy')).toBeLessThan(door.textContent!.indexOf('Human node form'))
    expect(door.textContent).toContain('Tap to toggle · hold to talk, release to stop')
    expect(door.textContent).toContain('does not request microphone access')
    expect(door.textContent).toContain('Preview target · Paimos (no session selected)')

    mic.click()
    await nextTick()
    expect(mic.getAttribute('aria-pressed')).toBe('true')
    expect(mounted.el.querySelector('[role="status"]')?.textContent).toContain('No microphone opened')

    const details = door.querySelector<HTMLDetailsElement>('.p6-node-door')!
    const summary = details.querySelector<HTMLElement>('summary')!
    const close = door.querySelector<HTMLButtonElement>('.p6-close')!
    summary.focus()
    summary.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(close)
    close.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(summary)

    details.open = true
    details.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()
    expect(door.querySelector('.p6-door-status')?.textContent).toContain('Name the node')

    const input = details.querySelector<HTMLInputElement>('#p6-node-title')!
    input.value = 'Local planning node'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    details.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()
    expect(mounted.el.querySelector('[role="status"]')?.textContent).toContain('staged in local preview state only')
    expect(door.querySelector('.p6-door-status')?.textContent).toContain('staged in local preview state only')
    expect(fetchSpy).not.toHaveBeenCalled()

    const submit = details.querySelector<HTMLButtonElement>('button[type="submit"]')!
    submit.focus()
    submit.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(close)
    close.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(submit)

    door.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await nextTick()
    const reopenedTrigger = mounted.el.querySelector<HTMLButtonElement>('[aria-label="Open the talk-first door"]')!
    expect(reopenedTrigger).not.toBeNull()
    await vi.waitFor(() => expect(document.activeElement).toBe(reopenedTrigger))
    expect(mounted.el.querySelector('main')?.hasAttribute('inert')).toBe(false)
    await mounted.unmount()
  })

  it('keeps controls labelled, actions visible without hover, and status polite', async () => {
    const mounted = await mountComponent(Paimos6PreviewView)
    expect(mounted.el.querySelectorAll('.p6-card-actions')).toHaveLength(3)
    expect(mounted.el.querySelectorAll('.p6-card-select[aria-label]')).toHaveLength(3)
    expect(mounted.el.querySelector('[role="status"]')?.getAttribute('aria-live')).toBe('polite')
    expect(mounted.el.querySelector('[aria-label="Open the talk-first door"]')).not.toBeNull()
    await mounted.unmount()
  })
})
