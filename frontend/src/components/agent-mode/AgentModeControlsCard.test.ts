/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { nextTick, reactive } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { mountComponent } from '@/components/ai/testMount'
import type { BoundControlCommand } from '@/composables/agent-mode/useAgentModeControls'
import type { ControlCommand } from '@/services/agentModeControls'
import AgentModeControlsCard from './AgentModeControlsCard.vue'

const COMMAND_ID = '22222222-2222-4222-8222-222222222222'

function binding(status: ControlCommand['status'] = 'pending_confirmation'): BoundControlCommand {
  return {
    deliveryKey: 'dlv-812', issueKey: 'PAI-812', userId: 7,
    command: {
      commandId: COMMAND_ID, statusRevision: 1, action: 'run.cancel.running', status,
      challengeTemplate: 'run_cancel_running', expiresAt: '2026-08-21T20:00:00Z',
      display: { issueKey: 'PAI-812', deliveryKey: 'dlv-812', runId: 42 }, outcome: null, reason: null,
    },
  }
}

function props(extra: Record<string, unknown> = {}) {
  return reactive({
    state: 'ready', targets: [], command: null, busy: false, available: true, transitionAvailable: true,
    selectedIssueKey: 'PAI-812', voicePhrase: '', focusToken: 0, focusReturnToken: 0,
    ...extra,
  })
}

describe('AgentModeControlsCard', () => {
  afterEach(() => { document.body.innerHTML = '' })

  it('renders exactly the server target set and emits opaque target-bound activations', async () => {
    const onActivate = vi.fn()
    const targets = [
      { action: 'issue.priority.set' },
      { action: 'run.cancel.running', runId: 42 },
      { action: 'input.respond', inputRequestId: '33333333-3333-4333-8333-333333333333', inputRequestRevision: 2, inputKind: 'approval' },
      { action: 'input.respond', inputRequestId: '44444444-4444-4444-8444-444444444444', inputRequestRevision: 1, inputKind: 'choice', optionCodes: ['choice_1', 'choice_2'] },
      { action: 'run.pause', runId: 42, runtimeState: 'running', runtimeRevision: 3 },
    ]
    const mounted = await mountComponent(AgentModeControlsCard, props({ targets, onActivate }))
    expect(mounted.el.textContent).toContain('Set priority')
    expect(mounted.el.textContent).toContain('Cancel running run')
    expect(mounted.el.textContent).toContain('Approve request')
    expect(mounted.el.textContent).toContain('Reject request')
    expect(mounted.el.textContent).toContain('Pause run')
    expect(mounted.el.textContent).not.toContain('Resume run')

    const cancel = [...mounted.el.querySelectorAll<HTMLButtonElement>('button')]
      .find((button) => button.textContent?.includes('Cancel running run'))!
    cancel.click()
    expect(onActivate).toHaveBeenCalledWith({ target: targets[1] })
    await mounted.unmount()
  })

  it('puts Back first, traps Tab, Escape withdraws, and shows the exact voice caption', async () => {
    const onWithdraw = vi.fn()
    const state = props({
      command: binding(), voicePhrase: 'Cancel running run PAI-812', onWithdraw,
    })
    const mounted = await mountComponent(AgentModeControlsCard, state)
    await nextTick()
    const dialog = mounted.el.querySelector<HTMLElement>('[role="dialog"]')!
    const buttons = [...dialog.querySelectorAll<HTMLButtonElement>('button')]
    expect(dialog.getAttribute('aria-label')).toContain('dlv-812, PAI-812: Cancel running run')
    expect(buttons[0].textContent).toContain('Back')
    expect(document.activeElement).toBe(buttons[0])
    expect(dialog.textContent).toContain('Voice phrase: “Cancel running run PAI-812”')

    buttons[1].focus()
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(buttons[0])
    buttons[0].focus()
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(buttons[1])
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(onWithdraw).toHaveBeenCalledOnce()
    await mounted.unmount()
  })

  it('disables both pending transitions offline and does not treat Escape as withdrawal', async () => {
    const onWithdraw = vi.fn()
    const mounted = await mountComponent(AgentModeControlsCard, props({
      state: 'offline', command: binding(), transitionAvailable: false, onWithdraw,
    }))
    const dialog = mounted.el.querySelector<HTMLElement>('[role="dialog"]')!
    expect([...dialog.querySelectorAll<HTMLButtonElement>('button')].every((button) => button.disabled)).toBe(true)
    dialog.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(onWithdraw).not.toHaveBeenCalled()
    await mounted.unmount()
  })

  it.each([
    ['pending_confirmation', 'Confirmation required'],
    ['accepted', 'Accepted — waiting'],
    ['applied', 'Applied'],
    ['rejected', 'Rejected'],
    ['expired', 'Rejected'],
  ] as const)('announces the exact %s lifecycle label', async (status, label) => {
    const mounted = await mountComponent(AgentModeControlsCard, props({ command: binding(status) }))
    const live = mounted.el.querySelector<HTMLElement>('[role="status"]')!
    expect(live.getAttribute('aria-live')).toBe('polite')
    expect(live.getAttribute('aria-atomic')).toBe('true')
    expect(live.textContent).toContain(label)
    await mounted.unmount()
  })

  it('announces Outcome unknown ahead of a terminal status without implying success', async () => {
    const value = binding('rejected')
    value.command.outcome = 'outcome_unknown'
    const mounted = await mountComponent(AgentModeControlsCard, props({ command: value }))
    expect(mounted.el.querySelector('[role="status"]')?.textContent).toContain('Outcome unknown')
    expect(mounted.el.querySelector('[role="status"]')?.textContent).not.toContain('Applied')
    await mounted.unmount()
  })
})
