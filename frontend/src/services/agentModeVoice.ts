/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 */

// PAI-808 — privacy-safe Agent Mode voice transport.
//
// Transcript and note bodies live only in request/response memory. This
// module deliberately has no storage, analytics, logging, URL-state, or
// caller-text TTS seam. Speech requests are serialized by the frozen pure
// narration contract, field by field.

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
import {
  toSpeechWire,
  type NarrationLocale,
  type NarrationSpeechRequest,
} from '@/components/agent-mode/agentModeNarration'
import {
  createIssueComment,
  type IssueComment,
} from '@/services/issueComments'
import type { VoiceProjectRef } from '@/composables/agent-mode/agentModeVoiceIntent'

export type AgentModeVoiceLanguage = NarrationLocale

export interface AgentModeVoiceTranscription {
  utteranceId: string
  text: string
  final: true
}

export interface AgentModeInternalNoteRequest {
  issueId: number
  body: string
  clientRequestId: string
}

export const AGENT_MODE_TRANSCRIPTION_WIRE_FIELDS = ['utterance_id', 'text', 'final'] as const

const TRANSCRIPTION_ID = /^utt_[0-9a-f]{32}$/
const ALLOWED_AUDIO_TYPES = new Set([
  'audio/webm',
  'video/webm',
  'audio/mp4',
  'video/mp4',
  'audio/mpeg',
  'audio/wav',
  'audio/x-wav',
  'audio/ogg',
])
const MAX_TRANSCRIPTION_AUDIO_BYTES = 12 * 1024 * 1024
const voiceResponseAuthority = new WeakMap<Response, {
  generation: number
  permissionsEpoch: string | null
}>()

function baseMediaType(value: string): string {
  return value.split(';', 1)[0].trim().toLowerCase()
}

function exactKeys(value: Record<string, unknown>, expected: readonly string[]): boolean {
  const actual = Object.keys(value).sort()
  const wanted = [...expected].sort()
  return actual.length === wanted.length && actual.every((key, index) => key === wanted[index])
}

function requirePrivateNoStore(response: Response) {
  const directives = new Set((response.headers.get('Cache-Control') ?? '')
    .toLowerCase()
    .split(',')
    .map((directive) => directive.trim()))
  if (!directives.has('private') || !directives.has('no-store')) {
    throw new ApiError(0, 'Agent Mode voice response failed its privacy contract')
  }
}

function handleSession(response: Response) {
  if (response.status !== 401) return
  announceSessionExpired()
  throw new SessionExpiredError()
}

function observePermissionsEpoch(response: Response, required: boolean): string | null {
  const expiry = response.headers.get('X-Session-Expires-At')
  if (expiry) {
    const parsed = new Date(expiry)
    if (!Number.isNaN(parsed.valueOf())) sessionExpiresAt.value = parsed
  }
  const raw = response.headers.get('X-Permissions-Epoch')
  if (raw == null) {
    if (required) throw new ApiError(response.status, 'Voice authority response is missing its epoch')
    return null
  }
  const next = parsePermissionsEpochHeader(raw)
  if (next == null) {
    throw new ApiError(response.status, 'Voice authority response returned an invalid epoch')
  }
  if (permissionsEpoch.value != null && comparePermissionsEpoch(next, permissionsEpoch.value) < 0) {
    throw new ApiError(response.status, 'Voice authority response returned a stale epoch')
  }
  observePermissionsEpochHeader(raw)
  return next
}

async function voiceFetch(path: string, init: RequestInit, failureMessage: string): Promise<Response> {
  const epochGeneration = capturePermissionsEpochGeneration()
  const response = await fetch(`/api${path}`, {
    ...init,
    cache: 'no-store',
    credentials: 'same-origin',
  })
  if (!isPermissionsEpochGenerationCurrent(epochGeneration)) {
    throw new ApiError(0, 'Voice request was superseded by an authentication change')
  }
  handleSession(response)
  const responseEpoch = observePermissionsEpoch(response, response.ok)
  requirePrivateNoStore(response)
  if (!response.ok) throw new ApiError(response.status, failureMessage)
  voiceResponseAuthority.set(response, {
    generation: epochGeneration,
    permissionsEpoch: responseEpoch,
  })
  return response
}

function requireCurrentVoiceResponse(response: Response) {
  const authority = voiceResponseAuthority.get(response)
  if (!authority || !isPermissionsEpochGenerationCurrent(authority.generation)) {
    throw new ApiError(0, 'Voice request was superseded by an authentication change')
  }
  if (
    authority.permissionsEpoch != null
    && permissionsEpoch.value != null
    && comparePermissionsEpoch(authority.permissionsEpoch, permissionsEpoch.value) < 0
  ) throw new ApiError(0, 'Voice response was superseded by a newer permissions epoch')
}

function parseTranscription(value: unknown): AgentModeVoiceTranscription | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const wire = value as Record<string, unknown>
  if (!exactKeys(wire, AGENT_MODE_TRANSCRIPTION_WIRE_FIELDS)) return null
  if (typeof wire.utterance_id !== 'string' || !TRANSCRIPTION_ID.test(wire.utterance_id)) return null
  if (
    typeof wire.text !== 'string'
    || wire.text === ''
    || wire.text !== wire.text.trim()
    || new TextEncoder().encode(wire.text).length > 8_192
  ) return null
  if (wire.final !== true) return null
  return { utteranceId: wire.utterance_id, text: wire.text, final: true }
}

/** Sends one bounded browser-recorded utterance. The current provider is
 * batch-final; accepting a partial response would be a false runtime claim. */
export async function transcribeAgentModeAudio(
  audio: Blob,
  language: AgentModeVoiceLanguage,
  signal?: AbortSignal,
): Promise<AgentModeVoiceTranscription> {
  const mediaType = baseMediaType(audio.type)
  if (!ALLOWED_AUDIO_TYPES.has(mediaType)) throw new ApiError(415, 'Unsupported voice recording format')
  if (audio.size === 0) throw new ApiError(422, 'No speech was recorded')
  if (audio.size > MAX_TRANSCRIPTION_AUDIO_BYTES) throw new ApiError(413, 'Voice recording is too large')

  const response = await voiceFetch(
    `/agent-mode/voice/transcribe?language=${language}`,
    {
      method: 'POST',
      headers: csrfHeaders({ 'Content-Type': audio.type }),
      body: audio,
      signal,
    },
    'Voice transcription failed',
  )
  const contentType = baseMediaType(response.headers.get('Content-Type') ?? '')
  if (contentType !== 'application/json') {
    throw new ApiError(response.status, 'Voice transcription returned an invalid response')
  }
  let raw: unknown
  try {
    raw = await response.json()
  } catch {
    throw new ApiError(response.status, 'Voice transcription returned an invalid response')
  }
  requireCurrentVoiceResponse(response)
  const parsed = parseTranscription(raw)
  if (!parsed) throw new ApiError(response.status, 'Voice transcription returned an invalid response')
  return parsed
}

/** Loads server-authored template speech. `toSpeechWire` rejects widened,
 * stale, or structurally unsafe inputs before a paid request can start. */
export async function speakAgentModeTemplate(
  request: NarrationSpeechRequest,
  signal?: AbortSignal,
): Promise<Blob> {
  const wire = toSpeechWire(request)
  if (!wire) throw new ApiError(400, 'Voice reply is not available for this delivery')
  const response = await voiceFetch(
    '/agent-mode/voice/speak',
    {
      method: 'POST',
      headers: csrfHeaders({ 'Content-Type': 'application/json' }),
      body: JSON.stringify(wire),
      signal,
    },
    'Voice reply failed',
  )
  const contentType = baseMediaType(response.headers.get('Content-Type') ?? '')
  if (contentType !== 'audio/mpeg') throw new ApiError(response.status, 'Voice reply returned invalid audio')
  if (response.headers.get('Content-Language') !== request.locale) {
    throw new ApiError(response.status, 'Voice reply returned the wrong language')
  }
  const audio = await response.blob()
  requireCurrentVoiceResponse(response)
  if (audio.size === 0) throw new ApiError(response.status, 'Voice reply returned invalid audio')
  return audio
}

/** Posts one confirmed internal note under its durable exact-once key. */
export function postAgentModeInternalNote(
  request: AgentModeInternalNoteRequest,
  signal?: AbortSignal,
): Promise<IssueComment> {
  return createIssueComment(request.issueId, request.body, 'internal', {
    clientRequestId: request.clientRequestId,
    signal,
  })
}

interface ProjectCatalogWire {
  id?: unknown
  key?: unknown
  name?: unknown
  status?: unknown
}

const PROJECT_STATUSES = new Set(['active', 'frozen', 'archived'])
const PROJECT_KEY = /^[A-Z][A-Z0-9]{2,9}$/
const UNSAFE_PROJECT_LABEL = /[\u0000-\u001f\u007f-\u009f]/u

function projectKeyAlias(value: unknown): string {
  return typeof value === 'string' && PROJECT_KEY.test(value) ? value : ''
}

function projectNameAlias(value: unknown): string {
  if (
    typeof value !== 'string'
    || value === ''
    || value !== value.trim()
    || UNSAFE_PROJECT_LABEL.test(value)
    || new TextEncoder().encode(value).byteLength > 256
  ) return ''
  return value
}

/** Loads the selector-independent project-filter vocabulary from the
 * principal's existing authorized project list. These refs never authorize a
 * delivery action: the next Agent Mode snapshot remains the sole authority. */
export async function loadAgentModeVoiceProjectCatalog(signal?: AbortSignal): Promise<VoiceProjectRef[]> {
  const epochGeneration = capturePermissionsEpochGeneration()
  const response = await fetch('/api/projects?status=all', {
    method: 'GET',
    cache: 'no-store',
    credentials: 'same-origin',
    signal,
  })
  if (!isPermissionsEpochGenerationCurrent(epochGeneration)) {
    throw new ApiError(0, 'Voice project catalog was superseded by an authentication change')
  }
  handleSession(response)
  const responseEpoch = observePermissionsEpoch(response, response.ok)
  if (!response.ok) throw new ApiError(response.status, 'Voice project catalog is unavailable')

  let raw: unknown
  try {
    raw = await response.json()
  } catch {
    throw new ApiError(response.status, 'Voice project catalog returned an invalid response')
  }
  if (!isPermissionsEpochGenerationCurrent(epochGeneration)) {
    throw new ApiError(0, 'Voice project catalog was superseded by an authentication change')
  }
  if (
    responseEpoch != null
    && permissionsEpoch.value != null
    && comparePermissionsEpoch(responseEpoch, permissionsEpoch.value) < 0
  ) throw new ApiError(0, 'Voice project catalog was superseded by a newer permissions epoch')
  if (!Array.isArray(raw) || raw.length > 10_000) {
    throw new ApiError(response.status, 'Voice project catalog returned an invalid response')
  }

  const projects: VoiceProjectRef[] = []
  const seen = new Set<number>()
  for (const candidate of raw) {
    if (!candidate || typeof candidate !== 'object' || Array.isArray(candidate)) {
      throw new ApiError(response.status, 'Voice project catalog returned an invalid response')
    }
    const project = candidate as ProjectCatalogWire
    if (
      typeof project.id !== 'number'
      || !Number.isSafeInteger(project.id)
      || project.id <= 0
      || seen.has(project.id)
      || typeof project.status !== 'string'
      || !PROJECT_STATUSES.has(project.status)
    ) {
      throw new ApiError(response.status, 'Voice project catalog returned an invalid response')
    }
    seen.add(project.id)
    projects.push({
      projectId: project.id,
      projectKey: projectKeyAlias(project.key),
      projectName: projectNameAlias(project.name),
    })
  }
  return projects
}
