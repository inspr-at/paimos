// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1677 / P05: execute the production actions and watchers that previously
// required repeated page loads. Native focus, pointer and geometry stay in specs.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import * as identity from '../src/lib/identityScope'
import * as recurrence from '../src/lib/recurrences'
import * as runs from '../src/lib/routineRuns'
import * as estimates from '../src/lib/estimates'
import * as decisionDesk from '../src/lib/decisionDesk'
import * as work from '../src/lib/work'
import { deferred, flush, setupSource, sourceModule } from './record-source'
import type { ListItem } from '../src/lib/api'
import type { AutopilotSettings } from '../src/lib/statusAutopilot'

const stops: (() => void)[] = []
beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-10-22T12:00:00Z'))
  vi.stubGlobal('navigator', { platform: 'Linux', userAgent: 'P05 runtime' })
})
afterEach(() => { for (const stop of stops.splice(0)) stop(); vi.useRealTimers(); vi.unstubAllGlobals() })

function owner() {
  const session = Vue.reactive({ identity: { tenant: { id: 'tenant-a' }, principal: { id: 'person-a', name: 'Ada', kind: 'person' } }, current: true, authenticationCurrent: () => session.current })
  const grants = Vue.reactive({ manage: true })
  const authz = { can: () => grants.manage, myWorkspaceRole: () => ({ key: 'admin' }), myProjectRole: () => ({ key: 'admin' }), onAccessRevalidate: () => () => {} }
  // Use the actual scope hook, including its synchronous reset and liveness
  // check; replacing it with a mock would erase the race these tests assert.
  const scopes = sourceModule<typeof import('../src/lib/useIdentityScope')>('lib/useIdentityScope.ts', {
    './authz': authz, './identityScope': identity, '../stores/session': { useSession: () => session },
  })
  return { session, grants, authz, scopes }
}
function component(path: string, props: Record<string, unknown>, modules: Record<string, unknown>) {
  const mounted = setupSource(path, props, modules)
  stops.push(mounted.stop)
  return mounted
}
const routine = (extra: Partial<recurrence.Recurrence> = {}): recurrence.Recurrence => ({
  id: 'routine-a', project_id: 'project-a', parent_id: 'project-a', paused: true, revision: 3, occurrence_count: 0, next_at: null,
  created_at: '2026-10-22T12:00:00Z', updated_at: '2026-10-22T12:00:00Z',
  template: { name: 'Check dependencies', title: 'Check {{date}}', description: 'Update dependencies.', acceptance_criteria: ['Tests pass'], estimate_hours: 1, priority: 'medium', tags: [], type: 'work' },
  trigger: { kind: 'time', rrule: 'FREQ=WEEKLY;BYDAY=MO', time_of_day: '08:00', timezone: 'UTC' },
  queue_each: true, overlap_policy: 'skip', catch_up_policy: 'one',
  definition: { scope: { kind: 'project', project_id: 'project-a' }, owner_principal_id: 'person-a', assignment: {
    goal: 'Update dependencies.', role: 'build', work_kind_id: 'kind-a', sources: [], allowed_actions: ['work.create'],
    runtime_requirements: { needs_native_host: true, needs_browser: false, runtime_class: 'native_coding' }, budget: { mode: 'off' },
  } },
  execution_policy: { eligible: false, consent_required: true, wait_reason: 'execution_consent_required', project_revision: 4, consent_revision: 0 },
  ...extra,
})

function editor() {
  const context = owner()
  const props = Vue.reactive({ project: { id: 'project-a', routeKey: 'PRJ' }, recurrence: routine() })
  const api = {
    listRoutineWorkKinds: vi.fn(async () => ({ items: [{ id: 'kind-a', label: 'Maintenance' }] })),
    resolveRoutineRoute: vi.fn(async () => ({ profile: null })), getRoutineGuardrails: vi.fn(async () => ({ rules: [] })),
    getRoutineExecutionSettings: vi.fn(async () => ({ automatic_launch_enabled: true })),
    previewRecurrenceDraft: vi.fn(async () => ({ times: ['2026-10-26T08:00:00Z'] })),
    getNode: vi.fn(), getRecurrence: vi.fn(async () => routine()), recurrenceHistory: vi.fn(async () => ({ items: [] })),
    createRecurrence: vi.fn(async () => routine()), updateRecurrence: vi.fn(async () => routine({ revision: 4 })),
    pauseRecurrence: vi.fn(async (_id: string, _paused: boolean, _revision: number, _signal: AbortSignal) => routine({ revision: 4, paused: false })),
    putRoutineConsent: vi.fn(async (_id: string, _request: unknown, _signal: AbortSignal) => routine({ revision: 5, paused: false })),
  }
  const mounted = component('components/recurrences/RecurrenceEditor.vue', props, {
    '../../lib/api': api, '../../lib/access': { getMembers: vi.fn(), getProjectMembers: vi.fn() },
    '../../lib/authz': context.authz, '../../lib/displayLanguage': { displayLanguage: () => 'en' },
    '../../lib/toast': { toast: vi.fn() }, '../../lib/useIdentityScope': context.scopes,
    '../../lib/workKinds': { activeKinds: (items: unknown[]) => items },
    '../../stores/projects': { useProjects: () => ({ byId: () => props.project, projects: [], load: vi.fn() }) },
    '../../stores/session': { useSession: () => context.session },
    '../../stores/workVocabulary': { useWorkVocabulary: () => ({ leaf: { name: 'Ticket', icon: '' }, levels: [] }) },
    '../../lib/workVocabulary': { workNoun: (name: string) => name.toLowerCase() }, '../../lib/recurrences': recurrence,
  })
  return { ...context, ...mounted, props, api }
}
async function readyConsent(state: ReturnType<typeof editor>['state']) {
  await flush()
  state.enterConsent()
  state.principal.value = 'agent-a'; state.runsAs.value = 'ready'
  await flush()
  expect(state.consentReady.value).toBe(true)
}

describe('P05 routine consent actions', () => {
  it('resumes the reviewed record before consenting to the returned revision as the captured agent', async () => {
    const { state, api, emitted } = editor()
    const held = deferred<recurrence.Recurrence>()
    api.pauseRecurrence.mockImplementationOnce(() => held.promise)
    await readyConsent(state)
    state.letAgentsRun()
    expect(api.pauseRecurrence).toHaveBeenCalledWith('routine-a', false, 3, expect.any(AbortSignal))
    expect(api.putRoutineConsent).not.toHaveBeenCalled()
    // A different picker value must not change the explicit choice in flight.
    state.principal.value = 'agent-b'
    held.resolve(routine({ revision: 4, paused: false })); await flush()
    expect(api.putRoutineConsent).toHaveBeenCalledExactlyOnceWith('routine-a', {
      expected_revision: 4, execute_consent: true, execution_principal_id: 'agent-a',
    }, expect.any(AbortSignal))
    expect(emitted.filter(event => event[0] === 'saved')).toEqual([['saved', routine({ revision: 5, paused: false }), 'consented']])
  })

  it.each(['person', 'tenant', 'record', 'project', 'permission'] as const)('a held resume cannot carry consent across a %s change', async change => {
    const { state, api, props, session, grants, emitted } = editor()
    const held = deferred<recurrence.Recurrence>()
    api.pauseRecurrence.mockImplementationOnce(() => held.promise)
    await readyConsent(state); state.letAgentsRun()
    expect(api.pauseRecurrence).toHaveBeenCalledOnce()
    const signal = api.pauseRecurrence.mock.calls[0]![3]
    if (change === 'person') session.identity.principal.id = 'person-b'
    if (change === 'tenant') session.identity.tenant.id = 'tenant-b'
    if (change === 'record') props.recurrence = routine({ id: 'routine-b' })
    if (change === 'project') props.project.id = 'project-b'
    if (change === 'permission') { grants.manage = false; grants.manage = true }
    expect(signal.aborted).toBe(true)
    held.resolve(routine({ revision: 4, paused: false })); await flush()
    expect(api.putRoutineConsent).not.toHaveBeenCalled()
    expect(emitted.some(event => event[0] === 'saved' || event[0] === 'changed')).toBe(false)
    expect(state.base.value.revision).toBe(3)
  })

  it('a failed consent stays resumed, never reports consented or kept paused, and does not retry itself', async () => {
    const { state, api, emitted } = editor()
    api.putRoutineConsent.mockRejectedValueOnce(Object.assign(new Error('execution_identity_unavailable'), { code: 'execution_identity_unavailable' }))
    await readyConsent(state); state.letAgentsRun(); await flush()
    expect(state.base.value).toMatchObject({ revision: 4, paused: false })
    expect(state.consentCode.value).toBe('execution_identity_unavailable')
    expect(state.consentFailure.value).not.toBe('')
    expect(api.putRoutineConsent).toHaveBeenCalledOnce()
    expect(emitted.some(event => event[0] === 'saved')).toBe(false)
    state.close()
    expect(emitted.at(-1)).toEqual(['saved', routine({ revision: 4, paused: false }), 'waiting'])
  })

  it('another owner or an unavailable agent cannot begin either consent write', async () => {
    const { state, api, session } = editor()
    await readyConsent(state)
    state.principal.value = null; state.letAgentsRun()
    expect(api.pauseRecurrence).not.toHaveBeenCalled()
    state.principal.value = 'agent-a'; session.identity.principal.id = 'person-b'
    state.letAgentsRun(); await flush()
    expect(api.pauseRecurrence).not.toHaveBeenCalled()
    expect(api.putRoutineConsent).not.toHaveBeenCalled()
  })
})

const run = (id: string, recurrence_id = 'routine-a'): runs.RoutineRun => ({
  id, recurrence_id, definition_revision: 3, owner_principal_id: 'person-a', state: 'created', reason: '',
  work_node_id: null, work_order_id: null, agent_run_id: null, created_at: '2026-10-22T12:00:00Z', updated_at: '2026-10-22T12:00:00Z',
  usage: { tokens: { measured: null, known: 0, unknown: 0 }, paid_microusd: null, estimated_api_usd: null,
    reserved_tokens: '0', reserved_microusd: '0', reserved_recovery_ms: '0', partial: false, truncated: false, gaps: [] },
})
const runPage = (id: string, next_cursor: string | null = null, recurrence_id = 'routine-a') => ({ items: [run(id, recurrence_id)], next_cursor })
function logbook(openRun = '') {
  const context = owner()
  const props = Vue.reactive({ item: routine(), projectKey: 'PRJ', mayManage: true, busy: false, openRun })
  const api = {
    listRoutineRuns: vi.fn(async (_id: string, _cursor: string | undefined, _signal: AbortSignal) => runPage('newest', 'older')),
    getRoutineRun: vi.fn(async (_id: string, _run: string, _signal: AbortSignal) => ({ run: run(openRun), attempts: { items: [], next_cursor: null }, actions: { items: [], next_cursor: null }, effects: { items: [], next_cursor: null } })),
    recurrenceHistory: vi.fn(async () => ({ items: [], next_cursor: null })), resolveRoutineRoute: vi.fn(async () => ({ profile: null })),
  }
  const mounted = component('components/routines/RoutineLogbook.vue', props, {
    '../../lib/api': api, '../../lib/access': { getMembers: vi.fn(async () => ({ people: [] })) }, '../../lib/authz': context.authz,
    '../../lib/recurrences': recurrence, '../../lib/routineRuns': runs, '../../lib/useIdentityScope': context.scopes,
    '../../lib/work': work, '../../stores/session': { useSession: () => context.session },
  })
  return { ...context, ...mounted, props, api }
}

describe('P05 logbook callbacks', () => {
  it.each(['person', 'routine', 'filter'] as const)('an older page cannot land after the %s changes', async change => {
    const { state, props, session, api } = logbook()
    await flush()
    expect(state.runs.value.map((value: runs.RoutineRun) => value.id)).toEqual(['newest'])
    const held = deferred<ReturnType<typeof runPage>>()
    api.listRoutineRuns.mockImplementationOnce(() => held.promise)
    state.load('older')
    expect(api.listRoutineRuns).toHaveBeenLastCalledWith('routine-a', 'older', expect.any(AbortSignal))
    const signal = api.listRoutineRuns.mock.calls.at(-1)![2]
    if (change === 'person') session.identity.principal.id = 'person-b'
    if (change === 'routine') props.item = routine({ id: 'routine-b' })
    if (change === 'filter') state.filter.value = 'changes'
    expect(signal.aborted).toBe(true)
    await flush()
    const shown = state.runs.value.map((value: runs.RoutineRun) => value.id)
    held.resolve(runPage('previous-owner')); await flush()
    expect(state.runs.value.map((value: runs.RoutineRun) => value.id)).toEqual(shown)
    expect(state.runs.value.some((value: runs.RoutineRun) => value.id === 'previous-owner')).toBe(false)
    expect(state.olderReason.value).toBe('')
  })

  it('failed pagination retains the shown rows and cursor so Retry reads that exact page', async () => {
    const { state, api } = logbook()
    await flush()
    api.listRoutineRuns.mockRejectedValueOnce(new Error('older page offline'))
    state.load('older'); await flush()
    expect(state.runs.value.map((value: runs.RoutineRun) => value.id)).toEqual(['newest'])
    expect(state.cursor.value).toBe('older'); expect(state.older.value).toBe('failed')
    expect(state.olderReason.value).toBe('older page offline')
    api.listRoutineRuns.mockResolvedValueOnce(runPage('older-run'))
    state.load('older'); await flush()
    expect(api.listRoutineRuns).toHaveBeenLastCalledWith('routine-a', 'older', expect.any(AbortSignal))
    expect(state.runs.value.map((value: runs.RoutineRun) => value.id)).toEqual(['newest', 'older-run'])
    expect(state.cursor.value).toBeNull(); expect(state.olderReason.value).toBe('')
  })

  it('a deep-linked run is pinned once, then deduplicated when Show older reaches it', async () => {
    const { state, api } = logbook('linked')
    await flush()
    expect(api.getRoutineRun).toHaveBeenCalledWith('routine-a', 'linked', expect.any(AbortSignal))
    expect(state.pinned.value?.id).toBe('linked'); expect([...state.opened.value]).toEqual(['linked'])
    api.listRoutineRuns.mockResolvedValueOnce(runPage('linked'))
    state.load('older'); await flush()
    expect(state.pinned.value).toBeNull()
    expect(state.runs.value.map((value: runs.RoutineRun) => value.id)).toEqual(['newest', 'linked'])
    expect([...state.opened.value]).toEqual(['linked'])
  })
})

function estimateEditor(save: (hours: number | null) => Promise<'ok' | 'conflict' | 'error'>) {
  const props = Vue.reactive({ item: { id: 'ticket-a', kind_slug: 'work', state: 'open', fields: { estimate_hours: 2 } } as ListItem, editable: true, save })
  const mounted = component('components/work/TicketEstimate.vue', props, {
    '../../lib/estimates': estimates, '../../lib/decisionDesk': decisionDesk,
  })
  return { ...mounted, props }
}
describe('P05 estimate actions', () => {
  it('invalid values never write; a conflict preserves the draft; clearing writes null after explicit retry', async () => {
    const save = vi.fn<(hours: number | null) => Promise<'ok' | 'conflict' | 'error'>>().mockResolvedValueOnce('conflict').mockResolvedValueOnce('ok')
    const { state } = estimateEditor(save)
    await state.edit(); state.draft.value = '201h'; await state.submit()
    expect(save).not.toHaveBeenCalled(); expect(state.error.value).toContain('up to 200 hours')
    state.draft.value = '30m'; await state.submit()
    expect(save).toHaveBeenCalledExactlyOnceWith(0.5)
    expect(state.editing.value).toBe(true); expect(state.draft.value).toBe('30m')
    expect(state.error.value).toBe('Changed elsewhere. Review your estimate and save again.')
    state.draft.value = ''; await state.submit()
    expect(save).toHaveBeenLastCalledWith(null); expect(save).toHaveBeenCalledTimes(2)
    expect(state.editing.value).toBe(false)
  })

  it.each(['ok', 'conflict', 'error'] as const)('a late %s for ticket A cannot close or mark ticket B’s estimate', async result => {
    const held = deferred<'ok' | 'conflict' | 'error'>(), save = vi.fn(() => held.promise)
    const { state, props } = estimateEditor(save)
    await state.edit(); state.draft.value = '3'; const writing = state.submit()
    expect(save).toHaveBeenCalledExactlyOnceWith(3)
    props.item = { ...props.item, id: 'ticket-b', fields: { estimate_hours: 4 } }; await flush()
    await state.edit(); state.draft.value = '5'
    held.resolve(result); await writing
    expect(state.editing.value).toBe(true); expect(state.draft.value).toBe('5')
    expect(state.error.value).toBe(''); expect(state.saving.value).toBe(false)
  })

  it('read-only estimates cannot write and parent editing starts from the plan, not the leaf total', async () => {
    const save = vi.fn(async () => 'ok' as const), { state, props } = estimateEditor(save)
    props.item.estimate = { hours: 32, planned_hours: 40, is_parent: true, open_children: 4, estimated_children: 2, leaf_count: 4, estimated_leaves: 2 }
    await state.edit(); expect(state.draft.value).toBe('40')
    props.editable = false; state.draft.value = '7'; await state.submit()
    expect(state.canEdit.value).toBe(false); expect(save).not.toHaveBeenCalled()
  })
})

const autopilotSettings = (): AutopilotSettings => ({ enabled: true, revision: 7, server_mode: 'suggest', effective_mode: 'suggest', rules: {
  new: { enabled: true, days: 2 }, backlog: { enabled: true, days: 30 }, blocked: { enabled: true, days: 3 },
  progress: { enabled: true, days: 7 }, done: { enabled: true, days: 14 }, publish: { enabled: true }, accept: { enabled: true, days: 7 },
} })
function autopilot() {
  const context = owner(), toast = vi.fn(), push = vi.fn()
  const api = {
    getStatusAutopilot: vi.fn(async () => autopilotSettings()),
    listAutopilotProjects: vi.fn(async () => ({ items: [], next_cursor: null, inherited_count: 2 })),
    getLatestAutomaticChanges: vi.fn(async () => ({ items: [] })),
    saveStatusAutopilot: vi.fn(async (_settings: AutopilotSettings, _confirm: boolean, _signal: AbortSignal) => ({ ...autopilotSettings(), revision: 8 })), saveProjectAutopilot: vi.fn(),
  }
  const mounted = component('components/settings/StatusAutopilot.vue', {}, {
    'vue-router': { useRouter: () => ({ push }) }, '../../lib/authz': context.authz,
    '../../lib/attention': { ATTENTION_KINDS: [], listAttention: vi.fn(async () => ({ counts: {}, total: 0 })) },
    '../../lib/useIdentityScope': context.scopes, '../../lib/toast': { toast }, '../../lib/statusAutopilot': api,
    '../../lib/statusNames': { statusName: (state: string) => state },
  })
  mounted.state.load()
  return { ...context, ...mounted, api, toast, push }
}
describe('P05 autopilot settings actions', () => {
  it('validates all day bounds before writing and preserves the server cap after explicit upgrade', async () => {
    const { state, api, toast } = autopilot()
    await flush()
    for (const value of ['', '0', '1.5', '366', 'NaN']) state.days('progress', { target: { value } }, true)
    expect(api.saveStatusAutopilot).not.toHaveBeenCalled()
    expect(state.errors.value.progress).toBe('1 to 365 days')
    state.days('progress', { target: { value: '8' } }, true); await flush()
    expect(api.saveStatusAutopilot).toHaveBeenCalledWith(expect.objectContaining({ revision: 7, rules: expect.objectContaining({ progress: { enabled: true, days: 8 } }) }), false, expect.any(AbortSignal))
    state.save(state.settings.value, true); await flush()
    expect(api.saveStatusAutopilot).toHaveBeenLastCalledWith(expect.objectContaining({ revision: 8 }), true, expect.any(AbortSignal))
    expect(state.settings.value.effective_mode).toBe('suggest')
    expect(toast).toHaveBeenCalledTimes(1)
  })

  it('a rejected settings write preserves the saved limits and never offers Undo', async () => {
    const { state, api, toast } = autopilot()
    await flush()
    api.saveStatusAutopilot.mockRejectedValueOnce(new Error('Another admin changed these settings. Reload to see their changes.'))
    state.days('progress', { target: { value: '8' } }, true); await flush()
    expect(state.settings.value).toEqual(autopilotSettings())
    expect(state.draftDays.value.progress).toBe('8'); expect(state.saved.value).toBe(false)
    expect(state.error.value).toContain('Another admin changed')
    expect(toast.mock.calls.some(call => call[1]?.action)).toBe(false)
  })

  it('a response after session expiry cannot replace settings or offer Undo', async () => {
    const { state, api, session, toast } = autopilot()
    await flush()
    const held = deferred<AutopilotSettings>()
    api.saveStatusAutopilot.mockImplementationOnce(() => held.promise)
    state.save({ ...state.settings.value, enabled: false })
    expect(api.saveStatusAutopilot).toHaveBeenCalledOnce()
    session.current = false
    held.resolve({ ...autopilotSettings(), revision: 8, enabled: false }); await flush()
    expect(state.settings.value).toEqual(autopilotSettings())
    expect(state.saved.value).toBe(false); expect(toast).not.toHaveBeenCalled()
  })

  it('Undo refuses both a newer settings revision and a different person', async () => {
    const { state, api, session, toast } = autopilot()
    await flush(); state.save({ ...state.settings.value, enabled: false }); await flush()
    const undo = toast.mock.calls[0]![1].action.run
    state.settings.value = { ...autopilotSettings(), revision: 9 }
    undo(); await flush()
    expect(api.saveStatusAutopilot).toHaveBeenCalledOnce()
    expect(toast.mock.calls.at(-1)![0]).toBe('Settings changed since. Reload before Undo.')
    session.identity.principal.id = 'person-b'; await flush()
    undo(); await flush()
    expect(api.saveStatusAutopilot).toHaveBeenCalledOnce()
  })
})
