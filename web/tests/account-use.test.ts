// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1054: the matrix helpers. Risk: a tri-state box, a bulk change or Undo
// shows a state the server did not store, or the pairing preview promises ticks
// the rule (trigger T3) does not give.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { allowedSet, applyCells, cellKey, columnState, cursorBefore, failureText, nextFromTri, predictedForNewAccount, projectContextSummary, rowState, switchChoices, SWITCHES, tickable, validMatrix, type WorkContext } from '../src/lib/accountUse.ts'
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

test('switches offer two values; a migrated "only with updates" model rule stays visible but is never offered fresh', () => {
  const models = SWITCHES.find(s => s.key === 'new_models')!
  assert.deepEqual(switchChoices(models, 'allow').map(c => c.value), ['allow', 'deny'])
  assert.deepEqual(switchChoices(models, 'shipped_only').map(c => c.label), ['Allow automatically', 'Ask first', 'Only with PAIMOS updates'])
  // Once left, it keeps its place as a retired choice that cannot be picked (AEON-541).
  assert.deepEqual(switchChoices(models, 'deny', true).map(c => [c.value, !!c.retired]), [['allow', false], ['deny', false], ['shipped_only', true]])
  assert.deepEqual(switchChoices(models, 'shipped_only', true).map(c => !!c.retired), [false, false, false])
  assert.equal(switchChoices(SWITCHES[0]!, 'allow', true).length, 2, 'only the model switch has a retired value')
  for (const s of SWITCHES) assert.equal(s.choices.length, 2, s.key)
  assert.deepEqual(SWITCHES.find(s => s.key === 'new_projects')!.choices.map(c => c.value), ['default', 'holding'])
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
