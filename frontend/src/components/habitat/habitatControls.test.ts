import { describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import {
  parseAssignmentHistory,
  parseHabitatControlOutcome,
  parseHabitatControlReceipt,
  parseHabitatMessageReceipt,
  sendHabitatMessage,
} from './habitatControls'
const id = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, '0')}`
const now = '2026-09-06T10:00:00Z'
const pending = {
  id: id(1),
  harness_session_id: id(2),
  sequence: 1,
  kind: 'stop',
  state: 'pending',
  requested_at: now,
  requested_by_user_id: 1,
}
describe('Strict control and assignment evidence', () => {
  it('binds a human message acknowledgement to the selected generation, revision, and level', async () => {
    const utterance = 'utt_0123456789abcdef0123456789abcdef'
    const wire = {
      schema_version: 1,
      utterance_id: utterance,
      harness_session_id: id(2),
      harness_session_revision: 4,
      message_id: id(7),
      delivery_id: id(8),
      delivery_level: 'steer',
      created_at: now,
    }
    const post = vi.spyOn(api, 'post').mockResolvedValue(wire)
    await expect(sendHabitatMessage(1, id(2), 4, utterance, 'Line one\nLine two', 'steer'))
      .resolves.toEqual({ messageId: id(7), deliveryId: id(8), deliveryLevel: 'steer', revision: 4 })
    expect(post).toHaveBeenCalledWith(
      `/projects/1/harness-sessions/${id(2)}/messages/v1`,
      {
        schema_version: 1,
        utterance_id: utterance,
        expected_revision: 4,
        text: 'Line one\nLine two',
        delivery_level: 'steer',
      },
      { signal: undefined },
    )
    expect(() => parseHabitatMessageReceipt({ ...wire, delivery_level: 'simple' }, id(2), utterance, 4, 'steer')).toThrow()
    expect(() => parseHabitatMessageReceipt({ ...wire, agent_name: 'impersonated' }, id(2), utterance, 4, 'steer')).toThrow()
  })
  it('does not confuse a pending receipt with execution and rejects mismatched or unproved outcomes', () => {
    const control = parseHabitatControlReceipt(
      { schema_version: 1, control: pending, state: 'requested' },
      id(2),
      'stop',
    )
    expect(control.state).toBe('pending')
    expect(control.outcome).toBeNull()
    const { requested_by_user_id: _user, ...base } = pending
    const result = {
      ...base,
      project_id: 1,
      correlation_id: id(1),
      state: 'applied',
      outcome: 'applied',
      reason: 'applied',
      completed_at: now,
    }
    expect(parseHabitatControlOutcome(result, 1, id(2), control).outcome).toBe('applied')
    for (const changed of [
      { ...result, project_id: 2 },
      { ...result, harness_session_id: id(3) },
      { ...result, correlation_id: id(3) },
      { ...result, completed_at: undefined },
      { ...result, outcome: 'rejected' },
      { ...result, private_field: 'fixture-canary' },
    ])
      expect(() => parseHabitatControlOutcome(changed, 1, id(2), control)).toThrow()
  })
  it('requires ordered bounded history for the selected worker and validates before/after bindings', () => {
    const event = {
      revision: 2,
      created_at: now,
      before_parent_harness_session_id: null,
      after_parent_harness_session_id: id(3),
      before_ticket_id: null,
      after_ticket_id: 4,
      before_work_shape: null,
      after_work_shape: 'ship',
    }
    const page = { schema_version: 1, session_id: id(2), events: [event], next_after_revision: 2 }
    expect(parseAssignmentHistory(page, id(2)).events[0].after_ticket_id).toBe(4)
    for (const changed of [
      { ...page, session_id: id(3) },
      { ...page, events: [event, event] },
      { ...page, events: [{ ...event, after_parent_harness_session_id: id(2) }] },
      { ...page, events: [{ ...event, after_ticket_id: null }] },
      { ...page, next_after_revision: 99 },
    ])
      expect(() => parseAssignmentHistory(changed, id(2))).toThrow()
    expect(() => parseAssignmentHistory(page, id(2), 2)).toThrow()
  })
})
