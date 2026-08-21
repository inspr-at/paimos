import { computed, effectScope, ref, type EffectScope, type Ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { permissionsEpoch, sessionExpired } from '@/api/client'
import { createIntakeTtsPlayback, type IntakePlaybackAudio } from '@/composables/useIntakeTtsPlayback'
import { useMicPermission } from '@/composables/useMicPermission'
import { useMicTranscript, type MicState } from '@/composables/useMicTranscript'
import type { Delivery } from '@/services/agentMode'
import { makeFixtureDelivery } from '@/services/agentModeFixtures'
import { normalizeWireDelivery } from '@/services/agentModeTransport'
import { useAgentModeVoice, type AgentModeVoiceActions } from './useAgentModeVoice'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, fail) => {
    resolve = done
    reject = fail
  })
  return { promise, reject, resolve }
}

function delivery(index: number): Delivery {
  return normalizeWireDelivery(makeFixtureDelivery(index))!
}

const SAME_ID_STT_ROTATIONS: Array<{
  name: string
  prepare?: (row: Delivery) => Delivery
  rotate: (row: Delivery) => Delivery
}> = [
  {
    name: 'delivery revision',
    rotate: (row) => ({ ...row, deliveryRevision: `${row.deliveryRevision}:rotated` }),
  },
  {
    name: 'issue and attempt identity',
    rotate: (row) => ({
      ...row,
      issueId: row.issueId + 10_000,
      attempt: { ...row.attempt, id: `${row.attempt.id}:rotated` },
    }),
  },
  {
    name: 'comment capability revocation',
    rotate: (row) => ({
      ...row,
      capabilities: { ...row.capabilities, comment: false },
    }),
  },
  {
    name: 'comment capability grant',
    prepare: (row) => ({
      ...row,
      capabilities: { ...row.capabilities, comment: false },
    }),
    rotate: (row) => ({
      ...row,
      capabilities: { ...row.capabilities, comment: true },
    }),
  },
]

function fakeMic() {
  const state = ref<MicState>('idle')
  const level = ref(0)
  const errorMessage = ref<string | null>(null)
  let sink: ((blob: Blob) => Promise<void>) | null = null
  const sinks: Array<(blob: Blob) => Promise<void>> = []
  const start = vi.fn(async (next: (blob: Blob) => Promise<void>) => {
    sink = next
    sinks.push(next)
    state.value = 'listening'
    return true
  })
  const stop = vi.fn(() => { state.value = 'idle' })
  const adapter = {
    state,
    level,
    errorMessage,
    isActive: computed(() => state.value === 'starting' || state.value === 'listening' || state.value === 'transcribing'),
    start,
    stop,
    micSupported: () => true,
  } as ReturnType<typeof useMicTranscript>
  return {
    adapter,
    emit: async (blob = new Blob(['audio'], { type: 'audio/webm' })) => sink?.(blob),
    emitFrom: async (index: number, blob = new Blob(['audio'], { type: 'audio/webm' })) => sinks[index]?.(blob),
    sinks,
    start,
    stop,
  }
}

function fakePermission(init: () => Promise<void> = async () => {}) {
  const permission = ref<'granted' | 'denied' | 'prompt' | 'unknown'>('granted')
  return {
    permission,
    init: vi.fn(init),
    requestAccess: vi.fn(async () => permission.value),
    recheck: vi.fn(async () => permission.value),
  } as ReturnType<typeof useMicPermission>
}

function fakePlayback() {
  const cancel = vi.fn((_resume = true) => false)
  const play = vi.fn(async (load: () => Promise<Blob>) => {
    await load()
    return true
  })
  const factory = vi.fn((_options: Parameters<typeof createIntakeTtsPlayback>[0]) => ({ cancel, play }))
  return { cancel, factory, play }
}

class VoiceAudio implements IntakePlaybackAudio {
  src: string
  onended: ((event: Event) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  pause = vi.fn()
  play = vi.fn(async () => {})

  constructor(src: string) {
    this.src = src
  }
}

function realPlaybackHarness() {
  const audios: VoiceAudio[] = []
  const revoked: string[] = []
  let sequence = 0
  const factory: typeof createIntakeTtsPlayback = (options) => createIntakeTtsPlayback({
    ...options,
    createObjectURL: () => `blob:agent-voice-${++sequence}`,
    revokeObjectURL: (url) => revoked.push(url),
    createAudio: (url) => {
      const audio = new VoiceAudio(url)
      audios.push(audio)
      return audio
    },
  })
  return { audios, factory, revoked }
}

interface Fixture {
  scope: EffectScope
  voice: ReturnType<typeof useAgentModeVoice>
  deliveries: Ref<Delivery[]>
  travelOrder: Ref<string[]>
  selectedId: Ref<string | null>
  online: Ref<boolean>
  degraded: Ref<boolean>
  authorityAvailable: Ref<boolean>
  authorityVersion: Ref<number>
  authorityEpoch: Readonly<Ref<string | null>>
  enabled: Ref<boolean>
  actions: { [K in keyof AgentModeVoiceActions]-?: ReturnType<typeof vi.fn> }
  mic: ReturnType<typeof fakeMic>
  permission: ReturnType<typeof fakePermission>
  playback: ReturnType<typeof fakePlayback>
  transcribe: ReturnType<typeof vi.fn>
  speak: ReturnType<typeof vi.fn>
  postNote: ReturnType<typeof vi.fn>
  loadProjectCatalog: ReturnType<typeof vi.fn>
}

const fixtures: Fixture[] = []

function fixture(overrides: {
  enabled?: boolean
  permission?: ReturnType<typeof fakePermission>
  loadProjectCatalog?: ReturnType<typeof vi.fn>
  postNote?: ReturnType<typeof vi.fn>
  speak?: ReturnType<typeof vi.fn>
  transcribe?: ReturnType<typeof vi.fn>
  authorityChanged?: () => void | Promise<void>
  createPlayback?: typeof createIntakeTtsPlayback
} = {}): Fixture {
  const rows = [delivery(0), delivery(1), delivery(2)]
  rows[1].title = 'Shared migration'
  rows[2].title = 'Shared migration'
  const deliveries = ref(rows)
  const travelOrder = ref(rows.map((row) => row.id))
  const selectedId = ref<string | null>(rows[0].id)
  const online = ref(true)
  const authorityAvailable = ref(true)
  const authorityVersion = ref(1)
  const authorityEpoch = computed(() => permissionsEpoch.value)
  const enabled = ref(overrides.enabled ?? true)
  const locale = ref('en')
  const degraded = ref(false)
  const mic = fakeMic()
  const permission = overrides.permission ?? fakePermission()
  const playback = fakePlayback()
  const transcribe = overrides.transcribe ?? vi.fn(async () => ({
    utteranceId: 'utt_0123456789abcdef0123456789abcdef', text: 'next', final: true as const,
  }))
  const speak = overrides.speak ?? vi.fn(async () => new Blob(['mp3'], { type: 'audio/mpeg' }))
  const postNote = overrides.postNote ?? vi.fn(async () => ({ id: 1 }))
  const loadProjectCatalog = overrides.loadProjectCatalog ?? vi.fn(async () => [
    { projectId: 12, projectKey: 'PAI', projectName: 'Paimos' },
    { projectId: 7, projectKey: 'OPS', projectName: 'Operations' },
  ])
  const actions = {
    selectDelivery: vi.fn(async (id: string) => { selectedId.value = id; return true }),
    setFilters: vi.fn(async () => true),
    clearFilters: vi.fn(async () => true),
    setDetail: vi.fn(async () => true),
    showDetails: vi.fn(async () => true),
    notePosted: vi.fn(async () => {}),
    authorityChanged: vi.fn(overrides.authorityChanged ?? (async () => {})),
  }
  const scope = effectScope()
  const voice = scope.run(() => useAgentModeVoice({
    deliveries,
    travelOrder,
    selectedId,
    online,
    degraded,
    locale,
    authorityAvailable,
    authorityVersion,
    authorityEpoch,
    enabled,
    actions,
    dependencies: {
      mic: mic.adapter,
      permission,
      createPlayback: overrides.createPlayback ?? playback.factory,
      transcribe,
      speak,
      postNote,
      loadProjectCatalog,
      sessionNonce: 'testsession',
    },
  }))!
  const result = {
    scope, voice, deliveries, travelOrder, selectedId, online, degraded, authorityAvailable, authorityVersion, authorityEpoch, enabled,
    actions, mic, permission, playback, transcribe, speak, postNote, loadProjectCatalog,
  } as Fixture
  fixtures.push(result)
  return result
}

async function flush() {
  for (let index = 0; index < 12; index += 1) await Promise.resolve()
}

describe('useAgentModeVoice', () => {
  beforeEach(() => {
    permissionsEpoch.value = null
    sessionExpired.value = false
  })

  afterEach(() => {
    for (const f of fixtures.splice(0)) {
      f.voice.dispose()
      f.scope.stop()
    }
    vi.restoreAllMocks()
  })

  it.each([true, false])('constructs without pre-initialization access when enabled=%s', (enabled) => {
    const f = fixture({ enabled })
    expect(f.voice.machine.value.selectedId).toBe(enabled ? f.selectedId.value : null)
  })

  it('routes partials, typed commands, and batch-final mic transcripts through one reducer with stable unique ids', async () => {
    const f = fixture()
    await f.voice.initialize()
    await f.voice.acceptPartial('future-stream-1', 'nex')
    expect(f.voice.draft.value).toBe('nex')
    expect(f.actions.selectDelivery).not.toHaveBeenCalled()

    await f.voice.submitTyped('next')
    await f.voice.submitTyped('previous')
    expect(f.actions.selectDelivery.mock.calls.map((call) => call[0])).toEqual([
      f.deliveries.value[1].id,
      f.deliveries.value[0].id,
    ])
    expect(f.voice.machine.value.executed).toEqual([
      'typed_testsession_1',
      'typed_testsession_2',
    ])

    await f.voice.startListening()
    await f.mic.emit()
    expect(f.transcribe).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'audio/webm' }),
      'en',
      expect.any(AbortSignal),
    )
    expect(f.actions.selectDelivery).toHaveBeenLastCalledWith(f.deliveries.value[1].id)
  })

  it('cancels a late permission start and scrubs late transcription after teardown', async () => {
    const permissionGate = deferred<void>()
    const f = fixture({ permission: fakePermission(() => permissionGate.promise) })
    const starting = f.voice.startListening()
    f.voice.stopListening()
    permissionGate.resolve()
    await expect(starting).resolves.toBe(false)
    expect(f.mic.start).not.toHaveBeenCalled()

    const transcriptGate = deferred<{ utteranceId: string; text: string; final: true }>()
    const second = fixture({ transcribe: vi.fn(() => transcriptGate.promise) })
    await second.voice.startListening()
    const utterance = second.mic.emit()
    second.voice.dispose()
    transcriptGate.resolve({
      utteranceId: 'utt_11111111111111111111111111111111', text: 'next', final: true,
    })
    await utterance
    expect(second.actions.selectDelivery).not.toHaveBeenCalled()
    expect(second.voice.draft.value).toBe('')
    expect(second.voice.note.value).toBeNull()

    const deniedGate = deferred<void>()
    const deniedPermission = fakePermission(() => deniedGate.promise)
    const denied = fixture({ permission: deniedPermission })
    const deniedStart = denied.voice.startListening()
    denied.voice.dispose()
    deniedPermission.permission.value = 'denied'
    deniedGate.resolve()
    await expect(deniedStart).resolves.toBe(false)
    expect(denied.voice.error.value).toBeNull()
    expect(denied.voice.wantsListening.value).toBe(false)
  })

  it('stops active capture and pending STT immediately when microphone permission is revoked', async () => {
    const transcriptGate = deferred<{ utteranceId: string; text: string; final: true }>()
    const signals: AbortSignal[] = []
    const permission = fakePermission()
    const f = fixture({
      permission,
      transcribe: vi.fn((_audio: Blob, _language: string, signal?: AbortSignal) => {
        if (signal) signals.push(signal)
        return transcriptGate.promise
      }),
    })
    await f.voice.startListening()
    const oldSink = f.mic.sinks[0]!
    const pending = f.mic.emit()
    await flush()

    permission.permission.value = 'denied'
    expect(f.voice.wantsListening.value).toBe(false)
    expect(f.mic.stop).toHaveBeenCalled()
    expect(signals[0]?.aborted).toBe(true)
    expect(f.voice.error.value).toBe('microphone')

    transcriptGate.resolve({
      utteranceId: 'utt_88888888888888888888888888888888', text: 'next', final: true,
    })
    await pending
    await oldSink(new Blob(['late-denied'], { type: 'audio/webm' }))
    expect(f.transcribe).toHaveBeenCalledOnce()
    expect(f.actions.selectDelivery).not.toHaveBeenCalled()
    permission.permission.value = 'granted'
    await flush()
    expect(f.mic.start).toHaveBeenCalledOnce()
    expect(f.voice.wantsListening.value).toBe(false)
  })

  it('discards an old recorder and late STT result when selection changes', async () => {
    const transcriptGate = deferred<{ utteranceId: string; text: string; final: true }>()
    const transcriptSignals: AbortSignal[] = []
    const f = fixture({
      transcribe: vi.fn((_audio: Blob, _language: string, signal?: AbortSignal) => {
        if (signal) transcriptSignals.push(signal)
        return transcriptGate.promise
      }),
    })
    await f.voice.initialize()
    await f.voice.startListening()
    const oldSink = f.mic.sinks[0]!
    await f.voice.acceptPartial('partial-a', 'note that PRIVATE_CANARY')
    const pending = oldSink(new Blob(['audio-a'], { type: 'audio/webm' }))
    await flush()
    expect(f.transcribe).toHaveBeenCalledOnce()

    const resetToken = f.voice.inputResetToken.value
    f.selectedId.value = f.deliveries.value[1]!.id
    await flush()
    expect(transcriptSignals[0]?.aborted).toBe(true)
    expect(f.voice.draft.value).toBe('')
    expect(f.voice.inputResetToken.value).toBe(resetToken + 1)
    expect(f.mic.stop).toHaveBeenCalled()
    expect(f.mic.start).toHaveBeenCalledTimes(2)

    transcriptGate.resolve({
      utteranceId: 'utt_33333333333333333333333333333333',
      text: 'note that PRIVATE_CANARY',
      final: true,
    })
    await pending
    expect(f.voice.note.value).toBeNull()
    expect(f.actions.selectDelivery).not.toHaveBeenCalled()

    // A callback retained by the old recorder carries A's context token even
    // after the adapter has been re-armed for B, so it is rejected pre-STT.
    await oldSink(new Blob(['spanning-a-b'], { type: 'audio/webm' }))
    expect(f.transcribe).toHaveBeenCalledOnce()
  })

  it.each(SAME_ID_STT_ROTATIONS)('drops a pending STT final across same-id $name rotation', async ({ prepare, rotate }) => {
    const transcriptGate = deferred<{ utteranceId: string; text: string; final: true }>()
    const signals: AbortSignal[] = []
    const f = fixture({
      transcribe: vi.fn((_audio: Blob, _language: string, signal?: AbortSignal) => {
        if (signal) signals.push(signal)
        return transcriptGate.promise
      }),
    })
    if (prepare) {
      f.deliveries.value = f.deliveries.value.map((row) => row.id === f.selectedId.value
        ? prepare(row)
        : row)
      await flush()
    }
    await f.voice.startListening()
    const oldSink = f.mic.sinks[0]!
    const pending = oldSink(new Blob(['same-id-pending'], { type: 'audio/webm' }))
    await flush()
    f.deliveries.value = f.deliveries.value.map((row) => row.id === f.selectedId.value
      ? rotate(row)
      : row)
    await flush()
    expect(signals[0]?.aborted).toBe(true)
    transcriptGate.resolve({
      utteranceId: 'utt_44444444444444444444444444444444',
      text: 'note that SAME_ID_PRIVATE_CANARY',
      final: true,
    })
    await pending
    expect(f.voice.note.value).toBeNull()
    expect(f.actions.selectDelivery).not.toHaveBeenCalled()
    await oldSink(new Blob(['same-id-old-sink'], { type: 'audio/webm' }))
    expect(f.transcribe).toHaveBeenCalledOnce()
  })

  it('releases the STT audio frame before queued effects and never maps action failure to transcription', async () => {
    const gate = deferred<boolean>()
    const blocked = fixture({
      transcribe: vi.fn(async () => ({
        utteranceId: 'utt_55555555555555555555555555555555',
        text: 'note that PRIVATE_QUEUE_CANARY',
        final: true as const,
      })),
    })
    blocked.actions.selectDelivery.mockImplementationOnce(() => gate.promise)
    const predecessor = blocked.voice.submitTyped('next')
    await flush()
    const queuedClarification = blocked.voice.submitTyped('select title Shared migration')
    const queuedQuery = blocked.voice.submitTyped('my password CANARY; search for reconnect')
    await blocked.voice.startListening()
    let micSettled = false
    const emitted = blocked.mic.emit().then(() => { micSettled = true })
    await flush()
    expect(micSettled).toBe(true)
    expect(blocked.voice.note.value?.binding.body).toBe('PRIVATE_QUEUE_CANARY')
    expect(blocked.voice.pendingEffectBatchCount()).toBe(3)
    blocked.voice.dispose()
    expect(blocked.voice.pendingEffectBatchCount()).toBe(0)
    gate.resolve(true)
    await Promise.all([predecessor, queuedClarification, queuedQuery, emitted])
    expect(blocked.voice.note.value).toBeNull()
    expect(blocked.voice.error.value).toBeNull()
    expect(blocked.postNote).not.toHaveBeenCalled()
    expect(blocked.actions.setFilters).not.toHaveBeenCalled()
    expect(blocked.speak).not.toHaveBeenCalled()

    const failing = fixture({
      transcribe: vi.fn(async () => ({
        utteranceId: 'utt_66666666666666666666666666666666',
        text: 'next',
        final: true as const,
      })),
    })
    failing.actions.selectDelivery.mockRejectedValueOnce(new Error('editor action failed'))
    await failing.voice.startListening()
    await failing.mic.emit()
    await flush()
    expect(failing.voice.error.value).not.toBe('transcription')
  })

  it('advances resolver context only for semantic delivery/order changes and preserves a valid note', async () => {
    const f = fixture()
    await f.voice.initialize()
    await f.voice.submitTyped('note that Keep the bound preview')
    const note = f.voice.note.value
    await f.voice.startListening()
    await f.voice.acceptPartial('partial-revision', 'PRIVATE PARTIAL')
    const resetToken = f.voice.inputResetToken.value
    const starts = f.mic.start.mock.calls.length
    const stops = f.mic.stop.mock.calls.length

    f.deliveries.value = f.deliveries.value.map((row) => row.id === f.selectedId.value
      ? { ...row, deliveryRevision: `${row.deliveryRevision}:next` }
      : row)
    await flush()
    expect(f.voice.draft.value).toBe('')
    expect(f.voice.note.value).toEqual(note)
    expect(f.voice.inputResetToken.value).toBe(resetToken)
    expect(f.mic.stop).toHaveBeenCalledTimes(stops + 1)
    expect(f.mic.start).toHaveBeenCalledTimes(starts + 1)

    // A clone with the same resolver fields is an ordinary poll: no capture
    // interruption and no typed-input security reset.
    const startsAfterRevision = f.mic.start.mock.calls.length
    const stopsAfterRevision = f.mic.stop.mock.calls.length
    f.deliveries.value = f.deliveries.value.map((row) => ({ ...row }))
    await flush()
    expect(f.mic.start).toHaveBeenCalledTimes(startsAfterRevision)
    expect(f.mic.stop).toHaveBeenCalledTimes(stopsAfterRevision)
    expect(f.voice.note.value).toEqual(note)

    f.travelOrder.value = [...f.travelOrder.value].reverse()
    await flush()
    expect(f.mic.start).toHaveBeenCalledTimes(startsAfterRevision + 1)
    expect(f.mic.stop).toHaveBeenCalledTimes(stopsAfterRevision + 1)

    f.deliveries.value = f.deliveries.value.map((row) => row.id === f.selectedId.value
      ? { ...row, attempt: { ...row.attempt, id: 'attempt-rotated' } }
      : row)
    await flush()
    expect(f.voice.note.value).toBeNull()
    expect(f.voice.inputResetToken.value).toBe(resetToken + 1)
  })

  it('keeps voice replies independent, uses only safe templates, and barge-in cancels speech', async () => {
    const f = fixture()
    await f.voice.initialize()
    await f.voice.submitTyped('select title Shared migration')
    expect(f.voice.candidates.value).toHaveLength(2)
    expect(f.speak).not.toHaveBeenCalled()
    expect(f.voice.replyState.value).toBe('off')

    f.voice.setVoiceReplies(true)
    await f.voice.submitTyped('select title Shared migration')
    expect(f.speak).toHaveBeenCalledOnce()
    const request = f.speak.mock.calls[0][0]
    expect(request).toMatchObject({
      template: 'clarification',
      deliveryId: f.selectedId.value,
      candidateIds: f.voice.candidates.value.map((candidate) => candidate.deliveryId),
    })
    expect(JSON.stringify(request)).not.toContain('Shared migration')

    await f.voice.startListening()
    expect(f.playback.cancel).toHaveBeenLastCalledWith(false)
    expect(f.voice.wantsListening.value).toBe(true)
  })

  it('does not narrate a clarification queued under a superseded selection context', async () => {
    const gate = deferred<boolean>()
    const f = fixture()
    f.actions.selectDelivery.mockImplementationOnce(() => gate.promise)
    f.voice.setVoiceReplies(true)
    const blocking = f.voice.submitTyped('next')
    await flush()
    const command = f.voice.submitTyped('select title Shared migration')
    const savedCandidates = f.voice.machine.value.candidates
    f.selectedId.value = f.deliveries.value[1].id
    f.selectedId.value = f.deliveries.value[0].id
    // Even if a future reducer mutation accidentally retains the same exact
    // candidates after A → B → A, the queued effect remains epoch-bound.
    f.voice.machine.value = {
      ...f.voice.machine.value,
      candidates: savedCandidates,
      candidateMatchCount: savedCandidates.length,
    }
    gate.resolve(true)
    await Promise.all([blocking, command])
    expect(f.speak).not.toHaveBeenCalled()
  })

  it('drops every queued action when the exact travel context changes', async () => {
    const gate = deferred<boolean>()
    const f = fixture()
    f.actions.selectDelivery.mockImplementationOnce(() => gate.promise)
    const blocking = f.voice.submitTyped('next')
    await flush()
    expect(f.actions.selectDelivery).toHaveBeenCalledOnce()

    const queued = f.voice.submitTyped('next')
    f.travelOrder.value = [
      f.travelOrder.value[0]!,
      f.travelOrder.value[2]!,
      f.travelOrder.value[1]!,
    ]
    gate.resolve(true)
    await Promise.all([blocking, queued])
    expect(f.actions.selectDelivery).toHaveBeenCalledOnce()
  })

  it('drops a queued project command when the authorized catalog aliases change', async () => {
    const loader = vi.fn()
      .mockResolvedValueOnce([{ projectId: 12, projectKey: 'PAI', projectName: 'Paimos' }])
      .mockResolvedValueOnce([{ projectId: 12, projectKey: 'PAI', projectName: 'Renamed' }])
    const gate = deferred<boolean>()
    const f = fixture({ loadProjectCatalog: loader })
    await f.voice.initialize()
    await f.voice.startListening()
    const oldSink = f.mic.sinks[f.mic.sinks.length - 1]!
    f.actions.selectDelivery.mockImplementationOnce(() => gate.promise)
    const blocking = f.voice.submitTyped('next')
    await flush()
    const queued = f.voice.submitTyped('project Paimos')

    await f.voice.reloadProjectCatalog()
    await oldSink(new Blob(['old-catalog-audio'], { type: 'audio/webm' }))
    gate.resolve(true)
    await Promise.all([blocking, queued])
    expect(f.actions.setFilters).not.toHaveBeenCalled()
    expect(f.transcribe).not.toHaveBeenCalled()
    expect(f.voice.projectCatalog.value).toEqual([
      { projectId: 12, projectKey: 'PAI', projectName: 'Renamed' },
    ])
  })

  it('binds a preview to the selection, focuses preview before confirm, and double-confirm posts once', async () => {
    const noteGate = deferred<{ id: number }>()
    const f = fixture({ postNote: vi.fn(() => noteGate.promise) })
    await f.voice.initialize()
    f.voice.setVoiceReplies(true)
    await f.voice.submitTyped('note that Keep punctuation — bitte.')
    const preview = f.voice.note.value!
    expect(preview.status).toBe('preview')
    expect(preview.binding.body).toBe('Keep punctuation — bitte.')
    expect(f.voice.noteFocusToken.value).toBe(1)
    expect(f.speak.mock.calls[0][0]).toMatchObject({ template: 'note_ready', candidateIds: [] })
    expect(JSON.stringify(f.speak.mock.calls[0][0])).not.toContain(preview.binding.body)

    const first = f.voice.confirmNote()
    const second = f.voice.confirmNote()
    await flush()
    expect(f.postNote).toHaveBeenCalledOnce()
    expect(f.postNote.mock.calls[0][0]).toEqual({
      issueId: preview.binding.issueId,
      body: preview.binding.body,
      clientRequestId: preview.binding.clientRequestId,
    })
    noteGate.resolve({ id: 1 })
    await Promise.all([first, second])
    expect(f.voice.note.value).toBeNull()
    expect(f.actions.notePosted).toHaveBeenCalledOnce()
  })

  it('holds a note offline, reauthorizes in connectivity-before-context order, and requires one fresh confirm', async () => {
    const f = fixture()
    await f.voice.initialize()
    await f.voice.submitTyped('note that Retry this once')
    const requestId = f.voice.note.value!.binding.clientRequestId

    f.online.value = false
    expect(f.voice.note.value?.status).toBe('held_offline')
    await f.voice.confirmNote()
    expect(f.postNote).not.toHaveBeenCalled()

    f.deliveries.value = f.deliveries.value.map((row) => ({ ...row }))
    f.online.value = true
    await flush()
    expect(f.voice.note.value?.status).toBe('preview')
    expect(f.postNote).not.toHaveBeenCalled()

    await f.voice.confirmNote()
    expect(f.postNote).toHaveBeenCalledOnce()
    expect(f.postNote.mock.calls[0][0].clientRequestId).toBe(requestId)
  })

  it('awaits the existing selection/editor guard and never treats rejection as success', async () => {
    const f = fixture()
    f.actions.selectDelivery.mockResolvedValue(false)
    await f.voice.submitTyped(f.deliveries.value[1].issueKey)
    expect(f.actions.selectDelivery).toHaveBeenCalledWith(f.deliveries.value[1].id)
    expect(f.voice.notice.value).toBe('selection_blocked')

    f.actions.showDetails.mockResolvedValue(false)
    await f.voice.submitTyped('show details')
    expect(f.actions.showDetails).toHaveBeenCalledWith(f.selectedId.value)
    expect(f.voice.notice.value).toBe('selection_blocked')
  })

  it('passes only a canonical explicit search operand and never the surrounding utterance', async () => {
    const f = fixture()
    await f.voice.submitTyped('my password CANARY; search for reconnect')
    expect(f.actions.setFilters).toHaveBeenCalledWith({ query: 'reconnect' })
    expect(JSON.stringify(f.actions.setFilters.mock.calls)).not.toContain('CANARY')
    expect(JSON.stringify(f.actions.setFilters.mock.calls)).not.toContain('password')

    f.actions.setFilters.mockClear()
    await f.voice.submitTyped(`search for ${'é'.repeat(81)}`)
    expect(f.actions.setFilters).not.toHaveBeenCalled()
    expect(f.voice.notice.value).toBe('unknown_command')
  })

  it('interlocks a permission-pending mic with TTS until audio settles and honors a late denial', async () => {
    const permissionGate = deferred<void>()
    const permission = fakePermission(() => permissionGate.promise)
    const playback = realPlaybackHarness()
    const f = fixture({ permission, createPlayback: playback.factory })
    const starting = f.voice.startListening()
    f.voice.setVoiceReplies(true)
    const speaking = f.voice.submitTyped('read status')
    await flush()
    expect(playback.audios).toHaveLength(1)
    expect(f.mic.start).not.toHaveBeenCalled()

    permissionGate.resolve()
    await expect(starting).resolves.toBe(false)
    expect(f.mic.start).not.toHaveBeenCalled()
    playback.audios[0].onended?.(new Event('ended'))
    await flush()
    expect(f.mic.start).toHaveBeenCalledOnce()
    await speaking

    const deniedGate = deferred<void>()
    const deniedPermission = fakePermission(() => deniedGate.promise)
    const deniedPlayback = realPlaybackHarness()
    const denied = fixture({ permission: deniedPermission, createPlayback: deniedPlayback.factory })
    const deniedStart = denied.voice.startListening()
    denied.voice.setVoiceReplies(true)
    const deniedSpeech = denied.voice.submitTyped('read status')
    await flush()
    deniedPermission.permission.value = 'denied'
    deniedGate.resolve()
    await expect(deniedStart).resolves.toBe(false)
    expect(denied.voice.wantsListening.value).toBe(false)
    expect(denied.voice.error.value).toBe('microphone')
    deniedPlayback.audios[0].onended?.(new Event('ended'))
    await flush()
    expect(denied.mic.start).not.toHaveBeenCalled()
    await deniedSpeech
  })

  it('cancels pending and active speech when the snapshot becomes degraded or offline', async () => {
    const speechGate = deferred<Blob>()
    const pendingPlayback = realPlaybackHarness()
    const pending = fixture({
      createPlayback: pendingPlayback.factory,
      speak: vi.fn(() => speechGate.promise),
    })
    pending.voice.setVoiceReplies(true)
    const command = pending.voice.submitTyped('read status')
    await flush()
    expect(pendingPlayback.audios).toEqual([])
    pending.degraded.value = true
    speechGate.resolve(new Blob(['late'], { type: 'audio/mpeg' }))
    await command
    expect(pendingPlayback.audios).toEqual([])

    const activePlayback = realPlaybackHarness()
    const active = fixture({ createPlayback: activePlayback.factory })
    active.voice.setVoiceReplies(true)
    await active.voice.submitTyped('read status')
    expect(activePlayback.audios).toHaveLength(1)
    active.online.value = false
    expect(activePlayback.audios[0].pause).toHaveBeenCalledOnce()
    expect(activePlayback.revoked).toEqual(['blob:agent-voice-1'])
  })

  it('cancels bound clarification and note-ready audio on local or authority invalidation', async () => {
    const statusPlayback = realPlaybackHarness()
    const status = fixture({ createPlayback: statusPlayback.factory })
    status.voice.setVoiceReplies(true)
    await status.voice.submitTyped('read status')
    status.deliveries.value = status.deliveries.value.map((row) => row.id === status.selectedId.value
      ? { ...row, deliveryRevision: `${row.deliveryRevision}:new` }
      : row)
    expect(statusPlayback.audios[0].pause).toHaveBeenCalledOnce()

    const clarificationPlayback = realPlaybackHarness()
    const clarification = fixture({ createPlayback: clarificationPlayback.factory })
    clarification.voice.setVoiceReplies(true)
    await clarification.voice.submitTyped('select title Shared migration')
    expect(clarificationPlayback.audios).toHaveLength(1)
    const choosing = clarification.voice.chooseCandidate(1)
    expect(clarificationPlayback.audios[0].pause).toHaveBeenCalledOnce()
    expect(clarificationPlayback.revoked).toEqual(['blob:agent-voice-1'])
    await choosing

    const revokedPlayback = realPlaybackHarness()
    const revoked = fixture({ createPlayback: revokedPlayback.factory })
    revoked.voice.setVoiceReplies(true)
    await revoked.voice.submitTyped('select title Shared migration')
    const revokedId = revoked.voice.candidates.value[0].deliveryId
    revoked.deliveries.value = revoked.deliveries.value.filter((row) => row.id !== revokedId)
    expect(revokedPlayback.audios[0].pause).toHaveBeenCalledOnce()

    const reorderedPlayback = realPlaybackHarness()
    const reordered = fixture({ createPlayback: reorderedPlayback.factory })
    reordered.voice.setVoiceReplies(true)
    await reordered.voice.submitTyped('select title Shared migration')
    reordered.voice.machine.value = {
      ...reordered.voice.machine.value,
      candidates: [...reordered.voice.machine.value.candidates].reverse(),
    }
    await reordered.voice.acceptPartial('reorder-check', 'two')
    expect(reorderedPlayback.audios[0].pause).toHaveBeenCalledOnce()
    expect(reorderedPlayback.revoked).toEqual(['blob:agent-voice-1'])

    const notePlayback = realPlaybackHarness()
    const note = fixture({ createPlayback: notePlayback.factory })
    note.voice.setVoiceReplies(true)
    await note.voice.submitTyped('note that Keep this private')
    expect(notePlayback.audios).toHaveLength(1)
    const cancelling = note.voice.cancelNote()
    expect(notePlayback.audios[0].pause).toHaveBeenCalledOnce()
    expect(notePlayback.revoked).toEqual(['blob:agent-voice-1'])
    await cancelling

    const confirmPlayback = realPlaybackHarness()
    const confirm = fixture({ createPlayback: confirmPlayback.factory })
    confirm.voice.setVoiceReplies(true)
    await confirm.voice.submitTyped('note that Confirm this once')
    await confirm.voice.confirmNote()
    expect(confirmPlayback.audios[0].pause).toHaveBeenCalledOnce()
    expect(confirmPlayback.revoked).toEqual(['blob:agent-voice-1'])

    const capabilityPlayback = realPlaybackHarness()
    const capability = fixture({ createPlayback: capabilityPlayback.factory })
    capability.voice.setVoiceReplies(true)
    await capability.voice.submitTyped('note that Same revision capability check')
    capability.deliveries.value = capability.deliveries.value.map((row) => row.id === capability.selectedId.value
      ? { ...row, capabilities: { ...row.capabilities, comment: false } }
      : row)
    expect(capabilityPlayback.audios[0].pause).toHaveBeenCalledOnce()
    expect(capability.voice.note.value).toBeNull()
  })

  it('drops STT and TTS results that themselves reveal a newer permissions epoch', async () => {
    permissionsEpoch.value = '10'
    const stt = fixture({
      transcribe: vi.fn(async () => {
        permissionsEpoch.value = '11'
        return { utteranceId: 'utt_22222222222222222222222222222222', text: 'next', final: true as const }
      }),
    })
    await stt.voice.startListening()
    await stt.mic.emit()
    expect(stt.actions.selectDelivery).not.toHaveBeenCalled()
    expect(stt.voice.machine.value.deliveries).toEqual([])

    stt.authorityVersion.value = 2
    await flush()
    const ttsPlayback = realPlaybackHarness()
    const tts = fixture({
      createPlayback: ttsPlayback.factory,
      speak: vi.fn(async () => {
        permissionsEpoch.value = '12'
        return new Blob(['stale'], { type: 'audio/mpeg' })
      }),
    })
    tts.voice.setVoiceReplies(true)
    await tts.voice.submitTyped('read status')
    expect(ttsPlayback.audios).toEqual([])
    expect(tts.voice.machine.value.deliveries).toEqual([])
  })

  it('reseeds offline state on an authority reset and detaches new commands from a hung old effect', async () => {
    permissionsEpoch.value = '20'
    const offline = fixture()
    offline.online.value = false
    permissionsEpoch.value = '21'
    offline.authorityVersion.value = 2
    await flush()
    await offline.voice.submitTyped('note that Never post while offline')
    expect(offline.voice.note.value?.status).toBe('held_offline')
    await offline.voice.confirmNote()
    expect(offline.voice.note.value?.status).toBe('held_offline')
    expect(offline.postNote).not.toHaveBeenCalled()

    const oldAction = deferred<boolean>()
    permissionsEpoch.value = '30'
    const active = fixture()
    active.actions.selectDelivery
      .mockImplementationOnce(() => oldAction.promise)
      .mockImplementationOnce(async (id: string) => { active.selectedId.value = id; return true })
    const oldCommand = active.voice.submitTyped('next')
    await flush()
    permissionsEpoch.value = '31'
    active.authorityVersion.value = 2
    await flush()
    const newCommand = active.voice.submitTyped('next')
    await newCommand
    expect(active.actions.selectDelivery).toHaveBeenCalledTimes(2)
    expect(active.voice.notice.value).toBe('selection_updated')
    oldAction.resolve(false)
    await oldCommand
    expect(active.voice.notice.value).toBe('selection_updated')
  })

  it('accepts the first valid epoch baseline catalog and preserves notes across ordinary committed refreshes', async () => {
    const loader = vi.fn(async () => {
      permissionsEpoch.value = '44'
      return [{ projectId: 12, projectKey: 'PAI', projectName: 'Paimos' }]
    })
    const f = fixture({ loadProjectCatalog: loader })
    await f.voice.initialize()
    expect(f.voice.projectCatalog.value).toEqual([{ projectId: 12, projectKey: 'PAI', projectName: 'Paimos' }])
    expect(loader).toHaveBeenCalledOnce()

    await f.voice.submitTyped('note that Preserve this exact draft')
    const note = f.voice.note.value
    f.authorityVersion.value += 1
    await flush()
    expect(f.voice.note.value).toEqual(note)
  })

  it('leaves epoch refresh ownership to deliveries and never starts a stale duplicate load', async () => {
    permissionsEpoch.value = '50'
    const disposed = fixture()
    permissionsEpoch.value = '51'
    disposed.voice.dispose()
    await flush()
    expect(disposed.actions.authorityChanged).not.toHaveBeenCalled()

    permissionsEpoch.value = '60'
    const superseded = fixture()
    permissionsEpoch.value = '61'
    permissionsEpoch.value = '62'
    await flush()
    expect(superseded.actions.authorityChanged).not.toHaveBeenCalled()
  })

  it('does not duplicate the parent snapshot load when a session is restored', async () => {
    permissionsEpoch.value = '70'
    const f = fixture()
    await f.voice.initialize()
    expect(f.loadProjectCatalog).toHaveBeenCalledOnce()

    sessionExpired.value = true
    sessionExpired.value = false
    permissionsEpoch.value = '71'
    await flush()
    expect(f.actions.authorityChanged).not.toHaveBeenCalled()
    expect(f.voice.machine.value.deliveries).toEqual([])

    // The parent deliveries composable's one successful load supplies the
    // strictly-later proof; voice then reopens and reloads vocabulary once.
    f.authorityVersion.value = 2
    await flush()
    expect(f.actions.authorityChanged).not.toHaveBeenCalled()
    expect(f.voice.machine.value.deliveries).toHaveLength(f.deliveries.value.length)
    expect(f.loadProjectCatalog).toHaveBeenCalledTimes(2)

    f.authorityVersion.value = 3
    await flush()
    expect(f.loadProjectCatalog).toHaveBeenCalledTimes(2)
  })

  it('uses the selector-independent project catalog and fences stale late catalogs on session/epoch/reset', async () => {
    const firstCatalog = deferred<Array<{ projectId: number; projectKey: string; projectName: string }>>()
    const secondCatalog = deferred<Array<{ projectId: number; projectKey: string; projectName: string }>>()
    const loader = vi.fn()
      .mockImplementationOnce(() => firstCatalog.promise)
      .mockImplementationOnce(() => secondCatalog.promise)
      .mockResolvedValue([{ projectId: 8, projectKey: 'NEW', projectName: 'New authority' }])
    permissionsEpoch.value = '4'
    const f = fixture({ loadProjectCatalog: loader })
    const initializing = f.voice.initialize()
    await flush()
    expect(loader).toHaveBeenCalledOnce()

    sessionExpired.value = true
    firstCatalog.resolve([{ projectId: 99, projectKey: 'OLD', projectName: 'Stale secret' }])
    await initializing
    expect(f.voice.projectCatalog.value).toEqual([])
    expect(f.voice.machine.value.deliveries).toEqual([])

    sessionExpired.value = false
    await flush()
    expect(f.voice.projectCatalog.value).toEqual([])
    f.authorityVersion.value = 2
    secondCatalog.resolve([{ projectId: 7, projectKey: 'OPS', projectName: 'Operations' }])
    await flush()
    expect(f.voice.projectCatalog.value).toEqual([{ projectId: 7, projectKey: 'OPS', projectName: 'Operations' }])
    await f.voice.submitTyped('project Operations')
    expect(f.actions.setFilters).toHaveBeenCalledWith({ projectId: 7, laneKey: null })

    permissionsEpoch.value = '5'
    expect(f.voice.projectCatalog.value).toEqual([])
    expect(f.voice.machine.value.deliveries).toEqual([])
    f.deliveries.value = f.deliveries.value.map((row) => ({ ...row }))
    await flush()
    expect(f.voice.projectCatalog.value).toEqual([])
    f.authorityVersion.value = 1
    await flush()
    expect(f.voice.projectCatalog.value).toEqual([])
    f.authorityVersion.value = 3
    await flush()
    expect(f.voice.projectCatalog.value).toEqual([{ projectId: 8, projectKey: 'NEW', projectName: 'New authority' }])
  })

  it('clears candidates, dictated text, mic, and pending network work on selection revocation and dispose', async () => {
    const f = fixture()
    await f.voice.initialize()
    await f.voice.submitTyped('select title Shared migration')
    await f.voice.submitTyped('note that private teardown body')
    expect(f.voice.note.value).not.toBeNull()

    f.selectedId.value = f.deliveries.value[1].id
    await flush()
    expect(f.voice.note.value).toBeNull()
    expect(f.voice.candidates.value).toEqual([])

    await f.voice.startListening()
    f.voice.dispose()
    expect(f.mic.stop).toHaveBeenCalled()
    expect(f.voice.machine.value.draft).toBe('')
    expect(f.voice.machine.value.candidates).toEqual([])
    expect(f.voice.machine.value.note).toBeNull()
  })

  it('aborts catalog, transcript, speech, and note transports and ignores every late settlement on dispose', async () => {
    const catalogGate = deferred<Array<{ projectId: number; projectKey: string; projectName: string }>>()
    const catalogSignals: AbortSignal[] = []
    const catalog = fixture({
      loadProjectCatalog: vi.fn((signal?: AbortSignal) => {
        if (signal) catalogSignals.push(signal)
        return catalogGate.promise
      }),
    })
    const initializing = catalog.voice.initialize()

    const transcriptGate = deferred<{ utteranceId: string; text: string; final: true }>()
    const transcriptSignals: AbortSignal[] = []
    const transcript = fixture({
      transcribe: vi.fn((_audio: Blob, _language: string, signal?: AbortSignal) => {
        if (signal) transcriptSignals.push(signal)
        return transcriptGate.promise
      }),
    })
    await transcript.voice.startListening()
    const transcribing = transcript.mic.emit()

    const speechGate = deferred<Blob>()
    const speechSignals: AbortSignal[] = []
    const speech = fixture({
      speak: vi.fn((_request, signal?: AbortSignal) => {
        if (signal) speechSignals.push(signal)
        return speechGate.promise
      }),
    })
    speech.voice.setVoiceReplies(true)
    const speaking = speech.voice.submitTyped('read status')

    const noteGate = deferred<{ id: number }>()
    const noteSignals: AbortSignal[] = []
    const note = fixture({
      postNote: vi.fn((_request, signal?: AbortSignal) => {
        if (signal) noteSignals.push(signal)
        return noteGate.promise
      }),
    })
    await note.voice.submitTyped('note that PRIVATE_DISPOSE_CANARY')
    const posting = note.voice.confirmNote()
    await flush()

    expect(catalogSignals).toHaveLength(1)
    expect(transcriptSignals).toHaveLength(1)
    expect(speechSignals).toHaveLength(1)
    expect(noteSignals).toHaveLength(1)
    for (const subject of [catalog, transcript, speech, note]) subject.voice.dispose()
    expect([
      catalogSignals[0]!.aborted,
      transcriptSignals[0]!.aborted,
      speechSignals[0]!.aborted,
      noteSignals[0]!.aborted,
    ]).toEqual([true, true, true, true])

    catalogGate.resolve([{ projectId: 99, projectKey: 'OLD', projectName: 'Stale' }])
    transcriptGate.resolve({
      utteranceId: 'utt_77777777777777777777777777777777', text: 'next', final: true,
    })
    speechGate.resolve(new Blob(['late'], { type: 'audio/mpeg' }))
    noteGate.resolve({ id: 1 })
    await Promise.all([initializing, transcribing, speaking, posting])
    for (const subject of [catalog, transcript, speech, note]) {
      expect(subject.voice.error.value).toBeNull()
      expect(subject.voice.note.value).toBeNull()
      expect(subject.voice.projectCatalog.value).toEqual([])
    }
    expect(transcript.actions.selectDelivery).not.toHaveBeenCalled()
    expect(note.actions.notePosted).not.toHaveBeenCalled()
  })
})
