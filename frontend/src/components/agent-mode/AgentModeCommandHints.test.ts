/* PAI-818 — contextual suggestion carousel interaction contract. */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import { mountComponent } from '@/components/ai/testMount'
import AgentModeCommandHints from './AgentModeCommandHints.vue'

const HINTS = [
  { id: 'show-all', command: 'Show all', label: 'Show all' },
  { id: 'next', command: 'Next', label: 'Next' },
  { id: 'read-status', command: 'Read status', label: 'Read status' },
] as const

describe('AgentModeCommandHints (PAI-818)', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.stubGlobal('matchMedia', () => ({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }))
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    document.body.innerHTML = ''
  })

  it('executes the exact grammar command and supports manual previous/next', async () => {
    const execute = vi.fn()
    const mounted = await mountComponent(AgentModeCommandHints, { hints: HINTS, onExecute: execute })
    await nextTick()
    const current = () => mounted.el.querySelector<HTMLButtonElement>('.am-hints-current')!
    expect(current().textContent).toContain('Show all')
    current().click()
    expect(execute).toHaveBeenLastCalledWith('Show all')

    const steps = [...mounted.el.querySelectorAll<HTMLButtonElement>('.am-hints-step')]
    steps[1].click()
    await nextTick()
    expect(current().textContent).toContain('Next')
    current().click()
    expect(execute).toHaveBeenLastCalledWith('Next')

    steps[0].click()
    await nextTick()
    expect(current().textContent).toContain('Show all')
    await mounted.unmount()
  })

  it('rotates automatically, pauses while focused, and keeps Show all one click away', async () => {
    const mounted = await mountComponent(AgentModeCommandHints, { hints: HINTS })
    await nextTick()
    const current = () => mounted.el.querySelector<HTMLButtonElement>('.am-hints-current')!
    expect(current().textContent).toContain('Show all')
    vi.advanceTimersByTime(7_000)
    await nextTick()
    expect(current().textContent).toContain('Next')
    expect(mounted.el.querySelector<HTMLButtonElement>('.am-hints-clear')?.textContent).toContain('Show all')
    mounted.el.querySelector<HTMLElement>('.am-hints')!.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    vi.advanceTimersByTime(14_000)
    expect(current().textContent).toContain('Next')
    await mounted.unmount()
  })
})
