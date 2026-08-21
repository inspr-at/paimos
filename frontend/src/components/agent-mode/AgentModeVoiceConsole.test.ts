/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { reactive, nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { mountComponent } from '@/components/ai/testMount'
import AgentModeVoiceConsole from './AgentModeVoiceConsole.vue'

function props(extra: Record<string, unknown> = {}) {
  return reactive<Record<string, any>>({
    micState: 'idle',
    micLevel: 0,
    micSupported: true,
    permission: 'granted',
    wantsListening: false,
    micStartPending: false,
    speechActive: false,
    authorized: true,
    audioAvailable: true,
    voiceRepliesEnabled: false,
    replyState: 'off',
    draft: '',
    candidates: [],
    candidateMatchCount: 0,
    candidateTruncated: false,
    note: null,
    noteTarget: '',
    notice: null,
    unsupportedControl: null,
    error: null,
    busy: false,
    noteFocusToken: 0,
    inputResetToken: 0,
    oneShotWarning: false,
    compact: false,
    ...extra,
  })
}

describe('AgentModeVoiceConsole', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('keeps typed input available and separates truthful mic and reply controls', async () => {
    const onSubmit = vi.fn()
    const onToggleMic = vi.fn()
    const onSetReplies = vi.fn()
    const state = props({ onSubmit, onToggleMic, onSetReplies })
    const mounted = await mountComponent(AgentModeVoiceConsole, state)

    const input = mounted.el.querySelector<HTMLInputElement>('#am-voice-command')!
    expect(input).not.toBeNull()
    expect(input.autocomplete).toBe('off')
    const buttons = [...mounted.el.querySelectorAll<HTMLButtonElement>('.am-voice-actions button')]
    expect(buttons[0].textContent).toContain('Start mic')
    expect(buttons[1].textContent).toContain('Voice replies')
    expect(buttons[1].textContent).toContain('Off')

    buttons[0].click()
    buttons[1].click()
    expect(onToggleMic).toHaveBeenCalledOnce()
    expect(onSetReplies).toHaveBeenCalledWith(true)

    input.value = 'next'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    mounted.el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()
    expect(onSubmit).toHaveBeenCalledWith('next')
    expect(input.value).toBe('')

    state.micState = 'transcribing'
    state.wantsListening = true
    state.replyState = 'loading'
    state.voiceRepliesEnabled = true
    await nextTick()
    expect(buttons[0].textContent).toContain('Transcribing')
    expect(buttons[1].textContent).toContain('Loading')
    expect(mounted.el.textContent).not.toContain('Listening')

    state.speechActive = true
    state.micState = 'listening'
    await nextTick()
    expect(buttons[0].textContent).toContain('Paused for reply')
    expect(buttons[0].classList.contains('is-active')).toBe(false)
    await mounted.unmount()
  })

  it('scrubs private local input only on the explicit reset token and preserves it while unavailable', async () => {
    const onSubmit = vi.fn()
    const state = props({ onSubmit })
    const mounted = await mountComponent(AgentModeVoiceConsole, state)
    const input = mounted.el.querySelector<HTMLInputElement>('#am-voice-command')!
    const form = mounted.el.querySelector('form')!

    input.value = 'note that PRIVATE_CANARY'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    state.notice = 'status_ready'
    await nextTick()
    expect(input.value).toBe('note that PRIVATE_CANARY')

    state.authorized = false
    state.audioAvailable = false
    await nextTick()
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()
    expect(onSubmit).not.toHaveBeenCalled()
    expect(input.value).toBe('note that PRIVATE_CANARY')
    expect(mounted.el.querySelector<HTMLButtonElement>('.am-voice-mic')!.disabled).toBe(true)

    state.inputResetToken += 1
    await nextTick()
    expect(input.value).toBe('')
    expect(mounted.el.textContent).not.toContain('PRIVATE_CANARY')
    await mounted.unmount()
  })

  it('shows at most three numbered key/title/lane candidates', async () => {
    const onChoose = vi.fn()
    const state = props({
      onChoose,
      candidateMatchCount: 8,
      candidateTruncated: true,
      candidates: [1, 2, 3, 4].map((index) => ({
        index,
        deliveryId: `delivery:${index}`,
        issueKey: `PAI-${800 + index}`,
        title: `Candidate ${index}`,
        lane: `Paimos / Epic ${index}`,
      })),
    })
    const mounted = await mountComponent(AgentModeVoiceConsole, state)
    const choices = [...mounted.el.querySelectorAll<HTMLButtonElement>('.am-voice-candidates li button')]
    expect(choices).toHaveLength(3)
    expect(choices[0].textContent).toContain('PAI-801')
    expect(choices[0].textContent).toContain('Candidate 1')
    expect(choices[0].textContent).toContain('Paimos / Epic 1')
    expect(mounted.el.textContent).not.toContain('PAI-804')
    choices[1].click()
    expect(onChoose).toHaveBeenCalledWith(2)
    await mounted.unmount()
  })

  it('focuses the preview container, never the Confirm button, and keeps the body out of live status', async () => {
    const body = 'PRIVATE CANARY dictated note body'
    const onConfirmNote = vi.fn()
    const onCancelNote = vi.fn()
    const state = props({
      onConfirmNote,
      onCancelNote,
      note: {
        status: 'preview',
        binding: {
          deliveryId: 'delivery:808', issueId: 808, issueKey: 'PAI-808', attemptId: 'attempt:1',
          selectionEpoch: 'epoch:1', body, clientRequestId: 'amv1-0123456789abcdef', utteranceId: 'typed_1',
        },
      },
      noteTarget: 'PAI · PAIMOS Core platform / Voice command console',
      notice: 'note_ready',
      oneShotWarning: true,
    })
    const mounted = await mountComponent(AgentModeVoiceConsole, state)
    state.noteFocusToken += 1
    await nextTick()
    await nextTick()

    const preview = mounted.el.querySelector<HTMLElement>('.am-voice-note')!
    const live = mounted.el.querySelector<HTMLElement>('.am-voice-status')!
    const noteButtons = [...mounted.el.querySelectorAll<HTMLButtonElement>('.am-voice-note-actions button')]
    expect(document.activeElement).toBe(preview)
    expect(document.activeElement).not.toBe(noteButtons[0])
    expect(preview.textContent).toContain(body)
    expect(preview.textContent).toContain('PAI-808')
    expect(preview.textContent).toContain('PAIMOS Core platform')
    expect(preview.textContent).toContain('one-shot run')
    expect(live.textContent).not.toContain(body)
    expect(live.textContent).not.toContain('CANARY')

    noteButtons[0].click()
    noteButtons[1].click()
    expect(onConfirmNote).toHaveBeenCalledOnce()
    expect(onCancelNote).toHaveBeenCalledOnce()
    await nextTick()
    expect(document.activeElement).toBe(mounted.el.querySelector('#am-voice-command'))
    await mounted.unmount()
  })
})
