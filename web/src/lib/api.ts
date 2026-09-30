// SPDX-License-Identifier: AGPL-3.0-only
export interface Identity {
  principal: { id: string; name: string; email?: string; kind?: 'person' | 'agent'; roles?: string[] }
  // brand: the workspace's own header brand (AEON-431), absent when unset.
  tenant: { id: string; name: string; brand?: import('./tenantBrand').TenantBrand }
  // The signed-in person's external identity; absent for agent keys.
  identity?: { email?: string; display_name?: string } | null
}

export function accountName(identity: Identity) {
  return identity.identity?.display_name?.trim() || identity.principal.name
}

export function accountEmail(identity: Identity) {
  return identity.identity?.email?.trim() || identity.principal.email || ''
}

import { learnPictures } from './avatar.ts'
import type { TicketEta } from './eta.ts'
import type { TicketEstimate } from './estimates.ts'
import type { TicketPlanning } from './planning.ts'
import { rowStore } from './rowStore.ts'

// codename: the running release's name (AEON-430); older servers leave it out.
export interface Version { version: string; scheme: string; brand?: import('./brand').Brand; codename?: string }

export class StaleRequestError extends Error {}
export class RequestFailure extends Error {
  readonly kind: 'timeout' | 'network'
  constructor(kind: 'timeout' | 'network') {
    super(kind === 'timeout' ? 'The server did not answer within 10 s' : 'No connection')
    this.kind = kind
  }
}

export const retryDelay = (attempt: number, random = Math.random) => 250 * 2 ** attempt * (0.75 + random() * 0.5)
export const retryable = (method: string, error: unknown) => method.toUpperCase() === 'GET' && error instanceof RequestFailure
const pause = (ms: number, signal?: AbortSignal) => new Promise<void>((resolve, reject) => {
  if (signal?.aborted) { reject(signal.reason); return }
  const timer = setTimeout(done, ms)
  function done() { signal?.removeEventListener('abort', cancelled); resolve() }
  function cancelled() { clearTimeout(timer); reject(signal?.reason) }
  signal?.addEventListener('abort', cancelled, { once: true })
})

// Public reads can use this without adding cookies or weakening their referrer policy.
export async function resilientFetch(url: string, init: RequestInit = {}) {
  const method = init.method ?? 'GET'
  const invocationStarted = Date.now()
  for (let attempt = 0; ; attempt++) {
    if (Date.now() - invocationStarted > 30_000) throw new StaleRequestError('Request crossed a long timer gap')
    const started = Date.now()
    const timeout = AbortSignal.timeout(10_000)
    const signal = init.signal ? AbortSignal.any([init.signal, timeout]) : timeout
    try {
      const response = await fetch(url, { ...init, signal })
      if (Date.now() - started > 30_000) throw new StaleRequestError('Request crossed a long timer gap')
      return response
    } catch (error) {
      if (Date.now() - started > 30_000) throw new StaleRequestError('Request crossed a long timer gap')
      if (init.signal?.aborted) throw error
      const failure = new RequestFailure(timeout.aborted ? 'timeout' : 'network')
      if (!retryable(method, failure) || attempt >= 2 || error instanceof StaleRequestError) {
        throw error instanceof StaleRequestError ? error : failure
      }
      await pause(retryDelay(attempt), init.signal ?? undefined)
    }
  }
}

export async function api(path: string, init: RequestInit = {}) {
  // A revoked tab stays readable, but must not send another protected request.
  // Sign-in and public resources remain available to recover in a new tab.
  if (sessionEnded.blocked && path !== '/auth/dev-login' && !path.startsWith('/public/') && path !== '/version') {
    return new Response(null, { status: 401 })
  }
  const response = await resilientFetch(`/api${path}`, {
    credentials: 'same-origin', cache: 'no-store', ...init,
    headers: { Accept: 'application/json', ...init.headers },
  })
  // Every caller, including those that handle Response themselves, revokes on 401.
  if (response.status === 401) { sessionEnded.blocked = true; sessionEnded.handler?.(path) }
  return response
}

// Keep the P0.3 auth wire contract here, separate from view components.
export async function getSession(): Promise<{ identity: Identity | null; devMode: boolean }> {
  const response = await api('/me')
  if (!response.ok && response.status !== 401) throw new Error('Session unavailable')
  // A 401 may have no JSON body; it still means sign-in is required.
  const body = await response.json().catch(() => {
    if (response.status === 401) return {}
    throw new Error('Invalid session response')
  })
  const devMode = body.dev_mode === true
  if (response.status === 401) return { identity: null, devMode }
  if (typeof body.principal?.id !== 'string' || typeof body.principal?.name !== 'string'
    || typeof body.tenant?.id !== 'string' || typeof body.tenant?.name !== 'string') {
    throw new Error('Invalid session response')
  }
  return { identity: body as Identity, devMode }
}

// R1 wire types mirror api/openapi.yaml. All workspace HTTP calls stay here.
export interface Kind {
  id: string; slug: string; label: string; short_prefix: string; icon: string
  allowed_child_kinds: string[] | null; field_schema: Record<string, unknown>
}
export interface WorkNode {
  estimate?: TicketEstimate
  id: string; key: string; kind_id: string; title: string; body: string
  fields: Record<string, unknown>; state: string; parent_id: string | null
  position: string; created_at: string; updated_at: string; deleted_at?: string | null
}
export interface Page<T> { items: T[]; next_cursor: string | null }
export interface SearchHit { node: WorkNode; score: number }
export interface NodeCreate {
  kind_id: string; title: string; body?: string; fields?: Record<string, unknown>
  state?: string; parent_id?: string | null; before_id?: string | null; key_prefix?: string
}
export type NodePatch = Partial<Pick<WorkNode, 'title' | 'body' | 'fields' | 'state'>>

export class APIError extends Error {
  readonly status: number
  // The parsed error body, when the server sent one (a 412 on move carries the current node).
  readonly body: Record<string, unknown>
  constructor(status: number, message: string, body: Record<string, unknown> = {}) { super(message); this.status = status; this.body = body }
}
async function json<T>(path: string, method = 'GET', body?: unknown, headers: Record<string, string> = {}, signal?: AbortSignal, answered?: (response: Response) => void): Promise<T> {
  const response = await api(path, {
    method,
    ...(signal ? { signal } : {}),
    ...(body === undefined ? { headers } : { headers: { 'Content-Type': 'application/json', ...headers }, body: JSON.stringify(body) }),
  })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    // Keep the caller's useful error wording after api() has revoked the session.
    if (response.status === 401) throw new APIError(401, 'your session has ended', data && typeof data === 'object' ? data : {})
    // Modules answer {error} or {code, message}; either reads as the reason.
    const reason = typeof data?.error === 'string' && data.error ? data.error : typeof data?.message === 'string' && data.message ? data.message : `Request failed (${response.status})`
    throw new APIError(response.status, reason, data && typeof data === 'object' ? data : {})
  }
  answered?.(response)
  return response.status === 204 ? undefined as T : response.json()
}
// The router registers what happens when a request finds the session ended.
export const sessionEnded: { blocked: boolean; handler: ((path: string) => void) | null } = { blocked: false, handler: null }
function query(values: object): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined && value !== '') params.set(key, String(value))
  }
  const encoded = params.toString()
  return encoded ? `?${encoded}` : ''
}
const idPath = (id: string) => encodeURIComponent(id)
// Node writes of this tab go to the row store with the exact revision the
// server answered: live views know those events, and only those, as already
// on screen (AEON-326). sent: when the request left, for causal order.
function writing<T>(send: () => Promise<T>, record: (result: T, sent: number) => void): Promise<T> {
  const sent = rowStore.mark()
  return send().then(result => { record(result, sent); return result })
}
const wroteNode = (node: WorkNode, sent: number) => { rowStore.wrote(node, sent) }
export const getKinds = () => json<{ items: Kind[] }>('/kinds').then(result => { rowStore.learnKinds(result.items); return result })
export const getNode = (id: string) => json<WorkNode>(`/nodes/${idPath(id)}`)
export const createNode = (body: NodeCreate) => writing(() => json<WorkNode>('/nodes', 'POST', body), wroteNode)
// ifUnmodifiedSince is the node's updated_at as read; a newer server copy answers 412.
export const updateNode = (id: string, body: NodePatch, options: { ifUnmodifiedSince?: string } = {}) =>
  writing(() => json<WorkNode>(`/nodes/${idPath(id)}`, 'PATCH', body, options.ifUnmodifiedSince ? { 'If-Unmodified-Since': options.ifUnmodifiedSince } : {}), wroteNode)
// ifUnmodifiedSince: the node's updated_at as read; the move answers 412 with the current node when it changed.
export const moveNode = (id: string, parent_id: string | null, before_id?: string | null, options: { ifUnmodifiedSince?: string } = {}) =>
  writing(() => json<WorkNode>(`/nodes/${idPath(id)}/move`, 'POST', { parent_id, before_id }, options.ifUnmodifiedSince ? { 'If-Unmodified-Since': options.ifUnmodifiedSince } : {}), wroteNode)
export const convertNode = (id: string, to_kind: string, options: { ifUnmodifiedSince?: string } = {}) =>
  writing(() => json<WorkNode>(`/nodes/${idPath(id)}/convert`, 'POST', { to_kind }, options.ifUnmodifiedSince ? { 'If-Unmodified-Since': options.ifUnmodifiedSince } : {}), wroteNode)
// A delete answers the revision of its node.deleted event in the revision
// header (null from an older server: then nothing proves the event is this
// tab's). Header names are case-insensitive; this is the wire form.
const REVISION_HEADER = 'aeon-revision'
export function deleteNode(id: string): Promise<{ revision: string | null }> {
  let revision: string | null = null
  return writing(
    () => json<void>(`/nodes/${idPath(id)}`, 'DELETE', undefined, {}, undefined, response => { revision = response.headers.get(REVISION_HEADER) || null }).then(() => ({ revision })),
    (result, sent) => { rowStore.deleted(id, result.revision, sent) },
  )
}
export const searchNodes = (q: string, params: { kind_id?: string; state?: string; cursor?: string; limit?: number } = {}, options: { signal?: AbortSignal } = {}) =>
  json<Page<SearchHit>>(`/search${query({ q, ...params })}`, 'GET', undefined, {}, options.signal)
// B1 list and project-summary wire types (api/openapi.yaml NodeListItem, listProjects).
// has_avatar: the person has a picture; avatars ask for one only then.
export interface ListPerson { id: string; name: string; has_avatar?: boolean }
export interface ListParent { id: string; key: string; title: string; kind_slug: string }
export interface ListProject { id: string; key: string; title: string }
export interface LeadWorker { name: string; key: string }
export interface ListItem extends WorkNode {
  kind_slug: string; kind_label: string; priority: string | null; assignee: ListPerson | null
  parent: ListParent | null; children_count: number; project: ListProject | null
  // The nearest epic above the item (a task's is its ticket's epic); absent on older servers.
  epic?: ListProject | null
  eta?: TicketEta
  // The live worker the Assignee cell leads with. Absent when none is bound.
  lead_worker?: LeadWorker | null
  // Model, tokens and (with harness.read) cost for the planning columns (AEON-329).
  planning?: TicketPlanning
}
export type Facets = Record<string, Record<string, number>>
export interface ListPage extends Page<ListItem> { facets?: Facets }
export interface ListQuery {
  within?: string; kind?: string[]; state?: string[]; priority?: string[]; assignee?: string[]
  tag?: string[]; epic?: string[]; cost_unit?: string[]; release?: string[]
  date_field?: string; date_from?: string; date_to?: string
  q?: string; hide_closed?: boolean; facets?: string[]; sort?: string; cursor?: string; limit?: number; parent_id?: string
  // Only these nodes (at most 200), every other filter still applied (AEON-326).
  ids?: string[]
}
// Work (ticket, task, epic) counts. A kind's state category wins when one is set.
// Otherwise open is every state that is not in progress, done, cancelled or archived
// (including open, blocked and unknown states); in_progress is in progress, active and QA;
// done is done, delivered and accepted; cancelled is separate; total counts every work state.
export interface ProjectSummary {
  id: string; key: string; title: string; state: string
  open: number; in_progress: number; done: number; cancelled?: number; total: number; last_activity: string
  // The people (and agents) most recently active in the project, newest first; absent on older servers.
  people?: ProjectPerson[]
}
export interface ProjectPerson { id: string; name: string; kind: 'person' | 'agent'; has_avatar?: boolean }
function listQuery(params: ListQuery): string {
  const values: Record<string, string | number | boolean | undefined> = {}
  for (const [key, value] of Object.entries(params)) {
    if (Array.isArray(value)) { if (value.length) values[key] = value.join(',') }
    else if (value !== undefined && value !== '' && value !== false) values[key] = value
  }
  return query(values)
}
export const listNodes = (params: ListQuery, options: { signal?: AbortSignal } = {}) => json<ListPage>(`/nodes${listQuery(params)}`, 'GET', undefined, {}, options.signal)
  .then(page => { learnPictures(page.items.map(item => item.assignee)); return page })
// U22 saved views: a project's list state with a name, own or shared (api/openapi.yaml SavedView).
export interface SavedView {
  id: string; owner_principal_id: string; project_id: string | null; name: string
  filters: Record<string, unknown>; sort_keys: string[]; group_by: string; columns: string[]; shared: boolean
  created_at: string; updated_at: string; deleted_at: string | null
}
export interface ViewWrite { name: string; project_id?: string | null; filters: Record<string, string>; sort_keys: string[]; group_by: string; columns: string[]; shared: boolean }
export const listViews = (projectId: string) => json<{ items: SavedView[] }>(`/views${query({ project_id: projectId })}`)
export const createView = (body: ViewWrite) => json<SavedView>('/views', 'POST', body)
export const updateView = (id: string, body: Partial<Omit<ViewWrite, 'project_id'>>) => json<SavedView>(`/views/${idPath(id)}`, 'PATCH', body)
export const deleteView = (id: string) => json<void>(`/views/${idPath(id)}`, 'DELETE')
export const restoreView = (id: string) => json<SavedView>(`/views/${idPath(id)}/restore`, 'POST')
// U22 bulk change: one change for many nodes, undone as one through its event.
export interface BulkChange {
  ids: string[]; state?: string; priority?: string | null; assignee?: string | null
  tags_add?: (string | { name: string; color?: string })[]; tags_remove?: string[]; parent_id?: string
  // Per node, the updated_at the list showed: a node changed since is skipped with code conflict.
  if_unmodified_since?: Record<string, string>
}
export interface BulkResult { event_id: number | null; items: WorkNode[]; unchanged: string[]; skipped: { id: string; key?: string; reason: string; code?: string }[] }
export const bulkChange = (body: BulkChange) => writing(() => json<BulkResult>('/nodes/bulk', 'POST', body), (result, sent) => { for (const node of result.items ?? []) rowStore.wrote(node, sent) })
export const undoEvent = (eventId: number) => json<unknown>(`/events/${eventId}/undo`, 'POST')
export const getProjects = (includeArchived = false) => json<{ items: ProjectSummary[] }>(`/projects${includeArchived ? '?include_archived=true' : ''}`)
  .then(page => { learnPictures(page.items.flatMap(project => project.people ?? [])); return page })
// Shared project groups (AEON-136): everyone reads them; admins write, and every
// write answers the event that POST /events/{id}/undo reverses.
export interface SharedProjectGroup { id: string; name: string; position: number; project_ids: string[]; created_by: string; created_at: string; updated_at: string }
export interface ProjectGroupWrite { group?: SharedProjectGroup; assignments?: { project_id: string; group_id: string | null }[]; event_id: number | null }
export const getProjectGroups = () => json<{ items: SharedProjectGroup[] }>('/project-groups')
export const createProjectGroup = (body: { name: string; project_ids?: string[]; position?: number }) => json<ProjectGroupWrite>('/project-groups', 'POST', body)
export const updateProjectGroup = (id: string, body: { name?: string; position?: number }) => json<ProjectGroupWrite>(`/project-groups/${idPath(id)}`, 'PATCH', body)
export const deleteProjectGroup = (id: string) => json<ProjectGroupWrite>(`/project-groups/${idPath(id)}`, 'DELETE')
export const assignProjectGroup = (groupId: string | null, projectIds: string[]) => json<ProjectGroupWrite>('/project-groups/assign', 'POST', { group_id: groupId, project_ids: projectIds })
export const undoGroupEvent = (eventId: number) => json<unknown>(`/events/${eventId}/undo`, 'POST')
// B2 ticket activity and comments.
export type ChangeField = 'status' | 'priority' | 'assignee' | 'title' | 'parent' | 'tags' | 'kind'
export interface ActivityChange { field: ChangeField; from: string | null; to: string | null }
export interface ActivityItem {
  id: string; at: string; type: 'comment' | 'change' | 'created'
  author: { id: string | null; name: string; has_avatar?: boolean; automatic?: boolean; job?: string; reason?: string }
  body_markdown?: string; changes?: ActivityChange[]
}
const authored = <T extends ActivityItem | { items: ActivityItem[] }>(value: T): T => { learnPictures('items' in value ? value.items.map(item => item.author) : [value.author]); return value }
export const getActivity = (nodeId: string, cursor?: string | null) =>
  json<{ items: ActivityItem[]; next_cursor: string | null }>(`/nodes/${idPath(nodeId)}/activity${query({ limit: 50, cursor: cursor ?? undefined })}`).then(authored)
export const createComment = (nodeId: string, body_markdown: string) => json<ActivityItem>(`/nodes/${idPath(nodeId)}/comments`, 'POST', { body_markdown }).then(authored)
export const updateComment = (nodeId: string, commentId: string, body_markdown: string) =>
  json<ActivityItem>(`/nodes/${idPath(nodeId)}/comments/${idPath(commentId)}`, 'PATCH', { body_markdown }).then(authored)
export const deleteComment = (nodeId: string, commentId: string) => json<void>(`/nodes/${idPath(nodeId)}/comments/${idPath(commentId)}`, 'DELETE')

export type RelationType = 'blocks' | 'relates' | 'implements' | 'cites' | 'duplicates' | 'customer_of' | 'contact_for'
export interface Relation { id: string; source_node_id: string; target_node_id: string; type: RelationType; created_at: string }
export const getRelations = (nodeId: string) => json<{ items: Relation[]; next_cursor: string | null }>(`/relations${query({ node_id: nodeId, limit: 100 })}`)
// A 409 carries the server's reason in words (an existing link, or a loop it
// spells out by key); callers show error.message as it stands.
export const createRelation = (body: { source_node_id: string; target_node_id: string; type: RelationType }) => json<Relation>('/relations', 'POST', body)
export const deleteRelation = (id: string) => json<void>(`/relations/${idPath(id)}`, 'DELETE')
// Key lookups also name the key asked for (a current or earlier key) and the node's project.
export interface NodePreview { id: string; key: string; title: string; state: string; requested_key?: string; project_id?: string }
export const lookupNodes = (ids: string[]) => json<{ items: NodePreview[] }>(`/nodes/lookup${query({ ids })}`)
// At most 100 keys per request; absent keys are left out of the answer.
export const lookupNodeKeys = (keys: string[]) => json<{ items: NodePreview[] }>(`/nodes/lookup${query({ keys: keys.join(',') })}`)
