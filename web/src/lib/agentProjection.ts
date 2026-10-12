// SPDX-License-Identifier: AGPL-3.0-only
// The Agents projection's pure parts (AEON-1210 G2/G4, audit AEON-1218): how open questions,
// queued work and workers bind to a project's lead, and which named server events wake which
// read. The store (stores/agentProjection.ts) holds the reads; the session tree, the lead band
// and panel, and the later Agents packages read the same bindings from it.
import type { Question } from './decisionDeskApi.ts'
import type { QueueWireEntry } from './workQueue.ts'
import { parseJson } from './json.ts'

// ---------- Bindings ----------
const SESSION_ID = /^[0-9a-f-]{36}$/i
/** The sessions a question was asked from: each asker's verified session, else the input's. Never a principal. */
export function questionSessions(question: Pick<Question, 'askers' | 'input'>): string[] {
  const ids = new Set<string>()
  for (const asker of question.askers ?? []) if (asker.session_id && SESSION_ID.test(asker.session_id)) ids.add(asker.session_id)
  if (!ids.size && question.input?.session_id && SESSION_ID.test(question.input.session_id)) ids.add(question.input.session_id)
  return [...ids]
}
/** Open questions by the exact session that asked. One without a recorded session binds to nobody: it stays on the Desk. */
export function asksBySession(questions: readonly Question[]): Map<string, Question[]> {
  const out = new Map<string, Question[]>()
  for (const question of questions) {
    if (question.state !== 'open') continue
    for (const id of questionSessions(question)) { const list = out.get(id); if (list) list.push(question); else out.set(id, [question]) }
  }
  return out
}
/** Queued work per project, in queue order. */
export function queueByProject(items: readonly Pick<QueueWireEntry, 'project_id'>[]): Map<string, number> {
  const out = new Map<string, number>()
  for (const item of items) if (item.project_id) out.set(item.project_id, (out.get(item.project_id) ?? 0) + 1)
  return out
}
/** Every session under a root by its real parent identity (never every worker of the project); cycles end the walk. */
export function subtree(root: string, sessions: Iterable<{ id: string; parent: string | null | undefined }>): Set<string> {
  const children = new Map<string, string[]>()
  for (const s of sessions) if (s.parent && s.parent !== s.id) { const list = children.get(s.parent); if (list) list.push(s.id); else children.set(s.parent, [s.id]) }
  const out = new Set<string>(), stack = [...(children.get(root) ?? [])]
  while (stack.length) {
    const id = stack.pop()!
    if (id === root || out.has(id)) continue
    out.add(id)
    stack.push(...(children.get(id) ?? []))
  }
  return out
}

// ---------- Live refresh: the named events and what each wakes ----------
// Emitters: internal/harness/lead.go, lead_decisions.go, reparent.go; modelregistry/lead_settings.go;
// internal/agentruns/queue*.go; internal/questions/store.go. They are wake hints only: the store
// rereads the authorized projections, never the event's snapshot.
export const LEAD_EVENTS = ['lead.start_requested', 'lead.removed', 'lead.claim_checked', 'lead.adoption_requested', 'lead.adoption_cancelled', 'lead.settings_changed']
export const DECISION_EVENTS = ['node.lead_decision_recorded']
export const QUEUE_EVENTS = ['queue.added', 'queue.add_undone', 'queue.removed', 'queue.moved', 'queue.reset', 'queue.routed', 'queue.picked_up', 'queue.estimate_applied', 'queue.snapshot_captured', 'queue.snapshot_applied', 'queue.snapshot_cancelled']
export const QUESTION_EVENTS = ['question.asked', 'question.merged', 'question.reused', 'question.answered', 'question.corrected', 'question.outcome_applied']
export const TREE_EVENTS = ['harness.reparented', 'harness.reparent_undone']
export const PROJECTION_EVENTS = [...LEAD_EVENTS, ...DECISION_EVENTS, ...QUEUE_EVENTS, ...QUESTION_EVENTS, ...TREE_EVENTS]

export interface Wake { all: boolean; leads: Set<string>; anyLead: boolean; decisions: Set<string>; queue: boolean; questions: boolean; trees: Set<string> }
export const emptyWake = (): Wake => ({ all: false, leads: new Set(), anyLead: false, decisions: new Set(), queue: false, questions: false, trees: new Set() })
/** The node an event names (a project for lead, decision and reparent events), read with a bound. */
export function eventNode(data: unknown): string | null {
  if (typeof data !== 'string' || data.length > 65_536) return null
  try {
    const value = parseJson(data)
    const node = value && typeof value === 'object' ? (value as { node_id?: unknown }).node_id : undefined
    return typeof node === 'string' && node.length <= 64 ? node : null
  } catch { return null }
}
/** Adds one hint to a batch. No event (a reconnect, a missed stream) rereads everything. */
export function addWake(wake: Wake, event?: string, node?: string | null): Wake {
  if (!event) { wake.all = true; return wake }
  if (LEAD_EVENTS.includes(event)) { if (node) wake.leads.add(node); else wake.anyLead = true }
  else if (DECISION_EVENTS.includes(event)) { if (node) { wake.leads.add(node); wake.decisions.add(node) } else wake.anyLead = true }
  else if (QUEUE_EVENTS.includes(event)) wake.queue = true
  else if (QUESTION_EVENTS.includes(event)) wake.questions = true
  else if (TREE_EVENTS.includes(event)) { if (node) wake.trees.add(node); else wake.all = true }
  return wake
}
