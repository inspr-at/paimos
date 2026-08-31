import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { permissionsEpoch, resetPermissionsEpoch, sessionExpired } from '@/api/client'
import { sendPaimos6SessionUtterance, type Paimos6SessionUtteranceRequest } from './sessionUtterance'

const UTTERANCE_ID = 'utt_0123456789abcdef0123456789abcdef'
const SESSION_ID = '17e5d8f7-0b11-4bee-a8a4-a11406de865a'
const MESSAGE_ID = '27e5d8f7-0b11-4bee-a8a4-a11406de865a'
const THREAD_ID = SESSION_ID
const DELIVERY_ID = '47e5d8f7-0b11-4bee-a8a4-a11406de865a'

function request(selected = true): Paimos6SessionUtteranceRequest {
  return {
    projectId: 42,
    utteranceId: UTTERANCE_ID,
    text: 'Please check the failing test.',
    selectedSession: selected ? { productSessionId: SESSION_ID, revision: 3 } : null,
  }
}

function result(routeKind: 'project_agent' | 'paimos' = 'project_agent') {
  return {
    schema_version: 1,
    utterance_id: UTTERANCE_ID,
    route_kind: routeKind,
    product_session_id: SESSION_ID,
    product_session_revision: 3,
    message_id: MESSAGE_ID,
    thread_id: THREAD_ID,
    delivery_id: routeKind === 'project_agent' ? DELIVERY_ID : null,
    created_at: '2026-08-30T22:45:00Z',
  }
}

function response(body: unknown, init: ResponseInit = {}) {
  const headers = new Headers(init.headers)
  if (!headers.has('Cache-Control')) headers.set('Cache-Control', 'private, no-store')
  if (!headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (!headers.has('X-Permissions-Epoch')) headers.set('X-Permissions-Epoch', '4')
  return new Response(JSON.stringify(body), { status: 201, ...init, headers })
}

describe('Paimos 6 session utterance service (PAI-862)', () => {
  beforeEach(() => {
    sessionExpired.value = false
    permissionsEpoch.value = null
    document.cookie = 'csrf_token=p6-voice-csrf; path=/'
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    document.cookie = 'csrf_token=; Max-Age=0; path=/'
    if (sessionExpired.value) resetPermissionsEpoch()
    sessionExpired.value = false
  })

  it('posts only the exact selected-session wire and parses the closed 201 result', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => response(result()))
    vi.stubGlobal('fetch', fetchMock)
    const input = request()
    Object.assign(input as object, { audio: 'not allowed', target: 'codex:amy', user_id: 7 })

    await expect(sendPaimos6SessionUtterance(input)).resolves.toEqual({
      utteranceId: UTTERANCE_ID,
      routeKind: 'project_agent',
      productSessionId: SESSION_ID,
      productSessionRevision: 3,
      messageId: MESSAGE_ID,
      threadId: THREAD_ID,
      deliveryId: DELIVERY_ID,
      createdAt: '2026-08-30T22:45:00Z',
    })
    const [url, init] = fetchMock.mock.calls[0]!
    expect(url).toBe('/api/projects/42/session-utterances/v1')
    expect(init).toMatchObject({ method: 'POST', cache: 'no-store', credentials: 'same-origin' })
    expect(new Headers(init!.headers).get('X-CSRF-Token')).toBe('p6-voice-csrf')
    expect(JSON.parse(String(init!.body))).toEqual({
      schema_version: 1,
      utterance_id: UTTERANCE_ID,
      text: 'Please check the failing test.',
      selected_session: { product_session_id: SESSION_ID, revision: 3 },
    })
    expect(String(init!.body)).not.toContain('audio')
    expect(String(init!.body)).not.toContain('codex:amy')
  })

  it('routes null selection only through a coherent Paimos result', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response(result('paimos'))))
    await expect(sendPaimos6SessionUtterance(request(false))).resolves.toMatchObject({
      routeKind: 'paimos',
      deliveryId: null,
    })
  })

  it.each([
    { ...result(), extra: true },
    { ...result(), utterance_id: 'utt_11111111111111111111111111111111' },
    { ...result(), route_kind: 'paimos' },
    result('paimos'),
    { ...result('paimos'), delivery_id: DELIVERY_ID },
    { ...result(), product_session_revision: 4 },
    { ...result(), thread_id: '37e5d8f7-0b11-4bee-a8a4-a11406de865a' },
    { ...result(), created_at: '2026-08-30T22:45:00+01:00' },
  ])('rejects widened or contradictory success wire %#', async (wire) => {
    vi.stubGlobal('fetch', vi.fn(async () => response(wire)))
    await expect(sendPaimos6SessionUtterance(request())).rejects.toThrow('invalid response')
  })

  it('retains only frozen Problem Details codes and validates requests before transport', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response({
      type: 'about:blank', title: 'Conflict', status: 409,
      code: 'session_utterance_selection_stale', detail: 'private detail is not copied',
    }, { status: 409, headers: { 'Content-Type': 'application/problem+json' } })))
    await expect(sendPaimos6SessionUtterance(request())).rejects.toMatchObject({
      status: 409,
      code: 'session_utterance_selection_stale',
    })

    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    for (const invalid of [
      { ...request(), utteranceId: 'bad' },
      { ...request(), text: ' surrounding ' },
      { ...request(), text: 'unsafe\ncontrol' },
      { ...request(), text: 'ü'.repeat(4_097) },
      { ...request(), selectedSession: { productSessionId: 'foreign', revision: 3 } },
      { ...request(), selectedSession: { productSessionId: SESSION_ID, revision: 0 } },
    ]) {
      await expect(sendPaimos6SessionUtterance(invalid)).rejects.toMatchObject({ status: 400 })
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
