// SPDX-License-Identifier: AGPL-3.0-only
import type { NativeStreamEvent } from '../nativeSessionContract'
import type { IconName } from '../../components/AppIcon.vue'
import { harnessLabel } from '../agentState'

export const eventTypes = [
  { key: 'text', label: 'Text', icon: 'message' }, { key: 'tools', label: 'Tools', icon: 'wrench' },
  { key: 'status', label: 'Status', icon: 'pulse' }, { key: 'asks', label: 'Asks', icon: 'shield' },
  { key: 'usage', label: 'Usage', icon: 'gauge' }, { key: 'lifecycle', label: 'Lifecycle', icon: 'history' },
] as const satisfies readonly { key: string; label: string; icon: IconName }[]
export type EventType = typeof eventTypes[number]['key']
export interface EventRow {
  id: string; type: EventType; name: string; text: string; measure: string; wireName: string; events: NativeStreamEvent[]
}
const statusNames: Record<string, string> = {
  accepted: 'Starting', waiting_for_exit: 'Waiting for exit', starting: 'Starting', idle: 'Idle', running: 'Running',
  requires_action: 'Needs you', quiescing: 'Pausing', taken: 'Person has control', returning: 'Returning control',
  recovering: 'Recovering', ownership_lost: 'Ownership lost', stopping: 'Stopping', stopped: 'Stopped', failed: 'Failed',
}
const operationNames: Record<string, string> = {
  start: 'Start', resume: 'Resume', input: 'Input', interrupt: 'Interrupt', stop: 'Stop', attach: 'Attach', renew: 'Renew',
  detach: 'Detach', take: 'Take control', return: 'Return control', approval: 'Approval', pty_input: 'Terminal input',
  resize: 'Terminal resize', control_end: 'End control',
}
const receiptStates: Record<string, string> = {
  accepted: 'accepted', queued: 'queued', write_started: 'write started', write_acknowledged: 'write acknowledged',
  turn_started: 'turn started', turn_completed: 'turn completed', applied: 'applied', exit_confirmed: 'exit confirmed',
  uncertain: 'uncertain', rejected: 'rejected', expired: 'expired',
}
export const missingEvents = (count: string | number | bigint) => `${count} ${String(count) === '1' ? 'event is' : 'events are'} missing from this live view.`
export const endText = (reason: string) => ({
  detached: 'Stopped watching. Watch live to continue.', stopped: 'Stopped · no more events.',
  rotation: 'The live connection changed. Watch live to reconnect.', unsupported: 'Live events are unavailable.',
  runtime_changed: 'The session runtime changed. Reopen this view to choose the current session.',
}[reason] ?? 'The live connection ended. Watch live to reconnect.')
function category(event: NativeStreamEvent): EventType {
  if (event.kind.startsWith('assistant.') || event.kind.startsWith('input.')) return 'text'
  if (event.kind.startsWith('tool.')) return 'tools'
  if (event.kind === 'approval') return 'asks'
  if (event.kind === 'quota') return 'usage'
  if (event.kind === 'structured') return event.payload.kind === 'tool_status' ? 'tools' : ['assistant_delta', 'final_reference'].includes(event.payload.kind) ? 'text' : 'status'
  if (event.kind === 'expert') {
    const name = event.payload.event_name
    if (/approval|question/.test(name)) return 'asks'
    if (/lifecycle|^session\.(started|resumed|interrupted|stopped|exited)$/.test(name)) return 'lifecycle'
    if (/tool/.test(name)) return 'tools'
    if (/rate_limit|usage|token/.test(name)) return 'usage'
    if (/assistant|stream_event|agentMessage/.test(name)) return 'text'
    return 'status'
  }
  return event.kind === 'snapshot' ? 'status' : 'lifecycle'
}
function nativeDescription(event: Extract<NativeStreamEvent, { kind: 'expert' }>): [string, string, string] {
  const frame = event.payload
  let data: Record<string, unknown> = {}
  if (!frame.omitted) {
    try {
      const parsed: unknown = JSON.parse(frame.frame_json)
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) data = parsed as Record<string, unknown>
    } catch { /* The bounded envelope remains available in details. */ }
  }
  const text = typeof data.text === 'string' ? data.text : typeof data.summary === 'string' ? data.summary : ''
  switch (category(event)) {
    case 'text': return [harnessLabel(frame.provider), text, text ? `${text.length} chars` : '']
    case 'tools': return [typeof data.tool_name === 'string' ? data.tool_name : 'Tool', text,
      typeof data.duration_ms === 'number' && Number.isFinite(data.duration_ms) && data.duration_ms >= 0 ? `${data.duration_ms} ms` : '']
    case 'usage': return ['Usage', text, typeof data.used_percent === 'number' && Number.isFinite(data.used_percent) ? `${data.used_percent} % of context` : '']
    case 'asks': return [/question/.test(frame.event_name) ? 'Question' : 'Approval', text, '']
    case 'lifecycle': {
      const kind = frame.event_name.split('.').at(-1)!
      const label = ({ started: 'Started', resumed: `Resumed · generation ${event.generation}`, interrupted: 'Interrupted', stopped: 'Stopped', exited: 'Exited' } as Record<string, string>)[kind]
      return [label ?? 'Lifecycle', text, '']
    }
    default: return [typeof data.state === 'string' ? statusNames[data.state] ?? 'Status' : 'Native event', text || (frame.omitted ? 'Native body omitted' : 'Native diagnostics'), '']
  }
}
function description(event: NativeStreamEvent, harness: string): [string, string, string] {
  switch (event.kind) {
    case 'expert': return nativeDescription(event)
    case 'assistant.delta': case 'assistant.final': return [harnessLabel(harness), event.payload.text, `${event.payload.text.length} chars`]
    case 'tool.start': return [event.payload.name, event.payload.args_summary ?? '', 'Running']
    case 'tool.result': return ['Tool', event.payload.result_summary ?? '', `${event.payload.duration_ms} ms`]
    case 'input.delivered': return ['Delivered', event.payload.text, event.payload.level === 'immediate' ? 'Mid-turn' : 'Next step']
    case 'input.not_confirmed': case 'input.generation_changed': case 'input.session_ended': case 'input.refused':
      return [{ 'input.not_confirmed': 'Delivery not confirmed', 'input.generation_changed': 'Session restarted', 'input.session_ended': 'Session ended', 'input.refused': 'Delivery refused' }[event.kind], event.payload.input_id, event.payload.retryable ? 'Retryable' : 'Final']
    case 'receipt': {
      const { operation, state } = event.payload
      const confirmed = ['applied', 'turn_started', 'turn_completed', 'exit_confirmed'].includes(state)
      const label = confirmed ? ({ start: 'Started', resume: `Resumed · generation ${event.generation}`, interrupt: 'Interrupted', stop: 'Stopped' } as Record<string, string>)[operation] : null
      return [label ?? 'Receipt', `${operationNames[operation] ?? 'Operation'} · ${receiptStates[state] ?? 'pending'}`, '']
    }
    case 'approval': return [event.payload.kind === 'question' ? 'Question' : 'Approval', event.payload.summary, '']
    case 'quota': {
      const { scope, state, used_percent } = event.payload
      const label = scope.replace(/[_-]/g, ' ')
      const usage = `${label.charAt(0).toUpperCase() + label.slice(1)} usage`
      return ['Usage', state === 'known' ? usage : `${usage} · ${state}`,
        used_percent === null ? '' : `${used_percent} %${scope === 'context' ? ' of context' : ''}`]
    }
    case 'snapshot': return [statusNames[event.payload.state] ?? 'Status', '', '']
    case 'structured': {
      const frame = event.payload
      const label = frame.kind === 'assistant_delta' || frame.kind === 'final_reference' ? harnessLabel(harness) : frame.kind === 'tool_status' ? 'Tool' : statusNames[frame.state ?? ''] ?? 'Status'
      return [label, frame.text ?? frame.reference ?? '', frame.kind === 'assistant_delta' && frame.text ? `${frame.text.length} chars` : '']
    }
    case 'gap': return ['Gap', missingEvents(event.payload.dropped_frames), '']
    case 'end': return [event.payload.reason === 'stopped' ? 'Stopped' : 'Viewing ended', endText(event.payload.reason), '']
    case 'stream.paused': return ['Paused', 'Structured events pause while a person has control.', '']
    case 'control.ended_by': return ['Control ended', '', '']
    case 'controller': return ['Person has control', '', '']
    case 'control.lease_expired': return ['Control expired', '', '']
    default: return ['Event', '', '']
  }
}
/** Coalesce only explicit C0 message/call identities, retaining every bounded raw frame. */
export function eventRows(events: NativeStreamEvent[], harness: string): EventRow[] {
  const rows: EventRow[] = [], paired = new Map<string, EventRow>()
  for (const event of events) {
    const message = event.kind === 'assistant.delta' || event.kind === 'assistant.final' ? event.payload.message_id : null
    const call = event.kind === 'tool.start' || event.kind === 'tool.result' ? event.payload.call_id : null
    const id = message ? `message:${event.generation}:${message}` : call ? `call:${event.generation}:${call}` : `event:${event.generation}:${event.seq}`
    const [name, text, measure] = description(event, harness), existing = paired.get(id)
    const wireName = event.kind === 'expert' ? event.payload.event_name : event.kind
    if (existing) {
      existing.events.push(event); existing.wireName = wireName; existing.measure = measure
      if (event.kind !== 'tool.result') existing.name = name
      existing.text = event.kind === 'assistant.delta' ? existing.text + text : event.kind === 'tool.result' ? `${existing.text} · ${text}` : text
      if (event.kind === 'assistant.delta' || event.kind === 'assistant.final') existing.measure = `${existing.text.length} chars`
    } else {
      const row = { id, type: category(event), name, text, measure, wireName, events: [event] }
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
