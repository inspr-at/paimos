// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { RequestFailure } from '../src/lib/api.ts'
import {
  IMPORT_MAX_BYTES, RulesError, applyEnabled, blankRule, calendarVersion, copyName, diffRules, diffSet, duplicateRule, groupState, groupsState, hasMovable, heldIdentities,
  identityFromText, layerInColumn, mergeQuery, parseDraftImport, publishBlock, replyUncertain, resetAvailability, resetRule, rulePayload, rulesEqual, tickTarget,
  runDraftImport, ruleSwitchLabel, scopeFor, touchRule, validVersion, validateDraft, validateRule, writeBlock, importWrites, draftImportProjects,
  setState, projectedRules, projectedBytes, largestProjected,
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
  assert.equal(tickTarget('on'), false)
  assert.equal(tickTarget('mixed'), true)
  assert.equal(groupsState([{ rules, held }, { rules: [rule({ identity: 'other', enabled: false })], held: new Set() }]), 'mixed')
  assert.equal(hasMovable([{ rules: [rule({ strength: 'locked' })], held: new Set() }]), false)
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
  assert.match(validateDraft('Secrets', [rule({ text: '' })]).error ?? '', /one line/)
  assert.equal(validateDraft('Secrets', [rule({ text: '' })]).warning, null)
  assert.match(validateDraft('Secrets', [rule({ source: { reference: '', edited_here: false } })]).error ?? '', /source/)
  const doubled = validateDraft('Secrets', [rule(), rule({ identity: 'keep-secrets-2' })])
  assert.equal(doubled.error, null)
  assert.equal(doubled.warning, 'This rule appears twice in this set; agents would get it twice.')
  assert.equal(validateDraft('Secrets', [rule(), rule({ identity: 'other', text: 'Say which tests ran.' })]).warning, null)
  assert.equal(ruleSwitchLabel('Record the source.', 'Secrets', true), 'Record the source. in Secrets is on')
  assert.equal(ruleSwitchLabel('Record the source.', 'Secrets', false), 'Record the source. in Secrets is off')
  assert.equal(ruleSwitchLabel('  ', 'Secrets (copy)', false), 'Untitled rule in Secrets (copy) is off')
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
  const copy = duplicateRule(rule({ strength: 'locked', text: 'Never print the environment.' }), ['keep-secrets'])
  assert.equal(copy.strength, 'normal')
  assert.equal(copy.text, 'Never print the environment.')
  assert.equal(copy.source.identity, undefined)
  assert.notEqual(copy.identity, 'keep-secrets')
  assert.equal(copyName('Secrets'), 'Secrets (copy)')
  assert.equal(copyName('Secrets', ['Secrets (copy)']), 'Secrets (copy 2)')
  assert.equal(copyName('Secrets (copy)', ['Secrets (copy)']), 'Secrets (copy 2)')
  assert.equal(copyName('Secrets', ['Secrets (copy)', 'Secrets (copy 2)']), 'Secrets (copy 3)')
  assert.ok(new TextEncoder().encode(copyName(`${'n'.repeat(200)}`)).length <= 128)
  assert.ok(new TextEncoder().encode(copyName('Secrets', ['Secrets (copy)', 'Secrets (copy 2)'])).length <= 128)
  const edited = rule({ text: 'Changed.', source: { reference: 'INSPR', identity: 'keep-secrets', edited_here: true } })
  const reset = resetAvailability(edited)
  assert.equal(reset.available, false)
  if (!reset.available) assert.equal(reset.show, true)
  const quiet = resetAvailability(rule({ source: { reference: 'INSPR', identity: 'keep-secrets', edited_here: false } }))
  assert.equal(quiet.available, false)
  if (!quiet.available) assert.equal(quiet.show, false)
  assert.equal(resetRule(edited, rule({ identity: 'other' })), null)
  const supplied = rule({ identity: 'keep-secrets', text: 'Original.', why: 'The template says so.' })
  const ready = resetAvailability(edited, supplied)
  assert.equal(ready.available, true)
  const restored = resetRule(edited, supplied)
  assert.equal(restored?.text, 'Original.')
  assert.equal(restored?.why, 'The template says so.')
  assert.equal(restored?.identity, 'keep-secrets')
  assert.equal(restored?.source.edited_here, false)
  assert.equal(restored?.source.reference, 'INSPR')
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
  assert.match('error' in dupId ? dupId.error : '', /already used in this set/)
  const dupText = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [{ name: 'Desk', rules: [importRule('one', 'Same line.'), importRule('two', 'Same line.')] }] }]), 300, TENANT, admin)
  assert.equal('plan' in dupText, true)
  const dupAcross = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [
    { name: 'Desk', rules: [importRule('same', 'One.')] },
    { name: 'Hours', rules: [importRule('same', 'Two.')] },
  ] }]), 300, TENANT, admin)
  assert.match('error' in dupAcross ? dupAcross.error : '', /already used in “Desk”/)
  assert.match('error' in dupAcross ? dupAcross.error : '', /one scope/)
  const override = parseDraftImport(draftFile([
    { scope: { layer: 'company' }, sets: [{ name: 'Floor', rules: [importRule('keep-secrets', 'Never print.')] }] },
    { scope: { layer: 'person', owner_id: SELF }, sets: [{ name: 'Desk', rules: [importRule('keep-secrets', 'My copy.')] }] },
  ]), 400, TENANT, admin)
  assert.equal('plan' in override, true)
  const projects = parseDraftImport(draftFile([
    { scope: { layer: 'project', project_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa' }, sets: [{ name: 'A', rules: [importRule('same', 'One.')] }] },
    { scope: { layer: 'project', project_id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb' }, sets: [{ name: 'B', rules: [importRule('same', 'Two.')] }] },
  ]), 400, TENANT, admin)
  assert.equal('plan' in projects, true)
  const unknown = parseDraftImport(draftFile([{ scope: { layer: 'workspace' }, sets: [{ name: 'Desk', rules: [] }] }]), 80, TENANT, admin)
  assert.match('error' in unknown ? unknown.error : '', /layer/)
  const project = parseDraftImport(draftFile([{ scope: { layer: 'project' }, sets: [{ name: 'Desk', rules: [] }] }]), 80, TENANT, admin)
  assert.match('error' in project ? project.error : '', /project id/)
  // A set that already exists by name in the same scope is compared, not refused:
  // identical sets are skipped, changed ones replace that set's saved draft.
  const existing = { id: 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', name: 'Secrets', revision: 4, rules: [importRule('one', 'One.'), importRule('gone', 'Gone.')] }
  const taken = parseDraftImport(draftFile([{ scope: { layer: 'company' }, sets: [
    { name: 'Secrets', rules: [importRule('one', 'One, sharper.'), importRule('two', 'Two.')] },
    { name: 'Fresh', rules: [importRule('three', 'Three.')] },
  ] }]), 200, TENANT, admin, [{ scope: { layer: 'company' }, sets: [existing] }])
  assert.equal('plan' in taken, true)
  if ('plan' in taken) {
    const [changed, fresh] = taken.plan.layers[0]!.sets
    assert.equal(changed?.action, 'update')
    assert.deepEqual(changed?.counts, { added: 1, changed: 1, unchanged: 0, removed: 1 })
    assert.equal(changed?.existing?.revision, 4)
    assert.equal(fresh?.action, 'create')
    assert.equal(importWrites(taken.plan), 2)
  }
  const same = parseDraftImport(draftFile([{ scope: { layer: 'company' }, sets: [{ name: 'Secrets', rules: existing.rules }] }]), 200, TENANT, admin, [{ scope: { layer: 'company' }, sets: [existing] }])
  assert.equal('plan' in same && same.plan.layers[0]?.sets[0]?.action, 'skip')
  assert.equal('plan' in same && importWrites(same.plan), 0)
  assert.deepEqual(draftImportProjects(draftFile([{ scope: { layer: 'project', project_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa' } }, { scope: { layer: 'project', project_id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa' } }, { scope: { layer: 'company' } }])), ['aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'])
  assert.deepEqual(draftImportProjects('{'), [])
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
    if (body.name === 'Hours') throw new RulesError(422, 'invalid_rule', 'The draft was not saved.')
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
  assert.match(refused.message, /changed since the file was reviewed/)
  assert.equal(present.calls.some(call => call.startsWith('create')), false)
  assert.equal(present.calls.some(call => call.startsWith('saveDraft')), false)
})

test('a reviewed update replaces only that draft, skips identical sets and reports each set', async () => {
  const memory = memoryIO()
  memory.layers.push({ id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', scope: { layer: 'company' }, names: ['Secrets', 'Git'] })
  const listed = await memory.io.listSets('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')
  const existing = [{ scope: { layer: 'company' } as RuleScope, sets: listed.sets }]
  const file = draftFile([{ scope: { layer: 'company' }, sets: [
    { name: 'Secrets', rules: [importRule('one', 'One.')] },
    { name: 'Git', rules: [] },
    { name: 'New', rules: [importRule('two', 'Two.')] },
  ] }])
  const parsed = parseDraftImport(file, file.length, TENANT, admin, existing)
  assert.equal('plan' in parsed, true)
  if (!('plan' in parsed)) return
  assert.deepEqual(parsed.plan.layers[0]!.sets.map(set => set.action), ['update', 'skip', 'create'])
  const outcome = await runDraftImport(parsed.plan, TENANT, admin, memory.io)
  assert.equal(outcome.status, 'imported')
  assert.deepEqual(outcome.results.map(result => [result.name, result.action, result.status]), [['Secrets', 'update', 'saved'], ['Git', 'skip', 'unchanged'], ['New', 'create', 'saved']])
  assert.deepEqual(memory.calls.filter(call => call.startsWith('createSet') || call.startsWith('saveDraft')), ['saveDraft:Secrets', 'createSet:New', 'saveDraft:New'])
  assert.equal(memory.calls.some(call => call.includes('publish')), false)

  // A failed update is reported for that set; later sets are reported as not started.
  const failing = memoryIO()
  failing.layers.push({ id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', scope: { layer: 'company' }, names: ['Secrets', 'Git'] })
  failing.io.saveDraft = async () => { throw new RulesError(409, 'revision_conflict', 'conflict') }
  const partial = await runDraftImport(parsed.plan, TENANT, admin, failing.io)
  assert.equal(partial.status, 'partial')
  assert.deepEqual(partial.results.map(result => result.status), ['failed', 'unchanged', 'not-started'])
  assert.match(partial.message, /not saved/)
})

test('set state, projected merge and budget follow the server merge', () => {
  const live = { name: 'Git', rules: [rule({ identity: 'b' }), rule({ identity: 'a' })] }
  assert.equal(setState({ name: 'Git', rules: [rule({ identity: 'a' }), rule({ identity: 'b' })], published_version: '260928100000.0.0' }, live), 'live')
  assert.equal(setState({ name: 'Git', rules: [rule({ identity: 'a' })], published_version: '260928100000.0.0' }, live), 'changed')
  assert.equal(setState({ name: 'Git', rules: [], published_version: '' }, null), 'new')
  const project = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
  const sets = [
    { id: 'c', scope: { layer: 'company' } as RuleScope, rules: [rule({ identity: 'keep', text: 'Company wins.', strength: 'locked', roles: [], harnesses: [] })] },
    { id: 'p', scope: { layer: 'project', project_id: project } as RuleScope, rules: [rule({ identity: 'keep', text: 'Project loses.' }), rule({ identity: 'off', enabled: false }), rule({ identity: 'codex-only', roles: [], harnesses: ['codex'] })] },
    { id: 'x', scope: { layer: 'project', project_id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb' } as RuleScope, rules: [rule({ identity: 'elsewhere' })] },
  ]
  const ctx = { projectId: project, personId: SELF, role: 'builder' as const, harness: 'claude-code' as const }
  assert.deepEqual(projectedRules(sets, ctx).map(item => item.text), ['Company wins.'])
  const bytes = (text: string) => new TextEncoder().encode(text).length
  assert.equal(projectedBytes(sets, ctx), bytes('# Aeon session rules\n\n- [keep] Company wins.\n'))
  const largest = largestProjected(sets, project, SELF)
  assert.equal(largest.harness, 'codex')
  assert.equal(largest.bytes, new TextEncoder().encode('# Aeon session rules\n\n- [codex-only] Never print the environment.\n- [keep] Company wins.\n').length)
})

test('a 5xx after a committed write is uncertain and does not continue', async () => {
  for (const status of [500, 502, 503, 504]) {
    assert.equal(replyUncertain(new RulesError(status, 'unavailable', 'lost')), true)
  }
  assert.equal(replyUncertain(new RulesError(409, 'revision_conflict', 'conflict')), false)
  assert.equal(replyUncertain(new RulesError(422, 'invalid_rule', 'rejected')), false)

  const parsed = parseDraftImport(draftFile([{ scope: { layer: 'person', owner_id: SELF }, sets: [
    { name: 'Desk', rules: [importRule('desk-clear', 'Keep the desk clear.')] },
    { name: 'Hours', rules: [importRule('hours-log', 'Log the hours.')] },
    { name: 'Later', rules: [importRule('later-note', 'Leave a note.')] },
  ] }]), 400, TENANT, admin)
  assert.equal('plan' in parsed, true)
  if (!('plan' in parsed)) return

  for (const status of [502, 503]) {
    const lost = memoryIO()
    lost.io.saveDraft = async (setId, body) => {
      lost.calls.push(`saveDraft:${body.name}`)
      if (body.name === 'Hours') throw new RulesError(status, 'unavailable', 'The draft was not saved.')
      return { id: setId, layer_id: 'bbbbbbbb-bbbb-4bbb-8bbb-000000000001', scope: { layer: 'person', owner_id: SELF }, name: body.name, revision: body.expected_revision + 1, rules: body.rules, published_version: '' }
    }
    const uncertain = await runDraftImport(parsed.plan, TENANT, admin, lost.io)
    assert.equal(uncertain.status, 'uncertain')
    assert.equal(uncertain.confirmed[0]?.rulesSaved, true)
    assert.equal(uncertain.confirmed[1]?.rulesSaved, null)
    assert.equal(uncertain.confirmed[1]?.set.revision, 1)
    assert.match(uncertain.message, new RegExp(uncertain.confirmed[1]?.set.id ?? 'missing'))
    assert.match(uncertain.message, /revision 1/)
    assert.match(uncertain.message, /unknown/)
    assert.match(uncertain.message, /Check the server/)
    assert.match(uncertain.message, /did not retry/)
    assert.doesNotMatch(uncertain.message, /not saved/)
    assert.doesNotMatch(uncertain.message, /not created/)
    assert.equal(lost.calls.includes('createSet:Later'), false)
    assert.equal(lost.calls.filter(call => call.startsWith('saveDraft:')).length, 2)
    assert.equal(lost.calls.some(call => call.includes('publish')), false)
  }

  const lostCreate = memoryIO()
  let created = 0
  lostCreate.io.createSet = async (layerId, name) => {
    created += 1
    lostCreate.calls.push(`createSet:${name}`)
    if (created === 2) throw new RulesError(502, 'unavailable', 'bad gateway')
    return { id: 'cccccccc-cccc-4ccc-8ccc-000000000001', layer_id: layerId, scope: { layer: 'person', owner_id: SELF }, name, revision: 1, rules: [], published_version: '' }
  }
  const createdUnknown = await runDraftImport(parsed.plan, TENANT, admin, lostCreate.io)
  assert.equal(createdUnknown.status, 'uncertain')
  assert.equal(createdUnknown.confirmed.length, 1)
  assert.match(createdUnknown.message, /did not confirm/)
  assert.doesNotMatch(createdUnknown.message, /not created/)
  assert.doesNotMatch(createdUnknown.message, /not saved/)
  assert.equal(lostCreate.calls.includes('createSet:Later'), false)
  assert.equal(lostCreate.calls.filter(call => call.startsWith('saveDraft:')).length, 1)
  assert.equal(lostCreate.calls.some(call => call.includes('publish')), false)
})

test('same-scope identity collisions are rejected before any write', async () => {
  const memory = memoryIO()
  const outcome = await runDraftImport({
    tenantId: TENANT,
    layers: [{
      scope: { layer: 'person', owner_id: SELF },
      sets: [
        { name: 'Desk', rules: [rule({ identity: 'same', text: 'One.' })] },
        { name: 'Hours', rules: [rule({ identity: 'same', text: 'Two.' })] },
      ],
    }],
  }, TENANT, admin, memory.io)
  assert.equal(outcome.status, 'rejected')
  assert.match(outcome.message, /already used in “Desk”/)
  assert.equal(memory.calls.some(call => call.startsWith('create')), false)
  assert.equal(memory.calls.some(call => call.startsWith('saveDraft')), false)
  assert.equal(memory.calls.some(call => call.includes('publish')), false)
})

test('publish diff lists added, changed and removed rules', () => {
  const changes = diffRules(
    [rule(), rule({ identity: 'gone', text: 'Gone.' })],
    [rule({ text: 'Updated.' }), rule({ identity: 'added', text: 'Added.' })],
  )
  assert.deepEqual(changes.map(change => change.kind), ['changed', 'added', 'removed'])
})

test('the approval lists TL;DR changes on their own, never as a rename or an unchanged rule', () => {
  const tldr = (en: string, extra: Record<string, unknown> = {}) => ({ en, basis: '0123456789abcdef', ...extra })
  const live = { name: 'Secrets', tldr: tldr('Keeps secrets out.'), rules: [
    rule({ identity: 'env', text: 'Never print the environment.', tldr: tldr('Dumps leak.') }),
    rule({ identity: 'src', text: 'Record the source.' }),
    rule({ identity: 'fit', text: 'Keep it short.', tldr: tldr('Short lines.') }),
  ] }
  const draft = { name: 'Secrets', tldr: tldr('No credential reaches a transcript.', { de: 'Keine Zugangsdaten.' }), rules: [
    rule({ identity: 'env', text: 'Never print the environment.' }),
    rule({ identity: 'src', text: 'Record the source.', tldr: tldr('Names its ticket.') }),
    rule({ identity: 'fit', text: 'Keep it short.', tldr: tldr('Short lines.', { basis: 'fedcba9876543210' }) }),
  ] }
  assert.deepEqual(diffSet(live, draft), [
    { kind: 'changed', label: 'No credential reaches a transcript.', about: 'set-tldr', de: 'Keine Zugangsdaten.' },
    { kind: 'removed', label: 'Dumps leak.', about: 'tldr', rule: 'Never print the environment.' },
    { kind: 'added', label: 'Names its ticket.', about: 'tldr', rule: 'Record the source.' },
    { kind: 'changed', label: 'Short lines.', about: 'tldr', rule: 'Keep it short.', confirmed: true },
  ])
  assert.deepEqual(diffSet({ ...live, tldr: null }, { ...live, name: 'Credentials' }).map(change => [change.kind, change.about]), [['changed', 'name'], ['added', 'set-tldr']])
  assert.deepEqual(diffSet(live, { ...live, tldr: null }), [{ kind: 'removed', label: 'Keeps secrets out.', about: 'set-tldr' }])
  const reworded = diffSet(live, { ...live, rules: [rule({ identity: 'env', text: 'Never dump the environment.', tldr: tldr('Dumps leak.') }), ...live.rules.slice(1)] })
  assert.deepEqual(reworded, [{ kind: 'changed', label: 'Never dump the environment.' }])
})

test('budget refusals keep another person’s size private in the message', async () => {
  const { rulesMessage } = await import('../src/lib/rules.ts')
  assert.equal(rulesMessage(new RulesError(422, 'rules_budget_exceeded', 'x', 12345, 12000)), 'The merged file is 12,345 bytes. The limit is 12,000.')
  assert.equal(rulesMessage(new RulesError(422, 'rules_budget_exceeded', 'A session file for another person or agent would exceed the limit.')), 'A session file for another person or agent would exceed the limit.')
  assert.match(rulesMessage(new RulesError(503, 'busy', 'busy')), /Nothing was changed/)
  assert.equal(replyUncertain(new RulesError(503, 'busy', 'busy')), false)
  assert.equal(replyUncertain(new RulesError(503, 'outcome_unknown', 'unknown')), true)
  assert.equal(rulesMessage(new RulesError(503, 'outcome_unknown', 'The set may have been created. Reload and check before creating it again.')), 'The set may have been created. Reload and check before creating it again.')
  assert.doesNotMatch(rulesMessage(new RulesError(503, 'outcome_unknown', '')), /safe/)
})

// ---------- AEON-314: explanations and the configurable budget ----------
const PROJECT = '33333333-3333-4333-8333-333333333333'
const ME = '11111111-1111-4111-8111-111111111111'
test('explanations travel with the draft without the derived check mark', async () => {
  const { rulePayload, rulesEqual, normalizeRule, setState, tldrEqual } = await import('../src/lib/rules.ts')
  const explained = rule({ tldr: { en: ' Keeps main safe. ', de: 'Schützt main.', basis: '0123456789abcdef', check: true } })
  const payload = rulePayload(normalizeRule(explained))
  assert.deepEqual(payload.tldr, { en: 'Keeps main safe.', de: 'Schützt main.', basis: '0123456789abcdef' })
  assert.equal(rulePayload(rule({ tldr: { en: '  ' } })).tldr, undefined)
  assert.equal(rulesEqual([rule()], [explained]), false)
  assert.equal(tldrEqual({ en: 'a', check: true }, { en: 'a' }), true)
  const live = { name: 'Git', rules: [rule()], tldr: null }
  const set = { name: 'Git', rules: [rule()], published_version: '260929100000.0.0', tldr: null }
  assert.equal(setState(set, live), 'live')
  assert.equal(setState({ ...set, tldr: { en: 'Git hygiene.' } }, live), 'changed')
  assert.equal(setState({ ...set, rules: [explained] }, live), 'changed')
})

test('an explanation is one short English line, German optional', async () => {
  const { validateTLDR, validateRule } = await import('../src/lib/rules.ts')
  assert.equal(validateTLDR('Short.', true), null)
  assert.match(validateTLDR('', true) ?? '', /Write a short explanation/)
  assert.equal(validateTLDR('', false), null)
  assert.match(validateTLDR('x'.repeat(301), true) ?? '', /300 bytes/)
  assert.match(validateTLDR('two\nlines', true) ?? '', /one line/)
  assert.match(validateRule(rule({ tldr: { en: '', de: 'Nur Deutsch.' } }), new Set()) ?? '', /Explanation/)
  assert.equal(validateRule(rule({ tldr: { en: 'Fine.' } }), new Set()), null)
})

test('the projected file counts each layer and finds the file furthest over a limit', async () => {
  const { projectedUsage, budgetExcess, largestProjected, budgetParts, budgetLine, overBudget, MERGE_HEADING_BYTES, DEFAULT_BUDGET } = await import('../src/lib/rules.ts')
  const company = { id: 'a', scope: { layer: 'company' as const }, rules: [rule({ identity: 'floor', text: 'Keep the floor.', strength: 'locked', roles: [], harnesses: [] })] }
  const project = { id: 'b', scope: { layer: 'project' as const, project_id: PROJECT }, rules: [rule({ identity: 'style', text: 'Use style.', roles: [], harnesses: [] }), rule({ identity: 'codex', text: 'Codex only rule.', roles: [], harnesses: ['codex'] })] }
  const ctx = { projectId: PROJECT, personId: ME, role: 'builder' as const, harness: 'claude-code' as const }
  const { bytes, usage } = projectedUsage([company, project], ctx)
  assert.equal(usage.company, '- [floor] Keep the floor.\n'.length)
  assert.equal(usage.project, '- [style] Use style.\n'.length)
  assert.equal(bytes, MERGE_HEADING_BYTES + usage.company! + usage.project!)
  const tight = { max_bytes: 12000, layer_max_bytes: { project: 30 } }
  assert.equal(budgetExcess(bytes, usage, tight), 0)
  const worst = largestProjected([company, project], PROJECT, ME, new Date(), tight)
  assert.equal(worst.harness, 'codex')
  assert.ok(budgetExcess(worst.bytes, worst.usage, tight) > 0)
  assert.ok(overBudget(worst.usage, worst.bytes, tight))
  assert.equal(overBudget(usage, bytes, DEFAULT_BUDGET), false)
  const parts = budgetParts({ company: 7100, project: 2300 }, 9422, { max_bytes: 12000, layer_max_bytes: { company: 8000 } })
  assert.deepEqual(parts.map(part => part.label), ['Company 7.1 of 8 kB', 'Project 2.3 kB', 'total 9.4 of 12 kB'])
  assert.equal(budgetLine({ company: 8100 }, 8200, { max_bytes: 12000, layer_max_bytes: { company: 8000 } }), 'Company 8.1 of 8 kB · total 8.2 of 12 kB')
  assert.equal(budgetParts({ company: 8100 }, 8200, { max_bytes: 12000, layer_max_bytes: { company: 8000 } })[0]!.over, true)
})

test('a layer cap refusal names the layer', async () => {
  const { rulesMessage } = await import('../src/lib/rules.ts')
  assert.equal(rulesMessage(new RulesError(422, 'rules_budget_exceeded', 'x', 8123, 8000, 'company')), 'Company rules would take 8,123 bytes of the session file. Their cap is 8,000.')
})

test('an import keeps the explanations of the rules it updates', async () => {
  const { annotateImport } = await import('../src/lib/rules.ts')
  const scope = { layer: 'company' as const }
  const stored = [rule({ identity: 'git', text: 'Old.', tldr: { en: 'Explained.', basis: '0123456789abcdef' } }), rule({ identity: 'same', tldr: { en: 'Kept.' } })]
  const plan = annotateImport({ tenantId: 't', layers: [{ scope, sets: [{ name: 'Git', rules: [rule({ identity: 'git', text: 'New.' }), rule({ identity: 'same' })] }] }] }, [{ scope, sets: [{ id: 's', name: 'Git', revision: 3, rules: stored }] }])
  const set = plan.layers[0]!.sets[0]!
  assert.equal(set.rules[0]!.tldr?.en, 'Explained.')
  assert.equal(set.rules[0]!.tldr?.basis, '0123456789abcdef')
  assert.equal(set.counts?.unchanged, 1)
  assert.equal(set.counts?.changed, 1)
})
