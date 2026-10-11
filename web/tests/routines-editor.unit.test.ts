// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { acceptsRecord, assignmentFrom, budgetFrom, consentRequest, routineDraft, routineState, staleFields, waitReasonWords, type Recurrence } from '../src/lib/recurrences'

// Risk: a routine says the wrong thing about why it waits, consents to a revision it
// no longer shows, or a late answer overwrites the record on screen (AEON-1182).
const record = (extra: Partial<Recurrence> = {}): Recurrence => ({
  id: 'r1', project_id: 'p1', parent_id: 'p1', paused: true, revision: 3, occurrence_count: 0, next_at: null, created_at: '', updated_at: '',
  template: { name: 'Weekly dependency update', title: 'Update dependencies {{date}}', description: 'Update npm and Go dependencies.', acceptance_criteria: ['All tests pass'], estimate_hours: 1, priority: 'medium', tags: [], type: 'work' },
  trigger: { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO', time_of_day: '08:00', timezone: 'Europe/Vienna' }, queue_each: true, overlap_policy: 'skip', catch_up_policy: 'one',
  definition: { scope: { kind: 'project', project_id: 'p1' }, owner_principal_id: 'markus', assignment: { goal: 'Update npm and Go dependencies.', role: 'build', work_kind_id: 'k1', sources: [], allowed_actions: ['work.create', 'pr.open'], runtime_requirements: { needs_native_host: true, needs_browser: false, runtime_class: 'native_coding' }, budget: { mode: 'tokens', token_ceiling: 400_000 } } },
  execution_policy: { eligible: false, consent_required: true, wait_reason: 'execution_consent_required', project_revision: 4, consent_revision: 0 },
  ...extra,
})

describe('routine wait reasons', () => {
  it('says every server wait reason in words, naming the owner and the project', () => {
    const reasons = ['automatic_launch_disabled', 'project_unavailable', 'qualification_unavailable', 'runtime_unavailable', 'qualification_runtime_changed', 'definition_unavailable', 'owner_unavailable', 'owner_policy_mismatch', 'capability_unqualified', 'budget_mode_unqualified', 'execution_consent_required', 'execution_identity_unavailable']
    for (const reason of reasons) {
      const words = waitReasonWords(reason, { owner: 'Markus', project: 'WEB' })
      expect(words, reason).not.toContain('_')
      expect(words, reason).toMatch(/^[A-Z].*\.$/)
    }
    expect(waitReasonWords('execution_consent_required', { owner: 'Markus', project: 'WEB' })).toBe('Waits for Markus to let agents run it.')
    expect(waitReasonWords('automatic_launch_disabled', { project: 'WEB' })).toBe('Agents can’t run routines in WEB yet.')
    expect(waitReasonWords('owner_policy_mismatch', { project: 'WEB' })).toBe('WEB’s qualification covers another owner’s routines.')
  })
  it('derives one row state: never-consented waits, a consented pause stops, ticket-only stays scheduled', () => {
    expect(routineState(record())).toBe('waiting')
    expect(routineState(record({ execution_policy: { ...record().execution_policy!, consent_revision: 2 } }))).toBe('stopped')
    expect(routineState(record({ paused: false, execution_policy: { ...record().execution_policy!, wait_reason: 'capability_unqualified' } }))).toBe('held')
    expect(routineState(record({ paused: false, execution_policy: { ...record().execution_policy!, eligible: true, wait_reason: '' }, last_run: { id: 'run', state: 'failed', reason: 'exit 1', created_at: '' } }))).toBe('failed')
    expect(routineState(record({ paused: false, definition: undefined }))).toBe('scheduled')
    expect(routineState(record({ definition: undefined }))).toBe('stopped')
  })
})

describe('save, then consent', () => {
  it('consents to the revision the resume returned; personal routines run as their owner', () => {
    const saved = record()
    const resumed = { ...saved, paused: false, revision: saved.revision + 1 }
    expect(consentRequest(resumed, 'agent-web')).toEqual({ expected_revision: 4, execute_consent: true, execution_principal_id: 'agent-web' })
    // No restricted agent picked: nothing is sent, rather than a request the server refuses.
    expect(consentRequest(resumed, null)).toBeNull()
    const personal = record({ definition: { ...saved.definition!, scope: { kind: 'personal' } } })
    expect(consentRequest(personal, 'agent-web')).toEqual({ expected_revision: 3, execute_consent: true, execution_principal_id: null })
  })
  it('saves an assignment the server accepts: consistent runtime and no ceilings when off', () => {
    const draft = { ...routineDraft(), kind: 'k1', browser: true, mode: 'off' as const }
    expect(assignmentFrom(draft, '  Check every link.  ')).toMatchObject({ goal: 'Check every link.', runtime_requirements: { needs_native_host: true, needs_browser: true, runtime_class: 'native_browser' }, budget: { mode: 'off' }, allowed_actions: ['work.create', 'work.update'] })
    expect(assignmentFrom(draft, 'x')!.budget).toEqual({ mode: 'off' })
    expect(budgetFrom('both', '400', '5')).toEqual({ mode: 'both', token_ceiling: 400_000, money_ceiling_microusd: 5_000_000 })
    expect(budgetFrom('money', '400', '0')).toBeNull()
    expect(routineDraft(record().definition!.assignment)).toMatchObject({ role: 'build', mode: 'tokens', tokens: '400', actions: ['work.create', 'pr.open'] })
  })
})

describe('stale and late answers', () => {
  it('applies an answer only to the open record at a revision at least the one shown', () => {
    expect(acceptsRecord({ id: 'r1', revision: 4 }, { id: 'r1', revision: 4 })).toBe(true)
    expect(acceptsRecord({ id: 'r1', revision: 4 }, { id: 'r1', revision: 5 })).toBe(true)
    expect(acceptsRecord({ id: 'r1', revision: 4 }, { id: 'r1', revision: 3 })).toBe(false)
    expect(acceptsRecord({ id: 'r2', revision: 1 }, { id: 'r1', revision: 9 })).toBe(false)
    expect(acceptsRecord(null, { id: 'r1', revision: 1 })).toBe(false)
  })
  it('names exactly the fields the server changed since the shown revision', () => {
    const shown = record(), server = record({ revision: 5, trigger: { ...shown.trigger, time_of_day: '09:00' } })
    expect(staleFields(shown, server)).toEqual(['trigger'])
    const both = record({ revision: 6, template: { ...shown.template, description: 'Only npm.' }, definition: { ...shown.definition!, assignment: { ...shown.definition!.assignment!, role: 'scout' } } })
    expect(staleFields(shown, both)).toEqual(['description', 'assignment'])
    expect(staleFields(shown, record({ revision: 7 }))).toEqual([])
  })
})
