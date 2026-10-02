// SPDX-License-Identifier: AGPL-3.0-only
// The authz contract (ADR-003) in memory, for the Access UI specs: the permission
// registry, built-in and custom roles, people with aliases, agents, invites,
// project bindings, the access audit and /api/me/permissions. It keeps the
// contract's rules (last owner, no escalation, roles in use, field reasons), so
// specs see the answers the real server gives. Register after the work mocks.
import type { Page, Route } from '@playwright/test'

export const ME = '11111111-1111-4111-8111-111111111111'
export const MIRA = '22222222-2222-4222-8222-222222222222'
export const JONAS = '33333333-3333-4333-8333-333333333333'
export const LENA = '44444444-4444-4444-8444-444444444444'
export const PAUL = '55555555-5555-4555-8555-555555555555'
export const CLEO = '66666666-6666-4666-8666-666666666666'
export const MBA_CLASSIC = '77777777-7777-4777-8777-777777777771'
export const JW_CLASSIC = '77777777-7777-4777-8777-777777777772'
export const AGENTUR_CLASSIC = '77777777-7777-4777-8777-777777777773'
export const COORDINATOR = '88888888-8888-4888-8888-888888888881'
export const DEPLOYER = '88888888-8888-4888-8888-888888888882'
export const SYSTEM = '88888888-8888-4888-8888-888888888889'
const now = Date.parse('2026-09-23T12:00:00Z')
const ago = (hours: number) => new Date(now - hours * 3_600_000).toISOString()

type Risk = 'low' | 'medium' | 'high'
// agent_grantable mirrors internal/authz/registry.go: human governance, approval
// decisions and the portal never go on an agent key.
const NOT_FOR_AGENTS = new Set(['ownership.transfer', 'members.manage', 'roles.manage', 'keys.manage', 'keys.read', 'settings.manage', 'audit.read', 'approvals.decide', 'approvals.decide_high', 'portal.quotes'])
const P = (key: string, group: string, description: string, risk: Risk, project = true) => ({ key, group, description, risk, grantable_at: project ? ['workspace', 'project'] : ['workspace'], agent_grantable: !NOT_FOR_AGENTS.has(key) })
export const REGISTRY = [
  P('nodes.read', 'Work', 'See projects, tickets and their history', 'low'),
  P('nodes.write', 'Work', 'Create and edit tickets, move them and change their status', 'medium'),
  P('nodes.delete', 'Work', 'Delete tickets and projects', 'high'),
  P('comments.read', 'Work', 'Read ticket comments', 'low'),
  P('comments.write', 'Work', 'Comment on tickets', 'low'),
  P('events.read', 'History', 'Read ticket activity and history', 'low'),
  P('search.read', 'Search', 'Search projects and tickets you may read', 'low'),
  P('knowledge.read', 'Knowledge', 'Read runbooks, guidelines and memories', 'low'),
  P('knowledge.write', 'Knowledge', 'Write and change knowledge entries', 'medium'),
  P('quotes.read', 'Business', 'See quotes and customers', 'low'),
  P('quotes.write', 'Business', 'Draft and change quotes', 'medium'),
  P('quotes.issue', 'Business', 'Issue quotes to customers', 'high', false),
  P('hours.log', 'Business', 'Log hours', 'low'),
  P('hours.approve', 'Business', 'Approve and lock hours', 'medium', false),
  P('approvals.decide', 'Agents', 'Approve or deny what agents ask to do', 'high', false),
  P('keys.manage', 'Agents', 'Create and revoke agent keys', 'high', false),
  P('members.read', 'People and access', 'See who is in the workspace and their roles', 'low'),
  P('members.manage', 'People and access', 'Invite people, change their roles and deactivate them', 'high'),
  P('roles.manage', 'People and access', 'Create, change and delete custom roles', 'high', false),
  P('audit.read', 'People and access', 'Read the access log', 'medium', false),
  P('kinds.manage', 'Workspace', 'Change ticket types and their fields', 'high', false),
  P('settings.manage', 'Workspace', 'Change workspace settings', 'high', false),
  P('workspace.manage', 'Workspace', 'Rename or close the workspace and appoint owners', 'high', false),
  P('ownership.transfer', 'Ownership', 'Transfer workspace ownership', 'high', false),
  P('portal.quotes', 'Customer portal', 'See and accept their own quotes', 'low', false),
]
const ALL = REGISTRY.map(p => p.key)
const MEMBER = ['nodes.read', 'nodes.write', 'comments.read', 'comments.write', 'events.read', 'search.read', 'knowledge.read', 'knowledge.write', 'quotes.read', 'hours.log', 'members.read']
export interface MockRole { id: string; key: string; name: string; description: string; builtin: boolean; permissions: string[]; based_on: string | null }
const builtin = (key: string, name: string, description: string, permissions: string[]): MockRole => ({ id: `role-${key}`, key, name, description, builtin: true, permissions, based_on: null })
export function roles(): MockRole[] {
  return [
    builtin('owner', 'Owner', 'Everything, including the workspace itself and who owns it.', ALL.filter(k => k !== 'portal.quotes')),
    builtin('admin', 'Admin', 'Runs the workspace: people, roles, settings and agents.', ALL.filter(k => k !== 'portal.quotes' && k !== 'workspace.manage' && k !== 'ownership.transfer')),
    builtin('member', 'Member', 'Does the work: tickets, knowledge, quotes and hours.', MEMBER),
    builtin('viewer', 'Viewer', 'Reads everything, changes nothing.', ['nodes.read', 'knowledge.read', 'quotes.read', 'members.read']),
    builtin('guest', 'Guest', 'An outside collaborator on the projects they are given.', ['nodes.read', 'nodes.write', 'comments.write', 'knowledge.read']),
    builtin('customer', 'Customer', 'A client in the portal: their own quotes.', ['portal.quotes']),
    { id: 'role-lead', key: 'delivery-lead', name: 'Delivery lead', description: 'A member who also approves hours and issues quotes.', builtin: false, permissions: [...MEMBER, 'hours.approve', 'quotes.write', 'quotes.issue'], based_on: 'role-member' },
  ]
}
const ref = (role: MockRole) => ({ id: role.id, key: role.key, name: role.name })

export interface AccessWorld {
  me: string
  roles: MockRole[]
  people: { principal_id: string; name: string; avatar_url: string | null; email: string | null; status: 'active' | 'deactivated'; identity: 'inspr_id' | null; workspace_role: string | null; aliases: { principal_id: string; name: string; source: 'classic' }[]; classic_role: string | null; last_active_at: string | null }[]
  agents: { description?: string; principal_id: string; name: string; workspace_role: string | null; last_seen_at: string | null; service: boolean; status?: 'active' | 'deactivated'; connected_computer?: boolean; paired_computer?: boolean }[]
  imported: { principal_id: string; name: string; classic_role: string | null; email?: string }[]
  bindings: { principal_id: string; project_id: string; role_id: string }[]
  invites: { id: string; email: string; workspace_role: string | null; project_roles: { project_id: string; role_id: string }[]; status: 'pending' | 'expired' | 'revoked' | 'accepted'; created_by: string; created_at: string; expires_at: string }[]
  keys: { id: string; principal_id: string; name: string; prefix: string; scopes: string[]; created_at: string; expires_at: string | null; last_used_at: string | null; revoked_at: string | null }[]
  events: { id: number; actor_principal_id: string; type: string; before: unknown; after: unknown; at: string }[]
  projects: Record<string, { key: string; title: string }>
  calls: { method: string; path: string; body: unknown }[]
  available: boolean
  provisioner?: string
  provisionFailures?: number
  // The session ended: every call answers 401, as the server does.
  sessionEnded?: boolean
  // Milliseconds before a create (invite, key) answers, to catch a sheet closed meanwhile.
  slow?: number
}
export function accessWorld(options: { role?: 'owner' | 'admin' | 'member' | 'viewer' | 'guest'; secondOwner?: boolean; available?: boolean; provisioner?: string; provisionFailures?: number } = {}): AccessWorld {
  const role = (key: string) => `role-${key}`
  const world: AccessWorld = {
    provisioner: options.provisioner,
    provisionFailures: options.provisionFailures,
    me: ME,
    roles: roles(),
    people: [
      { principal_id: ME, name: 'Markus Barta', avatar_url: null, email: 'markus@barta.com', status: 'active', identity: 'inspr_id', workspace_role: role(options.role ?? 'owner'), aliases: [{ principal_id: MBA_CLASSIC, name: 'mba (classic)', source: 'classic' }], classic_role: 'super_admin', last_active_at: ago(0.1) },
      { principal_id: MIRA, name: 'Mira Holm', avatar_url: null, email: 'mira@inspr.at', status: 'active', identity: 'inspr_id', workspace_role: options.secondOwner ? role('owner') : role('admin'), aliases: [], classic_role: 'admin', last_active_at: ago(3) },
      { principal_id: JONAS, name: 'Jonas Weber', avatar_url: null, email: 'jonas@inspr.at', status: 'active', identity: 'inspr_id', workspace_role: role('member'), aliases: [], classic_role: 'member', last_active_at: ago(26) },
      { principal_id: LENA, name: 'Lena Graf', avatar_url: null, email: 'lena@agentur-k.at', status: 'active', identity: null, workspace_role: null, aliases: [], classic_role: 'external', last_active_at: ago(24 * 4) },
      { principal_id: PAUL, name: 'Paul Steiner', avatar_url: null, email: 'paul@inspr.at', status: 'deactivated', identity: 'inspr_id', workspace_role: role('viewer'), aliases: [], classic_role: 'member', last_active_at: ago(24 * 40) },
      { principal_id: CLEO, name: 'Cleo Customer', avatar_url: null, email: 'cleo@hofer.at', status: 'active', identity: null, workspace_role: role('customer'), aliases: [], classic_role: null, last_active_at: ago(24 * 9) },
    ],
    agents: [
      { principal_id: COORDINATOR, name: 'aeon-coordinator', workspace_role: role('member'), last_seen_at: ago(0.5), service: false },
      { principal_id: DEPLOYER, name: 'pharos-deployer', workspace_role: role('viewer'), last_seen_at: ago(30), service: false },
      { principal_id: SYSTEM, name: 'System', workspace_role: null, last_seen_at: ago(0.2), service: true },
    ],
    imported: [
      { principal_id: JW_CLASSIC, name: 'jw (classic)', classic_role: 'member' },
      { principal_id: AGENTUR_CLASSIC, name: 'agentur-k (classic)', classic_role: 'external' },
    ],
    bindings: [
      { principal_id: JONAS, project_id: 'p-pharos', role_id: 'role-lead' },
      { principal_id: LENA, project_id: 'p-pharos', role_id: role('guest') },
      { principal_id: LENA, project_id: 'p-aeon', role_id: role('guest') },
    ],
    invites: [
      { id: 'inv-1', email: 'anna@studio.at', workspace_role: role('member'), project_roles: [{ project_id: 'p-pharos', role_id: 'role-lead' }], status: 'pending', created_by: MIRA, created_at: ago(20), expires_at: ago(-24 * 13) },
      { id: 'inv-2', email: 'ben@agentur-k.at', workspace_role: null, project_roles: [{ project_id: 'p-aeon', role_id: role('guest') }], status: 'expired', created_by: ME, created_at: ago(24 * 20), expires_at: ago(24 * 6) },
      { id: 'inv-3', email: 'old@inspr.at', workspace_role: role('viewer'), project_roles: [], status: 'revoked', created_by: ME, created_at: ago(24 * 30), expires_at: ago(24 * 16) },
      { id: 'inv-4', email: 'jonas@inspr.at', workspace_role: role('member'), project_roles: [], status: 'accepted', created_by: ME, created_at: ago(24 * 60), expires_at: ago(24 * 46) },
    ],
    keys: [
      { id: 'k1', principal_id: COORDINATOR, name: 'aeon-coordinator', prefix: 'c0or', scopes: [], created_at: ago(24 * 3), expires_at: null, last_used_at: ago(0.5), revoked_at: null },
      { id: 'k2', principal_id: DEPLOYER, name: 'pharos-deployer', prefix: 'ph4r', scopes: ['nodes.read'], created_at: ago(24 * 11), expires_at: '2026-12-31T00:00:00Z', last_used_at: null, revoked_at: null },
      { id: 'k3', principal_id: DEPLOYER, name: 'pharos-deployer', prefix: 'ph0l', scopes: [], created_at: ago(24 * 50), expires_at: null, last_used_at: ago(24 * 49), revoked_at: ago(24 * 48) },
    ],
    events: [],
    projects: { 'p-pharos': { key: 'PHAROS', title: 'Pharos' }, 'p-aeon': { key: 'AEON', title: 'Aeon' }, 'p-glint': { key: 'GLINT', title: 'Glint' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' } },
    calls: [],
    available: options.available ?? true,
  }
  // A history to read: oldest first, like the event log.
  const e = (hours: number, actor: string, type: string, before: unknown, after: unknown) => world.events.push({ id: world.events.length + 1, actor_principal_id: actor, type, before, after, at: ago(hours) })
  const r = (id: string) => ref(world.roles.find(x => x.id === id)!)
  e(24 * 60, ME, 'invite.created', null, { id: 'inv-4', email: 'jonas@inspr.at' })
  e(24 * 59, JONAS, 'invite.accepted', null, { id: 'inv-4', email: 'jonas@inspr.at', principal_id: JONAS })
  e(24 * 40, ME, 'principal.deactivated', { principal_id: PAUL, status: 'active' }, { principal_id: PAUL, status: 'deactivated' })
  e(24 * 30, ME, 'role.created', null, { id: 'role-lead', name: 'Delivery lead', permissions: world.roles.find(x => x.id === 'role-lead')!.permissions })
  e(24 * 11, ME, 'agent_key.created', null, { id: 'k2', principal_id: DEPLOYER, name: 'pharos-deployer', prefix: 'ph4r' })
  e(24 * 5, MIRA, 'binding.set', null, { principal_id: LENA, scope_type: 'project', project_id: 'p-aeon', role: r('role-guest') })
  e(26, MIRA, 'binding.set', { principal_id: JONAS, scope_type: 'project', project_id: 'p-pharos', role: r('role-member') }, { principal_id: JONAS, scope_type: 'project', project_id: 'p-pharos', role: r('role-lead') })
  e(20, MIRA, 'invite.created', null, { id: 'inv-1', email: 'anna@studio.at' })
  e(2, ME, 'principal.alias_linked', { principal_id: MBA_CLASSIC, linked_to: null }, { principal_id: MBA_CLASSIC, linked_to: ME })
  return world
}

const has = (world: AccessWorld, principal: string, permission: string) => {
  const person = world.people.find(p => p.principal_id === principal)
  const role = world.roles.find(r => r.id === person?.workspace_role)
  return person?.status === 'active' && !!role?.permissions.includes(permission)
}
const mine = (world: AccessWorld) => new Set(world.roles.find(r => r.id === world.people.find(p => p.principal_id === world.me)?.workspace_role)?.permissions ?? [])

// also: permissions granted besides the fixture role's, for suites that mock the
// whole app (the UI audit passes P1's admin set so non-Access screens stay reachable).
export async function mockAccess(page: Page, world: AccessWorld, options: { also?: string[] } = {}) {
  let nextId = 1000
  const accountByInvite = new Map<string, 'failed' | 'invited' | 'exists'>()
  const event = (type: string, before: unknown, after: unknown) => world.events.push({ id: world.events.length + 1, actor_principal_id: world.me, type, before, after, at: new Date(now + world.events.length * 1000).toISOString() })
  const fail = (route: Route, status: number, code: string, reason: string, field?: string) => route.fulfill({ status, json: { error: reason, code, reason, ...(field ? { field } : {}) } })
  const roleRef = (id: string | null) => { const role = world.roles.find(r => r.id === id); return role ? ref(role) : null }
  const projectRoles = (principal: string) => world.bindings.filter(b => b.principal_id === principal).map(b => ({ project_id: b.project_id, project_key: world.projects[b.project_id]?.key ?? '', project_title: world.projects[b.project_id]?.title ?? '', role: roleRef(b.role_id)! }))
  const activeOwners = () => world.people.filter(p => p.status === 'active' && p.workspace_role === 'role-owner')
  const lastOwner = (id: string) => { const p = world.people.find(x => x.principal_id === id); return p?.workspace_role === 'role-owner' && p.status === 'active' && activeOwners().length === 1 }
  const person = (p: AccessWorld['people'][number]) => ({ ...p, has_avatar: false, workspace_role: roleRef(p.workspace_role), project_roles: projectRoles(p.principal_id), last_owner: lastOwner(p.principal_id) })
  const nameOf = (id: string) => world.people.find(p => p.principal_id === id)?.name ?? world.agents.find(a => a.principal_id === id)?.name ?? world.imported.find(i => i.principal_id === id)?.name ?? world.people.flatMap(p => p.aliases).find(a => a.principal_id === id)?.name ?? ''
  const principalRef = (id: string) => ({ principal_id: id, name: nameOf(id) })
  const agent = (a: AccessWorld['agents'][number]) => ({ ...a, status: a.status ?? 'active', has_avatar: false, project_roles: projectRoles(a.principal_id), workspace_role: roleRef(a.workspace_role), key_count: world.keys.filter(k => k.principal_id === a.principal_id && !k.revoked_at && (!k.expires_at || Date.parse(k.expires_at) > now)).length })
  const invite = (i: AccessWorld['invites'][number]) => ({ ...i, created_by: principalRef(i.created_by), accepted_by: i.status === 'accepted' ? principalRef(JONAS) : null, accepted_at: i.status === 'accepted' ? ago(24 * 59) : null, workspace_role: roleRef(i.workspace_role), project_roles: i.project_roles.map(pr => ({ project_id: pr.project_id, project_key: world.projects[pr.project_id]?.key ?? '', project_title: world.projects[pr.project_id]?.title ?? '', role: roleRef(pr.role_id)! })) })
  const role = (r: MockRole) => ({ ...r, member_count: world.people.filter(p => p.workspace_role === r.id).length + world.agents.filter(a => a.workspace_role === r.id).length + world.bindings.filter(b => b.role_id === r.id).length })

  await page.route(/\/api\/(me\/permissions|authz\/permissions|roles|members|audit|agent-keys|projects\/[^/]+\/members)(\/|\?|$)/, async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method()
    let body: Record<string, unknown> = {}
    try { body = (request.postDataJSON() as Record<string, unknown>) ?? {} } catch { body = {} }
    world.calls.push({ method, path: `${path}${url.search}`, body })
    if (world.sessionEnded) return route.fulfill({ status: 401, json: { error: 'unauthorized' } })
    if (path === '/api/me/permissions' && !world.available) return route.fulfill({ status: 404, json: { error: 'not found' } })
    if (!world.available) return route.fulfill({ status: 404, json: { error: 'not found' } })
    const need = (permission: string) => has(world, world.me, permission)
    if (path === '/api/me/permissions') {
      const me = world.people.find(p => p.principal_id === world.me)!
      const projectId = url.searchParams.get('project_id')
      const binding = projectId ? world.bindings.find(b => b.principal_id === world.me && b.project_id === projectId) : undefined
      return route.fulfill({ json: {
        workspace: { role: roleRef(me.workspace_role), permissions: me.status === 'active' ? [...new Set([...mine(world), ...(options.also ?? [])])] : [] },
        project: projectId ? { id: projectId, role: roleRef(binding?.role_id ?? null), permissions: binding ? world.roles.find(r => r.id === binding.role_id)!.permissions : [] } : null,
      } })
    }
    if (path === '/api/authz/permissions') return route.fulfill({ json: REGISTRY })
    // ---------- Roles ----------
    if (path === '/api/roles' && method === 'GET') return route.fulfill({ json: world.roles.map(role) })
    const roleMatch = /^\/api\/roles(?:\/([^/]+))?$/.exec(path)
    if (roleMatch) {
      if (!need('roles.manage')) return fail(route, 403, 'forbidden', 'You need Manage roles to change roles.')
      const name = typeof body.name === 'string' ? body.name.trim() : undefined
      const permissions = Array.isArray(body.permissions) ? body.permissions as string[] : undefined
      const escalation = (permissions ?? []).filter(key => !mine(world).has(key))
      if (method === 'POST') {
        if (!name) return fail(route, 400, 'invalid', 'A role needs a name.', 'name')
        if (world.roles.some(r => r.name.toLowerCase() === name.toLowerCase())) return fail(route, 409, 'name_taken', `There is already a role called “${name}”.`, 'name')
        if (escalation.length) return fail(route, 403, 'escalation', `You cannot grant permissions you do not hold: ${escalation.join(', ')}.`, 'permissions')
        const created: MockRole = { id: `role-custom-${nextId++}`, key: name.toLowerCase().replace(/[^a-z0-9]+/g, '-'), name, description: String(body.description ?? ''), builtin: false, permissions: permissions ?? [], based_on: (body.based_on as string | undefined) ?? null }
        world.roles.push(created)
        event('role.created', null, { id: created.id, name, permissions: created.permissions })
        return route.fulfill({ status: 201, json: role(created) })
      }
      const target = world.roles.find(r => r.id === roleMatch[1])
      if (!target) return fail(route, 404, 'not_found', 'This role no longer exists.')
      if (target.builtin) return fail(route, 409, 'builtin', 'Built-in roles cannot be changed; duplicate one to customize it.')
      if (method === 'PATCH') {
        if (name !== undefined && !name) return fail(route, 400, 'invalid', 'A role needs a name.', 'name')
        if (name && world.roles.some(r => r.id !== target.id && r.name.toLowerCase() === name.toLowerCase())) return fail(route, 409, 'name_taken', `There is already a role called “${name}”.`, 'name')
        if (escalation.length) return fail(route, 403, 'escalation', `You cannot grant permissions you do not hold: ${escalation.join(', ')}.`, 'permissions')
        const before = { ...target, permissions: [...target.permissions] }
        if (name) target.name = name
        if (typeof body.description === 'string') target.description = body.description
        if (permissions) target.permissions = permissions
        event('role.updated', { id: before.id, name: before.name, permissions: before.permissions }, { id: target.id, name: target.name, permissions: target.permissions })
        return route.fulfill({ json: role(target) })
      }
      if (method === 'DELETE') {
        const inUse = role(target).member_count > 0
        const reassign = url.searchParams.get('reassign_to')
        if (inUse && !reassign) return fail(route, 409, 'in_use', 'This role is in use; choose a role for its people first.', 'reassign_to')
        if (reassign) {
          for (const p of world.people) if (p.workspace_role === target.id) p.workspace_role = reassign
          for (const a of world.agents) if (a.workspace_role === target.id) a.workspace_role = reassign
          for (const b of world.bindings) if (b.role_id === target.id) b.role_id = reassign
        }
        world.roles.splice(world.roles.indexOf(target), 1)
        event('role.deleted', { id: target.id, name: target.name }, reassign ? { reassigned_to: roleRef(reassign) } : null)
        return route.fulfill({ status: 204 })
      }
    }
    if (path === '/api/members/agents' && method === 'POST') {
      if (!need('keys.manage')) return fail(route, 403, 'forbidden', 'You need Manage agent keys.')
      const name = String(body.name ?? '').trim()
      if (!name) return fail(route, 400, 'invalid', 'Enter a name.', 'name')
      if (world.agents.some(a => a.name.toLowerCase() === name.toLowerCase())) return fail(route, 409, 'name_taken', 'This name is already in use; choose another name', 'name')
      const bindings = (body.project_roles ?? []) as { project_id: string; role_id: string }[]
      const roleIds = [body.workspace_role_id, ...bindings.map(b => b.role_id)].filter(Boolean)
      if (!roleIds.length || roleIds.some(id => !world.roles.find(r => r.id === id)?.permissions.every(k => mine(world).has(k)))) return fail(route, 403, 'forbidden', 'Role exceeds your permissions.')
      const created = { principal_id: `new-agent-${nextId++}`, name, description: String(body.description ?? ''), workspace_role: body.workspace_role_id as string ?? null, last_seen_at: null, service: false }
      world.agents.push(created)
      for (const b of bindings) world.bindings.push({ ...b, principal_id: created.principal_id })
      event('principal.agent_created', null, { principal_id: created.principal_id, name })
      return route.fulfill({ status: 201, json: agent(created) })
    }
    // ---------- Members ----------
    if (path === '/api/members' && method === 'GET') {
      if (!need('members.read')) return fail(route, 403, 'forbidden', 'You need See members to see who is here.')
      // Like internal/authz/members.go: imported classic identities appear in people as well as in imported.
      const importedPeople = world.imported.map(i => ({ principal_id: i.principal_id, name: i.name, avatar_url: null, has_avatar: false, email: i.email ?? null, status: 'active', identity: null, workspace_role: null, project_roles: [], aliases: [], classic_role: i.classic_role, last_active_at: null, last_owner: false }))
      return route.fulfill({ json: { people: [...world.people.map(person), ...importedPeople], agents: world.agents.map(agent), invites: world.invites.map(invite), imported: world.imported, owner_count: activeOwners().length, provisioner: world.provisioner ? { name: world.provisioner } : null } })
    }
    const roleOf = /^\/api\/members\/([^/]+)\/workspace-role$/.exec(path)
    if (roleOf && method === 'PUT') {
      if (!need('members.manage')) return fail(route, 403, 'forbidden', 'You need Manage members to change roles.')
      const id = roleOf[1], next = (body.role_id as string | null) ?? null
      const target = world.people.find(p => p.principal_id === id) ?? world.agents.find(a => a.principal_id === id)
      if (!target) return fail(route, 404, 'not_found', 'This person is no longer here.')
      if (next === 'role-guest') return fail(route, 400, 'project_only_role', 'Guest is a project role; grant it on a project instead', 'role_id')
      if (lastOwner(id) && next !== 'role-owner') return fail(route, 409, 'last_owner', 'The last active owner cannot be removed', 'role_id')
      // internal/authz/members.go: changing an owner, or making one, needs ownership.transfer.
      if ((target.workspace_role === 'role-owner' || next === 'role-owner') && !mine(world).has('ownership.transfer')) return fail(route, 403, 'forbidden', 'Permission denied')
      const beyond = world.roles.find(r => r.id === next)?.permissions.filter(k => !mine(world).has(k)) ?? []
      if (beyond.length) return fail(route, 403, 'forbidden', 'You cannot grant a role with permissions you do not hold', 'role_id')
      const before = roleRef(target.workspace_role)
      target.workspace_role = next
      // authz.workspace_role_changed, which the access log reads as binding.set or binding.removed.
      event(next ? 'binding.set' : 'binding.removed', { principal_id: id, role_id: before?.id ?? null }, { principal_id: id, role_id: next })
      return route.fulfill({ json: 'service' in target ? agent(target) : person(target) })
    }
    const lifecycle = /^\/api\/members\/([^/]+)\/(deactivate|reactivate)$/.exec(path)
    if (lifecycle && method === 'POST') {
      if (!need('members.manage')) return fail(route, 403, 'forbidden', 'You need Manage members to deactivate people.')
      const agentTarget = world.agents.find(a => a.principal_id === lifecycle[1])
      if (agentTarget) {
        // internal/authz/lifecycle.go for an agent: internal identities refuse, a connected computer
        // goes with its computer, and deactivating revokes every key.
        if (agentTarget.service) return fail(route, 403, 'forbidden', 'Permission denied')
        const was = agentTarget.status ?? 'active'
        if (lifecycle[2] === 'deactivate') {
          if (was === 'deactivated') return fail(route, 409, 'conflict', 'This agent is already deactivated', 'principal_id')
          if (agentTarget.connected_computer) return fail(route, 409, 'connected_computer', 'This is a connected computer. Disconnect it under Agents first.', 'principal_id')
          agentTarget.status = 'deactivated'
          for (const k of world.keys) if (k.principal_id === agentTarget.principal_id && !k.revoked_at) k.revoked_at = new Date(now).toISOString()
        } else {
          if (was === 'active') return fail(route, 409, 'conflict', 'This agent is already active', 'principal_id')
          agentTarget.status = 'active'
        }
        event(lifecycle[2] === 'deactivate' ? 'principal.deactivated' : 'principal.reactivated', { principal_id: agentTarget.principal_id, status: was }, { principal_id: agentTarget.principal_id, status: agentTarget.status })
        return route.fulfill({ json: agent(agentTarget) })
      }
      const target = world.people.find(p => p.principal_id === lifecycle[1])
      if (!target) return fail(route, 404, 'not_found', 'This person is no longer here.')
      if (lifecycle[2] === 'deactivate') {
        if (lastOwner(target.principal_id)) return fail(route, 409, 'last_owner', 'The last active owner cannot be deactivated. Make another person an owner first.', 'principal_id')
        target.status = 'deactivated'
        event('principal.deactivated', { principal_id: target.principal_id, status: 'active' }, { principal_id: target.principal_id, status: 'deactivated' })
      } else {
        target.status = 'active'
        event('principal.reactivated', { principal_id: target.principal_id, status: 'deactivated' }, { principal_id: target.principal_id, status: 'active' })
      }
      return route.fulfill({ json: person(target) })
    }
    const alias = /^\/api\/members\/([^/]+)\/aliases(?:\/([^/]+))?$/.exec(path)
    if (alias) {
      if (!need('members.manage')) return fail(route, 403, 'forbidden', 'You need Manage members to link identities.')
      const target = world.people.find(p => p.principal_id === alias[1])
      if (!target) return fail(route, 404, 'not_found', 'This person is no longer here.')
      if (method === 'POST') {
        const from = world.imported.find(i => i.principal_id === body.from_principal_id)
        if (!from) return fail(route, 409, 'conflict', 'That person is already linked. Unlink them first.', 'from_principal_id')
        world.imported.splice(world.imported.indexOf(from), 1)
        target.aliases.push({ principal_id: from.principal_id, name: from.name, source: 'classic' })
        event('principal.alias_linked', { principal_id: from.principal_id, linked_to: null }, { principal_id: from.principal_id, linked_to: target.principal_id })
        return route.fulfill({ json: person(target) })
      }
      if (method === 'DELETE') {
        const index = target.aliases.findIndex(a => a.principal_id === alias[2])
        if (index === -1) return fail(route, 404, 'not_found', 'That alias is not linked to this person', 'from_principal_id')
        const [gone] = target.aliases.splice(index, 1)
        world.imported.push({ principal_id: gone.principal_id, name: gone.name, classic_role: null })
        event('principal.alias_unlinked', { principal_id: gone.principal_id, linked_to: target.principal_id }, { principal_id: gone.principal_id, linked_to: null })
        return route.fulfill({ status: 204 })
      }
    }
    if (world.slow && method === 'POST' && (path === '/api/members/invites' || path === '/api/agent-keys')) await new Promise(resolve => setTimeout(resolve, world.slow))
    if (path === '/api/members/invites' && method === 'POST') {
      if (!need('members.manage')) return fail(route, 403, 'forbidden', 'You need Manage members to invite people.')
      if (body.provision_account && !world.provisioner) return fail(route, 400, 'not_configured', 'Account provisioning is not configured', 'provision_account')
      if (body.provision_account && !String(body.display_name ?? '').trim()) return fail(route, 400, 'invalid', "Enter the invitee's name", 'display_name')
      const email = String(body.email ?? '').trim().toLowerCase()
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) return fail(route, 400, 'invalid', 'Enter an email address', 'email')
      if (body.workspace_role_id === 'role-guest') return fail(route, 400, 'project_only_role', 'Guest is a project role; grant it on a project instead', 'workspace_role_id')
      if (world.people.some(p => p.email === email && p.status === 'active' && p.identity)) return fail(route, 409, 'already_member', 'This person is already an active member', 'email')
      if (world.invites.some(i => i.email === email && i.status === 'pending')) return fail(route, 409, 'pending', `There is already a pending invite for ${email}. Revoke it first to send a new link.`, 'email')
      const days = Number(body.expires_in_days ?? 14)
      if (!(days >= 1 && days <= 90)) return fail(route, 400, 'invalid', 'Expiry must be between 1 and 90 days', 'expires_in_days')
      const created = { id: `inv-${nextId++}`, email, workspace_role: (body.workspace_role_id as string | undefined) ?? null, project_roles: (body.project_roles as { project_id: string; role_id: string }[] | undefined) ?? [], status: 'pending' as const, created_by: world.me, created_at: new Date(now).toISOString(), expires_at: new Date(now + days * 86_400_000).toISOString() }
      world.invites.unshift(created)
      event('invite.created', null, { id: created.id, email })
      let account: { status: 'failed' | 'invited'; reason?: string } | undefined
      if (body.provision_account) {
        const failed = (world.provisionFailures ?? 0) > 0
        if (failed) world.provisionFailures = (world.provisionFailures ?? 0) - 1
        account = failed ? { status: 'failed', reason: 'The sign-in account could not be set up. Retry the invite or ask an administrator.' } : { status: 'invited' }
        accountByInvite.set(created.id, account.status)
        event(failed ? 'invite.account_provision_failed' : 'invite.account_provisioned', null, { invite_id: created.id, status: account.status })
      }
      return route.fulfill({ status: 201, json: { invite: invite(created), join_url: `https://aeon.inspr.at/join/tok_${created.id}_s3cr3t`, ...(account ? { account } : {}) } })
    }
    const provisionMatch = /^\/api\/members\/invites\/([^/]+)\/provision$/.exec(path)
    if (provisionMatch && method === 'POST') {
      if (!need('members.manage')) return fail(route, 403, 'forbidden', 'Permission denied')
      if (!world.provisioner) return fail(route, 400, 'not_configured', 'Account provisioning is not configured')
      if (accountByInvite.get(provisionMatch[1]) !== 'failed') return fail(route, 409, 'conflict', 'This invite cannot be provisioned again')
      const failed = (world.provisionFailures ?? 0) > 0
      if (failed) world.provisionFailures = (world.provisionFailures ?? 0) - 1
      const account = failed ? { status: 'failed', reason: 'The sign-in account could not be set up. Retry the invite or ask an administrator.' } : { status: 'invited' }
      accountByInvite.set(provisionMatch[1], account.status as 'failed' | 'invited')
      event(failed ? 'invite.account_provision_failed' : 'invite.account_provisioned', null, { invite_id: provisionMatch[1], status: account.status })
      return route.fulfill({ json: account })
    }
    const inviteMatch = /^\/api\/members\/invites\/([^/]+)$/.exec(path)
    if (inviteMatch && method === 'DELETE') {
      if (!need('members.manage')) return fail(route, 403, 'forbidden', 'You need Manage members to revoke invites.')
      const target = world.invites.find(i => i.id === inviteMatch[1])
      if (!target || target.status !== 'pending') return fail(route, 409, 'not_pending', 'Only a pending invite can be revoked.')
      target.status = 'revoked'
      event('invite.revoked', null, { id: target.id, email: target.email })
      return route.fulfill({ status: 204 })
    }
    // ---------- Project members ----------
    const project = /^\/api\/projects\/([^/]+)\/members(?:\/([^/]+))?$/.exec(path)
    if (project) {
      const projectId = project[1]
      if (method === 'GET') {
        if (!need('members.read')) return fail(route, 403, 'forbidden', 'Permission denied')
        // Like internal/authz/project_members.go: one row per active person or agent
        // who reaches the project (a project binding, or a workspace role that reads
        // work); no deactivated principals, classic aliases or service principals.
        const out: unknown[] = []
        const who = [...world.people.filter(p => p.status === 'active').map(p => ({ ...p, kind: 'person' as const })), ...world.agents.filter(a => !a.service).map(a => ({ ...a, kind: 'agent' as const }))]
        for (const p of who) {
          const ws = world.roles.find(r => r.id === p.workspace_role) ?? null
          const binding = world.bindings.find(b => b.project_id === projectId && b.principal_id === p.principal_id)
          if (binding) out.push({ principal_id: p.principal_id, name: p.name, avatar_url: null, has_avatar: false, kind: p.kind, via: 'project', role: roleRef(binding.role_id), workspace_role: ws ? ref(ws) : null })
          else if (ws?.permissions.includes('nodes.read')) out.push({ principal_id: p.principal_id, name: p.name, avatar_url: null, has_avatar: false, kind: p.kind, via: 'workspace', role: ref(ws), workspace_role: ref(ws) })
        }
        return route.fulfill({ json: out })
      }
      if (!need('members.manage')) return fail(route, 403, 'forbidden', 'Permission denied')
      const principal = project[2]!
      const existing = world.bindings.find(b => b.project_id === projectId && b.principal_id === principal)
      if (method === 'PUT') {
        const roleId = String(body.role_id ?? '')
        const target = world.roles.find(r => r.id === roleId)
        if (!target) return fail(route, 400, 'invalid', 'Choose a role in this workspace', 'role_id')
        if (target.builtin && (target.key === 'owner' || target.key === 'customer')) return fail(route, 400, 'invalid', 'Owner and Customer are workspace roles; choose a project role', 'role_id')
        // Like the server: only the role's project-grantable permissions, against mine on this project.
        const myBinding = world.bindings.find(b => b.principal_id === world.me && b.project_id === projectId)
        const onProject = new Set([...mine(world), ...(world.roles.find(r => r.id === myBinding?.role_id)?.permissions ?? [])])
        if (target.permissions.filter(k => REGISTRY.find(p => p.key === k)?.grantable_at.includes('project')).some(k => !onProject.has(k))) return fail(route, 403, 'forbidden', 'You cannot grant a role with permissions you do not hold', 'role_id')
        const before = existing ? roleRef(existing.role_id) : null
        if (existing) existing.role_id = roleId; else world.bindings.push({ principal_id: principal, project_id: projectId, role_id: roleId })
        event('binding.set', before ? { principal_id: principal, scope_type: 'project', project_id: projectId, role: before } : null, { principal_id: principal, scope_type: 'project', project_id: projectId, role: roleRef(roleId) })
        return route.fulfill({ json: { id: `binding-${principal}-${projectId}`, principal_id: principal, project_id: projectId, scope_type: 'project', role: roleRef(roleId), created_at: new Date(now).toISOString() } })
      }
      if (method === 'DELETE') {
        if (!existing) {
          const p = world.people.find(x => x.principal_id === principal) ?? world.agents.find(x => x.principal_id === principal)
          if (p && world.roles.find(r => r.id === p.workspace_role)?.permissions.includes('nodes.read')) return fail(route, 409, 'via_workspace', 'This access comes from the workspace role; change it in Members', 'principal_id')
          return fail(route, 404, 'not_found', 'No project access to remove', 'principal_id')
        }
        world.bindings.splice(world.bindings.indexOf(existing), 1)
        event('binding.removed', { principal_id: principal, scope_type: 'project', project_id: projectId, role: roleRef(existing.role_id) }, null)
        return route.fulfill({ status: 204 })
      }
    }
    // ---------- Audit ----------
    if (path === '/api/audit') {
      if (!need('audit.read')) return fail(route, 403, 'forbidden', 'You need Read the access log.')
      // Like internal/authz/audit.go: oldest first after ?after=<id>, 50 a page,
      // names resolved, snapshots under data.
      const after = Number(url.searchParams.get('after') ?? 0)
      const items = world.events.filter(e => !after || e.id > after)
      const page = items.slice(0, 50).map(e => {
        const snap = (v: unknown) => (v && typeof v === 'object' ? v as Record<string, unknown> : {})
        const either = Object.keys(snap(e.after)).length ? snap(e.after) : snap(e.before)
        const subjectId = typeof either.principal_id === 'string' ? either.principal_id : ''
        const projectId = typeof either.project_id === 'string' ? either.project_id : ''
        return { id: e.id, type: e.type, at: e.at, actor: principalRef(e.actor_principal_id), subject: subjectId ? principalRef(subjectId) : null, project: projectId ? { id: projectId, key: world.projects[projectId]?.key ?? '' } : null, data: { before: e.before, after: e.after } }
      })
      return route.fulfill({ json: { items: page, next_after: items.length > 50 ? page.at(-1)!.id : null } })
    }
    // ---------- Agent keys ----------
    if (path === '/api/agent-keys' && method === 'GET') return route.fulfill({ json: { keys: world.keys } })
    if (path === '/api/agent-keys' && method === 'POST') {
      if (!need('keys.manage')) return fail(route, 403, 'forbidden', 'You need Manage agent keys.')
      const old = body.rotate_key_id ? world.keys.find(k => k.id === body.rotate_key_id) : undefined
      if (body.rotate_key_id && !old) return fail(route, 404, 'not_found', 'No such key.')
      if (old?.revoked_at) return fail(route, 409, 'conflict', 'Key already revoked or rotated.')
      if (body.expires_at && (!Number.isFinite(Date.parse(String(body.expires_at))) || Date.parse(String(body.expires_at)) <= now)) return fail(route, 400, 'invalid', 'expires_at must be in the future')
      const agentRow = world.agents.find(a => a.principal_id === (old?.principal_id ?? body.principal_id)) ?? world.agents.find(a => a.name === body.name)
      if (!agentRow) return route.fulfill({ status: 404, json: { error: 'agent not found' } })
      if (agentRow.service) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
      if ((agentRow.status ?? 'active') === 'deactivated') return route.fulfill({ status: 409, json: { error: 'agent is deactivated; reactivate it first' } })
      const scopes = old ? Array.isArray(body.rotation_scopes) ? [...body.rotation_scopes as string[]] : [...old.scopes] : Array.isArray(body.scopes) ? (body.scopes as string[]).map(k => k.replace(/:/g, '.')) : []
      if (scopes.length > 256 || scopes.some(k => !REGISTRY.find(p => p.key === k)?.agent_grantable)) return route.fulfill({ status: 400, json: { error: 'invalid scopes' } })
      // Never more than the creator holds, nor (on a shared role) than the agent's role.
      const agentRole = world.roles.find(r => r.id === agentRow.workspace_role)
      const projectScopes = world.bindings.filter(b => b.principal_id === agentRow.principal_id).flatMap(b => world.roles.find(r => r.id === b.role_id)?.permissions ?? []).filter(k => REGISTRY.find(p => p.key === k)?.grantable_at.includes('project'))
      if (projectScopes.length && scopes.some(k => !projectScopes.includes(k))) return fail(route, 403, 'forbidden', 'Beyond project role.')
      if (scopes.some(k => !mine(world).has(k) || (agentRole && !agentRole.permissions.includes(k)))) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
      const prefix = `n${String(nextId++).slice(-3)}`
      const key = { id: `k-${prefix}`, principal_id: agentRow?.principal_id ?? `agent-${prefix}`, name: old?.name ?? String(body.name), prefix, scopes, created_at: new Date(now).toISOString(), expires_at: (body.expires_at as string | undefined) ?? null, last_used_at: null, revoked_at: null }
      if (old) {
        const before = { ...old }
        old.revoked_at = new Date(now).toISOString()
        event('agent_key.revoked', before, { ...old })
      }
      world.keys.unshift(key)
      event('agent_key.created', null, { id: key.id, principal_id: key.principal_id, name: key.name, prefix })
      return route.fulfill({ status: 201, json: { id: key.id, token: `aeon_${prefix}_T0k3nS3cr3tValue`, prefix, name: key.name, expires_at: key.expires_at } })
    }
    const scopeMatch = /^\/api\/agent-keys\/([^/]+)\/scopes$/.exec(path)
    if (scopeMatch) {
      if (!need('keys.manage')) return fail(route, 403, 'forbidden', 'You need Manage agent keys.')
      const key = world.keys.find(k => k.id === scopeMatch[1])
      if (!key) return fail(route, 404, 'not_found', 'No such key.')
      if (key.revoked_at || key.expires_at && Date.parse(key.expires_at) <= now) return fail(route, 409, 'conflict', 'Key is revoked or expired.')
      const agent = world.agents.find(a => a.principal_id === key.principal_id)
      const role = world.roles.find(r => r.id === agent?.workspace_role)
      const eligible = REGISTRY.filter(p => p.agent_grantable && mine(world).has(p.key)).map(p => p.key)
      const grantable = eligible.filter(p => role?.permissions.includes(p))
      const roleGrantable = need('roles.manage') && role && !role.builtin ? eligible.filter(p => !role.permissions.includes(p)) : []
      const before = { ...key, scopes: [...key.scopes] }
      const pruned = key.scopes.filter(k => !REGISTRY.some(p => p.key === k))
      const cleaned = key.scopes.filter(k => !pruned.includes(k))
      if (method === 'GET') {
        if (pruned.length) { key.scopes = cleaned; event('agent_key.scopes_changed', before, { ...key, pruned_scopes: pruned }) }
        return route.fulfill({ json: { key, grantable_scopes: grantable, agent_role: role ? { ...role, member_count: world.agents.filter(a => a.workspace_role === role.id).length + world.people.filter(p => p.workspace_role === role.id).length + world.bindings.filter(b => b.role_id === role.id).length } : null, role_grantable_scopes: roleGrantable } })
      }
      if (method === 'PATCH') {
        if (world.slow) await new Promise(resolve => setTimeout(resolve, world.slow))
        if (body.expires_at !== undefined && body.expires_at !== null && (typeof body.expires_at !== 'string' || !Number.isFinite(Date.parse(body.expires_at)) || Date.parse(body.expires_at) <= now)) return fail(route, 400, 'invalid', 'expires_at must be null or a future timestamp')
        const added = body.add as string[] ?? []
        const removed = body.remove as string[] ?? []
        const extension = body.role_extension as { role_id: string; add: string[] } | undefined
        const roleBefore = role ? { ...role, permissions: [...role.permissions] } : null
        if (extension && (!need('roles.manage') || !role || role.builtin || role.id !== extension.role_id || extension.add.some(k => !added.includes(k) || !eligible.includes(k)))) return fail(route, 403, 'forbidden', 'Role extension denied.')
        const roleAfter = [...new Set([...(role?.permissions ?? []), ...(extension?.add ?? [])])]
        const after = [...new Set([...cleaned.filter(k => !removed.includes(k)), ...added])]
        if (after.some(k => !eligible.includes(k) || !roleAfter.includes(k))) return fail(route, 403, 'forbidden', 'Scopes exceed live grants.')
        key.scopes = after
        if (body.expires_at !== undefined) key.expires_at = body.expires_at as string | null
        if (extension && role) role.permissions = roleAfter
        event('agent_key.scopes_changed', { ...before, ...(extension ? { role: roleBefore } : {}) }, { ...key, ...(extension && role ? { role: { ...role, permissions: [...role.permissions] } } : {}), ...(pruned.length ? { pruned_scopes: pruned } : {}) })
        return route.fulfill({ json: key })
      }
    }

    const keyMatch = /^\/api\/agent-keys\/([^/]+)$/.exec(path)
    if (keyMatch && method === 'DELETE') {
      const key = world.keys.find(k => k.id === keyMatch[1])
      if (!key) return fail(route, 404, 'not_found', 'No such key.')
      key.revoked_at = new Date(now).toISOString()
      event('agent_key.revoked', null, { id: key.id, principal_id: key.principal_id, name: key.name, prefix: key.prefix })
      return route.fulfill({ status: 204 })
    }
    return route.fallback()
  })
}
