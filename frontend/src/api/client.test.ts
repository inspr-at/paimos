/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  ApiError,
  StalePermissionsEpochError,
  api,
  errMsg,
  mustChangePassword,
  permissionsEpoch,
  resetPermissionsEpoch,
  sessionExpired,
} from './client'

/**
 * ACME-1 regression guards for the 401 → sessionExpired interceptor.
 *
 * The contract is:
 *   - A 401 on any non-auth endpoint flips sessionExpired.value to true
 *     so the AppLayout banner renders.
 *   - A 401 on /auth/login, /auth/me, /auth/forgot, /auth/reset,
 *     /auth/totp/verify, or /auth/reset/validate does NOT flip the
 *     flag — those 401s are expected (wrong password, pristine load,
 *     bad reset token) and would nag the user on the login page.
 *
 * We stub global.fetch to return canned status codes without spinning
 * up a real server.
 */

type FetchStub = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>

function stubFetch(impl: FetchStub) {
  globalThis.fetch = vi.fn(impl) as unknown as typeof fetch
}

function makeResponse(status: number, body: unknown = { error: 'unauthorized' }): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

function epochResponse(epoch: string, body: unknown = { ok: true }): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: {
      'Content-Type': 'application/json',
      'X-Permissions-Epoch': epoch,
    },
  })
}

class FakeXMLHttpRequest {
  static instances: FakeXMLHttpRequest[] = []
  readonly upload = { addEventListener: vi.fn() }
  readonly requestHeaders = new Map<string, string>()
  readonly responseHeaders = new Map<string, string>()
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  status = 200
  responseText = '{}'
  withCredentials = false

  constructor() {
    FakeXMLHttpRequest.instances.push(this)
  }

  open = vi.fn()
  send = vi.fn()
  setRequestHeader(name: string, value: string) {
    this.requestHeaders.set(name.toLowerCase(), value)
  }
  getResponseHeader(name: string) {
    return this.responseHeaders.get(name.toLowerCase()) ?? null
  }
}

describe('api client 401 interceptor', () => {
  let originalFetch: typeof fetch

  beforeEach(() => {
    originalFetch = globalThis.fetch
    resetPermissionsEpoch()
    sessionExpired.value = false
    mustChangePassword.value = false
  })

  afterEach(() => {
    globalThis.fetch = originalFetch
    vi.unstubAllGlobals()
  })

  it('flips sessionExpired on 401 from a data endpoint', async () => {
    stubFetch(async () => makeResponse(401))
    await expect(api.get('/projects')).rejects.toThrow()
    expect(sessionExpired.value).toBe(true)
  })

  it('flips sessionExpired on 401 from a non-GET data endpoint', async () => {
    stubFetch(async () => makeResponse(401))
    await expect(api.post('/issues', { title: 'x' })).rejects.toThrow()
    expect(sessionExpired.value).toBe(true)
  })

  it('does NOT flip sessionExpired on 401 from /auth/login (wrong password)', async () => {
    stubFetch(async () => makeResponse(401))
    await expect(api.post('/auth/login', { username: 'a', password: 'b' })).rejects.toThrow()
    expect(sessionExpired.value).toBe(false)
  })

  it('does NOT flip sessionExpired on 401 from /auth/me (pristine page load)', async () => {
    stubFetch(async () => makeResponse(401))
    await expect(api.get('/auth/me')).rejects.toThrow()
    expect(sessionExpired.value).toBe(false)
  })

  it('does NOT flip sessionExpired on 401 from /auth/reset/validate (bad token)', async () => {
    stubFetch(async () => makeResponse(401))
    await expect(api.get('/auth/reset/validate?token=bad')).rejects.toThrow()
    expect(sessionExpired.value).toBe(false)
  })

  it('does NOT flip sessionExpired on a successful request', async () => {
    stubFetch(async () => makeResponse(200, { ok: true }))
    await api.get('/projects')
    expect(sessionExpired.value).toBe(false)
  })

  it('does NOT flip sessionExpired on a non-401 error', async () => {
    stubFetch(async () => makeResponse(500, { error: 'boom' }))
    await expect(api.get('/projects')).rejects.toThrow()
    expect(sessionExpired.value).toBe(false)
  })

  it('salvages JSON objects with stray bytes before the payload', async () => {
    stubFetch(async () => new Response('null{"action":"optimize","body":{"optimized":"Better text"}}', {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))

    const payload = await api.get<{ action: string; body: { optimized: string } }>('/ai/action-preview')

    expect(payload.action).toBe('optimize')
    expect(payload.body.optimized).toBe('Better text')
  })

  it('surfaces a clean ApiError when the server returns invalid JSON', async () => {
    stubFetch(async () => new Response('nullboom', {
      status: 502,
      headers: { 'Content-Type': 'application/json' },
    }))

    await expect(api.get('/ai/action')).rejects.toThrow('invalid JSON response: nullboom')
  })

  it('hydrates ApiError from Problem Details bodies', async () => {
    stubFetch(async () => makeResponse(400, {
      type: 'https://paimos.com/errors/enum_violation',
      title: 'Invalid enum value',
      status: 400,
      detail: 'type "Tasky" is not valid',
      code: 'enum_violation',
      field: 'type',
      valid_values: ['ticket', 'task'],
      request_id: 'req-1',
    }))

    try {
      await api.post('/projects/6/issues', { title: 'Bad', type: 'Tasky' })
      throw new Error('expected request to fail')
    } catch (e) {
      expect(e).toBeInstanceOf(ApiError)
      const err = e as ApiError
      expect(err.message).toBe('type "Tasky" is not valid')
      expect(err.code).toBe('enum_violation')
      expect(err.field).toBe('type')
      expect(err.valid_values).toEqual(['ticket', 'task'])
      expect(err.request_id).toBe('req-1')
    }
  })

  it('shows request id for AI invalid-response errors without raw output', async () => {
    const rawSentinel = 'RAW_MODEL_OUTPUT_SENTINEL_DO_NOT_EXPOSE'
    stubFetch(async () => makeResponse(502, {
      type: 'https://paimos.com/errors/ai_action_invalid_response',
      title: 'Bad Gateway',
      status: 502,
      detail: 'AI returned an invalid response. Try again or choose a different model.',
      code: 'ai_action_invalid_response',
      request_id: 'ai-req-1',
    }))

    let caught: unknown
    try {
      await api.post('/ai/action', { action: 'detect_duplicates' })
    } catch (e) {
      caught = e
    }

    const message = errMsg(caught, 'AI action failed')
    expect(message).toBe('AI returned an invalid response. Try again or choose a different model. Request ID: ai-req-1')
    expect(message).not.toContain(rawSentinel)
  })

  it('uses Problem Details code for the must-change-password gate', async () => {
    stubFetch(async () => makeResponse(403, {
      type: 'https://paimos.com/errors/must_change_password',
      title: 'Password change required',
      status: 403,
      detail: 'password change required before continuing',
      code: 'must_change_password',
      error: 'must_change_password',
      request_id: 'req-2',
    }))

    let caught: unknown
    try {
      await api.get('/projects')
    } catch (e) {
      caught = e
    }
    expect(mustChangePassword.value).toBe(true)
    expect(errMsg(caught)).toBe('')
  })

  it('keeps the full int64 epoch range exact and rejects lower or malformed present headers', async () => {
    for (const epoch of ['9007199254740992', '9007199254740993', '9223372036854775807']) {
      stubFetch(async () => epochResponse(epoch))
      await expect(api.get('/projects')).resolves.toEqual({ ok: true })
      expect(permissionsEpoch.value).toBe(epoch)
    }

    for (const epoch of ['9007199254740992', '01', '9223372036854775808']) {
      stubFetch(async () => epochResponse(epoch))
      await expect(api.get('/projects')).rejects.toBeInstanceOf(
        epoch === '9007199254740992' ? StalePermissionsEpochError : ApiError,
      )
      expect(permissionsEpoch.value).toBe('9223372036854775807')
    }
  })

  it('rejects a response whose older body finishes after a newer epoch committed', async () => {
    const oldBody = deferred<string>()
    const oldResponse = epochResponse('10')
    Object.defineProperty(oldResponse, 'text', {
      value: vi.fn(async () => await oldBody.promise),
    })
    let call = 0
    stubFetch(async () => {
      call += 1
      return call === 1 ? oldResponse : epochResponse('11', { newest: true })
    })

    const oldRequest = api.get('/auth/me')
    await Promise.resolve()
    await Promise.resolve()
    expect(permissionsEpoch.value).toBe('10')
    await expect(api.get('/projects')).resolves.toEqual({ newest: true })
    expect(permissionsEpoch.value).toBe('11')

    oldBody.resolve(JSON.stringify({ stale: true }))
    await expect(oldRequest).rejects.toBeInstanceOf(StalePermissionsEpochError)
  })

  it('rejects an old-session body after an identity reset while accepting the new lower baseline', async () => {
    const oldBody = deferred<string>()
    const oldResponse = epochResponse('10')
    Object.defineProperty(oldResponse, 'text', {
      value: vi.fn(async () => await oldBody.promise),
    })
    let call = 0
    stubFetch(async () => {
      call += 1
      return call === 1 ? oldResponse : epochResponse('5', { principal: 'new' })
    })

    const oldRequest = api.get('/auth/me')
    await Promise.resolve()
    await Promise.resolve()
    expect(permissionsEpoch.value).toBe('10')
    resetPermissionsEpoch()
    await expect(api.get('/auth/me')).resolves.toEqual({ principal: 'new' })
    expect(permissionsEpoch.value).toBe('5')

    oldBody.resolve(JSON.stringify({ principal: 'old' }))
    await expect(oldRequest).rejects.toMatchObject({
      message: 'request superseded by an authentication change',
    })
    expect(permissionsEpoch.value).toBe('5')
  })

  it('makes XHR uploads reject lower, malformed, and old-generation authority headers', async () => {
    FakeXMLHttpRequest.instances = []
    vi.stubGlobal('XMLHttpRequest', FakeXMLHttpRequest as unknown as typeof XMLHttpRequest)
    permissionsEpoch.value = '11'

    const lower = api.upload('/attachments', new FormData())
    const lowerXHR = FakeXMLHttpRequest.instances[0]!
    lowerXHR.responseHeaders.set('x-permissions-epoch', '10')
    lowerXHR.responseText = JSON.stringify({ id: 1 })
    lowerXHR.onload?.()
    await expect(lower).rejects.toBeInstanceOf(StalePermissionsEpochError)

    const malformed = api.upload('/attachments', new FormData())
    const malformedXHR = FakeXMLHttpRequest.instances[1]!
    malformedXHR.responseHeaders.set('x-permissions-epoch', '01')
    malformedXHR.responseText = JSON.stringify({ id: 2 })
    malformedXHR.onload?.()
    await expect(malformed).rejects.toMatchObject({
      message: 'response carried an invalid permissions epoch',
    })

    const oldGeneration = api.upload('/attachments', new FormData())
    const oldXHR = FakeXMLHttpRequest.instances[2]!
    resetPermissionsEpoch()
    oldXHR.responseHeaders.set('x-permissions-epoch', '11')
    oldXHR.responseText = JSON.stringify({ id: 3 })
    oldXHR.onload?.()
    await expect(oldGeneration).rejects.toMatchObject({
      message: 'request superseded by an authentication change',
    })

    const current = api.upload<{ id: number }>('/attachments', new FormData())
    const currentXHR = FakeXMLHttpRequest.instances[3]!
    currentXHR.responseHeaders.set('x-permissions-epoch', '5')
    currentXHR.responseText = JSON.stringify({ id: 4 })
    currentXHR.onload?.()
    await expect(current).resolves.toEqual({ id: 4 })
    expect(permissionsEpoch.value).toBe('5')
  })
})
