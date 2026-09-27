// SPDX-License-Identifier: AGPL-3.0-only
// Putting tickets into a release: an open planning release, or a new one.
// A ticket that already sits in another open release is moved only after a yes.

import { APIError } from './api.ts'
import { confirmAction } from './confirm.ts'
import { getWalker, type Journey } from './journey.ts'
import {
  addReleaseMembership, isMoveConflict, isStaleRevision, openNewRelease,
  type MembershipResult, type MembershipTicket,
} from './releaseMembership.ts'
import { statusMeta } from './work.ts'

export class AssignCancelled extends Error {
  constructor() { super('cancelled'); this.name = 'AssignCancelled' }
}

export class ReleaseOpenedWithoutTickets extends Error {
  readonly journey: Journey
  readonly releaseId: string
  constructor(message: string, journey: Journey, releaseId: string) {
    super(message)
    this.name = 'ReleaseOpenedWithoutTickets'
    this.journey = journey
    this.releaseId = releaseId
  }
}

export interface AssignTicket { id: string; key: string; title: string; state?: string; kind?: string }
export type ReleaseTarget = { kind: 'existing'; id: string; title: string } | { kind: 'new'; title: string }
export interface AssignOutcome {
  releaseId: string
  releaseTitle: string
  result: MembershipResult
  journey: Journey | null
  skipped: { key: string; reason: string }[]
}

function closed(ticket: AssignTicket): boolean {
  return ticket.kind === 'epic' || (!!ticket.state && statusMeta(ticket.state).closed)
}

async function confirmMoves(moves: { key: string; title: string; releaseTitle?: string | null }[], into: string): Promise<boolean> {
  const one = moves.length === 1 ? moves[0] : null
  return confirmAction({
    title: one ? `Move ${one.key}?` : `Move ${moves.length} tickets?`,
    body: one
      ? `${one.key} is in ${one.releaseTitle || 'another open release'}. Moving it puts it in ${into}.`
      : `These tickets are already in another open release. Moving them puts them in ${into}.`,
    points: moves.slice(0, 8).map(ticket => `${ticket.key} · ${ticket.title}${ticket.releaseTitle ? ` · now in ${ticket.releaseTitle}` : ''}`),
    confirmLabel: `Move into ${into}`,
    cancelLabel: 'Leave them',
  })
}

export async function confirmOptionMoves(tickets: MembershipTicket[], into: string): Promise<boolean> {
  const moves = tickets.filter(ticket => ticket.availability === 'other_release')
  if (!moves.length) return true
  return confirmMoves(moves.map(ticket => ({ key: ticket.key, title: ticket.title, releaseTitle: ticket.release_title })), into)
}

export async function assignToRelease(projectId: string, tickets: AssignTicket[], target: ReleaseTarget, knownMoves: { key: string; title: string; releaseTitle: string | null }[] = []): Promise<AssignOutcome> {
  const skipped = tickets.filter(closed).map(ticket => ({ key: ticket.key, reason: ticket.kind === 'epic' ? 'An epic is not a release ticket.' : 'Closed tickets cannot join a release.' }))
  const ids = [...new Set(tickets.filter(ticket => !closed(ticket)).map(ticket => ticket.id))]
  if (!ids.length) throw new Error(skipped[0]?.reason ?? 'Choose a ticket to add.')
  if (ids.length > 100) throw new Error('Add up to 100 tickets at a time.')
  const into = target.title
  if (knownMoves.length && !await confirmMoves(knownMoves, into)) throw new AssignCancelled()

  let journey: Journey | null = null
  let releaseId = target.kind === 'existing' ? target.id : ''
  let revision = 0
  let title = into
  if (target.kind === 'new') {
    const opened = await openNewRelease(projectId)
    journey = opened.journey
    releaseId = opened.releaseId
    revision = opened.revision
    title = opened.title || target.title
  } else {
    const walker = await getWalker(projectId, releaseId)
    if (walker.state !== 'planning') throw new Error(`${title} is not in planning.`)
    revision = walker.revision
  }

  const write = async (confirmMove: boolean) => addReleaseMembership(projectId, releaseId, { expected_revision: revision, ticket_node_ids: ids, confirm_move: confirmMove })
  try {
    let result: MembershipResult
    try {
      result = await write(knownMoves.length > 0)
    } catch (error) {
      if (isStaleRevision(error) && target.kind === 'existing') {
        revision = (await getWalker(projectId, releaseId)).revision
        result = await write(knownMoves.length > 0)
      } else if (isMoveConflict(error)) {
        const moving = knownMoves.length ? knownMoves : tickets.filter(ticket => ids.includes(ticket.id))
        if (!await confirmMoves(moving, title)) throw new AssignCancelled()
        result = await write(true)
      } else throw error
    }
    return { releaseId, releaseTitle: title, result, journey, skipped }
  } catch (error) {
    if (error instanceof AssignCancelled) throw error
    if (journey) throw new ReleaseOpenedWithoutTickets(`${title} is open, but the tickets were not added. Add them from Plan.`, journey, releaseId)
    if (error instanceof APIError && error.status === 404) throw new Error('This server cannot add tickets to a release yet.')
    throw error
  }
}
