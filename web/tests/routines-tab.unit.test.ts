// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import type { Recurrence } from '../src/lib/recurrences'
import { RUN_LOOK, STATE_LOOK, checkedRuns, routinesLine, rowNote, runChain, runLine, runState, runTitle, settledWords, stateCounts, triggerShort, usageShort, type RoutineRun, type RunDetail, type RunUsage } from '../src/lib/routineRuns'

// Risk (AEON-1192): the logbook makes a run look more finished than it is (a created
// ticket or a failed run in the Done tint), shows unknown or partial usage as zero or
// complete, or a routine row's state and reason disagree with S21's evidence.
const usage = (extra: Partial<RunUsage> = {}): RunUsage => ({ tokens: { measured: '164000', known: 1, unknown: 0 }, paid_microusd: '0', estimated_api_usd: '2.40', reserved_tokens: '0', reserved_microusd: '0', reserved_recovery_ms: '0', partial: false, truncated: false, gaps: [], ...extra })
const run = (extra: Partial<RoutineRun> = {}): RoutineRun => ({ id: 'run-1', recurrence_id: 'r1', definition_revision: 4, owner_principal_id: 'markus', state: 'done', reason: '', work_node_id: 'n1', work_order_id: 'n1', agent_run_id: 'a1', created_at: '2026-10-05T06:00:00Z', updated_at: '2026-10-05T06:40:00Z', usage: usage(), ...extra })
const detail = (extra: Partial<RunDetail> = {}): RunDetail => ({ run: run(), attempts: { items: [], next_cursor: null }, actions: { items: [], next_cursor: null }, effects: { items: [], next_cursor: null }, ...extra })
const action = (extra: Partial<RunDetail['actions']['items'][number]> = {}) => ({ id: 'act-1', attempt_id: 'att-1', kind: 'pr.open', state: 'succeeded', target_node_id: 'n1', evaluation_id: 'e1', evaluation_result: 'allow' as const, approval_id: null, approval_state: null, effect_id: null, pull_request_number: 530, created_at: '2026-10-05T06:30:00Z', ...extra })
const attempt = (extra: Partial<RunDetail['attempts']['items'][number]> = {}) => ({ id: 'att-1', role: 'build', state: 'done' as const, reason: '', agent_run_id: 'a1', exit_confirmed: true, exit_code: 0, created_at: '2026-10-05T06:01:00Z', ...extra })
const ctx = { ticket: { key: 'WEB-318', title: 'Update dependencies 2026-10-05', state: 'in-progress', number: 15, projectKey: 'WEB' }, when: (at: string) => at.slice(11, 16), stateWord: (state: string) => state }
const texts = (steps: ReturnType<typeof runChain>['steps']) => steps.map(step => `${step.label}:${step.tone}:${step.parts.map(part => part.text).join('')}`)

describe('run states', () => {
  it('only Done uses the Done tint; created never shows a check and failed is danger', () => {
    for (const [state, look] of Object.entries(RUN_LOOK)) if (state !== 'done') expect(look.tone, state).not.toBe('ok')
    expect(RUN_LOOK.created.icon).not.toBe('check')
    expect(RUN_LOOK.failed.tone).toBe('bad')
    expect(STATE_LOOK.failed.tone).toBe('bad')
    expect(runState('something_new')).toBe('held')
  })
  it('a created ticket says it is not a finished run; running says how long so far', () => {
    expect(runTitle(run({ state: 'created' }), 0)).toBe('Ticket created · no agent run')
    expect(runLine(run({ state: 'created', reason: 'The occurrence was created; execution has not started.' }), 'Mon 5 Oct, 08:00', true)).toBe('Mon 5 Oct, 08:00 · The occurrence was created; execution has not started · a created ticket is not a finished run')
    expect(runTitle(run({ state: 'running', created_at: '2026-10-11T01:02:00Z' }), Date.parse('2026-10-11T01:14:30Z'))).toBe('Running · 12 min so far')
    expect(runLine(run({ state: 'failed', reason: 'Execution failed and has not completed successfully.' }), 'Sun 11 Oct, 02:00', true)).toBe('Sun 11 Oct, 02:00 · Execution failed and has not completed successfully')
  })
  it('a run page that is not one is a read failure, never an empty logbook', () => {
    expect(() => checkedRuns({ id: 'r1', template: {} })).toThrow()
    expect(checkedRuns({ items: [run({ state: 'weird' as RoutineRun['state'] })], next_cursor: null }).items[0]!.state).toBe('held')
  })
})

describe('usage words', () => {
  it('never shows unknown or partial usage as zero or complete', () => {
    expect(usageShort(run())).toEqual({ text: '164k tokens', warn: false })
    expect(usageShort(run({ state: 'failed', usage: usage({ tokens: { measured: null, known: 0, unknown: 1 }, gaps: ['execution_usage_unknown', 'measurement_incomplete'] }) }))).toEqual({ text: 'Usage unknown', warn: true })
    expect(usageShort(run({ state: 'failed', usage: usage({ tokens: { measured: '46000', known: 1, unknown: 1 }, partial: true, gaps: ['source_visibility_incomplete'] }) }))).toEqual({ text: 'At least 46k tokens', warn: true })
    expect(usageShort(run({ state: 'running', usage: usage({ tokens: { measured: '64000', known: 1, unknown: 0 } }) }))).toEqual({ text: '64k tokens so far', warn: false })
    expect(usageShort(run({ state: 'created' })).text).toBe('')
  })
  it('settles in measured facts: paid, the API list price and what stays reserved', () => {
    expect(settledWords(run())).toEqual({ tone: 'ok', strong: '', text: '164k tokens · $0.00 paid · about $2.40 at API list prices, not charged' })
    expect(settledWords(run({ usage: usage({ paid_microusd: null, estimated_api_usd: null, partial: true, gaps: ['paid_usage_unknown'] }) })).text).toBe('164k tokens · no paid charge recorded')
    const failed = settledWords(run({ state: 'failed', usage: usage({ tokens: { measured: null, known: 0, unknown: 1 }, paid_microusd: null, estimated_api_usd: null, reserved_tokens: '80000', gaps: ['source_visibility_incomplete'] }) }))
    expect(failed.tone).toBe('warn'); expect(failed.strong).toBe('Usage unknown'); expect(failed.text).toContain('80k tokens stay reserved until the run settles')
    expect(settledWords(run({ state: 'waiting_for_person', usage: usage({ tokens: { measured: '212000', known: 1, unknown: 0 }, reserved_tokens: '400000' }) }))).toEqual({ tone: 'hold', strong: 'Not yet', text: '212k tokens so far · 400k tokens stay reserved until the run settles' })
    expect(settledWords(run({ usage: usage({ tokens: { measured: null, known: 0, unknown: 0 }, gaps: ['usage_visibility_withheld'] }) })).tone).toBe('off')
  })
})

describe('the run chain', () => {
  it('a confirmed clean exit is a neutral check; an unconfirmed exit is unknown; checks and people say what they decided', () => {
    const chain = runChain(detail({ run: run({ state: 'waiting_for_person' }), attempts: { items: [attempt()], next_cursor: null }, actions: { items: [action({ state: 'needs_person', evaluation_result: 'needs_person', approval_id: 'ap-1', approval_state: 'pending' })], next_cursor: null } }), ctx)
    expect(texts(chain.steps)).toEqual([
      'Ticket:ok:WEB-318 Update dependencies 2026-10-05 · in-progress · #15',
      'Agent:ok:Build · finished; its exit was confirmed',
      'Actions:wait:Open pull request #530: waits for a person',
      'Check:wait:Open pull request #530: asks a person',
      'Person:wait:Open pull request #530: waiting for a person · Open on the Decision Desk',
      'Settled:hold:Not yet · 164k tokens so far',
    ])
    expect(chain.steps.find(step => step.label === 'Person')!.parts.at(-1)).toMatchObject({ kind: 'go', to: '/decision-desk?item=a:ap-1' })
    const unknown = runChain(detail({ run: run({ state: 'held' }), attempts: { items: [attempt({ state: 'held', exit_confirmed: false, exit_code: null, reason: 'Process exit has not been confirmed.' })], next_cursor: null } }), ctx)
    expect(texts(unknown.steps)[1]).toBe('Agent:warn:Build · exit unknown · Process exit has not been confirmed')
  })
  it('a failed run: error exit and failed effect are danger, nothing reads as filed', () => {
    const chain = runChain(detail({ run: run({ state: 'failed', usage: usage({ tokens: { measured: null, known: 0, unknown: 1 }, gaps: ['source_visibility_incomplete'] }) }), attempts: { items: [attempt({ state: 'failed', exit_code: 1 })], next_cursor: null }, actions: { items: [action({ kind: 'work.create', state: 'failed', evaluation_result: 'block', pull_request_number: null })], next_cursor: 'more' }, effects: { items: [{ id: 'ef-1', action_id: 'act-1', kind: 'work.create', state: 'failed', created_at: '2026-10-05T06:31:00Z' }], next_cursor: null } }), ctx)
    expect(chain.steps.filter(step => ['Agent', 'Actions', 'Check', 'Effect'].includes(step.label)).map(step => step.tone)).toEqual(['bad', 'bad', 'bad', 'bad'])
    expect(chain.steps.some(step => step.tone === 'ok' && step.label !== 'Ticket')).toBe(false)
    expect(chain.truncated).toBe(true)
  })
  it('no actions recorded says so; a created run stops after the agent', () => {
    const quiet = runChain(detail({ attempts: { items: [attempt()], next_cursor: null } }), ctx)
    expect(texts(quiet.steps).slice(2, 5)).toEqual(['Actions:off:No actions recorded', 'Check:off:Nothing to check: no actions recorded', 'Person:off:Not needed'])
    const created = runChain(detail({ run: run({ state: 'created', reason: 'The occurrence was created; execution has not started.' }) }), ctx)
    expect(texts(created.steps)).toEqual(['Ticket:ok:WEB-318 Update dependencies 2026-10-05 · in-progress · #15', 'Agent:off:Didn’t start: The occurrence was created; execution has not started'])
  })
})

describe('routine rows', () => {
  const routine = (extra: Partial<Recurrence> = {}): Recurrence => ({
    id: 'r1', project_id: 'p1', parent_id: 'p1', paused: false, revision: 4, occurrence_count: 15, next_at: '2026-10-12T06:00:00Z', created_at: '', updated_at: '',
    template: { name: 'Weekly dependency update', title: 'Update dependencies {{date}}', description: 'Update.', acceptance_criteria: ['Tests pass'], estimate_hours: 1, priority: 'medium', tags: [], type: 'work' },
    trigger: { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO', time_of_day: '08:00', timezone: 'Europe/Vienna' }, queue_each: true, overlap_policy: 'skip', catch_up_policy: 'one',
    definition: { scope: { kind: 'project', project_id: 'p1' }, owner_principal_id: 'markus', assignment: { goal: 'Update.', role: 'build', work_kind_id: 'k1', sources: [], allowed_actions: ['work.create'], runtime_requirements: { needs_native_host: true, needs_browser: false, runtime_class: 'native_coding' }, budget: { mode: 'off' } } },
    execution_policy: { eligible: true, consent_required: true, wait_reason: '', project_revision: 2, consent_revision: 4 }, ...extra,
  })
  const note = (item: Recurrence, ownerName?: string) => rowNote(item, { me: 'markus', ownerName, project: 'WEB', when: at => at.slice(5, 10) })
  it('says the reason in words for every state; created is never called done', () => {
    expect(note(routine({ last_run: { id: 'x', state: 'failed', reason: 'Execution failed and has not completed successfully.', created_at: '2026-10-11T00:00:00Z' } }))).toBe('Execution failed and has not completed successfully')
    expect(note(routine({ last_run: { id: 'x', state: 'created', reason: 'The occurrence was created; execution has not started.', created_at: '2026-10-05T06:00:00Z' } }))).toBe('Last run: ticket created · 10-05')
    expect(note(routine({ execution_policy: { eligible: false, consent_required: true, wait_reason: 'execution_consent_required', project_revision: 2, consent_revision: 0 } }))).toBe('You to let agents run it')
    expect(note(routine({ definition: { ...routine().definition!, owner_principal_id: 'mia' }, execution_policy: { eligible: false, consent_required: true, wait_reason: 'execution_consent_required', project_revision: 2, consent_revision: 0 } }), 'Mia Holm')).toBe('Mia Holm decides')
    expect(note(routine({ execution_policy: { eligible: false, consent_required: true, wait_reason: 'capability_unqualified', project_revision: 2, consent_revision: 4 } }))).toBe('No computer is qualified for this kind of work yet (for example, a browser)')
    expect(note(routine({ definition: undefined, last_result: { recurrence_id: 'r1', occurrence_key: 'k', number: 41, scheduled_at: '', created_at: '', node_id: 'n', source_event_id: null, outcome: 'created', reason: '', key: 'AEON-1176' } }))).toBe('Last: AEON-1176 · #41')
  })
  it('counts states, and the Agents line appears only while something waits or failed', () => {
    const waiting = routine({ id: 'r2', execution_policy: { eligible: false, consent_required: true, wait_reason: 'execution_consent_required', project_revision: 2, consent_revision: 0 } })
    const failed = routine({ id: 'r3', last_run: { id: 'x', state: 'failed', reason: 'x', created_at: '' } })
    expect(stateCounts([routine(), waiting, failed])).toMatchObject({ scheduled: 1, waiting: 1, failed: 1, running: 0 })
    expect(routinesLine([routine()], false, () => 'Mon 12 Oct, 08:00')).toBeNull()
    expect(routinesLine([routine(), waiting, failed], false, () => 'Mon 12 Oct, 08:00')).toBe('1 waits for a person · 1 failed · next Mon 12 Oct, 08:00')
    expect(routinesLine([waiting], true, () => 'Mon 12 Oct, 08:00')).toBe('1+ wait for a person · next Mon 12 Oct, 08:00')
  })
  it('names triggers briefly', () => {
    expect(triggerShort({ kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO,TH', time_of_day: '08:00', timezone: 'Europe/Vienna' })).toBe('Mon, Thu 08:00')
    expect(triggerShort({ kind: 'time', rrule: 'FREQ=DAILY', time_of_day: '02:00', timezone: 'Europe/Vienna' })).toBe('Daily 02:00')
    expect(triggerShort({ kind: 'event', event: 'release.published' })).toBe('After each release')
  })
})
