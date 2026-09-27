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
import { listRuns, type AgentAccount, type AgentRun, type ModelProfile } from './agents.ts'

export interface WorkOrder {
  node_id: string; status: 'draft' | 'ready' | 'running' | 'blocked' | 'done' | 'cancelled'
  revision: number; assignee_principal_id: string | null
  criteria: { id: string; description: string; checked_at: string | null }[]
}
export interface StartSelection { ticket: WorkNode; agentId: string; profileId: string; accountId?: string }
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
export async function startAgent(selection: StartSelection): Promise<{ run: AgentRun; reused: boolean }> {
  const { agentId, profileId, accountId } = selection
  const ticket = await getNode(selection.ticket.id)
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
  const candidates = orders.filter(o => !['done', 'cancelled'].includes(o.status) && (!o.assignee_principal_id || o.assignee_principal_id === agentId))
  if (candidates.length > 1) throw new Error('This ticket has several active work orders for this agent. Resolve them before starting another run.')
  let order = candidates[0]
  if (order) {
    if (order.status === 'blocked') throw new Error('This work order is blocked. Resolve its blocker before starting the agent.')
    let runCursor: string | undefined
    do {
      const page = await listRuns({ work_order: order.node_id, cursor: runCursor, limit: 200 })
      const live = page.items.filter(activeRun)
      if (live.length) {
        const same = live.find(r => r.agent_principal_id === agentId && r.model_profile_id === profileId
          && (!accountId || r.requested_account_id === accountId))
        if (same) return { run: same, reused: true }
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
  const run = await request<AgentRun>(`/work-orders/${encodeURIComponent(order.node_id)}/runs`, 'POST', {
    agent_principal_id: agentId, model_profile_id: profileId, ...(accountId ? { requested_account_id: accountId } : {}),
  })
  return { run, reused: false }
}

export interface DispatchHint { label: string; detail: string; tone: 'neutral' | 'warn' }
const freshProbe = (a: AgentAccount, now: number) => {
  const at = Date.parse(a.last_probe_at ?? '')
  return Number.isFinite(at) && at <= now && now - at < 120_000
}
function accountHeadroom(account: AgentAccount, now: number): 'open' | 'unknown' | 'closed' {
  const windows = (account.windows ?? []).filter(w => Date.parse(w.starts_at) <= now && now < Date.parse(w.ends_at))
  if (!windows.length) return 'closed'
  if (windows.some(w => w.provisional)) return 'unknown'
  const open = windows.every(w => [w.allowance, w.used, w.reserved].every(Number.isSafeInteger)
    && BigInt(w.allowance) - BigInt(w.used) - BigInt(w.reserved) > 0n)
  return open ? 'open' : 'closed'
}
// This is an observed preflight hint, never a reservation. The daemon enforces
// its enrollment, current parallel capacity, estimates and exact allowance pace.
export function dispatchHint(accounts: AgentAccount[], agentId: string, profile: ModelProfile | undefined, now: number, accountId?: string): DispatchHint {
  if (!agentId || !profile) return { label: 'Choose an agent and model', detail: 'The daemon will check account capacity before claiming the run.', tone: 'neutral' }
  const owned = accounts.filter(a => a.registered_by_principal_id === agentId)
  if (!owned.some(a => freshProbe(a, now))) return { label: 'No daemon online', detail: 'No account probe from this agent in the last two minutes. A queued run waits until its daemon connects.', tone: 'warn' }
  const available = owned.filter(a => (!accountId || a.id === accountId) && a.harness === profile.harness && a.state === 'available' && a.last_probe_ok && freshProbe(a, now))
  const ranks = available.map(account => accountHeadroom(account, now))
  if (ranks.includes('open')) return { label: 'Account available', detail: 'Recent account probe received. The daemon rechecks pacing and capacity, then runs in its configured workspace.', tone: 'neutral' }
  if (ranks.includes('unknown')) return { label: 'Allowance unknown', detail: 'Measured usage is missing for an active window, so remaining allowance is unknown. The run can queue; the daemon rechecks before it claims.', tone: 'warn' }
  return { label: 'No eligible account', detail: 'No matching account reports a fresh sign-in and allowance headroom. The run can queue; dispatch waits for capacity.', tone: 'warn' }
}

export function launchState(run: AgentRun): { label: string; detail: string } {
  if (run.status === 'queued') return { label: 'Queued', detail: 'Waiting for the daemon to reserve an account and claim this run.' }
  if (run.status === 'starting' || run.status === 'running' || run.status === 'waiting') return { label: 'Claimed', detail: 'The daemon claimed the run. Its managed session appears when registration is reported.' }
  return { label: ({ completed: 'Completed', failed: 'Failed', cancelled: 'Cancelled', ownership_lost: 'Ownership lost' })[run.status] ?? run.status, detail: 'This run has ended. Its reported outcome is available in Agents.' }
}
