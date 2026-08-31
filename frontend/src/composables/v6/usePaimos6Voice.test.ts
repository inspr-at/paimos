import { computed, effectScope, nextTick, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/api/client'
import { usePaimos6Voice, type Paimos6VoiceSelection } from './usePaimos6Voice'
import type { Paimos6SessionUtteranceRequest } from '@/v6/sessionUtterance'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

function fixture(send = vi.fn(async (_request: Paimos6SessionUtteranceRequest, _signal?: AbortSignal) => ({
  utteranceId: 'utt_0123456789abcdef0123456789abcdef',
  routeKind: 'project_agent' as const,
  productSessionId: '17e5d8f7-0b11-4bee-a8a4-a11406de865a',
  productSessionRevision: 3,
  messageId: '27e5d8f7-0b11-4bee-a8a4-a11406de865a',
  threadId: '37e5d8f7-0b11-4bee-a8a4-a11406de865a',
  deliveryId: '47e5d8f7-0b11-4bee-a8a4-a11406de865a',
  createdAt: '2026-08-31T10:00:00Z',
}))) {
  const principalId = ref<number | null>(7)
  const authorityKey = ref('epoch:1')
  const projectId = ref<number | null>(42)
  const selection = ref<Paimos6VoiceSelection | null>({
    productSessionId: '17e5d8f7-0b11-4bee-a8a4-a11406de865a',
    revision: 3,
    destination: 'codex:amy',
    available: true,
  })
  const locale = ref('en')
  const micState = ref<'idle' | 'listening' | 'error'>('idle')
  let sink: ((blob: Blob) => Promise<void>) | null = null
  const mic = {
    state: micState,
    level: ref(0),
    errorMessage: ref<string | null>(null),
    isActive: computed(() => micState.value !== 'idle'),
    start: vi.fn(async (next: (blob: Blob) => Promise<void>) => {
      sink = next
      micState.value = 'listening'
      return true
    }),
    finish: vi.fn(() => {
      if (!sink) return false
      const active = sink
      sink = null
      void active(new Blob(['voice']))
      return true
    }),
    stop: vi.fn(() => { micState.value = 'idle' }),
    micSupported: vi.fn(() => true),
  }
  const permission = {
    permission: ref<'granted' | 'denied'>('granted'),
    init: vi.fn(async () => {}),
    requestAccess: vi.fn(async () => 'granted' as const),
    recheck: vi.fn(async () => 'granted' as const),
  }
  const transcribe = vi.fn(async () => ({
    utteranceId: 'utt_0123456789abcdef0123456789abcdef',
    text: 'Ship the bounded slice',
    final: true as const,
  }))
  const scope = effectScope()
  const voice = scope.run(() => usePaimos6Voice({
    principalId,
    authorityKey,
    projectId,
    selection,
    locale,
    dependencies: { mic, permission, transcribe, send },
  }))!
  return { scope, voice, mic, transcribe, send, selection, authorityKey }
}

describe('usePaimos6Voice one-generation delivery (PAI-862)', () => {
  it('coalesces tap/hold overlap into one finalized transcript and one send', async () => {
    const test = fixture()
    await expect(test.voice.start()).resolves.toBe(true)
    await expect(test.voice.start()).resolves.toBe(false)
    expect(test.voice.finish()).toBe(true)
    expect(test.voice.finish()).toBe(false)
    await vi.waitFor(() => expect(test.send).toHaveBeenCalledTimes(1))
    expect(test.transcribe).toHaveBeenCalledTimes(1)
    expect(test.send.mock.calls[0]![0]).toMatchObject({
      projectId: 42,
      utteranceId: 'utt_0123456789abcdef0123456789abcdef',
      text: 'Ship the bounded slice',
      selectedSession: {
        productSessionId: '17e5d8f7-0b11-4bee-a8a4-a11406de865a',
        revision: 3,
      },
    })
    expect(test.voice.state.value).toBe('delivered')
    test.scope.stop()
  })

  it('returns to idle without sending when finalization yields no audio blob', async () => {
    const test = fixture()
    await expect(test.voice.start()).resolves.toBe(true)
    test.mic.finish.mockImplementationOnce(() => {
      queueMicrotask(() => { test.mic.state.value = 'idle' })
      return true
    })
    expect(test.voice.finish()).toBe(true)
    await vi.waitFor(() => expect(test.voice.state.value).toBe('idle'))
    expect(test.transcribe).not.toHaveBeenCalled()
    expect(test.send).not.toHaveBeenCalled()
    expect(test.voice.message.value).toBe('No finalized speech was captured. Nothing was sent.')
    test.scope.stop()
  })

  it('retries with the exact frozen utterance ID and body', async () => {
    const firstFailure = new ApiError(503, 'offline')
    const send = vi.fn(async (_request: Paimos6SessionUtteranceRequest, _signal?: AbortSignal) => ({
        utteranceId: 'utt_0123456789abcdef0123456789abcdef',
        routeKind: 'project_agent' as const,
        productSessionId: '17e5d8f7-0b11-4bee-a8a4-a11406de865a',
        productSessionRevision: 3,
        messageId: '27e5d8f7-0b11-4bee-a8a4-a11406de865a',
        threadId: '37e5d8f7-0b11-4bee-a8a4-a11406de865a',
        deliveryId: '47e5d8f7-0b11-4bee-a8a4-a11406de865a',
        createdAt: '2026-08-31T10:00:00Z',
      }))
    send.mockRejectedValueOnce(firstFailure)
    const test = fixture(send)
    await test.voice.start()
    test.voice.finish()
    await vi.waitFor(() => expect(test.voice.state.value).toBe('retryable'))
    const frozen = send.mock.calls[0]![0]
    await expect(test.voice.retry()).resolves.toBe(true)
    expect(send).toHaveBeenCalledTimes(2)
    expect(send.mock.calls[1]![0]).toEqual(frozen)
    test.scope.stop()
  })

  it('aborts and never reroutes when selection or authority changes mid-transcription', async () => {
    const pending = deferred<{ utteranceId: string; text: string; final: true }>()
    const test = fixture()
    test.transcribe.mockImplementationOnce(() => pending.promise)
    await test.voice.start()
    test.voice.finish()
    test.selection.value = {
      productSessionId: '57e5d8f7-0b11-4bee-a8a4-a11406de865a',
      revision: 1,
      destination: 'codex:other',
      available: true,
    }
    await nextTick()
    pending.resolve({
      utteranceId: 'utt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
      text: 'Must not reroute',
      final: true,
    })
    await Promise.resolve()
    expect(test.send).not.toHaveBeenCalled()
    expect(test.voice.state.value).toBe('stale')

    test.authorityKey.value = 'epoch:2'
    await nextTick()
    expect(test.send).not.toHaveBeenCalled()
    test.scope.stop()
  })
})
