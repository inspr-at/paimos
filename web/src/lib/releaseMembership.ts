// SPDX-License-Identifier: AGPL-3.0-only
// Release membership (AEON-227), as implemented by RM1. A planning release accepts
// existing tickets through ticket-options and one membership write. A new release
// takes its tickets on the journey action itself; a rejected ticket leaves no release.

import { api, APIError, type WorkNode } from './api.ts'
import {
  getJourney, listReleases, postAction, releaseName, releaseRefs,
  type ActionWrite, type Journey, type ReleaseRef, type Walker,
} from './journey.ts'

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

// The open planning release is the journey's current release while Plan is the stage.
export function planningRelease(journey: Pick<Journey, 'stage' | 'current_release_id'>, releases: ReleaseRef[]): ReleaseRef | null {
  if (journey.stage !== 'plan' || !journey.current_release_id) return null
  return releases.find(release => release.id === journey.current_release_id) ?? null
}

export function nextReleaseTitle(releases: Pick<ReleaseRef, 'number'>[]): string {
  const number = releases.reduce((max, release) => Math.max(max, release.number), 0) + 1
  return `Release ${number}`
}

export function canOpenRelease(journey: Journey, planning: ReleaseRef | null): { ok: boolean; reason: string } {
  if (planning) return { ok: false, reason: `${planning.title.trim() || releaseName(planning)} is still in planning. Add tickets there, or finish it before opening another.` }
  if (!journey.current_release_id) {
    if (journey.next_action.key === 'open_first_release' && journey.next_action.available) return { ok: true, reason: '' }
    return { ok: false, reason: journey.next_action.reason || 'The journey is not ready for its first release.' }
  }
  if (journey.next_action.key === 'plan_next_release' && journey.next_action.available) return { ok: true, reason: '' }
  return { ok: false, reason: journey.next_action.reason || 'The current release is not live yet, so the next one cannot open.' }
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

// One journey action. ticket_node_ids travel with it. release_id, when the next
// release is planned, is the release that is current now. A fresh response's
// current_release_id is the release just opened. An exact replay returns the
// project's current journey, which may have moved on. There is no confirm_move.
export function newReleaseAction(journey: Pick<Journey, 'current_release_id' | 'revision'>, ticketIds: string[], idempotencyKey: string): ActionWrite {
  if (journey.current_release_id) {
    return {
      action: 'plan_next_release', expected_revision: journey.revision, idempotency_key: idempotencyKey,
      release_id: journey.current_release_id, ticket_node_ids: ticketIds,
    }
  }
  return { action: 'open_first_release', expected_revision: journey.revision, idempotency_key: idempotencyKey, ticket_node_ids: ticketIds }
}

export class ReleaseUnconfirmed extends Error {
  readonly action: ActionWrite
  constructor(action: ActionWrite) {
    super('The new release is not confirmed yet. Try the same request again.')
    this.name = 'ReleaseUnconfirmed'
    this.action = action
  }
}

// A 4xx on the first try is a finished rejection: that response was received and
// the transaction rolled back. A lost response, a 5xx, a 408, or a 200 that
// does not name a release is not. Once the outcome is already unknown, a later
// status is not proof by itself. ensureJourney and the person check run before
// lookupReceipt, so a deleted project is a 404, and 401, 403 or 429 can arrive,
// before the receipt is read. Those keep the exact action. A stale revision is
// different: the receipt was absent, so that try committed nothing.
export function isDefiniteReleaseRejection(error: unknown): error is APIError {
  return error instanceof APIError && error.status >= 400 && error.status < 500 && error.status !== 408
}

export function newReleaseFailure(error: unknown): string {
  if (error instanceof ReleaseUnconfirmed) return error.message
  if (error instanceof Error && error.message.startsWith('The new release was not opened.')) return error.message
  if (!isDefiniteReleaseRejection(error)) return 'The new release is not confirmed yet. Try the same request again.'
  if (isMoveConflict(error)) return 'The new release was not opened. A ticket is already in another open release, and creating a release cannot move it.'
  return `The new release was not opened. ${error.message}`
}

// The journey in the response is the project's current journey. On an exact
// replay that can be a later release than the one this action created.
export interface OpenedRelease { journey: Journey }

interface PendingOpen { action: ActionWrite; ambiguous: boolean }

export interface ReleaseOpenClient {
  getJourney(projectId: string): Promise<Journey>
  postAction(projectId: string, body: ActionWrite): Promise<Journey>
  listReleases(projectId: string): Promise<WorkNode[]>
  newKey(): string
}

const liveOpenClient: ReleaseOpenClient = {
  getJourney, postAction, listReleases, newKey: () => crypto.randomUUID(),
}

const pendingOpen = new Map<string, PendingOpen>()
const opening = new Map<string, { ids: string; promise: Promise<OpenedRelease> }>()

export function resetReleaseOpenForTests() {
  pendingOpen.clear()
  opening.clear()
}

function ticketSet(ids: string[]): string {
  return [...ids].map(id => id.toLowerCase()).sort().join('\n')
}

function namedRelease(value: unknown): Journey | null {
  if (!value || typeof value !== 'object') return null
  const journey = value as Journey
  if (typeof journey.revision !== 'number' || typeof journey.current_release_id !== 'string' || !journey.current_release_id) return null
  return journey
}

function rememberUnknown(projectId: string, action: ActionWrite) {
  const pending = pendingOpen.get(projectId)
  if (pending?.action === action) pending.ambiguous = true
  else pendingOpen.set(projectId, { action, ambiguous: true })
}

async function postExact(client: ReleaseOpenClient, projectId: string, action: ActionWrite, alreadyAmbiguous: boolean): Promise<Journey> {
  let body: unknown
  try {
    body = await client.postAction(projectId, action)
  } catch (error) {
    if (isStaleRevision(error)) throw error
    // The first received 4xx finished the attempt. After the outcome is unknown,
    // a 4xx that can be returned before the receipt is read does not prove the
    // first try failed, and it must not mint another create.
    if (!alreadyAmbiguous && isDefiniteReleaseRejection(error)) {
      if (pendingOpen.get(projectId)?.action === action) pendingOpen.delete(projectId)
      throw error
    }
    rememberUnknown(projectId, action)
    throw new ReleaseUnconfirmed(action)
  }
  const journey = namedRelease(body)
  if (!journey) {
    rememberUnknown(projectId, action)
    throw new ReleaseUnconfirmed(action)
  }
  if (pendingOpen.get(projectId)?.action === action) pendingOpen.delete(projectId)
  return journey
}

async function openOnce(projectId: string, ticketIds: string[], client: ReleaseOpenClient): Promise<OpenedRelease> {
  const pending = pendingOpen.get(projectId)
  if (pending && ticketSet(pending.action.ticket_node_ids ?? []) !== ticketSet(ticketIds)) throw new ReleaseUnconfirmed(pending.action)
  let action = pending?.action ?? null
  let ambiguous = pending?.ambiguous ?? false
  if (!action) {
    let journey: Journey
    try {
      journey = await client.getJourney(projectId)
    } catch (error) {
      throw new Error(`The new release was not opened. ${error instanceof Error ? error.message : 'The journey could not be read.'}`)
    }
    action = newReleaseAction(journey, ticketIds, client.newKey())
    pendingOpen.set(projectId, { action, ambiguous: false })
    ambiguous = false
  }
  let next: Journey
  try {
    next = await postExact(client, projectId, action, ambiguous)
  } catch (error) {
    if (!isStaleRevision(error)) throw error
    // That 409 committed nothing, so the next attempt is a new request.
    pendingOpen.delete(projectId)
    let journey: Journey
    try {
      journey = await client.getJourney(projectId)
      const open = canOpenRelease(journey, planningRelease(journey, releaseRefs(await client.listReleases(projectId))))
      if (!open.ok) throw new Error(`The new release was not opened. ${open.reason}`)
    } catch (refreshError) {
      if (refreshError instanceof Error && refreshError.message.startsWith('The new release was not opened.')) throw refreshError
      throw new Error(`The new release was not opened. ${refreshError instanceof Error ? refreshError.message : 'The journey could not be read again.'}`)
    }
    action = newReleaseAction(journey, ticketIds, client.newKey())
    pendingOpen.set(projectId, { action, ambiguous: false })
    next = await postExact(client, projectId, action, false)
  }
  return { journey: next }
}

// Same tickets as an unconfirmed create must replay that action. A different
// set must not start a second create or a membership write beside it.
export function assertReleaseOpen(projectId: string, ticketIds: string[]): 'replay' | 'clear' {
  const pending = pendingOpen.get(projectId)
  if (!pending) return 'clear'
  if (ticketSet(pending.action.ticket_node_ids ?? []) !== ticketSet(ticketIds)) throw new ReleaseUnconfirmed(pending.action)
  return 'replay'
}

// One journey action, never a follow-up membership write. A stale revision is
// the only case that mints a new idempotency key. A lost or unusable response
// keeps the exact action and replays it. A later response that can arrive
// before receipt reconciliation, including a deleted project's 404 and a 401,
// 403 or 429, keeps that same key. A second click joins the attempt in flight
// instead of starting another. The returned journey is not proof of which
// release received the tickets.
export async function openReleaseWithTickets(projectId: string, ticketIds: string[], client: ReleaseOpenClient = liveOpenClient): Promise<OpenedRelease> {
  const ids = ticketSet(ticketIds)
  const current = opening.get(projectId)
  if (current) {
    if (current.ids === ids) return current.promise
    return current.promise.then(
      () => openReleaseWithTickets(projectId, ticketIds, client),
      () => openReleaseWithTickets(projectId, ticketIds, client),
    )
  }
  const promise = openOnce(projectId, ticketIds, client).finally(() => {
    if (opening.get(projectId)?.promise === promise) opening.delete(projectId)
  })
  opening.set(projectId, { ids, promise })
  return promise
}

export const MEMBERSHIP_BATCH = 100

export interface NativeMembership {
  is_parent?: boolean
  release_count?: number
  leaf_node_ids?: string[]
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
  | { status: 'member'; title: string; releaseId: string; releaseState: string | null; isParent?: boolean; releaseCount?: number; leafNodeIds?: string[] }

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
      ...(Array.isArray(ticket.leaf_node_ids) ? { leaf_node_ids: ticket.leaf_node_ids.filter((id): id is string => typeof id==='string') } : {}),
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
    out.set(id, { status: 'member', title: row.release_title ?? '', releaseId: row.release_node_id ?? '', releaseState: row.release_state, ...(row.is_parent ? { isParent: true, releaseCount: row.release_count, leafNodeIds: row.leaf_node_ids } : {}) })
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
    if (view.isParent && !view.leafNodeIds) return { status: 'unknown' }
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
