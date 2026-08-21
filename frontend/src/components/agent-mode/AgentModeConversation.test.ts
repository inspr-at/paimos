/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { h } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'

import { mountComponent } from '@/components/ai/testMount'
import AgentModeConversation from './AgentModeConversation.vue'

describe('AgentModeConversation controls seam', () => {
  afterEach(() => { document.body.innerHTML = '' })

  it('renders one controls surface and labels the dock as feed state, not microphone state', async () => {
    const mounted = await mountComponent(AgentModeConversation, {
      lines: [{ id: 'one', role: 'system', text: 'Authorized narration' }],
      live: true,
      liveLabel: 'Live',
      compact: true,
    }, {
      controls: () => h('div', { 'data-test': 'voice-console' }, 'Voice controls'),
    })

    expect(mounted.el.querySelectorAll('[data-test="voice-console"]')).toHaveLength(1)
    expect(mounted.el.querySelectorAll('.am-conv-controls')).toHaveLength(1)
    expect(mounted.el.querySelector('.am-conv-compact-live strong')?.textContent).toBe('Delivery feed')
    expect(mounted.el.textContent).not.toContain('Listening')
    await mounted.unmount()
  })
})
