// SPDX-License-Identifier: AGPL-3.0-only
// Effective permissions for UI mocks. Access decisions are driven by this
// endpoint, independently of deprecated principal.roles in /api/me.
export type MockRole = 'admin' | 'member' | 'viewer' | 'customer' | 'guest'

const common = ['authz.read', 'nodes.read', 'kinds.read', 'knowledge.read', 'journey.read', 'views.read', 'search.read', 'profile.read']
const member = [...common, 'nodes.write', 'nodes.move', 'nodes.delete', 'relations.read', 'relations.write', 'relations.delete', 'attachments.read', 'attachments.write', 'attachments.delete', 'knowledge.write', 'knowledge.delete', 'comments.write', 'comments.delete', 'journey.act', 'releases.write', 'requirements.write', 'harness.read', 'harness.control', 'run.claim', 'work_orders.write', 'approvals.decide', 'inbox.read', 'inbox.send', 'crm.read', 'crm.write', 'hours.read', 'hours.write', 'quotes.read', 'quotes.write', 'profile.write']
const admin = [...member, 'harness.recover', 'harness.force_stop', 'approvals.decide_high', 'intake.decide', 'inbox.manage', 'settings.manage', 'plugins.manage', 'project_groups.write', 'keys.read', 'keys.manage', 'members.read', 'members.manage', 'roles.read', 'roles.manage', 'quotes.manage', 'quotes.issue', 'hours.approve']

// Guest (ADR-003 P2) is a project role: nothing in the workspace, read and
// comment on the projects it is bound to.
const guest = ['authz.read', 'profile.read', 'kinds.read', 'nodes.read', 'relations.read', 'comments.read', 'comments.write', 'attachments.read', 'knowledge.read', 'journey.read', 'views.read', 'events.read', 'search.read']

export function mockGuestPermissions(projectId?: string, boundProjects: string[] = []) {
  const bound = !!projectId && boundProjects.includes(projectId)
  const permissions = bound ? [...guest] : []
  return { workspace: { role: null, permissions: [] }, project: projectId ? { id: projectId, role: bound ? { id: 'role-guest', key: 'guest', name: 'Guest' } : null, permissions } : null }
}

// Each call returns its own permission list. Callers add grants with push();
// handing out the module arrays made a later spec see the previous spec's grants.
export function mockEffectivePermissions(role: MockRole, projectId?: string) {
  if (role === 'guest') return mockGuestPermissions(projectId, projectId ? [projectId] : [])
  const source = role === 'admin' ? admin : role === 'member' ? member : role === 'customer' ? ['authz.read', 'profile.read', 'profile.write', 'quotes.portal_read', 'quotes.portal_accept'] : common
  const permissions = [...source]
  const ref = { id: `role-${role}`, key: role, name: role[0].toUpperCase() + role.slice(1) }
  // Copies: a test that adds a permission must not grant it to later tests in the worker.
  return { workspace: { role: ref, permissions: [...permissions] }, project: projectId ? { id: projectId, role: null, permissions: [...permissions] } : null }
}
