/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

// PAI-809 — client lifecycle owner for the two-phase control protocol.
// Capability and target truth is server-owned. The browser persists only one
// opaque command id per effective user + canonical delivery in sessionStorage.

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
  permissionsEpoch,
  permissionsEpochGeneration,
  sessionExpired,
} from '@/api/client'
import { ssAgentModeControlCommandKey } from '@/constants/storage'
import type { Delivery } from '@/services/agentMode'
import {
  AgentModeControlTransportError,
  createControlCommand,
  getControlCommand,
  isControlCommandId,
  issueControlGrant,
  newControlIdempotencyKey,
  transitionControlCommand,
  type ControlCommand,
  type ControlCommandCreate,
  type ControlGrant,
  type ControlPriority,
  type ControlTarget,
} from '@/services/agentModeControls'

export type AgentModeControlState =
  | 'idle'
  | 'loading'
  | 'ready'
  | 'offline'
  | 'forbidden'
  | 'not-found'
  | 'conflict'
  | 'error'

export interface BoundControlCommand {
  command: ControlCommand
  deliveryKey: string
  issueKey: string
  userId: number
}

export type ControlActivation =
  | { target: Extract<ControlTarget, { action: 'issue.priority.set' }>; priority: ControlPriority }
  | { target: Extract<ControlTarget, { action: 'run.cancel.queued' | 'run.cancel.running' }> }
  | {
    target: Extract<ControlTarget, { action: 'input.respond'; inputKind: 'approval' }>
    response: 'approve' | 'reject'
  }
  | {
    target: Extract<ControlTarget, { action: 'input.respond'; inputKind: 'choice' }>
    response: 'choice'
    choiceOrdinal: number
  }
  | { target: Extract<ControlTarget, { action: 'run.pause' | 'run.resume' }> }

export interface AgentModeControlsDependencies {
  issueGrant?: typeof issueControlGrant
  getCommand?: typeof getControlCommand
  createCommand?: typeof createControlCommand
  transitionCommand?: typeof transitionControlCommand
  newIdempotencyKey?: typeof newControlIdempotencyKey
  storage?: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> | null
  pollBaseMs?: number
  pollMaxMs?: number
}

export const AGENT_MODE_CONTROLS_DEPENDENCIES_KEY: InjectionKey<AgentModeControlsDependencies> = Symbol('agent-mode-controls-dependencies')

export interface UseAgentModeControlsOptions {
  delivery: Readonly<Ref<Delivery | null>>
  userId: Readonly<Ref<number | null>>
  online: Readonly<Ref<boolean>>
  degraded: Readonly<Ref<boolean>>
  authorityAvailable: Readonly<Ref<boolean>>
  authorityVersion: Readonly<Ref<number>>
  enabled?: Readonly<Ref<boolean>>
  dependencies?: AgentModeControlsDependencies
}

const STORAGE_PROBE_ID = '00000000-0000-4000-8000-000000000000'

function browserSessionStorage(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.sessionStorage
  } catch {
    return null
  }
}

function targetSignature(target: ControlTarget): string {
  return JSON.stringify(target)
}

function requestFor(grant: ControlGrant, activation: ControlActivation): ControlCommandCreate | null {
  const common = { grantId: grant.grantId, grantRevision: grant.revision }
  switch (activation.target.action) {
    case 'issue.priority.set':
      return 'priority' in activation
        ? { ...common, action: activation.target.action, priority: activation.priority }
        : null
    case 'run.cancel.queued':
    case 'run.cancel.running':
      return { ...common, action: activation.target.action, runId: activation.target.runId }
    case 'run.pause':
    case 'run.resume':
      return {
        ...common,
        action: activation.target.action,
        runId: activation.target.runId,
        runtimeRevision: activation.target.runtimeRevision,
      }
    case 'input.respond': {
      if (!('response' in activation)) return null
      const base = {
        ...common,
        action: activation.target.action,
        inputRequestId: activation.target.inputRequestId,
        inputRequestRevision: activation.target.inputRequestRevision,
      } as const
      if (activation.response === 'choice') {
        const ordinal = activation.choiceOrdinal
        if (!Number.isSafeInteger(ordinal) || ordinal == null || ordinal < 1 || ordinal > activation.target.optionCodes.length) return null
        return { ...base, inputResponse: 'choice', choiceOrdinal: ordinal }
      }
      if (activation.target.inputKind !== 'approval') return null
      return { ...base, inputResponse: activation.response }
    }
  }
}

function terminal(command: ControlCommand): boolean {
  return command.status === 'applied' || command.status === 'rejected' || command.status === 'expired'
}

export function useAgentModeControls(options: UseAgentModeControlsOptions) {
  const dependencies = options.dependencies ?? {}
  const issueGrant = dependencies.issueGrant ?? issueControlGrant
  const getCommand = dependencies.getCommand ?? getControlCommand
  const createCommand = dependencies.createCommand ?? createControlCommand
  const transitionCommand = dependencies.transitionCommand ?? transitionControlCommand
  const makeKey = dependencies.newIdempotencyKey ?? newControlIdempotencyKey
  const storage = dependencies.storage === undefined
    ? browserSessionStorage()
    : dependencies.storage
  const pollBaseMs = dependencies.pollBaseMs ?? 1_000
  const pollMaxMs = dependencies.pollMaxMs ?? 8_000

  const state = ref<AgentModeControlState>('idle')
  const grant = shallowRef<ControlGrant | null>(null)
  const boundCommand = shallowRef<BoundControlCommand | null>(null)
  const busy = ref(false)
  const storageAvailable = ref(storage !== null)
  const focusToken = ref(0)
  const focusReturnToken = ref(0)

  let alive = true
  let generation = 0
  let controller: AbortController | null = null
  let pollTimer: ReturnType<typeof setTimeout> | null = null
  let grantExpiryTimer: ReturnType<typeof setTimeout> | null = null
  let pollAttempt = 0
  let grantMutation: { deliveryKey: string; key: string } | null = null
  let commandMutation: { signature: string; key: string } | null = null
  let transitionMutation: { operation: 'confirm' | 'withdraw'; commandId: string; revision: number; key: string } | null = null

  const enabled = () => options.enabled?.value !== false
  const contextUsable = () => alive
    && enabled()
    && options.online.value
    && !options.degraded.value
    && options.authorityAvailable.value
    && !sessionExpired.value
    && options.userId.value != null
    && options.delivery.value != null

  const targets = computed<readonly ControlTarget[]>(() => grant.value?.targets ?? [])
  const controlAvailable = computed(() => contextUsable()
    && storageAvailable.value
    && state.value === 'ready'
    && !busy.value)
  const transitionAvailable = computed(() => contextUsable()
    && storageAvailable.value
    && boundCommand.value?.command.status === 'pending_confirmation'
    && !busy.value)

  function storageKey(deliveryKey: string, userId = options.userId.value): string | null {
    return userId == null ? null : ssAgentModeControlCommandKey(userId, deliveryKey)
  }

  function removeStored(deliveryKey: string, userId = options.userId.value) {
    const key = storageKey(deliveryKey, userId)
    if (!storage || !key) return
    try { storage.removeItem(key) } catch { storageAvailable.value = false }
  }

  function readStored(deliveryKey: string): string | null {
    const key = storageKey(deliveryKey)
    if (!storage || !key) {
      storageAvailable.value = false
      return null
    }
    try {
      const value = storage.getItem(key)
      if (value == null) return null
      if (!isControlCommandId(value)) {
        storage.removeItem(key)
        storageAvailable.value = false
        return null
      }
      return value
    } catch {
      storageAvailable.value = false
      return null
    }
  }

  function proveStorageWritable(deliveryKey: string): boolean {
    const key = storageKey(deliveryKey)
    if (!storage || !key) return false
    try {
      const previous = storage.getItem(key)
      storage.setItem(key, previous && isControlCommandId(previous) ? previous : STORAGE_PROBE_ID)
      if (previous && isControlCommandId(previous)) storage.setItem(key, previous)
      else storage.removeItem(key)
      storageAvailable.value = true
      return true
    } catch {
      storageAvailable.value = false
      return false
    }
  }

  function storeCommand(binding: BoundControlCommand): boolean {
    const key = storageKey(binding.deliveryKey, binding.userId)
    if (!storage || !key) return false
    try {
      storage.setItem(key, binding.command.commandId)
      storageAvailable.value = true
      return true
    } catch {
      storageAvailable.value = false
      return false
    }
  }

  function cancelPendingWork() {
    generation += 1
    controller?.abort()
    controller = null
    if (pollTimer !== null) clearTimeout(pollTimer)
    pollTimer = null
    pollAttempt = 0
    if (grantExpiryTimer !== null) clearTimeout(grantExpiryTimer)
    grantExpiryTimer = null
    busy.value = false
  }

  function scheduleGrantExpiry(next: ControlGrant) {
    if (grantExpiryTimer !== null) clearTimeout(grantExpiryTimer)
    const delay = Math.max(0, Math.min(2_147_483_647, Date.parse(next.expiresAt) - Date.now()))
    grantExpiryTimer = setTimeout(() => {
      grantExpiryTimer = null
      if (grant.value?.grantId !== next.grantId || grant.value.revision !== next.revision) return
      grant.value = null
      if (!boundCommand.value) state.value = 'conflict'
    }, delay)
  }

  function classify(error: unknown): AgentModeControlState {
    const next: AgentModeControlState = error instanceof AgentModeControlTransportError ? error.kind : 'error'
    state.value = next
    return next
  }

  function schedulePoll() {
    if (!alive || boundCommand.value?.command.status !== 'accepted') return
    if (pollTimer !== null) clearTimeout(pollTimer)
    const wait = Math.min(pollMaxMs, pollBaseMs * 2 ** pollAttempt)
    pollAttempt += 1
    pollTimer = setTimeout(() => {
      pollTimer = null
      void reloadCommand()
    }, wait)
  }

  function acceptCommand(next: ControlCommand, binding: Omit<BoundControlCommand, 'command'>): boolean {
    const current = boundCommand.value?.command
    if (
      next.display.deliveryKey !== binding.deliveryKey
      || next.display.issueKey !== binding.issueKey
      || (current && current.commandId !== next.commandId)
    ) {
      state.value = 'error'
      return false
    }
    if (current?.commandId === next.commandId && next.statusRevision < current.statusRevision) return true
    if (
      current?.commandId === next.commandId
      && next.statusRevision === current.statusRevision
      && JSON.stringify(next) !== JSON.stringify(current)
    ) {
      state.value = 'error'
      return false
    }
    const nextBinding = { ...binding, command: next }
    boundCommand.value = nextBinding
    if (terminal(next)) {
      removeStored(binding.deliveryKey, binding.userId)
      pollAttempt = 0
      if (pollTimer !== null) clearTimeout(pollTimer)
      pollTimer = null
    } else {
      storeCommand(nextBinding)
      if (next.status === 'accepted') schedulePoll()
    }
    return true
  }

  async function reloadCommand() {
    const binding = boundCommand.value
    if (!binding || !contextUsable()) return
    const token = generation
    controller?.abort()
    const requestController = new AbortController()
    controller = requestController
    try {
      const next = await getCommand(binding.command.commandId, requestController.signal)
      if (!alive || token !== generation || requestController.signal.aborted) return
      if (!acceptCommand(next, binding)) return
      state.value = 'ready'
    } catch (error) {
      if (!alive || token !== generation || requestController.signal.aborted) return
      classify(error)
      if (state.value === 'forbidden' || state.value === 'not-found') {
        removeStored(binding.deliveryKey, binding.userId)
        boundCommand.value = null
      } else if (binding.command.status === 'accepted') {
        schedulePoll()
      }
    }
  }

  async function loadGrant(delivery: Delivery, userId: number) {
    const token = generation
    const contextVersion = options.authorityVersion.value
    const mutation = grantMutation?.deliveryKey === delivery.id
      ? grantMutation
      : { deliveryKey: delivery.id, key: makeKey() }
    grantMutation = mutation
    controller?.abort()
    const requestController = new AbortController()
    controller = requestController
    state.value = 'loading'
    try {
      const next = await issueGrant(delivery.id, mutation.key, requestController.signal)
      if (
        !alive
        || token !== generation
        || requestController.signal.aborted
        || userId !== options.userId.value
        || delivery.id !== options.delivery.value?.id
        || contextVersion !== options.authorityVersion.value
      ) return
      if (next.deliveryKey !== delivery.id || next.issueKey !== delivery.issueKey) {
        state.value = 'error'
        return
      }
      grant.value = next
      scheduleGrantExpiry(next)
      grantMutation = null
      state.value = 'ready'
    } catch (error) {
      if (!alive || token !== generation || requestController.signal.aborted) return
      classify(error)
    }
  }

  async function restoreOrLoad() {
    const delivery = options.delivery.value
    const userId = options.userId.value
    if (!contextUsable() || !delivery || userId == null) {
      state.value = options.degraded.value || !options.online.value ? 'offline' : 'idle'
      return
    }
    if (!storageAvailable.value) {
      state.value = 'error'
      return
    }
    const commandId = readStored(delivery.id)
    if (!storageAvailable.value) {
      state.value = 'error'
      return
    }
    if (!commandId) {
      await loadGrant(delivery, userId)
      return
    }
    state.value = 'loading'
    const token = generation
    controller?.abort()
    const requestController = new AbortController()
    controller = requestController
    try {
      const command = await getCommand(commandId, requestController.signal)
      if (!alive || token !== generation || requestController.signal.aborted
        || delivery.id !== options.delivery.value?.id || userId !== options.userId.value) return
      if (command.commandId !== commandId || command.display.deliveryKey !== delivery.id) {
        removeStored(delivery.id, userId)
        state.value = 'error'
        return
      }
      acceptCommand(command, {
        deliveryKey: command.display.deliveryKey,
        issueKey: command.display.issueKey,
        userId,
      })
      state.value = 'ready'
      // A terminal command is shown truthfully, but no stale grant is inferred.
      // The operator may dismiss it before a fresh grant is issued.
    } catch (error) {
      if (!alive || token !== generation || requestController.signal.aborted) return
      const failure = classify(error)
      if (failure === 'forbidden' || failure === 'not-found') removeStored(delivery.id, userId)
    }
  }

  async function activate(activation: ControlActivation): Promise<boolean> {
    const currentGrant = grant.value
    const delivery = options.delivery.value
    if (!controlAvailable.value || !currentGrant || !delivery || boundCommand.value) return false
    if (!currentGrant.targets.some((target) => targetSignature(target) === targetSignature(activation.target))) return false
    const request = requestFor(currentGrant, activation)
    if (!request || !proveStorageWritable(delivery.id)) {
      state.value = 'error'
      return false
    }
    const signature = JSON.stringify(request)
    const mutation = commandMutation?.signature === signature
      ? commandMutation
      : { signature, key: makeKey() }
    commandMutation = mutation
    const token = generation
    const binding = { deliveryKey: delivery.id, issueKey: currentGrant.issueKey, userId: options.userId.value! }
    controller?.abort()
    const requestController = new AbortController()
    controller = requestController
    busy.value = true
    try {
      const command = await createCommand(delivery.id, request, mutation.key, requestController.signal)
      if (!alive || token !== generation || requestController.signal.aborted) return false
      if (
        command.action !== request.action
        || command.display.deliveryKey !== binding.deliveryKey
        || command.display.issueKey !== binding.issueKey
      ) {
        try {
          await transitionCommand(command.commandId, 'withdraw', command.statusRevision, makeKey())
        } catch { /* server expiry remains the final fail-safe */ }
        state.value = 'error'
        return false
      }
      const nextBinding = {
        deliveryKey: command.display.deliveryKey,
        issueKey: command.display.issueKey,
        userId: binding.userId,
        command,
      }
      if (!storeCommand(nextBinding)) {
        // Creation can only return a pending challenge. Best-effort exact
        // withdrawal keeps an unresumable browser from abandoning it.
        try {
          await transitionCommand(command.commandId, 'withdraw', command.statusRevision, makeKey())
        } catch { /* server expiry remains the final fail-safe */ }
        state.value = 'error'
        return false
      }
      boundCommand.value = nextBinding
      commandMutation = null
      state.value = 'ready'
      focusToken.value += 1
      return true
    } catch (error) {
      if (!alive || token !== generation || requestController.signal.aborted) return false
      classify(error)
      return false
    } finally {
      if (token === generation) busy.value = false
    }
  }

  async function transition(operation: 'confirm' | 'withdraw'): Promise<boolean> {
    const binding = boundCommand.value
    if (!binding || binding.command.status !== 'pending_confirmation' || !contextUsable() || busy.value) return false
    const revision = binding.command.statusRevision
    const mutation = transitionMutation
      && transitionMutation.operation === operation
      && transitionMutation.commandId === binding.command.commandId
      && transitionMutation.revision === revision
      ? transitionMutation
      : { operation, commandId: binding.command.commandId, revision, key: makeKey() }
    transitionMutation = mutation
    const token = generation
    controller?.abort()
    const requestController = new AbortController()
    controller = requestController
    busy.value = true
    try {
      const next = await transitionCommand(binding.command.commandId, operation, revision, mutation.key, requestController.signal)
      if (!alive || token !== generation || requestController.signal.aborted) return false
      if (!acceptCommand(next, binding)) return false
      transitionMutation = null
      state.value = 'ready'
      if (operation === 'withdraw') focusReturnToken.value += 1
      return true
    } catch (error) {
      if (!alive || token !== generation || requestController.signal.aborted) return false
      classify(error)
      return false
    } finally {
      if (token === generation) busy.value = false
    }
  }

  const confirm = () => transition('confirm')
  const withdraw = () => transition('withdraw')
  function confirmExact(commandId: string, statusRevision: number): Promise<boolean> {
    const current = boundCommand.value?.command
    if (
      !current
      || current.status !== 'pending_confirmation'
      || current.commandId !== commandId
      || current.statusRevision !== statusRevision
    ) return Promise.resolve(false)
    return transition('confirm')
  }

  async function changeContext() {
    const binding = boundCommand.value
    cancelPendingWork()
    const token = generation
    grant.value = null
    grantMutation = null
    commandMutation = null
    if (binding && binding.command.status === 'pending_confirmation') {
      // Selection/principal drift cannot relabel a challenge. Withdraw it
      // against its original command resource before discarding local state.
      const revision = binding.command.statusRevision
      const mutation = transitionMutation
        && transitionMutation.operation === 'withdraw'
        && transitionMutation.commandId === binding.command.commandId
        && transitionMutation.revision === revision
        ? transitionMutation
        : { operation: 'withdraw' as const, commandId: binding.command.commandId, revision, key: makeKey() }
      transitionMutation = mutation
      const requestController = new AbortController()
      controller = requestController
      try {
        const next = await transitionCommand(
          binding.command.commandId,
          'withdraw',
          revision,
          mutation.key,
          requestController.signal,
        )
        if (!alive || token !== generation || requestController.signal.aborted) return
        if (!acceptCommand(next, binding)) return
        transitionMutation = null
        if (next.status === 'accepted') {
          state.value = options.online.value && !options.degraded.value ? 'ready' : 'offline'
          return
        }
        if (!terminal(next)) {
          state.value = 'conflict'
          return
        }
        removeStored(binding.deliveryKey, binding.userId)
      } catch (error) {
        if (!alive || token !== generation || requestController.signal.aborted) return
        const failure = error instanceof AgentModeControlTransportError ? error.kind : 'error'
        if (failure !== 'forbidden' && failure !== 'not-found') {
          state.value = failure
          return
        }
        // Concealed/revoked is definitive: no old projection remains safe.
        removeStored(binding.deliveryKey, binding.userId)
      }
      boundCommand.value = null
    } else if (binding && binding.command.status === 'accepted') {
      transitionMutation = null
      // Accepted work remains bound to the original safe label and is polled;
      // it is never cosmetically withdrawn or relabeled to the new selection.
      schedulePoll()
      state.value = options.online.value && !options.degraded.value ? 'ready' : 'offline'
      return
    } else {
      transitionMutation = null
      boundCommand.value = null
    }
    if (!alive || token !== generation) return
    await restoreOrLoad()
  }

  function dismissTerminal() {
    const binding = boundCommand.value
    if (!binding || !terminal(binding.command)) return
    removeStored(binding.deliveryKey, binding.userId)
    boundCommand.value = null
    focusReturnToken.value += 1
    void restoreOrLoad()
  }

  function authorityReset(
    clearStorage: boolean,
    context?: { deliveryKey: string; userId: number } | null,
  ) {
    const binding = boundCommand.value
    cancelPendingWork()
    grant.value = null
    boundCommand.value = null
    grantMutation = null
    commandMutation = null
    transitionMutation = null
    if (clearStorage) {
      if (binding) removeStored(binding.deliveryKey, binding.userId)
      else if (context) removeStored(context.deliveryKey, context.userId)
      else if (options.delivery.value && options.userId.value != null) {
        removeStored(options.delivery.value.id, options.userId.value)
      }
    }
    state.value = 'idle'
  }

  let previousContext = options.delivery.value && options.userId.value != null
    ? { deliveryKey: options.delivery.value.id, userId: options.userId.value }
    : null

  watch([options.delivery, options.userId], ([delivery, userId]) => {
    const next = delivery && userId != null ? { deliveryKey: delivery.id, userId } : null
    if (JSON.stringify(next) === JSON.stringify(previousContext)) return
    const previous = previousContext
    previousContext = next
    if (previous && next && previous.userId !== next.userId) {
      authorityReset(true, previous)
      void restoreOrLoad()
      return
    }
    void changeContext()
  }, { flush: 'sync' })
  watch([options.online, options.degraded], ([online, degraded]) => {
    if (!online || degraded) {
      controller?.abort()
      grant.value = null
      if (grantExpiryTimer !== null) clearTimeout(grantExpiryTimer)
      grantExpiryTimer = null
      state.value = 'offline'
      busy.value = false
      return
    }
    void restoreOrLoad()
  }, { flush: 'sync' })
  watch(options.authorityAvailable, (available) => {
    if (!available) authorityReset(true)
    else void restoreOrLoad()
  }, { flush: 'sync' })
  watch(options.authorityVersion, () => {
    if (!contextUsable()) return
    cancelPendingWork()
    grant.value = null
    grantMutation = null
    commandMutation = null
    if (boundCommand.value) void reloadCommand()
    else void restoreOrLoad()
  }, { flush: 'sync' })
  watch(permissionsEpochGeneration, () => authorityReset(true), { flush: 'sync' })
  watch(permissionsEpoch, (next, previous) => {
    if (next !== previous && previous != null) authorityReset(true)
  }, { flush: 'sync' })
  watch(sessionExpired, (expired) => {
    if (expired) authorityReset(true)
  }, { flush: 'sync' })
  if (options.enabled) watch(options.enabled, (value) => {
    if (!value) authorityReset(false)
    else void restoreOrLoad()
  }, { flush: 'sync' })

  function initialize() {
    if (!storage) {
      storageAvailable.value = false
      state.value = 'error'
      return Promise.resolve()
    }
    return restoreOrLoad()
  }

  function dispose() {
    if (!alive) return
    alive = false
    cancelPendingWork()
    grant.value = null
    boundCommand.value = null
  }

  if (getCurrentInstance()) {
    onMounted(() => { void initialize() })
    onBeforeUnmount(dispose)
  }

  return {
    state,
    grant,
    targets,
    boundCommand,
    busy,
    storageAvailable,
    controlAvailable,
    transitionAvailable,
    focusToken,
    focusReturnToken,
    initialize,
    activate,
    confirm,
    confirmExact,
    withdraw,
    reloadCommand,
    dismissTerminal,
    dispose,
  }
}
