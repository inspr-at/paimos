/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

// PAI-808 — side-effect owner for the pure Agent Mode voice reducer.
// Raw transcript/note text never leaves these in-memory refs except for the
// one STT request and one explicitly confirmed internal-comment request.

import {
  computed,
  getCurrentInstance,
  onBeforeUnmount,
  onMounted,
  ref,
  shallowRef,
  watch,
  type InjectionKey,
  type Ref,
} from 'vue'

import {
  buildClarificationSpeech,
  buildNoteReadySpeech,
  buildStatusSpeech,
  type NarrationLocale,
  type NarrationSpeechRequest,
} from '@/components/agent-mode/agentModeNarration'
import {
  voiceReducer,
  initialVoiceState,
  type VoiceEffect,
  type VoiceEvent,
  type VoiceMachineState,
  type VoiceNoticeCode,
  type VoiceNoteBinding,
} from '@/composables/agent-mode/agentModeVoiceMachine'
import type {
  DetailLevelIntent,
  UnsupportedVoiceControl,
  VoiceControlActivation,
  VoiceFilterPatch,
  VoiceProjectRef,
} from '@/composables/agent-mode/agentModeVoiceIntent'
import { stepSelection as resolveStepSelection } from '@/composables/agent-mode/agentModeSelection'
import { createIntakeTtsPlayback } from '@/composables/useIntakeTtsPlayback'
import { useMicPermission } from '@/composables/useMicPermission'
import { useMicTranscript } from '@/composables/useMicTranscript'
import {
  comparePermissionsEpoch,
  permissionsEpoch,
  permissionsEpochGeneration,
  sessionExpired,
} from '@/api/client'
import type { Delivery } from '@/services/agentMode'
import type { ControlCommand, ControlTarget } from '@/services/agentModeControls'
import { parseAgentModeSearchFilter } from '@/services/agentModeTransport'
import {
  loadAgentModeVoiceProjectCatalog,
  postAgentModeInternalNote,
  speakAgentModeTemplate,
  transcribeAgentModeAudio,
  type AgentModeVoiceLanguage,
} from '@/services/agentModeVoice'

export type AgentModeVoiceActionNotice =
  | 'selection_updated'
  | 'selection_blocked'
  | 'filters_updated'
  | 'detail_updated'
  | 'status_ready'
  | 'portfolio_status'
  | 'clarification_ready'
  | 'note_ready'
  | 'note_saved'
  | 'note_cancelled'
  | 'note_discarded'
  | 'command_unsupported'
  | 'control_requested'
  | 'control_confirmed'
  | 'control_blocked'

export type AgentModeVoiceNotice = VoiceNoticeCode | AgentModeVoiceActionNotice
export type AgentModeVoiceError = 'microphone' | 'transcription' | 'speech' | 'note'

export interface AgentModeVoiceCandidateView {
  index: number
  deliveryId: string
  issueKey: string
  title: string
  lane: string
}

export interface AgentModeVoiceActions {
  selectDelivery: (deliveryId: string) => boolean | Promise<boolean>
  setFilters: (patch: VoiceFilterPatch) => boolean | Promise<boolean>
  clearFilters: () => boolean | Promise<boolean>
  setDetail: (level: DetailLevelIntent) => boolean | Promise<boolean>
  showDetails: (deliveryId: string) => boolean | Promise<boolean>
  /** First utterance creates only the visible two-phase challenge. */
  requestControl?: (activation: VoiceControlActivation) => boolean | Promise<boolean>
  /** Second utterance must still match this exact persisted id + revision. */
  confirmControl?: (commandId: string, statusRevision: number) => boolean | Promise<boolean>
  notePosted?: () => void | Promise<void>
  /** Force one canonical Agent Mode snapshot after an authority epoch change.
   * The separately observed authorityVersion is the success proof. */
  authorityChanged: () => void | Promise<void>
}

type MicAdapter = ReturnType<typeof useMicTranscript>
type PermissionAdapter = ReturnType<typeof useMicPermission>
type PlaybackFactory = typeof createIntakeTtsPlayback

export interface AgentModeVoiceDependencies {
  mic?: MicAdapter
  permission?: PermissionAdapter
  createPlayback?: PlaybackFactory
  transcribe?: typeof transcribeAgentModeAudio
  speak?: typeof speakAgentModeTemplate
  postNote?: typeof postAgentModeInternalNote
  loadProjectCatalog?: typeof loadAgentModeVoiceProjectCatalog
  sessionNonce?: string
}

/** Narrow deterministic seam for mounted View tests. Production provides
 * nothing and therefore uses the real microphone and authenticated APIs. */
export const AGENT_MODE_VOICE_DEPENDENCIES_KEY: InjectionKey<AgentModeVoiceDependencies> = Symbol('agent-mode-voice-dependencies')

export interface UseAgentModeVoiceOptions {
  deliveries: Readonly<Ref<readonly Delivery[]>>
  /** Exact order used by the parent selection step action. */
  travelOrder: Readonly<Ref<readonly string[]>>
  selectedId: Readonly<Ref<string | null>>
  online: Readonly<Ref<boolean>>
  degraded: Readonly<Ref<boolean>>
  locale: Readonly<Ref<string>>
  /** False while an ACL/reset replacement snapshot is not yet authorized. */
  authorityAvailable: Readonly<Ref<boolean>>
  /** Incremented only when the parent commits a current canonical snapshot. */
  authorityVersion: Readonly<Ref<number>>
  /** Exact response epoch of that committed canonical snapshot. */
  authorityEpoch: Readonly<Ref<string | null>>
  controlTargets?: Readonly<Ref<readonly ControlTarget[]>>
  controlChallenge?: Readonly<Ref<AgentModeVoiceControlChallenge | null>>
  enabled?: Readonly<Ref<boolean>>
  actions: AgentModeVoiceActions
  dependencies?: AgentModeVoiceDependencies
}

export interface AgentModeVoiceControlChallenge {
  command: ControlCommand
  issueKey: string
  phrase: string
}

function randomSessionNonce(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID().replace(/-/g, '')
  }
  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') {
    const words = new Uint32Array(4)
    crypto.getRandomValues(words)
    return [...words].map((word) => word.toString(16).padStart(8, '0')).join('')
  }
  return `${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`
}

function asLocale(value: string): NarrationLocale {
  return value.toLowerCase().startsWith('de') ? 'de' : 'en'
}

function laneLabel(delivery: Delivery | undefined): string {
  if (!delivery) return ''
  const lane = delivery.lane.epicTitle || delivery.lane.epicKey
  return lane ? `${delivery.lane.projectKey} / ${lane}` : delivery.lane.projectKey
}

function noteTargetLabel(delivery: Delivery | undefined): string {
  if (!delivery) return ''
  const project = delivery.lane.projectName
    ? `${delivery.lane.projectKey} · ${delivery.lane.projectName}`
    : delivery.lane.projectKey
  const lane = delivery.lane.epicTitle || delivery.lane.epicKey
  return lane ? `${project} / ${lane}` : project
}

/**
 * Exact in-memory signature for everything the intent resolver may use.
 * Object identity and unrelated presentation/freshness fields deliberately
 * do not participate, so an identical poll cannot interrupt the microphone.
 */
function resolverContextSignature(
  deliveries: readonly Delivery[],
  projectCatalog: readonly VoiceProjectRef[],
  selectedId: string | null,
  travelOrder: readonly string[],
  controlTargets: readonly ControlTarget[],
  controlChallenge: AgentModeVoiceControlChallenge | null,
): string {
  return JSON.stringify([
    selectedId,
    travelOrder,
    deliveries.map((delivery) => [
      delivery.id,
      delivery.deliveryRevision,
      delivery.issueId,
      delivery.issueKey,
      delivery.title,
      delivery.actor?.name ?? null,
      delivery.actor?.label ?? null,
      delivery.lane.projectKey,
      delivery.lane.projectName,
      delivery.lane.epicKey,
      delivery.lane.epicTitle,
      delivery.attempt.id,
      delivery.capabilities.comment,
    ]),
    projectCatalog.map((project) => [
      project.projectId,
      project.projectKey,
      project.projectName,
    ]),
    controlTargets,
    controlChallenge == null ? null : [
      controlChallenge.command.commandId,
      controlChallenge.command.statusRevision,
      controlChallenge.command.status,
      controlChallenge.command.challengeTemplate,
      controlChallenge.command.display,
      controlChallenge.issueKey,
      controlChallenge.phrase,
    ],
  ])
}

function typedTargetSignature(delivery: Delivery | null): string {
  return JSON.stringify(delivery
    ? [delivery.id, delivery.issueId, delivery.attempt.id, delivery.capabilities.comment]
    : null)
}

export function useAgentModeVoice(options: UseAgentModeVoiceOptions) {
  const dependencies = options.dependencies ?? {}
  const mic = dependencies.mic ?? useMicTranscript()
  const permission = dependencies.permission ?? useMicPermission()
  const createPlayback = dependencies.createPlayback ?? createIntakeTtsPlayback
  const transcribe = dependencies.transcribe ?? transcribeAgentModeAudio
  const speak = dependencies.speak ?? speakAgentModeTemplate
  const postNote = dependencies.postNote ?? postAgentModeInternalNote
  const loadProjectCatalog = dependencies.loadProjectCatalog ?? loadAgentModeVoiceProjectCatalog
  const sessionNonce = dependencies.sessionNonce ?? randomSessionNonce()

  const projectCatalog = shallowRef<readonly VoiceProjectRef[]>([])
  const machine = shallowRef<VoiceMachineState>(initialVoiceState())
  const notice = ref<AgentModeVoiceNotice | null>(null)
  const unsupportedControl = ref<UnsupportedVoiceControl | null>(null)
  const error = ref<AgentModeVoiceError | null>(null)
  const wantsListening = ref(false)
  const voiceRepliesEnabled = ref(false)
  const replyLoading = ref(false)
  const speechActive = ref(false)
  const micStartPending = ref(false)
  const commandBusy = ref(false)
  const noteFocusToken = ref(0)
  const inputResetToken = ref(0)

  let alive = true
  let operationGeneration = 0
  let selectionVersion = 0
  let utteranceContextGeneration = 0
  let lastResolverContextSignature = ''
  let lastTypedTargetSignature = ''
  let previousSelectedId = options.selectedId.value
  let typedSequence = 0
  let finalSequence = 0
  let micStartAttempt = 0
  let effectChain: Promise<void> = Promise.resolve()
  let nextEffectBatchId = 0
  const pendingEffectBatches = new Map<number, readonly VoiceEffect[]>()
  let transcriptController: AbortController | null = null
  let speechController: AbortController | null = null
  let noteController: AbortController | null = null
  let catalogController: AbortController | null = null
  let catalogGeneration = 0
  let speechBinding: {
    template: NarrationSpeechRequest['template']
    deliveryId: string
    deliveryRevision: string
    candidateIds: readonly string[]
    noteClientRequestId: string | null
  } | null = null

  const isEnabled = () => options.enabled?.value !== false
  const authorityFenced = ref(!options.authorityAvailable.value || sessionExpired.value || !isEnabled())
  let fencedAtAuthorityVersion = options.authorityVersion.value
  let fencedAtPermissionsEpoch = permissionsEpoch.value
  const canOperate = () => alive
    && isEnabled()
    && options.authorityAvailable.value
    && !sessionExpired.value
    && !authorityFenced.value

  function selectionEpoch(): string {
    return `amv:${sessionNonce}:${selectionVersion}:${options.selectedId.value ?? 'none'}`
  }

  function currentDelivery(deliveryId = options.selectedId.value): Delivery | null {
    if (!deliveryId) return null
    return options.deliveries.value.find((delivery) => delivery.id === deliveryId) ?? null
  }

  function currentContextEvent(): VoiceEvent {
    return {
      type: 'context',
      deliveries: canOperate() ? options.deliveries.value : [],
      projectCatalog: canOperate() ? projectCatalog.value : [],
      controlTargets: canOperate() ? options.controlTargets?.value ?? [] : [],
      controlChallenge: canOperate() ? options.controlChallenge?.value ?? null : null,
      selectedId: canOperate() ? options.selectedId.value : null,
      selectionEpoch: selectionEpoch(),
    }
  }

  function currentResolverContextSignature(): string {
    return canOperate()
      ? resolverContextSignature(
          options.deliveries.value,
          projectCatalog.value,
          options.selectedId.value,
          options.travelOrder.value,
          options.controlTargets?.value ?? [],
          options.controlChallenge?.value ?? null,
        )
      : resolverContextSignature([], [], null, [], [], null)
  }

  function reduceNow(event: VoiceEvent): VoiceEffect[] {
    const result = voiceReducer(machine.value, event)
    machine.value = result.state
    invalidateSpeechIfStale()
    return result.effects
  }

  function enqueueEffects(effects: readonly VoiceEffect[]): Promise<void> {
    if (effects.length === 0) return Promise.resolve()
    const batchId = ++nextEffectBatchId
    pendingEffectBatches.set(batchId, effects)
    const generation = operationGeneration
    const effectSelectionEpoch = machine.value.selectionEpoch
    const effectUtteranceContextGeneration = utteranceContextGeneration
    const task = effectChain.then(async () => {
      const batch = pendingEffectBatches.get(batchId)
      pendingEffectBatches.delete(batchId)
      if (!batch) return
      if (!alive || generation !== operationGeneration) return
      commandBusy.value = true
      try {
        for (const effect of batch) {
          if (!alive || generation !== operationGeneration) return
          await runEffect(
            effect,
            generation,
            effectSelectionEpoch,
            effectUtteranceContextGeneration,
          )
        }
      } finally {
        if (generation === operationGeneration) commandBusy.value = false
      }
    })
    effectChain = task.catch(() => {})
    return task
  }

  function dispatch(event: VoiceEvent): Promise<void> {
    return enqueueEffects(reduceNow(event))
  }

  function cancelSpeech(resumeMic: boolean) {
    speechController?.abort()
    speechController = null
    speechBinding = null
    replyLoading.value = false
    playback.cancel(resumeMic)
  }

  async function startMicCapture(): Promise<boolean> {
    if (
      !canOperate()
      || !options.online.value
      || options.degraded.value
      || speechActive.value
      || !wantsListening.value
    ) return false
    const generation = operationGeneration
    const captureUtteranceContextGeneration = utteranceContextGeneration
    const captureMicAttempt = micStartAttempt
    const started = await mic.start((audio) => handleUtterance(audio, captureUtteranceContextGeneration))
    // A resolver-context change already stopped this recorder generation and
    // may have started its replacement. Never let the stale await stop it.
    if (
      captureUtteranceContextGeneration !== utteranceContextGeneration
      || captureMicAttempt !== micStartAttempt
    ) return false
    if (
      !alive
      || generation !== operationGeneration
      || !wantsListening.value
      || !canOperate()
      || !options.online.value
      || options.degraded.value
      || speechActive.value
    ) {
      mic.stop()
      return false
    }
    if (!started) error.value = 'microphone'
    return started
  }

  const micInterlockActive = computed(() => mic.isActive.value || micStartPending.value)
  const playback = createPlayback({
    micActive: micInterlockActive,
    stopMic: mic.stop,
    canResumeMic: () => canOperate()
      && options.online.value
      && !options.degraded.value
      && wantsListening.value,
    resumeMic: () => { void startMicCapture() },
    onActiveChange: (active) => { speechActive.value = active },
  })

  async function requestSpeech(request: NarrationSpeechRequest | null) {
    if (
      !request
      || !voiceRepliesEnabled.value
      || !options.online.value
      || options.degraded.value
      || !canOperate()
    ) return
    const current = currentDelivery()
    if (
      !current
      || current.id !== request.deliveryId
      || current.deliveryRevision !== request.deliveryRevision
    ) return

    const authorized = new Set(options.deliveries.value.map((delivery) => delivery.id))
    if (request.candidateIds.some((candidateId) => !authorized.has(candidateId))) return
    if (request.template === 'clarification') {
      const currentCandidates = machine.value.candidates.map((candidate) => candidate.deliveryId)
      if (
        currentCandidates.length !== request.candidateIds.length
        || request.candidateIds.some((candidateId, index) => currentCandidates[index] !== candidateId)
      ) return
    }
    const noteBinding = request.template === 'note_ready' ? machine.value.note?.binding ?? null : null
    if (request.template === 'note_ready' && (!noteBinding || !bindingIsCurrent(noteBinding))) return

    speechController?.abort()
    const controller = new AbortController()
    speechController = controller
    speechBinding = {
      template: request.template,
      deliveryId: request.deliveryId,
      deliveryRevision: request.deliveryRevision,
      candidateIds: [...request.candidateIds],
      noteClientRequestId: noteBinding?.clientRequestId ?? null,
    }
    replyLoading.value = true
    const generation = operationGeneration
    const played = await playback.play(() => speak(request, controller.signal))
    if (speechController !== controller) return
    speechController = null
    replyLoading.value = false
    if (!played && !controller.signal.aborted && alive && generation === operationGeneration) error.value = 'speech'
  }

  function setNotice(code: AgentModeVoiceNotice) {
    notice.value = code
    unsupportedControl.value = null
  }

  async function executeCommand(effect: Extract<VoiceEffect, { type: 'execute' }>, generation: number) {
    const command = effect.command
    switch (command.type) {
      case 'select': {
        const changed = await options.actions.selectDelivery(command.deliveryId)
        if (!alive || generation !== operationGeneration) return
        setNotice(changed ? 'selection_updated' : 'selection_blocked')
        break
      }
      case 'step': {
        const authorized = new Set(options.deliveries.value.map((delivery) => delivery.id))
        const exactOrder = options.travelOrder.value.filter((id) => authorized.has(id))
        const target = resolveStepSelection(
          exactOrder,
          options.selectedId.value,
          command.direction === 'next' ? 1 : -1,
        )
        const changed = !!target
          && target !== options.selectedId.value
          && await options.actions.selectDelivery(target)
        if (!alive || generation !== operationGeneration) return
        setNotice(changed ? 'selection_updated' : 'selection_blocked')
        break
      }
      case 'set_filters': {
        if (!options.online.value || options.degraded.value) {
          setNotice('selection_blocked')
          break
        }
        let patch = command.patch
        if (patch.query !== undefined) {
          const query = parseAgentModeSearchFilter(patch.query)
          if (query == null) {
            setNotice('unknown_command')
            break
          }
          patch = { ...patch, query }
        }
        const changed = await options.actions.setFilters(patch)
        if (!alive || generation !== operationGeneration) return
        setNotice(changed ? 'filters_updated' : 'selection_blocked')
        break
      }
      case 'clear_filters': {
        if (!options.online.value || options.degraded.value) {
          setNotice('selection_blocked')
          break
        }
        const changed = await options.actions.clearFilters()
        if (!alive || generation !== operationGeneration) return
        setNotice(changed ? 'filters_updated' : 'selection_blocked')
        break
      }
      case 'set_detail': {
        const changed = await options.actions.setDetail(command.level)
        if (!alive || generation !== operationGeneration) return
        setNotice(changed ? 'detail_updated' : 'selection_blocked')
        break
      }
      case 'show_details': {
        const changed = await options.actions.showDetails(command.deliveryId)
        if (!alive || generation !== operationGeneration) return
        setNotice(changed ? 'detail_updated' : 'selection_blocked')
        break
      }
      case 'read_status': {
        setNotice(command.scope === 'portfolio' ? 'portfolio_status' : 'status_ready')
        if (command.scope === 'selection') {
          const delivery = currentDelivery(command.deliveryId)
          await requestSpeech(delivery ? buildStatusSpeech(delivery, asLocale(options.locale.value)) : null)
        }
        break
      }
      case 'draft_note':
      case 'confirm_note':
      case 'cancel_note':
        // These are consumed by the pure machine before an execute effect.
        break
      case 'request_control': {
        const created = await options.actions.requestControl?.(command.activation) ?? false
        if (!alive || generation !== operationGeneration) return
        setNotice(created ? 'control_requested' : 'control_blocked')
        break
      }
      case 'confirm_control': {
        const confirmed = await options.actions.confirmControl?.(
          command.commandId,
          command.statusRevision,
        ) ?? false
        if (!alive || generation !== operationGeneration) return
        setNotice(confirmed ? 'control_confirmed' : 'control_blocked')
        break
      }
    }
  }

  function bindingIsCurrent(binding: VoiceNoteBinding): boolean {
    const delivery = currentDelivery(binding.deliveryId)
    return !!delivery
      && machine.value.online
      && machine.value.selectedId === binding.deliveryId
      && machine.value.selectionEpoch === binding.selectionEpoch
      && delivery.issueId === binding.issueId
      && delivery.attempt.id === binding.attemptId
      && delivery.capabilities.comment === true
  }

  async function submitNote(binding: VoiceNoteBinding, generation: number) {
    if (!bindingIsCurrent(binding) || !canOperate()) {
      for (const effect of reduceNow(currentContextEvent())) await runEffect(effect, generation)
      return
    }
    noteController = new AbortController()
    const controller = noteController
    try {
      await postNote({
        issueId: binding.issueId,
        body: binding.body,
        clientRequestId: binding.clientRequestId,
      }, controller.signal)
      if (!alive || generation !== operationGeneration || noteController !== controller) return
      for (const effect of reduceNow({
        type: 'note_settled',
        clientRequestId: binding.clientRequestId,
        ok: true,
      })) await runEffect(effect, generation)
      await options.actions.notePosted?.()
    } catch {
      if (!alive || generation !== operationGeneration || controller.signal.aborted) return
      error.value = 'note'
      for (const effect of reduceNow({
        type: 'note_settled',
        clientRequestId: binding.clientRequestId,
        ok: false,
      })) await runEffect(effect, generation)
    } finally {
      if (noteController === controller) noteController = null
    }
  }

  async function runEffect(
    effect: VoiceEffect,
    generation: number,
    effectSelectionEpoch = machine.value.selectionEpoch,
    effectUtteranceContextGeneration = utteranceContextGeneration,
  ) {
    const contextDependent = effect.type === 'execute'
      || effect.type === 'clarify'
      || effect.type === 'note_preview'
    if (
      contextDependent
      && (
        machine.value.selectionEpoch !== effectSelectionEpoch
        || utteranceContextGeneration !== effectUtteranceContextGeneration
      )
    ) return
    switch (effect.type) {
      case 'execute':
        await executeCommand(effect, generation)
        break
      case 'clarify': {
        setNotice('clarification_ready')
        const delivery = currentDelivery()
        await requestSpeech(delivery
          ? buildClarificationSpeech(delivery, effect.candidates, asLocale(options.locale.value))
          : null)
        break
      }
      case 'note_preview': {
        const note = machine.value.note
        if (!note || note.binding.clientRequestId !== effect.clientRequestId) return
        setNotice(note.status === 'held_offline' ? 'offline_hold' : 'note_ready')
        noteFocusToken.value += 1
        const delivery = currentDelivery(note.binding.deliveryId)
        if (note.status !== 'held_offline') {
          await requestSpeech(delivery ? buildNoteReadySpeech(delivery, asLocale(options.locale.value)) : null)
        }
        break
      }
      case 'submit_note': {
        const note = machine.value.note
        if (
          !note
          || note.status !== 'submitting'
          || note.binding.clientRequestId !== effect.clientRequestId
        ) return
        await submitNote(note.binding, generation)
        break
      }
      case 'note_discarded':
        if (effect.reason === 'submitted') setNotice('note_saved')
        else if (effect.reason === 'cancelled') setNotice('note_cancelled')
        else if (effect.reason !== 'replaced' && effect.reason !== 'reset') setNotice('note_discarded')
        break
      case 'notice':
        setNotice(effect.code)
        break
      case 'unsupported':
        notice.value = 'command_unsupported'
        unsupportedControl.value = effect.control
        break
    }
  }

  async function handleUtterance(
    audio: Blob,
    captureUtteranceContextGeneration = utteranceContextGeneration,
  ): Promise<void> {
    if (
      !canOperate()
      || !options.online.value
      || options.degraded.value
      || !wantsListening.value
      || captureUtteranceContextGeneration !== utteranceContextGeneration
    ) return
    transcriptController?.abort()
    const controller = new AbortController()
    transcriptController = controller
    const generation = operationGeneration
    const utteranceSelectionEpoch = selectionEpoch()
    const requestUtteranceContextGeneration = utteranceContextGeneration
    let finalEvent: Extract<VoiceEvent, { type: 'final' }> | null = null
    try {
      const result = await transcribe(audio, asLocale(options.locale.value) as AgentModeVoiceLanguage, controller.signal)
      if (
        !alive
        || generation !== operationGeneration
        || transcriptController !== controller
        || !wantsListening.value
        || selectionEpoch() !== utteranceSelectionEpoch
        || requestUtteranceContextGeneration !== utteranceContextGeneration
      ) return
      finalSequence += 1
      finalEvent = {
        type: 'final',
        utteranceId: result.utteranceId,
        text: result.text,
        sequence: finalSequence,
      }
    } catch {
      if (alive && generation === operationGeneration && !controller.signal.aborted) error.value = 'transcription'
    } finally {
      if (transcriptController === controller) transcriptController = null
    }
    // Reduce synchronously, then release this frame (and its raw Blob/result)
    // without waiting behind an unrelated editor/action promise. Downstream
    // action failures are not transcription failures.
    if (finalEvent) void dispatch(finalEvent).catch(() => {})
  }

  async function startListening(): Promise<boolean> {
    if (!canOperate() || !options.online.value || options.degraded.value) return false
    const generation = operationGeneration
    const attempt = ++micStartAttempt
    error.value = null
    cancelSpeech(false)
    wantsListening.value = true
    micStartPending.value = true
    try {
      await permission.init()
    } catch {
      if (attempt === micStartAttempt) {
        micStartPending.value = false
        wantsListening.value = false
        error.value = 'microphone'
      }
      return false
    }
    if (attempt === micStartAttempt) micStartPending.value = false
    if (
      !alive
      || generation !== operationGeneration
      || attempt !== micStartAttempt
      || !wantsListening.value
      || !canOperate()
    ) return false
    if (permission.permission.value === 'denied') {
      wantsListening.value = false
      error.value = 'microphone'
      return false
    }
    if (!options.online.value || options.degraded.value || speechActive.value) return false
    return startMicCapture()
  }

  function stopListening() {
    micStartAttempt += 1
    micStartPending.value = false
    wantsListening.value = false
    transcriptController?.abort()
    transcriptController = null
    mic.stop()
  }

  async function toggleListening(): Promise<boolean> {
    if (wantsListening.value || mic.isActive.value) {
      stopListening()
      return false
    }
    return startListening()
  }

  function setVoiceReplies(enabled: boolean) {
    voiceRepliesEnabled.value = enabled
    if (!enabled) cancelSpeech(true)
  }

  function nextTypedUtteranceId(): string {
    typedSequence += 1
    return `typed_${sessionNonce}_${typedSequence}`
  }

  function submitTyped(text: string): Promise<void> {
    if (!canOperate() || text.trim() === '') return Promise.resolve()
    error.value = null
    return dispatch({ type: 'typed', utteranceId: nextTypedUtteranceId(), text })
  }

  function chooseCandidate(index: number): Promise<void> {
    return submitTyped(String(index))
  }

  function confirmNote(): Promise<void> {
    return submitTyped('confirm note')
  }

  function cancelNote(): Promise<void> {
    return submitTyped('cancel note')
  }

  function acceptPartial(utteranceId: string, text: string): Promise<void> {
    if (!canOperate()) return Promise.resolve()
    return dispatch({ type: 'partial', utteranceId, text })
  }

  function invalidateSpeechIfStale() {
    if (!speechBinding) return
    const delivery = currentDelivery(speechBinding.deliveryId)
    const authorized = new Set(options.deliveries.value.map((candidate) => candidate.id))
    const currentCandidateIds = machine.value.candidates.map((candidate) => candidate.deliveryId)
    const note = machine.value.note
    if (
      options.selectedId.value !== speechBinding.deliveryId
      || delivery?.deliveryRevision !== speechBinding.deliveryRevision
      || (speechBinding.template === 'clarification'
        && (
          speechBinding.candidateIds.some((candidateId) => !authorized.has(candidateId))
          || currentCandidateIds.length !== speechBinding.candidateIds.length
          || speechBinding.candidateIds.some((candidateId, index) => currentCandidateIds[index] !== candidateId)
        ))
      || (speechBinding.template === 'note_ready' && (
        speechBinding.noteClientRequestId == null
        || note?.binding.clientRequestId !== speechBinding.noteClientRequestId
        || !note
        || (note.status !== 'preview' && note.status !== 'failed')
        || !bindingIsCurrent(note.binding)
      ))
    ) cancelSpeech(true)
  }

  function syncContext() {
    if (!alive) return
    const targetSignature = typedTargetSignature(canOperate() ? currentDelivery() : null)
    if (targetSignature !== lastTypedTargetSignature) {
      lastTypedTargetSignature = targetSignature
      inputResetToken.value += 1
    }
    const signature = currentResolverContextSignature()
    const resolverChanged = signature !== lastResolverContextSignature
    if (resolverChanged) {
      lastResolverContextSignature = signature
      utteranceContextGeneration += 1
      transcriptController?.abort()
      transcriptController = null
      const resumeListening = wantsListening.value
      if (mic.isActive.value) mic.stop()
      reduceNow({ type: 'resolver_context_changed' })
      if (
        resumeListening
        && !mic.isActive.value
        && !micStartPending.value
        && !speechActive.value
        && canOperate()
        && options.online.value
        && !options.degraded.value
      ) void startMicCapture()
    }
    invalidateSpeechIfStale()
    void enqueueEffects(reduceNow(currentContextEvent()))
  }

  function clearProjectCatalog(sync = true) {
    catalogGeneration += 1
    catalogController?.abort()
    catalogController = null
    projectCatalog.value = []
    if (sync) syncContext()
  }

  async function reloadProjectCatalog() {
    clearProjectCatalog()
    if (!canOperate() || !options.online.value || options.degraded.value) return
    const controller = new AbortController()
    catalogController = controller
    const generation = ++catalogGeneration
    const authorityGeneration = operationGeneration
    const epoch = permissionsEpoch.value
    try {
      const projects = await loadProjectCatalog(controller.signal)
      if (
        !alive
        || controller.signal.aborted
        || catalogController !== controller
        || generation !== catalogGeneration
        || authorityGeneration !== operationGeneration
        || (epoch != null && permissionsEpoch.value !== epoch)
        || !canOperate()
      ) return
      projectCatalog.value = projects
      syncContext()
    } catch {
      // Fail closed. A project command reports no_project_catalog through the
      // pure reducer; this transport failure never exposes hidden details.
    } finally {
      if (catalogController === controller) catalogController = null
    }
  }

  function resetEphemeralState() {
    operationGeneration += 1
    utteranceContextGeneration += 1
    lastResolverContextSignature = resolverContextSignature([], [], null, [], [], null)
    lastTypedTargetSignature = typedTargetSignature(null)
    effectChain = Promise.resolve()
    pendingEffectBatches.clear()
    micStartAttempt += 1
    micStartPending.value = false
    wantsListening.value = false
    transcriptController?.abort()
    transcriptController = null
    noteController?.abort()
    noteController = null
    cancelSpeech(false)
    mic.stop()
    machine.value = initialVoiceState({ online: options.online.value })
    notice.value = null
    unsupportedControl.value = null
    error.value = null
    commandBusy.value = false
    voiceRepliesEnabled.value = false
    noteFocusToken.value += 1
    inputResetToken.value += 1
  }

  function authorityReset() {
    resetEphemeralState()
    clearProjectCatalog(false)
  }

  function fenceAuthority(_request: boolean) {
    if (!alive) return
    authorityFenced.value = true
    authorityReset()
    fencedAtAuthorityVersion = options.authorityVersion.value
    fencedAtPermissionsEpoch = permissionsEpoch.value
  }

  function acceptFreshAuthorityIfProven() {
    if (!authorityFenced.value) return
    const authorityVersion = options.authorityVersion.value
    if (
      !Number.isSafeInteger(authorityVersion)
      || authorityVersion <= fencedAtAuthorityVersion
      || (fencedAtPermissionsEpoch != null && (
        permissionsEpoch.value !== fencedAtPermissionsEpoch
        || options.authorityEpoch.value == null
        || comparePermissionsEpoch(options.authorityEpoch.value, fencedAtPermissionsEpoch) < 0
      ))
      || !alive
      || !isEnabled()
      || sessionExpired.value
      || !options.authorityAvailable.value
    ) return
    authorityFenced.value = false
    syncContext()
    void reloadProjectCatalog()
  }

  async function initialize() {
    if (!canOperate()) return
    const generation = operationGeneration
    await permission.init()
    if (!alive || generation !== operationGeneration || !canOperate()) return
    await reloadProjectCatalog()
  }

  function dispose() {
    if (!alive) return
    alive = false
    resetEphemeralState()
    clearProjectCatalog(false)
  }

  machine.value = initialVoiceState({
    online: options.online.value,
    deliveries: canOperate() ? options.deliveries.value : [],
    projectCatalog: [],
    controlTargets: canOperate() ? options.controlTargets?.value ?? [] : [],
    controlChallenge: canOperate() ? options.controlChallenge?.value ?? null : null,
    selectedId: canOperate() ? options.selectedId.value : null,
    selectionEpoch: selectionEpoch(),
  })
  lastResolverContextSignature = currentResolverContextSignature()
  lastTypedTargetSignature = typedTargetSignature(canOperate() ? currentDelivery() : null)

  watch(options.selectedId, (selectedId) => {
    if (selectedId !== previousSelectedId) {
      previousSelectedId = selectedId
      selectionVersion += 1
      syncContext()
      return
    }
    syncContext()
  }, { flush: 'sync' })
  watch(options.deliveries, syncContext, { flush: 'sync' })
  watch(options.travelOrder, syncContext, { flush: 'sync' })
  if (options.controlTargets) watch(options.controlTargets, syncContext, { flush: 'sync' })
  if (options.controlChallenge) watch(options.controlChallenge, syncContext, { flush: 'sync' })
  watch(options.online, (online) => {
    if (!alive) return
    if (!online) {
      stopListening()
      cancelSpeech(true)
    }
    void enqueueEffects(reduceNow({ type: 'connectivity', online }))
    // Ordering is intentional: reconnect first transitions held_offline to
    // awaiting_revalidation, then this fresh context reauthorizes it. A new
    // explicit confirm is still required; reconnect never auto-submits.
    if (online) syncContext()
  }, { flush: 'sync' })
  watch(options.authorityAvailable, (available) => {
    if (!alive) return
    if (!available) fenceAuthority(false)
    else acceptFreshAuthorityIfProven()
  }, { flush: 'sync' })
  watch(options.authorityVersion, () => {
    if (!alive) return
    if (authorityFenced.value) acceptFreshAuthorityIfProven()
    else syncContext()
  }, { flush: 'sync' })
  watch(permissionsEpochGeneration, () => {
    if (!alive) return
    fenceAuthority(false)
    fencedAtPermissionsEpoch = null
  }, { flush: 'sync' })
  if (options.enabled) watch(options.enabled, (enabled) => {
    if (!alive) return
    fenceAuthority(enabled)
  }, { flush: 'sync' })
  watch(sessionExpired, () => {
    if (!alive) return
    // The parent deliveries composable owns the one forced snapshot on
    // session restoration. Voice remains fenced until that commit advances
    // authorityVersion; asking here would duplicate the same paid load.
    fenceAuthority(false)
  }, { flush: 'sync' })
  watch(permissionsEpoch, (epoch, previous) => {
    if (!alive) return
    // -1 → first observed epoch establishes a baseline; it is not a change to
    // an authority this voice session previously trusted.
    if (previous == null || epoch == null || comparePermissionsEpoch(epoch, previous) <= 0) return
    // The delivery owner tracks the response-local epoch and schedules exactly
    // one canonical replacement. Voice only fences/rebases and waits for that
    // committed version+epoch proof.
    if (authorityFenced.value) {
      if (
        fencedAtPermissionsEpoch == null
        || comparePermissionsEpoch(epoch, fencedAtPermissionsEpoch) > 0
      ) fencedAtPermissionsEpoch = epoch
      return
    }
    fenceAuthority(false)
  }, { flush: 'sync' })
  watch(options.degraded, (degraded) => {
    if (!alive) return
    if (!degraded) return
    stopListening()
    cancelSpeech(true)
  }, { flush: 'sync' })
  watch(permission.permission, (state) => {
    if (!alive || state !== 'denied') return
    stopListening()
    error.value = 'microphone'
  }, { flush: 'sync' })
  watch(mic.state, (state) => {
    if (!alive) return
    if (state !== 'error') return
    stopListening()
    error.value = 'microphone'
  }, { flush: 'sync' })

  if (getCurrentInstance()) {
    onMounted(() => { void initialize() })
    onBeforeUnmount(dispose)
  }

  const candidates = computed<AgentModeVoiceCandidateView[]>(() => machine.value.candidates.map((candidate) => ({
    ...candidate,
    lane: laneLabel(options.deliveries.value.find((delivery) => delivery.id === candidate.deliveryId)),
  })))
  const replyState = computed<'off' | 'loading' | 'ready'>(() => (
    !voiceRepliesEnabled.value ? 'off' : replyLoading.value ? 'loading' : 'ready'
  ))
  const operational = computed(() => canOperate())
  const audioAvailable = computed(() => operational.value
    && options.online.value
    && !options.degraded.value)

  return {
    machine,
    draft: computed(() => machine.value.draft),
    candidates,
    candidateMatchCount: computed(() => machine.value.candidateMatchCount),
    candidateTruncated: computed(() => machine.value.candidateTruncated),
    note: computed(() => machine.value.note),
    noteTarget: computed(() => noteTargetLabel(
      options.deliveries.value.find((delivery) => delivery.id === machine.value.note?.binding.deliveryId),
    )),
    notice,
    unsupportedControl,
    error,
    permission: permission.permission,
    micState: mic.state,
    micLevel: mic.level,
    micActive: mic.isActive,
    micStartPending,
    speechActive,
    operational,
    audioAvailable,
    micSupported: mic.micSupported,
    wantsListening,
    voiceRepliesEnabled,
    replyState,
    commandBusy,
    noteFocusToken,
    inputResetToken,
    projectCatalog,
    pendingEffectBatchCount: () => pendingEffectBatches.size,
    initialize,
    reloadProjectCatalog,
    startListening,
    stopListening,
    toggleListening,
    setVoiceReplies,
    submitTyped,
    chooseCandidate,
    confirmNote,
    cancelNote,
    acceptPartial,
    dispose,
  }
}
