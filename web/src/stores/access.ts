// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import * as wire from '../lib/access'
import type { Agent, Members, Permission, Person, Role } from '../lib/access'
import { accessChanged } from '../lib/authz'
import { useSession } from './session'

// Settings -> Access: the registry, the roles and the members, read together and
// kept current. Every change goes to the server first; the list is then read
// again, and my own permissions are asked for again, since a change may be mine.
export const useAccess = defineStore('access', () => {
  const registry = ref<Permission[]>([])
  const roles = ref<Role[]>([])
  const members = ref<Members | null>(null)
  const state = ref<'idle' | 'loading' | 'ready' | 'error'>('idle')
  const error = ref('')
  let request: Promise<void> | undefined
  // What is held belongs to one person in one workspace. Signing out, another
  // person signing in or another workspace starts from nothing, and answers to
  // requests made for the previous one are dropped.
  const session = useSession()
  const owner = () => session.identity ? `${session.identity.tenant.id}:${session.identity.principal.id}` : ''
  let heldFor = owner()
  function reset() { registry.value = []; roles.value = []; members.value = null; state.value = 'idle'; error.value = ''; request = undefined; heldFor = owner() }
  // A 401 clears identity but leaves this view mounted so drafts and one-time
  // links survive. A subsequent sign-in starts with fresh access data.
  watch([owner, () => session.requiresSignIn], ([now, needsSignIn]) => {
    if (needsSignIn && !now) return
    if (now !== heldFor || (!needsSignIn && !now)) reset()
  })

  const imported = computed(() => members.value?.imported ?? [])
  // The server lists imported classic identities among people too; they show only in their own group.
  const people = computed(() => { const skip = new Set(imported.value.map(i => i.principal_id)); return (members.value?.people ?? []).filter(p => !skip.has(p.principal_id)) })
  const agents = computed(() => members.value?.agents ?? [])
  const invites = computed(() => members.value?.invites ?? [])
  const roleById = computed(() => new Map(roles.value.map(role => [role.id, role])))
  // Every principal's name, aliases and imported identities included, for the audit.
  const names = computed(() => {
    const out = new Map<string, string>()
    for (const p of people.value) { out.set(p.principal_id, p.name); for (const a of p.aliases) out.set(a.principal_id, a.name) }
    for (const a of agents.value) out.set(a.principal_id, a.name)
    for (const i of imported.value) out.set(i.principal_id, i.name)
    return out
  })

  function load(force = false): Promise<void> {
    if (session.requiresSignIn) return Promise.resolve()
    if (request) return request
    if (state.value === 'ready' && !force) return Promise.resolve()
    if (state.value !== 'ready') state.value = 'loading'
    const asker = heldFor
    let mine: Promise<void> | undefined = undefined
    mine = (async () => {
      try {
        const [nextRegistry, nextRoles, nextMembers] = await Promise.all([wire.getRegistry(), wire.getRoles(), wire.getMembers()])
        if (asker !== heldFor) return
        registry.value = nextRegistry
        roles.value = nextRoles
        members.value = nextMembers
        state.value = 'ready'
        error.value = ''
      } catch (e) {
        if (asker !== heldFor) return
        if (state.value !== 'ready') state.value = 'error'
        error.value = e instanceof Error ? e.message : 'Access could not be loaded.'
      } finally { if (request === mine) request = undefined }
    })()
    request = mine
    return request
  }
  // After a change: the lists again, and my own permissions.
  // My own access may have changed too: every cached answer is asked again.
  async function settle() { await Promise.all([load(true), accessChanged()]) }

  async function setWorkspaceRole(principalId: string, roleId: string | null) { await wire.setWorkspaceRole(principalId, roleId); await settle() }
  async function deactivate(principalId: string) { await wire.deactivate(principalId); await settle() }
  async function reactivate(principalId: string) { await wire.reactivate(principalId); await settle() }
  async function linkAlias(principalId: string, fromId: string) { await wire.linkAlias(principalId, fromId); await settle() }
  async function unlinkAlias(principalId: string, fromId: string) { await wire.unlinkAlias(principalId, fromId); await settle() }
  async function invite(body: Parameters<typeof wire.createInvite>[0]) { const out = await wire.createInvite(body); await settle(); return out }
  async function retryInviteProvision(id: string) { return wire.retryInviteProvision(id) }
  async function revokeInvite(id: string) { await wire.revokeInvite(id); await settle() }
  async function createRole(body: Parameters<typeof wire.createRole>[0]) { const role = await wire.createRole(body); await settle(); return role }
  async function updateRole(id: string, body: Parameters<typeof wire.updateRole>[1]) { const role = await wire.updateRole(id, body); await settle(); return role }
  async function deleteRole(id: string, reassignTo?: string) { await wire.deleteRole(id, reassignTo); await settle() }
  async function setProjectRole(projectId: string, principalId: string, roleId: string) { await wire.setProjectRole(projectId, principalId, roleId); await settle() }
  async function removeProjectMember(projectId: string, principalId: string) { await wire.removeProjectMember(projectId, principalId); await settle() }

  function person(id: string): Person | undefined { return people.value.find(p => p.principal_id === id) }
  function agent(id: string): Agent | undefined { return agents.value.find(a => a.principal_id === id) }

  return {
    registry, roles, members, state, error, people, agents, invites, imported, roleById, names,
    load, settle, person, agent, setWorkspaceRole, deactivate, reactivate, linkAlias, unlinkAlias, invite, retryInviteProvision, revokeInvite,
    createRole, updateRole, deleteRole, setProjectRole, removeProjectMember,
  }
})
