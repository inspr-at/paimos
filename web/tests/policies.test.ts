// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { createPoliciesReader, deniedPolicy, ELSEWHERE_RULES, keyLimits, ownerLinks, stepState, truncatedLadder, type PolicyLadder, type PolicyStep } from '../src/lib/policies.ts'
import type { Permission } from '../src/lib/access.ts'

const ladder = (role: PolicyLadder['role'] = 'review-gate'): PolicyLadder => ({ role, setup: true, truncated: false, steps: [], dispatch_family_order: ['openai', 'xai', 'anthropic', 'cursor', 'google', 'local'], review_floors: ['Other family'] })
function deferred<T>() { let resolve!: (value: T) => void, reject!: (error: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { resolve, reject, promise } }

test('each tab calls its own source and refusals disclose no other tab data', async () => {
  const paths: string[] = []
  const reader = createPoliciesReader(() => 'tenant/person', async path => { paths.push(path); return path.startsWith('/models') ? ladder() : [] })
  await reader.load('ladders', 'review-gate', false)
  assert.equal(reader.state.value, 'denied'); assert.deepEqual(paths, [])
  await reader.load('elsewhere', 'review-gate', true)
  assert.equal(reader.state.value, 'loaded'); assert.deepEqual(paths, [])
  await reader.load('ladders', 'review-gate', true)
  assert.equal(reader.state.value, 'loaded'); assert.ok(reader.ladder.value)
  await reader.load('keys', 'review-gate', true)
  assert.equal(reader.ladder.value, null)
  assert.deepEqual(paths, ['/models/routes?role=review-gate', '/authz/permissions'])
  assert.equal(deniedPolicy('ladders'), "You can't see this: it needs See models.")
  assert.match(deniedPolicy('keys'), /See roles and a person session/)
})

test('tab and role switches discard slow successes and failures, even when aborted requests still resolve', async () => {
  const old = deferred<PolicyLadder>(), newer = deferred<PolicyLadder>()
  let calls = 0
  const reader = createPoliciesReader(() => 'tenant/person', () => (++calls === 1 ? old.promise : newer.promise))
  const a = reader.load('ladders', 'review-gate', true)
  const b = reader.load('ladders', 'build', true)
  newer.resolve(ladder('build')); await b
  old.resolve(ladder()); await a
  assert.equal(reader.ladder.value?.role, 'build')
  const stale = deferred<PolicyLadder>()
  const second = createPoliciesReader(() => 'tenant/person', () => stale.promise)
  const pending = second.load('ladders', 'review-gate', true)
  await second.load('elsewhere', 'review-gate', true)
  stale.reject(new Error('late failure')); await pending
  assert.equal(second.state.value, 'loaded'); assert.equal(second.ladder.value, null)
})

test('identity, tenant, permission loss and disposal discard responses and clear read state', async () => {
  for (const next of ['tenant/another-person', 'another-tenant/person', '']) {
    let owner = 'tenant/person'
    const answer = deferred<PolicyLadder>()
    const reader = createPoliciesReader(() => owner, () => answer.promise)
    const pending = reader.load('ladders', 'review-gate', true)
    owner = next; reader.reset(); answer.resolve(ladder()); await pending
    assert.equal(reader.ladder.value, null); assert.deepEqual(reader.registry.value, [])
  }
  const held = deferred<PolicyLadder>()
  const reader = createPoliciesReader(() => 'tenant/person', () => held.promise)
  const pending = reader.load('ladders', 'review-gate', true)
  await reader.load('ladders', 'review-gate', false)
  held.resolve(ladder()); await pending
  assert.equal(reader.state.value, 'denied'); assert.equal(reader.ladder.value, null)
  reader.dispose()
})

test('failures and invalid or over-limit answers never report success; other tabs still work', async () => {
  for (const answer of [Promise.reject(Object.assign(new Error('denied'), { status: 403 })), Promise.reject(Object.assign(new Error('timeout'), { status: 503 })), Promise.resolve({ ...ladder(), role: 'build' } as PolicyLadder), Promise.resolve({ ...ladder(), steps: Array(51).fill({}) })]) {
    const reader = createPoliciesReader(() => 'tenant/person', () => answer)
    await reader.load('ladders', 'review-gate', true)
    assert.notEqual(reader.state.value, 'loaded'); assert.equal(reader.ladder.value, null)
    await reader.load('elsewhere', 'review-gate', true)
    assert.equal(reader.state.value, 'loaded')
  }
})

test('live registry exclusions group by risk, and owner links need actual destination permissions', () => {
  const p = (key: string, risk: Permission['risk'], agent_grantable: boolean): Permission => ({ key, risk, agent_grantable, description: key, group: '', grantable_at: ['workspace'] })
  assert.deepEqual(keyLimits([p('allowed', 'high', true), p('excluded-low', 'low', false), p('excluded-high', 'high', false)]).map(g => [g.risk, g.items.map(p => p.key)]), [['high', ['excluded-high']], ['low', ['excluded-low']]])
  assert.deepEqual(ownerLinks(() => false), [])
  assert.deepEqual(ownerLinks(p => p === 'account.read').map(row => row.to), ['/settings/accounts'])
  assert.equal(ownerLinks(p => p === 'roles.read').length, 0)
  assert.equal(ELSEWHERE_RULES.find(row => row.owner === 'GitHub')?.status, 'Advisory')
  for (const title of ['Model preferences', 'Review ladder editing', 'Fix rounds and lane pause']) assert.equal(ELSEWHERE_RULES.find(row => row.title === title)?.to, undefined)
  assert.equal(ELSEWHERE_RULES.some(row => /freeze|cutoff|entry_closes_at/i.test(row.title)), false)
})

test('truncation and suspension expiry are honest with an injected observation time', () => {
  const now = Date.parse('2026-10-02T18:00:00Z')
  const step = { state: 'unavailable', reason: 'out of credit', valid_until: '2026-10-02T19:30:00Z' } as PolicyStep
  assert.equal(stepState(step, now), 'Unavailable · out of credit · until 19:30 UTC (2 Oct 2026)')
  assert.equal(stepState(step, now + 7200000), 'Suspension expired · eligible again')
  assert.match(truncatedLadder(), /first 50 steps; the rest are applied by AEON but not listed here/)
})
