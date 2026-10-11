// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1054: the matrix helpers. Risk: a tri-state box, a bulk change or Undo
// shows a state the server did not store, or the pairing preview promises ticks
// the rule (trigger T3) does not give.
import { afterEach, test } from 'node:test'
import assert from 'node:assert/strict'
import { allowedSet, applyCells, cellKey, columnState, contextNames, cursorBefore, failureText, modelRuleLabel, named, nextFromTri, predictedForNewAccount, projectContextSummary, rowState, ruleBody, ruleLabel, RULES, rulesPreview, saveRules, tickable, validMatrix, type RuleChange, type WorkContext } from '../src/lib/accountUse.ts'
import { APIError } from '../src/lib/api.ts'
import { capacityWaitText, isCapacityWait } from '../src/lib/capacityWait.ts'

const ctx = (id: string, name: string, kind: WorkContext['kind'] = 'regular', over: Partial<WorkContext> = {}): WorkContext => ({ id, name, kind, new_accounts_override: null, archived_at: null, revision: 1, ...over })
const contexts = [ctx('c2', 'Zeta client'), ctx('h', 'Unassigned', 'holding'), ctx('d', 'Default', 'default'), ctx('c1', 'Acme', 'regular', { new_accounts_override: 'deny' })]

test('columns: default first, then by name; Unassigned never takes ticks', () => {
  assert.deepEqual(tickable(contexts).map(c => c.id), ['d', 'c1', 'c2'])
})

test('tri-state rows and columns resolve to one value; anything but all becomes allowed', () => {
  const cols = tickable(contexts), accounts = [{ id: 'a', label: '', harness: 'codex', plan: null, billing_mode: 'api', owner_id: null }, { id: 'b', label: '', harness: 'claude', plan: null, billing_mode: 'subscription', owner_id: null }]
  const set = allowedSet([{ account_id: 'a', context_id: 'd', allowed: true }, { account_id: 'a', context_id: 'c1', allowed: true }, { account_id: 'a', context_id: 'c2', allowed: true }, { account_id: 'b', context_id: 'd', allowed: true }])
  assert.equal(rowState('a', cols, set), 'all')
  assert.equal(rowState('b', cols, set), 'some')
  assert.equal(columnState('d', accounts, set), 'all')
  assert.equal(columnState('c1', accounts, set), 'some')
  assert.equal(columnState('c2', [accounts[1]!], set), 'none')
  assert.equal(nextFromTri('all'), false)
  assert.equal(nextFromTri('some'), true)
  assert.equal(nextFromTri('none'), true)
  assert.equal(rowState('a', [], set), 'none', 'an empty row is never shown as all')
})

test('a save and its Undo restore exactly the earlier set', () => {
  const before = allowedSet([{ account_id: 'a', context_id: 'd', allowed: true }])
  const changes = [{ account_id: 'a', context_id: 'd', allowed: false }, { account_id: 'a', context_id: 'c2', allowed: true }]
  const undo = [{ account_id: 'a', context_id: 'd', allowed: true }, { account_id: 'a', context_id: 'c2', allowed: false }]
  const after = applyCells(before, changes)
  assert.deepEqual([...after], [cellKey('a', 'c2')])
  assert.deepEqual([...applyCells(after, undo)], [...before])
})

test('the pairing preview matches T3: ask first ticks nothing, allow skips "never" and Unassigned', () => {
  assert.deepEqual(predictedForNewAccount({ new_accounts: 'ask' }, contexts), [])
  assert.deepEqual(predictedForNewAccount({ new_accounts: 'allow' }, contexts).map(c => c.id), ['d', 'c2'])
})

test('For new things: three rules, Automatic or Ask first, each value with a sentence, an example and a short phrase in this workspace\'s names', () => {
  assert.deepEqual(RULES.map(d => d.key), ['new_accounts', 'new_contexts', 'new_projects'])
  for (const d of RULES) for (const value of [d.auto, d.ask]) {
    for (const words of [d.say, d.example, d.short]) assert.ok(words[value], `${d.key}:${value}`)
    assert.doesNotMatch(d.example[value]!, /@|mbp\d/, 'examples name no person or computer')
  }
  assert.deepEqual(RULES.map(d => [ruleLabel(d, d.auto), ruleLabel(d, d.ask)]), [['Automatic', 'Ask first'], ['Automatic', 'Ask first'], ['Automatic', 'Ask first']])
  // A renamed default context is named as it is called here.
  const names = contextNames([ctx('d', 'Everything else', 'default'), ctx('h', 'Unassigned', 'holding')])
  const projects = RULES.find(d => d.key === 'new_projects')!
  assert.equal(named(projects.say.default!, names), 'A new project goes to Everything else, so the accounts ticked there work on it.')
  assert.equal(rulesPreview({ new_accounts: 'ask', new_contexts: 'allow', new_projects: 'holding', new_models: 'allow' }, names), 'accounts ask first · contexts start with every account · projects wait in Unassigned')
  assert.deepEqual(contextNames([]), { default: 'Default', unassigned: 'Unassigned' })
  assert.deepEqual(['allow', 'deny', 'shipped_only'].map(v => modelRuleLabel(v as 'allow')), ['Automatic', 'Ask first', 'Only with PAIMOS updates'])
})

// Contract (AEON-1173 c2, AEON-1148): only Models' Auto-update writes new_models. A Settings rule save
// names only its own rules, so it cannot overwrite the model rule even when handed the whole rule set.
const originalFetch = globalThis.fetch
afterEach(() => { globalThis.fetch = originalFetch })
test('a rule save never carries new_models: only the rules it names travel', async () => {
  const stored = { new_accounts: 'allow', new_contexts: 'ask', new_projects: 'default', new_models: 'deny', revision: 6, enforced_at: null, confirmation_required: false, confirmed_at: null }
  const bodies: Record<string, unknown>[] = []
  globalThis.fetch = async (url, init) => {
    assert.equal(String(url), '/api/account-use/rules'); assert.equal(init?.method, 'PUT')
    bodies.push(JSON.parse(String(init?.body)))
    return Response.json(stored)
  }
  await saveRules(5, { new_accounts: 'allow' })
  // A caller holding every rule (as the old screen did) still sends only the three Settings rules.
  await saveRules(6, { ...stored } as unknown as RuleChange)
  await saveRules(7, { new_projects: 'holding', new_models: 'allow' } as unknown as RuleChange)
  assert.deepEqual(bodies, [
    { expected_revision: 5, new_accounts: 'allow' },
    { expected_revision: 6, new_accounts: 'allow', new_contexts: 'ask', new_projects: 'default' },
    { expected_revision: 7, new_projects: 'holding' },
  ])
  for (const body of bodies) assert.equal('new_models' in body, false)
  assert.deepEqual(ruleBody(1, {}), { expected_revision: 1 })
})

test('a partial or malformed matrix is rejected rather than shown', () => {
  const rules = { new_accounts: 'ask', new_contexts: 'ask', new_projects: 'default', new_models: 'allow', revision: 3, enforced_at: null, confirmation_required: false, confirmed_at: null }
  const good = { rules, accounts: [], contexts: [ctx('d', 'Default', 'default')], cells: [], next_account: null, next_context: null, running_outside: [], running_outside_truncated: false }
  assert.equal(validMatrix(good), true)
  assert.equal(validMatrix({ ...good, running_outside_truncated: undefined }), false)
  assert.equal(validMatrix({ ...good, rules: { ...rules, revision: 0 } }), false)
  assert.equal(validMatrix({ ...good, rules: { ...rules, new_models: 'maybe' } }), false)
  assert.equal(validMatrix({ ...good, contexts: Array.from({ length: 201 }, (_, i) => ctx(`c${i}`, 'X')) }), false)
})

test('a conflict says nothing was saved; other failures never claim success', () => {
  assert.match(failureText(new APIError(409, 'account_use_revision_conflict')), /was not saved; the current state is read again/)
  assert.match(failureText(new APIError(413, 'too big')), /more than 1,000 cells/)
  assert.match(failureText(new Error('network')), /was not saved/)
})

test('refusals for a context read "Not allowed for X"', () => {
  assert.equal(isCapacityWait({ code: 'context', run_now_allowed: false }), true)
  assert.equal(capacityWaitText({ code: 'context', context: 'Acme', run_now_allowed: false }), 'Not allowed for Acme')
  assert.equal(capacityWaitText({ code: 'context', context: { id: 'x' }, run_now_allowed: false }), 'No account is allowed for this project’s context')
})

test('a page starts exactly at a context: the cursor is the id just before it', () => {
  assert.equal(cursorBefore('e1000000-0000-4000-8000-000000000240'), 'e1000000-0000-4000-8000-00000000023f')
  assert.equal(cursorBefore('E1000000-0000-4000-8000-000000000000'), 'e1000000-0000-4000-7fff-ffffffffffff')
  assert.equal(cursorBefore('00000000-0000-0000-0000-000000000000'), undefined)
  assert.equal(cursorBefore('not-a-uuid'), undefined)
})

test('a project summary says when the account count is incomplete', () => {
  const acme = ctx('c1', 'Acme')
  assert.equal(projectContextSummary(false, null, [], false), 'This project has no context yet, so no account may work on it. Choose one.')
  assert.equal(projectContextSummary(true, null, [], false), 'No account may work on this project until it gets a context.')
  assert.equal(projectContextSummary(true, acme, ['Main', 'Extra'], false), '2 accounts may work here: Main, Extra.')
  assert.equal(projectContextSummary(true, acme, ['Main'], true), 'At least 1 account may work here: Main. Only the first 1,000 accounts were counted; see Settings under Accounts and computers for all.')
  assert.match(projectContextSummary(true, acme, Array.from({ length: 14 }, (_, i) => `A${i}`), false), /A11 and 2 more\.$/)
})
