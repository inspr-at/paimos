// SPDX-License-Identifier: AGPL-3.0-only
// Shared by the mounted session panel and its runtime boundary tests.
import { api } from './api'
import { parseJson } from './json'
import { bindingKey, type ExpertRequest, type NativeExpertTransport } from './nativeExpert/ports'
import { boundedEvent, boundedPage } from './nativeExpert/buffer'
import { nativeSessionBounds, nativeSessionPayloadSchemas, type NativeBinding, type NativeSessionView, type NativeStreamEvent, type NativeEventPage } from './nativeSessionContract'

const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const nonempty = (value: unknown): value is string => typeof value === 'string' && value.length > 0
const nonnegativeInteger = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0
const nullableText = (value: unknown): value is string | null => value === null || typeof value === 'string'
function isNativeBinding(value: unknown): value is NativeBinding {
  return record(value) && ['tenant_id', 'project_id', 'owned_session_id', 'harness_session_id', 'runtime_epoch', 'daemon_id', 'daemon_generation'].every(key => nonempty(value[key])) && nonnegativeInteger(value.binding_revision) && nullableText(value.run_id)
}
export function isNativeContext(value: unknown): value is NativeSessionView {
  if (!record(value) || !nonempty(value.owned_session_id) || !nonempty(value.owner_person_id) || value.profile !== 'native' ||
    !nonnegativeInteger(value.generation) || !nonnegativeInteger(value.revision) ||
    !['coordinator', 'worker'].includes(String(value.role)) || !['claude', 'codex', 'cursor', 'grok', 'pi', 'gemini', 'opencode'].includes(String(value.harness)) ||
    !['accepted', 'waiting_for_exit', 'starting', 'idle', 'running', 'requires_action', 'quiescing', 'taken', 'returning', 'recovering', 'ownership_lost', 'stopping', 'stopped', 'failed'].includes(String(value.state)) ||
    !['structured', 'terminal', 'none'].includes(String(value.mode)) || typeof value.conversation_ref !== 'string' ||
    !nullableText(value.predecessor_harness_session_id) || !nullableText(value.reason) || !nullableText(value.terminal_viewer_url) ||
    (value.current_binding !== null && !isNativeBinding(value.current_binding))) return false
  const caps = value.capabilities, controller = value.controller
  return record(caps) && ['input', 'interrupt', 'stop', 'resume', 'take', 'expert', 'terminal_view', 'approvals', 'questions', 'correlated_receipts', 'take_supported', 'events_supported', 'interrupt_supported', 'chat_supported'].every(key => typeof caps[key] === 'boolean') &&
    ['native', 'queued', 'next_step', 'unsupported'].includes(String(caps.steer)) && ['stream', 'stream_and_qualified_idle', 'unsupported'].includes(String(caps.quota)) && nonempty(caps.qualification_digest) &&
    (controller === null || record(controller) && ['controller_id', 'actor_id', 'client_id', 'epoch', 'expires_at'].every(key => nonempty(controller[key])))
}
// Check the untrusted envelope before the existing event validator checks its
// kind-specific payload, privacy, binding, generation, sequence and byte bounds.
function isNativeEnvelope(value: unknown): value is NativeStreamEvent {
  return record(value) && isNativeBinding(value.binding) && nonnegativeInteger(value.generation) &&
    typeof value.seq === 'string' && typeof value.sequence === 'string' && typeof value.dropped_frames === 'string' &&
    typeof value.observed_at === 'string' && value.source === 'native' && typeof value.kind === 'string' &&
    Object.hasOwn(nativeSessionPayloadSchemas, value.kind) && record(value.payload)
}
export function readNativeEvent(value: unknown, request: ExpertRequest): NativeStreamEvent {
  if (!isNativeEnvelope(value)) throw new Error('Invalid live event')
  const event = boundedEvent(value, request)
  if (!event) throw new Error('Invalid live event')
  return event
}
export function readNativePage(value: unknown, request: ExpertRequest, before: string | null): NativeEventPage {
  if (!record(value) || !nonnegativeInteger(value.generation) || !Array.isArray(value.items) || value.items.length > nativeSessionBounds.event_page_items ||
    !nullableText(value.next_before) || typeof value.truncated !== 'boolean') throw new Error('Invalid event page')
  const page = { generation: value.generation, items: value.items.map(item => readNativeEvent(item, request)), next_before: value.next_before, truncated: value.truncated }
  if (!boundedPage(page, request, before)) throw new Error('Invalid event page')
  return page
}
async function boundedJSON(path: string, signal: AbortSignal, body?: unknown, headers?: Record<string, string>): Promise<unknown> {
  const response = await api(path, { signal, ...(body === undefined ? {} : { method: 'POST', body: JSON.stringify(body) }), headers: { 'Content-Type': 'application/json', ...headers } }, 3000)
  return readBoundedJSON(response)
}
export async function readBoundedJSON(response: Response): Promise<unknown> {
  if (!response.ok) throw { status: response.status, reason: response.status === 503 ? 'feature_disabled' : [401, 403, 404].includes(response.status) ? 'permission_revoked' : response.status === 409 ? 'cursor_expired' : 'native_failure' }
  if (!response.body) throw new Error('Missing session response')
  const reader = response.body.getReader(), chunks: Uint8Array[] = []; let size = 0
  try { for (;;) { const part = await reader.read(); if (part.done) break; size += part.value.byteLength; if (size > nativeSessionBounds.event_page_bytes) throw new Error('Session response exceeds bounds'); chunks.push(part.value) } }
  finally { await reader.cancel().catch(() => {}); reader.releaseLock() }
  const bytes = new Uint8Array(size); let offset = 0
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength }
  return parseJson(new TextDecoder('utf-8', { fatal: true }).decode(bytes))
}
// Read-only structured transport. Expert/PTY exports stay closed until I2-core
// supplies an explicit server-approved owner consent. No control grant is requested.
export const eventTransport: NativeExpertTransport = {
  async events(request, before, limit, signal) {
    if (request.view !== 'structured') throw { reason: 'qualification_missing' }
    const path = `/projects/${encodeURIComponent(request.binding.project_id)}/native-sessions/${encodeURIComponent(request.binding.owned_session_id)}/events`
    const query = new URLSearchParams({ generation: String(request.generation), limit: String(Math.min(limit, nativeSessionBounds.event_page_items)) })
    if (before) query.set('before', before)
    return readNativePage(await boundedJSON(`${path}?${query}`, AbortSignal.any([signal, AbortSignal.timeout(3000)])), request, before)
  },
  async stream(request, receive, signal) {
    if (request.view !== 'structured') throw { reason: 'qualification_missing' }
    const path = `/projects/${encodeURIComponent(request.binding.project_id)}/native-sessions/${encodeURIComponent(request.binding.owned_session_id)}`
    const query = new URLSearchParams({ view: 'structured', expected_runtime_epoch: request.binding.runtime_epoch,
      expected_binding_revision: String(request.binding.binding_revision), expected_harness_session_id: request.binding.harness_session_id })
    const attached = await boundedJSON(`${path}/attach`, AbortSignal.any([signal, AbortSignal.timeout(3000)]),
      { request_id: crypto.randomUUID(), expected_binding: request.binding, client_id: crypto.randomUUID(), mode: 'view', view: 'structured', consent_id: null })
    if (!record(attached) || attached.mode !== 'view' || attached.control_capability !== null || !nonempty(attached.viewer_id) || !isNativeBinding(attached.binding) ||
      bindingKey(attached.binding) !== bindingKey(request.binding) || !nonnegativeInteger(attached.expires_in_ms) || attached.expires_in_ms === 0) throw new Error('Invalid viewer grant')
    const lease = AbortSignal.any([signal, AbortSignal.timeout(Math.max(1, Math.min(attached.expires_in_ms, 30_000)))])
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
    try {
      const connecting = new AbortController(), timeout = setTimeout(() => connecting.abort(), 3000)
      let response: Response
      try { response = await api(`${path}/stream?${query}`, { signal: AbortSignal.any([lease, connecting.signal]), headers: { Accept: 'text/event-stream', 'X-Aeon-Viewer-ID': attached.viewer_id, ...(request.lastEventId ? { 'Last-Event-ID': request.lastEventId } : {}) } }, 30_000) } finally { clearTimeout(timeout) }
      if (!response.ok || !response.body) throw { status: response.status, reason: response.status === 503 ? 'feature_disabled' : [401, 403, 404].includes(response.status) ? 'permission_revoked' : response.status === 409 ? 'cursor_expired' : 'native_failure' }
      reader = response.body.getReader()
      const decoder = new TextDecoder('utf-8', { fatal: true }); let pending = ''
      for (;;) {
        const part = await reader.read(); if (part.done || lease.aborted) break
        if (part.value.byteLength > nativeSessionBounds.browser_tail_bytes) throw new Error('Live frame exceeds bounds')
        pending = (pending + decoder.decode(part.value, { stream: true })).replace(/\r\n/g, '\n')
        let end: number
        while ((end = pending.indexOf('\n\n')) >= 0) {
          const frame = pending.slice(0, end); pending = pending.slice(end + 2)
          if (frame.length > nativeSessionBounds.exported_frame_bytes) throw new Error('Live frame exceeds bounds')
          const data = frame.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trimStart()).join('\n')
          if (data && !signal.aborted) receive(readNativeEvent(parseJson(data), request))
        }
        if (pending.length > nativeSessionBounds.exported_frame_bytes) throw new Error('Live frame exceeds bounds')
      }
    } finally {
      await reader?.cancel().catch(() => {}); reader?.releaseLock()
      await boundedJSON(`${path}/detach`, AbortSignal.timeout(3000), { request_id: crypto.randomUUID(), expected_binding: attached.binding, viewer_id: attached.viewer_id }).catch(() => {})
    }
  },
}
