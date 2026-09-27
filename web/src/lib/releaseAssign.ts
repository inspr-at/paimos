// SPDX-License-Identifier: AGPL-3.0-only
// Putting tickets into a release: an open planning release, or a new one.
// A ticket that already sits in another planning release is moved only after a yes.
// A new release is one journey action. A definite rejection rolls it back.
// A lost response stays unconfirmed: the same tickets replay that action and
// do not send a membership write. The action response is the current journey,
// so the tickets' native membership decides which release, if any, to name.

import { APIError } from './api.ts'
import { confirmAction } from './confirm.ts'
import { getWalker, type Journey } from './journey.ts'
import {
  addReleaseMembership, assertReleaseOpen, isMoveConflict, isStaleRevision, listNativeMemberships, newReleaseFailure, openReleaseWithTickets,
  reconcileOpenedMembership, ReleaseUnconfirmed,
  type MembershipResult, type MembershipTicket, type OpenedMembership,
} from './releaseMembership.ts'
import { statusMeta } from './work.ts'

export class AssignCancelled extends Error {
  constructor() { super('cancelled'); this.name = 'AssignCancelled' }
}

export interface AssignTicket { id: string; key: string; title: string; state?: string; kind?: string }
export type ReleaseTarget = { kind: 'existing'; id: string; title: string } | { kind: 'new'; title: string }
export interface AssignOutcome {
  releaseId: string
  releaseTitle: string
  result: MembershipResult | null
  journey: Journey | null
  skipped: { key: string; reason: string }[]
  // Set when the journey action is confirmed. Absent for an existing release.
  opened?: OpenedMembership
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
  const open = assertReleaseOpen(projectId, ids)
  if (target.kind === 'new' || open === 'replay') {
    if (target.kind === 'new' && open !== 'replay' && knownMoves.length) throw new Error(newReleaseFailure(new APIError(409, 'confirm_move required to move a ticket from another release', {})))
    try {
      const opened = await openReleaseWithTickets(projectId, ids)
      const membership = reconcileOpenedMembership(ids, await listNativeMemberships(projectId, ids))
      return {
        releaseId: membership.status === 'added' ? membership.releaseId : '',
        releaseTitle: membership.status === 'added' ? membership.releaseTitle : target.title,
        result: null,
        journey: opened.journey,
        skipped,
        opened: membership,
      }
    } catch (error) {
      if (error instanceof ReleaseUnconfirmed) throw error
      throw new Error(newReleaseFailure(error))
    }
  }

  const into = target.title
  if (knownMoves.length && !await confirmMoves(knownMoves, into)) throw new AssignCancelled()
  const releaseId = target.id
  let revision = 0
  const loaded = await getWalker(projectId, releaseId)
  if (loaded.state !== 'planning') throw new Error(`${into} is not in planning.`)
  revision = loaded.revision
  const write = async (confirmMove: boolean) => addReleaseMembership(projectId, releaseId, { expected_revision: revision, ticket_node_ids: ids, confirm_move: confirmMove })
  try {
    let result: MembershipResult
    try {
      result = await write(knownMoves.length > 0)
    } catch (error) {
      if (isStaleRevision(error)) {
        revision = (await getWalker(projectId, releaseId)).revision
        result = await write(knownMoves.length > 0)
      } else if (isMoveConflict(error)) {
        const moving = knownMoves.length ? knownMoves : tickets.filter(ticket => ids.includes(ticket.id))
        if (!await confirmMoves(moving, into)) throw new AssignCancelled()
        result = await write(true)
      } else throw error
    }
    return { releaseId, releaseTitle: into, result, journey: null, skipped }
  } catch (error) {
    if (error instanceof AssignCancelled) throw error
    if (error instanceof APIError && (error.status === 404 || error.status === 405)) throw new Error('This server cannot add tickets to a release yet.')
    throw error
  }
}
