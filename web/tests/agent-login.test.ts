// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { agentLoginCommand } from '../src/lib/agentLogin.ts'
import { agentScopeCeiling, type Role, type Permission } from '../src/lib/access.ts'

test('CLI login uses the current origin, excluding credentials, paths and query strings', () => {
  assert.equal(agentLoginCommand('https://pm.barta.cm/settings/access/agents?new=1'), "paimos --instance pm.barta.cm auth login --name pm.barta.cm --url 'https://pm.barta.cm'")
  assert.equal(agentLoginCommand('http://localhost:5175'), "paimos --instance localhost auth login --name localhost --url 'http://localhost:5175'")
  assert.equal(agentLoginCommand('http://[::1]:5175/settings'), "paimos --instance 1 auth login --name 1 --url 'http://[::1]:5175'")
  assert.equal(agentLoginCommand('https://[2001:db8::1]/x'), "paimos --instance 2001-db8--1 auth login --name 2001-db8--1 --url 'https://[2001:db8::1]'")
  assert.doesNotMatch(agentLoginCommand('http://[fe80::1]/'), /--(?:instance|name) -/)
})
test('project-only agent ceiling excludes workspace-only permissions and fails closed for missing roles', () => {
  const role: Role = { id: 'viewer', key: 'viewer', name: 'Viewer', description: '', permissions: ['nodes.read', 'account.manage'], builtin: true, based_on: null, member_count: 0 }
  const registry: Permission[] = [ { key: 'nodes.read', group: 'Work', description: '', risk: 'low', grantable_at: ['workspace', 'project'], agent_grantable: true }, { key: 'account.manage', group: 'Accounts', description: '', risk: 'high', grantable_at: ['workspace'], agent_grantable: true } ]
  const agent = { principal_id: 'agent', workspace_role: null, project_roles: [{ project_id: 'p', project_key: 'P', project_title: 'Project', role }] }
  assert.deepEqual([...agentScopeCeiling(agent, [role], registry)!], ['nodes.read'])
  assert.deepEqual([...agentScopeCeiling(agent, [], registry)!], [])
})
