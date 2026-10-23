// SPDX-License-Identifier: AGPL-3.0-only
import { afterEach, expect, it, vi } from 'vitest'
import { eventTransport, isNativeContext, readBoundedJSON, readNativeEvent, readNativePage } from '../src/lib/nativeSessionProgram'
import { nativeSessionBounds as bounds } from '../src/lib/nativeSessionContract'
import { sessionEnded } from '../src/lib/api'
import { binding, frame, request, session } from './native-runtime-fixtures'

afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); sessionEnded.blocked = false })
const emptyPage = { generation: 1, items: [], next_before: null, truncated: false }
const grant = () => ({ viewer_id: 'viewer-fixture', mode: 'view', control_capability: null, binding: { ...binding }, expires_in_ms: 30_000 })
const json = (value: unknown) => new Response(JSON.stringify(value))

// Original AEON-1309 adversarial JSON/shape fixtures run through the functions
// used by SessionPanel, including a successful read between failed reads.
it('rejects malformed native context and incomplete capabilities before enabling a viewer', async () => {
  await expect(readBoundedJSON(new Response('{'))).rejects.toBeInstanceOf(SyntaxError)
  expect(isNativeContext(await readBoundedJSON(new Response('null')))).toBe(false)
  const valid = session()
  expect(isNativeContext(await readBoundedJSON(json(valid)))).toBe(true)
  const booleans = Object.entries(valid.capabilities).filter(([, value]) => typeof value === 'boolean').map(([key]) => key)
  for (const key of booleans) {
    for (const value of [undefined, 'true', null, 1]) {
      expect(isNativeContext({ ...valid, capabilities: { ...valid.capabilities, [key]: value } }), key).toBe(false)
      expect(isNativeContext(valid)).toBe(true)
    }
  }
  for (const change of [
    { generation: -1 }, { revision: 1.5 }, { owned_session_id: '' }, { owner_person_id: '' },
    { profile: 'legacy' }, { role: 'unknown' }, { harness: 'unknown' }, { state: 'unknown' }, { mode: 'unknown' },
    { conversation_ref: null }, { predecessor_harness_session_id: 42 }, { reason: false }, { terminal_viewer_url: 42 },
    { current_binding: { ...binding, binding_revision: -1 } }, { current_binding: { ...binding, daemon_id: '' } },
    { controller: { controller_id: 'controller', actor_id: 'person-1', client_id: 'client', epoch: 'epoch' } },
    { capabilities: { ...valid.capabilities, steer: 'unknown' } }, { capabilities: { ...valid.capabilities, quota: 'unknown' } },
    { capabilities: { ...valid.capabilities, qualification_digest: '' } },
  ]) expect(isNativeContext({ ...valid, ...change }), JSON.stringify(change)).toBe(false)
})

it('rejects malformed replay and event envelopes before granting or opening a stream', async () => {
  const fetch = vi.fn()
  vi.stubGlobal('fetch', fetch)
  for (const body of ['{', 'null', JSON.stringify({ ...emptyPage, items: [null] })]) {
    fetch.mockReset().mockResolvedValue(new Response(body))
    await expect(eventTransport.events(request(), null, 200, new AbortController().signal)).rejects.toThrow()
    expect(fetch).toHaveBeenCalledOnce()
    expect(fetch.mock.calls[0]?.[0]).toContain('/events?')
  }
  for (const value of [null, { ...frame(), observed_at: 42 }, { ...frame(), payload: null }, { ...frame(), kind: 'unknown' }]) {
    expect(() => readNativeEvent(value, request())).toThrow('Invalid live event')
  }
  for (const value of [null, { ...emptyPage, items: [null] }, { ...emptyPage, truncated: 'false' },
    { ...emptyPage, generation: 2 }, { ...emptyPage, next_before: 'not-a-cursor' }]) {
    expect(() => readNativePage(value, request(), null)).toThrow()
  }
  expect(readNativePage({ ...emptyPage, items: [frame(2), frame(1)] }, request(), null).items.map(event => event.seq)).toEqual(['2', '1'])
})

it('refuses malformed or controlling attach grants without streaming or detaching an ungranted viewer', async () => {
  const fetch = vi.fn()
  vi.stubGlobal('fetch', fetch)
  for (const body of ['{', 'null', ...[
    { ...grant(), viewer_id: 42 }, { ...grant(), mode: 'control' }, { ...grant(), control_capability: 'synthetic-unusable-value' },
    { ...grant(), expires_in_ms: 0 }, { ...grant(), expires_in_ms: -1 },
    { ...grant(), binding: { ...binding, runtime_epoch: 'runtime-b' } },
  ].map(value => JSON.stringify(value))]) {
    fetch.mockReset().mockResolvedValue(new Response(body))
    const receive = vi.fn()
    await expect(eventTransport.stream(request(), receive, new AbortController().signal)).rejects.toThrow()
    expect(receive).not.toHaveBeenCalled()
    expect(fetch).toHaveBeenCalledOnce()
    const [url, init] = fetch.mock.calls[0]!
    expect(url).toBe('/api/projects/project-1/native-sessions/owned-a/attach')
    expect(JSON.parse(init.body)).toMatchObject({ mode: 'view', view: 'structured', consent_id: null, expected_binding: binding })
  }
  fetch.mockClear()
  await expect(eventTransport.stream(request('expert'), vi.fn(), new AbortController().signal)).rejects.toEqual({ reason: 'qualification_missing' })
  expect(fetch).not.toHaveBeenCalled()
})

it('detaches the captured viewer after invalid SSE and accepts only an exact bound structured stream', async () => {
  const receive = vi.fn(), fetch = vi.fn()
  vi.stubGlobal('fetch', fetch)
  for (const body of ['{', 'null', JSON.stringify({ ...frame(), observed_at: 42 })]) {
    fetch.mockReset().mockResolvedValueOnce(json(grant())).mockResolvedValueOnce(new Response(`data: ${body}\n\n`)).mockResolvedValueOnce(json({}))
    await expect(eventTransport.stream(request(), receive, new AbortController().signal)).rejects.toThrow()
    expect(receive).not.toHaveBeenCalled()
    expect(fetch.mock.calls.map(([url]) => new URL(url, 'https://fixture.test').pathname.split('/').at(-1))).toEqual(['attach', 'stream', 'detach'])
    expect(JSON.parse(fetch.mock.calls[2]![1].body)).toMatchObject({ viewer_id: 'viewer-fixture', expected_binding: binding })
  }
  const encoded = new TextEncoder().encode(`: ping\r\n\r\ndata: ${JSON.stringify(frame())}\r\n\r\n`)
  fetch.mockReset().mockResolvedValueOnce(json(grant())).mockResolvedValueOnce(new Response(new ReadableStream({
    start(controller) { controller.enqueue(encoded.slice(0, 23)); controller.enqueue(encoded.slice(23)); controller.close() },
  }))).mockResolvedValueOnce(json({}))
  await eventTransport.stream({ ...request(), lastEventId: '1:0' }, receive, new AbortController().signal)
  expect(receive).toHaveBeenCalledExactlyOnceWith(frame())
  const [url, init] = fetch.mock.calls[1]!, query = new URL(url, 'https://fixture.test').searchParams
  expect(Object.fromEntries(query)).toEqual({ view: 'structured', expected_runtime_epoch: binding.runtime_epoch,
    expected_binding_revision: String(binding.binding_revision), expected_harness_session_id: binding.harness_session_id })
  expect(init.headers).toMatchObject({ 'X-Aeon-Viewer-ID': 'viewer-fixture', 'Last-Event-ID': '1:0' })
  expect(fetch.mock.calls).toHaveLength(3)
})

it('bounds decoded response bytes and UTF-8, releases readers, and preserves precise refusal reasons', async () => {
  for (const [status, reason] of [[401, 'permission_revoked'], [403, 'permission_revoked'], [404, 'permission_revoked'],
    [409, 'cursor_expired'], [503, 'feature_disabled'], [500, 'native_failure']] as const) {
    await expect(readBoundedJSON(new Response(null, { status }))).rejects.toEqual({ status, reason })
  }
  await expect(readBoundedJSON(new Response(null))).rejects.toThrow('Missing session response')
  await expect(readBoundedJSON(new Response(new Uint8Array([0xff])))).rejects.toBeInstanceOf(TypeError)
  const cancel = vi.fn()
  const body = new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(new Uint8Array(bounds.event_page_bytes + 1)) }, cancel })
  await expect(readBoundedJSON(new Response(body))).rejects.toThrow('Session response exceeds bounds')
  expect(cancel).toHaveBeenCalledOnce()
  expect(body.locked).toBe(false)
  const exact = `"${'x'.repeat(bounds.event_page_bytes - 2)}"`
  expect((await readBoundedJSON(new Response(exact)) as string).length).toBe(bounds.event_page_bytes - 2)
})

it('late stream chunks after cancellation cannot emit an event and detach only the original viewer', async () => {
  let deliver!: (value: Uint8Array) => void
  let opened!: () => void
  const started = new Promise<void>(resolve => { opened = resolve })
  const body = new ReadableStream<Uint8Array>({ start(controller) { deliver = value => { controller.enqueue(value); controller.close() } } })
  const fetch = vi.fn().mockResolvedValueOnce(json(grant())).mockImplementationOnce(async () => { opened(); return new Response(body) }).mockResolvedValueOnce(json({}))
  vi.stubGlobal('fetch', fetch)
  const abort = new AbortController(), receive = vi.fn(), captured = request()
  const pending = eventTransport.stream(captured, receive, abort.signal)
  await started
  abort.abort()
  deliver(new TextEncoder().encode(`data: ${JSON.stringify(frame())}\n\n`))
  await pending
  expect(receive).not.toHaveBeenCalled()
  expect(body.locked).toBe(false)
  expect(fetch).toHaveBeenCalledTimes(3)
  expect(JSON.parse(fetch.mock.calls[2]![1].body)).toMatchObject({ expected_binding: binding, viewer_id: 'viewer-fixture' })
})
