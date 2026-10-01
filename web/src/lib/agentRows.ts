// SPDX-License-Identifier: AGPL-3.0-only
// The one module that talks to the session and run endpoints (AEON-449), reads and
// writes alike, the launch of a run included. Whatever it returns that is a session or
// a run is a Wire row: stamped with what its answer knew about it (its own row_version,
// the list snapshot's position when the read had one, the tick its request started at)
// and unreadable until the ledger has judged it (ledger.ts, wire.ts). So no component
// can render or keep a row that went around the ledger, and eslint.config.js forbids any
// other file from naming these endpoints.
//
// What is not a row stays a plain value: the answer to a control, a recovery preview, a
// thread. Sub-resources use fixed operations here; no path builder is exported.
import { api, APIError } from './api.ts'
import type { CapacityWait } from './capacityWait.ts'
import type { AttentionReason } from './agentSignals.ts'
import type { SessionChangeRequest, SessionControl } from './agents.ts'
import type { LivePage } from './liveAgents.ts'
import { parsePosition, stampAt, tick } from './position.ts'
import { wrapRow, type Wire } from './wire.ts'

export type Harness = 'codex' | 'claude' | 'pi' | 'cursor' | 'grok' | 'media' | 'terminal'
export interface MetadataChange {
  field: 'display_label' | 'model' | 'reasoning_effort'
  previous_value: string | null
  value: string | null
  at: string
}
export interface ProcessOwnership { daemon_id: string; generation: string; process_id: string; root_pid: number; group_id: number; started_at: string }
export interface NodeSummary { id: string; key: string; title: string }
export interface Paged<T> { items: T[]; next_cursor: string | null }

// A session as the API sends it. Nothing outside the ledger may hold one: see
// HarnessSession in lib/agents.ts for the type the page works with.
export interface CurrentAgentActivity { text: string; source: 'agent' | 'auto'; at: string }
export interface HarnessSessionRow {
  agent_activity_mode?: 'off' | 'tool_activity' | 'agent_summary'
  current_activity?: CurrentAgentActivity | null
  current_activity_history?: CurrentAgentActivity[]
  vendor_limited?: boolean; limit_window?: string; limit_resets_at?: string | null
  handed_over_to_id?: string; adopted_from_id?: string | null; can_reparent?: boolean
  watch?: import('./attachWatch').AttachStatus
  id: string; project_id: string; agent_principal_id: string
  archived_at?: string | null; recovery_process_state?: 'unknown' | null
  process_ownership?: ProcessOwnership; process_observed_at?: string
  generator?: string | null; command?: string | null
  display_label?: string | null
  model?: string | null; reasoning_effort?: string | null; account_label?: string | null; harness_version?: string | null
  brief?: string | null; worktree?: string | null; branch?: string | null; commits?: { sha: string; subject: string }[]
  activity_note?: string | null; activity_note_id?: number; activity_history?: { note: string; at: string }[]
  // SN1 returns bounded history only on GET session detail, never on list rows.
  metadata_history?: MetadataChange[]
  run_id: string | null; ticket_node_id: string | null; work_order_id: string | null; parent_harness_session_id: string | null
  harness: Harness; host: string; management_mode: 'managed' | 'unmanaged'; role: 'coordinator' | 'worker'
  work_shape: 'unknown' | 'ship' | 'scout'; advertised_capabilities: string[]
  phase: 'starting' | 'working' | 'yielded' | 'stopping' | 'stopped'; activity: 'unknown' | 'busy' | 'idle' | 'throttled'
  run_status?: string | null; needs_attention?: boolean; has_problem?: boolean; finished: boolean; attention_reasons?: AttentionReason[]; activity_sequence: number; revision: number
  // The row's own revision: bumped by the server inside every statement that changes
  // the row, so the larger of two copies is the newer. Always sent; optional here only
  // so a fixture may omit it (such a row can never replace one that has it).
  row_version?: number
  heartbeat_at: string | null; stopped_at: string | null; stop_reason: string | null; created_at: string
  eta_ready_at?: string | null; eta_live_at?: string | null; progress_pct?: number | null; eta_reported_at?: string | null; eta_stale?: boolean
  // AEON-280: the last inbox pull of this generation; absent until it pulls once.
  inbox_seen_at?: string | null; inbox_seen_via?: 'hook' | 'drain' | 'long_poll' | 'stream' | 'ack' | null
  // AEON-369: true when a vendor session reference is stored. The reference itself is never returned.
  has_vendor_session_ref?: boolean
  // The tenant-wide list adds node summaries (B7) and the agent principal's name (U13).
  project?: NodeSummary; ticket?: NodeSummary | null; agent?: { id: string; name: string } | null
}

export interface AgentRunRow {
  capacity_override?: '' | 'now'
  wait?: CapacityWait
  id: string; work_order_id: string; agent_principal_id: string; model_profile_id?: string | null
  account_id?: string | null; status: 'queued' | 'starting' | 'running' | 'waiting' | 'completed' | 'failed' | 'cancelled' | 'ownership_lost'
  requested_account_id?: string | null
  requested_model?: string | null; effective_model?: string | null; model_evidence: 'unverified' | 'vendor_reported'
  input_tokens: number; output_tokens: number; cost_micros: number
  outcome?: 'completed' | 'failed' | 'cancelled' | 'ownership_lost' | null; duration_ms?: number | null
  started_at?: string | null; ended_at?: string | null; created_at: string
  // The row's own revision; see HarnessSessionRow.
  row_version?: number
}

export interface RemoveSessionResult { session: Wire<HarnessSessionRow>; message: string; processes_signalled: false; process_state: 'unknown'; event_id?: number; undoable?: boolean }
export interface ReparentResult { session: Wire<HarnessSessionRow>; event_id: number; undoable: boolean }
export interface StaleRemoval { items: RemoveSessionResult[]; cutoff: string; more: boolean }

const enc = encodeURIComponent
const query = (params: Record<string, string | number | boolean | undefined>) => {
  const q = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) if (value !== undefined && value !== '') q.set(key, String(value))
  const text = q.toString()
  return text ? `?${text}` : ''
}
// The path of a session's own sub-resources (controls, recovery, requests, read marker,
// watch, provenance). A session row itself is read only through this module.
const sessionResource = (projectId: string, sessionId: string, rest: string) => `/projects/${enc(projectId)}/harness-sessions/${enc(sessionId)}/${rest}`
// The same for the sub-resources that hang off a session by its id alone (its delivery rating).
const sessionResourceById = (sessionId: string, rest: string) => `/harness-sessions/${enc(sessionId)}/${rest}`

// What one answer knows: the tick its request started at and, for a read, the position
// of the snapshot it returned. A write's position is the write floor (api() raises it)
// and says nothing about its body, so its rows carry none.
interface Answer<B> { body: B; start: number; position: number | undefined }

async function call<B>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<Answer<B>> {
  const start = tick()
  const response = await api(path, { method, ...(signal ? { signal } : {}), ...(body === undefined ? {} : {
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data?.error === 'string' ? data.error : `Request failed (${response.status})`, data)
  }
  const position = method === 'GET' ? parsePosition(response) : undefined
  return { body: await response.json() as B, start, position }
}
const rowOf = <T extends { id: string; row_version?: number }>(row: T, at: Answer<unknown>) => wrapRow(row, at.position, at.start)
// A page keeps the position of its snapshot beside it, for the order of the reads of one collection.
function pageOf<T extends { id: string; row_version?: number }>(at: Answer<Paged<T>>): Paged<Wire<T>> {
  return stampAt({ items: at.body.items.map(item => rowOf(item, at)), next_cursor: at.body.next_cursor }, { position: at.position, start: at.start })
}
const resultOf = (result: Omit<RemoveSessionResult, 'session'> & { session: HarnessSessionRow }, at: Answer<unknown>): RemoveSessionResult => ({ ...result, session: rowOf(result.session, at) })

// ---------- Reads ----------
// Tenant-wide sessions, newest first, with project and ticket summaries.
// view=current (the Agents list) omits sessions that ended more than 24 hours ago;
// view=all, the server default, keeps every generation for history (AEON-291).
export const listAllSessions = async (params: { ticket?: string; agent?: string; project?: string; state?: string; view?: 'current' | 'all'; cursor?: string; limit?: number } = {}) =>
  pageOf(await call<Paged<HarnessSessionRow>>(`/harness-sessions${query({ limit: 200, ...params })}`))
// Session detail: the list row plus the bounded history only the detail carries.
export const getSession = async (projectId: string, sessionId: string, signal?: AbortSignal) => {
  const at = await call<HarnessSessionRow>(`/projects/${enc(projectId)}/harness-sessions/${enc(sessionId)}`, 'GET', undefined, signal)
  return rowOf(at.body, at)
}
export const listRuns = async (params: { session?: string; agent?: string; work_order?: string; cursor?: string; limit?: number } = {}) =>
  pageOf(await call<Paged<AgentRunRow>>(`/runs${query({ limit: 50, ...params })}`))
export const getRun = async (id: string) => {
  const at = await call<AgentRunRow>(`/runs/${enc(id)}`)
  return rowOf(at.body, at)
}
// The change requests (rename, model) a session's detail carries. Not a row: the session itself is not read here.
export const readSessionRequests = async (projectId: string, sessionId: string, signal?: AbortSignal): Promise<SessionChangeRequest[]> =>
  (await call<{ controls?: SessionChangeRequest[] }>(`/projects/${enc(projectId)}/harness-sessions/${enc(sessionId)}`, 'GET', undefined, signal)).body.controls ?? []
// Agents working right now in every visible project, in one read (AEON-184). A
// privacy-filtered projection for the Projects page, never merged with session rows.
export const getLiveAgents = async () => {
  const answer = await call<LivePage>('/harness-sessions/live?include_inactive=true')
  return stampAt(answer.body, { position: answer.position, start: answer.start })
}

// ---------- Writes ----------
export const removeSession = async (session: { id: string; project_id: string }, reason: string) => {
  const at = await call<Omit<RemoveSessionResult, 'session'> & { session: HarnessSessionRow }>(`/projects/${enc(session.project_id)}/harness-sessions/${enc(session.id)}/remove`, 'POST', { reason })
  return resultOf(at.body, at)
}
export const removeStaleSessions = async (projectId: string, reason: string): Promise<StaleRemoval> => {
  const at = await call<Omit<StaleRemoval, 'items'> & { items: (Omit<RemoveSessionResult, 'session'> & { session: HarnessSessionRow })[] }>(`/projects/${enc(projectId)}/harness-sessions/remove-stale`, 'POST', { reason })
  return { ...at.body, items: at.body.items.map(item => resultOf(item, at)) }
}
export const reparentSession = async (worker: { id: string; project_id: string; revision: number }, lead: { id: string }): Promise<ReparentResult> => {
  const at = await call<Omit<ReparentResult, 'session'> & { session: HarnessSessionRow }>(`/projects/${enc(worker.project_id)}/harness-sessions/${enc(worker.id)}/reparent`, 'POST', { expected_revision: worker.revision, parent_harness_session_id: lead.id })
  return { ...at.body, session: rowOf(at.body.session, at) }
}
// Undo of a removal or a move restores the record as it was (POST /events/{id}/undo).
export const undoRemoval = async (eventId: number) => {
  const at = await call<{ after: HarnessSessionRow }>(`/events/${enc(String(eventId))}/undo`, 'POST')
  return rowOf(at.body.after, at)
}
// Queues one run for a work order: the launch of an agent. The result is the run's first row.
export const createRun = async (workOrderId: string, body: { agent_principal_id: string; model_profile_id: string; requested_account_id?: string; capacity_override?: 'now' }) => {
  const at = await call<AgentRunRow>(`/work-orders/${enc(workOrderId)}/runs`, 'POST', body)
  return rowOf(at.body, at)
}
export const runNowOnce = async (id: string) => {
  const at = await call<AgentRunRow>(`/runs/${enc(id)}/capacity-override`, 'POST', { capacity_override: 'now' })
  return rowOf(at.body, at)
}
// Person-only: a queued run ends as cancelled and its capacity holds are released (AEON-402).
export const cancelRun = async (id: string) => {
  const at = await call<AgentRunRow>(`/runs/${enc(id)}/cancel`, 'POST')
  return rowOf(at.body, at)
}

// Fixed sub-resource operations keep raw session/run paths private. These return
// controls, settings, previews and viewer metadata, never a session or run row.
const post = (body: unknown): RequestInit => ({ method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
export const readControlResponse = (project: string, session: string, control: string) => api(sessionResource(project, session, `controls/${enc(control)}`))
export const readManagedSettings = (project: string, session: string) => api(sessionResource(project, session, 'managed-settings'))
export const sendManagedControl = (project: string, session: string, body: unknown) => api(sessionResource(project, session, 'managed-controls'), post(body))
export const sendSessionControl = async (project: string, session: string, kind: 'interrupt' | 'stop'): Promise<SessionControl> =>
  (await call<SessionControl>(sessionResource(project, session, `controls/${kind}`), 'POST', {})).body
export const readSessionControl = async (project: string, session: string, control: string): Promise<SessionControl> =>
  (await call<SessionControl>(sessionResource(project, session, `controls/${enc(control)}`))).body
export const requestManagedSessionControl = async (session: { project_id: string; id: string; process_ownership?: Readonly<ProcessOwnership> }, kind: 'interrupt' | 'stop'): Promise<SessionControl> =>
  (await call<SessionControl>(sessionResource(session.project_id, session.id, 'managed-controls'), 'POST', { request_id: crypto.randomUUID(), kind, expected_ownership: { ...session.process_ownership } })).body
export const sendSessionRequest = (project: string, session: string, body: unknown, signal?: AbortSignal) => api(sessionResource(project, session, 'requests'), { ...post(body), signal })
export const readSessionRecovery = (project: string, session: string) => api(sessionResource(project, session, 'recovery'))
export const archiveSession = (project: string, session: string, body: unknown) => api(sessionResource(project, session, 'archive'), post(body))
export const forceStopSession = (project: string, session: string, body: unknown) => api(sessionResource(project, session, 'controls/force-stop'), post(body))
export const readSessionProvenance = (project: string, session: string) => api(sessionResource(project, session, 'provenance'))
export const readSessionMarker = (project: string, session: string) => api(sessionResource(project, session, 'read-marker'))
export const writeSessionMarker = (project: string, session: string, body: { last_read_message_id: string; last_read_event_id: number }) => api(sessionResource(project, session, 'read-marker'), { ...post(body), method: 'PUT', keepalive: true })
export const openSessionWatch = (project: string, session: string) => new EventSource(`/api${sessionResource(project, session, 'watch')}`)
export const readSessionRating = (session: string, signal?: AbortSignal) => api(sessionResourceById(session, 'delivery-rating'), { signal })
export const writeSessionRating = (session: string, body: { score: number | null; tags: string[]; comment: string }) => api(sessionResourceById(session, 'delivery-rating'), { ...post(body), method: 'PUT' })
export const deleteSessionRating = (session: string) => api(sessionResourceById(session, 'delivery-rating'), { method: 'DELETE' })
