// SPDX-License-Identifier: AGPL-3.0-only
// Single adapter for P1/P2/P3 and protected AEON-455/436/524 integrations.
import { readKeyTrims, decideKeyTrim, type KeyTrimProposal } from './keyTrim'
import { api, APIError, getNode, getProjects, getRelations, lookupNodeKeys, type WorkNode } from './api'
import { listApprovals, decideApproval, listMessages, resolveMessage, type Approval, type ProjectMessage, type HarnessSession } from './agents'
import { canDecideApproval, decidedApprovals } from './agentState'
import { getDoctrineInbox, submitDoctrineInbox, dismissDoctrineInbox, type DoctrineInboxItem } from './doctrine'
import { listAttachments, type Attachment } from './attachments'
import { loadTicketOutcomes, type OutcomeEvent } from './ticketOutcomes'
import { decidePhone, phoneRequest, reviewPath, type PhoneReview } from './deskPhoneApproval'
import { readTierResponse, decideTierResponse } from './agentRows'
import { answerFor, CUSTOM_ANSWER, type DeskChoice, type DeskDraft, type DeskItem, type DeskOutcome } from './decisionDesk'

export interface QuestionInput {
  request_id: string; question: string; context?: string; findings?: string; options: DeskChoice[]
  recommend?: string; why?: string; meanwhile: 'carries_on' | 'parked' | 'paused' | 'stopped'; meanwhile_text?: string
  ticket_id?: string; source_handover_id?: string; suggested_outcome?: DeskOutcome
  source_request_id?: string
}
export interface QuestionAnswer {
  id: string; revision: number; answer: string; option_id?: string; reason?: string; outcome: DeskOutcome
  decided_by: string; created_at: string; deliver_after: string; replaces?: string
}
export interface Question {
  id: string; project_id: string; revision: number; state: 'open' | 'answered'; input: QuestionInput
  suggested_outcome: DeskOutcome; suggestion_reason: 'agent_suggestion' | 'ticket_default' | 'project_default'
  askers: { id: string; principal_id: string; reply_root_id: string; comment_node_id: string; input: QuestionInput; from_record?: { label: 'From the record'; decision_id: string; revision: number } }[]
  answer?: QuestionAnswer; created_at: string; updated_at: string
  pending: { id: string; revision: number; kind: 'inbox' | 'comment' | 'outcome'; state: 'pending' | 'delivered' | 'failed' | 'replaced'; deliver_after: string; receipt_state?: 'queued' | 'handed_off' | 'failed'; error_code?: string }[]
}
export interface QuestionPage { items: Question[]; has_more: boolean }
export async function deskRequest<T>(path: string, body?: unknown): Promise<T> {
  const response = await api(path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!response.ok) {
    const error = await response.json().catch(() => ({}))
    throw new APIError(response.status, error.error ?? `Request failed (${response.status})`, error)
  }
  return response.json() as Promise<T>
}
export type QuestionState = 'open' | 'answered'
export const MAX_QUESTION_PAGES = 10
export const readQuestions = (offset = 0, state: QuestionState = 'open') => deskRequest<QuestionPage>(`/decision-desk?state=${state}&limit=100&offset=${offset}`)
async function readQuestionPages(state: QuestionState, pages: number): Promise<QuestionPage> {
  const items: Question[] = []
  let has_more = false
  for (let page = 0; page < Math.min(MAX_QUESTION_PAGES, Math.max(1, pages)); page++) {
    const result = await readQuestions(page * 100, state)
    items.push(...result.items); has_more = result.has_more
    if (!has_more) break
  }
  return { items, has_more }
}
export function deliveryTime(value?: string): string {
  const timestamp = value ? Date.parse(value) : NaN
  return Number.isFinite(timestamp) ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(timestamp) : 'an unavailable time'
}
export const readQuestion = (id: string) => deskRequest<Question>(`/questions/${encodeURIComponent(id)}`)
export const decideQuestion = (id: string, revision: number, draft: DeskDraft, requestId: string) => deskRequest<Question>(`/questions/${encodeURIComponent(id)}/decision`, {
  request_id: requestId, expected_revision: revision, outcome: draft.outcome,
  ...(draft.optionId === CUSTOM_ANSWER ? { answer: draft.answer.trim() } : { option_id: draft.optionId }), reason: draft.reason.trim(),
})
export function questionItem(question: Question, projectName: string): DeskItem {
  const pending = question.pending.filter(effect => effect.revision === question.revision && effect.kind !== 'outcome')
  const delivery = pending.some(effect => effect.state === 'failed' || effect.receipt_state === 'failed') ? 'Delivery failed; the answer is recorded.'
    : pending.some(effect => effect.state === 'pending') ? `Delivery scheduled for ${deliveryTime(question.answer?.deliver_after ?? pending[0]?.deliver_after)}`
    : pending.some(effect => effect.receipt_state === 'handed_off') ? 'Handed to the agent.'
    : pending.some(effect => effect.state === 'delivered') ? 'Dispatched; receiver confirmation may still be pending.' : undefined
  const reuse = question.askers.find(asker => asker.from_record)?.from_record
  return {
    id: `q:${question.id}`, kind: question.input.source_handover_id ? 'handover' : 'question', projectId: question.project_id, projectName,
    ticketId: question.input.ticket_id, title: question.input.question, context: question.input.context ?? '', findings: question.input.findings ?? '',
    meanwhile: question.input.meanwhile_text || question.input.meanwhile.replaceAll('_', ' '),
    destination: `${question.askers.length} asker${question.askers.length === 1 ? '' : 's'} · inbox or verified successor · ${question.input.ticket_id ? 'ticket comment' : 'question record'}`,
    choices: [...question.input.options, { id: CUSTOM_ANSWER, title: 'Something else', description: 'Write the answer in your own words.', answer: '', field: true }],
    recommended: question.input.recommend, why: question.input.why ?? '', outcome: question.answer?.outcome ?? question.suggested_outcome,
    suggestion: question.suggestion_reason === 'agent_suggestion' ? 'Suggested by the agent.' : question.suggestion_reason === 'ticket_default' ? 'This question belongs to a ticket.' : `For ${projectName}, from now on.`,
    revision: question.revision, createdAt: question.created_at, held: question.input.meanwhile !== 'carries_on', decided: question.state === 'answered',
    answer: question.answer?.answer, optionId: question.answer ? question.answer.option_id || CUSTOM_ANSWER : undefined, reason: question.answer?.reason, delivery: question.answer?.replaces ? `Correction of the earlier answer. ${delivery ?? 'Delivery state is not supplied.'}` : delivery,
    fromRecord: reuse ? `${reuse.label} · revision ${reuse.revision}` : undefined,
  }
}
function base(id: string, kind: DeskItem['kind'], title: string, context: string, choices: DeskChoice[]): DeskItem {
  return { id, kind, title, context, choices, projectId: '', projectName: 'Workspace', findings: '', meanwhile: 'Waiting for a person.', destination: 'The native protected workflow.',
    why: '', outcome: 'once', suggestion: 'This action keeps its native permissions.', revision: 1, createdAt: '', held: true, decided: false }
}
export function approvalItem(approval: Approval, projectName = approval.resource_kind === 'tenant' ? 'Workspace' : 'Project name unavailable'): DeskItem {
  return { ...base(`a:${approval.id}`, 'approval', `Allow ${approval.scope}?`, approval.rationale, [
    { id: 'approved', title: 'Approve', description: 'Grant this request through the approvals API.', answer: 'Approved' },
    { id: 'denied', title: 'Deny', description: 'Keep the permission unchanged.', answer: 'Denied' },
  ]), projectName, ticketId: approval.resource_kind === 'node' ? approval.resource_id ?? undefined : undefined, createdAt: approval.proposed_at, expiresAt: approval.expires_at, decided: decidedApprovals([approval], Date.now()).length > 0, optionId: approval.decision ?? undefined, answer: approval.decision ?? (Date.parse(approval.expires_at) <= Date.now() ? 'Expired without a decision' : undefined) }
}
export function actionItem(message: ProjectMessage, projectId: string, projectName: string): DeskItem {
  return { ...base(`m:${message.id}`, 'action', 'An agent needs your steer', message.body, [
    { id: 'reply', title: 'Reply to the agent', description: 'Steer the work with an inbox reply.', answer: '', field: true },
    { id: 'resolved', title: 'I did it myself', description: 'Resolve the request.', answer: 'Resolved' },
    { id: 'dismissed', title: 'Not needed', description: 'Dismiss the request.', answer: 'Dismissed' },
  ]), projectId, projectName, createdAt: message.created_at ?? '', destination: 'Reply to the original request, or its native Resolve / Dismiss action.' }
}
export function ruleItem(rule: DoctrineInboxItem): DeskItem {
  return { ...base(`r:${rule.id}`, 'rule', rule.label, rule.why ?? '', [
    { id: 'propose', title: 'Propose the change', description: 'Open the existing doctrine pull request flow.', answer: 'Propose the change' },
    { id: 'dismiss', title: 'Not now', description: 'Return a reason to the proposer.', answer: 'Not now' },
  ]), prUrl: rule.pr_url || undefined, createdAt: rule.created_at, findings: rule.proposed ?? '', ticketKey: rule.ticket, destination: 'Doctrine inbox, reviewed pull request, release, then pin.' }
}
export function keyTrimItem(proposal: KeyTrimProposal): DeskItem {
  const pending = proposal.state === 'pending'
  return { ...base(`k:${proposal.id}`, 'key_trim', `Trim ${proposal.key_name}?`, proposal.evidence.summary, []),
    projectName: 'Workspace', revision: proposal.revision, createdAt: proposal.created_at,
    expiresAt: pending ? proposal.expires_at : undefined, decided: !pending, keyTrim: proposal,
    answer: pending ? undefined : proposal.state === 'applied' ? 'Trim approved' : proposal.state === 'restored' ? 'Previous scopes restored' : proposal.state === 'expired' ? 'Expired without a decision' : 'Trim declined',
    unavailable: pending ? proposal.blocked_reason : undefined,
    meanwhile: 'The key keeps its current permissions until a person approves.',
    destination: 'This key only. Approval and restore recheck live scopes and permissions and record an audit event.',
    suggestion: 'Approve applies this exact scope reduction. Decline keeps the current set.',
    delivery: proposal.state === 'applied' ? 'Trim applied. Restore is available for 30 days, subject to current scopes and grant authority.' : proposal.state === 'restored' ? 'The previous scope set was restored.' : undefined,
  }
}
export interface DeskSources { questions: Map<string, Question>; approvals: Map<string, Approval>; actions: Map<string, ProjectMessage>; rules: Map<string, DoctrineInboxItem>; keyTrims: Map<string, KeyTrimProposal> }
export const emptySources = (): DeskSources => ({ questions: new Map(), approvals: new Map(), actions: new Map(), rules: new Map(), keyTrims: new Map() })
/** Upstream owners supply their native, permission-checked flows here. No
 * guessed endpoints, boolean verification bypass, or Q&A fallback is accepted. */
export interface ProtectedDeskAdapters {
  // AEON-455 owns user verification and the approval write together.
  phoneApproval?: (approval: Approval, decision: 'approved' | 'denied', reason: string) => Promise<void>
  // AEON-436's exact generation, revision and native permission contract.
  tiers?: { read: () => Promise<{ items: DeskItem[]; warnings: string[] }>; allowed: (item: DeskItem) => boolean; decide: (item: DeskItem, draft: DeskDraft, requestId: string) => Promise<DeskItem> }
}
interface TierRequest { id: string; session_id: string; tier: 'default' | 'fast' | 'fastest'; reason: string; state: 'pending' | 'approved' | 'declined'; created_at: string }
interface SessionTier { session_id: string; revision: number; read_only: boolean; read_only_reason?: string; requests: TierRequest[] }
async function tierBody(response: Response): Promise<SessionTier> {
  if (!response.ok) { const error = await response.json().catch(() => ({})); throw new APIError(response.status, error.error || 'Tier requests are unavailable.', error) }
  return response.json()
}
export function nativeTierAdapter(sessions: () => Promise<readonly HarnessSession[]>, can: (permission: string, project?: string) => boolean): NonNullable<ProtectedDeskAdapters['tiers']> {
  let records = new Map<string, { session: HarnessSession; tier: SessionTier; request: TierRequest }>()
  return {
    async read() {
      const available = (await sessions()).filter(session => session.advertised_capabilities.includes('service_tier_v1'))
      const warnings: string[] = available.length > 30 ? ['Tier requests are limited to the first 30 visible sessions.'] : []
      const next = new Map<string, { session: HarnessSession; tier: SessionTier; request: TierRequest }>()
      const reads = await Promise.allSettled(available.slice(0, 30).map(async session => ({ session, tier: await tierBody(await readTierResponse(session.project_id, session.id)) })))
      const items: DeskItem[] = []
      for (const read of reads) {
        if (read.status === 'rejected') continue
        const { session, tier } = read.value
        if (tier.requests.length > 100) warnings.push('Only the first 100 tier requests per session are shown.')
        for (const request of tier.requests.slice(0, 100)) {
          const id = `t:${request.id}`; next.set(id, { session, tier, request })
          items.push({ ...base(id, 'tier', `Use ${request.tier === 'fastest' ? 'Fastest' : request.tier === 'fast' ? 'Fast' : 'Default'} for this session?`, request.reason, [
            { id: 'approve', title: 'Approve the tier request', description: 'Use the native ownership and tier revision checks.', answer: 'Tier request approved' },
            { id: 'decline', title: 'Keep the current tier', description: 'Decline this request through the tier API.', answer: 'Tier request declined' },
          ]), projectId: session.project_id, projectName: session.project?.title || 'Project name unavailable', ticketId: session.ticket_node_id || undefined,
            revision: tier.revision, createdAt: request.created_at, decided: request.state !== 'pending', optionId: request.state === 'pending' ? undefined : request.state === 'approved' ? 'approve' : 'decline',
            answer: request.state === 'pending' ? undefined : request.state, unavailable: tier.read_only ? tier.read_only_reason || 'This session is read-only.' : !session.process_ownership ? 'The exact process ownership is unavailable.' : undefined,
            destination: 'A protected tier control for the original session. Applied at the next safe point.' })
        }
      }
      records = next
      if (reads.some(read => read.status === 'rejected')) warnings.push('Some tier requests could not be read. Open Agents to inspect their source.')
      return { items, warnings }
    },
    allowed: item => can('harness.control', item.projectId),
    async decide(item, draft, requestId) {
      const source = records.get(item.id)
      if (!source || source.session.project_id !== item.projectId || source.tier.revision !== item.revision || source.request.state !== 'pending' || source.tier.read_only || !source.session.process_ownership || !can('harness.control', item.projectId)) throw new Error('The tier request changed or is no longer actionable.')
      const result = await tierBody(await decideTierResponse(item.projectId, source.session.id, source.request.id, { request_id: requestId, tier: source.request.tier,
        expected_revision: source.tier.revision, expected_ownership: { ...source.session.process_ownership }, decision: draft.optionId === 'approve' ? 'approve' : 'decline' }))
      if (!result.requests.some(request => request.id === source.request.id && request.state === (draft.optionId === 'approve' ? 'approved' : 'declined'))) throw new Error('The tier decision was not confirmed.')
      return { ...item, decided: true, optionId: draft.optionId, answer: answerFor(item, draft), delivery: draft.optionId === 'approve' ? 'Tier change recorded; application waits for the next safe point.' : 'Tier request declined.' }
    },
  }
}
export async function nativePhoneApproval(approval: Approval, decision: 'approved' | 'denied', reason: string) {
  let review: PhoneReview
  try { review = await phoneRequest<PhoneReview>(reviewPath('approval', approval.id)) }
  catch (cause) { if (cause instanceof APIError && cause.status === 404) throw new Error('Phone verification is not available from this server.'); throw cause }
  const current = review.approval
  if (!review.pending || review.request_id !== approval.id || !current || current.id !== approval.id || current.scope !== approval.scope
    || current.resource_kind !== approval.resource_kind || current.resource_id !== approval.resource_id || current.rationale !== approval.rationale || current.expires_at !== approval.expires_at) {
    throw new Error('The approval changed or expired. Reopen it before verifying.')
  }
  // The native ceremony binds both the person and decision to request_hash.
  const result = await decidePhone(review, { decision, reason, request_hash: review.request_hash })
  if (result.pending || result.approval?.decision !== decision) throw new Error('The verified decision was not confirmed.')
}
export interface DeskRead { items: DeskItem[]; sources: DeskSources; warnings: string[]; hasMore: Record<QuestionState, boolean> }
export async function loadDesk(pages = { open: 1, answered: 1 }, adapters: ProtectedDeskAdapters = {}): Promise<DeskRead> {
  const sources = emptySources(), warnings: string[] = [], items: DeskItem[] = []
  const [projectsResult, openResult, answeredResult, approvalsResult, rulesResult, trimsResult] = await Promise.allSettled([getProjects(), readQuestionPages('open', pages.open), readQuestionPages('answered', pages.answered), listApprovals(), getDoctrineInbox(), readKeyTrims()])
  const projects = projectsResult.status === 'fulfilled' ? projectsResult.value.items : []
  if (projectsResult.status === 'rejected') warnings.push('Project names unavailable; source access could not be confirmed.')
  const name = (id: string) => projects.find(project => project.id === id)?.title ?? 'Project name unavailable'
  for (const result of [openResult, answeredResult]) {
    if (result.status === 'fulfilled') for (const question of result.value.items) { sources.questions.set(`q:${question.id}`, question); items.push(questionItem(question, name(question.project_id))) }
    else warnings.push('Questions could not be read. They may be inaccessible; this is not an empty desk.')
  }
  if (approvalsResult.status === 'fulfilled') {
    if (approvalsResult.value.length >= 200) warnings.push('Approvals may be incomplete: the source limit is 200 requests.')
    // Reuse the ticket ancestry contract, bounded and cached within this read.
    const nodes = new Map<string, Promise<WorkNode>>()
    const readNode = (id: string) => { if (!nodes.has(id)) nodes.set(id, getNode(id)); return nodes.get(id)! }
    const nodeIds = [...new Set(approvalsResult.value.filter(row => row.resource_kind === 'node').map(row => row.resource_id).filter((id): id is string => !!id))].slice(0, 30)
    const names = new Map<string, string>()
    await Promise.all(nodeIds.map(async resource => {
      let id: string | null = resource
      try {
        for (let depth = 0; id && depth < 8; depth++) {
          const project = projects.find(project => project.id === id)
          if (project) { names.set(resource, project.title); break }
          id = (await readNode(id)).parent_id
        }
      } catch { /* A missing name never becomes a fictitious Workspace. */ }
    }))
    for (const approval of approvalsResult.value.slice(0, 200)) { sources.approvals.set(`a:${approval.id}`, approval); items.push(approvalItem(approval, approval.resource_id ? names.get(approval.resource_id) : undefined)) }
  } else warnings.push('Approvals could not be read.')
  if (rulesResult.status === 'fulfilled') for (const rule of rulesResult.value.items) { sources.rules.set(`r:${rule.id}`, rule); items.push(ruleItem(rule)) }
  else warnings.push('Rule changes could not be read.')
  if (projects.length > 30) warnings.push('Action requests are limited to the first 30 visible projects in this read.')
  const actionReads = await Promise.allSettled(projects.slice(0, 30).map(async project => ({ project, page: await listMessages(project.id, { pending: true, limit: 100 }) })))
  for (const read of actionReads) {
    if (read.status === 'rejected') { warnings.push('Some action requests could not be read.'); continue }
    if (read.value.page.items.length === 100) warnings.push(`Action requests for ${read.value.project.title} may be incomplete.`)
    const projected = new Set([...sources.questions.values()].map(question => question.input.source_request_id).filter(Boolean))
    for (const message of read.value.page.items.filter(message => message.is_action_request && message.status === 'held' && !message.human_resolution_outcome && !projected.has(message.id))) {
      const item = actionItem(message, read.value.project.id, read.value.project.title); sources.actions.set(item.id, message); items.push(item)
    }
  }
  if (trimsResult.status === 'fulfilled') {
    warnings.push(...trimsResult.value.warnings)
    for (const proposal of trimsResult.value.items) { const item = keyTrimItem(proposal); sources.keyTrims.set(item.id, proposal); items.push(item) }
  } else warnings.push('Key trim proposals could not be read; key management rights are required.')
  if (adapters.tiers) {
    try { const read = await adapters.tiers.read(); items.push(...read.items.slice(0, 100).map(item => ({ ...item, kind: 'tier' as const }))); warnings.push(...read.warnings); if (read.items.length > 100) warnings.push('Only the first 100 tier requests are shown.') }
    catch { warnings.push('Tier requests could not be read.') }
  }
  return { items, sources, warnings: [...new Set(warnings)], hasMore: { open: openResult.status === 'fulfilled' && openResult.value.has_more, answered: answeredResult.status === 'fulfilled' && answeredResult.value.has_more } }
}
export function decisionPermission(item: DeskItem, sources: DeskSources, can: (permission: string, project?: string) => boolean, adapters: ProtectedDeskAdapters = {}): boolean {
  if (item.kind === 'question' || item.kind === 'handover') return can('questions.read', item.projectId) && can('questions.decide', item.projectId)
  if (item.kind === 'key_trim') return can('keys.manage')
  if (item.kind === 'approval') { const approval = sources.approvals.get(item.id); return !!approval && canDecideApproval(approval, can) }
  if (item.kind === 'action') return can('inbox.manage')
  if (item.kind === 'rule') return can('rules.write')
  return item.kind === 'tier' && adapters.tiers?.allowed(item) === true
}
export async function commitDesk(item: DeskItem, draft: DeskDraft, sources: DeskSources, requestId: string, phone: boolean, adapters: ProtectedDeskAdapters = {}): Promise<DeskItem> {
  if (item.kind === 'key_trim') {
    const source = sources.keyTrims.get(item.id)
    if (!source || !item.keyTrim || source.key_id !== item.keyTrim.key_id || source.revision !== item.revision || source.state !== item.keyTrim.state) throw new Error('The key trim source changed. Reopen the memo.')
    const decision = draft.optionId
    if (decision !== 'approve' && decision !== 'decline' && decision !== 'restore') throw new Error('Choose a key trim action.')
    if (source.blocked_reason && decision !== 'decline') throw new Error(source.blocked_reason)
    const updated = await decideKeyTrim(source, decision, requestId)
    sources.keyTrims.set(item.id, updated)
    return keyTrimItem(updated)
  }
  if (item.kind === 'question' || item.kind === 'handover') {
    const question = sources.questions.get(item.id)
    if (!question || question.revision !== item.revision) throw new Error('The question changed. Reopen it before deciding.')
    const updated = await decideQuestion(question.id, item.revision, draft, requestId)
    sources.questions.set(item.id, updated)
    return questionItem(updated, item.projectName)
  }
  if (item.kind === 'approval') {
    const approval = sources.approvals.get(item.id)
    if (!approval) throw new Error('The approval is no longer accessible.')
    if (phone) {
      if (item.decided) throw new Error('This protected decision cannot be replaced here.')
      await (adapters.phoneApproval ?? nativePhoneApproval)(approval, draft.optionId === 'approved' ? 'approved' : 'denied', draft.reason.trim())
      return { ...item, decided: true, answer: answerFor(item, draft), optionId: draft.optionId, reason: draft.reason, delivery: 'Recorded by the verified approval workflow.' }
    }
    if (item.decided) throw new Error('This protected decision cannot be replaced here.')
    await decideApproval(approval.id, draft.optionId === 'approved' ? 'approved' : 'denied', draft.reason.trim())
  } else if (item.kind === 'action') {
    const source = sources.actions.get(item.id)
    if (!source) throw new Error('The original request is unavailable.')
    if (draft.optionId === 'reply') {
      // P3 preserves the exact original request and generation. Never choose a newer session.
      const reply = await deskRequest<{ status: 'accepted' | 'held' | 'pending'; question_id?: string; answer_revision?: number; deliver_after?: string }>(`/projects/${encodeURIComponent(item.projectId)}/messages`, { to: source.sender_principal_id, body: answerFor(item, draft), idempotency_key: requestId, reply_to: source.id,
        ...(source.sender_session_id ? { recipient_session_id: source.sender_session_id } : {}), expects_reply: false, is_action_request: false, delivery_level: 'simple' })
      if (reply.status === 'held') throw new Error('The reply remains held. Delivery was not confirmed.')
      if (reply.status === 'pending' && (!reply.question_id || !reply.answer_revision || !reply.deliver_after)) throw new Error('The pending reply response is incomplete. Refresh before trying again.')
      return { ...item, decided: true, answer: answerFor(item, draft), optionId: draft.optionId, reason: draft.reason,
        delivery: reply.status === 'pending' ? `Reply recorded; delivery scheduled for ${deliveryTime(reply.deliver_after)}.` : 'Reply accepted by the inbox; receiver confirmation is pending.' }
    } else await resolveMessage(item.projectId, source.id, draft.optionId === 'resolved' ? 'resolved' : 'dismissed', draft.reason.trim())
  } else if (item.kind === 'rule') {
    if (draft.optionId === 'propose') await submitDoctrineInbox(item.id.slice(2))
    else await dismissDoctrineInbox(item.id.slice(2), draft.reason.trim())
  } else if (item.kind === 'tier' && adapters.tiers?.allowed(item)) return adapters.tiers.decide(item, draft, requestId)
  else throw new Error('The protected tier workflow is not connected yet (AEON-436).')
  return { ...item, decided: true, answer: answerFor(item, draft), optionId: draft.optionId, reason: draft.reason, delivery: 'Recorded by the native workflow.' }
}
export interface DeskContext { ticket?: WorkNode; attachments: Attachment[]; related: WorkNode[]; outcomes: OutcomeEvent[]; warnings: string[] }
export async function loadDeskContext(ticketId: string, ticketKey?: string): Promise<DeskContext> {
  if (ticketKey) {
    try {
      const lookup = await lookupNodeKeys([ticketKey])
      const node = lookup.items.find(node => node.key === ticketKey || node.requested_key === ticketKey)
      if (!node) throw new Error('Ticket not found')
      ticketId = node.id
    } catch { return { attachments: [], related: [], outcomes: [], warnings: ['Ticket context is unavailable or inaccessible.'] } }
  }
  const [ticket, attachments, relations, outcomes] = await Promise.allSettled([getNode(ticketId), listAttachments(ticketId), getRelations(ticketId), loadTicketOutcomes(ticketId)])
  const warnings: string[] = []
  if (ticket.status === 'rejected') warnings.push('Ticket context is unavailable or inaccessible.')
  if (attachments.status === 'rejected') warnings.push('Attachments are unavailable or inaccessible.')
  if (relations.status === 'rejected') warnings.push('Related records are unavailable or inaccessible.')
  if (outcomes.status === 'rejected') warnings.push('PR / check evidence is unavailable or inaccessible.')
  const ids = relations.status === 'fulfilled' ? [...new Set(relations.value.items.map(relation => relation.source_node_id === ticketId ? relation.target_node_id : relation.source_node_id))].slice(0, 20) : []
  if (relations.status === 'fulfilled' && (relations.value.next_cursor || relations.value.items.length > 20)) warnings.push('Only the first 20 related records are shown.')
  const related = await Promise.allSettled(ids.map(id => getNode(id)))
  if (related.some(read => read.status === 'rejected')) warnings.push('Some related records are inaccessible.')
  return { ticket: ticket.status === 'fulfilled' ? ticket.value : undefined, attachments: attachments.status === 'fulfilled' ? attachments.value : [],
    related: related.flatMap(read => read.status === 'fulfilled' ? [read.value] : []), outcomes: outcomes.status === 'fulfilled' ? outcomes.value : [], warnings }
}
