/* PAI-862 — one-generation voice delivery for the Paimos 6 preview. */

import { computed, getCurrentInstance, onBeforeUnmount, ref, watch, type Ref } from 'vue'

import { ApiError } from '@/api/client'
import { useMicPermission } from '@/composables/useMicPermission'
import { useMicTranscript } from '@/composables/useMicTranscript'
import { transcribeAgentModeAudio, type AgentModeVoiceLanguage, type AgentModeVoiceTranscription } from '@/services/agentModeVoice'
import {
  sendPaimos6SessionUtterance,
  type Paimos6SessionUtteranceRequest,
  type Paimos6SessionUtteranceResult,
  type Paimos6SelectedSession,
} from '@/v6/sessionUtterance'

export type Paimos6VoiceState =
  | 'idle'
  | 'permission'
  | 'listening'
  | 'transcribing'
  | 'sending'
  | 'delivered'
  | 'retryable'
  | 'stale'
  | 'unavailable'

export interface Paimos6VoiceSelection extends Paimos6SelectedSession {
  destination: string
  available: boolean
}

type MicAdapter = ReturnType<typeof useMicTranscript>
type PermissionAdapter = ReturnType<typeof useMicPermission>

export interface Paimos6VoiceDependencies {
  mic?: MicAdapter
  permission?: PermissionAdapter
  transcribe?: (
    audio: Blob,
    language: AgentModeVoiceLanguage,
    signal?: AbortSignal,
  ) => Promise<AgentModeVoiceTranscription>
  send?: (
    request: Paimos6SessionUtteranceRequest,
    signal?: AbortSignal,
  ) => Promise<Paimos6SessionUtteranceResult>
}

export interface UsePaimos6VoiceOptions {
  principalId: Readonly<Ref<number | null>>
  authorityKey: Readonly<Ref<string>>
  projectId: Readonly<Ref<number | null>>
  selection: Readonly<Ref<Paimos6VoiceSelection | null>>
  locale: Readonly<Ref<string>>
  dependencies?: Paimos6VoiceDependencies
}

interface FrozenBinding {
  generation: number
  context: string
  projectId: number
  selectedSession: Paimos6SelectedSession | null
  destination: string
}

interface RetryableDelivery {
  binding: FrozenBinding
  request: Paimos6SessionUtteranceRequest
}

function language(locale: string): AgentModeVoiceLanguage {
  return locale.toLowerCase().startsWith('de') ? 'de' : 'en'
}

function problemCode(error: unknown): string | null {
  return error instanceof ApiError && typeof error.code === 'string' ? error.code : null
}

export function usePaimos6Voice(options: UsePaimos6VoiceOptions) {
  const dependencies = options.dependencies ?? {}
  const mic = dependencies.mic ?? useMicTranscript()
  const permission = dependencies.permission ?? useMicPermission()
  const transcribe = dependencies.transcribe ?? transcribeAgentModeAudio
  const send = dependencies.send ?? sendPaimos6SessionUtterance

  const state = ref<Paimos6VoiceState>('idle')
  const message = ref('Ready for one ephemeral utterance to Paimos.')
  const deliveredDestination = ref<string | null>(null)
  let alive = true
  let generation = 0
  let binding: FrozenBinding | null = null
  let retryable: RetryableDelivery | null = null
  let transcriptionController: AbortController | null = null
  let deliveryController: AbortController | null = null

  function contextSignature(): string {
    const selection = options.selection.value
    return JSON.stringify([
      options.authorityKey.value,
      options.principalId.value,
      options.projectId.value,
      selection === null ? null : [
        selection.productSessionId,
        selection.revision,
        selection.destination,
        selection.available,
      ],
    ])
  }

  function contextCurrent(candidate: FrozenBinding): boolean {
    return alive && candidate.generation === generation && candidate.context === contextSignature()
  }

  function releaseOperations() {
    transcriptionController?.abort()
    transcriptionController = null
    deliveryController?.abort()
    deliveryController = null
    mic.stop()
  }

  function invalidate(nextState: 'idle' | 'stale', nextMessage: string) {
    generation += 1
    releaseOperations()
    binding = null
    retryable = null
    deliveredDestination.value = null
    state.value = nextState
    message.value = nextMessage
  }

  function freezeBinding(): FrozenBinding | null {
    const principalId = options.principalId.value
    const projectId = options.projectId.value
    if (principalId === null || projectId === null) return null
    const selected = options.selection.value
    return {
      generation,
      context: contextSignature(),
      projectId,
      selectedSession: selected === null ? null : {
        productSessionId: selected.productSessionId,
        revision: selected.revision,
      },
      destination: selected?.destination ?? 'Paimos',
    }
  }

  function failDelivery(error: unknown, candidate: FrozenBinding, request: Paimos6SessionUtteranceRequest) {
    if (!contextCurrent(candidate)) return
    const code = problemCode(error)
    if (code === 'session_utterance_selection_stale') {
      retryable = null
      state.value = 'stale'
      message.value = 'The selected session changed before delivery. Review the current selection and try again.'
      return
    }
    if (code === 'session_utterance_target_unavailable' || code === 'session_utterance_not_found') {
      retryable = null
      state.value = 'unavailable'
      message.value = 'The selected destination is no longer available. Nothing was rerouted.'
      return
    }
    retryable = { binding: candidate, request }
    state.value = 'retryable'
    message.value = `Delivery to ${candidate.destination} failed. Retry will reuse the same utterance ID and body.`
  }

  async function deliver(candidate: FrozenBinding, request: Paimos6SessionUtteranceRequest) {
    deliveryController?.abort()
    const controller = new AbortController()
    deliveryController = controller
    state.value = 'sending'
    message.value = `Sending the finalized utterance to ${candidate.destination}…`
    try {
      const result = await send(request, controller.signal)
      if (!contextCurrent(candidate) || deliveryController !== controller) return
      retryable = null
      deliveredDestination.value = candidate.destination
      state.value = 'delivered'
      message.value = result.routeKind === 'paimos'
        ? 'Delivered to Paimos.'
        : `Delivered to ${candidate.destination}.`
    } catch (error) {
      if (!controller.signal.aborted) failDelivery(error, candidate, request)
    } finally {
      if (deliveryController === controller) deliveryController = null
    }
  }

  async function handleAudio(audio: Blob, candidate: FrozenBinding) {
    if (!contextCurrent(candidate) || binding !== candidate) return
    // One generation accepts only its first finalized blob.
    mic.stop()
    binding = null
    state.value = 'transcribing'
    message.value = 'Transcribing in browser memory; raw audio will not be stored.'
    const controller = new AbortController()
    transcriptionController = controller
    try {
      const final = await transcribe(audio, language(options.locale.value), controller.signal)
      if (!contextCurrent(candidate) || transcriptionController !== controller) return
      const request: Paimos6SessionUtteranceRequest = {
        projectId: candidate.projectId,
        utteranceId: final.utteranceId,
        text: final.text,
        selectedSession: candidate.selectedSession,
      }
      retryable = { binding: candidate, request }
      await deliver(candidate, request)
    } catch {
      if (!controller.signal.aborted && contextCurrent(candidate)) {
        retryable = null
        state.value = 'retryable'
        message.value = 'Transcription failed before delivery. Record a new utterance to try again.'
      }
    } finally {
      if (transcriptionController === controller) transcriptionController = null
    }
  }

  async function start(): Promise<boolean> {
    if (!alive || ['permission', 'listening', 'transcribing', 'sending'].includes(state.value)) return false
    const selected = options.selection.value
    if (selected && !selected.available) {
      invalidate('idle', 'Ready for one ephemeral utterance to Paimos.')
      state.value = 'unavailable'
      message.value = 'The selected session has no available delivery target. Nothing will be rerouted.'
      return false
    }
    generation += 1
    releaseOperations()
    retryable = null
    deliveredDestination.value = null
    const candidate = freezeBinding()
    if (!candidate) {
      state.value = 'unavailable'
      message.value = 'Voice delivery is unavailable until an authorized project is ready.'
      return false
    }
    binding = candidate
    state.value = 'permission'
    message.value = 'Checking microphone permission…'
    try {
      await permission.init()
    } catch {
      if (contextCurrent(candidate)) {
        binding = null
        state.value = 'permission'
        message.value = 'Microphone permission could not be checked.'
      }
      return false
    }
    if (!contextCurrent(candidate) || binding !== candidate) return false
    if (permission.permission.value === 'denied') {
      binding = null
      state.value = 'permission'
      message.value = 'Microphone permission is blocked in this browser. Change the site permission to continue.'
      return false
    }
    const started = await mic.start((audio) => handleAudio(audio, candidate))
    if (!contextCurrent(candidate) || binding !== candidate) {
      mic.stop()
      return false
    }
    if (!started) {
      binding = null
      state.value = 'permission'
      message.value = 'Microphone capture is unavailable or permission was denied.'
      return false
    }
    state.value = 'listening'
    message.value = `Listening for one utterance to ${candidate.destination}.`
    return true
  }

  function finish(): boolean {
    if (!binding || !['permission', 'listening'].includes(state.value)) return false
    const finalized = mic.finish?.() ?? false
    if (finalized) {
      state.value = 'transcribing'
      message.value = 'Finalizing the captured utterance…'
      return true
    }
    invalidate('idle', 'No finalized speech was captured. Nothing was sent.')
    return false
  }

  function cancel() {
    invalidate('idle', 'Capture cancelled. No audio or transcript was stored or sent.')
  }

  async function retry(): Promise<boolean> {
    const pending = retryable
    if (!pending || state.value !== 'retryable' || !contextCurrent(pending.binding)) {
      if (pending) invalidate('stale', 'The selection or authority changed. The prior utterance cannot be retried.')
      return false
    }
    await deliver(pending.binding, pending.request)
    return deliveredDestination.value !== null
  }

  function dispose() {
    alive = false
    generation += 1
    releaseOperations()
    binding = null
    retryable = null
    deliveredDestination.value = null
  }

  const captureActive = computed(() => state.value === 'permission' || state.value === 'listening')
  const busy = computed(() => state.value === 'transcribing' || state.value === 'sending')
  const canRetry = computed(() => state.value === 'retryable' && retryable !== null)
  const supported = computed(() => mic.micSupported())

  watch(contextSignature, (next, previous) => {
    if (next === previous) return
    const hadPendingGeneration = binding !== null || retryable !== null
      || ['permission', 'listening', 'transcribing', 'sending'].includes(state.value)
    invalidate(
      hadPendingGeneration ? 'stale' : 'idle',
      hadPendingGeneration
        ? 'The selection or authority changed. The prior utterance was cancelled and not rerouted.'
        : 'Ready for one ephemeral utterance to Paimos.',
    )
  }, { flush: 'sync' })

  watch(permission.permission, (next) => {
    if (next !== 'denied' || !binding) return
    invalidate('stale', 'Microphone permission changed. The active utterance was cancelled.')
  }, { flush: 'sync' })

  watch(mic.state, (next) => {
    if (next === 'idle' && binding && state.value === 'transcribing' && !transcriptionController) {
      binding = null
      retryable = null
      state.value = 'idle'
      message.value = 'No finalized speech was captured. Nothing was sent.'
      return
    }
    if (next !== 'error' || !binding) return
    invalidate('idle', 'Microphone capture failed. No audio or transcript was stored or sent.')
    state.value = 'permission'
  }, { flush: 'sync' })

  if (getCurrentInstance()) onBeforeUnmount(dispose)

  return {
    state,
    message,
    deliveredDestination,
    captureActive,
    busy,
    canRetry,
    supported,
    start,
    finish,
    cancel,
    retry,
    dispose,
  }
}
