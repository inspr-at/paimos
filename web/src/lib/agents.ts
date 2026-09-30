// SPDX-License-Identifier: AGPL-3.0-only
import type { CapacityWait } from './capacityWait.ts'
import type { DeployTarget } from './deployTarget'
// The agents workspace HTTP surface for a person's session: harness sessions and
// their typed controls, runs, approvals, accounts with allowance windows, models
// and project messages. Worker-only endpoints (heartbeat, drain, claim) are absent.
import { api, APIError } from './api.ts'
import type { AttentionReason } from './agentSignals.ts'
import type { LivePage } from './liveAgents.ts'

export type Harness = 'codex' | 'claude' | 'pi' | 'cursor' | 'grok'
export interface MetadataChange {
  field: 'display_label' | 'model' | 'reasoning_effort'
  previous_value: string | null
  value: string | null
  at: string
}
export interface ProcessOwnership { daemon_id: string; generation: string; process_id: string; root_pid: number; group_id: number; started_at: string }
export interface HarnessSession {
  vendor_limited?: boolean; limit_window?: string; limit_resets_at?: string | null
  handed_over_to_id?: string; adopted_from_id?: string | null; can_reparent?: boolean
  watch?: import("./attachWatch").AttachStatus
  id: string; project_id: string; agent_principal_id: string
  archived_at?: string | null; recovery_process_state?: 'unknown' | null
  process_ownership?: ProcessOwnership; process_observed_at?: string
  display_label?: string | null
  model?: string | null; reasoning_effort?: string | null; account_label?: string | null; harness_version?: string | null
  brief?: string | null; worktree?: string | null; branch?: string | null; commits?: { sha: string; subject: string }[]
  activity_note?: string | null; activity_note_id?: number; activity_history?: { note: string; at: string }[]
  run_id: string | null; ticket_node_id: string | null; work_order_id: string | null; parent_harness_session_id: string | null
  harness: Harness; host: string; management_mode: 'managed' | 'unmanaged'; role: 'coordinator' | 'worker'
  work_shape: 'unknown' | 'ship' | 'scout'; advertised_capabilities: string[]
  phase: 'starting' | 'working' | 'yielded' | 'stopping' | 'stopped'; activity: 'unknown' | 'busy' | 'idle' | 'throttled'
  run_status?: string | null; needs_attention?: boolean; has_problem?: boolean; attention_reasons?: AttentionReason[]; activity_sequence: number; revision: number; heartbeat_at: string | null; stopped_at: string | null; stop_reason: string | null; created_at: string
  eta_ready_at?: string | null; eta_live_at?: string | null; progress_pct?: number | null; eta_reported_at?: string | null; eta_stale?: boolean
  // AEON-280: the last inbox pull of this generation; absent until it pulls once.
  inbox_seen_at?: string | null; inbox_seen_via?: 'hook' | 'drain' | 'long_poll' | 'stream' | 'ack' | null
  // AEON-369: true when a vendor session reference is stored. The reference itself is never returned.
  has_vendor_session_ref?: boolean
  // The tenant-wide list adds node summaries (B7) and the agent principal's name (U13).
  project?: NodeSummary; ticket?: NodeSummary | null; agent?: { id: string; name: string } | null
}
// SN1 returns bounded history only on GET session detail, never on list rows.
export interface HarnessSessionDetail extends HarnessSession { metadata_history?: MetadataChange[] }
export interface NodeSummary { id: string; key: string; title: string }
export interface Paged<T> { items: T[]; next_cursor: string | null }
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
  ongoing_use_approved?: boolean
  reading_support?: 'every_5_min' | 'first_run' | 'statusline' | 'none'; quota_fingerprint?: string; statusline_enabled?: boolean
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
export interface AgentRun {
  capacity_override?: '' | 'now'
  wait?: CapacityWait
  id: string; work_order_id: string; agent_principal_id: string; model_profile_id?: string | null
  account_id?: string | null; status: 'queued' | 'starting' | 'running' | 'waiting' | 'completed' | 'failed' | 'cancelled' | 'ownership_lost'
  requested_account_id?: string | null
  requested_model?: string | null; effective_model?: string | null; model_evidence: 'unverified' | 'vendor_reported'
  input_tokens: number; output_tokens: number; cost_micros: number
  outcome?: 'completed' | 'failed' | 'cancelled' | 'ownership_lost' | null; duration_ms?: number | null
  started_at?: string | null; ended_at?: string | null; created_at: string
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
  const response = await api(path, { method, ...(body === undefined ? {} : {
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' ? data.error : `Request failed (${response.status})`, data)
  }
  if (response.status === 204) return undefined as T
  return response.json()
}
const enc = encodeURIComponent
const sessionPath = (projectId: string, sessionId = '') => `/projects/${enc(projectId)}/harness-sessions${sessionId ? `/${enc(sessionId)}` : ''}`

const query = (params: Record<string, string | number | boolean | undefined>) => {
  const q = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) if (value !== undefined && value !== '') q.set(key, String(value))
  const text = q.toString()
  return text ? `?${text}` : ''
}
// Tenant-wide sessions, newest first, with project and ticket summaries.
// view=current (the Agents list) omits sessions that ended more than 24 hours ago;
// view=all, the server default, keeps every generation for history (AEON-291).
export const listAllSessions = (params: { ticket?: string; agent?: string; project?: string; state?: string; view?: 'current' | 'all'; cursor?: string; limit?: number } = {}) =>
  request<Paged<HarnessSession>>(`/harness-sessions${query({ limit: 200, ...params })}`)
// Agents working right now in every visible project, in one read (AEON-184).
export const getLiveAgents = () => request<LivePage>('/harness-sessions/live?include_inactive=true')
export const listRuns = (params: { session?: string; agent?: string; work_order?: string; cursor?: string; limit?: number } = {}) =>
  request<Paged<AgentRun>>(`/runs${query({ limit: 50, ...params })}`)
export interface RemoveSessionResult { session: HarnessSession; message: string; processes_signalled: false; process_state: 'unknown'; event_id?: number; undoable?: boolean }
export const removeSession = (session: HarnessSession, reason: string) => request<RemoveSessionResult>(`${sessionPath(session.project_id, session.id)}/remove`, 'POST', { reason })
// Undo of a removal restores the record exactly as it was (POST /events/{id}/undo).
export const reparentSession = (worker: HarnessSession, lead: HarnessSession) => request<{ session: HarnessSession; event_id: number; undoable: boolean }>(`${sessionPath(worker.project_id, worker.id)}/reparent`, 'POST', { expected_revision: worker.revision, parent_harness_session_id: lead.id })
export const undoRemoval = (eventId: number) => request<{ after: HarnessSession }>(`/events/${enc(String(eventId))}/undo`, 'POST').then(event => event.after)
export const removeStaleSessions = (projectId: string, reason: string) => request<{ items: RemoveSessionResult[]; cutoff: string; more: boolean }>(`${sessionPath(projectId)}/remove-stale`, 'POST', { reason })
export const requestControl = (projectId: string, sessionId: string, kind: SessionControl['kind']) => request<SessionControl>(`${sessionPath(projectId, sessionId)}/controls/${kind}`, 'POST', {})
// A managed_control_v1 session takes Interrupt and Stop through the
// ownership-aware route, bound to the exact process generation (AEON-291).
export const requestManagedControl = (session: HarnessSession, kind: SessionControl['kind']) =>
  request<SessionControl>(`${sessionPath(session.project_id, session.id)}/managed-controls`, 'POST', { request_id: crypto.randomUUID(), kind, expected_ownership: { ...session.process_ownership } })
export const getControl = (projectId: string, sessionId: string, controlId: string) => request<SessionControl>(`${sessionPath(projectId, sessionId)}/controls/${enc(controlId)}`)
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
export const getRun = (id: string) => request<AgentRun>(`/runs/${enc(id)}`)
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
const HARNESS_EVENTS = ['registered', 'bound', 'yielded', 'stopped', 'removed', 'restored', 'revived', 'control_requested', 'control_claimed', 'control_completed']
const OTHER_EVENTS = ['approval.proposed', 'approval.approved', 'approval.denied', 'approval.revoked', 'run.created', 'run.claimed', 'run.telemetry', 'work_order.started', 'work_order.updated', 'inbox.compat_sent', 'inbox.delivery_queued', 'inbox.reply_obligation_closed', 'inbox.action_resolved']
// Delivery progress of sent messages (AEON-280). These only refresh message
// status, never the whole workspace.
export const DELIVERY_EVENTS = ['inbox.message_fetched', 'inbox.receipt_handed_off', 'inbox.receipt_failed', 'inbox.delivery_failed']
export function subscribeAgents(changed: () => void, connection: (live: boolean) => void = () => {}, delivery: () => void = () => {}): () => void {
  if (typeof EventSource === 'undefined') return () => {}
  const stream = new EventSource('/api/events/stream')
  stream.onopen = () => { connection(true); changed(); delivery() }
  stream.onerror = () => connection(false)
  for (const name of [...HARNESS_EVENTS.map(kind => `harness.${kind}`), ...OTHER_EVENTS]) stream.addEventListener(name, changed)
  for (const name of DELIVERY_EVENTS) stream.addEventListener(name, () => delivery())
  return () => stream.close()
}

export const approveAccountCapacity = (id: string) => request<void>(`/agent-accounts/${enc(id)}/capacity/approve`, 'POST', {})
export const runNowOnce = (id: string) => request<AgentRun>(`/runs/${enc(id)}/capacity-override`, 'POST', { capacity_override: 'now' })
// Person-only: a queued run ends as cancelled and its capacity holds are released (AEON-402).
export const cancelRun = (id: string) => request<AgentRun>(`/runs/${enc(id)}/cancel`, 'POST')
export const setClaudeStatusline = (id: string, enabled: boolean) => request<{ enabled: boolean }>(`/agent-accounts/${enc(id)}/statusline`, 'PUT', { enabled })

export function claudeStatuslineCopy(audience: AgentAccount['statusline_opt_in'], name: string) {
  return audience === 'workspace' ? `Show ${name} in this Claude account's status line` : `Show ${name} in your Claude status line`
}
