// SPDX-License-Identifier: AGPL-3.0-only
import type { NativeBinding, NativeSessionView, NativeStreamEvent } from '../src/lib/nativeSessionContract'
import type { ExpertRequest } from '../src/lib/nativeExpert/ports'

export const binding: NativeBinding = {
  tenant_id: 'tenant-1', project_id: 'project-1', owned_session_id: 'owned-a', harness_session_id: 'harness-a',
  runtime_epoch: 'runtime-a', daemon_id: 'daemon-1', daemon_generation: 'daemon-generation-1', binding_revision: 1, run_id: null,
}
export const request = (view: ExpertRequest['view'] = 'structured'): ExpertRequest => ({
  binding: { ...binding }, generation: 1, revision: 1, principalId: 'person-1', view,
  consentId: view === 'expert' ? 'consent-a' : null, lastEventId: null,
})
export const session = (): NativeSessionView => ({
  owned_session_id: binding.owned_session_id, current_binding: { ...binding }, owner_person_id: 'owner-1',
  role: 'worker', profile: 'native', harness: 'codex', state: 'running', mode: 'structured', revision: 1, generation: 1,
  capabilities: { input: true, steer: 'native', interrupt: true, stop: true, resume: true, take: true, expert: true,
    terminal_view: true, approvals: true, questions: true, correlated_receipts: true, quota: 'stream',
    qualification_digest: 'qualified-test', take_supported: true, events_supported: true, interrupt_supported: true, chat_supported: true },
  conversation_ref: 'conversation-opaque', predecessor_harness_session_id: null, controller: null, quota: null, last_receipt: null,
  reason: null, terminal_viewer_url: 'https://terminal.example.test/session-a',
})
export const frame = (seq: number | string = 1): NativeStreamEvent => ({
  binding: { ...binding }, generation: 1, seq: String(seq), sequence: String(seq), source: 'native',
  observed_at: '2026-10-13T02:03:04Z', dropped_frames: '0', kind: 'assistant.delta',
  payload: { message_id: 'message-1', text: 'Validated event' },
})
export const expertFrame = (value: unknown = { type: 'tool_progress', tool_name: 'Read', duration_ms: 100 }): NativeStreamEvent => ({
  ...frame(), kind: 'expert', payload: { provider: 'claude', event_name: 'tool_progress', native_thread_ref: 'opaque-thread',
    native_turn_ref: 'opaque-turn', frame_json: JSON.stringify(value), redacted_paths: ['session_id'], omitted: false, original_bytes: 140 },
})
