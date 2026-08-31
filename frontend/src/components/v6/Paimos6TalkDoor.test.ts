import { describe, expect, it } from 'vitest'

import { mountComponent } from '@/components/ai/testMount'
import Paimos6TalkDoor from './Paimos6TalkDoor.vue'

describe('Paimos6TalkDoor orchestrator identity (PAI-865)', () => {
  it('renders the configured display label exactly without deriving its casing', async () => {
    const mounted = await mountComponent(Paimos6TalkDoor, {
      open: true,
      targetAgent: null,
      orchestratorLabel: 'aMY / Primary',
      orchestratorStatus: 'orchestrator configured',
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
    })
    const identity = mounted.el.querySelector('.p6-orchestrator')?.textContent ?? ''
    expect(identity).toContain('Paimos')
    expect(identity).toContain('orchestrator not configured')
    expect(identity).not.toMatch(/Amy|Star|Aithema|START/)
    expect(mounted.el.textContent).toContain('What should Paimos do?')
    expect(mounted.el.textContent).toContain('Talk to Paimos')
    await mounted.unmount()
  })
})
