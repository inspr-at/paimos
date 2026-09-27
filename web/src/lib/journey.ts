// SPDX-License-Identifier: AGPL-3.0-only
// The project journey (R3): the server's projection, its actions and the data
// each stage shows. The server derives the stage and the single next action; the
// face only shows them and sends the one action with the journey's revision.
import { api, APIError, getKinds, listNodes, type ListItem, type WorkNode } from './api.ts'
import type { Approval } from './agents.ts'
import { statusMeta } from './work.ts'

export const STAGES = ['inspire', 'shape', 'requirements', 'plan', 'build', 'deploy', 'access', 'live'] as const
export type Stage = typeof STAGES[number]
export const isStage = (value: unknown): value is Stage => typeof value === 'string' && (STAGES as readonly string[]).includes(value)
export const STAGE_LABEL: Record<Stage, string> = {
  inspire: 'Inspire', shape: 'Shape', requirements: 'Requirements', plan: 'Plan', build: 'Build', deploy: 'Deploy', access: 'Access', live: 'Live',
}
// Who carries each stage: Aithema drafts, this product plans and builds (null:
// its brand's short name), Pharos deploys, Janus grants access.
export const STAGE_OWNER: Record<Stage, string | null> = {
  inspire: 'Aithema', shape: 'Aithema', requirements: 'Aithema', plan: null, build: null, deploy: 'Pharos', access: 'Janus', live: 'INSPR',
}
export type Profile = 'personal' | 'professional' | 'enterprise'
export const PROFILES: Record<Profile, { label: string; line: string }> = {
  personal: { label: 'Personal', line: 'One person, own time, learning along the way.' },
  professional: { label: 'Professional', line: 'A small business shipping a real job on a budget.' },
  enterprise: { label: 'Enterprise', line: 'A company with contracts, reviews and compliance.' },
}

export type StageState = 'done' | 'current' | 'later' | 'skipped' | 'blocked'
export type GateOfferState = 'pending' | 'approved_live' | 'expired' | 'revoked' | 'rejected' | 'grant_missing'
export interface JourneyStage {
  key: Stage; state: StageState; gate_scope: string; gate_approval_id: string | null; gate_live: boolean; handoff_id: string | null
  gate_offer_id?: string; gate_offer_state?: GateOfferState; gate_offer_expires_at?: string
}
export type ActionKey = 'confirm_brief' | 'go' | 'reduce_scope' | 'park' | 'drop' | 'reopen' | 'open_first_release' | 'start_build' | 'mark_candidate'
  | 'approve_candidate' | 'reject_candidate' | 'approve_deploy' | 'retry_deploy' | 'approve_permit' | 'plan_next_release'
export type NextKey = 'continue_intake' | 'confirm_brief' | 'decide' | 'reopen' | 'approve_requirements' | 'open_first_release' | 'start_build' | 'wait_for_build'
  | 'mark_candidate' | 'approve_candidate' | 'approve_deploy' | 'retry_deploy' | 'approve_permit' | 'plan_next_release'
export interface NextAction { key: NextKey; label: string; stage: Stage; available: boolean; reason?: string; approval_request_id: string | null }
// Whether Pharos can admit a launch now; the reason is the current blocker (empty when it can).
export interface LaunchReadiness { can_admit: boolean; reason: string }
export interface Journey {
  project_node_id: string; profile: Profile; revision: number; stage: Stage; stages: JourneyStage[]
  next_action: NextAction; requirements_revision: number; current_release_id: string | null
  // B10 (AEON-78): the requirements content digest, the exact approval scope the
  // requirements gate needs, and launch readiness for the Deploy stage.
  requirements_digest_sha256: string
  requirements_approval_scope: string
  launch_readiness: LaunchReadiness
  // B11, not on the server yet: how the stage was reached ('derived' from an
  // imported project's history). Absent means unknown.
  stage_source?: 'journey' | 'derived'
  // U24: the project came from classic Paimos with its history (older servers:
  // only while the stage is still derived from it).
  imported?: boolean
}
// Whether the project came with its history rather than starting here.
export const isImported = (journey: Pick<Journey, 'imported' | 'stage_source'>) => journey.imported ?? journey.stage_source === 'derived'
export interface Requirement {
  node_id: string; project_node_id: string; kind: 'functional' | 'nonfunctional'; revision: number
  status: 'draft' | 'agreed' | 'superseded'; title: string; feature_node_id: string | null; generated_ticket_ids: string[]
}
export type ReleaseState = 'planning' | 'building' | 'candidate' | 'deploying' | 'refused' | 'access' | 'released' | 'superseded'
export interface WalkerFeature { feature_node_id: string; epic_key: string; title: string; selection: 'empty' | 'none' | 'some' | 'all'; included_count: number; open_count: number }
export interface WalkerTicket {
  ticket_node_id: string; key: string; title: string; feature_node_id: string | null; included: boolean; position: number
  estimated_hours: number | null; screen_node_ids: string[]
}
export interface Walker { release_node_id: string; project_node_id: string; state: ReleaseState; revision: number; features: WalkerFeature[]; tickets: WalkerTicket[] }
export interface PlanWrite { expected_revision: number; ordered_ticket_ids: string[]; included_ticket_ids: string[] }
export interface IntakeSource {
  id: string; project_node_id: string; kind: 'url' | 'file' | 'note' | 'conversation'; label: string; locator?: string; file_id?: string
  content_sha256: string; created_at: string
}
export interface IntakeTurn { id: string; source_id: string; ordinal: number; speaker: 'person' | 'agent'; speaker_principal_id: string; body: string; created_at: string }
export interface Citation { source_id: string; turn_id?: string; locator: string }
export interface TicketSuggestion { title: string; estimated_hours: string | number; later: boolean; access_change: boolean }
export interface IntakeDraft {
  id: string; kind: 'brief' | 'requirement'; requirement_kind?: Requirement['kind']; target_node_id?: string; title: string; body: string
  base_event_id: number; citations: Citation[]; ticket_suggestions: TicketSuggestion[]; status: 'proposed' | 'accepted' | 'rejected'
  proposed_at: string; accepted_at: string | null
}
export interface Intake { sources: IntakeSource[]; turns: IntakeTurn[]; drafts: IntakeDraft[] }
export interface Handoff {
  id: string; project_node_id: string; release_node_id: string; stage: 'deploy' | 'access'; operation: 'prepare' | 'apply' | 'deploy' | 'verify'
  plugin_id: string; attempt: number; authority_epoch: number; state: 'requested' | 'active' | 'blocked' | 'succeeded' | 'failed' | 'revoked'
  expires_at: string; result?: { outcome: 'succeeded' | 'failed'; blocker_code?: string; completed_at: string }
}

// ---------- Requests ----------
async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(path, { method, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    const text = typeof data?.error === 'string' ? data.error : typeof data?.message === 'string' ? data.message : `Request failed (${response.status})`
    throw new APIError(response.status, text, data && typeof data === 'object' ? data : {})
  }
  return response.status === 204 ? undefined as T : response.json()
}
const enc = encodeURIComponent
const root = (project: string) => `/projects/${enc(project)}`
const releaseRoot = (project: string, release: string) => `${root(project)}/releases/${enc(release)}`
export const getJourney = (project: string) => request<Journey>(`${root(project)}/journey`)
export const putProfile = (project: string, profile: Profile, expected_revision: number) => request<Journey>(`${root(project)}/journey/profile`, 'PUT', { profile, expected_revision })
export interface ActionWrite { action: ActionKey; expected_revision: number; idempotency_key: string; approval_request_id?: string | null; release_id?: string | null; reason?: string | null }
export const postAction = (project: string, body: ActionWrite) => request<Journey>(`${root(project)}/journey/actions`, 'POST', body)
export const getRequirements = (project: string) => request<Requirement[]>(`${root(project)}/requirements`)
export const addRequirement = (project: string, body: { kind: Requirement['kind']; title: string; body: string; expected_revision: number; idempotency_key: string }) =>
  request<Requirement>(`${root(project)}/requirements`, 'POST', body)
export const agreeRequirements = (project: string, body: { expected_revision: number; approval_request_id: string; idempotency_key: string }) =>
  request<Requirement[]>(`${root(project)}/requirements/agree`, 'POST', body)
export const getWalker = (project: string, release: string) => request<Walker>(`${releaseRoot(project, release)}/walker`)
export const putPlan = (project: string, release: string, body: PlanWrite) => request<Walker>(`${releaseRoot(project, release)}/plan`, 'PUT', body)
export const getIntake = (project: string) => request<Intake>(`${root(project)}/intake`)
export const acceptDraft = (project: string, draft: IntakeDraft) =>
  request<IntakeDraft>(`${root(project)}/intake/drafts/${enc(draft.id)}/accept`, 'POST', { expected_base_event_id: draft.base_event_id })
export const getHandoff = (id: string) => request<Handoff>(`/stage-handoffs/${enc(id)}`)
// A ticket added while planning joins the release (or the backlog) under a
// feature, or under none. Proposed route; older servers answer 404 or 405.
export interface TicketAdd { title: string; feature_node_id: string | null; included: boolean; expected_revision: number; idempotency_key: string }
export const addPlanTicket = (project: string, release: string, body: TicketAdd) => request<Walker>(`${releaseRoot(project, release)}/tickets`, 'POST', body)

// Releases are R1 nodes of the release kind under the project (history and the
// backfilled releases); the journey's current release is one of them.
export async function listReleases(project: string): Promise<WorkNode[]> {
  const { items: kinds } = await getKinds()
  const kind = kinds.find(k => k.slug === 'release')
  if (!kind) return []
  const items: WorkNode[] = []
  let cursor: string | undefined
  for (let page = 0; page < 20; page++) {
    const result = await request<{ items: WorkNode[]; next_cursor: string | null }>(`/nodes?parent_id=${enc(project)}&include_descendants=true&kind_id=${enc(kind.id)}&sort=created_at&limit=200${cursor ? `&cursor=${enc(cursor)}` : ''}`)
    items.push(...result.items)
    cursor = result.next_cursor ?? undefined
    if (!cursor) break
  }
  return items
}
// Every piece of work in the project, with its epic, state and assignee (for the
// walker's feature line, the build's progress and ticket details).
export async function listWork(project: string): Promise<ListItem[]> {
  const items: ListItem[] = []
  let cursor: string | undefined
  for (let page = 0; page < 12; page++) {
    const result = await listNodes({ within: project, kind: ['epic', 'ticket', 'task'], limit: 500, sort: 'key', cursor })
    items.push(...result.items)
    cursor = result.next_cursor ?? undefined
    if (!cursor) break
  }
  return items
}

// Stage plugins (Pharos deploys, Janus grants access) and the gates of their steps.
export interface PluginInfo {
  id: string; owner: string; installation?: { enabled: boolean } | null
  workflow_steps: { key: string; gates: string[] }[] | null; integrations: { id: string; permission: string }[] | null
}
export const listPlugins = () => request<PluginInfo[]>('/plugins')
export const PLUGIN_GATE: Record<string, string> = {
  artifact_identity: 'Artifact identity', backup_ready: 'Backup ready', readiness: 'Host readiness', launch_admission: 'Launch admission',
  person_decision: 'Your decision', prerequisite_seal: 'Prerequisite seal', observed_state: 'Observed state', deployment_succeeded: 'Deployment succeeded',
  bounded_permit: 'Bounded permit',
}

// ---------- Releases: number, name and order ----------
export interface ReleaseRef { id: string; key: string; title: string; state: string; created_at: string; number: number; version: string | null }
// Journey releases are titled "Release N"; backfilled ones carry their version as
// the title. Numbers count releases in the order they were created.
export function releaseRefs(nodes: WorkNode[]): ReleaseRef[] {
  const sorted = [...nodes].sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at) || a.key.localeCompare(b.key))
  return sorted.map((node, index) => {
    const named = /^Release (\d+)$/i.exec(node.title.trim())
    return { id: node.id, key: node.key, title: node.title, state: node.state, created_at: node.created_at, number: named ? Number(named[1]) : index + 1, version: named ? null : node.title.trim() }
  })
}
export const CALENDAR_VERSION = /^v?[1-9]\d(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])([01]\d|2[0-3])[0-5]\d[0-5]\d\.0\.0$/
export const releaseName = (release: Pick<ReleaseRef, 'number'>) => `Release ${release.number}`
// Work that was finished (done, accepted, delivered), apart from work that was
// dropped (cancelled, archived): progress counts the first and leaves out the second.
export const isDropped = (state: string) => ['cancelled', 'archived'].includes(statusMeta(state).key)
export const isFinished = (state: string) => statusMeta(state).closed && !isDropped(state)
// A release node's state in release words: an imported release kept its classic
// status (done, backlog, …), and "Backlog" would read as unscheduled work.
export function releaseStateLabel(state: string): string {
  const s = state.toLowerCase().replace(/[\s-]+/g, '_')
  if (s === 'done' || s === 'released' || s === 'closed') return 'Released'
  if (s === 'cancelled' || s === 'canceled') return 'Cancelled'
  if (s === 'in_progress' || s === 'active' || s === 'building') return 'In progress'
  if (s === 'qa') return 'In QA'
  return 'Planned'
}
export const RELEASE_STATE_LABEL: Record<ReleaseState, string> = {
  planning: 'Planning', building: 'Building', candidate: 'Candidate', deploying: 'Deploying', refused: 'Refused', access: 'Access', released: 'Live', superseded: 'Superseded',
}

// ---------- Features and tickets (the walker) ----------
export interface FeatureRef { id: string; key: string; title: string; derived: boolean }
export interface TicketGroup { id: string; feature: FeatureRef | null; tickets: WalkerTicket[] }
export const orderedTickets = (walker: Pick<Walker, 'tickets'>) => [...walker.tickets].sort((a, b) => a.position - b.position || a.key.localeCompare(b.key))
// Features come from the walker; tickets it does not tie to one (imported or
// added by hand) group under their epic in the work list, when they have one.
export function ticketGroups(walker: Pick<Walker, 'features' | 'tickets'>, epicOf: (ticketId: string) => { id: string; key: string; title: string } | null = () => null): TicketGroup[] {
  const tickets = orderedTickets(walker)
  const groups = new Map<string, TicketGroup>()
  for (const feature of walker.features) groups.set(feature.feature_node_id, { id: feature.feature_node_id, feature: { id: feature.feature_node_id, key: feature.epic_key, title: feature.title, derived: false }, tickets: [] })
  const loose: WalkerTicket[] = []
  for (const ticket of tickets) {
    const id = ticket.feature_node_id ?? epicOf(ticket.ticket_node_id)?.id ?? null
    if (id && !groups.has(id)) {
      const epic = epicOf(ticket.ticket_node_id)
      if (epic) groups.set(id, { id, feature: { id, key: epic.key, title: epic.title, derived: true }, tickets: [] })
    }
    if (id && groups.has(id)) groups.get(id)!.tickets.push(ticket)
    else loose.push(ticket)
  }
  // Features in the order of their first ticket; empty features after them; then the loose tickets.
  const out = [...groups.values()].sort((a, b) => {
    const pa = a.tickets[0]?.position ?? Infinity, pb = b.tickets[0]?.position ?? Infinity
    return pa - pb
  })
  if (loose.length) out.push({ id: '', feature: null, tickets: loose })
  return out
}
// The walker's order: grouped by feature, then by position within the feature.
export const walkOrder = (groups: TicketGroup[]) => groups.flatMap(group => group.tickets)

// Tri-state: all, some or none of a feature's open tickets are in the release.
export type Selection = 'empty' | 'none' | 'some' | 'all'
export function selectionOf(tickets: WalkerTicket[], included: Set<string>): Selection {
  if (!tickets.length) return 'empty'
  const n = tickets.filter(t => included.has(t.ticket_node_id)).length
  return n === 0 ? 'none' : n === tickets.length ? 'all' : 'some'
}
// The next pick for a feature: some → all → none → the last partial pick (then all again).
export function nextPick(tickets: WalkerTicket[], included: Set<string>, remembered?: string[]): Set<string> {
  const state = selectionOf(tickets, included)
  const next = new Set(included)
  const ids = tickets.map(t => t.ticket_node_id)
  if (state === 'all') ids.forEach(id => next.delete(id))
  else if (state === 'none' && remembered?.length) ids.forEach(id => remembered.includes(id) ? next.add(id) : next.delete(id))
  else ids.forEach(id => next.add(id))
  return next
}
export function nextPickLabel(state: Selection, remembered?: string[]) {
  return state === 'all' ? 'defer all to the backlog' : state === 'none' && remembered?.length ? `restore the earlier ${remembered.length}` : 'include all'
}
// The plan write: every eligible ticket in the given order, and the included subset.
export function planWrite(walker: Walker, order: WalkerTicket[], included: Set<string>): PlanWrite {
  return { expected_revision: walker.revision, ordered_ticket_ids: order.map(t => t.ticket_node_id), included_ticket_ids: order.filter(t => included.has(t.ticket_node_id)).map(t => t.ticket_node_id) }
}
export const hours = (value: number | null | undefined) => value == null ? '' : `${Number.isInteger(value) ? value : value.toFixed(1)} h`

// ---------- Gates ----------
export type Gate = 'shape' | 'requirements' | 'build' | 'candidate' | 'deploy' | 'access'
export const GATE_OF_ACTION: Partial<Record<NextKey, Gate>> = {
  decide: 'shape', reopen: 'shape', approve_requirements: 'requirements', start_build: 'build', mark_candidate: 'build', approve_candidate: 'candidate',
  approve_deploy: 'deploy', retry_deploy: 'deploy', approve_permit: 'access',
}
export const GATE_OF_STAGE: Record<Stage, Gate | null> = {
  inspire: null, shape: 'shape', requirements: 'requirements', plan: 'build', build: 'candidate', deploy: 'deploy', access: 'access', live: null,
}
export const GATE_LABEL: Record<Gate, string> = {
  shape: 'Shape gate', requirements: 'Requirements gate', build: 'Build gate', candidate: 'Candidate gate', deploy: 'Deployment gate', access: 'Access gate',
}
// Project gates (shape, requirements) sit on the project; the others on the current release.
export const gateOnRelease = (gate: Gate) => !['shape', 'requirements'].includes(gate)
// Approvals an agent requested for a gate. The requirements gate is scoped to the
// revision and digest being agreed (journey.requirements.r<rev>.d<digest>).
export function gateApprovals(approvals: Approval[], gate: Gate, resourceId: string | null): Approval[] {
  if (!resourceId) return []
  const scope = `journey.${gate}`
  return approvals
    .filter(a => a.resource_kind === 'node' && a.resource_id === resourceId && (a.scope === scope || a.scope.startsWith(`${scope}.`)))
    .sort((a, b) => Date.parse(b.proposed_at) - Date.parse(a.proposed_at))
}
// The approval the next action uses: the one the projection offers, or for the
// requirements gate the newest live one scoped to the revision being agreed.
export function offeredApproval(approvals: Approval[], journey: Journey, gate: Gate, now: number): Approval | null {
  const resource = gateOnRelease(gate) ? journey.current_release_id : journey.project_node_id
  const candidates = gateApprovals(approvals, gate, resource).filter(a => a.decision !== 'denied' && Date.parse(a.expires_at) > now)
  if (gate === 'requirements') {
    // The server names the exact scope (revision and content digest); older servers do not.
    if (journey.requirements_approval_scope) {
      const exact = candidates.filter(a => a.scope === journey.requirements_approval_scope)
      return exact.find(a => a.decision === 'approved') ?? exact[0] ?? null
    }
    const refined = candidates.filter(a => a.scope.startsWith('journey.requirements.r'))
    const exact = refined.filter(a => a.scope.startsWith(`journey.requirements.r${journey.revision}.`))
    const pool = exact.length ? exact : refined
    return pool.find(a => a.decision === 'approved') ?? pool[0] ?? null
  }
  const offered = journey.next_action.approval_request_id
  return (offered ? candidates.find(a => a.id === offered) : null) ?? candidates.find(a => a.decision === 'approved') ?? candidates[0] ?? null
}

// ---------- Copy ----------
// What each next action means, in the prototype's words.
export const ACTION_LONG: Record<NextKey, string> = {
  continue_intake: 'Aithema turns the conversation and sources into the brief. Nothing is decided yet.',
  confirm_brief: 'Aithema drafted the brief from the sources. Confirm it; the lenses then check business, market, reuse, compliance and risk.',
  decide: 'Compare the estimate with the budget, then go, reduce scope, park or drop. The decision is recorded.',
  reopen: 'This project is parked or dropped with its reason. Reopen it to decide again.',
  approve_requirements: 'Agreed requirements become features with tickets. Non-functional ones become knowledge entries and acceptance criteria.',
  open_first_release: 'Opens release 1 for planning. Tickets are then ticked into it; the rest stay in the backlog.',
  start_build: 'The ticked tickets form the release. The crew builds them; unticked tickets stay in the backlog.',
  wait_for_build: 'Agents are working on the release. You approve the release candidate when it is ready.',
  mark_candidate: 'Every ticket of the release is done. Marking it as the candidate hands it to review.',
  approve_candidate: 'Check the preview. Approving hands the release to deployment.',
  approve_deploy: 'Backup evidence and the build are recorded, then the host applies the release.',
  retry_deploy: 'The host refused the release. Retry with fresh evidence, or send the candidate back.',
  approve_permit: 'Grants the bounded permit to the people who use it. Every use is recorded. Then it is live.',
  plan_next_release: 'Backlog tickets and new input form the next release. The agreed requirements stay; tickets that revise them say so.',
}
export const STAGE_LATER: Record<Stage, string> = {
  inspire: 'Everything starts here. Aithema turns it into the brief.',
  shape: 'The brief and the decision.',
  requirements: 'Agreeing what will be built. Agreed requirements become tickets.',
  plan: 'Choosing the tickets of the release.',
  build: 'Agents build the release; you approve the candidate.',
  deploy: 'The host applies the release after your approval.',
  access: 'Who may use it.',
  live: 'The release runs and you plan the next one.',
}
export const BLOCKER_LABEL: Record<string, string> = {
  dependency_pending: 'A required step is still pending', dependency_failed: 'A required step failed', reporter_stale: 'The reporter went quiet',
  external_waiting: 'Waiting on the host', policy_refused: 'The host policy refused it',
}
