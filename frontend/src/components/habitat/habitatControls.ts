/* PAIMOS — Copyright (C) 2026 Markus Barta; AGPL-3.0-only. */
import { api } from '@/api/client'
import { parseIsoInstant } from '@/services/agentModeAggregateSchema'
import { epoch, fields, invalid, object, positive, uuid } from './habitatBoundary'
export type ControlKind = 'interrupt' | 'stop'
export type HabitatMessageLevel = 'simple' | 'steer'
export interface HabitatMessageReceipt {
  messageId: string
  deliveryId: string
  deliveryLevel: HabitatMessageLevel
  revision: number
}
export interface HabitatControl {
  id: string
  kind: ControlKind
  state: 'pending' | 'claimed' | 'applied' | 'rejected'
  outcome: string | null
  reason: string | null
}
const states = {
  pending: 'requested',
  claimed: 'claimed',
  applied: 'completed',
  rejected: 'failed',
} as const
function parseControl(
  value: unknown,
  sessionId: string,
  kind: ControlKind,
  expected?: { projectId: number; id: string },
): HabitatControl {
  const row = object(value)
  fields(
    row,
    [
      'id',
      'harness_session_id',
      'sequence',
      'kind',
      'state',
      'requested_at',
      ...(expected ? ['project_id', 'correlation_id'] : ['requested_by_user_id']),
    ],
    ['reason', 'claimed_at', 'completed_at', ...(expected ? ['outcome'] : [])],
  )
  if (
    !uuid(row.id) ||
    row.harness_session_id !== sessionId ||
    row.kind !== kind ||
    !positive(row.sequence) ||
    !Object.prototype.hasOwnProperty.call(states, String(row.state)) ||
    !parseIsoInstant(row.requested_at) ||
    ['claimed_at', 'completed_at'].some(
      (key) => row[key] !== undefined && !parseIsoInstant(row[key]),
    ) ||
    (row.reason !== undefined &&
      !['applied', 'not_running', 'unsupported', 'ownership_lost', 'failed'].includes(
        String(row.reason),
      ))
  )
    invalid()
  const terminal = row.state === 'applied' || row.state === 'rejected'
  if (
    terminal
      ? !row.completed_at || !row.reason || (row.state === 'applied') !== (row.reason === 'applied')
      : row.completed_at !== undefined || row.reason !== undefined
  )
    invalid()
  if (row.state === 'claimed' && !row.claimed_at) invalid()
  if (
    expected
      ? row.id !== expected.id ||
        row.correlation_id !== expected.id ||
        row.project_id !== expected.projectId ||
        (terminal ? row.outcome !== row.state : row.outcome !== undefined)
      : !positive(row.requested_by_user_id)
  )
    invalid()
  return {
    id: row.id as string,
    kind,
    state: row.state as HabitatControl['state'],
    outcome: terminal ? String(row.state) : null,
    reason: (row.reason as string | undefined) ?? null,
  }
}
export function parseHabitatControlReceipt(value: unknown, sessionId: string, kind: ControlKind) {
  const root = object(value)
  fields(root, ['schema_version', 'control', 'state'])
  if (root.schema_version !== 1) invalid()
  const control = parseControl(root.control, sessionId, kind)
  if (root.state !== states[control.state]) invalid()
  return control
}
export function parseHabitatControlOutcome(
  value: unknown,
  projectId: number,
  sessionId: string,
  control: HabitatControl,
) {
  return parseControl(value, sessionId, control.kind, { projectId, id: control.id })
}
export async function requestHabitatControl(
  projectId: number,
  sessionId: string,
  kind: ControlKind,
  revision: number,
  requestKey: string,
  signal?: AbortSignal,
) {
  if (!positive(projectId) || !positive(revision) || !uuid(sessionId) || !uuid(requestKey))
    invalid()
  return parseHabitatControlReceipt(
    await api.post<unknown>(
      `/projects/${projectId}/harness-sessions/${sessionId}/controls/v1/${kind}`,
      { expected_revision: revision, request_key: requestKey },
      { signal },
    ),
    sessionId,
    kind,
  )
}
export function parseHabitatMessageReceipt(
  value: unknown,
  sessionId: string,
  utteranceId: string,
  revision: number,
  deliveryLevel: HabitatMessageLevel,
): HabitatMessageReceipt {
  const root = object(value)
  fields(root, [
    'schema_version',
    'utterance_id',
    'harness_session_id',
    'harness_session_revision',
    'message_id',
    'delivery_id',
    'delivery_level',
    'created_at',
  ])
  if (
    root.schema_version !== 1 ||
    root.utterance_id !== utteranceId ||
    root.harness_session_id !== sessionId ||
    root.harness_session_revision !== revision ||
    root.delivery_level !== deliveryLevel ||
    !uuid(root.message_id) ||
    !uuid(root.delivery_id) ||
    !parseIsoInstant(root.created_at)
  )
    invalid()
  return {
    messageId: root.message_id as string,
    deliveryId: root.delivery_id as string,
    deliveryLevel,
    revision,
  }
}
export async function sendHabitatMessage(
  projectId: number,
  sessionId: string,
  revision: number,
  utteranceId: string,
  text: string,
  deliveryLevel: HabitatMessageLevel,
  signal?: AbortSignal,
) {
  if (
    !positive(projectId) ||
    !positive(revision) ||
    !uuid(sessionId) ||
    !/^utt_[0-9a-f]{32}$/.test(utteranceId) ||
    !text ||
    text !== text.trim() ||
    new TextEncoder().encode(text).length > 8192 ||
    !['simple', 'steer'].includes(deliveryLevel)
  )
    invalid()
  return parseHabitatMessageReceipt(
    await api.post<unknown>(
      `/projects/${projectId}/harness-sessions/${sessionId}/messages/v1`,
      {
        schema_version: 1,
        utterance_id: utteranceId,
        expected_revision: revision,
        text,
        delivery_level: deliveryLevel,
      },
      { signal },
    ),
    sessionId,
    utteranceId,
    revision,
    deliveryLevel,
  )
}
export async function loadHabitatControl(
  projectId: number,
  sessionId: string,
  control: HabitatControl,
  signal?: AbortSignal,
) {
  return parseHabitatControlOutcome(
    epoch(
      await api.getWithMeta<unknown>(
        `/projects/${projectId}/harness-sessions/${sessionId}/controls/${control.id}`,
        { signal },
      ),
    ),
    projectId,
    sessionId,
    control,
  )
}
export interface AssignmentEvent {
  revision: number
  created_at: string
  before_parent_harness_session_id: string | null
  after_parent_harness_session_id: string | null
  before_ticket_id: number | null
  after_ticket_id: number | null
  before_work_shape: 'ship' | 'scout' | null
  after_work_shape: 'ship' | 'scout' | null
}
export interface AssignmentHistory {
  events: AssignmentEvent[]
  next: number | null
}
export function parseAssignmentHistory(
  value: unknown,
  sessionId: string,
  after = 0,
): AssignmentHistory {
  const root = object(value)
  fields(root, ['schema_version', 'session_id', 'events', 'next_after_revision'])
  if (
    root.schema_version !== 1 ||
    root.session_id !== sessionId ||
    !uuid(sessionId) ||
    !Array.isArray(root.events) ||
    root.events.length > 50
  )
    invalid()
  let revision = after
  const events = (root.events as unknown[]).map((raw) => {
    const row = object(raw)
    fields(row, [
      'revision',
      'created_at',
      'before_parent_harness_session_id',
      'after_parent_harness_session_id',
      'before_ticket_id',
      'after_ticket_id',
      'before_work_shape',
      'after_work_shape',
    ])
    if (!positive(row.revision) || row.revision <= revision || !parseIsoInstant(row.created_at))
      invalid()
    revision = Number(row.revision)
    for (const prefix of ['before', 'after']) {
      const parent = row[`${prefix}_parent_harness_session_id`],
        ticket = row[`${prefix}_ticket_id`],
        shape = row[`${prefix}_work_shape`]
      if (
        (parent !== null && (!uuid(parent) || parent === sessionId)) ||
        (ticket !== null && !positive(ticket)) ||
        (ticket === null ? shape !== null : shape !== 'ship' && shape !== 'scout')
      )
        invalid()
    }
    return row as unknown as AssignmentEvent
  })
  if (root.next_after_revision !== (events.length ? revision : null)) invalid()
  return { events, next: root.next_after_revision as number | null }
}
export async function loadAssignmentHistory(
  projectId: number,
  sessionId: string,
  after = 0,
  signal?: AbortSignal,
) {
  if (!positive(projectId) || !uuid(sessionId) || !Number.isSafeInteger(after) || after < 0)
    invalid()
  return parseAssignmentHistory(
    epoch(
      await api.getWithMeta<unknown>(
        `/projects/${projectId}/harness-sessions/${sessionId}/assignment-history/v1?after_revision=${after}&limit=50`,
        { signal },
      ),
    ),
    sessionId,
    after,
  )
}
