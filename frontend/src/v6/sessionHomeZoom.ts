/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-864 — strict, bounded semantic-zoom session-home boundary.
 */

import { api } from '@/api/client'
import {
  parsePaimos6SessionRow,
  toPaimos6SessionViewModel,
  type Paimos6SessionViewModel,
  type Paimos6SessionWire,
} from '@/v6/sessionHome'

export type Paimos6ZoomBand = 'detail' | 'overview' | 'aggregate' | 'far'

export interface Paimos6SessionZoomTotals {
  sessions: number
  unread: number
  attention_sessions: number
  exception_messages: number
  action_requests: number
  exception_targets: number
  sampled_exception_targets: number
}

export interface Paimos6SessionZoomWire {
  schema_version: 2
  project_id: number
  zoom: string
  band: Paimos6ZoomBand
  sample_limit: number
  sample_truncated: boolean
  sessions: Paimos6SessionWire[]
  selected_session: Paimos6SessionWire | null
  totals: Paimos6SessionZoomTotals
}

export interface Paimos6SessionZoomProjection {
  projectId: number
  zoom: string
  band: Paimos6ZoomBand
  sampleLimit: number
  sampleTruncated: boolean
  sessions: Paimos6SessionViewModel[]
  selectedSession: Paimos6SessionViewModel | null
  totals: Paimos6SessionZoomTotals
}

type UnknownRecord = Record<string, unknown>

const CANONICAL_ZOOM = /^[1-9]\d{0,63}$/
const CANONICAL_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const BANDS = new Set<Paimos6ZoomBand>(['detail', 'overview', 'aggregate', 'far'])

export class Paimos6SessionZoomContractError extends Error {
  constructor() {
    super('invalid Paimos 6 semantic-zoom contract')
    this.name = 'Paimos6SessionZoomContractError'
  }
}

function record(value: unknown): UnknownRecord | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as UnknownRecord
    : null
}

function exactKeys(value: UnknownRecord, keys: readonly string[]): boolean {
  const actual = Object.keys(value)
  return actual.length === keys.length && keys.every((key) => Object.prototype.hasOwnProperty.call(value, key))
}

function nonNegativeInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null
}

export function isCanonicalPaimos6Zoom(value: unknown): value is string {
  return typeof value === 'string' && CANONICAL_ZOOM.test(value)
}

export function isCanonicalPaimos6SessionId(value: unknown): value is string {
  return typeof value === 'string' && CANONICAL_UUID.test(value)
}

export function paimos6ZoomBand(zoom: string): Paimos6ZoomBand {
  if (!isCanonicalPaimos6Zoom(zoom)) throw new Paimos6SessionZoomContractError()
  if (zoom.length < 2) return 'detail'
  if (zoom.length < 3) return 'overview'
  if (zoom.length < 4) return 'aggregate'
  return 'far'
}

export function paimos6ZoomSampleLimit(zoom: string): number {
  const band = paimos6ZoomBand(zoom)
  if (band === 'aggregate' || band === 'far') return 100
  const first = zoom.charCodeAt(0) - 48
  return zoom.length === 1 ? first : first * 10 + zoom.charCodeAt(1) - 48
}

export function incrementPaimos6Zoom(zoom: string): string {
  if (!isCanonicalPaimos6Zoom(zoom)) return '10'
  const digits = zoom.split('')
  let carry = 1
  for (let index = digits.length - 1; index >= 0 && carry === 1; index -= 1) {
    const next = digits[index].charCodeAt(0) - 48 + carry
    digits[index] = String(next % 10)
    carry = next === 10 ? 1 : 0
  }
  if (carry === 1) digits.unshift('1')
  const result = digits.join('')
  return result.length <= 64 ? result : zoom
}

export function decrementPaimos6Zoom(zoom: string): string {
  if (!isCanonicalPaimos6Zoom(zoom) || zoom === '1') return '1'
  const digits = zoom.split('')
  let borrow = 1
  for (let index = digits.length - 1; index >= 0 && borrow === 1; index -= 1) {
    const next = digits[index].charCodeAt(0) - 48 - borrow
    digits[index] = String(next < 0 ? 9 : next)
    borrow = next < 0 ? 1 : 0
  }
  if (digits[0] === '0') digits.shift()
  return digits.join('')
}

export function canonicalPaimos6Zoom(raw: unknown): { zoom: string; replace: boolean } {
  if (raw === undefined) return { zoom: '10', replace: false }
  if (isCanonicalPaimos6Zoom(raw)) return { zoom: raw, replace: false }
  return { zoom: '10', replace: true }
}

function sameWireRow(left: Paimos6SessionWire, right: Paimos6SessionWire): boolean {
  return JSON.stringify(left) === JSON.stringify(right)
}

function targetKey(session: Paimos6SessionWire): string {
  return session.target.kind === 'paimos' ? 'paimos' : `project-agent:${session.target.project_agent_id}`
}

export function parsePaimos6SessionZoom(
  value: unknown,
  expectedProjectId: number,
  expectedZoom: string,
  expectedSelectedSessionId: string | null,
): Paimos6SessionZoomWire {
  if (!isCanonicalPaimos6Zoom(expectedZoom)
    || expectedSelectedSessionId !== null && !isCanonicalPaimos6SessionId(expectedSelectedSessionId)) {
    throw new Paimos6SessionZoomContractError()
  }

  const root = record(value)
  if (!root || !exactKeys(root, [
    'schema_version', 'project_id', 'zoom', 'band', 'sample_limit', 'sample_truncated',
    'sessions', 'selected_session', 'totals',
  ])
    || root.schema_version !== 2
    || root.project_id !== expectedProjectId
    || root.zoom !== expectedZoom
    || typeof root.band !== 'string'
    || !BANDS.has(root.band as Paimos6ZoomBand)
    || root.band !== paimos6ZoomBand(expectedZoom)
    || root.sample_limit !== paimos6ZoomSampleLimit(expectedZoom)
    || typeof root.sample_truncated !== 'boolean'
    || !Array.isArray(root.sessions)) {
    throw new Paimos6SessionZoomContractError()
  }

  const sessions: Paimos6SessionWire[] = []
  const seen = new Set<string>()
  for (const raw of root.sessions) {
    const session = parsePaimos6SessionRow(raw)
    if (!session || seen.has(session.product_session_id)) throw new Paimos6SessionZoomContractError()
    seen.add(session.product_session_id)
    sessions.push(session)
  }

  let selectedSession: Paimos6SessionWire | null = null
  if (root.selected_session !== null) {
    selectedSession = parsePaimos6SessionRow(root.selected_session)
    if (!selectedSession) throw new Paimos6SessionZoomContractError()
  }
  if (expectedSelectedSessionId === null ? selectedSession !== null : selectedSession?.product_session_id !== expectedSelectedSessionId) {
    throw new Paimos6SessionZoomContractError()
  }
  if (selectedSession && seen.has(selectedSession.product_session_id)) {
    const sampled = sessions.find((session) => session.product_session_id === selectedSession?.product_session_id)
    if (!sampled || !sameWireRow(sampled, selectedSession)) throw new Paimos6SessionZoomContractError()
  }

  const rawTotals = record(root.totals)
  if (!rawTotals || !exactKeys(rawTotals, [
    'sessions', 'unread', 'attention_sessions', 'exception_messages', 'action_requests',
    'exception_targets', 'sampled_exception_targets',
  ])) throw new Paimos6SessionZoomContractError()

  const totals = {
    sessions: nonNegativeInteger(rawTotals.sessions),
    unread: nonNegativeInteger(rawTotals.unread),
    attention_sessions: nonNegativeInteger(rawTotals.attention_sessions),
    exception_messages: nonNegativeInteger(rawTotals.exception_messages),
    action_requests: nonNegativeInteger(rawTotals.action_requests),
    exception_targets: nonNegativeInteger(rawTotals.exception_targets),
    sampled_exception_targets: nonNegativeInteger(rawTotals.sampled_exception_targets),
  }
  if (Object.values(totals).some((count) => count === null)) throw new Paimos6SessionZoomContractError()
  const parsedTotals = totals as Paimos6SessionZoomTotals
  const sampleLimit = root.sample_limit as number
  const truncated = parsedTotals.sessions > sessions.length

  const sampleTargetFacts = new Map<string, Paimos6SessionWire>()
  for (const session of sessions) {
    if (!sampleTargetFacts.has(targetKey(session))) sampleTargetFacts.set(targetKey(session), session)
  }
  const visibleSessions = selectedSession && !seen.has(selectedSession.product_session_id)
    ? [...sessions, selectedSession]
    : sessions
  const targetFacts = new Map<string, Paimos6SessionWire>()
  for (const session of visibleSessions) {
    if (session.target.kind === 'paimos' && (session.inbox.unread_count !== 0
      || session.inbox.latest_unread_at !== null
      || session.attention.required
      || session.attention.exception_count !== 0
      || session.attention.action_request_count !== 0
      || session.attention.reason !== null)) {
      throw new Paimos6SessionZoomContractError()
    }
    const key = targetKey(session)
    const existing = targetFacts.get(key)
    if (existing && (existing.inbox.unread_count !== session.inbox.unread_count
      || existing.inbox.latest_unread_at !== session.inbox.latest_unread_at
      || existing.attention.required !== session.attention.required
      || existing.attention.exception_count !== session.attention.exception_count
      || existing.attention.action_request_count !== session.attention.action_request_count
      || existing.attention.reason !== session.attention.reason)) {
      throw new Paimos6SessionZoomContractError()
    }
    targetFacts.set(key, session)
  }
  const sampledExceptionalTargets = [...sampleTargetFacts.values()]
    .filter((session) => session.target.kind === 'project_agent' && session.attention.required)
  const visibleExceptionalTargets = [...targetFacts.values()]
    .filter((session) => session.target.kind === 'project_agent' && session.attention.required)
  const visibleUnread = [...targetFacts.values()].reduce((sum, session) => sum + session.inbox.unread_count, 0)
  const visibleExceptionMessages = visibleExceptionalTargets.reduce((sum, session) => sum + session.attention.exception_count, 0)
  const visibleActionRequests = visibleExceptionalTargets.reduce((sum, session) => sum + session.attention.action_request_count, 0)
  const visibleAttentionSessions = visibleSessions.filter((session) => session.attention.required).length

  if (sessions.length > sampleLimit
    || parsedTotals.sessions < sessions.length
    || parsedTotals.sessions < visibleSessions.length
    || root.sample_truncated !== truncated
    || parsedTotals.sampled_exception_targets !== sampledExceptionalTargets.length
    || parsedTotals.unread < visibleUnread
    || parsedTotals.attention_sessions < visibleAttentionSessions
    || parsedTotals.exception_messages < visibleExceptionMessages
    || parsedTotals.action_requests < visibleActionRequests
    || parsedTotals.action_requests > parsedTotals.exception_messages
    || parsedTotals.attention_sessions > parsedTotals.sessions
    || parsedTotals.exception_targets > parsedTotals.sessions
    || parsedTotals.exception_targets < visibleExceptionalTargets.length
    || parsedTotals.exception_targets > parsedTotals.attention_sessions
    || parsedTotals.exception_targets > parsedTotals.exception_messages
    || parsedTotals.sampled_exception_targets > parsedTotals.exception_targets) {
    throw new Paimos6SessionZoomContractError()
  }
  if (parsedTotals.sessions === 0 && (selectedSession !== null || Object.values(parsedTotals).some((count) => count !== 0))) {
    throw new Paimos6SessionZoomContractError()
  }

  return {
    schema_version: 2,
    project_id: expectedProjectId,
    zoom: expectedZoom,
    band: root.band as Paimos6ZoomBand,
    sample_limit: sampleLimit,
    sample_truncated: root.sample_truncated,
    sessions,
    selected_session: selectedSession,
    totals: parsedTotals,
  }
}

export async function loadPaimos6SessionZoom(
  projectId: number,
  zoom: string,
  selectedSessionId: string | null,
  signal?: AbortSignal,
): Promise<Paimos6SessionZoomProjection> {
  if (!isCanonicalPaimos6Zoom(zoom)
    || selectedSessionId !== null && !isCanonicalPaimos6SessionId(selectedSessionId)) {
    throw new Paimos6SessionZoomContractError()
  }
  const selectedQuery = selectedSessionId === null
    ? ''
    : `&selected_session_id=${encodeURIComponent(selectedSessionId)}`
  const wire = await api.get<unknown>(
    `/projects/${projectId}/session-home/zoom/v1?zoom=${encodeURIComponent(zoom)}${selectedQuery}`,
    { signal },
  )
  const parsed = parsePaimos6SessionZoom(wire, projectId, zoom, selectedSessionId)
  return {
    projectId: parsed.project_id,
    zoom: parsed.zoom,
    band: parsed.band,
    sampleLimit: parsed.sample_limit,
    sampleTruncated: parsed.sample_truncated,
    sessions: parsed.sessions.map(toPaimos6SessionViewModel),
    selectedSession: parsed.selected_session === null ? null : toPaimos6SessionViewModel(parsed.selected_session),
    totals: parsed.totals,
  }
}
