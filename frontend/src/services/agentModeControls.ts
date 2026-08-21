/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

// PAI-809 — strict operator transport for Agent Mode supervisory controls.
// This boundary accepts only the closed public projection. In particular it
// never repairs malformed identifiers, integers, enums, dates, or extra
// fields into something that looks usable to the controls composable.

import { api, ApiError, isSessionExpiredError, isStalePermissionsEpochError } from '@/api/client'
import { isExactAgentModeDeliveryKey } from '@/services/agentModeTransport'

export const CONTROL_ACTIONS = [
  'issue.priority.set',
  'run.cancel.queued',
  'run.cancel.running',
  'input.respond',
  'run.pause',
  'run.resume',
] as const
export type ControlAction = (typeof CONTROL_ACTIONS)[number]

export const CONTROL_COMMAND_STATUSES = [
  'pending_confirmation',
  'accepted',
  'applied',
  'rejected',
  'expired',
] as const
export type ControlCommandStatus = (typeof CONTROL_COMMAND_STATUSES)[number]

export const CONTROL_OUTCOMES = ['applied', 'rejected', 'outcome_unknown'] as const
export type ControlOutcome = (typeof CONTROL_OUTCOMES)[number]

export const CONTROL_SAFE_REASONS = [
  'withdrawn',
  'confirmation_expired',
  'stale_target',
  'capability_unavailable',
  'capability_revoked',
  'capability_expired',
  'lease_revoked',
  'lease_expired',
  'policy_requires_second_approver',
  'credential_revoked',
  'authority_changed',
  'input_superseded',
  'input_expired',
  'run_terminal',
  'cancelled',
  'runtime_state_changed',
  'runner_lost',
  'natural_exit',
  'unsupported_platform',
  'effect_rejected',
  'process_termination_failed',
] as const
export type ControlSafeReason = (typeof CONTROL_SAFE_REASONS)[number]

export const CONTROL_CHALLENGE_TEMPLATES = [
  'issue_priority_set',
  'run_cancel_queued',
  'run_cancel_running',
  'input_approve',
  'input_reject',
  'input_choice',
  'run_pause',
  'run_resume',
] as const
export type ControlChallengeTemplate = (typeof CONTROL_CHALLENGE_TEMPLATES)[number]

export type ControlPriority = 'low' | 'medium' | 'high'
export type ControlInputKind = 'approval' | 'choice'
export type ControlRuntimeState = 'running' | 'paused'

export interface ControlGrant {
  grantId: string
  revision: number
  deliveryKey: string
  issueKey: string
  actions: readonly ControlAction[]
  targets: readonly ControlTarget[]
  expiresAt: string
}

export type ControlTarget =
  | { action: 'issue.priority.set' }
  | { action: 'run.cancel.queued' | 'run.cancel.running'; runId: number }
  | {
    action: 'input.respond'
    inputRequestId: string
    inputRequestRevision: number
    inputKind: 'approval'
  }
  | {
    action: 'input.respond'
    inputRequestId: string
    inputRequestRevision: number
    inputKind: 'choice'
    optionCodes: readonly string[]
  }
  | {
    action: 'run.pause' | 'run.resume'
    runId: number
    runtimeState: ControlRuntimeState
    runtimeRevision: number
  }

export interface ControlDisplayBinding {
  issueKey: string
  deliveryKey: string
}

export type ControlDisplay = ControlDisplayBinding & (
  | { priority: ControlPriority }
  | { runId: number }
  | { inputKind: ControlInputKind }
  | { inputKind: ControlInputKind; choiceOrdinal: number; choiceCode: string }
  | { runtimeState: ControlRuntimeState; runtimeRevision: number }
)

export interface ControlCommand {
  commandId: string
  statusRevision: number
  action: ControlAction
  status: ControlCommandStatus
  challengeTemplate: ControlChallengeTemplate
  expiresAt: string
  display: ControlDisplay
  outcome: ControlOutcome | null
  reason: ControlSafeReason | null
}

export type ControlCommandCreate =
  | { grantId: string; grantRevision: number; action: 'issue.priority.set'; priority: ControlPriority }
  | { grantId: string; grantRevision: number; action: 'run.cancel.queued' | 'run.cancel.running'; runId: number }
  | {
    grantId: string
    grantRevision: number
    action: 'input.respond'
    inputRequestId: string
    inputRequestRevision: number
    inputResponse: 'approve' | 'reject'
  }
  | {
    grantId: string
    grantRevision: number
    action: 'input.respond'
    inputRequestId: string
    inputRequestRevision: number
    inputResponse: 'choice'
    choiceOrdinal: number
  }
  | {
    grantId: string
    grantRevision: number
    action: 'run.pause' | 'run.resume'
    runId: number
    runtimeRevision: number
  }

export type AgentModeControlTransportErrorKind =
  | 'offline'
  | 'forbidden'
  | 'not-found'
  | 'conflict'
  | 'error'

export class AgentModeControlTransportError extends Error {
  constructor(
    public readonly kind: AgentModeControlTransportErrorKind,
    public readonly status: number | null,
  ) {
    super(`Agent Mode controls ${kind}`)
    this.name = 'AgentModeControlTransportError'
  }
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const RFC3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|([+-])(\d{2}):(\d{2}))$/
const ISSUE_KEY = /^[A-Z][A-Z0-9]{0,9}-[1-9][0-9]{0,8}$/
const ACTION_SET = new Set<string>(CONTROL_ACTIONS)
const STATUS_SET = new Set<string>(CONTROL_COMMAND_STATUSES)
const OUTCOME_SET = new Set<string>(CONTROL_OUTCOMES)
const REASON_SET = new Set<string>(CONTROL_SAFE_REASONS)
const TEMPLATE_SET = new Set<string>(CONTROL_CHALLENGE_TEMPLATES)
const PRIORITIES = new Set<string>(['low', 'medium', 'high'])
const INPUT_KINDS = new Set<string>(['approval', 'choice'])
const RUNTIME_STATES = new Set<string>(['running', 'paused'])
const OPTION_CODES = new Set<string>([
  'choice_1', 'choice_2', 'choice_3', 'choice_4',
  'choice_5', 'choice_6', 'choice_7', 'choice_8',
])

function record(value: unknown): Record<string, unknown> | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null
  const proto = Object.getPrototypeOf(value)
  return proto === Object.prototype || proto === null ? value as Record<string, unknown> : null
}

function exactKeys(value: Record<string, unknown>, required: readonly string[], optional: readonly string[] = []): boolean {
  const allowed = new Set([...required, ...optional])
  const keys = Object.keys(value)
  return required.every((key) => Object.prototype.hasOwnProperty.call(value, key))
    && keys.every((key) => allowed.has(key))
}

function positiveInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

function uuid(value: unknown): value is string {
  return typeof value === 'string' && UUID.test(value)
}

export function isControlCommandId(value: unknown): value is string {
  return uuid(value)
}

function instant(value: unknown): value is string {
  if (typeof value !== 'string') return false
  const match = RFC3339.exec(value)
  if (!match) return false
  const [year, month, day, hour, minute, second, offsetHour, offsetMinute] = [
    match[1], match[2], match[3], match[4], match[5], match[6], match[8] ?? '0', match[9] ?? '0',
  ].map(Number)
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const monthDays = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  return month >= 1
    && month <= 12
    && day >= 1
    && day <= monthDays[month - 1]
    && hour <= 23
    && minute <= 59
    && second <= 59
    && offsetHour <= 23
    && offsetMinute <= 59
}

function closedString<T extends string>(value: unknown, values: ReadonlySet<string>): value is T {
  return typeof value === 'string' && values.has(value)
}

function failSchema(): never {
  throw new AgentModeControlTransportError('error', null)
}

export function parseControlGrant(value: unknown): ControlGrant {
  const wire = record(value)
  if (!wire || !exactKeys(wire, ['grant_id', 'revision', 'delivery_key', 'issue_key', 'actions', 'targets', 'expires_at'])) failSchema()
  if (
    !uuid(wire.grant_id)
    || !positiveInteger(wire.revision)
    || !isExactAgentModeDeliveryKey(wire.delivery_key)
    || typeof wire.issue_key !== 'string'
    || !ISSUE_KEY.test(wire.issue_key)
    || !Array.isArray(wire.actions)
    || !wire.actions.every((action) => closedString<ControlAction>(action, ACTION_SET))
    || new Set(wire.actions).size !== wire.actions.length
    || !Array.isArray(wire.targets)
    || !instant(wire.expires_at)
  ) failSchema()
  const actions = wire.actions as ControlAction[]
  const targets = wire.targets.map(parseTarget)
  if (targets.some((target) => !actions.includes(target.action))) failSchema()
  if (new Set(targets.map((target) => JSON.stringify(target))).size !== targets.length) failSchema()
  return {
    grantId: wire.grant_id,
    revision: wire.revision,
    deliveryKey: wire.delivery_key,
    issueKey: wire.issue_key,
    actions: [...actions],
    targets,
    expiresAt: wire.expires_at,
  }
}

function parseTarget(value: unknown): ControlTarget {
  const wire = record(value)
  if (!wire || !closedString<ControlAction>(wire.action, ACTION_SET)) failSchema()
  switch (wire.action) {
    case 'issue.priority.set':
      if (!exactKeys(wire, ['action'])) failSchema()
      return { action: wire.action }
    case 'run.cancel.queued':
    case 'run.cancel.running':
      if (!exactKeys(wire, ['action', 'run_id']) || !positiveInteger(wire.run_id)) failSchema()
      return { action: wire.action, runId: wire.run_id }
    case 'input.respond': {
      const commonKeys = ['action', 'input_request_id', 'input_request_revision', 'input_kind']
      if (!uuid(wire.input_request_id)
        || !positiveInteger(wire.input_request_revision)
        || !closedString<ControlInputKind>(wire.input_kind, INPUT_KINDS)
      ) failSchema()
      if (wire.input_kind === 'approval') {
        if (!exactKeys(wire, commonKeys)) failSchema()
        return {
          action: wire.action,
          inputRequestId: wire.input_request_id,
          inputRequestRevision: wire.input_request_revision,
          inputKind: wire.input_kind,
        }
      }
      if (
        !exactKeys(wire, [...commonKeys, 'option_codes'])
        || !Array.isArray(wire.option_codes)
        || wire.option_codes.length === 0
        || !wire.option_codes.every((code, index) => (
          typeof code === 'string' && OPTION_CODES.has(code) && code === `choice_${index + 1}`
        ))
        || new Set(wire.option_codes).size !== wire.option_codes.length
      ) failSchema()
      return {
        action: wire.action,
        inputRequestId: wire.input_request_id,
        inputRequestRevision: wire.input_request_revision,
        inputKind: wire.input_kind,
        optionCodes: [...wire.option_codes],
      }
    }
    case 'run.pause':
    case 'run.resume':
      if (
        !exactKeys(wire, ['action', 'run_id', 'runtime_state', 'runtime_revision'])
        || !positiveInteger(wire.run_id)
        || !closedString<ControlRuntimeState>(wire.runtime_state, RUNTIME_STATES)
        || !positiveInteger(wire.runtime_revision)
      ) failSchema()
      return {
        action: wire.action,
        runId: wire.run_id,
        runtimeState: wire.runtime_state,
        runtimeRevision: wire.runtime_revision,
      }
  }
}

function parseDisplay(action: ControlAction, template: ControlChallengeTemplate, value: unknown): ControlDisplay {
  const wire = record(value)
  if (
    !wire
    || typeof wire.issue_key !== 'string'
    || !ISSUE_KEY.test(wire.issue_key)
    || !isExactAgentModeDeliveryKey(wire.delivery_key)
  ) failSchema()
  const binding = { issueKey: wire.issue_key, deliveryKey: wire.delivery_key }
  if (action === 'issue.priority.set' && template === 'issue_priority_set') {
    if (!exactKeys(wire, ['issue_key', 'delivery_key', 'priority']) || !closedString<ControlPriority>(wire.priority, PRIORITIES)) failSchema()
    return { ...binding, priority: wire.priority }
  }
  if ((action === 'run.cancel.queued' && template === 'run_cancel_queued')
    || (action === 'run.cancel.running' && template === 'run_cancel_running')) {
    if (!exactKeys(wire, ['issue_key', 'delivery_key', 'run_id']) || !positiveInteger(wire.run_id)) failSchema()
    return { ...binding, runId: wire.run_id }
  }
  if (action === 'input.respond') {
    if (template === 'input_choice') {
      if (
        !exactKeys(wire, ['issue_key', 'delivery_key', 'input_kind', 'choice_ordinal', 'choice_code'])
        || wire.input_kind !== 'choice'
        || !positiveInteger(wire.choice_ordinal)
        || wire.choice_ordinal > 8
        || wire.choice_code !== `choice_${wire.choice_ordinal}`
      ) failSchema()
      return { ...binding, inputKind: 'choice', choiceOrdinal: wire.choice_ordinal, choiceCode: wire.choice_code }
    }
    if (
      !['input_approve', 'input_reject'].includes(template)
      || !exactKeys(wire, ['issue_key', 'delivery_key', 'input_kind'])
      || !closedString<ControlInputKind>(wire.input_kind, INPUT_KINDS)
      || wire.input_kind !== 'approval'
    ) failSchema()
    return { ...binding, inputKind: wire.input_kind }
  }
  if ((action === 'run.pause' && template === 'run_pause') || (action === 'run.resume' && template === 'run_resume')) {
    if (
      !exactKeys(wire, ['issue_key', 'delivery_key', 'runtime_state', 'runtime_revision'])
      || !closedString<ControlRuntimeState>(wire.runtime_state, RUNTIME_STATES)
      || !positiveInteger(wire.runtime_revision)
    ) failSchema()
    return { ...binding, runtimeState: wire.runtime_state, runtimeRevision: wire.runtime_revision }
  }
  return failSchema()
}

export function parseControlCommand(value: unknown): ControlCommand {
  const wire = record(value)
  const required = ['command_id', 'status_revision', 'action', 'status', 'challenge_template', 'expires_at', 'display']
  if (!wire || !exactKeys(wire, required, ['outcome', 'reason'])) failSchema()
  if (
    !uuid(wire.command_id)
    || !positiveInteger(wire.status_revision)
    || !closedString<ControlAction>(wire.action, ACTION_SET)
    || !closedString<ControlCommandStatus>(wire.status, STATUS_SET)
    || !closedString<ControlChallengeTemplate>(wire.challenge_template, TEMPLATE_SET)
    || !instant(wire.expires_at)
    || (wire.outcome !== undefined && !closedString<ControlOutcome>(wire.outcome, OUTCOME_SET))
    || (wire.reason !== undefined && !closedString<ControlSafeReason>(wire.reason, REASON_SET))
  ) failSchema()
  return {
    commandId: wire.command_id,
    statusRevision: wire.status_revision,
    action: wire.action,
    status: wire.status,
    challengeTemplate: wire.challenge_template,
    expiresAt: wire.expires_at,
    display: parseDisplay(wire.action, wire.challenge_template, wire.display),
    outcome: wire.outcome ?? null,
    reason: wire.reason ?? null,
  }
}

export function newControlIdempotencyKey(): string {
  const direct = globalThis.crypto?.randomUUID?.()
  if (direct && UUID.test(direct)) return direct
  const bytes = new Uint8Array(16)
  if (typeof globalThis.crypto?.getRandomValues !== 'function') {
    throw new AgentModeControlTransportError('error', null)
  }
  globalThis.crypto.getRandomValues(bytes)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = [...bytes].map((byte) => byte.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

function classify(error: unknown): never {
  if (error instanceof AgentModeControlTransportError) throw error
  if (isSessionExpiredError(error) || isStalePermissionsEpochError(error)) throw error
  if (error instanceof ApiError) {
    if (error.status === 0) throw new AgentModeControlTransportError('offline', 0)
    if (error.status === 403) throw new AgentModeControlTransportError('forbidden', 403)
    if (error.status === 404) throw new AgentModeControlTransportError('not-found', 404)
    if (error.status === 409) throw new AgentModeControlTransportError('conflict', 409)
    throw new AgentModeControlTransportError('error', error.status)
  }
  throw new AgentModeControlTransportError('offline', 0)
}

function mutationOptions(idempotencyKey: string, signal?: AbortSignal) {
  if (!UUID.test(idempotencyKey)) throw new AgentModeControlTransportError('error', null)
  return { signal, headers: { 'Idempotency-Key': idempotencyKey } }
}

export async function issueControlGrant(deliveryKey: string, idempotencyKey: string, signal?: AbortSignal): Promise<ControlGrant> {
  if (!isExactAgentModeDeliveryKey(deliveryKey)) failSchema()
  try {
    return parseControlGrant(await api.post(
      `/agent-mode/deliveries/${encodeURIComponent(deliveryKey)}/control-capability-grants`,
      {},
      mutationOptions(idempotencyKey, signal),
    ))
  } catch (error) { return classify(error) }
}

export async function getControlCommand(commandId: string, signal?: AbortSignal): Promise<ControlCommand> {
  if (!uuid(commandId)) failSchema()
  try {
    return parseControlCommand(await api.get(`/agent-mode/control-commands/${commandId}`, { signal }))
  } catch (error) { return classify(error) }
}

function createWire(request: ControlCommandCreate): Record<string, unknown> {
  const value = record(request)
  if (!value
    || !uuid(value.grantId)
    || !positiveInteger(value.grantRevision)
    || !closedString<ControlAction>(value.action, ACTION_SET)
  ) failSchema()
  const common = { grant_id: request.grantId, grant_revision: request.grantRevision, action: request.action }
  switch (request.action) {
    case 'issue.priority.set':
      if (!exactKeys(value, ['grantId', 'grantRevision', 'action', 'priority'])
        || !closedString<ControlPriority>(value.priority, PRIORITIES)) failSchema()
      return { ...common, priority: request.priority }
    case 'run.cancel.queued':
    case 'run.cancel.running':
      if (!exactKeys(value, ['grantId', 'grantRevision', 'action', 'runId']) || !positiveInteger(value.runId)) failSchema()
      return { ...common, run_id: request.runId }
    case 'input.respond': {
      const inputKeys = ['grantId', 'grantRevision', 'action', 'inputRequestId', 'inputRequestRevision', 'inputResponse']
      if (!uuid(value.inputRequestId) || !positiveInteger(value.inputRequestRevision)) failSchema()
      if (value.inputResponse === 'choice') {
        if (!exactKeys(value, [...inputKeys, 'choiceOrdinal'])
          || !positiveInteger(value.choiceOrdinal) || value.choiceOrdinal > 8) failSchema()
      } else if (!['approve', 'reject'].includes(String(value.inputResponse)) || !exactKeys(value, inputKeys)) failSchema()
      return {
        ...common,
        input_request_id: request.inputRequestId,
        input_request_revision: request.inputRequestRevision,
        input_response: request.inputResponse,
        ...(request.inputResponse === 'choice' ? { choice_ordinal: request.choiceOrdinal } : {}),
      }
    }
    case 'run.pause':
    case 'run.resume':
      if (!exactKeys(value, ['grantId', 'grantRevision', 'action', 'runId', 'runtimeRevision'])
        || !positiveInteger(value.runId) || !positiveInteger(value.runtimeRevision)) failSchema()
      return { ...common, run_id: request.runId, runtime_revision: request.runtimeRevision }
  }
}

export async function createControlCommand(
  deliveryKey: string,
  request: ControlCommandCreate,
  idempotencyKey: string,
  signal?: AbortSignal,
): Promise<ControlCommand> {
  if (!isExactAgentModeDeliveryKey(deliveryKey)) failSchema()
  const body = createWire(request)
  try {
    return parseControlCommand(await api.post(
      `/agent-mode/deliveries/${encodeURIComponent(deliveryKey)}/control-commands`,
      body,
      mutationOptions(idempotencyKey, signal),
    ))
  } catch (error) { return classify(error) }
}

export async function transitionControlCommand(
  commandId: string,
  operation: 'confirm' | 'withdraw',
  statusRevision: number,
  idempotencyKey: string,
  signal?: AbortSignal,
): Promise<ControlCommand> {
  if (!uuid(commandId) || !['confirm', 'withdraw'].includes(operation) || !positiveInteger(statusRevision)) failSchema()
  try {
    return parseControlCommand(await api.post(
      `/agent-mode/control-commands/${commandId}`,
      { operation, status_revision: statusRevision },
      mutationOptions(idempotencyKey, signal),
    ))
  } catch (error) { return classify(error) }
}
