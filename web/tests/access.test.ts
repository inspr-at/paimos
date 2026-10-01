// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  AccessError, agentDeactivatePoints, agentScopeCeiling, auditSentence, beyond, categoryOf, defaultProjectRole, defaultWorkspaceRole, diff, effectLine, groupPermissions, inviteRoles,
  isLastOwner, permissionLabel, projectRolesOf, projectSummary, splitAgents, validEmail, workspaceRolesOf, type Agent, type Permission, type Role,
} from '../src/lib/access.ts'

const P = (key: string, group: string, risk: Permission['risk'], project = true): Permission => ({ key, group, description: key, risk, grantable_at: project ? ['workspace', 'project'] : ['workspace'] })
const registry = [P('nodes.read', 'Work', 'low'), P('nodes.delete', 'Work', 'high'), P('members.manage', 'People', 'high'), P('audit.read', 'People', 'medium', false), P('portal.quotes', 'Portal', 'low', false)]
const role = (id: string, key: string, permissions: string[], builtin = true): Role => ({ id, key, name: key[0]!.toUpperCase() + key.slice(1), description: '', builtin, permissions, based_on: null, member_count: 0 })

test('permissions read as words and group in registry order', () => {
  assert.equal(permissionLabel('members.manage'), 'Manage members')
  assert.equal(permissionLabel('nodes.read'), 'See work')
  assert.equal(permissionLabel('widgets.frob'), 'Frob widgets')
  assert.deepEqual(groupPermissions(registry).map(g => [g.group, g.items.length]), [['Work', 2], ['People', 2], ['Portal', 1]])
})

test('a role’s effect puts its riskiest permissions first; diffs and escalation', () => {
  assert.equal(effectLine(['nodes.read', 'members.manage', 'nodes.delete'], registry, 2), 'Can delete work, manage members, and 1 more.')
  assert.equal(effectLine([], registry), 'No access of its own.')
  assert.deepEqual(diff(['a', 'b'], ['b', 'c']), { added: ['c'], removed: ['a'] })
  assert.deepEqual(beyond(['nodes.read', 'members.manage'], new Set(['nodes.read'])), ['members.manage'])
})

test('the last active owner comes from the server, never from role keys', () => {
  assert.equal(isLastOwner({ last_owner: true }), true)
  assert.equal(isLastOwner({ last_owner: false }), false)
})

test('defaults, project roles, invites and summaries', () => {
  const roles = [role('r-owner', 'owner', ['nodes.read', 'members.manage']), role('r-member', 'member', ['nodes.read']), role('r-guest', 'guest', ['nodes.read']), role('r-customer', 'customer', ['portal.quotes'])]
  assert.equal(defaultWorkspaceRole(roles), 'r-member')
  assert.equal(defaultProjectRole(roles), 'r-guest')
  // The server refuses Owner and Customer on a project and Guest in the workspace.
  assert.deepEqual(projectRolesOf(roles, registry).map(r => r.key), ['member', 'guest'])
  assert.deepEqual(workspaceRolesOf(roles).map(r => r.key), ['owner', 'member', 'customer'])
  assert.equal(inviteRoles({ workspace_role: { id: 'r', key: 'member', name: 'Member' }, project_roles: [{ project_id: 'p', project_key: 'P', project_title: 'Pharos', role: { id: 'g', key: 'guest', name: 'Guest' } }] }), 'Member in the workspace · Guest on Pharos')
  assert.equal(inviteRoles({ workspace_role: null, project_roles: [] }), 'No access yet')
  assert.equal(projectSummary([]), '')
  const pr = (t: string) => ({ project_id: t, project_key: t, project_title: t, role: { id: 'g', key: 'guest', name: 'Guest' } })
  assert.equal(projectSummary([pr('A'), pr('B')]), 'A, B')
  assert.equal(projectSummary([pr('A'), pr('B'), pr('C')]), '3 projects')
  assert.equal(validEmail(' anna@studio.at '), true)
  assert.equal(validEmail('anna@'), false)
})

test('audit events read as sentences from the server’s shape', () => {
  const names = { principal: (id?: string | null) => ({ me: 'Markus', mira: 'Mira', jw: 'jw (classic)' } as Record<string, string>)[id ?? ''] ?? 'someone', project: (id?: string | null) => id === 'p1' ? 'Pharos' : '', role: (id?: string | null) => ({ r1: 'Member', r2: 'Admin' } as Record<string, string>)[id ?? ''] ?? '' }
  const e = (type: string, before: unknown, after: unknown, subject: string | null = null, project: string | null = null) => ({ id: 1, type, at: '2026-09-23T10:00:00Z', actor: { principal_id: 'me', name: 'Markus' }, subject: subject ? { principal_id: subject, name: names.principal(subject) } : null, project: project ? { id: project, key: 'PHA' } : null, data: { before, after } })
  // Project binding: role objects.
  assert.deepEqual(auditSentence(e('binding.set', { principal_id: 'mira', scope_type: 'project', project_id: 'p1', role: { name: 'Member' } }, { principal_id: 'mira', scope_type: 'project', project_id: 'p1', role: { name: 'Guest' } }, 'mira', 'p1'), names),
    { actor: 'Markus', subject: 'Mira', text: 'changed Mira’s role on Pharos from Member to Guest' })
  // Workspace role change: role ids.
  assert.equal(auditSentence(e('binding.set', { principal_id: 'mira', role_id: 'r1' }, { principal_id: 'mira', role_id: 'r2' }, 'mira'), names).text, 'changed Mira’s role in the workspace from Member to Admin')
  assert.equal(auditSentence(e('role.updated', { name: 'Lead', permissions: ['a'] }, { name: 'Lead', permissions: ['a', 'b', 'c'] }), names).text, 'changed the role Lead (2 added)')
  assert.equal(auditSentence(e('principal.alias_linked', { principal_id: 'jw', linked_to: null }, { principal_id: 'jw', linked_to: 'mira' }, 'jw'), names).text, 'linked jw (classic) to Mira')
  assert.equal(auditSentence(e('principal.deactivated', { principal_id: 'mira', status: 'active' }, { principal_id: 'mira', status: 'deactivated' }, 'mira'), names).text, 'deactivated Mira; their sessions and keys were revoked')
  assert.equal(auditSentence(e('agent_key.scopes_changed', { name: 'worker', role: { name: 'Agent worker', permissions: ['nodes.read'] } }, { name: 'worker', role: { name: 'Agent worker', permissions: ['nodes.read', 'nodes.write'] }, pruned_scopes: ['retired.scope'] }), names).text, 'changed scopes for the key worker; added nodes.write to the role Agent worker; pruned unknown scopes retired.scope')
  assert.equal(auditSentence(e('agent_key.revoked', null, { principal_id: 'x', name: 'deployer', prefix: 'ph4r' }), names).text, 'revoked the key deployer (aeon_ph4r_…)')
  assert.equal(auditSentence(e('something.else', null, {}), names).text, 'something else')
  assert.deepEqual(['role.created', 'binding.removed', 'invite.revoked', 'principal.deactivated', 'agent_key.created', 'node.updated'].map(categoryOf), ['roles', 'bindings', 'invites', 'lifecycle', 'keys', null])
})

test('errors carry the server’s reason and field', () => {
  const error = new AccessError(409, { error: 'conflict', code: 'last_owner', reason: 'The last owner stays.', field: 'role_id' })
  assert.equal(error.message, 'The last owner stays.')
  assert.equal(error.field, 'role_id')
  assert.equal(error.code, 'last_owner')
})

const agent = (name: string, extra: Partial<Agent> = {}): Agent => ({ principal_id: name, name, has_avatar: false, workspace_role: null, key_count: 0, last_seen_at: null, service: false, ...extra })

test('rotation and codes cap private or absent roles without promoting project grants', () => {
  const privateRole = role('private', 'agent_worker', ['nodes.read'], false)
  const projectRole = role('project', 'member', ['nodes.read', 'nodes.delete', 'audit.read'])
  const projectBinding = { project_id: 'p', project_key: 'P', project_title: 'Project', role: { id: projectRole.id, key: projectRole.key, name: projectRole.name } }
  const worker = agent('worker', { workspace_role: { id: privateRole.id, key: privateRole.key, name: privateRole.name }, project_roles: [projectBinding] })
  const roles = [privateRole, projectRole]
  assert.equal(agentScopeCeiling(worker, roles, registry), null)
  assert.deepEqual([...agentScopeCeiling(worker, roles, registry, true)!], ['nodes.read'])
  assert.deepEqual([...agentScopeCeiling(agent('unbound'), roles, registry, true)!], [])
  assert.equal(agentScopeCeiling(agent('new'), roles, registry), null)
  const projectOnly = agent('project-only', { project_roles: [projectBinding] })
  assert.deepEqual([...agentScopeCeiling(projectOnly, roles, registry, true)!], ['nodes.read', 'nodes.delete'])
  assert.deepEqual([...agentScopeCeiling(agent('stale', { workspace_role: { id: 'missing', key: 'missing', name: 'Missing' } }), roles, registry, true)!], [])
})

test('agents split into working, deactivated and internal; a server without status knows only working ones', () => {
  const groups = splitAgents([agent('a'), agent('b', { status: 'deactivated' }), agent('c', { status: 'active' }), agent('System', { service: true }), agent('d', { status: 'deactivated', connected_computer: false })])
  assert.deepEqual(groups.working.map(a => a.name), ['a', 'c'])
  assert.deepEqual(groups.deactivated.map(a => a.name), ['b', 'd'])
  assert.deepEqual(groups.service.map(a => a.name), ['System'])
})

test('deactivating an agent says how many keys go with it', () => {
  assert.match(agentDeactivatePoints(0)[0]!, /no active key/)
  assert.match(agentDeactivatePoints(1)[0]!, /Its active key is revoked now/)
  assert.match(agentDeactivatePoints(3)[0]!, /Its 3 active keys are revoked now/)
  assert.ok(agentDeactivatePoints(2).some(line => /revoked keys stay revoked/.test(line)))
  // A retired computer takes no key: its confirmation points to pairing afresh, never to a new key.
  const computer = agentDeactivatePoints(0, true).join(' ')
  assert.match(computer, /connect it afresh by pairing it/)
  assert.doesNotMatch(computer, /new one|new key/)
})
