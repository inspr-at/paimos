// SPDX-License-Identifier: AGPL-3.0-only
import type { NativeStreamEvent } from '../nativeSessionContract'
import type { IconName } from '../../components/AppIcon.vue'

export const eventTypes = [
  { key: 'text', label: 'Text', icon: 'message' }, { key: 'tools', label: 'Tools', icon: 'wrench' },
  { key: 'status', label: 'Status', icon: 'pulse' }, { key: 'asks', label: 'Asks', icon: 'shield' },
  { key: 'usage', label: 'Usage', icon: 'gauge' }, { key: 'lifecycle', label: 'Lifecycle', icon: 'history' },
] as const satisfies readonly { key: string; label: string; icon: IconName }[]
export type EventType = typeof eventTypes[number]['key']
export interface EventRow {
  id: string; type: EventType; name: string; text: string; measure: string; events: NativeStreamEvent[]
}
function category(event: NativeStreamEvent): EventType {
  if (event.kind.startsWith('assistant.') || event.kind.startsWith('input.')) return 'text'
  if (event.kind.startsWith('tool.')) return 'tools'
  if (event.kind === 'approval') return 'asks'
  if (event.kind === 'quota') return 'usage'
  if (event.kind === 'structured') return event.payload.kind === 'tool_status' ? 'tools' : event.payload.kind === 'assistant_delta' ? 'text' : 'status'
  if (event.kind === 'expert') {
    const name = event.payload.event_name
    if (/tool/.test(name)) return 'tools'
    if (/rate_limit|usage|token/.test(name)) return 'usage'
    if (/assistant|stream_event|agentMessage/.test(name)) return 'text'
    return 'status'
  }
  return event.kind === 'snapshot' ? 'status' : 'lifecycle'
}
function description(event: NativeStreamEvent): [string, string, string] {
  switch (event.kind) {
    case 'expert': return [event.payload.event_name, event.payload.provider, event.payload.omitted ? `Omitted · ${event.payload.original_bytes} bytes` : `${event.payload.redacted_paths.length} redactions`]
    case 'assistant.delta': return [event.kind, event.payload.text, 'Writing']
    case 'assistant.final': return [event.kind, event.payload.text, event.payload.inferred ? 'Reply correlation inferred' : '']
    case 'tool.start': return [event.kind, [event.payload.name, event.payload.args_summary].filter(Boolean).join(' · '), 'Running']
    case 'tool.result': return [event.kind, event.payload.result_summary ?? '', `${event.payload.duration_ms} ms`]
    case 'input.delivered': return [event.kind, event.payload.text, event.payload.level]
    case 'input.not_confirmed': case 'input.generation_changed': case 'input.session_ended': case 'input.refused':
      return [event.kind, event.payload.input_id, event.payload.retryable ? 'Retryable' : 'Final']
    case 'receipt': return [event.kind, `${event.payload.operation} · ${event.payload.state}`, event.payload.reason ?? '']
    case 'approval': return [event.kind, event.payload.summary, event.payload.kind]
    case 'quota': return [event.kind, `${event.payload.source} · ${event.payload.state}`, event.payload.used_percent === null ? '' : `${event.payload.used_percent}% used`]
    case 'snapshot': return [event.kind, `${event.payload.state} · ${event.payload.mode}`, `Generation ${event.generation}`]
    case 'structured': return [event.kind, event.payload.text ?? event.payload.state ?? event.payload.reference ?? '', event.payload.kind]
    case 'gap': return [event.kind, event.payload.reason, `${event.payload.dropped_frames} frames missing`]
    case 'end': return [event.kind, event.payload.reason, '']
    case 'stream.paused': return [event.kind, 'Structured events pause while a person has control.', '']
    case 'control.ended_by': return [event.kind, event.payload.reason, event.payload.by.id]
    case 'controller': return [event.kind, event.payload.actor_id, event.payload.expires_at]
    default: return [event.kind, '', '']
  }
}
/** Coalesce only explicit C0 message/call identities, retaining every bounded raw frame. */
export function eventRows(events: NativeStreamEvent[]): EventRow[] {
  const rows: EventRow[] = [], paired = new Map<string, EventRow>()
  for (const event of events) {
    const message = event.kind === 'assistant.delta' || event.kind === 'assistant.final' ? event.payload.message_id : null
    const call = event.kind === 'tool.start' || event.kind === 'tool.result' ? event.payload.call_id : null
    const id = message ? `message:${event.generation}:${message}` : call ? `call:${event.generation}:${call}` : `event:${event.generation}:${event.seq}`
    const [name, text, measure] = description(event), existing = paired.get(id)
    if (existing) {
      existing.events.push(event); existing.name = name; existing.measure = measure
      existing.text = event.kind === 'assistant.delta' ? existing.text + text : event.kind === 'tool.result' ? `${existing.text} · ${text}` : text
    } else {
      const row = { id, type: category(event), name, text, measure, events: [event] }
      rows.push(row); paired.set(id, row)
    }
  }
  return rows
}

export function correlations(row: EventRow): [string, string][] {
  const event = row.events.at(-1)!, data = event.payload as unknown as Record<string, unknown>
  return [['Generation', String(event.generation)], ['Source', event.source], ...[
    'message_id', 'call_id', 'input_id', 'operation_id', 'request_id', 'turn_ref', 'native_thread_ref', 'native_turn_ref',
  ].flatMap(key => typeof data[key] === 'string' ? [[key, data[key] as string]] : [])] as [string, string][]
}
