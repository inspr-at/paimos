// SPDX-License-Identifier: AGPL-3.0-only
import { ref } from 'vue'
import { api, sessionEnded } from './api.ts'

interface Grant { role: { id: string; key: string; name: string } | null; permissions: string[] }
interface Effective { workspace: Grant; project: (Grant & { id: string }) | null }

const cache = new Map<string, Effective | null>()
const requests = new Map<string, Promise<void>>()
const revision = ref(0)
let epoch = 0
const keyOf = (projectId?: string) => projectId || ''

// A missing or stale answer grants nothing. The revision makes Vue computed
// callers update as soon as the server's effective set arrives.
export function can(permission: string, projectId?: string): boolean {
  revision.value
  if (revoked) return false
  const key = keyOf(projectId)
  const current = cache.get(key)
  if (current === undefined) { void refreshPermissions(projectId); return false }
  if (current === null) return false
  return current.workspace.permissions.includes(permission) || current.project?.permissions.includes(permission) === true
}

export async function refreshPermissions(projectId?: string): Promise<void> {
  // After a 401 nothing asks by itself (no refetch loop); accessChanged may.
  if (revoked) return
  const key = keyOf(projectId)
  if (requests.has(key)) return requests.get(key)
  return ask(projectId)
}

// One request for one scope. Only the newest request per scope may answer, so a
// slow older answer never overwrites a newer one.
const asked = new Map<string, number>()
function ask(projectId?: string): Promise<void> {
  const key = keyOf(projectId)
  const started = epoch
  const turn = (asked.get(key) ?? 0) + 1
  asked.set(key, turn)
  const current = () => started === epoch && asked.get(key) === turn
  const request = (async () => {
    try {
      const response = await api(`/me/permissions${projectId ? `?project_id=${encodeURIComponent(projectId)}` : ''}`)
      if (response.status === 401) { if (current()) sessionGone(); return }
      if (!response.ok) throw new Error('permissions unavailable')
      const body: Effective = await response.json()
      if (!Array.isArray(body.workspace?.permissions) || (projectId && body.project?.id !== projectId)) throw new Error('invalid permissions')
      if (current()) { cache.set(key, body); revoked = false }
    } catch { if (current()) cache.set(key, null) }
    finally { if (current()) requests.delete(key); revision.value++ }
  })()
  requests.set(key, request)
  return request
}

// Caches derived from what the caller may see (ticket keys in release notes)
// follow the permissions: 'reset' when the person, workspace or session changes
// (drop everything now), 'refresh' when the same person's access is asked again.
type AccessListener = (change: 'reset' | 'refresh') => void
const accessListeners = new Set<AccessListener>()
export function onAccessChange(listener: AccessListener): () => void { accessListeners.add(listener); return () => accessListeners.delete(listener) }
function notifyAccess(change: 'reset' | 'refresh') { for (const listener of accessListeners) listener(change) }

export function clearPermissions(): void { epoch++; revoked = false; cache.clear(); requests.clear(); asked.clear(); revision.value++; notifyAccess('reset') }
// The session ended (a 401): nothing is granted any more and nothing is asked
// until the session is refreshed, so the page keeps its drafts without a request
// loop. Every answer in flight is dropped.
let revoked = false
export function revokePermissions(): void { epoch++; revoked = true; requests.clear(); for (const key of cache.keys()) cache.set(key, null); revision.value++; notifyAccess('reset') }
export function permissionsRevoked(): boolean { revision.value; return revoked }
// Every Access, Settings and permission request that meets a 401 ends up here:
// grants go at once, while the current view keeps its draft or one-time secret.
export function sessionGone(): void { revokePermissions(); sessionEnded.handler?.('') }
// While signed in, a role change or window focus rechecks each scope. The old
// answers stay on screen until replacements arrive; after a 401 no recheck runs
// until an explicit sign-in starts a fresh session.
export async function accessChanged(): Promise<void> {
  if (revoked) return
  // Scopes still in flight are asked again too: their older answers must not land.
  const keys = new Set(['', ...cache.keys(), ...requests.keys()])
  notifyAccess('refresh')
  await Promise.all([...keys].map(key => ask(key || undefined)))
}

if (typeof window !== 'undefined') {
  window.addEventListener('focus', () => { void accessChanged() })
}

// ---------- Reading the answer (Settings > Access) ----------
// Views over the same cached answer, never a second source: my workspace role
// and permissions (to hide what I may not give away), and whether the server
// has answered at all (a server without the endpoint grants nothing).
export function permissionsKnown(projectId?: string): boolean {
  revision.value
  if (revoked) return true
  if (!cache.has(keyOf(projectId))) { void refreshPermissions(projectId); return false }
  return true
}
// Waits until the server has answered for this scope (asking once if nothing is
// cached). 'known' means can() now reflects the server; 'unavailable' means the
// answer could not be had, which is not a denial and must not be shown as one.
export async function ensurePermissions(projectId?: string): Promise<'known' | 'unavailable'> {
  const key = keyOf(projectId)
  if (!cache.has(key) || requests.has(key)) await refreshPermissions(projectId)
  return !revoked && cache.get(key) ? 'known' : 'unavailable'
}
export function permissionsAvailable(): boolean { revision.value; return !!cache.get('') }
export function myWorkspaceRole(): Grant['role'] { revision.value; return cache.get('')?.workspace.role ?? null }
export function myPermissions(projectId?: string): Set<string> {
  revision.value
  if (!projectId) return new Set(cache.get('')?.workspace.permissions ?? [])
  // On a project: my workspace permissions plus that project's, as can() sees them.
  const answer = cache.get(keyOf(projectId))
  if (answer === undefined && !revoked) void refreshPermissions(projectId)
  return new Set([...(answer?.workspace.permissions ?? []), ...(answer?.project?.permissions ?? [])])
}

// The owner may explicitly grant conversation viewing without holding it.
// Keep this separate from can()/myPermissions(): it never enables viewing.
export function grantablePermissions(mine: Set<string>): Set<string> {
  const grants = new Set(mine)
  if (!permissionsRevoked() && myWorkspaceRole()?.key === 'owner') grants.add('harness.watch')
  return grants
}
