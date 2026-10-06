// SPDX-License-Identifier: AGPL-3.0-only
// Access: people, invites, roles, project access, agents and the access audit
// (ADR-003, the authz contract). Wire types mirror the contract exactly; the
// helpers below are free of Vue so they can be unit-tested.
import permissionWords from '../../../internal/authz/permission_labels.json' with { type: 'json' }
import projectSelfPermissions from '../../../internal/authz/project_self_permissions.json' with { type: 'json' }
import builtinAgentExclusions from '../../../internal/authz/builtin_agent_exclusions.json' with { type: 'json' }
import { api } from './api.ts'
import { learnPictures } from './avatar.ts'
import { sessionGone } from './authz.ts'
import type { AgentKey } from './settings.ts'

export type Risk = 'low' | 'medium' | 'high'
export type Scope = 'workspace' | 'project'
export interface RoleRef { id: string; key: string; name: string }
// agent_grantable: may be a scope on an agent key (contract v2 #6).
export interface Permission { key: string; group: string; description: string; risk: Risk; grantable_at: Scope[]; agent_grantable: boolean }
export interface Role { id: string; key: string; name: string; description: string; builtin: boolean; permissions: string[]; based_on: string | null; member_count: number }
export interface ProjectRole { project_id: string; project_key: string; project_title: string; role: RoleRef }
export interface Alias { principal_id: string; name: string; source: 'classic' }
export type Status = 'active' | 'deactivated'
export interface Person {
  principal_id: string; name: string; avatar_url: string | null; has_avatar: boolean; email: string | null; status: Status; identity: 'inspr_id' | null
  workspace_role: RoleRef | null; project_roles: ProjectRole[]; aliases: Alias[]; classic_role: string | null; last_active_at: string | null
  // The server says who is the last active owner (contract v2 #5); the UI never counts role keys.
  last_owner: boolean
}
export type AgentPreviewReason = 'not_key_creator' | 'key_revoked' | 'key_expired' | 'agent_inactive'
// Whether this caller may preview the agent's session file. creator_name is sent only to workspace owners and admins.
export interface AgentPreview { allowed: boolean; reason?: AgentPreviewReason; creator_name?: string }
// status is absent on older servers, which only know active agents; connected_computer marks the
// runtime identity of a computer that is still paired (it is retired by disconnecting the computer);
// paired_computer marks any computer's identity, retired ones too: it never takes a key, a computer
// is connected by pairing it afresh.
export interface Agent { description?: string; project_roles?: ProjectRole[]; principal_id: string; name: string; has_avatar: boolean; workspace_role: RoleRef | null; key_count: number; last_seen_at: string | null; service: boolean; status?: Status; connected_computer?: boolean; paired_computer?: boolean; preview?: AgentPreview }
// What deactivating an agent does: the keys go with it, the history stays. A computer's identity takes
// no key, so reactivating it is followed by pairing the computer afresh.
export function agentDeactivatePoints(keys: number, pairedComputer = false): string[] {
  return [
    keys ? (keys === 1 ? 'Its active key is revoked now; anything using it is refused.' : `Its ${keys} active keys are revoked now; anything using them is refused.`) : 'It has no active key to revoke.',
    'Its role, project access and history stay.',
    pairedComputer
      ? 'You can reactivate it later; a computer takes no key, so you connect it afresh by pairing it.'
      : 'You can reactivate it later; revoked keys stay revoked, so it needs a new one.',
  ]
}
export const CONNECTED_COMPUTER_REASON = 'A connected computer. Disconnect it on the Agents page first.'
// Internal service identities always run; everyone else is working or deactivated. A server that
// predates the status only knows working agents.
export function splitAgents(agents: Agent[]): { working: Agent[]; deactivated: Agent[]; service: Agent[] } {
  const service = agents.filter(a => a.service)
  const rest = agents.filter(a => !a.service)
  return { working: rest.filter(a => a.status !== 'deactivated'), deactivated: rest.filter(a => a.status === 'deactivated'), service }
}
export type InviteStatus = 'pending' | 'expired' | 'revoked' | 'accepted'
export interface Invite {
  id: string; email: string; workspace_role: RoleRef | null; project_roles: ProjectRole[]; status: InviteStatus
  created_by: PrincipalRef; created_at: string; expires_at: string; accepted_by: PrincipalRef | null; accepted_at: string | null
}
export interface InviteAccountResult { status: 'invited' | 'exists' | 'failed'; reason?: string }
export interface InviteCreated { invite: Invite; join_url: string; account?: InviteAccountResult }
export interface Imported { principal_id: string; name: string; classic_role: string | null }
export interface PrincipalRef { principal_id: string; name: string }
// people also lists the imported classic identities; the UI shows those only in their own group.
export interface Members { people: Person[]; agents: Agent[]; invites: Invite[]; imported: Imported[]; owner_count: number; provisioner?: { name: string } | null }
// One row per person or agent: role is the project binding's role when there is one (via project), else the workspace role.
export interface ProjectMember { principal_id: string; name: string; avatar_url: string | null; has_avatar: boolean; kind: 'person' | 'agent'; via: 'workspace' | 'project'; role: RoleRef; workspace_role: RoleRef | null }
export interface ProjectBinding { id: string; principal_id: string; project_id: string; scope_type: 'project'; role: RoleRef; created_at: string }
// Names are resolved by the server, including people who have since left (contract v2 #1).
export interface AuditEvent { id: number; type: string; at: string; actor: PrincipalRef; subject: PrincipalRef | null; project: { id: string; key: string } | null; data: { before?: unknown; after?: unknown } }

// ---------- Errors: {error, code, field?, reason} ----------
export class AccessError extends Error {
  readonly status: number
  readonly code: string
  readonly field: string | null
  constructor(status: number, body: { error?: string; code?: string; field?: string; reason?: string; message?: string }) {
    super(body.reason || body.error || body.message || `Request failed (${status})`)
    this.status = status
    this.code = body.code ?? ''
    this.field = body.field ?? null
  }
}
async function call<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (response.status === 401) sessionGone()
  if (!response.ok) throw new AccessError(response.status, await response.json().catch(() => ({})))
  return response.status === 204 ? undefined as T : response.json() as Promise<T>
}
const id = (value: string) => encodeURIComponent(value)

export const getRegistry = () => call<Permission[]>('/authz/permissions')
export const getRoles = () => call<Role[]>('/roles')
export const createRole = (body: { name: string; description: string; permissions: string[]; based_on?: string | null }) => call<Role>('/roles', 'POST', body)
export const updateRole = (roleId: string, body: { name?: string; description?: string; permissions?: string[] }) => call<Role>(`/roles/${id(roleId)}`, 'PATCH', body)
export const deleteRole = (roleId: string, reassignTo?: string) => call<void>(`/roles/${id(roleId)}${reassignTo ? `?reassign_to=${id(reassignTo)}` : ''}`, 'DELETE')
// Avatars ask for a picture only for people a payload says have one (lib/avatar).
const pictureHints = (people: { principal_id: string; has_avatar: boolean; kind?: string }[]) =>
  learnPictures(people.filter(p => p.kind !== 'agent').map(p => ({ id: p.principal_id, has_avatar: p.has_avatar })))
export const getMembers = () => call<Members>('/members').then(members => { pictureHints(members.people); return members })
export const setWorkspaceRole = (principalId: string, roleId: string | null) => call<Person | Agent>(`/members/${id(principalId)}/workspace-role`, 'PUT', { role_id: roleId })
export const getProjectMembers = (projectId: string) => call<ProjectMember[]>(`/projects/${id(projectId)}/members`).then(members => { pictureHints(members); return members })
export const setProjectRole = (projectId: string, principalId: string, roleId: string) => call<ProjectBinding>(`/projects/${id(projectId)}/members/${id(principalId)}`, 'PUT', { role_id: roleId })
export const removeProjectMember = (projectId: string, principalId: string) => call<void>(`/projects/${id(projectId)}/members/${id(principalId)}`, 'DELETE')
export const createInvite = (body: { email: string; workspace_role_id?: string; project_roles?: { project_id: string; role_id: string }[]; expires_in_days?: number; provision_account?: boolean; display_name?: string }) =>
  call<InviteCreated>('/members/invites', 'POST', body)
export const retryInviteProvision = (inviteId: string) => call<InviteAccountResult>(`/members/invites/${id(inviteId)}/provision`, 'POST')
export const revokeInvite = (inviteId: string) => call<void>(`/members/invites/${id(inviteId)}`, 'DELETE')
export const deactivate = (principalId: string) => call<Person>(`/members/${id(principalId)}/deactivate`, 'POST')
export const reactivate = (principalId: string) => call<Person>(`/members/${id(principalId)}/reactivate`, 'POST')
export const linkAlias = (principalId: string, fromPrincipalId: string) => call<Person>(`/members/${id(principalId)}/aliases`, 'POST', { from_principal_id: fromPrincipalId })
export const unlinkAlias = (principalId: string, fromPrincipalId: string) => call<void>(`/members/${id(principalId)}/aliases/${id(fromPrincipalId)}`, 'DELETE')
// Read a bounded newest-first window. The server's legacy ascending pagination
// remains available to other clients; before walks toward older access events.
export const AUDIT_MAX_PAGES = 40
export async function getAudit(): Promise<{ items: AuditEvent[]; complete: boolean }> {
  const items: AuditEvent[] = []
  let before: number | null = null
  for (let page = 0; page < AUDIT_MAX_PAGES; page++) {
    const body: { items: AuditEvent[]; next_before: number | null } = await call(`/audit?category=access&order=desc${before != null ? `&before=${before}` : ''}`)
    if (!Object.hasOwn(body, 'next_before')) throw new Error('Newest access history is unavailable. Try again after the server update.')
    items.push(...body.items)
    before = body.next_before
    if (before == null) return { items, complete: true }
  }
  return { items, complete: false }
}
export const createAgent = (body: { name: string; description?: string; workspace_role_id?: string; project_roles?: { project_id: string; role_id: string }[] }) => call<Agent>('/members/agents', 'POST', body)

// Agent keys (existing endpoints): a new key's secret is shown only once.
export interface AgentKeyCreated { id: string; token: string; prefix: string; name: string; expires_at: string | null }
export const createAgentKey = (agent: PrincipalRef, expiresAt: string | null, scopes: string[]) => call<AgentKeyCreated>('/agent-keys', 'POST', { principal_id: agent.principal_id, name: agent.name, scopes, ...(expiresAt ? { expires_at: expiresAt } : {}) })
export const adoptAgentKey = (keyId: string) => call<AgentKey>(`/agent-keys/${id(keyId)}/adopt`, 'POST')
export const revokeAgentKey = (keyId: string) => call<void>(`/agent-keys/${id(keyId)}`, 'DELETE')
export const getAgentKeyScopes = (keyId: string) => call<{ key: AgentKey; grantable_scopes: string[]; agent_role?: Role | null; role_grantable_scopes?: string[] }>(`/agent-keys/${id(keyId)}/scopes`)
export const changeAgentKeyScopes = (keyId: string, add: string[], remove: string[], roleExtension?: { role_id: string; add: string[] }, expiresAt?: string | null) => call<AgentKey>(`/agent-keys/${id(keyId)}/scopes`, 'PATCH', { add, remove, ...(roleExtension ? { role_extension: roleExtension } : {}), ...(expiresAt === undefined ? {} : { expires_at: expiresAt }) })
export const keyExpiryAfter = (days: number, now = Date.now()) => days === 0 ? null : new Date(Math.floor(now / 1000) * 1000 + days * 86_400_000).toISOString()
// One confirmed request commits the replacement and revocation together.
export const rotateAgentKey = (keyId: string, expiresAt: string | null, scopes?: string[]) => call<AgentKeyCreated>('/agent-keys', 'POST', { rotate_key_id: keyId, expires_at: expiresAt, ...(scopes === undefined ? {} : { rotation_scopes: scopes }) })

export function keyExpiry(key: Pick<AgentKey, 'expires_at' | 'revoked_at'>, now = Date.now()): { label: string; soon: boolean } {
  if (!key.expires_at) return { label: 'Never', soon: false }
  const remaining = Date.parse(key.expires_at) - now
  if (remaining <= 0) return { label: 'Expired', soon: false }
  const days = Math.ceil(remaining / 86_400_000)
  return { label: remaining < 86_400_000 ? 'In less than a day' : `In ${days} ${days === 1 ? 'day' : 'days'}`, soon: !key.revoked_at && remaining < 14 * 86_400_000 }
}

// ---------- Agent key scopes ----------
// What a key may call: registry permissions marked agent_grantable (contract v2
// #6). An empty list grants nothing (SEC4); the server never grants more than the
// creator holds. MAX_KEY_SCOPES mirrors the server's raw input guard (AEON-367).
export const MAX_KEY_SCOPES = 256
export const keyScopes = (registry: Permission[]) => registry.filter(p => p.agent_grantable)
// What the coordinating agent uses end to end (internal/cli compat test).
export const COORDINATOR_SCOPES = ['account.manage', 'inbox.read', 'inbox.send', 'models.read', 'nodes.read', 'nodes.write', 'nodes.configure', 'relations.read', 'relations.write', 'events.read', 'events.undo', 'search.read', 'views.read', 'views.write']
export const TICKET_WORKER_SCOPES = ['nodes.read', 'nodes.write', 'comments.read', 'comments.write', 'events.read', 'search.read']
export type PresetScopes = string[] | 'all'
export const KEY_SCOPE_PRESETS: { id: string; label: string; description: string; scopes: PresetScopes }[] = [
  { id: 'full-access', label: 'Full access', description: 'Every agent-grantable scope allowed by this agent’s role and your permissions.', scopes: 'all' },
  { id: 'ticket-worker', label: 'Ticket worker', description: 'Read and edit tickets, read their history, read and write comments, and search.', scopes: TICKET_WORKER_SCOPES },
  { id: 'coordinator', label: 'Coordinator', description: 'Coordinate work, messages, accounts, views and history.', scopes: COORDINATOR_SCOPES },
]
// Presets are suggestions only. The registry and live creator/role ceiling are
// authoritative, including when a permission stops being agent-grantable.
export const presetScopes = (wanted: PresetScopes, registry: Permission[]) => wanted === 'all' ? keyScopes(registry).map(p => p.key) : wanted
export const grantablePresetScopes = (wanted: PresetScopes, held: Set<string>, registry: Permission[]) =>
  presetScopes(wanted, registry).filter(key => held.has(key) && registry.some(p => p.key === key && p.agent_grantable))
// Bulk selection never expands a role. None removes only the displayed group,
// including unavailable old scopes; All adds only within the live ceiling.
export function selectScopeGroup(selected: Set<string>, keys: string[], all: boolean, held: Set<string>, registry: Permission[]): Set<string> {
  const next = new Set(selected)
  for (const key of all ? grantablePresetScopes(keys, held, registry) : keys) {
    if (all) next.add(key)
    else next.delete(key)
  }
  return next
}
export const PERSON_ONLY_KEY_NOTE = 'Person-only: managing members, roles, keys and settings; reading keys; the access audit log; approval decisions; rule publishing; conversation watching; harness force-stop and recovery; ownership transfer; the customer portal.'
export function matchesScope(permission: Permission, term: string): boolean {
  const needle = term.trim().toLowerCase()
  return !needle || `${permissionLabel(permission.key)} ${permission.key} ${permission.group} ${permission.description}`.toLowerCase().includes(needle)
}
export const scopeLabel = (key: string) => permissionLabel(key)
// How a key is named on screen. Real prefixes start with the workspace id, the
// same for every key, so a long one shows its distinguishing tail.
export const keyHint = (prefix: string) => prefix.length > 12 ? `aeon_…${prefix.slice(-8)}_…` : `aeon_${prefix}_…`
// The most a new key for this agent may carry besides my own permissions: an
// agent on a shared role (built-in or custom) is capped by its live grants; an agent
// with no role, or with the role the server keeps for it alone, may configure
// that role during creation. Codes and edits respect existing grants. Rotation
// also caps a generated private role at its configured workspace permissions.
// null means creation may configure a role; it never applies to rotation.
export const agentRolePermissions = (role: Pick<Role, 'builtin' | 'permissions'>): string[] =>
  role.permissions.filter(key => !role.builtin || !builtinAgentExclusions.permissions.includes(key))
export function agentScopeCeiling(agent: Pick<Agent, 'principal_id' | 'workspace_role' | 'project_roles'>, roles: Role[], registry: Permission[] = [], mode: 'create' | 'existing' | 'rotate' = 'create'): Set<string> | null {
  const role = agent.workspace_role ? roles.find(r => r.id === agent.workspace_role!.id) : undefined
  const grantable = new Set(keyScopes(registry).map(p => p.key))
  const projectKeys = new Set(keyScopes(registry).filter(p => p.grantable_at.includes('project') || projectSelfPermissions.permissions.includes(p.key)).map(p => p.key))
  const projectPermissions = (agent.project_roles ?? []).flatMap(pr => {
    const projectRole = roles.find(r => r.id === pr.role.id)
    return projectRole ? agentRolePermissions(projectRole) : []
  }).filter(k => projectKeys.has(k))
  if (!role && agent.project_roles?.length) return new Set(projectPermissions)
  if (!role) return agent.workspace_role || mode !== 'create' ? new Set() : null
  const privateRole = !role.builtin && role.key === `agent_${agent.principal_id.replace(/-/g, '')}`
  if (privateRole && mode === 'create') return null
  if (privateRole && mode === 'rotate') return new Set(role.permissions.filter(key => grantable.has(key)))
  // Like AgentKeyCeilingTx: project permissions may be key scopes too. Actual
  // requests still check the project's binding; this grants no workspace access.
  return new Set([...agentRolePermissions(role), ...projectPermissions].filter(key => grantable.has(key)))
}

// ---------- Permissions in words ----------
const RESOURCE: Record<string, string> = permissionWords.resources
const ACTION: Record<string, string> = permissionWords.actions

// "members.manage" -> "Manage members"; "nodes.read" -> "See work".
export function permissionLabel(key: string): string {
  if (key in permissionWords.special) return (permissionWords.special as Record<string, string>)[key]!
  const [resource, ...rest] = key.split('.')
  const action = rest.join('.')
  const noun = RESOURCE[resource!] ?? resource!.replace(/_/g, ' ')
  const verb = ACTION[action] ?? action.replace(/[._]/g, ' ')
  return `${verb.charAt(0).toUpperCase()}${verb.slice(1)} ${noun}`
}
export const RISK_LABEL: Record<Risk, string> = { low: 'Low risk', medium: 'Medium risk', high: 'High risk' }
const RISK_ORDER: Record<Risk, number> = { high: 0, medium: 1, low: 2 }

// Groups in the registry's own order, each with its permissions.
export function groupPermissions(registry: Permission[]): { group: string; items: Permission[] }[] {
  const out: { group: string; items: Permission[] }[] = []
  for (const permission of registry) {
    let bucket = out.find(g => g.group === permission.group)
    if (!bucket) { bucket = { group: permission.group, items: [] }; out.push(bucket) }
    bucket.items.push(permission)
  }
  return out
}
export function diff(base: string[], next: string[]): { added: string[]; removed: string[] } {
  const a = new Set(base), b = new Set(next)
  return { added: next.filter(k => !a.has(k)), removed: base.filter(k => !b.has(k)) }
}
// A role's effect in one line: its riskiest permissions first, in words.
export function effectLine(permissions: string[], registry: Permission[], max = 4): string {
  if (!permissions.length) return 'No access of its own.'
  const byKey = new Map(registry.map(p => [p.key, p]))
  const sorted = [...permissions].sort((x, y) => RISK_ORDER[byKey.get(x)?.risk ?? 'low'] - RISK_ORDER[byKey.get(y)?.risk ?? 'low'] || registry.findIndex(p => p.key === x) - registry.findIndex(p => p.key === y))
  const words = sorted.slice(0, max).map(key => { const label = permissionLabel(key); return label.charAt(0).toLowerCase() + label.slice(1) })
  const rest = sorted.length - words.length
  return `Can ${words.join(', ')}${rest > 0 ? `, and ${rest} more` : ''}.`
}
// Permissions in `wanted` that `mine` lacks: a role holding them cannot be granted by me.
export const beyond = (wanted: string[], mine: Set<string>) => wanted.filter(key => !mine.has(key))

// ---------- Defaults ----------
// What a new invite starts with: Member in the workspace; Guest on a project.
export const defaultWorkspaceRole = (roles: Role[]) => roles.find(r => r.builtin && r.key === 'member')?.id ?? ''
export const defaultProjectRole = (roles: Role[]) => roles.find(r => r.builtin && r.key === 'guest')?.id ?? roles[0]?.id ?? ''
// Where a role may be given. The server refuses Guest in the workspace
// (project_only_role) and Owner or Customer on a project, so they are never
// offered there; a custom role goes wherever one of its permissions is grantable.
const PROJECT_ONLY = new Set(['guest'])
const WORKSPACE_ONLY = new Set(['owner', 'customer'])
export const workspaceRolesOf = (roles: Role[]) => roles.filter(role => !(role.builtin && PROJECT_ONLY.has(role.key)))
export const projectRolesOf = (roles: Role[], registry: Permission[]) => roles.filter(role => !(role.builtin && WORKSPACE_ONLY.has(role.key))
  && role.permissions.some(key => registry.find(p => p.key === key)?.grantable_at.includes('project')))

// ---------- People ----------
// The last active owner can never be demoted, deactivated or removed; the server says who that is.
export const isLastOwner = (person: Pick<Person, 'last_owner'>) => person.last_owner
// Changing an owner's role (or making someone owner) needs Transfer ownership
// (internal/authz/members.go); making someone owner is already an escalation.
export const OWNER_TRANSFER = 'ownership.transfer'
export const ownerChangeNeedsTransfer = (current: Role | null | undefined, mine: Set<string>) => !!current?.builtin && current.key === 'owner' && !mine.has(OWNER_TRANSFER)
export const OWNER_TRANSFER_REASON = 'Changing an owner’s role needs Transfer ownership, which you do not hold. An owner can change it.'
// An open dialog whose permission went away keeps its draft and cannot submit.
// What giving a role asks of me: in the workspace every permission in it; on a
// project only those grantable on a project, against what I hold there.
export const neededToGive = (role: Pick<Role, 'permissions'>, scope: Scope, registry: Permission[]) =>
  scope === 'workspace' ? role.permissions : role.permissions.filter(key => registry.find(p => p.key === key)?.grantable_at.includes('project'))
// Agent creation requires the creator to hold the full role even on projects
// (internal/authz/agent_creation.go). Never suggest Admin or Owner automatically.
export function suggestAgentRole(roles: Role[], wanted: string[], scope: Scope, registry: Permission[], mine: Set<string>): Role | undefined {
  if (!wanted.length || wanted.some(key => !registry.some(p => p.key === key && p.agent_grantable && p.grantable_at.includes(scope)))) return undefined
  const candidates = (scope === 'project' ? projectRolesOf(roles, registry) : workspaceRolesOf(roles)).filter(role =>
    !(role.builtin && ['owner', 'admin', 'customer'].includes(role.key)) && !beyond(role.permissions, mine).length
    && wanted.every(key => neededToGive(role, scope, registry).includes(key)))
  const grants = (role: Role) => neededToGive(role, scope, registry)
  const risk = (role: Role) => grants(role).filter(key => registry.find(p => p.key === key)?.risk === 'high').length
  return candidates.sort((a, b) => grants(a).length - grants(b).length || risk(a) - risk(b) || a.name.localeCompare(b.name) || a.id.localeCompare(b.id))[0]
}
export function agentDescription(presetLabel: string, scope: 'workspace' | 'projects', projectKeys: string[]): string {
  return `${presetLabel || 'CLI or script agent'} for ${scope === 'workspace' ? 'the workspace' : projectKeys.length ? projectKeys.join(', ') : 'selected projects'}`.slice(0, 1000)
}
export const lostPermission = (permission: string) => `You no longer have ${permissionLabel(permission)}, so this cannot be saved. What you chose stays here.`
export const LAST_OWNER_REASON = 'The last active owner keeps Owner, so the workspace always has someone who can manage it. Make another person an owner first.'
export function projectSummary(roles: ProjectRole[]): string {
  if (!roles.length) return ''
  if (roles.length <= 2) return roles.map(r => r.project_title).join(', ')
  return `${roles.length} projects`
}
export function identityLine(person: Pick<Person, 'email' | 'identity'>): string {
  return person.email ?? (person.identity === 'inspr_id' ? 'INSPR ID' : '')
}
export function matchesPerson(person: Person, needle: string): boolean {
  const text = `${person.name} ${person.email ?? ''} ${person.aliases.map(a => a.name).join(' ')} ${person.workspace_role?.name ?? ''}`.toLowerCase()
  return !needle || text.includes(needle.trim().toLowerCase())
}

// ---------- Invites ----------
export const INVITE_LABEL: Record<InviteStatus, string> = { pending: 'Pending', expired: 'Expired', revoked: 'Revoked', accepted: 'Accepted' }
export const EXPIRY_DAYS = [7, 14, 30, 60, 90] as const
export const validEmail = (value: string) => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim())
export function inviteRoles(invite: Pick<Invite, 'workspace_role' | 'project_roles'>): string {
  const parts = [invite.workspace_role ? `${invite.workspace_role.name} in the workspace` : '', ...invite.project_roles.map(r => `${r.role.name} on ${r.project_title}`)].filter(Boolean)
  return parts.join(' · ') || 'No access yet'
}
export const creatorName = (invite: Invite) => invite.created_by.name || 'Someone'

// ---------- Audit ----------
export type AuditCategory = 'roles' | 'bindings' | 'invites' | 'lifecycle' | 'keys'
export const AUDIT_CATEGORIES: { id: AuditCategory; label: string }[] = [
  { id: 'bindings', label: 'Access' }, { id: 'roles', label: 'Roles' }, { id: 'invites', label: 'Invites' }, { id: 'lifecycle', label: 'People' }, { id: 'keys', label: 'Keys' },
]
export function categoryOf(type: string): AuditCategory | null {
  if (type.startsWith('role.')) return 'roles'
  if (type.startsWith('binding.')) return 'bindings'
  if (type.startsWith('invite.')) return 'invites'
  if (type.startsWith('principal.')) return 'lifecycle'
  if (type.startsWith('agent_key.')) return 'keys'
  return null
}
export interface Names { principal: (id: string | null | undefined) => string; project: (id: string | null | undefined) => string; role: (id: string | null | undefined) => string }
type Snapshot = Record<string, unknown>
const obj = (value: unknown): Snapshot => value && typeof value === 'object' && !Array.isArray(value) ? value as Snapshot : {}
const str = (value: unknown) => typeof value === 'string' ? value : ''
// One event as a sentence: who did what to whom, in plain words. The server
// names the actor, the subject and the project; data carries the before and
// after snapshots, whose roles come as objects or as role_id.
export function auditSentence(event: AuditEvent, names: Names): { actor: string; text: string; subject: string } {
  const before = obj(event.data?.before), after = obj(event.data?.after), either = Object.keys(after).length ? after : before
  const actor = event.actor?.name || names.principal(event.actor?.principal_id)
  const who = event.subject?.name || names.principal(str(either.principal_id))
  const roleName = (snap: Snapshot) => str(obj(snap.role).name) || (str(snap.role_id) ? names.role(str(snap.role_id)) : '')
  const projectId = event.project?.id || str(either.project_id) || (str(either.scope_type) === 'project' ? str(either.scope_id) : '')
  const place = projectId ? ` on ${names.project(projectId) || event.project?.key || 'a project'}` : ' in the workspace'
  switch (event.type) {
    case 'role.created': return { actor, subject: str(after.name), text: `created the role ${str(after.name)}` }
    case 'role.updated': {
      const d = diff((before.permissions as string[] | undefined) ?? [], (after.permissions as string[] | undefined) ?? [])
      const renamed = str(before.name) && str(before.name) !== str(after.name) ? `renamed the role ${str(before.name)} to ${str(after.name)}` : `changed the role ${str(after.name)}`
      const changes = [d.added.length ? `${d.added.length} added` : '', d.removed.length ? `${d.removed.length} removed` : ''].filter(Boolean).join(', ')
      return { actor, subject: str(after.name), text: changes ? `${renamed} (${changes})` : renamed }
    }
    case 'role.deleted': return { actor, subject: str(before.name), text: `deleted the role ${str(before.name) || 'a custom role'}` }
    case 'binding.set': {
      const from = roleName(before), to = roleName(after)
      if (!to) return { actor, subject: who, text: `changed ${who}’s access${place}` }
      return { actor, subject: who, text: from && from !== to ? `changed ${who}’s role${place} from ${from} to ${to}` : `gave ${who} the role ${to}${place}` }
    }
    case 'binding.removed': return { actor, subject: who, text: `removed ${who}’s ${roleName(before) ? `${roleName(before)} role` : 'access'}${place}` }
    case 'invite.created': return { actor, subject: str(after.email), text: `invited ${str(after.email) || 'someone'}` }
    case 'invite.revoked': return { actor, subject: str(either.email), text: `revoked the invite for ${str(either.email) || 'someone'}` }
    case 'invite.accepted': return { actor, subject: str(either.email) || who, text: `accepted the invite${str(either.email) ? ` for ${str(either.email)}` : ''}` }
    case 'principal.agent_created': return { actor, subject: who, text: `created the agent ${who || str(after.name)}` }
    case 'principal.deactivated': return { actor, subject: who, text: `deactivated ${who}; their sessions and keys were revoked` }
    case 'principal.reactivated': return { actor, subject: who, text: `reactivated ${who}` }
    case 'principal.alias_linked':
    case 'principal.alias_unlinked': {
      // The server's snapshot is the alias principal itself ({id, name, linked_to}).
      const alias = str(either.from_principal_id) || str(either.principal_id) || str(either.id)
      const person = str(after.linked_to) || str(before.linked_to) || str(either.to_principal_id)
      const aliasName = alias && alias !== person ? (str(either.name) || names.principal(alias)) : ''
      const personName = person ? names.principal(person) : who
      const linked = event.type === 'principal.alias_linked'
      return { actor, subject: personName, text: aliasName ? `${linked ? 'linked' : 'unlinked'} ${aliasName} ${linked ? 'to' : 'from'} ${personName}` : `${linked ? 'linked a classic identity to' : 'unlinked a classic identity from'} ${personName}` }
    }
    case 'agent_key.created': return { actor, subject: who, text: `created the key ${str(either.name) || 'for an agent'}${either.prefix ? ` (${keyHint(str(either.prefix))})` : ''}` }
    case 'agent_key.owner_workstation_changed': return { actor, subject: who, text: `${after.owner_workstation ? 'marked' : 'unmarked'} the owner workstation key ${str(either.name)}` }
    case 'agent_key.governance_used': return { actor, subject: who, text: `used key ${str(after.key_id)} on computer ${str(after.computer_id)} for ${str(after.action)}; ${after.step_up ? 'confirmed locally' : 'without local confirmation'} (${str(after.outcome)})` }
    case 'agent_key.scopes_changed': {
      const role = obj(after.role), priorRole = obj(before.role)
      const roleDelta = diff((priorRole.permissions as string[] | undefined) ?? [], (role.permissions as string[] | undefined) ?? [])
      const pruned = Array.isArray(after.pruned_scopes) ? after.pruned_scopes.filter((s): s is string => typeof s === 'string') : []
      const changes = [roleDelta.added.length ? `added ${roleDelta.added.join(', ')} to the role ${str(role.name)}` : '', pruned.length ? `pruned unknown scopes ${pruned.join(', ')}` : ''].filter(Boolean)
      const expiryChanged = before.expires_at !== after.expires_at
      const scopesChanged = (before.scopes as string[] | undefined)?.join(',') !== (after.scopes as string[] | undefined)?.join(',')
      const what = expiryChanged ? (scopesChanged ? 'scopes and expiry' : 'expiry') : 'scopes'
      if (expiryChanged) changes.push(after.expires_at === null ? 'expiry set to Never' : `expires ${str(after.expires_at)}`)
      return { actor, subject: who, text: [`changed ${what} for the key ${str(either.name) || ''}${either.prefix ? ` (${keyHint(str(either.prefix))})` : ''}`.trim(), ...changes].join('; ') }
    }
    case 'agent_key.revoked': return { actor, subject: who, text: `revoked the key ${str(either.name) || ''}${either.prefix ? ` (${keyHint(str(either.prefix))})` : ''}`.replace(/ +/g, ' ').trim() }
    default: return { actor, subject: '', text: event.type.replace(/[._]/g, ' ') }
  }
}
