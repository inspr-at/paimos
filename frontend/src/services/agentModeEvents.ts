/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

// PAI-804 — dedicated Agent Mode invalidation stream. Event payloads are
// scheduling hints only; this module never patches snapshot truth.

import {
  buildAgentModeFilterParams,
  isExactAgentModeDeliveryKey,
  isAgentModeCursor,
  type AgentModeSnapshotQuery,
} from './agentModeTransport'

export const AGENT_MODE_EVENTS_PATH = '/api/agent-mode/deliveries/events'
export const AGENT_MODE_EVENT_HINT_LIMIT = 512

export interface AgentModeMessageEvent {
  data: unknown
  lastEventId: string
}

export interface AgentModeEventSourceLike {
  addEventListener(type: string, listener: (event: AgentModeMessageEvent) => void): void
  onerror: ((event: unknown) => void) | null
  /** Native EventSource state: 0 CONNECTING, 1 OPEN, 2 CLOSED. */
  readonly readyState?: number
  close(): void
}

export type AgentModeEventSourceFactory = (url: string) => AgentModeEventSourceLike

export interface AgentModeStreamHint {
  deliveryId: string
  deliveryRevision: number
  changeSequence: number
}

export interface AgentModeEventStreamCallbacks {
  onOpen(): void
  onRefetch(hints: readonly AgentModeStreamHint[]): void
  onCheckpoint(): void
  onReset(): void
  onInvalid(): void
  onError(readyState: number | undefined): void
}

export interface AgentModeEventStream {
  readonly url: string
  close(): void
}

type UnknownRecord = Record<string, unknown>

function record(value: unknown): UnknownRecord | null {
  return value != null && typeof value === 'object' && !Array.isArray(value)
    ? value as UnknownRecord
    : null
}

function exactKeys(value: UnknownRecord, expected: readonly string[]): boolean {
  const actual = Object.keys(value).sort()
  const wanted = [...expected].sort()
  return actual.length === wanted.length && actual.every((key, index) => key === wanted[index])
}

function parseJSON(data: unknown): UnknownRecord | null {
  if (typeof data !== 'string') return null
  try {
    return record(JSON.parse(data))
  } catch {
    return null
  }
}

function parseHints(value: unknown): AgentModeStreamHint[] | null {
  if (!Array.isArray(value) || value.length > AGENT_MODE_EVENT_HINT_LIMIT) return null
  const hints: AgentModeStreamHint[] = []
  const seen = new Set<string>()
  for (const raw of value) {
    const source = record(raw)
    if (!source || !exactKeys(source, ['delivery_id', 'delivery_revision', 'change_sequence'])) return null
    const deliveryId = isExactAgentModeDeliveryKey(source.delivery_id) ? source.delivery_id : null
    const deliveryRevision = typeof source.delivery_revision === 'number'
      && Number.isSafeInteger(source.delivery_revision)
      && source.delivery_revision >= 0
      ? source.delivery_revision
      : null
    const changeSequence = typeof source.change_sequence === 'number'
      && Number.isSafeInteger(source.change_sequence)
      && source.change_sequence > 0
      ? source.change_sequence
      : null
    if (!deliveryId || deliveryRevision == null || changeSequence == null || seen.has(deliveryId)) return null
    seen.add(deliveryId)
    hints.push({ deliveryId, deliveryRevision, changeSequence })
  }
  return hints
}

function parseBatch(event: AgentModeMessageEvent): AgentModeStreamHint[] | null {
  if (!isAgentModeCursor(event.lastEventId)) return null
  const source = parseJSON(event.data)
  if (!source) return null
  const hasHints = Object.prototype.hasOwnProperty.call(source, 'hints')
  if (!exactKeys(source, hasHints ? ['schema_version', 'hints'] : ['schema_version']) || source.schema_version !== 1) {
    return null
  }
  return hasHints ? parseHints(source.hints) : []
}

function validReset(event: AgentModeMessageEvent): boolean {
  // A reset frame has no `id:` line, but native EventSource carries the last
  // observed id into MessageEvent.lastEventId. Therefore only its generic
  // identity-free payload is validated here.
  const source = parseJSON(event.data)
  return !!source
    && exactKeys(source, ['schema_version', 'reason'])
    && source.schema_version === 1
    && source.reason === 'resync_required'
}

export function agentModeStreamBindingKey(query: AgentModeSnapshotQuery = {}): string {
  const params = buildAgentModeFilterParams(query, false)
  const queryString = params.toString()
  return queryString ? `${AGENT_MODE_EVENTS_PATH}?${queryString}` : AGENT_MODE_EVENTS_PATH
}

export function buildAgentModeEventsURL(query: AgentModeSnapshotQuery, cursor: string): string {
  if (!isAgentModeCursor(cursor)) throw new Error('invalid Agent Mode stream cursor')
  const params = buildAgentModeFilterParams(query, false)
  params.set('cursor', cursor)
  return `${AGENT_MODE_EVENTS_PATH}?${params.toString()}`
}

const nativeEventSourceFactory: AgentModeEventSourceFactory = (url) => {
  if (typeof EventSource === 'undefined') throw new Error('EventSource unavailable')
  return new EventSource(url) as unknown as AgentModeEventSourceLike
}

export function openAgentModeEventStream(
  query: AgentModeSnapshotQuery,
  cursor: string,
  callbacks: AgentModeEventStreamCallbacks,
  factory: AgentModeEventSourceFactory = nativeEventSourceFactory,
): AgentModeEventStream {
  const url = buildAgentModeEventsURL(query, cursor)
  const source = factory(url)
  let closed = false
  const active = (callback: () => void) => {
    if (!closed) callback()
  }
  source.addEventListener('open', () => active(callbacks.onOpen))
  source.addEventListener('refetch', (event) => active(() => {
    const hints = parseBatch(event)
    if (hints === null) callbacks.onInvalid()
    else callbacks.onRefetch(hints)
  }))
  source.addEventListener('checkpoint', (event) => active(() => {
    if (parseBatch(event) === null) callbacks.onInvalid()
    else callbacks.onCheckpoint()
  }))
  source.addEventListener('reset', (event) => active(() => {
    if (!validReset(event)) callbacks.onInvalid()
    else callbacks.onReset()
  }))
  // Default `message` is unsupported. Unknown named events have no listener
  // and are ignored exactly as they are by native EventSource.
  source.addEventListener('message', () => active(callbacks.onInvalid))
  source.onerror = () => active(() => callbacks.onError(source.readyState))
  return {
    url,
    close() {
      if (closed) return
      closed = true
      source.close()
    },
  }
}
