import { describe, expect, it } from 'vitest'
import { habitatFixture } from './__fixtures__/orchestration'
import {
  canRemoveWorker,
  canFinishThenRemove,
  canStopNowRemove,
  FINISH_THEN_REMOVE_UNAVAILABLE,
  HANDOFF_UNAVAILABLE,
  parseHabitatRetirementReceipt,
  RETIRE_AFTER_WORK_CONTROL,
} from './habitatRemoval'

describe('habitatRemoval capability gates', () => {
  it('requires advertised stop capability before offering removal', () => {
    const fixture = habitatFixture('100', 1)
    const worker = fixture.fleet.workers[0]
    worker.capabilities.stop = true
    expect(canRemoveWorker(worker, true, true)).toBe(true)
    expect(canFinishThenRemove(worker, true, true)).toBe(true)
    expect(canStopNowRemove(worker, true, true)).toBe(true)
    worker.capabilities.stop = false
    expect(canRemoveWorker(worker, true, true)).toBe(false)
  })

  it('documents capability-gated finish, unsupported handoff, and the retire control contract', () => {
    expect(FINISH_THEN_REMOVE_UNAVAILABLE).toContain('owned stop')
    expect(HANDOFF_UNAVAILABLE).toContain('not available')
    expect(RETIRE_AFTER_WORK_CONTROL.path).toContain('retire-after-work')
    expect(RETIRE_AFTER_WORK_CONTROL.semantics).toContain('stopped-generation proof')
  })

  it('parses only generation-proven completed retirement receipts', () => {
    const session = '00000000-0000-4000-8000-000000000001'
    const retirement = '00000000-0000-4000-8000-000000000002'
    expect(
      parseHabitatRetirementReceipt(
        {
          schema_version: 1,
          retirement: {
            id: retirement,
            project_id: 1,
            harness_session_id: session,
            correlation_id: retirement,
            kind: 'retire_after_work',
            requested_revision: 4,
            state: 'completed',
            reason: 'applied',
            requested_at: '2026-09-09T10:00:00.000Z',
            claimed_at: '2026-09-09T10:01:00.000Z',
            stopping_at: '2026-09-09T10:02:00.000Z',
            completed_at: '2026-09-09T10:03:00.000Z',
            owned_stop_receipt: true,
            stopped_generation_proof: true,
          },
        },
        1,
        session,
      ).state,
    ).toBe('completed')
    expect(() =>
      parseHabitatRetirementReceipt(
        {
          schema_version: 1,
          retirement: {
            id: retirement,
            project_id: 1,
            harness_session_id: session,
            correlation_id: retirement,
            kind: 'retire_after_work',
            requested_revision: 4,
            state: 'completed',
            reason: 'applied',
            requested_at: '2026-09-09T10:00:00.000Z',
            claimed_at: '2026-09-09T10:01:00.000Z',
            stopping_at: '2026-09-09T10:02:00.000Z',
            completed_at: '2026-09-09T10:03:00.000Z',
            owned_stop_receipt: true,
            stopped_generation_proof: false,
          },
        },
        1,
        session,
      ),
    ).toThrow()
  })

  it('keeps an applied stop receipt pending until stopped-generation proof arrives', () => {
    const session = '00000000-0000-4000-8000-000000000001'
    const retirement = '00000000-0000-4000-8000-000000000002'
    const pending = {
      schema_version: 1,
      retirement: {
        id: retirement,
        project_id: 1,
        harness_session_id: session,
        correlation_id: retirement,
        kind: 'retire_after_work',
        requested_revision: 4,
        state: 'stopping',
        reason: 'applied',
        requested_at: '2026-09-09T10:00:00.000Z',
        claimed_at: '2026-09-09T10:01:00.000Z',
        stopping_at: '2026-09-09T10:02:00.000Z',
        completed_at: '2026-09-09T10:03:00.000Z',
        owned_stop_receipt: true,
        stopped_generation_proof: false,
      },
    }
    expect(parseHabitatRetirementReceipt(pending, 1, session)).toEqual({
      id: retirement,
      kind: 'retire_after_work',
      state: 'stopping',
      outcome: null,
      reason: 'applied',
    })
    expect(() =>
      parseHabitatRetirementReceipt(
        {
          ...pending,
          retirement: { ...pending.retirement, owned_stop_receipt: false },
        },
        1,
        session,
      ),
    ).toThrow()
    expect(() =>
      parseHabitatRetirementReceipt(
        {
          ...pending,
          retirement: { ...pending.retirement, reason: 'failed' },
        },
        1,
        session,
      ),
    ).toThrow()
    expect(() =>
      parseHabitatRetirementReceipt(
        {
          ...pending,
          retirement: { ...pending.retirement, stopped_generation_proof: true },
        },
        1,
        session,
      ),
    ).toThrow()
    expect(() =>
      parseHabitatRetirementReceipt(
        {
          ...pending,
          retirement: { ...pending.retirement, completed_at: undefined },
        },
        1,
        session,
      ),
    ).toThrow()
  })
})
