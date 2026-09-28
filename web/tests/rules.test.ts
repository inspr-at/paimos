// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
  applyEnabled, blankRule, calendarVersion, diffRules, duplicateRule, groupState, heldIdentities,
  identityFromText, layerInColumn, mergeQuery, publishBlock, resetAvailability, rulePayload, rulesEqual,
  scopeFor, touchRule, validVersion, validateDraft, validateRule, writeBlock,
  type AgentRule, type Caller, type RuleScope,
} from '../src/lib/rules.ts'

const person = (grants: string[], project: string[] = []): Caller => ({
  id: '11111111-1111-4111-8111-111111111111', kind: 'person',
  allows: (permission, projectId) => grants.includes(permission) || (!!projectId && project.includes(permission)),
})
const agent = (grants: string[]): Caller => ({ ...person(grants), id: '22222222-2222-4222-8222-222222222222', kind: 'agent' })

function rule(patch: Partial<AgentRule> = {}): AgentRule {
  return {
    identity: 'keep-secrets', text: 'Never print the environment.', why: 'It would reveal credentials.',
    details: '', strength: 'normal', enabled: true, expires_at: null, roles: ['builder'], harnesses: ['codex'],
    source: { reference: 'AEON-252', edited_here: false }, ...patch,
  }
}

test('calendar versions are UTC inspr-calendar-v2 coordinates', () => {
  assert.equal(calendarVersion(new Date(Date.UTC(2026, 8, 28, 10, 56, 44))), '260928105644.0.0')
  assert.equal(validVersion('260928105644.0.0'), true)
  assert.equal(validVersion('260231105644.0.0'), false)
  assert.equal(validVersion('060928105644.0.0'), false)
})

test('locked rules and higher locks cannot be switched off', () => {
  const held = new Set(['keep-secrets'])
  const rules = [rule({ strength: 'locked' }), rule({ identity: 'local-copy', text: 'Local copy' }), rule({ identity: 'keep-secrets', text: 'Lower copy', enabled: true })]
  assert.equal(groupState(rules, held), 'on')
  const off = applyEnabled(rules, false, held)
  assert.equal(off[0].enabled, true)
  assert.equal(off[1].enabled, false)
  assert.equal(off[2].enabled, true)
  assert.equal(groupState(off, held), 'mixed')
  assert.equal(applyEnabled(off, true, held)[1].enabled, true)
})

test('a lower layer does not treat a higher locked identity as its own switch', () => {
  const ctx = { projectId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', personId: '11111111-1111-4111-8111-111111111111', agentId: '', role: 'builder' as const, harness: 'cursor' as const, taskId: '' }
  const sets = [
    { scope: { layer: 'company' as const }, rules: [rule({ strength: 'locked' })] },
    { scope: { layer: 'project' as const, project_id: ctx.projectId }, rules: [rule({ enabled: false })] },
  ]
  const held = heldIdentities(sets, ctx, 1)
  assert.equal(held.has('keep-secrets'), true)
  assert.equal(layerInColumn({ layer: 'project', project_id: ctx.projectId }, 'project', ctx, 'role'), true)
  assert.equal(layerInColumn({ layer: 'project', project_id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb' }, 'project', ctx, 'role'), false)
})

test('permissions follow grants, never a bare membership', () => {
  const company: RuleScope = { layer: 'company' }
  const project: RuleScope = { layer: 'project', project_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa' }
  const own: RuleScope = { layer: 'person', owner_id: person([]).id }
  const other: RuleScope = { layer: 'person', owner_id: '33333333-3333-4333-8333-333333333333' }
  assert.equal(writeBlock(agent(['rules.write', 'rules.publish']), company), 'Agents cannot edit company rules.')
  assert.equal(publishBlock(agent(['rules.publish']), company), 'Only a person can publish rules.')
  assert.match(writeBlock(person(['rules.write']), company) ?? '', /publish/)
  assert.equal(writeBlock(person(['rules.write', 'rules.publish']), company), null)
  assert.match(writeBlock(person([]), project) ?? '', /that project/)
  assert.match(writeBlock(person([], ['rules.write']), project) ?? '', /publish/)
  assert.equal(writeBlock(person([], ['rules.write', 'rules.publish']), project), null)
  assert.equal(publishBlock(person(['rules.publish'], []), project), null)
  assert.match(publishBlock(person([], ['rules.publish']), company) ?? '', /workspace/)
  assert.equal(writeBlock(person(['rules.write']), own), null)
  assert.match(writeBlock(person(['rules.write', 'rules.publish']), other) ?? '', /Only that person/)
  assert.match(publishBlock(person(['rules.publish']), other) ?? '', /Only that person/)
})

test('project and role edits need publish authority; own person and named agent stay writable', () => {
  const projectA = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
  const projectB = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
  const self = '11111111-1111-4111-8111-111111111111'
  const agentId = '22222222-2222-4222-8222-222222222222'
  const taskId = '33333333-3333-4333-8333-333333333333'
  const grants = (workspace: string[], byProject: Record<string, string[]> = {}, kind: Caller['kind'] = 'person', id = self): Caller => ({
    id, kind,
    allows: (permission, projectId) => workspace.includes(permission) || (!!projectId && (byProject[projectId] ?? []).includes(permission)),
  })
  const member = grants(['rules.read', 'rules.write'])
  const admin = grants(['rules.read', 'rules.write', 'rules.publish'])
  const project: RuleScope = { layer: 'project', project_id: projectA }
  const role: RuleScope = { layer: 'agent', role: 'builder' }
  const own: RuleScope = { layer: 'person', owner_id: self }
  const named: RuleScope = { layer: 'agent', owner_id: self, agent_id: agentId }
  const task: RuleScope = { layer: 'agent', project_id: projectA, owner_id: self, agent_id: agentId, task_id: taskId }

  assert.match(writeBlock(member, project) ?? '', /publish/)
  assert.match(writeBlock(member, role) ?? '', /workspace permission to publish/)
  assert.equal(writeBlock(admin, project), null)
  assert.equal(writeBlock(admin, role), null)
  assert.equal(writeBlock(member, own), null)
  assert.equal(writeBlock(member, named), null)
  assert.match(publishBlock(member, own) ?? '', /publish/)
  assert.equal(publishBlock(admin, own), null)
  assert.equal(publishBlock(grants(['rules.publish'], {}, 'agent', agentId), named), 'Only a person can publish rules.')

  const scoped = grants([], { [projectA]: ['rules.write', 'rules.publish'], [projectB]: ['rules.write'] })
  assert.equal(writeBlock(scoped, project), null)
  assert.match(writeBlock(scoped, { layer: 'project', project_id: projectB }) ?? '', /publish/)
  assert.match(writeBlock(scoped, role) ?? '', /workspace permission to write/)
  assert.equal(writeBlock(grants([], { [projectA]: ['rules.write'] }, 'person', self), task), null)
  assert.equal(writeBlock(grants(['rules.write'], {}, 'agent', agentId), { layer: 'agent', owner_id: self, agent_id: agentId }), null)
})

test('draft validation and payloads stay inside the contract', () => {
  const locked = rule({ strength: 'locked', enabled: false, expires_at: '2026-10-01T00:00:00Z' })
  assert.match(validateRule(locked, new Set()) ?? '', /stays on/)
  assert.equal(rulePayload(touchRule(locked, { strength: 'locked' })).enabled, true)
  assert.equal(rulePayload(touchRule(locked, { strength: 'locked' })).expires_at, undefined)
  assert.match(validateDraft('Secrets', [rule({ text: '' })]) ?? '', /one line/)
  assert.match(validateDraft('Secrets', [rule({ source: { reference: '', edited_here: false } })]) ?? '', /source/)
  const edited = touchRule(rule({ source: { reference: 'AEON-252', identity: 'keep-secrets', edited_here: false } }), { text: 'Never print env.' })
  assert.equal(edited.source.edited_here, true)
  const payload = rulePayload(rule())
  assert.equal(payload.source.revision, undefined)
  assert.equal(rulesEqual([rule()], [rule({ details: '' })]), true)
})

test('new identities stay unique and reset needs an original', () => {
  const taken = new Set(['new-rule'])
  assert.equal(identityFromText('New rule', taken), 'new-rule-2')
  assert.equal(blankRule(taken).identity, 'new-rule-2')
  const copy = duplicateRule(rule({ strength: 'locked' }), ['keep-secrets'])
  assert.equal(copy.strength, 'normal')
  assert.notEqual(copy.identity, 'keep-secrets')
  const reset = resetAvailability(rule({ source: { reference: 'INSPR', identity: 'keep-secrets', edited_here: true } }))
  assert.equal(reset.available, false)
  if (!reset.available) assert.equal(reset.show, true)
  const supplied = rule({ identity: 'keep-secrets', text: 'Original.' })
  const ready = resetAvailability(rule({ source: { reference: 'INSPR', identity: 'keep-secrets', edited_here: true } }), supplied)
  assert.equal(ready.available, true)
})

test('merge query names the preview and refuses a loose project id', () => {
  const ctx = { projectId: 'p-aeon', personId: '11111111-1111-4111-8111-111111111111', agentId: '', role: 'builder' as const, harness: 'cursor' as const, taskId: '' }
  assert.equal('error' in mergeQuery(ctx), true)
  const ready = mergeQuery({ ...ctx, projectId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', agentId: '22222222-2222-4222-8222-222222222222', taskId: '33333333-3333-4333-8333-333333333333' })
  assert.equal('query' in ready && ready.query.includes('harness=cursor') && ready.query.includes('task_id='), true)
  const scope = scopeFor('agent', { ...ctx, projectId: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', agentId: '22222222-2222-4222-8222-222222222222', taskId: '33333333-3333-4333-8333-333333333333' }, 'task')
  assert.equal('scope' in scope && scope.scope.task_id, '33333333-3333-4333-8333-333333333333')
})

test('publish diff lists added, changed and removed rules', () => {
  const changes = diffRules(
    [rule(), rule({ identity: 'gone', text: 'Gone.' })],
    [rule({ text: 'Updated.' }), rule({ identity: 'added', text: 'Added.' })],
  )
  assert.deepEqual(changes.map(change => change.kind), ['changed', 'added', 'removed'])
})
