/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-861 — strict GET /projects/{id}/session-home/v1 boundary.
 * Every wire object is closed. Rows become UI-visible only after the whole
 * projection, its truth relationships, totals, and canonical ordering pass.
 */

import { api } from '@/api/client'

export type Paimos6TargetKind = 'paimos' | 'project_agent'
export type Paimos6SessionPhase = 'paimos' | 'starting' | 'working' | 'yielded' | 'stopping' | 'unavailable'
export type Paimos6SessionReason = 'paimos_target' | 'active' | 'no_active_harness' | 'stale_harness' | 'ambiguous_harness' | 'ownership_mismatch'
export type Paimos6ManagementMode = 'managed' | 'unmanaged'
export type Paimos6SteerMode = 'direct' | 'paimos_nudge'
export type Paimos6AttentionReason = 'action_request' | 'sender_not_allowed'

export interface Paimos6AdvertisedCapabilities {
  inbox: boolean
  status: boolean
  steer: boolean
  interrupt: boolean
  stop: boolean
}

export interface Paimos6SessionHomeWire {
  schema_version: 1
  project_id: number
  sessions: Paimos6SessionWire[]
  totals: { sessions: number; unread: number; attention: number }
}

export interface Paimos6SessionWire {
  product_session_id: string
  title: string
  summary: string
  revision: number
  updated_at: string
  target: {
    kind: Paimos6TargetKind
    project_agent_id: number | null
    agent_name: string | null
    address: string | null
  }
  status: { phase: Paimos6SessionPhase; reason: Paimos6SessionReason }
  harness: {
    harness: string
    management_mode: Paimos6ManagementMode
    advertised_capabilities: Paimos6AdvertisedCapabilities
  } | null
  controls: { steer: Paimos6SteerMode; interrupt: boolean; stop: boolean }
  node: { node_id: number; node_key: string; label: string } | null
  inbox: { unread_count: number; latest_unread_at: string | null }
  attention: {
    required: boolean
    exception_count: number
    action_request_count: number
    reason: Paimos6AttentionReason | null
  }
}

export interface Paimos6SessionViewModel {
  id: string
  revision: number
  title: string
  summary: string
  agent: string
  status: Paimos6SessionPhase
  statusLabel: string
  attention: boolean
  attentionReason: string | null
  exceptionCount: number
  actionRequestCount: number
  node: { id: number; key: string; label: string } | null
  unread: number
  latestUnreadAt: string | null
  mode: Paimos6ManagementMode | 'unavailable' | 'paimos'
  harnessName: string | null
  advertisedCapabilities: Paimos6AdvertisedCapabilities | null
  capabilities: {
    directSteer: boolean
    interrupt: boolean
    stop: boolean
    paimosSteer: boolean
  }
}

type UnknownRecord = Record<string, unknown>

export class Paimos6SessionHomeContractError extends Error {
  constructor() {
    super('invalid Paimos 6 session-home contract')
    this.name = 'Paimos6SessionHomeContractError'
  }
}

const TARGET_KINDS = new Set<Paimos6TargetKind>(['paimos', 'project_agent'])
const PHASES = new Set<Paimos6SessionPhase>(['paimos', 'starting', 'working', 'yielded', 'stopping', 'unavailable'])
const REASONS = new Set<Paimos6SessionReason>(['paimos_target', 'active', 'no_active_harness', 'stale_harness', 'ambiguous_harness', 'ownership_mismatch'])
const MANAGEMENT_MODES = new Set<Paimos6ManagementMode>(['managed', 'unmanaged'])
const STEER_MODES = new Set<Paimos6SteerMode>(['direct', 'paimos_nudge'])
const ATTENTION_REASONS = new Set<Paimos6AttentionReason>(['action_request', 'sender_not_allowed'])
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/

function record(value: unknown): UnknownRecord | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as UnknownRecord
    : null
}

function exactKeys(value: UnknownRecord, keys: readonly string[]): boolean {
  const actual = Object.keys(value)
  return actual.length === keys.length && keys.every((key) => Object.prototype.hasOwnProperty.call(value, key))
}

function text(value: unknown): string | null {
  return typeof value === 'string' && !/[\u0000-\u001f\u007f]/.test(value) ? value : null
}

function nonEmptyText(value: unknown): string | null {
  const parsed = text(value)
  return parsed !== null && parsed.trim() !== '' ? parsed : null
}

function positiveInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 ? value : null
}

function nonNegativeInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null
}

function instant(value: unknown): string | null {
  return typeof value === 'string' && RFC3339.test(value) && !Number.isNaN(Date.parse(value)) ? value : null
}

function enumValue<T extends string>(value: unknown, values: ReadonlySet<T>): T | null {
  return typeof value === 'string' && values.has(value as T) ? value as T : null
}

export function parsePaimos6SessionRow(value: unknown): Paimos6SessionWire | null {
  const row = record(value)
  if (!row || !exactKeys(row, [
    'product_session_id', 'title', 'summary', 'revision', 'updated_at', 'target',
    'status', 'harness', 'controls', 'node', 'inbox', 'attention',
  ])) return null

  const id = typeof row.product_session_id === 'string' && UUID.test(row.product_session_id)
    ? row.product_session_id
    : null
  const title = nonEmptyText(row.title)
  const summary = text(row.summary)
  const revision = positiveInteger(row.revision)
  const updatedAt = instant(row.updated_at)

  const target = record(row.target)
  if (!target || !exactKeys(target, ['kind', 'project_agent_id', 'agent_name', 'address'])) return null
  const targetKind = enumValue(target.kind, TARGET_KINDS)
  const projectAgentId = target.project_agent_id === null ? null : positiveInteger(target.project_agent_id)
  const agentName = target.agent_name === null ? null : nonEmptyText(target.agent_name)
  const address = target.address === null ? null : nonEmptyText(target.address)

  const status = record(row.status)
  if (!status || !exactKeys(status, ['phase', 'reason'])) return null
  const phase = enumValue(status.phase, PHASES)
  const reason = enumValue(status.reason, REASONS)

  let harness: Paimos6SessionWire['harness'] = null
  if (row.harness !== null) {
    const rawHarness = record(row.harness)
    if (!rawHarness || !exactKeys(rawHarness, ['harness', 'management_mode', 'advertised_capabilities'])) return null
    const harnessName = nonEmptyText(rawHarness.harness)
    const managementMode = enumValue(rawHarness.management_mode, MANAGEMENT_MODES)
    const capabilities = record(rawHarness.advertised_capabilities)
    if (!capabilities || !exactKeys(capabilities, ['inbox', 'status', 'steer', 'interrupt', 'stop'])) return null
    if (!['inbox', 'status', 'steer', 'interrupt', 'stop'].every((key) => typeof capabilities[key] === 'boolean')) return null
    if (!harnessName || !managementMode) return null
    harness = {
      harness: harnessName,
      management_mode: managementMode,
      advertised_capabilities: {
        inbox: capabilities.inbox as boolean,
        status: capabilities.status as boolean,
        steer: capabilities.steer as boolean,
        interrupt: capabilities.interrupt as boolean,
        stop: capabilities.stop as boolean,
      },
    }
  }

  const controls = record(row.controls)
  if (!controls || !exactKeys(controls, ['steer', 'interrupt', 'stop'])) return null
  const steer = enumValue(controls.steer, STEER_MODES)
  if (!steer || typeof controls.interrupt !== 'boolean' || typeof controls.stop !== 'boolean') return null

  let node: Paimos6SessionWire['node'] = null
  if (row.node !== null) {
    const rawNode = record(row.node)
    if (!rawNode || !exactKeys(rawNode, ['node_id', 'node_key', 'label'])) return null
    const nodeId = positiveInteger(rawNode.node_id)
    const nodeKey = nonEmptyText(rawNode.node_key)
    const label = nonEmptyText(rawNode.label)
    if (!nodeId || !nodeKey || !label) return null
    node = { node_id: nodeId, node_key: nodeKey, label }
  }

  const inbox = record(row.inbox)
  if (!inbox || !exactKeys(inbox, ['unread_count', 'latest_unread_at'])) return null
  const unreadCount = nonNegativeInteger(inbox.unread_count)
  const latestUnreadAt = inbox.latest_unread_at === null ? null : instant(inbox.latest_unread_at)

  const attention = record(row.attention)
  if (!attention || !exactKeys(attention, ['required', 'exception_count', 'action_request_count', 'reason'])) return null
  const exceptionCount = nonNegativeInteger(attention.exception_count)
  const actionRequestCount = nonNegativeInteger(attention.action_request_count)
  const attentionReason = attention.reason === null ? null : enumValue(attention.reason, ATTENTION_REASONS)

  if (!id || !title || summary === null || !revision || !updatedAt || !targetKind || !phase || !reason
    || unreadCount === null || latestUnreadAt === null && inbox.latest_unread_at !== null
    || typeof attention.required !== 'boolean' || exceptionCount === null || actionRequestCount === null
    || attentionReason === null && attention.reason !== null) return null

  if (unreadCount === 0 ? latestUnreadAt !== null : latestUnreadAt === null) return null
  if (actionRequestCount > exceptionCount) return null
  if (attention.required !== (exceptionCount > 0)) return null
  if (exceptionCount === 0 ? attentionReason !== null : attentionReason === null) return null
  if (actionRequestCount > 0 && attentionReason !== 'action_request') return null
  if (exceptionCount > 0 && actionRequestCount === 0 && attentionReason !== 'sender_not_allowed') return null

  if (targetKind === 'paimos') {
    if (projectAgentId !== null || agentName !== null || address !== 'paimos' || harness !== null
      || phase !== 'paimos' || reason !== 'paimos_target' || steer !== 'paimos_nudge'
      || controls.interrupt || controls.stop) return null
  } else {
    if (!projectAgentId || !agentName) return null
    if (phase === 'paimos' || reason === 'paimos_target') return null
    if (harness === null && (address !== null || phase !== 'unavailable' || steer !== 'paimos_nudge'
      || controls.interrupt || controls.stop)) return null
    if (harness !== null && (address !== `${harness.harness}:${agentName}`
      || phase === 'unavailable' || reason !== 'active'
      || (steer === 'direct') !== harness.advertised_capabilities.steer)) return null
  }

  if (harness?.management_mode === 'unmanaged' && (controls.interrupt || controls.stop)) return null
  if (steer === 'direct' && (!harness || !harness.advertised_capabilities.steer || address === null)) return null
  if (controls.interrupt && (!harness || harness.management_mode !== 'managed' || !harness.advertised_capabilities.interrupt)) return null
  if (controls.stop && (!harness || harness.management_mode !== 'managed' || !harness.advertised_capabilities.stop)) return null

  return {
    product_session_id: id,
    title,
    summary,
    revision,
    updated_at: updatedAt,
    target: { kind: targetKind, project_agent_id: projectAgentId, agent_name: agentName, address },
    status: { phase, reason },
    harness,
    controls: { steer, interrupt: controls.interrupt, stop: controls.stop },
    node,
    inbox: { unread_count: unreadCount, latest_unread_at: latestUnreadAt },
    attention: {
      required: attention.required,
      exception_count: exceptionCount,
      action_request_count: actionRequestCount,
      reason: attentionReason,
    },
  }
}

export function parsePaimos6SessionHome(value: unknown, expectedProjectId: number): Paimos6SessionHomeWire {
  const root = record(value)
  if (!root || !exactKeys(root, ['schema_version', 'project_id', 'sessions', 'totals'])
    || root.schema_version !== 1 || root.project_id !== expectedProjectId || !Array.isArray(root.sessions)) {
    throw new Paimos6SessionHomeContractError()
  }

  const sessions: Paimos6SessionWire[] = []
  const seen = new Set<string>()
  for (const raw of root.sessions) {
    const session = parsePaimos6SessionRow(raw)
    if (!session || seen.has(session.product_session_id)) throw new Paimos6SessionHomeContractError()
    const previous = sessions[sessions.length - 1]
    if (previous) {
      const previousTime = Date.parse(previous.updated_at)
      const currentTime = Date.parse(session.updated_at)
      if (currentTime > previousTime
        || currentTime === previousTime && session.product_session_id < previous.product_session_id) {
        throw new Paimos6SessionHomeContractError()
      }
    }
    seen.add(session.product_session_id)
    sessions.push(session)
  }

  const totals = record(root.totals)
  if (!totals || !exactKeys(totals, ['sessions', 'unread', 'attention'])) throw new Paimos6SessionHomeContractError()
  const sessionTotal = nonNegativeInteger(totals.sessions)
  const unreadTotal = nonNegativeInteger(totals.unread)
  const attentionTotal = nonNegativeInteger(totals.attention)
  const inboxByTarget = new Map<string, Paimos6SessionWire['inbox']>()
  for (const session of sessions) {
    const targetKey = session.target.kind === 'paimos'
      ? 'paimos'
      : `project-agent:${session.target.project_agent_id}`
    const existing = inboxByTarget.get(targetKey)
    if (existing && (existing.unread_count !== session.inbox.unread_count
      || existing.latest_unread_at !== session.inbox.latest_unread_at)) {
      throw new Paimos6SessionHomeContractError()
    }
    inboxByTarget.set(targetKey, session.inbox)
  }
  const uniqueTargetUnread = [...inboxByTarget.values()]
    .reduce((sum, inbox) => sum + inbox.unread_count, 0)
  if (sessionTotal !== sessions.length
    || unreadTotal !== uniqueTargetUnread
    || attentionTotal !== sessions.filter((session) => session.attention.required).length) {
    throw new Paimos6SessionHomeContractError()
  }

  return {
    schema_version: 1,
    project_id: expectedProjectId,
    sessions,
    totals: { sessions: sessionTotal, unread: unreadTotal, attention: attentionTotal },
  }
}

const STATUS_LABELS: Record<Paimos6SessionPhase, string> = {
  paimos: 'Paimos target',
  starting: 'Starting',
  working: 'Working',
  yielded: 'Yielded',
  stopping: 'Stopping',
  unavailable: 'Unavailable',
}

const REASON_LABELS: Record<Paimos6SessionReason, string> = {
  paimos_target: 'Paimos-owned target',
  active: 'active harness',
  no_active_harness: 'no active harness',
  stale_harness: 'stale harness',
  ambiguous_harness: 'ambiguous harness',
  ownership_mismatch: 'ownership mismatch',
}

export function toPaimos6SessionViewModel(session: Paimos6SessionWire): Paimos6SessionViewModel {
  const attentionReason = session.attention.reason === 'action_request'
    ? `${session.attention.action_request_count} action request${session.attention.action_request_count === 1 ? '' : 's'} held for you`
    : session.attention.reason === 'sender_not_allowed'
      ? `${session.attention.exception_count} sender exception${session.attention.exception_count === 1 ? '' : 's'} need attention`
      : null
  return {
    id: session.product_session_id,
    revision: session.revision,
    title: session.title,
    summary: session.summary,
    agent: session.target.kind === 'paimos' ? 'Paimos' : session.target.address ?? 'Unavailable target',
    status: session.status.phase,
    statusLabel: `${STATUS_LABELS[session.status.phase]} · ${REASON_LABELS[session.status.reason]}`,
    attention: session.attention.required,
    attentionReason,
    exceptionCount: session.attention.exception_count,
    actionRequestCount: session.attention.action_request_count,
    node: session.node ? { id: session.node.node_id, key: session.node.node_key, label: session.node.label } : null,
    unread: session.inbox.unread_count,
    latestUnreadAt: session.inbox.latest_unread_at,
    mode: session.target.kind === 'paimos'
      ? 'paimos'
      : session.harness?.management_mode ?? 'unavailable',
    harnessName: session.harness?.harness ?? null,
    advertisedCapabilities: session.harness?.advertised_capabilities ?? null,
    capabilities: {
      directSteer: session.controls.steer === 'direct',
      interrupt: session.controls.interrupt,
      stop: session.controls.stop,
      paimosSteer: session.controls.steer === 'paimos_nudge',
    },
  }
}

export async function loadPaimos6SessionHome(projectId: number, signal?: AbortSignal) {
  const wire = await api.get<unknown>(`/projects/${projectId}/session-home/v1`, { signal })
  const parsed = parsePaimos6SessionHome(wire, projectId)
  return {
    projectId: parsed.project_id,
    sessions: parsed.sessions.map(toPaimos6SessionViewModel),
    totals: parsed.totals,
  }
}
