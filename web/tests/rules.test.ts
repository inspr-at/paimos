// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { RequestFailure } from '../src/lib/api.ts'
import {
  IMPORT_MAX_BYTES, RulesError, applyEnabled, blankRule, calendarVersion, diffRules, duplicateRule, groupState, heldIdentities,
  identityFromText, layerInColumn, mergeQuery, parseDraftImport, publishBlock, resetAvailability, rulePayload, rulesEqual,
  runDraftImport, scopeFor, touchRule, validVersion, validateDraft, validateRule, writeBlock,
  type AgentRule, type Caller, type ImportIO, type RuleScope, type RuleSet,
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

const TENANT = 't1'
const SELF = '11111111-1111-4111-8111-111111111111'
const admin = person(['rules.write', 'rules.publish'])

function draftFile(layers: unknown, tenant = TENANT) {
  return JSON.stringify({ schema: 'aeon.rules-draft-import.v1', tenant_id: tenant, layers })
}
function importRule(identity: string, text: string, patch: Partial<AgentRule> = {}) {
  return rule({ identity, text, ...patch })
}

test('draft import accepts a person file and rejects anything it must not write', () => {
  const originalFetch = globalThis.fetch
  let fetched = false
  globalThis.fetch = () => { fetched = true; return Promise.reject(new Error('network')) }
  try {
    const file = draftFile([{
      scope: { layer: 'person', owner_id: SELF },
      sets: [{ name: 'Desk', rules: [importRule('desk-clear', 'Keep <script>alert(1)</script> as text.')] }],
    }])
    const parsed = parseDraftImport(file, file.length, TENANT, admin)
    assert.equal('plan' in parsed, true)
    if ('plan' in parsed) {
      assert.equal(parsed.plan.layers[0]?.sets[0]?.rules[0]?.text.includes('<script>'), true)
      assert.equal(parsed.plan.tenantId, TENANT)
    }
    assert.equal(fetched, false)
  } finally { globalThis.fetch = originalFetch }

  const other = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [{ name: 'Desk', rules: [] }] }], '99999999-9999-4999-8999-999999999999'), 80, TENANT, admin)
  assert.equal('error' in other && other.error, 'This file is for a different workspace.')
  const schema = parseDraftImport(JSON.stringify({ schema: 'other', tenant_id: TENANT, layers: [] }), 40, TENANT, admin)
  assert.match('error' in schema ? schema.error : '', /aeon\.rules-draft-import\.v1/)
  const extra = parseDraftImport(JSON.stringify({ schema: 'aeon.rules-draft-import.v1', tenant_id: TENANT, layers: [], publish: true }), 80, TENANT, admin)
  assert.match('error' in extra ? extra.error : '', /unknown field/)
  const malformed = parseDraftImport('{', 1, TENANT, admin)
  assert.match('error' in malformed ? malformed.error : '', /not JSON/)
  const huge = draftFile([])
  assert.match('error' in parseDraftImport(huge, IMPORT_MAX_BYTES + 1, TENANT, admin) ? (parseDraftImport(huge, IMPORT_MAX_BYTES + 1, TENANT, admin) as { error: string }).error : '', /2 MiB/)

  const lockedOff = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [{ name: 'Desk', rules: [importRule('stay-on', 'Stay on.', { strength: 'locked', enabled: false })] }] }]), 200, TENANT, admin)
  assert.match('error' in lockedOff ? lockedOff.error : '', /stays on/)
  const expiring = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [{ name: 'Desk', rules: [importRule('stay-on', 'Stay on.', { strength: 'locked', expires_at: '2026-10-01T00:00:00Z' })] }] }]), 220, TENANT, admin)
  assert.match('error' in expiring ? expiring.error : '', /does not expire/)
  const dupScope = parseDraftImport(draftFile([
    { scope: { layer: 'company' }, sets: [{ name: 'A', rules: [importRule('one', 'One.')] }] },
    { scope: { layer: 'company' }, sets: [{ name: 'B', rules: [importRule('two', 'Two.')] }] },
  ]), 300, TENANT, admin)
  assert.match('error' in dupScope ? dupScope.error : '', /more than once/)
  const dupSet = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [
    { name: 'Desk', rules: [importRule('one', 'One.')] },
    { name: 'Desk', rules: [importRule('two', 'Two.')] },
  ] }]), 300, TENANT, admin)
  assert.match('error' in dupSet ? dupSet.error : '', /listed twice/)
  const dupId = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [{ name: 'Desk', rules: [importRule('same', 'One.'), importRule('same', 'Two.')] }] }]), 300, TENANT, admin)
  assert.match('error' in dupId ? dupId.error : '', /already used/)
  const unknown = parseDraftImport(draftFile([{ scope: { layer: 'workspace' }, sets: [{ name: 'Desk', rules: [] }] }]), 80, TENANT, admin)
  assert.match('error' in unknown ? unknown.error : '', /layer/)
  const project = parseDraftImport(draftFile([{ scope: { layer: 'project' }, sets: [{ name: 'Desk', rules: [] }] }]), 80, TENANT, admin)
  assert.match('error' in project ? project.error : '', /project id/)
  const taken = parseDraftImport(draftFile([{ scope: { layer: 'company' }, sets: [{ name: 'Secrets', rules: [importRule('one', 'One.')] }] }]), 120, TENANT, admin, [{ scope: { layer: 'company' }, names: ['Secrets'] }])
  assert.match('error' in taken ? taken.error : '', /does not change existing/)
  const otherPerson = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: '33333333-3333-4333-8333-333333333333' }, sets: [{ name: 'Desk', rules: [importRule('one', 'One.')] }] }]), 160, TENANT, admin)
  assert.match('error' in otherPerson ? otherPerson.error : '', /Only that person/)
  const agentCaller = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [{ name: 'Desk', rules: [] }] }]), 80, TENANT, agent(['rules.write', 'rules.publish']))
  assert.match('error' in agentCaller ? agentCaller.error : '', /Only a person/)
  const member = parseDraftImport(draftFile([{ scope: { layer: 'company' }, sets: [{ name: 'Floor', rules: [importRule('one', 'One.')] }] }]), 120, TENANT, person(['rules.write']))
  assert.match('error' in member ? member.error : '', /publish/)

  const sets = Array.from({ length: 21 }, (_, index) => ({ name: `Set ${index}`, rules: [importRule(`rule-${index}`, `Rule ${index}.`)] }))
  const tooMany = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets }]), 4000, TENANT, admin)
  assert.match('error' in tooMany ? tooMany.error : '', /20 sets/)
  const rules = (start: number, count: number) => Array.from({ length: count }, (_, index) => importRule(`rule-${start + index}`, `Rule ${start + index}.`))
  const tooWide = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [{ name: 'A', rules: rules(0, 60) }, { name: 'B', rules: rules(60, 41) }] }]), 8000, TENANT, admin)
  assert.match('error' in tooWide ? tooWide.error : '', /100 rules/)
})

function memoryIO() {
  const calls: string[] = []
  const layers: { id: string; scope: RuleScope; names: string[] }[] = []
  let n = 0
  const io: ImportIO = {
    listLayers: async () => { calls.push('listLayers'); return { layers: layers.map(layer => ({ id: layer.id, scope: layer.scope })) } },
    listSets: async id => {
      calls.push(`listSets:${id}`)
      const layer = layers.find(item => item.id === id)
      const sets: RuleSet[] = (layer?.names ?? []).map(name => ({ id: `dddddddd-dddd-4ddd-8ddd-${name.length.toString(16).padStart(12, '0')}`, layer_id: id, scope: layer?.scope ?? { layer: 'company' }, name, revision: 1, rules: [], published_version: '' }))
      return { sets }
    },
    createLayer: async scope => {
      calls.push('createLayer')
      const layer = { id: 'bbbbbbbb-bbbb-4bbb-8bbb-000000000001', scope }
      layers.push({ ...layer, names: [] })
      return layer
    },
    createSet: async (layerId, name) => {
      calls.push(`createSet:${name}`)
      n += 1
      const layer = layers.find(item => item.id === layerId)
      layer?.names.push(name)
      return { id: `cccccccc-cccc-4ccc-8ccc-${n.toString(16).padStart(12, '0')}`, layer_id: layerId, scope: layer?.scope ?? { layer: 'company' }, name, revision: 1, rules: [], published_version: '' }
    },
    saveDraft: async (setId, body) => {
      calls.push(`saveDraft:${body.name}`)
      const layer = layers.find(item => item.names.includes(body.name))
      return { id: setId, layer_id: layer?.id ?? '', scope: layer?.scope ?? { layer: 'company' }, name: body.name, revision: body.expected_revision + 1, rules: body.rules, published_version: '' }
    },
  }
  return { calls, io, layers }
}

test('draft import writes new sets in order and stops without publishing', async () => {
  const parsed = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [
    { name: 'Desk', rules: [importRule('desk-clear', 'Keep the desk clear.')] },
    { name: 'Hours', rules: [importRule('hours-log', 'Log the hours.')] },
    { name: 'Later', rules: [importRule('later-note', 'Leave a note.')] },
  ] }]), 400, TENANT, admin)
  assert.equal('plan' in parsed, true)
  if (!('plan' in parsed)) return
  const memory = memoryIO()
  memory.io.saveDraft = async (setId, body) => {
    memory.calls.push(`saveDraft:${body.name}`)
    if (body.name === 'Hours') throw new RulesError(500, 'unavailable', 'The draft was not saved.')
    return { id: setId, layer_id: 'bbbbbbbb-bbbb-4bbb-8bbb-000000000001', scope: { layer: 'person', owner_id: SELF }, name: body.name, revision: body.expected_revision + 1, rules: body.rules, published_version: '' }
  }
  const partial = await runDraftImport(parsed.plan, TENANT, admin, memory.io)
  assert.equal(partial.status, 'partial')
  assert.equal(partial.confirmed.length, 2)
  assert.equal(partial.confirmed[0]?.rulesSaved, true)
  assert.equal(partial.confirmed[1]?.rulesSaved, false)
  assert.match(partial.message, /not saved/)
  assert.match(partial.message, /Nothing was rolled back or published/)
  assert.equal(memory.calls.includes('createSet:Later'), false)
  assert.equal(memory.calls.filter(call => call.startsWith('saveDraft:')).length, 2)

  const lost = memoryIO()
  let created = 0
  lost.io.createSet = async (layerId, name) => {
    created += 1
    lost.calls.push(`createSet:${name}`)
    if (created === 2) throw new RequestFailure('network')
    return { id: 'cccccccc-cccc-4ccc-8ccc-000000000001', layer_id: layerId, scope: { layer: 'person', owner_id: SELF }, name, revision: 1, rules: [], published_version: '' }
  }
  const uncertain = await runDraftImport(parsed.plan, TENANT, admin, lost.io)
  assert.equal(uncertain.status, 'uncertain')
  assert.match(uncertain.message, /Check the server/)
  assert.match(uncertain.message, /did not roll/)
  assert.equal(lost.calls.includes('createSet:Later'), false)
  assert.equal(lost.calls.filter(call => call.startsWith('createSet:')).length, 2)

  const present = memoryIO()
  present.layers.push({ id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', scope: { layer: 'company' }, names: ['Secrets'] })
  const company = parseDraftImport(draftFile([{ scope: { layer: 'company' }, sets: [{ name: 'Secrets', rules: [importRule('one', 'One.')] }] }]), 120, TENANT, admin)
  assert.equal('plan' in company, true)
  if (!('plan' in company)) return
  const refused = await runDraftImport(company.plan, TENANT, admin, present.io)
  assert.equal(refused.status, 'rejected')
  assert.equal(present.calls.some(call => call.startsWith('create')), false)
})

test('publish diff lists added, changed and removed rules', () => {
  const changes = diffRules(
    [rule(), rule({ identity: 'gone', text: 'Gone.' })],
    [rule({ text: 'Updated.' }), rule({ identity: 'added', text: 'Added.' })],
  )
  assert.deepEqual(changes.map(change => change.kind), ['changed', 'added', 'removed'])
})
