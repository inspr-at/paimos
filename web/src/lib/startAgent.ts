// SPDX-License-Identifier: AGPL-3.0-only
// AC3 uses existing modules: workorders.New and agentruns.New. No additional
// httpapi.Module, plugin manifest, router entry or server wiring is needed.
// GET /api/nodes?parent_id=…&kind=work_order discovers the ticket's orders;
// POST /api/work-orders creates one, PATCH /api/work-orders/{id} readies it,
// POST /api/work-orders/{id}/runs queues it. Only agentd reserves and claims.
// The additive requested_account_id field (migration 0851) pins an optional
// choice without pretending an account was already reserved.
// Host → harness → account → model → thinking maps onto this call.
// model_profile_id is the enabled profile for that harness, model and effort.
// The account's daemon is the host. Unknown JSON fields are rejected, so the
// five choices are not sent as extra properties.
import { api, APIError, getNode, listNodes, type WorkNode } from './api.ts'
import { capacityWaitText } from './capacityWait.ts'
import type { AgentRun } from './agents.ts'
import { createRun, listRuns, runNowOnce, type AgentRunRow } from './agentRows.ts'
import type { Wire } from './wire.ts'

export interface WorkOrder {
  kind?: 'build' | 'review'
  node_id: string; status: 'draft' | 'ready' | 'running' | 'blocked' | 'done' | 'cancelled'
  revision: number; assignee_principal_id: string | null
  criteria: { id: string; description: string; checked_at: string | null }[]
}
// Every run this reads or launches is judged by the page's run ledger before anything
// here looks at it or hands it on (AEON-449): the decision to reuse a run, and the
// result the dialog shows, are the rows that stand, never a copy a later read can outrank.
export interface RunAdmission { run: (wire: Wire<AgentRunRow>) => AgentRun; runs: (wires: Wire<AgentRunRow>[]) => AgentRun[] }
export interface StartSelection { ticket: WorkNode; agentId: string; profileId: string; accountId?: string; runNow?: boolean }
export const activeRun = (run: AgentRun) => ['queued', 'starting', 'running', 'waiting'].includes(run.status)

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : {
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, data.error || data.message || `Request failed (${response.status})`)
  }
  return response.json()
}

// Re-read on every attempt: a lost POST response must not blindly create another
// order/run. The UI serializes submissions; the existing APIs do not promise
// cross-client idempotency, and conflicts are surfaced instead of overwritten.
export async function startAgent(selection: StartSelection, admission: RunAdmission): Promise<{ run: AgentRun; reused: boolean }> {
  const { agentId, profileId, accountId } = selection
  const ticket = await getNode(selection.ticket.id)
  if (ticket.is_leaf !== true) throw new Error('Select a work leaf before starting an agent.')
  const orders: WorkOrder[] = []
  let cursor: string | undefined
  do {
    const page = await listNodes({ parent_id: ticket.id, kind: ['work_order'], limit: 200, cursor })
    // Keep reads bounded even on tickets with a long work-order history.
    for (let offset = 0; offset < page.items.length; offset += 6) {
      orders.push(...await Promise.all(page.items.slice(offset, offset + 6).map(n => request<WorkOrder>(`/work-orders/${encodeURIComponent(n.id)}`))))
    }
    cursor = page.next_cursor ?? undefined
  } while (cursor)
  const candidates = orders.filter(o => o.kind !== 'review' && !['done', 'cancelled'].includes(o.status) && (!o.assignee_principal_id || o.assignee_principal_id === agentId))
  if (candidates.length > 1) throw new Error('This ticket has several active work orders for this agent. Resolve them before starting another run.')
  let order = candidates[0]
  if (order) {
    if (order.status === 'blocked') throw new Error('This work order is blocked. Resolve its blocker before starting the agent.')
    let runCursor: string | undefined
    do {
      const page = await listRuns({ work_order: order.node_id, cursor: runCursor, limit: 200 })
      const live = admission.runs(page.items).filter(activeRun)
      if (live.length) {
        const same = live.find(r => r.agent_principal_id === agentId && r.model_profile_id === profileId
          && (!accountId || r.requested_account_id === accountId))
        if (same) return { run: selection.runNow && same.status === 'queued' ? admission.run(await runNowOnce(same.id)) : same, reused: true }
        throw new Error('This work order already has an active run. Follow it in Agents before starting another.')
      }
      runCursor = page.next_cursor ?? undefined
    } while (runCursor)
  } else {
    const acceptance = typeof ticket.fields.acceptance_criteria === 'string' ? ticket.fields.acceptance_criteria.trim() : ''
    order = await request<WorkOrder>('/work-orders', 'POST', {
      title: `${ticket.key}: ${ticket.title}`, parent_id: ticket.id, assignee_principal_id: agentId,
      body: [ticket.body, acceptance && `## Acceptance criteria\n\n${acceptance}`].filter(Boolean).join('\n\n'),
      criteria: [acceptance || `Complete ${ticket.key} as described and attach evidence of the result.`],
    })
  }
  if (order.status === 'draft' || !order.assignee_principal_id) {
    order = await request<WorkOrder>(`/work-orders/${encodeURIComponent(order.node_id)}`, 'PATCH', {
      expected_revision: order.revision, assignee_principal_id: agentId,
      ...(order.status === 'draft' ? { status: 'ready' } : {}),
    })
  }
  const run = admission.run(await createRun(order.node_id, {
    agent_principal_id: agentId, model_profile_id: profileId, ...(accountId ? { requested_account_id: accountId } : {}),
    ...(selection.runNow ? { capacity_override: 'now' as const } : {}),
  }))
  return { run, reused: false }
}

export function launchState(run: AgentRun): { label: string; detail: string } {
  if (run.status === 'queued' && run.wait) return { label: 'Waiting', detail: capacityWaitText(run.wait) }
  if (run.status === 'queued') return { label: 'Queued', detail: 'Waiting for the daemon to reserve an account and claim this run.' }
  if (run.status === 'starting' || run.status === 'running' || run.status === 'waiting') return { label: 'Claimed', detail: 'The daemon claimed the run. Its managed session appears when registration is reported.' }
  return { label: ({ completed: 'Completed', failed: 'Failed', cancelled: 'Cancelled', ownership_lost: 'Ownership lost' })[run.status] ?? run.status, detail: 'This run has ended. Its reported outcome is available in Agents.' }
}

export function staleGrantRejection(error: unknown): boolean {
  return error instanceof APIError && error.status === 409 && /allow the model profile|model profile is not eligible/i.test(error.message)
}
