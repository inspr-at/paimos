// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { boundedEvent, boundedPage, ExpertBuffer } from '../src/lib/nativeExpert/buffer'
import { expertRequest, liveTerminalLink, type ExpertConsent, type ExpertViewer } from '../src/lib/nativeExpert/ports'
import { correlations, eventRows, missingEvents } from '../src/lib/nativeExpert/rows'
import { terminalRequest } from '../src/lib/nativeTerminal/ports'
import { nativeSessionBounds as bounds, type NativeStreamEvent } from '../src/lib/nativeSessionContract'
import { binding, expertFrame, frame, request, session } from './native-runtime-fixtures'

const altered = (base: NativeStreamEvent, patch: object) => ({ ...base, ...patch }) as NativeStreamEvent
const viewer: ExpertViewer = { tenantId: binding.tenant_id, principalId: 'person-1', kind: 'person', canRead: true, canWatch: true, canReadStructured: true }
const consent = (): ExpertConsent => ({ id: 'consent-a', ownerPersonId: 'owner-1', binding: { ...binding }, active: true })
const page = (items: NativeStreamEvent[], next_before: string | null = null) => ({ generation: 1, items, next_before, truncated: false })

it('fences diagnostics by every binding dimension, generation, native source and uint64 sequence', () => {
  expect(boundedEvent(frame(), request())).toEqual(frame())
  for (const [key, value] of Object.entries(binding)) {
    expect(boundedEvent(altered(frame(), { binding: { ...binding, [key]: typeof value === 'number' ? value + 1 : `${value}-other` } }), request()), key).toBeNull()
  }
  for (const key of ['seq', 'sequence', 'dropped_frames']) for (const value of ['-1', '01', '1.0', '18446744073709551616', 1]) {
    expect(boundedEvent(altered(frame(), { [key]: value }), request()), `${key}: ${value}`).toBeNull()
  }
  for (const patch of [{ generation: 2 }, { source: 'transcript' }, { kind: 'terminal' }, { kind: 'unknown' },
    { payload: { message_id: 'message-1', text: 'Validated event', extra: true } }, { payload: { message_id: 'message-1' } },
    { payload: { message_id: 42, text: 'Validated event' } }]) {
    expect(boundedEvent(altered(frame(), patch), request()), JSON.stringify(patch)).toBeNull()
  }
  expect(boundedEvent(altered(frame(), { seq: '18446744073709551615', sequence: '18446744073709551615' }), request())).not.toBeNull()
})

it('rejects private, malformed, omitted, deep and oversized expert bodies before retaining a defensive copy', () => {
  expect(boundedEvent(expertFrame(), request())).toBeNull()
  expect(boundedEvent(expertFrame(), request('expert'))).toEqual(expertFrame())
  for (const key of ['authorization', 'access_token', 'id_token', 'refresh_token', 'token', 'api_key', 'password', 'credential',
    'cookie', 'headers', 'env', 'cwd', 'config_dir', 'home', 'mcpservers', 'thinking', 'redacted_thinking', 'reasoning', 'tool_input', 'tool_result']) {
    expect(boundedEvent(expertFrame({ nested: { [key.toUpperCase()]: 'synthetic-private-canary' } }), request('expert')), key).toBeNull()
  }
  const base = expertFrame()
  if (base.kind !== 'expert') throw new Error('expert fixture')
  for (const patch of [{ frame_json: '{' }, { omitted: true }, { redacted_paths: Array(bounds.expert_redaction_paths + 1).fill('path') },
    { original_bytes: -1 }, { redacted_paths: [42] }, { frame_json: JSON.stringify({ text: 'ü'.repeat(bounds.exported_frame_bytes / 2) }) }]) {
    expect(boundedEvent(altered(base, { payload: { ...base.payload, ...patch } }), request('expert'))).toBeNull()
  }
  expect(boundedEvent(altered(base, { payload: { ...base.payload, omitted: true, frame_json: '' } }), request('expert'))).not.toBeNull()
  let deep: unknown = 'leaf'
  for (let n = 0; n <= bounds.native_frame_decode_depth; n++) deep = { nested: deep }
  expect(boundedEvent(expertFrame(deep), request('expert'))).toBeNull()
  const input = frame(), retained = boundedEvent(input, request())!
  input.binding.runtime_epoch = 'later-runtime'
  if (input.kind === 'assistant.delta') input.payload.text = 'changed after callback'
  expect(retained).toEqual(frame())
})

it('requires exclusive descending replay and advancing cursors while enforcing count and byte limits', () => {
  expect(boundedPage(page([frame(3), frame(2)], '2'), request(), '4')?.map(event => event.seq)).toEqual(['2', '3'])
  for (const value of [page([frame(3), frame(3)]), page([frame(2), frame(3)]), page([frame(4)]),
    page([], '1'), page([frame(3)], '4'), { ...page([frame(3)]), generation: 2 }]) {
    expect(boundedPage(value, request(), '4')).toBeNull()
  }
  // No cursor can explain this rejection: all 501 frames are valid and
  // descending, so only the count limit can reject the otherwise valid page.
  const tooMany = page(Array.from({ length: bounds.event_page_items + 1 }, (_, n) => frame(1000 - n)))
  expect(boundedPage(tooMany, request(), null)).toBeNull()
  const large = Array.from({ length: 25 }, (_, n) => altered(frame(30 - n), { payload: { message_id: `large-${n}`, text: 'x'.repeat(50_000) } }))
  expect(large.every(event => boundedEvent(event, request()) !== null)).toBe(true)
  expect(boundedPage(page(large), request(), null)).toBeNull()
  expect(boundedPage(page([]), request(), null)).toEqual([])
})

it('deduplicates and bounds retained diagnostics without sacrificing the live end to earlier pages', () => {
  const buffer = new ExpertBuffer()
  buffer.add([frame(3), frame(1), frame(2), frame(3)])
  expect(buffer.events.map(event => event.seq)).toEqual(['1', '2', '3'])
  const bytes = buffer.bytes
  buffer.add([frame(3)])
  expect(buffer.bytes).toBe(bytes)
  buffer.clear()
  buffer.add(Array.from({ length: bounds.event_page_items + 1 }, (_, n) => frame(n + 2)))
  expect(buffer.events).toHaveLength(bounds.event_page_items)
  expect(buffer.events[0]?.seq).toBe('3')
  expect(buffer.evicted).toBe(1)
  buffer.add([frame(1)])
  expect(buffer.events.at(-1)?.seq).toBe(String(bounds.event_page_items + 2))
  expect(buffer.events[0]?.seq).toBe('3')
  expect(buffer.evicted).toBe(2)
  buffer.clear()
  buffer.add(Array.from({ length: 12 }, (_, n) => altered(frame(n + 1), { payload: { message_id: String(n), text: 'x'.repeat(50_000) } })))
  expect(buffer.bytes).toBeLessThanOrEqual(bounds.browser_tail_bytes)
  expect(buffer.evicted).toBeGreaterThan(0)
  expect(buffer.events.at(-1)?.seq).toBe('12')
  buffer.clear()
  expect([buffer.events, buffer.bytes, buffer.evicted]).toEqual([[], 0, 0])
})

it('coalesces only explicit message and call identities and keeps final, receipt, quota and gap evidence honest', () => {
  const events: NativeStreamEvent[] = [
    altered(frame(1), { payload: { message_id: 'message-1', text: 'First ' } }),
    altered(frame(2), { payload: { message_id: 'message-1', text: 'delta' } }),
    { ...frame(3), kind: 'assistant.final', payload: { message_id: 'message-1', input_id: 'input-1', inferred: true, text: 'Explicit final' } },
    { ...frame(4), kind: 'tool.start', payload: { call_id: 'call-1', name: 'Read', args_summary: 'Approved summary' } },
    { ...frame(5), kind: 'tool.result', payload: { call_id: 'call-1', result_summary: 'Finished summary', duration_ms: 400 } },
    { ...frame(6), kind: 'receipt', payload: { request_id: 'request-1', operation_id: 'operation-1', binding, operation: 'input',
      state: 'uncertain', input_id: 'input-1', turn_ref: 'turn-1', source_sequence: '5', updated_at: frame().observed_at, reason: 'input_outcome_unconfirmed' } },
    { ...frame(7), kind: 'gap', payload: { reason: 'cursor_expired', dropped_frames: '12', reload_snapshot: true } },
  ]
  const rows = eventRows(events, 'codex')
  expect(rows.map(row => [row.name, row.text, row.measure, row.wireName])).toEqual([
    ['Codex', 'Explicit final', '14 chars', 'assistant.final'],
    ['Read', 'Approved summary · Finished summary', '400 ms', 'tool.result'],
    ['Receipt', 'Input · uncertain', '', 'receipt'], ['Gap', '12 events are missing from this live view.', '', 'gap'],
  ])
  expect(rows[0]?.events).toEqual(events.slice(0, 3))
  expect(correlations(rows[0]!)).toContainEqual(['input_id', 'input-1'])
  expect(eventRows([frame(), altered(frame(2), { generation: 2 })], 'codex')).toHaveLength(2)
  expect(missingEvents('1')).toBe('1 event is missing from this live view.')
  // Retain every original quotaCases vector from native-expert-stream.
  for (const [scope, state, used_percent, text, measure] of [
    ['context', 'known', 23, 'Context usage', '23 % of context'], ['account', 'known', 62, 'Account usage', '62 %'],
    ['account', 'limited', 100, 'Account usage · limited', '100 %'], ['context', 'limited', 100, 'Context usage · limited', '100 % of context'],
    ['account', 'stale', 62, 'Account usage · stale', '62 %'], ['context', 'stale', 23, 'Context usage · stale', '23 % of context'],
    ['account', 'unknown', null, 'Account usage · unknown', ''], ['context', 'unsupported', null, 'Context usage · unsupported', ''],
    ['context', 'unknown', null, 'Context usage · unknown', ''],
  ] as const) {
    const quota: NativeStreamEvent = { ...frame(), kind: 'quota', payload: { account_id: 'account-1', profile_binding_revision: 1,
      source: 'codex_stream', scope, state, used_percent, observed_at: frame().observed_at, fresh_until: frame().observed_at, resets_at: null } }
    expect(eventRows([quota], 'codex')[0]).toMatchObject({ type: 'usage', name: 'Usage', text, measure, wireName: 'quota' })
  }
})

it('derives view-only requests from current person, permission, consent and binding without inheriting terminal authority', () => {
  const current = session(), approved = consent()
  expect(expertRequest(current, viewer, approved)?.view).toBe('expert')
  expect(expertRequest(current, { ...viewer, kind: 'agent' }, approved)).toMatchObject({ view: 'structured', consentId: null })
  for (const invalid of [null, { ...approved, active: false }, { ...approved, ownerPersonId: 'other-owner' },
    { ...approved, binding: { ...binding, runtime_epoch: 'other-runtime' } }]) {
    expect(expertRequest(current, viewer, invalid)).toMatchObject({ view: 'structured', consentId: null })
    expect(terminalRequest({ ...current, state: 'taken', mode: 'terminal' }, viewer, invalid)).toBeNull()
  }
  for (const who of [{ ...viewer, canRead: false }, { ...viewer, tenantId: 'other-tenant' }, { ...viewer, principalId: '' }]) {
    expect(expertRequest(current, who, approved)).toBeNull()
    expect(terminalRequest({ ...current, state: 'taken', mode: 'terminal' }, who, approved)).toBeNull()
  }
  expect(terminalRequest(current, viewer, approved)).toBeNull()
  const terminal = terminalRequest({ ...current, state: 'taken', mode: 'terminal' }, viewer, approved)!
  expect(terminal).toMatchObject({ view: 'terminal', mode: 'view', binding, principalId: 'person-1', consentId: 'consent-a' })
  expect(Object.isFrozen(terminal)).toBe(true)
  expect(Object.isFrozen(terminal.binding)).toBe(true)
  expect(terminal).not.toHaveProperty('control_capability')
  for (const value of ['http://terminal.example.test/view', 'https://user:synthetic@terminal.example.test/view',
    'https://terminal.example.test/view?cap=synthetic', 'https://terminal.example.test/view#input', 'x'.repeat(2049)]) {
    expect(liveTerminalLink({ ...current, terminal_viewer_url: value }, viewer)).toBeNull()
  }
  expect(liveTerminalLink(current, viewer)).toMatchObject({ target: '_blank', rel: 'noopener noreferrer' })
  expect(liveTerminalLink(current, { ...viewer, kind: 'agent' })).toBeNull()
})
