// SPDX-License-Identifier: AGPL-3.0-only
import type { DeployTarget } from './deployTarget'
import { agentEventIdentity, type AgentEventIdentity } from './tierEvidenceLive.ts'
import { watchStreamHealth } from './streamHealth.ts'
// The agents workspace HTTP surface for a person's session: harness sessions and
// their typed controls, runs, approvals, accounts with allowance windows, models
// and project messages. Worker-only endpoints (heartbeat, drain, claim) are absent.
import { api, APIError } from './api.ts'
import { stamp, tick } from './position.ts'
import type { Admitted } from './ledger.ts'
import { sendSessionControl, requestManagedSessionControl, readSessionControl, type AgentRunRow, type HarnessSessionRow } from './agentRows.ts'

// Sessions and runs are fetched only by agentRows.ts and shown only after the ledger
// admitted them (AEON-449): the types a component works with are the admitted ones.
export type { Harness, MetadataChange, ProcessOwnership, NodeSummary, Paged, HarnessSessionRow, AgentRunRow } from './agentRows.ts'
export type HarnessSession = Admitted<HarnessSessionRow>
export type AgentRun = Admitted<AgentRunRow>
export interface SessionChangeRequest {
  id: string; session_id: string; expected_generation: string; kind: 'rename_request' | 'model_request'
  state: 'pending' | 'claimed' | 'completed'; sequence: number; expires_at: string
  outcome: 'applied' | 'rejected' | null; reason: string | null
  request_payload: { display_label?: string; model?: string; reasoning_effort?: string; account_id?: string; model_profile_id?: string }
}
export interface SessionControl {
  id: string; session_id: string; kind: 'interrupt' | 'stop'; state: 'pending' | 'claimed' | 'completed'; sequence: number
  outcome: 'applied' | 'rejected' | null; reason: string | null; created_at: string; claimed_at: string | null; completed_at: string | null
}
export interface AllowanceWrite {
  starts_at: string; ends_at: string; unit: 'requests' | 'tokens' | 'cost_micros' | 'percent'
  allowance: number; pace_model: 'steady' | 'frontload' | 'unrestricted'; burst_ratio: number
}
export interface AllowanceWindow extends AllowanceWrite {
  id: string; account_id: string; used: number; reserved: number
  provisional?: boolean
  /** A limit a person set by hand; it caps on top of the readings (AEON-384). */
  set_by_you?: boolean
}
export interface AgentAccount {
  usage_probe_enabled?: boolean
  usage_budget?: import('./capacity').CapacityBudget
  owner_person_id?: string; owner_person_name?: string; linked_at?: string; link_revision?: number
  ongoing_use_approved?: boolean
  reading_support?: 'every_5_min' | 'first_run' | 'statusline' | 'none'; quota_fingerprint?: string; quota_pool_fingerprint?: string; statusline_enabled?: boolean
  statusline_opt_in?: 'own' | 'workspace'
  provider?: string; model?: string; model_status?: 'known' | 'unknown' | 'unchecked'; model_data_note?: boolean
  openrouter_credits?: { observed_at: string; usage: number | null; limit: number | null; remaining: number | null }

  id: string; account_key: string; harness: string; daemon_id: string; label: string
  registered_by_principal_id: string; state: 'available' | 'draining' | 'unavailable'
  max_parallel_runs?: number; last_probe_at?: string | null; last_probe_ok?: boolean | null; created_at: string
  // Display metadata only. Callers must not render account_key. Null grants mean
  // the legacy tenant catalog; the start dialog does not expand them itself.
  plan?: string
  host_label?: string
  group_id?: string
  group_name?: string
  allowed_model_profile_ids?: string[] | null
  windows?: AllowanceWindow[]
}
export interface Approval {
  id: string; agent_principal_id: string; agent_name?: string | null; scope: string; resource_kind: 'tenant' | 'node' | 'run'
  resource_id?: string | null; run_id?: string | null; rationale: string; expires_at: string; proposed_at: string
  decision: 'approved' | 'denied' | null; decided_by_principal_id?: string | null
  risk?: 'low' | 'medium' | 'high'
  target?: DeployTarget | null
  target_digest_sha256?: string
}
export interface ModelProfile { id: string; slug: string; harness: string; family: string; model: string; effort: string; tier: string; enabled: boolean }
export interface ModelResolution { role: string; profile: ModelProfile | null; owner_required: boolean; source: string }
export interface MessageTarget { id: string; principal_id: string; address: string; adapter: string; target_kind: string; maximum_level: string; role: string; enabled: boolean }
export interface ProjectMessage {
  optimistic?: boolean; send_failed?: boolean; client_id?: string
  recipient_session_id?: string; sender_session_id?: string; sender_label?: string; from?: string
  id: string; sender_principal_id: string; recipient_principal_id: string; to: string; body: string; reply_to?: string | null
  sent_event_id: number; is_action_request: boolean; expects_reply: boolean; delivery_level: 'simple' | 'steer'
  status: 'accepted' | 'held'; reply_obligation: 'none' | 'open' | 'closed'
  created_at?: string; human_resolution_outcome?: 'resolved' | 'dismissed' | null
}
export interface HeldResolution { message_id: string; decision: 'resolved' | 'dismissed'; created_at: string }
export interface MessagePage { items: ProjectMessage[]; next_after: number; preamble?: string }
export interface MessageSend { recipient_session_id?: string; sender_session_id?: string; to: string; body: string; idempotency_key: string; reply_to?: string; expects_reply: boolean; is_action_request: boolean; delivery_level: 'simple' | 'steer' }

async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const start = tick()
  const response = await api(path, { method, ...(body === undefined ? {} : {
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' ? data.error : `Request failed (${response.status})`, data)
  }
  if (response.status === 204) return undefined as T
  // Every object of the body keeps the position it answers for and the tick its request
  // started at, so a row is merged by them wherever it ends up (AEON-449).
  return stamp(await response.json() as T, response, start)
}
export interface HostLabel { host: string; label: string }
export const listHostLabels = () => request<HostLabel[]>('/me/host-labels')
export const setHostLabel = (host: string, label: string | null) => request<HostLabel>('/me/host-labels', 'PUT', { host, label })
const enc = encodeURIComponent

const query = (params: Record<string, string | number | boolean | undefined>) => {
  const q = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) if (value !== undefined && value !== '') q.set(key, String(value))
  const text = q.toString()
  return text ? `?${text}` : ''
}
export const requestControl = sendSessionControl
export const requestManagedControl = requestManagedSessionControl
export const getControl = readSessionControl
export const setPiAccountModel = (id: string, model: string) => request<AgentAccount>(`/agent-accounts/${enc(id)}/model`, 'PUT', { model })
export const listAccounts = () => request<AgentAccount[]>('/agent-accounts')
export interface AccountGroup { id: string; harness: string; name: string; exclusive: boolean; account_ids: string[]; project_ids: string[] }
export const listGroups = () => request<AccountGroup[]>('/agent-accounts/groups')
export const createGroup = (body: { harness: string; name: string; exclusive: boolean; account_ids: string[]; project_ids: string[] }) => request<AccountGroup>('/agent-accounts/groups', 'POST', body)
export const deleteGroup = (id: string) => request<void>(`/agent-accounts/groups/${enc(id)}`, 'DELETE')
export interface TicketPin { ticket_id: string; harness: string; account_id?: string; group_id?: string }
export const listPins = (ticketId: string) => request<TicketPin[]>(`/agent-accounts/pins?ticket_id=${enc(ticketId)}`)
export const putPin = (body: { ticket_id: string; harness: string; account_id?: string; group_id?: string }) => request<void>('/agent-accounts/pins', 'PUT', body)
export const deletePin = (ticketId: string, harness: string) => request<void>(`/agent-accounts/pins?ticket_id=${enc(ticketId)}&harness=${enc(harness)}`, 'DELETE')
export const setRunTarget = (runId: string, body: { account_id?: string; group_id?: string }) => request<void>(`/agent-accounts/runs/${enc(runId)}/target`, 'POST', body)
export const setAccountState = (id: string, state: AgentAccount['state']) => request<AgentAccount>(`/agent-accounts/${enc(id)}`, 'PATCH', { state })
// Person-only Remove: the account leaves the lists; its runs and history stay (AEON-402).
export const archiveAccount = (id: string) => request<AgentAccount>(`/agent-accounts/${enc(id)}/archive`, 'POST')
export const listApprovals = () => request<Approval[]>('/approvals?limit=200')
export const decideApproval = (id: string, decision: 'approved' | 'denied', reason: string) => request<Approval>(`/approvals/${enc(id)}/decision`, 'POST', { decision, reason })
export const revokeApproval = (id: string) => request<Approval>(`/approvals/${enc(id)}/revoke`, 'POST')
export const listModels = () => request<ModelProfile[]>('/models')
// Task-appropriate profile for a work role on one harness. A miss is "routing did not answer", not a guessed model.
// The start cascade must not call this to fill models an account did not grant.
export async function resolveModelRole(role: string, harness: string): Promise<ModelProfile | null> {
  try {
    const data = await request<ModelResolution>(`/models/resolve${query({ role, harness })}`)
    return data?.profile?.id ? data.profile : null
  } catch {
    return null
  }
}
export const listTargets = (projectId: string) => request<MessageTarget[]>(`/projects/${enc(projectId)}/message-targets`)
export const listMessages = (projectId: string, params: { session?: string; newest_first?: boolean; pending?: boolean; address?: string; thread?: string; after?: number; limit?: number } = {}) =>
  request<MessagePage>(`/projects/${enc(projectId)}/messages${query({ limit: 200, newest_first: true, ...params })}`)
export const resolveMessage = (projectId: string, messageId: string, decision: HeldResolution['decision'], note: string) =>
  request<HeldResolution>(`/projects/${enc(projectId)}/messages/${enc(messageId)}/resolution`, 'POST', { decision, note })
export const sendMessage = (projectId: string, body: MessageSend) => request<ProjectMessage>(`/projects/${enc(projectId)}/messages`, 'POST', body)
// AEON-280: the sender's delivery progress per message (sender-only; others omitted).
export interface MessageStatus { message_id: string; status: 'sent' | 'delivered' | 'read' | 'not_delivered'; reason?: string; delivered_at: string | null; read_at: string | null; deliver_by: string | null }
export const messageStatuses = (ids: string[]) => request<{ items: MessageStatus[] }>(`/inbox/message-status?ids=${ids.map(enc).join(',')}`)

export const message = (error: unknown) => error instanceof Error ? error.message : 'Request failed. Please retry.'


// Named server events that change what the agents workspace shows. They are wake
// hints only: the caller re-reads the authorized projections. Heartbeats use the
// periodic refresh; registration/stop and reconnect wake the consumer immediately.
const HARNESS_EVENTS = ['registered', 'bound', 'yielded', 'stopped', 'removed', 'restored', 'revived', 'archived', 'metadata_changed', 'adopted', 'handed_over', 'stop_confirmed', 'control_requested', 'control_claimed', 'control_completed', 'pause_requested', 'pause_planned', 'paused', 'pause_interrupt_requested', 'pause_level_changed', 'pause_stop_requested', 'resume_requested', 'resumed', 'pause_cancelled', 'tier_requested', 'tier_declined', 'tier_reported', 'tier_changed', 'tier_cancelled', 'usage_reported']
const OTHER_EVENTS = ['inbox.sent', 'harness.leaving_requested', 'harness.leaving_cancelled', 'approval.proposed', 'approval.approved', 'approval.denied', 'approval.revoked', 'run.created', 'run.claimed', 'run.telemetry', 'work_order.started', 'work_order.updated', 'inbox.compat_sent', 'inbox.delivery_queued', 'inbox.reply_obligation_closed', 'inbox.action_resolved']
// Delivery progress of sent messages (AEON-280). These only refresh message
// status, never the whole workspace.
export const DELIVERY_EVENTS = ['inbox.message_fetched', 'inbox.receipt_handed_off', 'inbox.receipt_failed', 'inbox.delivery_failed']
export function subscribeAgents(changed: (event?: string, identity?: AgentEventIdentity) => void, connection: (live: boolean) => void = () => {}, delivery: () => void = () => {}, telemetry = false, recover: () => void = changed): () => void {
  if (typeof EventSource === 'undefined') return () => {}
  let stream: EventSource
  let stopped = false
  const health = watchStreamHealth(() => {
    stream.close()
    connection(false)
    recover(); delivery()
    connect()
  })
  function connect() {
    const source = stream = new EventSource('/api/events/stream?after=latest')
    const current = (run: (event: Event) => void) => (event: Event) => { if (!stopped && stream === source) { health.heard(); run(event) } }
    source.onopen = current(() => { connection(true); changed(); delivery() })
    source.onerror = () => { if (!stopped && stream === source) connection(false) }
    source.addEventListener('stream.ping', current(() => {}))
    source.addEventListener('stream.ready', current(() => {}))
    for (const name of [...HARNESS_EVENTS.map(kind => `harness.${kind}`), ...OTHER_EVENTS]) source.addEventListener(name, current(event => { const identity = agentEventIdentity((event as MessageEvent).data); if (identity) changed(name, identity); else changed(name) }))
    // Heartbeats prove liveness without invalidating the 20-second live feed poll.
    source.addEventListener('harness.heartbeat', current(() => { if (telemetry) changed() }))
    for (const name of DELIVERY_EVENTS) source.addEventListener(name, current(delivery))
  }
  connect()
  return () => { stopped = true; health.stop(); stream.close() }
}

export const approveAccountCapacity = (id: string) => request<void>(`/agent-accounts/${enc(id)}/capacity/approve`, 'POST', {})
export const setClaudeStatusline = (id: string, enabled: boolean) => request<{ enabled: boolean }>(`/agent-accounts/${enc(id)}/statusline`, 'PUT', { enabled })

export function claudeStatuslineCopy(audience: AgentAccount['statusline_opt_in'], name: string) {
  return audience === 'workspace' ? `Show ${name} in this Claude account's status line` : `Show ${name} in your Claude status line`
}

/** Pool only accounts a person explicitly confirms; fingerprints remain hints. */
export function putQuotaPool(account_ids: string[], quota_fingerprint: string, confirmed: boolean): Promise<void> {
  return request<void>('/agent-accounts/quota-pool', 'PUT', { account_ids, quota_fingerprint, confirmed })
}

export const readPauseDefault = () => request<{ default_level: import('./agentPause').PauseLevel }>('/me/agent-pause-settings')
export const savePauseDefault = (default_level: import('./agentPause').PauseLevel) => request<{ default_level: import('./agentPause').PauseLevel }>('/me/agent-pause-settings', 'PUT', { default_level })
export const readEstimateInterval = () => request<{ interval_minutes: number }>('/settings/eta-interval')

export const setUsageProbe = (id: string, binding_revision: number, enabled: boolean) => request<void>(`/agent-accounts/${enc(id)}/usage-probe`, 'PUT', { binding_revision, enabled })
