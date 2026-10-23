// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1678: execute the production handlers before retiring browser matrices.
// Negative controls remove one guard in memory; no product source is changed.
import { afterEach, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import * as rules from '../src/lib/rules'
import { deferred, flush, setupSource, sourceText } from './record-source'

const scopes: { stop(): void }[] = []
afterEach(() => {
  scopes.splice(0).forEach(scope => scope.stop())
  vi.unstubAllGlobals()
})

const componentPath = 'components/rules/AgentRulesSection.vue'
const rule = (identity: string, patch: Partial<rules.AgentRule> = {}): rules.AgentRule => ({
  identity, text: `Record ${identity}.`, why: 'Keep the audit attributable.', strength: 'normal', enabled: true,
  roles: ['builder'], harnesses: ['codex'], source: { reference: 'AEON-1678', edited_here: false }, ...patch,
})
const set = (id: string): rules.RuleSet => ({
  id, layer_id: 'company', scope: { layer: 'company' }, name: id, revision: 7, published_version: '',
  rules: [rule('locked', { strength: 'locked' }), rule('movable')],
})
function without(source: string, guard: string, replacement = '') {
  expect(source.split(guard)).toHaveLength(2)
  return source.replace(guard, replacement)
}
async function rejectedOracle(probe: () => Promise<void>, expected: string) {
  await expect(probe()).rejects.toMatchObject({ name: 'AssertionError', message: expect.stringContaining(expected) })
}

function section(source?: string) {
  const saved: { id: string; body: Parameters<typeof rules.saveDraft>[1] }[] = []
  const notices: { message: string; tone?: string }[] = []
  const publish = vi.fn(async () => ({ versions: [] }))
  let write: (id: string, body: Parameters<typeof rules.saveDraft>[1]) => Promise<rules.RuleSet> = async (id, body) => ({
    ...set(id), name: body.name, revision: body.expected_revision + 1, rules: body.rules,
  })
  const session = reactive({ identity: { principal: { id: 'person', kind: 'person', name: 'Person' }, tenant: { id: 'tenant', name: 'Workspace' } } })
  const permissions = new Set(['rules.read', 'rules.write', 'rules.publish'])
  const create = vi.fn(async (layer: string, name: string) => ({ ...set(`copy-${name}`), layer_id: layer, name, revision: 1, rules: [] }))
  const c = setupSource(componentPath, {}, {
    '../../lib/api': { api: async () => new Response('{}'), accountName: () => 'Person', getProjects: async () => ({ items: [] }) },
    '../../lib/authz': { can: (permission: string) => permissions.has(permission) },
    '../../lib/access': { getMembers: async () => ({ agents: [] }) },
    '../../lib/toast': { toast: (message: string, options?: { tone?: string }) => notices.push({ message, tone: options?.tone }) },
    '../../stores/session': { useSession: () => session },
    '../../lib/rules': { ...rules, publishSets: publish, createSet: create,
      saveDraft: async (id: string, body: Parameters<typeof rules.saveDraft>[1]) => {
        saved.push({ id, body: JSON.parse(JSON.stringify(body)) })
        return write(id, body)
      },
    },
  }, source)
  scopes.push(c)
  c.state.dbAuthoring.value = true
  c.state.model.value = [{ layer: { id: 'company', scope: { layer: 'company' } }, sets: ['first', 'second', 'third'].map(id => ({ remote: set(id), live: null })) }]
  c.state.loading.value = false
  return { ...c, saved, notices, publish, create, session, permissions, write: (fn: typeof write) => { write = fn } }
}

async function partialLayer(source?: string) {
  const c = section(source)
  c.write(async (id, body) => {
    if (id === 'second') throw new rules.RulesError(400, 'invalid_rule', 'The draft was not saved.')
    return { ...set(id), revision: 8, rules: body.rules }
  })
  await c.state.onSectionTick('company', false)
  expect(c.saved.map(row => row.id), 'partial layer stops before the third set').toEqual(['first', 'second'])
  expect(c.saved.map(row => row.body.expected_revision)).toEqual([7, 7])
  expect(c.state.find('first').set.remote.rules.map((r: rules.AgentRule) => r.enabled)).toEqual([true, false])
  expect(c.state.find('second').set.remote.rules.map((r: rules.AgentRule) => r.enabled)).toEqual([true, true])
  expect(c.state.find('third').set.remote.revision).toBe(7)
  expect(c.notices).toEqual([{ message: 'Saved 1 draft. The next set was not saved.', tone: 'error' }])
  expect(c.publish).not.toHaveBeenCalled()
}

it('P06 rules: a partial layer tick keeps the committed draft, restores the failed set and stops before later sets', async () => {
  await partialLayer()
  await rejectedOracle(() => partialLayer(without(sourceText(componentPath),
    "if (result.status === 'failed') { failure = result.message; break }",
    "if (result.status === 'failed') { failure = result.message; continue }")), 'partial layer stops before the third set')
})

async function rolledBack(source?: string) {
  for (const kind of ['set', 'rule'] as const) {
    const c = section(source), response = deferred<rules.RuleSet>(), started = deferred()
    c.write(() => { started.resolve(); return response.promise })
    const pending = kind === 'set' ? c.state.onSetTick('first', false) : c.state.onRuleTick('first', 'movable', false)
    await flush()
    expect(c.saved).toHaveLength(1)
    await started.promise
    expect(c.state.find('first').set.remote.rules.map((r: rules.AgentRule) => r.enabled)).toEqual([true, false])
    expect(c.state.saving.value).toBe(true)
    await c.state.onSetTick('third', false)
    expect(c.saved).toHaveLength(1)
    response.reject(new rules.RulesError(400, 'invalid_rule', 'The draft was not saved.'))
    await pending
    expect(c.state.find('first').set.remote.rules.map((r: rules.AgentRule) => r.enabled), 'failed tick restores the saved rules').toEqual([true, true])
    expect(c.state.tickFor('first')).toBe('on')
    expect(c.state.saving.value).toBe(false)
    expect(c.notices).toEqual([{ message: 'The draft was not saved.', tone: 'error' }])
    expect(c.publish).not.toHaveBeenCalled()
  }
}

it('P06 rules: held set/rule tick failures roll back their optimistic state and reject overlapping ticks', async () => {
  await rolledBack()
  await rejectedOracle(() => rolledBack(without(sourceText(componentPath), 'found.set.remote = previous', 'void previous')),
    'failed tick restores the saved rules')
})

async function draftUntilPublish(source?: string) {
  const c = section(source)
  c.state.edit('first')
  c.state.editing.value.name = 'Edited set'
  expect(c.state.dirty.value).toBe(true)
  await c.state.onSectionTick('company', false)
  await c.state.onSetTick('third', false)
  await c.state.onSetTick('first', false)
  c.state.duplicateAt(1)
  expect(c.saved).toEqual([])
  expect(c.state.editing.value.rules.map((r: rules.AgentRule) => [r.identity, r.enabled, r.strength])).toEqual([
    ['locked', true, 'locked'], ['movable', false, 'normal'], ['movable-copy', true, 'normal'],
  ])
  c.state.openPublish()
  expect(c.state.publishTarget.value).toBeNull()
  expect(c.publish).not.toHaveBeenCalled()
  await c.state.save()
  expect(c.saved).toHaveLength(1)
  expect(c.saved[0]).toMatchObject({ id: 'first', body: { expected_revision: 7, name: 'Edited set' } })
  expect(c.state.editing.value).toBeNull()
  c.state.openPublish(['first'])
  expect(c.state.publishTarget.value).toEqual(['first'])
  expect(c.publish.mock.calls, 'review alone never publishes').toEqual([])
  await c.state.confirmPublish('Reviewed by a person.')
  expect(c.publish).toHaveBeenCalledExactlyOnceWith([{ set_id: 'first', expected_revision: 8, version: 'auto' }], 'Reviewed by a person.')
  expect(c.state.publishTarget.value).toBeNull()
}

it('P06 rules: editing keeps ticks and duplicates local until an explicit draft save; publication requires a separate confirmation', async () => {
  await draftUntilPublish()
  await rejectedOracle(() => draftUntilPublish(without(sourceText(componentPath),
    'publishTarget.value = ids ?? publishable.value.map(item => item.set.remote.id)',
    "publishTarget.value = ids ?? publishable.value.map(item => item.set.remote.id); void confirmPublish('automatic')")),
  'review alone never publishes')
})

it('P06 rules: duplicate sets have independent identities and never change the original or publish', async () => {
  const c = section()
  await c.state.duplicateSet('first')
  await c.state.duplicateSet('first')
  expect(c.create.mock.calls).toEqual([['company', 'first (copy)'], ['company', 'first (copy 2)']])
  expect(c.saved.map(row => [row.id, row.body.expected_revision])).toEqual([
    ['copy-first (copy)', 1], ['copy-first (copy 2)', 1],
  ])
  for (const { body } of c.saved) {
    expect(body.rules.map(r => [r.identity, r.text, r.enabled, r.strength, r.source])).toEqual([
      ['locked-copy', 'Record locked.', true, 'normal', { reference: 'AEON-1678', edited_here: false }],
      ['movable-copy', 'Record movable.', true, 'normal', { reference: 'AEON-1678', edited_here: false }],
    ])
  }
  expect(c.state.find('first').set.remote).toEqual(set('first'))
  expect(c.publish).not.toHaveBeenCalled()
})

it('P06 rules: company ticks and duplication require a person with write and publish grants', async () => {
  for (const [kind, missing] of [['person', 'rules.write'], ['person', 'rules.publish'], ['agent', '']] as const) {
    const c = section()
    c.session.identity.principal.kind = kind
    c.permissions.delete(missing)
    expect(c.state.tickFor('first')).toBeNull()
    expect(c.state.sectionTick('company')).toBeNull()
    expect(c.state.duplicateReason('first')).toMatch(/Agents cannot|permission to/)
    await c.state.onSectionTick('company', false)
    await c.state.duplicateSet('first')
    expect(c.saved).toEqual([])
    expect(c.create).not.toHaveBeenCalled()
    expect(c.publish).not.toHaveBeenCalled()
  }
})

function budget(source?: string) {
  const put = vi.fn(async (value: rules.RuleBudget) => ({ ...rules.DEFAULT_BUDGET, ...value }))
  const c = setupSource('components/rules/RulesBudgetSection.vue', {
    canManage: true, view: { ...rules.DEFAULT_BUDGET, blocking_clients: [], blocking_clients_more: 2 },
  }, { '../../lib/rules': { ...rules, putBudget: put } }, source)
  scopes.push(c)
  c.state.start()
  return { ...c, put }
}

async function clientBudget(source?: string) {
  const c = budget(source)
  expect(c.state.blockingCount.value).toBe(2)
  for (const [raw, valid, tokens] of [['12000', true, 3000], ['64000', true, 16000], ['64001', true, 16001], ['128001', true, 32001], ['', false, NaN]] as const) {
    c.state.total.value = raw
    expect(c.state.validEstimate.value).toBe(valid)
    if (valid) expect(c.state.tokens.value).toBe(tokens)
  }
  c.state.total.value = '500000'
  const client = (max_session_file_bytes: number) => ({ host: 'host', harness: 'codex', max_session_file_bytes })
  expect([12000, 32768, 512000].map(max => c.state.deliveredBytes(client(max))), 'each client keeps its delivery ceiling').toEqual([12000, 32768, 500000])
  for (const value of ['1999', '500001', '3e4', '4.5', '-1', '']) {
    c.state.total.value = value; await c.state.save()
    expect(c.state.error.value).toBe('The budget is between 2,000 and 500,000 bytes.')
  }
  c.state.total.value = '2000'; c.state.caps.value.company = '499'; await c.state.save()
  expect(c.state.error.value).toBe('A company cap is between 500 bytes and the total.')
  c.state.caps.value.company = '2001'; await c.state.save()
  expect(c.state.error.value).toBe('A company cap is between 500 bytes and the total.')
  expect(c.put).not.toHaveBeenCalled()
  c.state.caps.value.company = '500'; await c.state.save()
  expect(c.put).toHaveBeenCalledExactlyOnceWith({ max_bytes: 2000, layer_max_bytes: { company: 500 } })
  expect(c.emitted).toEqual([['saved', { ...rules.DEFAULT_BUDGET, max_bytes: 2000, layer_max_bytes: { company: 500 } }]])
}

it('P06 rules: client delivery uses each client ceiling and invalid totals/layer caps never write', async () => {
  await clientBudget()
  await rejectedOracle(() => clientBudget(without(sourceText('components/rules/RulesBudgetSection.vue'),
    'Math.min(validEstimate.value ? proposedBytes.value : props.view.max_bytes, client.max_session_file_bytes)',
    'validEstimate.value ? proposedBytes.value : props.view.max_bytes')), 'each client keeps its delivery ceiling')
})

async function pendingBudget(source?: string) {
  const c = budget(source), response = deferred<rules.RuleBudgetView>()
  c.put.mockImplementation(() => response.promise)
  c.state.total.value = '64000'
  const first = c.state.save(), second = c.state.save()
  try {
    expect(c.put.mock.calls.length, 'a pending budget has one write').toBe(1)
    expect(c.state.busy.value).toBe(true)
    expect(c.emitted).toEqual([])
  } finally {
    response.reject(new rules.RulesError(400, 'invalid_budget', 'The budget was refused.'))
    await Promise.all([first, second])
  }
  expect(c.state.editing.value).toBe(true)
  expect(c.state.total.value).toBe('64000')
  expect(c.state.error.value).toBe('The budget was refused.')
  expect(c.emitted).toEqual([])
  expect(c.state.busy.value).toBe(false)
}

it('P06 rules: a held budget save sends once, reports refusal and retains the unsaved values', async () => {
  await pendingBudget()
  await rejectedOracle(() => pendingBudget(without(sourceText('components/rules/RulesBudgetSection.vue'), 'if (busy.value) return')),
    'a pending budget has one write')
})

const provenancePath = 'components/agents/ProvenanceDetail.vue'
function provenance(source?: string) {
  const reads: { project: string; session: string; answer: ReturnType<typeof deferred<Response>> }[] = []
  const props = reactive({ projectId: 'project-a', sessionId: 'session-a', now: 1000 })
  const c = setupSource(provenancePath, props, {
    '../../lib/agentRows': { readSessionProvenance: (project: string, session: string) => {
      const answer = deferred<Response>(); reads.push({ project, session, answer }); return answer.promise
    } },
    '../../lib/work': { absoluteTime: String, relativeTime: String },
  }, source)
  scopes.push(c)
  return { ...c, props, reads }
}
const provenancePage = (id: string) => ({ revisions: [{ id, revision: 1, recorded_at: '2026-10-01T00:00:00Z', items: [] }], truncated: false })

async function staleProvenance(source?: string) {
  const c = provenance(source)
  expect(c.reads.map(r => [r.project, r.session])).toEqual([['project-a', 'session-a']])
  c.props.sessionId = 'session-b'; await flush()
  c.reads[1]!.answer.resolve(new Response(JSON.stringify(provenancePage('b')))); await flush()
  expect(c.state.page.value.revisions.map((r: { id: string }) => r.id)).toEqual(['b'])
  c.reads[0]!.answer.resolve(new Response(JSON.stringify(provenancePage('a')))); await flush()
  expect(c.state.page.value.revisions.map((r: { id: string }) => r.id), 'late provenance belongs to its captured session').toEqual(['b'])
  expect(c.state.state.value).toBe('ready')
}

it('P06 metadata: delayed provenance cannot replace the session now open', async () => {
  await staleProvenance()
  await rejectedOracle(() => staleProvenance(without(sourceText(provenancePath),
    'if (props.projectId !== projectId || props.sessionId !== sessionId) return')),
  'late provenance belongs to its captured session')
})

it('P06 metadata: forbidden/missing provenance clears the old data and stale failures cannot hide a newer record', async () => {
  const c = provenance()
  c.reads[0]!.answer.resolve(new Response(JSON.stringify(provenancePage('a')))); await flush()
  for (const status of [403, 404]) {
    c.props.projectId = `project-${status}`; await flush()
    expect(c.state.page.value).toEqual({ revisions: [], truncated: false })
    c.reads.at(-1)!.answer.resolve(new Response('{}', { status })); await flush()
    expect(c.state.state.value).toBe('error')
    expect(c.state.page.value.revisions).toEqual([])
  }
  c.props.sessionId = 'old-failure'; await flush()
  const failed = c.reads.at(-1)!
  c.props.sessionId = 'current'; await flush()
  c.reads.at(-1)!.answer.resolve(new Response(JSON.stringify({ ...provenancePage('current'), truncated: true }))); await flush()
  failed.answer.reject(new Error('old request unavailable')); await flush()
  expect(c.state.state.value).toBe('ready')
  expect(c.state.page.value).toEqual({ ...provenancePage('current'), truncated: true })
  expect(c.state.itemName({ kind: 'rules_merged', logical_name: 'private-name' })).toBe('Merged rules')
  expect(c.state.itemName({ kind: 'rules_set', logical_name: 'set-id' })).toBe('Published set')
  expect(c.state.itemTip({ kind: 'rules_set', logical_name: 'set-id' })).toBe('set-id')
  expect(c.state.shortHash('a'.repeat(64))).toBe('a'.repeat(12))
})
