// SPDX-License-Identifier: AGPL-3.0-only
// Release membership (AEON-227), as implemented by RM1. A planning release accepts
// existing tickets through ticket-options and one membership write. A new release
// takes its tickets on the journey action itself; a rejected ticket leaves no release.

import { api, APIError } from './api.ts'
import {
  getJourney, listReleases, postAction, releaseName, releaseRefs,
  type ActionWrite, type Journey, type ReleaseRef, type Walker,
} from './journey.ts'

export type TicketAvailability = 'addable' | 'included' | 'closed' | 'released' | 'other_release' | 'unsupported'

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
  walker: Walker
  event_id: number
}

const AVAILABILITY = new Set<TicketAvailability>(['addable', 'included', 'closed', 'released', 'other_release', 'unsupported'])

export function canSelectTicket(ticket: Pick<MembershipTicket, 'availability'>): boolean {
  return ticket.availability === 'addable' || ticket.availability === 'other_release'
}

export function availabilityMark(ticket: Pick<MembershipTicket, 'availability' | 'release_title'>): string {
  switch (ticket.availability) {
    case 'included': return 'In this release'
    case 'closed': return 'Closed'
    case 'released': return 'Already released'
    case 'other_release': return ticket.release_title ? `In ${ticket.release_title}` : 'In another open release'
    case 'unsupported': return 'Not a release ticket'
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
  if (planning) return { ok: false, reason: `${releaseName(planning)} is still in planning. Add tickets there, or finish it before opening another.` }
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
    const availability = AVAILABILITY.has(ticket.availability as TicketAvailability) ? ticket.availability as TicketAvailability : 'closed'
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
  const body = data as { walker?: Walker; event_id?: unknown }
  if (!body.walker || typeof body.walker !== 'object' || !Array.isArray(body.walker.tickets)) throw new Error('The release did not answer.')
  return { walker: body.walker, event_id: typeof body.event_id === 'number' ? body.event_id : 0 }
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
// release is planned, is the release that is current now. The response names the
// new release as current_release_id. There is no confirm_move on this action.
export function newReleaseAction(journey: Pick<Journey, 'current_release_id' | 'revision'>, ticketIds: string[], idempotencyKey: string): ActionWrite {
  if (journey.current_release_id) {
    return {
      action: 'plan_next_release', expected_revision: journey.revision, idempotency_key: idempotencyKey,
      release_id: journey.current_release_id, ticket_node_ids: ticketIds,
    }
  }
  return { action: 'open_first_release', expected_revision: journey.revision, idempotency_key: idempotencyKey, ticket_node_ids: ticketIds }
}

export function newReleaseFailure(error: unknown): string {
  if (isMoveConflict(error)) return 'The new release was not opened. A ticket is already in another open release, and creating a release cannot move it.'
  const message = error instanceof Error ? error.message : 'The tickets were not added.'
  if (message.startsWith('The new release was not opened.')) return message
  return `The new release was not opened. ${message}`
}

export interface OpenedRelease { journey: Journey; releaseId: string }

// The action is the only write. A stale journey revision is retried once, with a
// new idempotency key, because that 409 committed nothing. Any other rejection
// is returned as-is so the caller can say that no release was opened.
export async function openReleaseWithTickets(projectId: string, ticketIds: string[]): Promise<OpenedRelease> {
  const send = (journey: Journey) => postAction(projectId, newReleaseAction(journey, ticketIds, crypto.randomUUID()))
  let journey = await getJourney(projectId)
  let next: Journey
  try {
    next = await send(journey)
  } catch (error) {
    if (!isStaleRevision(error)) throw error
    journey = await getJourney(projectId)
    const open = canOpenRelease(journey, planningRelease(journey, releaseRefs(await listReleases(projectId))))
    if (!open.ok) throw new Error(open.reason)
    next = await send(journey)
  }
  if (!next.current_release_id) throw new Error('The journey did not name the new release.')
  return { journey: next, releaseId: next.current_release_id }
}
