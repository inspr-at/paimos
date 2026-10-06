// SPDX-License-Identifier: AGPL-3.0-only
// Release membership (AEON-227), as implemented by RM1. A planning release accepts
// existing tickets through ticket-options and one membership write. A new release
// takes its tickets on the journey action itself; a rejected ticket leaves no release.

import { api, APIError } from './api.ts'
import { type Walker } from './releaseData.ts'

export type TicketAvailability = 'addable' | 'included' | 'closed' | 'released' | 'other_release' | 'active_release' | 'unsupported' | 'unavailable'

export interface MembershipTicket {
  ticket_node_id: string
  key: string
  title: string
  status: string
  type: string
  feature_node_id: string | null
  release_node_id: string | null
  release_title: string | null
  availability: TicketAvailability
}

export interface TicketOptionQuery {
  q?: string
  status?: string
  epic?: string
  type?: string
  limit?: number
}

export interface TicketOptions {
  expected_revision: number
  tickets: MembershipTicket[]
}

export interface MembershipWrite {
  expected_revision: number
  ticket_node_ids: string[]
  confirm_move: boolean
}

export interface MembershipResult {
  leaf_node_ids?: string[]
  walker: Walker
  event_id: number
}

const AVAILABILITY = new Set<TicketAvailability>(['addable', 'included', 'closed', 'released', 'other_release', 'active_release', 'unsupported'])

// other_release is a planning source and may move after confirmation.
// active_release is any other live source; the write would refuse it.
export function canSelectTicket(ticket: Pick<MembershipTicket, 'availability'>): boolean {
  return ticket.availability === 'addable' || ticket.availability === 'other_release'
}

export function availabilityMark(ticket: Pick<MembershipTicket, 'availability' | 'release_title'>): string {
  switch (ticket.availability) {
    case 'included': return 'In this release'
    case 'closed': return 'Closed'
    case 'released': return 'Already released'
    case 'other_release': return ticket.release_title ? `In ${ticket.release_title}` : 'In another planning release'
    case 'active_release': return ticket.release_title ? `In ${ticket.release_title} · not planning` : 'Not in a planning release'
    case 'unsupported': return 'Not a release ticket'
    case 'unavailable': return 'Cannot be added'
    default: return ''
  }
}

export function ticketOptionQuery(query: TicketOptionQuery): string {
  const params = new URLSearchParams()
  const q = query.q?.trim()
  if (q) params.set('q', q)
  if (query.status) params.set('status', query.status)
  if (query.epic) params.set('epic', query.epic)
  if (query.type) params.set('type', query.type)
  if (query.limit) params.set('limit', String(query.limit))
  const text = params.toString()
  return text ? `?${text}` : ''
}

export function isStaleRevision(error: unknown): boolean {
  return error instanceof APIError && error.status === 409 && /revision/i.test(error.message)
}

// RM1 membership: another open release is a 409 whose error text asks for
// confirm_move. The body is {"error"} with no code. Closed and released tickets
// are a different 409 and must not be retried as a move. A new release cannot
// send confirm_move; that 409 rolls the release back.
export function isMoveConflict(error: unknown): boolean {
  if (!(error instanceof APIError) || error.status !== 409 || isStaleRevision(error)) return false
  const code = error.body.code
  if (code === 'other_release' || code === 'confirm_move') return true
  return /another open release|confirm_move/i.test(error.message)
}

function text(value: unknown): string { return typeof value === 'string' ? value : '' }
function nullable(value: unknown): string | null { return typeof value === 'string' && value ? value : null }

export function parseTicketOptions(data: unknown): TicketOptions {
  if (!data || typeof data !== 'object') throw new Error('The ticket list did not answer.')
  const body = data as { expected_revision?: unknown; tickets?: unknown }
  const revision = body.expected_revision
  if (typeof revision !== 'number' || revision < 1) throw new Error('The release revision is missing.')
  const rows = Array.isArray(body.tickets) ? body.tickets : []
  const tickets: MembershipTicket[] = []
  for (const row of rows) {
    if (!row || typeof row !== 'object') continue
    const ticket = row as Record<string, unknown>
    const id = text(ticket.ticket_node_id)
    const key = text(ticket.key)
    if (!id || !key) continue
    const availability = AVAILABILITY.has(ticket.availability as TicketAvailability) ? ticket.availability as TicketAvailability : 'unavailable'
    tickets.push({
      ticket_node_id: id, key, title: text(ticket.title), status: text(ticket.status), type: text(ticket.type),
      feature_node_id: nullable(ticket.feature_node_id), release_node_id: nullable(ticket.release_node_id),
      release_title: nullable(ticket.release_title), availability,
    })
  }
  return { expected_revision: revision, tickets }
}

export function parseMembership(data: unknown): MembershipResult {
  if (!data || typeof data !== 'object') throw new Error('The release did not answer.')
  const body = data as { walker?: Walker; event_id?: unknown; leaf_node_ids?: unknown }
  if (!body.walker || typeof body.walker !== 'object' || !Array.isArray(body.walker.tickets)) throw new Error('The release did not answer.')
  return { walker: body.walker, event_id: typeof body.event_id === 'number' ? body.event_id : 0, ...(Array.isArray(body.leaf_node_ids) ? { leaf_node_ids: body.leaf_node_ids.filter((id): id is string => typeof id === 'string') } : {}) }
}

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    const message = typeof data?.error === 'string' ? data.error : typeof data?.message === 'string' ? data.message : `Request failed (${response.status})`
    throw new APIError(response.status, message, data && typeof data === 'object' ? data : {})
  }
  return response.status === 204 ? undefined as T : response.json()
}

const enc = encodeURIComponent
const releaseRoot = (project: string, release: string) => `/projects/${enc(project)}/releases/${enc(release)}`

export async function listReleaseTicketOptions(project: string, release: string, query: TicketOptionQuery): Promise<TicketOptions> {
  const data = await request<unknown>(`${releaseRoot(project, release)}/ticket-options${ticketOptionQuery(query)}`)
  return parseTicketOptions(data)
}

export async function addReleaseMembership(project: string, release: string, body: MembershipWrite): Promise<MembershipResult> {
  if (body.ticket_node_ids.length < 1 || body.ticket_node_ids.length > 100) throw new Error('Add between 1 and 100 tickets.')
  const data = await request<unknown>(`${releaseRoot(project, release)}/membership`, 'POST', body)
  return parseMembership(data)
}

export interface ReleaseCreate { idempotency_key: string; ticket_node_ids: string[] }
export class ReleaseUnconfirmed extends Error {
  readonly action: ReleaseCreate
  constructor(action: ReleaseCreate) { super('The new release is not confirmed yet. Try the same request again.'); this.name = 'ReleaseUnconfirmed'; this.action = action }
}
export function newReleaseFailure(error: unknown): string {
  if (error instanceof ReleaseUnconfirmed) return error.message
  return `The new release was not opened. ${error instanceof Error ? error.message : 'Please try again.'}`
}
export interface OpenedRelease { result: MembershipResult }
export interface ReleaseOpenClient {
  create(projectId: string, body: ReleaseCreate): Promise<MembershipResult>
  newKey(): string
}
const liveOpenClient: ReleaseOpenClient = {
  create: async (project, body) => parseMembership(await request(`/projects/${enc(project)}/releases`, 'POST', body)),
  newKey: () => crypto.randomUUID(),
}
const pendingOpen = new Map<string, { action: ReleaseCreate; ambiguous: boolean }>()
const opening = new Map<string, { ids: string; promise: Promise<OpenedRelease> }>()
let releaseOpenOwner = 0
export function resetReleaseOpen() { releaseOpenOwner++; pendingOpen.clear(); opening.clear() }
export const resetReleaseOpenForTests = resetReleaseOpen
function ticketSet(ids: string[]): string { return [...ids].map(id => id.toLowerCase()).sort().join('\n') }
export function assertReleaseOpen(projectId: string, ticketIds: string[]): 'replay' | 'clear' {
  const pending = pendingOpen.get(projectId)
  if (!pending) return 'clear'
  if (ticketSet(pending.action.ticket_node_ids) !== ticketSet(ticketIds)) throw new ReleaseUnconfirmed(pending.action)
  return 'replay'
}
export async function openReleaseWithTickets(projectId: string, ticketIds: string[], client: ReleaseOpenClient = liveOpenClient): Promise<OpenedRelease> {
  assertReleaseOpen(projectId, ticketIds)
  const ids = ticketSet(ticketIds), flight = opening.get(projectId)
  if (flight) {
    if (flight.ids === ids) return flight.promise
    throw new Error('Another release request is still running.')
  }
  const owner = releaseOpenOwner
  const pending = pendingOpen.get(projectId) ?? { action: { idempotency_key: client.newKey(), ticket_node_ids: ticketIds }, ambiguous: false }
  pendingOpen.set(projectId, pending)
  const promise = (async () => {
    try {
      const result = parseMembership(await client.create(projectId, pending.action))
      if (owner !== releaseOpenOwner) throw new Error('Account changed during release creation.')
      if (!result.walker.release_node_id) throw new Error('Missing release identity')
      if (pendingOpen.get(projectId) === pending) pendingOpen.delete(projectId)
      return { result }
    } catch (error) {
      if (owner !== releaseOpenOwner) throw new Error('Account changed during release creation.')
      if (!pending.ambiguous && error instanceof APIError && error.status >= 400 && error.status < 500 && error.status !== 408) {
        if (pendingOpen.get(projectId) === pending) pendingOpen.delete(projectId)
        throw error
      }
      pending.ambiguous = true
      throw new ReleaseUnconfirmed(pending.action)
    }
  })().finally(() => { if (opening.get(projectId)?.promise === promise) opening.delete(projectId) })
  opening.set(projectId, { ids, promise })
  return promise
}

export const MEMBERSHIP_BATCH = 100

export interface NativeMembership {
  is_parent?: boolean
  release_count?: number
  leaf_node_ids?: string[]
  assigned_leaf_count?: number
  inheritance_note?: string | null
  ticket_node_id: string
  release_node_id: string | null
  release_title: string | null
  release_state: string | null
}

export type NativeReleaseView =
  | { status: 'pending' }
  | { status: 'unknown' }
  | { status: 'none'; isParent?: boolean; inheritanceNote?: string | null }
  | { status: 'member'; title: string; releaseId: string; releaseState: string | null; isParent?: boolean; releaseCount?: number; leafNodeIds?: string[]; assignedLeafCount?: number }

export function membershipQuery(ids: string[]): string {
  const params = new URLSearchParams()
  for (const id of ids) params.append('ticket_node_id', id)
  const text = params.toString()
  return text ? `?${text}` : ''
}

export function parseNativeMemberships(data: unknown): NativeMembership[] {
  if (!data || typeof data !== 'object' || !Array.isArray((data as { tickets?: unknown }).tickets)) {
    throw new Error('The release membership did not answer.')
  }
  const tickets: NativeMembership[] = []
  for (const row of (data as { tickets: unknown[] }).tickets) {
    if (!row || typeof row !== 'object') continue
    const ticket = row as Record<string, unknown>
    const id = text(ticket.ticket_node_id)
    if (!id) continue
    tickets.push({
      ticket_node_id: id,
      release_node_id: nullable(ticket.release_node_id),
      release_title: nullable(ticket.release_title),
      release_state: nullable(ticket.release_state),
      ...(typeof ticket.is_parent === 'boolean' ? { is_parent: ticket.is_parent } : {}),
      ...(Number.isSafeInteger(ticket.release_count) && (ticket.release_count as number)>=0 ? { release_count: ticket.release_count as number } : {}),
      ...(Array.isArray(ticket.leaf_node_ids) && ticket.leaf_node_ids.length <= 1000 && ticket.leaf_node_ids.every(id => typeof id === 'string' && id.trim().length > 0) && new Set(ticket.leaf_node_ids).size === ticket.leaf_node_ids.length ? { leaf_node_ids: ticket.leaf_node_ids as string[] } : {}),
      ...(Number.isSafeInteger(ticket.assigned_leaf_count) && (ticket.assigned_leaf_count as number)>=0 ? { assigned_leaf_count: ticket.assigned_leaf_count as number } : {}),
      ...(typeof ticket.inheritance_note==='string' ? { inheritance_note: ticket.inheritance_note } : {}),
    })
  }
  return tickets
}

// A returned row with no release is no membership. A requested id with no row
// is unknown: it is not the same as an empty membership, and it is not the
// imported fields.release label.
export function nativeViews(requested: string[], rows: NativeMembership[]): Map<string, NativeReleaseView> {
  const byId = new Map(rows.map(row => [row.ticket_node_id, row]))
  const out = new Map<string, NativeReleaseView>()
  for (const id of requested) {
    const row = byId.get(id)
    if (!row || (!row.release_title && !(row.is_parent && (row.release_count ?? 0)>1))) {
      out.set(id, row ? { status: 'none', ...(row.is_parent ? { isParent: true } : {}), ...(row.inheritance_note ? { inheritanceNote: row.inheritance_note } : {}) } : { status: 'unknown' })
      continue
    }
    out.set(id, { status: 'member', title: row.release_title ?? '', releaseId: row.release_node_id ?? '', releaseState: row.release_state, ...(row.is_parent ? { isParent: true, releaseCount: row.release_count, leafNodeIds: row.leaf_node_ids, assignedLeafCount: row.assigned_leaf_count } : {}) })
  }
  return out
}

export type OpenedMembership =
  | { status: 'added'; releaseId: string; releaseTitle: string; count: number }
  | { status: 'changed' }
  | { status: 'unknown' }

// current_release_id on a replay can be a later release. The tickets' own
// membership rows are the only identity that can be reported. A missing row
// stays unknown. An empty or split membership is a change, not a selected count.
export function reconcileOpenedMembership(ticketIds: string[], views: Map<string, NativeReleaseView>): OpenedMembership {
  if (!ticketIds.length) return { status: 'changed' }
  let releaseId = ''
  let releaseTitle = ''
  const leaves = new Set<string>()
  for (const id of ticketIds) {
    const view = views.get(id)
    if (!view || view.status === 'unknown' || view.status === 'pending') return { status: 'unknown' }
    if (view.status !== 'member' || !view.releaseId || !view.title) return { status: 'changed' }
    if (view.isParent) {
      if ((view.releaseCount ?? 1) > 1) return { status: 'changed' }
      if (!view.leafNodeIds || view.assignedLeafCount === undefined || view.assignedLeafCount > view.leafNodeIds.length) return { status: 'unknown' }
      if (!view.leafNodeIds.length || view.assignedLeafCount < view.leafNodeIds.length) return { status: 'changed' }
    }
    for (const leaf of view.leafNodeIds ?? [id]) leaves.add(leaf)
    if (!releaseId) {
      releaseId = view.releaseId
      releaseTitle = view.title
    } else if (releaseId !== view.releaseId) return { status: 'changed' }
  }
  return { status: 'added', releaseId, releaseTitle, count: leaves.size }
}

export function openedMembershipMessage(outcome: OpenedMembership): string | null {
  if (outcome.status === 'added') return null
  if (outcome.status === 'unknown') return 'The release action is confirmed, but release membership could not be read.'
  return 'The release action is confirmed, but those tickets are not all in one release.'
}

export function releaseViewIsParent(view: NativeReleaseView | undefined): boolean { return !!view && (view.status === 'none' || view.status === 'member') && view.isParent === true }

export function releaseCell(view: NativeReleaseView | undefined): { text: string; label: string; kind: 'unknown' | 'none' | 'member' } {
  if (!view || view.status === 'pending' || view.status === 'unknown') return { text: '—', label: 'Release unknown', kind: 'unknown' }
  if (view.status === 'none') return { text: '—', label: view.inheritanceNote === 'parent_release_closed' ? 'Backlog: the parent release is frozen or released' : 'No release', kind: 'none' }
  if (view.isParent) { const value = (view.releaseCount ?? 1)>1 ? `Ships in ${view.releaseCount} releases` : `Ships in ${view.title}`; return { text: value, label: value, kind: 'member' } }
  return { text: view.title, label: `Release ${view.title}`, kind: 'member' }
}

export async function listNativeMemberships(project: string, ids: string[], fetcher: (path: string) => Promise<unknown> = request): Promise<Map<string, NativeReleaseView>> {
  const unique = [...new Set(ids.map(id => id.trim()).filter(Boolean))]
  const out = new Map<string, NativeReleaseView>()
  for (let index = 0; index < unique.length; index += MEMBERSHIP_BATCH) {
    const batch = unique.slice(index, index + MEMBERSHIP_BATCH)
    try {
      const data = await fetcher(`/projects/${enc(project)}/release-memberships${membershipQuery(batch)}`)
      for (const [id, view] of nativeViews(batch, parseNativeMemberships(data))) out.set(id, view)
    } catch {
      for (const id of batch) out.set(id, { status: 'unknown' })
    }
  }
  return out
}
