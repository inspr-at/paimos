// SPDX-License-Identifier: AGPL-3.0-only
import { nativeSessionBounds as bounds, nativeSessionPayloadSchemas, type NativeEventPage, type NativeStreamEvent } from '../nativeSessionContract'
import { bindingKey, type ExpertRequest } from './ports'

const encoder = new TextEncoder()
const uint64 = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9][0-9]{0,19})$/.test(value) && BigInt(value) <= 18446744073709551615n
const denied = new Set(['authorization', 'access_token', 'id_token', 'refresh_token', 'token', 'api_key', 'password', 'credential', 'cookie', 'headers', 'env', 'cwd', 'config_dir', 'home', 'mcpservers', 'thinking', 'redacted_thinking', 'reasoning', 'tool_input', 'tool_result'])
const payloadFields: Record<string, string> = {
  expert: 'provider event_name native_thread_ref native_turn_ref frame_json redacted_paths omitted original_bytes',
  structured: 'kind text reference state',
  receipt: 'request_id operation_id binding operation state input_id turn_ref source_sequence updated_at reason',
  approval: 'request_id binding turn_ref native_request_ref kind effect_digest summary allowed_decisions expires_at desk_item_id step_up_required approval_id expires_in_ms',
  controller: 'controller_id actor_id client_id epoch expires_at',
  quota: 'account_id profile_binding_revision source scope state used_percent observed_at fresh_until resets_at',
  gap: 'reason dropped_frames reload_snapshot', end: 'reason reload_snapshot',
  snapshot: 'owned_session_id current_binding owner_person_id role profile harness state mode capabilities revision conversation_ref predecessor_harness_session_id controller quota last_receipt reason generation terminal_viewer_url',
  'assistant.delta': 'message_id text', 'assistant.final': 'message_id input_id inferred text',
  'tool.start': 'call_id name args_summary', 'tool.result': 'call_id result_summary duration_ms',
  'input.delivered': 'input_id from level text',
  'input.not_confirmed': 'input_id retryable', 'input.generation_changed': 'input_id retryable', 'input.session_ended': 'input_id retryable', 'input.refused': 'input_id retryable',
  'stream.paused': 'reason generation', 'control.lease_expired': 'generation', 'control.ended_by': 'by reason',
}
function validPayload(event: NativeStreamEvent): boolean {
  const payload = event.payload as unknown as Record<string, unknown>, fields = payloadFields[event.kind]?.split(' ')
  if (!fields || Object.keys(payload).length !== fields.length || !fields.every(key => Object.hasOwn(payload, key))) return false
  const strings = (...keys: string[]) => keys.every(key => typeof payload[key] === 'string')
  switch (event.kind) {
    case 'expert': return strings('provider', 'event_name', 'frame_json') && typeof payload.omitted === 'boolean' && Number.isSafeInteger(payload.original_bytes) && Number(payload.original_bytes) >= 0 && Array.isArray(payload.redacted_paths) && payload.redacted_paths.every(value => typeof value === 'string')
    case 'assistant.delta': return strings('message_id', 'text')
    case 'assistant.final': return strings('message_id', 'text') && typeof payload.inferred === 'boolean'
    case 'tool.start': return strings('call_id', 'name')
    case 'tool.result': return strings('call_id') && Number.isSafeInteger(payload.duration_ms) && Number(payload.duration_ms) >= 0
    case 'input.delivered': return strings('input_id', 'text', 'level') && !!payload.from && typeof payload.from === 'object'
    case 'input.not_confirmed': case 'input.generation_changed': case 'input.session_ended': case 'input.refused': return strings('input_id') && typeof payload.retryable === 'boolean'
    case 'receipt': return strings('operation_id', 'request_id', 'operation', 'state') && uint64(payload.source_sequence)
    case 'snapshot': return strings('owned_session_id', 'state', 'mode')
    case 'gap': return strings('reason') && uint64(payload.dropped_frames) && typeof payload.reload_snapshot === 'boolean'
    case 'end': return strings('reason') && typeof payload.reload_snapshot === 'boolean'
    case 'control.ended_by': return strings('reason') && !!payload.by && typeof payload.by === 'object'
    default: return true
  }
}
function safeTree(value: unknown): boolean {
  let units = 0
  function visit(value: unknown, depth: number): boolean {
    if (depth > bounds.native_frame_decode_depth || (units += 4) > bounds.exported_frame_bytes) return false
    if (typeof value === 'string') return (units += value.length) <= bounds.exported_frame_bytes
    if (!value || typeof value !== 'object') return true
    for (const key in value) {
      if (!Object.hasOwn(value, key)) continue
      if ((units += key.length) > bounds.exported_frame_bytes || denied.has(key.toLowerCase()) || !visit((value as Record<string, unknown>)[key], depth + 1)) return false
    }
    return true
  }
  return visit(value, 0)
}

/** The port is typed and already privacy-filtered. These bounds also fence faulty adapters. */
export function boundedEvent(event: NativeStreamEvent, request: ExpertRequest): NativeStreamEvent | null {
  if (!uint64(event.seq) || !uint64(event.sequence) || !uint64(event.dropped_frames) ||
    event.generation !== request.generation || bindingKey(event.binding) !== bindingKey(request.binding) ||
    event.source !== 'native' || event.kind === 'terminal' || !Object.hasOwn(nativeSessionPayloadSchemas, event.kind) ||
    !event.payload || typeof event.payload !== 'object' || !safeTree(event) || !validPayload(event)) return null
  const json = JSON.stringify(event)
  if (json.length > bounds.exported_frame_bytes || encoder.encode(json).length > bounds.exported_frame_bytes) return null
  if (event.kind === 'snapshot') {
    if (event.payload.owned_session_id !== request.binding.owned_session_id ||
      event.payload.generation !== request.generation || bindingKey(event.payload.current_binding) !== bindingKey(request.binding) ||
      (request.view === 'structured' && event.payload.terminal_viewer_url !== null)) return null
  }
  if (event.kind === 'receipt' && event.payload.binding && bindingKey(event.payload.binding) !== bindingKey(request.binding)) return null
  if (event.kind === 'approval' && bindingKey(event.payload.binding) !== bindingKey(request.binding)) return null
  if (event.kind === 'expert') {
    if (request.view !== 'expert') return null
    const frame = event.payload
    if (typeof frame.frame_json !== 'string' || !Array.isArray(frame.redacted_paths) || frame.redacted_paths.length > bounds.expert_redaction_paths) return null
    if (frame.omitted && frame.frame_json !== '') return null
    if (!frame.omitted) {
      try { if (!safeTree(JSON.parse(frame.frame_json))) return null } catch { return null }
    }
  }
  // The caller cannot mutate retained frames after delivering a callback.
  return JSON.parse(json) as NativeStreamEvent
}

export class ExpertBuffer {
  events: NativeStreamEvent[] = []
  bytes = 0
  evicted = 0
  private sizes = new Map<string, number>()
  clear() { this.events = []; this.bytes = 0; this.evicted = 0; this.sizes.clear() }
  add(events: NativeStreamEvent[]) {
    for (const event of events) {
      if (this.sizes.has(event.seq)) continue
      const size = encoder.encode(JSON.stringify(event)).length
      this.sizes.set(event.seq, size); this.bytes += size; this.events.push(event)
    }
    this.events.sort((a, b) => BigInt(a.seq) < BigInt(b.seq) ? -1 : BigInt(a.seq) > BigInt(b.seq) ? 1 : 0)
    // An earlier page may not evict the live end. Live arrivals evict the oldest.
    while (this.events.length > bounds.event_page_items || this.bytes > bounds.browser_tail_bytes) {
      const event = this.events.shift()!
      this.bytes -= this.sizes.get(event.seq)!; this.sizes.delete(event.seq); this.evicted++
    }
  }
}

export function boundedPage(page: NativeEventPage, request: ExpertRequest, before: string | null): NativeStreamEvent[] | null {
  if (page.generation !== request.generation || !Array.isArray(page.items) || page.items.length > bounds.event_page_items ||
    typeof page.truncated !== 'boolean' || (page.next_before !== null && !uint64(page.next_before))) return null
  let previous = before === null ? 18446744073709551616n : BigInt(before)
  let bytes = 0
  const result: NativeStreamEvent[] = []
  for (const event of page.items) {
    const safe = boundedEvent(event, request)
    if (!safe || BigInt(safe.seq) >= previous) return null
    bytes += encoder.encode(JSON.stringify(safe)).length
    if (bytes > bounds.event_page_bytes) return null
    previous = BigInt(safe.seq); result.push(safe)
  }
  // C0's next_before is exclusive and must make progress, even on empty pages.
  if (page.next_before !== null && (page.items.length === 0 || BigInt(page.next_before) > previous || (before !== null && BigInt(page.next_before) >= BigInt(before)))) return null
  return result.reverse()
}
