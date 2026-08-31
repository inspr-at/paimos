import { afterEach, describe, expect, it, vi } from 'vitest'

import { mountComponent } from '@/components/ai/testMount'
import Paimos6TalkDoor from './Paimos6TalkDoor.vue'

afterEach(() => vi.useRealTimers())

describe('Paimos6TalkDoor orchestrator identity (PAI-865)', () => {
  it('renders the configured display label exactly without deriving its casing', async () => {
    const mounted = await mountComponent(Paimos6TalkDoor, {
      open: true,
      targetAgent: null,
      orchestratorLabel: 'aMY / Primary',
      orchestratorStatus: 'orchestrator configured',
      voiceState: 'idle',
      voiceMessage: 'Ready for one ephemeral utterance to Paimos.',
      voiceSupported: true,
      voiceCanRetry: false,
    })
    const text = mounted.el.textContent ?? ''
    expect(text).toContain('What should aMY / Primary do?')
    expect(text).toContain('Preview target · aMY / Primary (no session selected)')
    expect(text).toContain('Talk to aMY / Primary')
    expect(text).toContain('orchestrator configured')
    expect(text).not.toContain('What should Amy do?')
    await mounted.unmount()
  })

  it('renders only neutral Paimos identity and the exact unset status', async () => {
    const mounted = await mountComponent(Paimos6TalkDoor, {
      open: true,
      targetAgent: null,
      orchestratorLabel: 'Paimos',
      orchestratorStatus: 'orchestrator not configured',
      voiceState: 'idle',
      voiceMessage: 'Ready for one ephemeral utterance to Paimos.',
      voiceSupported: true,
      voiceCanRetry: false,
    })
    const identity = mounted.el.querySelector('.p6-orchestrator')?.textContent ?? ''
    expect(identity).toContain('Paimos')
    expect(identity).toContain('orchestrator not configured')
    expect(identity).not.toMatch(/Amy|Star|Aithema|START/)
    expect(mounted.el.textContent).toContain('What should Paimos do?')
    expect(mounted.el.textContent).toContain('Talk to Paimos')
    await mounted.unmount()
  })

  it('suppresses the synthetic click after hold-to-talk so one gesture finalizes once', async () => {
    vi.useFakeTimers()
    const start = vi.fn()
    const finish = vi.fn()
    const mounted = await mountComponent(Paimos6TalkDoor, {
      open: true,
      targetAgent: 'codex:amy',
      orchestratorLabel: 'Paimos',
      orchestratorStatus: 'orchestrator configured',
      voiceState: 'idle',
      voiceMessage: 'Ready.',
      voiceSupported: true,
      voiceCanRetry: false,
      onVoiceStart: start,
      onVoiceFinish: finish,
    })
    const mic = mounted.el.querySelector<HTMLButtonElement>('.p6-mic')!
    mic.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, button: 0, pointerId: 7 }))
    await vi.advanceTimersByTimeAsync(450)
    expect(start).toHaveBeenCalledTimes(1)
    mic.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, button: 0, pointerId: 7 }))
    mic.click()
    expect(finish).toHaveBeenCalledTimes(1)
    expect(start).toHaveBeenCalledTimes(1)
    await mounted.unmount()
  })
})
