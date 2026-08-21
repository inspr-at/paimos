import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/services/issueComments', () => ({ createIssueComment: vi.fn() }))

import {
  permissionsEpoch,
  resetPermissionsEpoch,
  SessionExpiredError,
  sessionExpired,
  sessionExpiresAt,
} from '@/api/client'
import { buildClarificationSpeech, buildStatusSpeech } from '@/components/agent-mode/agentModeNarration'
import { createIssueComment } from '@/services/issueComments'
import {
  loadAgentModeVoiceProjectCatalog,
  postAgentModeInternalNote,
  speakAgentModeTemplate,
  transcribeAgentModeAudio,
} from './agentModeVoice'

function response(body: BodyInit | null, init: ResponseInit = {}) {
  const headers = new Headers(init.headers)
  if (!headers.has('Cache-Control')) headers.set('Cache-Control', 'private, no-store')
  if (!headers.has('X-Permissions-Epoch')) headers.set('X-Permissions-Epoch', '4')
  if (!headers.has('X-Session-Expires-At')) headers.set('X-Session-Expires-At', '2027-01-02T03:04:05Z')
  if (!headers.has('Content-Language')) headers.set('Content-Language', 'en')
  return new Response(body, { ...init, headers })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

describe('agentModeVoice service', () => {
  beforeEach(() => {
    sessionExpired.value = false
    sessionExpiresAt.value = null
    permissionsEpoch.value = null
    document.cookie = 'csrf_token=voice-csrf; path=/'
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    document.cookie = 'csrf_token=; Max-Age=0; path=/'
  })

  it('posts raw MIME audio to the exact language query and accepts only a strict final DTO', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => response(JSON.stringify({
      utterance_id: 'utt_0123456789abcdef0123456789abcdef',
      text: 'show PAI-808',
      final: true,
    }), { headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const audio = new Blob(['browser audio'], { type: 'audio/webm;codecs=opus' })

    await expect(transcribeAgentModeAudio(audio, 'de')).resolves.toEqual({
      utteranceId: 'utt_0123456789abcdef0123456789abcdef',
      text: 'show PAI-808',
      final: true,
    })
    expect(fetchMock).toHaveBeenCalledOnce()
    const [url, init] = fetchMock.mock.calls[0]!
    expect(url).toBe('/api/agent-mode/voice/transcribe?language=de')
    expect(init).toMatchObject({ method: 'POST', body: audio, cache: 'no-store', credentials: 'same-origin' })
    expect(new Headers(init!.headers).get('Content-Type')).toBe('audio/webm;codecs=opus')
    expect(new Headers(init!.headers).get('X-CSRF-Token')).toBe('voice-csrf')
    expect(permissionsEpoch.value).toBe('4')
    expect(sessionExpiresAt.value?.toISOString()).toBe('2027-01-02T03:04:05.000Z')
  })

  it.each([
    { utterance_id: 'utt_0123456789abcdef0123456789abcdef', text: 'next', final: false },
    { utterance_id: 'bad', text: 'next', final: true },
    { utterance_id: 'utt_0123456789abcdef0123456789abcdef', text: ' next ', final: true },
    { utterance_id: 'utt_0123456789abcdef0123456789abcdef', text: 'next', final: true, partial: false },
    { utterance_id: 'utt_0123456789abcdef0123456789abcdef', text: 'ü'.repeat(4_097), final: true },
  ])('rejects malformed or widened transcription payload %#', async (wire) => {
    vi.stubGlobal('fetch', vi.fn(async () => response(JSON.stringify(wire), {
      headers: { 'Content-Type': 'application/json' },
    })))
    await expect(transcribeAgentModeAudio(new Blob(['x'], { type: 'audio/webm' }), 'en'))
      .rejects.toThrow('invalid response')
  })

  it('accepts transcription text through the exact 8192-byte UTF-8 boundary', async () => {
    for (const text of ['a'.repeat(8_192), 'ü'.repeat(4_096)]) {
      vi.stubGlobal('fetch', vi.fn(async () => response(JSON.stringify({
        utterance_id: 'utt_0123456789abcdef0123456789abcdef', text, final: true,
      }), { headers: { 'Content-Type': 'application/json' } })))
      await expect(transcribeAgentModeAudio(new Blob(['x'], { type: 'audio/webm' }), 'en'))
        .resolves.toMatchObject({ text, final: true })
    }

    vi.stubGlobal('fetch', vi.fn(async () => response(JSON.stringify({
      utterance_id: 'utt_0123456789abcdef0123456789abcdef', text: 'a'.repeat(8_193), final: true,
    }), { headers: { 'Content-Type': 'application/json' } })))
    await expect(transcribeAgentModeAudio(new Blob(['x'], { type: 'audio/webm' }), 'en'))
      .rejects.toThrow('invalid response')
  })

  it('rejects JSONP and oversized audio locally before transport', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response('{}', {
      headers: { 'Content-Type': 'application/jsonp' },
    })))
    await expect(transcribeAgentModeAudio(new Blob(['x'], { type: 'audio/webm' }), 'en'))
      .rejects.toThrow('invalid response')

    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const oversized = new Blob([new Uint8Array(12 * 1024 * 1024 + 1)], { type: 'audio/webm' })
    await expect(transcribeAgentModeAudio(oversized, 'en')).rejects.toMatchObject({ status: 413 })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('serializes the exact five-key TTS wire and never spreads caller fields', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => response(new Blob(['mp3']), {
      headers: { 'Content-Type': 'audio/mpeg' },
    }))
    vi.stubGlobal('fetch', fetchMock)
    const request = buildClarificationSpeech(
      { id: 'delivery:808', deliveryRevision: 'delivery:808:7' },
      [{ deliveryId: 'delivery:809' }, { deliveryId: 'delivery:810' }],
      'en',
    )!
    Object.assign(request as object, { text: 'private note', trustRevision: 'secret' })

    await expect(speakAgentModeTemplate(request)).resolves.toBeInstanceOf(Blob)
    const [, init] = fetchMock.mock.calls[0]!
    expect(JSON.parse(String(init!.body))).toEqual({
      template: 'clarification',
      locale: 'en',
      delivery_id: 'delivery:808',
      delivery_revision: 'delivery:808:7',
      candidate_ids: ['delivery:809', 'delivery:810'],
    })
    expect(Object.keys(JSON.parse(String(init!.body))).sort()).toEqual([
      'candidate_ids', 'delivery_id', 'delivery_revision', 'locale', 'template',
    ])
    expect(String(init!.body)).not.toContain('private note')
    expect(String(init!.body)).not.toContain('secret')
    expect(sessionExpiresAt.value?.toISOString()).toBe('2027-01-02T03:04:05.000Z')
  })

  it('requires TTS audio to declare the requested language', async () => {
    for (const language of [null, 'de']) {
      const headers = new Headers({
        'Cache-Control': 'private, no-store',
        'Content-Type': 'audio/mpeg',
        'X-Permissions-Epoch': '4',
      })
      if (language != null) headers.set('Content-Language', language)
      vi.stubGlobal('fetch', vi.fn(async () => new Response(new Blob(['mp3']), { headers })))
      const request = buildStatusSpeech({ id: 'delivery:808', deliveryRevision: 'delivery:808:7' }, 'en')!
      await expect(speakAgentModeTemplate(request)).rejects.toThrow('wrong language')
    }
  })

  it('fails before fetch when the speech subject is stale or structurally unsafe', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const unsafe = buildStatusSpeech({ id: 'delivery:808', deliveryRevision: null }, 'en')
    expect(unsafe).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('rejects responses without private no-store and propagates session expiry without reading bodies', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', {
      headers: { 'Content-Type': 'application/json', 'X-Permissions-Epoch': '4' },
    })))
    await expect(transcribeAgentModeAudio(new Blob(['x'], { type: 'audio/webm' }), 'en'))
      .rejects.toThrow('privacy contract')

    vi.stubGlobal('fetch', vi.fn(async () => response('private response', { status: 401 })))
    await expect(transcribeAgentModeAudio(new Blob(['x'], { type: 'audio/webm' }), 'en'))
      .rejects.toBeInstanceOf(SessionExpiredError)
    expect(sessionExpired.value).toBe(true)
  })

  it('posts dictated text only as an internal comment under its stable request id', async () => {
    vi.mocked(createIssueComment).mockResolvedValue({ id: 4 } as never)
    const signal = new AbortController().signal
    await postAgentModeInternalNote({
      issueId: 808,
      body: 'Keep this exact punctuation — bitte.',
      clientRequestId: 'amv1-0123456789abcdef',
    }, signal)
    expect(createIssueComment).toHaveBeenCalledWith(
      808,
      'Keep this exact punctuation — bitte.',
      'internal',
      { clientRequestId: 'amv1-0123456789abcdef', signal },
    )
  })

  it('loads active, frozen, and archived project refs without retaining extra fields', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => response(JSON.stringify([
      { id: 12, key: 'PAI', name: 'Paimos', status: 'active', customer_name: 'secret' },
      { id: 7, key: 'OPS', name: 'Operations', status: 'frozen', description: 'ignored' },
      { id: 9, key: 'ARC', name: 'Archive', status: 'archived', ai_policy: { hidden: true } },
      { id: 10, key: '', name: ' ', status: 'active' },
      { id: 11, key: 'LONG', name: 'x'.repeat(257), status: 'active' },
    ]), { headers: { 'Content-Type': 'application/json', 'X-Permissions-Epoch': '44' } }))
    vi.stubGlobal('fetch', fetchMock)
    const signal = new AbortController().signal

    await expect(loadAgentModeVoiceProjectCatalog(signal)).resolves.toEqual([
      { projectId: 12, projectKey: 'PAI', projectName: 'Paimos' },
      { projectId: 7, projectKey: 'OPS', projectName: 'Operations' },
      { projectId: 9, projectKey: 'ARC', projectName: 'Archive' },
      { projectId: 10, projectKey: '', projectName: '' },
      { projectId: 11, projectKey: 'LONG', projectName: '' },
    ])
    const [url, init] = fetchMock.mock.calls[0]!
    expect(url).toBe('/api/projects?status=all')
    expect(init).toMatchObject({ method: 'GET', cache: 'no-store', credentials: 'same-origin', signal })
    expect(permissionsEpoch.value).toBe('44')
    expect(sessionExpiresAt.value?.toISOString()).toBe('2027-01-02T03:04:05.000Z')
  })

  it('rejects deleted, duplicate, or malformed project identity instead of partially broadening it', async () => {
    for (const catalog of [
      [{ id: 1, key: 'DEL', name: 'Deleted', status: 'deleted' }],
      [{ id: 1, key: 'A', name: 'One', status: 'active' }, { id: 1, key: 'B', name: 'Two', status: 'active' }],
      [{ id: '1', key: 'PAI', name: 'Wrong id', status: 'active' }],
    ]) {
      vi.stubGlobal('fetch', vi.fn(async () => response(JSON.stringify(catalog), {
        headers: { 'Content-Type': 'application/json' },
      })))
      await expect(loadAgentModeVoiceProjectCatalog()).rejects.toThrow('invalid response')
    }
  })

  it('maps unusable labels field-locally without erasing valid project ids or aliases', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response(JSON.stringify([
      { id: 1, key: 'bad key', name: 'Usable Name', status: 'active' },
      { id: 2, key: 'OPS', name: '\u0000unsafe', status: 'frozen' },
      { id: 3, key: null, name: null, status: 'archived' },
    ]), { headers: { 'Content-Type': 'application/json' } })))

    await expect(loadAgentModeVoiceProjectCatalog()).resolves.toEqual([
      { projectId: 1, projectKey: '', projectName: 'Usable Name' },
      { projectId: 2, projectKey: 'OPS', projectName: '' },
      { projectId: 3, projectKey: '', projectName: '' },
    ])
  })

  it('requires a canonical safe permissions epoch on every successful voice response', async () => {
    for (const epoch of [null, '-1', '1.5', '01', '9223372036854775808']) {
      const headers = new Headers({
        'Cache-Control': 'private, no-store',
        'Content-Type': 'application/json',
      })
      if (epoch != null) headers.set('X-Permissions-Epoch', epoch)
      vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify([]), { headers })))
      await expect(loadAgentModeVoiceProjectCatalog()).rejects.toThrow(/epoch/)
    }

    for (const epoch of ['9007199254740992', '9007199254740993', '9223372036854775807']) {
      vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify([]), {
        headers: { 'Content-Type': 'application/json', 'X-Permissions-Epoch': epoch },
      })))
      await expect(loadAgentModeVoiceProjectCatalog()).resolves.toEqual([])
      expect(permissionsEpoch.value).toBe(epoch)
    }

    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      utterance_id: 'utt_0123456789abcdef0123456789abcdef', text: 'next', final: true,
    }), {
      headers: { 'Cache-Control': 'private, no-store', 'Content-Type': 'application/json' },
    })))
    await expect(transcribeAgentModeAudio(new Blob(['x'], { type: 'audio/webm' }), 'en'))
      .rejects.toThrow(/epoch/)
  })

  it('rejects a transcription whose epoch becomes stale while its JSON body is streaming', async () => {
    const body = deferred<unknown>()
    const delayed = response('', {
      headers: { 'Content-Type': 'application/json', 'X-Permissions-Epoch': '11' },
    })
    Object.defineProperty(delayed, 'json', {
      value: vi.fn(async () => await body.promise),
    })
    vi.stubGlobal('fetch', vi.fn(async () => delayed))

    const pending = transcribeAgentModeAudio(new Blob(['x'], { type: 'audio/webm' }), 'en')
    await Promise.resolve()
    await Promise.resolve()
    expect(permissionsEpoch.value).toBe('11')
    permissionsEpoch.value = '12'
    body.resolve({
      utterance_id: 'utt_0123456789abcdef0123456789abcdef',
      text: 'next',
      final: true,
    })

    await expect(pending).rejects.toThrow('newer permissions epoch')
  })

  it('rejects a project catalog whose epoch becomes stale while its JSON body is streaming', async () => {
    const body = deferred<unknown>()
    const delayed = response('', {
      headers: { 'Content-Type': 'application/json', 'X-Permissions-Epoch': '11' },
    })
    Object.defineProperty(delayed, 'json', {
      value: vi.fn(async () => await body.promise),
    })
    vi.stubGlobal('fetch', vi.fn(async () => delayed))

    const pending = loadAgentModeVoiceProjectCatalog()
    await Promise.resolve()
    await Promise.resolve()
    expect(permissionsEpoch.value).toBe('11')
    permissionsEpoch.value = '12'
    body.resolve([{ id: 1, key: 'PAI', name: 'Paimos', status: 'active' }])

    await expect(pending).rejects.toThrow('newer permissions epoch')
  })

  it('rejects TTS audio whose epoch becomes stale while its blob body is streaming', async () => {
    const body = deferred<Blob>()
    const delayed = response('', {
      headers: {
        'Content-Type': 'audio/mpeg',
        'Content-Language': 'en',
        'X-Permissions-Epoch': '11',
      },
    })
    Object.defineProperty(delayed, 'blob', {
      value: vi.fn(async () => await body.promise),
    })
    vi.stubGlobal('fetch', vi.fn(async () => delayed))

    const request = buildStatusSpeech({ id: 'delivery:808', deliveryRevision: 'delivery:808:7' }, 'en')!
    const pending = speakAgentModeTemplate(request)
    await Promise.resolve()
    await Promise.resolve()
    expect(permissionsEpoch.value).toBe('11')
    permissionsEpoch.value = '12'
    body.resolve(new Blob(['mp3'], { type: 'audio/mpeg' }))

    await expect(pending).rejects.toThrow('newer permissions epoch')
  })

  it('rejects a direct-fetch body that settles after the authenticated identity generation resets', async () => {
    const body = deferred<unknown>()
    const delayed = response('', {
      headers: { 'Content-Type': 'application/json', 'X-Permissions-Epoch': '11' },
    })
    Object.defineProperty(delayed, 'json', {
      value: vi.fn(async () => await body.promise),
    })
    vi.stubGlobal('fetch', vi.fn(async () => delayed))

    const pending = loadAgentModeVoiceProjectCatalog()
    await Promise.resolve()
    await Promise.resolve()
    resetPermissionsEpoch()
    body.resolve([{ id: 1, key: 'PAI', name: 'Paimos', status: 'active' }])

    await expect(pending).rejects.toThrow('authentication change')
  })

})
