/* PAI-862 — strict Paimos 6 session-utterance boundary. */

import {
  ApiError,
  SessionExpiredError,
  announceSessionExpired,
  capturePermissionsEpochGeneration,
  comparePermissionsEpoch,
  csrfHeaders,
  isPermissionsEpochGenerationCurrent,
  observePermissionsEpochHeader,
  parsePermissionsEpochHeader,
  permissionsEpoch,
  sessionExpiresAt,
} from '@/api/client'

export type Paimos6UtteranceRouteKind = 'project_agent' | 'paimos'

export interface Paimos6SelectedSession {
  productSessionId: string
  revision: number
}

export interface Paimos6SessionUtteranceRequest {
  projectId: number
  utteranceId: string
  text: string
  selectedSession: Paimos6SelectedSession | null
}

export interface Paimos6SessionUtteranceResult {
  utteranceId: string
  routeKind: Paimos6UtteranceRouteKind
  productSessionId: string
  productSessionRevision: number
  messageId: string
  threadId: string
  deliveryId: string | null
  createdAt: string
}

export type Paimos6SessionUtteranceProblemCode =
  | 'session_utterance_request_invalid'
  | 'session_utterance_id_invalid'
  | 'session_utterance_text_invalid'
  | 'session_utterance_payload_too_large'
  | 'session_utterance_idempotency_conflict'
  | 'session_utterance_selection_stale'
  | 'session_utterance_target_unavailable'
  | 'session_utterance_not_found'
  | 'session_utterance_write_failed'

type UnknownRecord = Record<string, unknown>

const RESPONSE_FIELDS = [
  'schema_version',
  'utterance_id',
  'route_kind',
  'product_session_id',
  'product_session_revision',
  'message_id',
  'thread_id',
  'delivery_id',
  'created_at',
] as const
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const UTTERANCE_ID = /^utt_[0-9a-f]{32}$/
const UTC_RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/u
const PROBLEM_CODES = new Set<Paimos6SessionUtteranceProblemCode>([
  'session_utterance_request_invalid',
  'session_utterance_id_invalid',
  'session_utterance_text_invalid',
  'session_utterance_payload_too_large',
  'session_utterance_idempotency_conflict',
  'session_utterance_selection_stale',
  'session_utterance_target_unavailable',
  'session_utterance_not_found',
  'session_utterance_write_failed',
])

function record(value: unknown): UnknownRecord | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as UnknownRecord
    : null
}

function exactKeys(value: UnknownRecord, expected: readonly string[]): boolean {
  const actual = Object.keys(value)
  return actual.length === expected.length
    && expected.every((key) => Object.prototype.hasOwnProperty.call(value, key))
}

function positiveInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

function baseMediaType(value: string): string {
  return value.split(';', 1)[0].trim().toLowerCase()
}

function requirePrivateNoStore(response: Response) {
  const directives = new Set((response.headers.get('Cache-Control') ?? '')
    .toLowerCase()
    .split(',')
    .map((directive) => directive.trim()))
  if (!directives.has('private') || !directives.has('no-store')) {
    throw new ApiError(0, 'Paimos 6 utterance response failed its privacy contract')
  }
}

function observeAuthority(response: Response, required: boolean): string | null {
  const expiry = response.headers.get('X-Session-Expires-At')
  if (expiry) {
    const parsed = new Date(expiry)
    if (!Number.isNaN(parsed.valueOf())) sessionExpiresAt.value = parsed
  }
  const raw = response.headers.get('X-Permissions-Epoch')
  if (raw == null) {
    if (required) throw new ApiError(response.status, 'Paimos 6 utterance response is missing its epoch')
    return null
  }
  const next = parsePermissionsEpochHeader(raw)
  if (next == null) throw new ApiError(response.status, 'Paimos 6 utterance response returned an invalid epoch')
  if (permissionsEpoch.value != null && comparePermissionsEpoch(next, permissionsEpoch.value) < 0) {
    throw new ApiError(response.status, 'Paimos 6 utterance response returned a stale epoch')
  }
  observePermissionsEpochHeader(raw)
  return next
}

function requireCurrentAuthority(generation: number, responseEpoch: string | null) {
  if (!isPermissionsEpochGenerationCurrent(generation)) {
    throw new ApiError(0, 'Paimos 6 utterance request was superseded by an authentication change')
  }
  if (responseEpoch != null && permissionsEpoch.value != null
    && comparePermissionsEpoch(responseEpoch, permissionsEpoch.value) < 0) {
    throw new ApiError(0, 'Paimos 6 utterance response was superseded by a newer permissions epoch')
  }
}

async function problemError(response: Response): Promise<ApiError> {
  const error = new ApiError(response.status, 'Paimos 6 utterance delivery failed')
  if (baseMediaType(response.headers.get('Content-Type') ?? '') !== 'application/problem+json') return error
  try {
    const body = record(await response.json())
    if (body && typeof body.code === 'string' && PROBLEM_CODES.has(body.code as Paimos6SessionUtteranceProblemCode)) {
      error.code = body.code
    }
  } catch {
    // Outer gates may intentionally return a different Problem Details shape.
  }
  return error
}

function validateRequest(request: Paimos6SessionUtteranceRequest) {
  if (!positiveInteger(request.projectId) || !UTTERANCE_ID.test(request.utteranceId)
    || request.text === '' || request.text !== request.text.trim() || CONTROL.test(request.text)
    || new TextEncoder().encode(request.text).byteLength > 8_192) {
    throw new ApiError(400, 'Invalid Paimos 6 utterance request')
  }
  if (request.selectedSession && (!UUID.test(request.selectedSession.productSessionId)
    || !positiveInteger(request.selectedSession.revision))) {
    throw new ApiError(400, 'Invalid Paimos 6 selected session')
  }
}

function parseResult(
  value: unknown,
  request: Paimos6SessionUtteranceRequest,
): Paimos6SessionUtteranceResult | null {
  const wire = record(value)
  if (!wire || !exactKeys(wire, RESPONSE_FIELDS) || wire.schema_version !== 1
    || wire.utterance_id !== request.utteranceId
    || (wire.route_kind !== 'project_agent' && wire.route_kind !== 'paimos')
    || typeof wire.product_session_id !== 'string' || !UUID.test(wire.product_session_id)
    || !positiveInteger(wire.product_session_revision)
    || typeof wire.message_id !== 'string' || !UUID.test(wire.message_id)
    || typeof wire.thread_id !== 'string' || !UUID.test(wire.thread_id)
    || wire.thread_id !== wire.product_session_id
    || typeof wire.created_at !== 'string' || !UTC_RFC3339.test(wire.created_at)
    || Number.isNaN(Date.parse(wire.created_at))) return null

  const deliveryId = wire.delivery_id
  if (wire.route_kind === 'project_agent') {
    if (typeof deliveryId !== 'string' || !UUID.test(deliveryId)) return null
  } else if (deliveryId !== null) return null

  if (request.selectedSession === null) {
    if (wire.route_kind !== 'paimos') return null
  } else if (wire.route_kind !== 'project_agent'
    || wire.product_session_id !== request.selectedSession.productSessionId
    || wire.product_session_revision !== request.selectedSession.revision) return null

  return {
    utteranceId: wire.utterance_id as string,
    routeKind: wire.route_kind,
    productSessionId: wire.product_session_id,
    productSessionRevision: wire.product_session_revision,
    messageId: wire.message_id,
    threadId: wire.thread_id,
    deliveryId: deliveryId as string | null,
    createdAt: wire.created_at,
  }
}

export async function sendPaimos6SessionUtterance(
  request: Paimos6SessionUtteranceRequest,
  signal?: AbortSignal,
): Promise<Paimos6SessionUtteranceResult> {
  validateRequest(request)
  const generation = capturePermissionsEpochGeneration()
  const response = await fetch(`/api/projects/${request.projectId}/session-utterances/v1`, {
    method: 'POST',
    cache: 'no-store',
    credentials: 'same-origin',
    headers: csrfHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({
      schema_version: 1,
      utterance_id: request.utteranceId,
      text: request.text,
      selected_session: request.selectedSession === null ? null : {
        product_session_id: request.selectedSession.productSessionId,
        revision: request.selectedSession.revision,
      },
    }),
    signal,
  })
  if (!isPermissionsEpochGenerationCurrent(generation)) {
    throw new ApiError(0, 'Paimos 6 utterance request was superseded by an authentication change')
  }
  if (response.status === 401) {
    announceSessionExpired()
    throw new SessionExpiredError()
  }
  const responseEpoch = observeAuthority(response, response.status === 201)
  requirePrivateNoStore(response)
  if (response.status !== 201) throw await problemError(response)
  if (baseMediaType(response.headers.get('Content-Type') ?? '') !== 'application/json') {
    throw new ApiError(response.status, 'Paimos 6 utterance returned an invalid response')
  }
  let raw: unknown
  try {
    raw = await response.json()
  } catch {
    throw new ApiError(response.status, 'Paimos 6 utterance returned an invalid response')
  }
  requireCurrentAuthority(generation, responseEpoch)
  const parsed = parseResult(raw, request)
  if (!parsed) throw new ApiError(response.status, 'Paimos 6 utterance returned an invalid response')
  return parsed
}
