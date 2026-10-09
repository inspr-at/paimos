// SPDX-License-Identifier: AGPL-3.0-only
// Single adapter for P1/P2/P3 and protected AEON-455/436/524 integrations.
import { readKeyTrims, decideKeyTrim, type KeyTrimProposal, type KeyTrimCursors } from './keyTrim'
import { approveStepup, declineStepup, readStepups, stepupAnswer, stepupDelivery, stepupTitle, type StepupDevices, type StepupRequest } from './stepup'
import { api, APIError, getNode, getProjects, getRelations, lookupNodeKeys, type WorkNode } from './api'
import { listApprovals, decideApproval, listMessages, resolveMessage, type Approval, type ProjectMessage, type HarnessSession } from './agents'
import { canDecideApproval, decidedApprovals } from './agentState'
import { getDoctrineInbox, submitDoctrineInbox, dismissDoctrineInbox, type DoctrineInboxItem } from './doctrine'
import { listAttachments, type Attachment } from './attachments'
import { loadTicketOutcomes, type OutcomeEvent } from './ticketOutcomes'
import { decidePhone, phoneRequest, reviewPath, type PhoneReview } from './deskPhoneApproval'
import { readTierResponse, decideTierResponse } from './agentRows'
import { answerFor, CUSTOM_ANSWER, deskItemID, type DeskChoice, type DeskDraft, type DeskItem, type DeskOutcome, type DeskProjection, type DeskProjectionItem, type DeskOutcomeAvailability, type DeskDoctrineTarget, type DeskOutcomeEffect } from './decisionDesk'

export interface QuestionInput {
  request_id: string; question: string; context?: string; findings?: string; options: DeskChoice[]
  recommend?: string; why?: string; meanwhile: 'carries_on' | 'parked' | 'paused' | 'stopped'; meanwhile_text?: string
  ticket_id?: string; source_handover_id?: string; suggested_outcome?: DeskOutcome
  source_request_id?: string; doctrine?: DeskDoctrineTarget
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
  outcomes?: DeskOutcomeAvailability[]; pending: DeskOutcomeEffect[]
}
export interface QuestionPage { items: Question[]; has_more: boolean; next_cursor?: string }
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
export type QuestionQuery = { state: 'open'; offset?: number } | { state: 'answered'; cursor?: string }
export const readQuestions = (query: QuestionQuery) => deskRequest<QuestionPage>(query.state === 'answered'
  ? `/decision-desk?state=answered&order=desc&limit=100${query.cursor ? `&cursor=${encodeURIComponent(query.cursor)}` : ''}`
  : `/decision-desk?state=open&limit=100&offset=${query.offset ?? 0}`)
interface QuestionPosition { pages: number; nextCursor?: string; cursors: string[] }
interface QuestionPages extends QuestionPage { position: QuestionPosition }
function pagePosition(state: QuestionState, result: QuestionPage, previous: QuestionPosition): QuestionPosition {
  if (result.items.length > 100) throw new Error('The question page exceeded its limit. Refresh before loading more.')
  const cursors = [...previous.cursors]
  if (state === 'answered' && result.has_more) {
    if (!result.next_cursor || result.next_cursor.length > 512 || cursors.includes(result.next_cursor)) throw new Error('The answered page cursor is unavailable. Refresh before loading more.')
    cursors.push(result.next_cursor)
  }
  return { pages: previous.pages + 1, nextCursor: state === 'answered' && result.has_more ? result.next_cursor : undefined, cursors }
}
async function readQuestionPages(state: QuestionState, pages: number): Promise<QuestionPages> {
  const items = new Map<string, Question>()
  let has_more = false, position: QuestionPosition = { pages: 0, cursors: [] }
  for (let page = 0; page < Math.min(MAX_QUESTION_PAGES, Math.max(1, pages)); page++) {
    const result = await readQuestions(state === 'answered' ? { state, cursor: position.nextCursor } : { state, offset: page * 100 })
    position = pagePosition(state, result, position)
    result.items.forEach(question => items.set(question.id, question)); has_more = result.has_more
    if (!has_more) break
  }
  return { items: [...items.values()], has_more, position }
}
export function deliveryTime(value?: string): string {
  const timestamp = value ? Date.parse(value) : NaN
  return Number.isFinite(timestamp) ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(timestamp) : 'an unavailable time'
}
export const readQuestion = (id: string) => deskRequest<Question>(`/questions/${encodeURIComponent(id)}`)
export const readApproval = (id: string) => deskRequest<Approval>(`/approvals/${encodeURIComponent(id)}`)
export const decideQuestion = (id: string, revision: number, draft: DeskDraft, requestId: string, doctrine?: DeskDoctrineTarget) => deskRequest<Question>(`/questions/${encodeURIComponent(id)}/decision`, {
  request_id: requestId, expected_revision: revision, outcome: draft.outcome,
  ...(draft.outcome === 'doctrine' && doctrine ? { doctrine: { ...doctrine } } : {}),
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
    id: `q:${question.id}`, source: `/api/questions/${encodeURIComponent(question.id)}`, kind: question.input.source_handover_id ? 'handover' : 'question', projectId: question.project_id, projectName,
    ticketId: question.input.ticket_id, title: question.input.question, context: question.input.context ?? '', findings: question.input.findings ?? '',
    meanwhile: question.input.meanwhile_text || question.input.meanwhile.replaceAll('_', ' '),
    destination: `${question.askers.length} asker${question.askers.length === 1 ? '' : 's'} · inbox or verified successor · ${question.input.ticket_id ? 'ticket comment' : 'question record'}`,
    choices: [...question.input.options, { id: CUSTOM_ANSWER, title: 'Something else', description: 'Write the answer in your own words.', answer: '', field: true }],
    recommended: question.input.recommend, why: question.input.why ?? '', outcome: question.answer?.outcome ?? question.suggested_outcome,
    suggestion: question.suggestion_reason === 'agent_suggestion' ? 'Suggested by the agent.' : question.suggestion_reason === 'ticket_default' ? 'This question belongs to a ticket.' : `For ${projectName}, from now on.`,
    revision: question.revision, createdAt: question.created_at, held: question.input.meanwhile !== 'carries_on', decided: question.state === 'answered',
    answer: question.answer?.answer, optionId: question.answer ? question.answer.option_id || CUSTOM_ANSWER : undefined, reason: question.answer?.reason, delivery: question.answer?.replaces ? `Correction of the earlier answer. ${delivery ?? 'Delivery state is not supplied.'}` : delivery,
    outcomes: question.outcomes?.map(row => ({ ...row })), doctrine: question.input.doctrine ? { ...question.input.doctrine } : undefined,
    outcomeEffects: question.pending.filter(effect => effect.revision === question.revision && effect.kind === 'outcome').map(effect => ({ ...effect, effect_data: effect.effect_data ? { ...effect.effect_data, review_required: effect.effect_data.review_required?.map(review => ({ ...review })) } : undefined })),
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
  ]), approval: { ...approval }, source: `/api/approvals/${encodeURIComponent(approval.id)}`, projectName, ticketId: approval.resource_kind === 'node' ? approval.resource_id ?? undefined : undefined, createdAt: approval.proposed_at, expiresAt: approval.expires_at, decided: decidedApprovals([approval], Date.now()).length > 0, optionId: approval.decision ?? undefined, answer: approval.decision ?? (Date.parse(approval.expires_at) <= Date.now() ? 'Expired without a decision' : undefined) }
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
    { id: 'propose', title: 'Propose the change', description: 'Open the existing doctrine pull request flow.', answer: 'Propose the change', unavailable: rule.outdated ? 'The rule changed since this was proposed. Edit against the current rule first.' : undefined },
    { id: 'dismiss', title: 'Not now', description: 'Return a reason to the proposer.', answer: 'Not now' },
  ]), rule: { ...rule }, prUrl: rule.pr_url || undefined, createdAt: rule.created_at, findings: rule.proposed ?? '', ticketKey: rule.ticket, destination: 'Doctrine inbox, reviewed pull request, release, then pin.' }
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
export function stepupItem(request: StepupRequest, projectName = request.project_id ? 'Project name unavailable' : 'Workspace'): DeskItem {
  const pending = request.state === 'pending'
  return { ...base(`s:${request.id}`, 'stepup', stepupTitle(request, projectName), 'An agent asked for this one protected change. Its own key cannot make it.', []),
    projectId: request.project_id ?? '', projectName, revision: request.revision, createdAt: request.created_at,
    expiresAt: pending ? request.expires_at : undefined, decided: !pending, stepup: request, answer: stepupAnswer(request),
    findings: `Needs ${request.permission} · ${projectName}\nUsed once; expires 15 minutes after the request.\nBound to this change (sha256):\n${request.request_digest}`,
    meanwhile: 'The agent waits for this one change.',
    destination: 'This change only, applied once as you after your passkey or a fresh sign-in. The agent keeps its key and scopes as they are.',
    suggestion: 'Approve asks for your passkey or a fresh sign-in, then applies exactly this change, once.',
    delivery: stepupDelivery(request),
  }
}
export interface DeskSources { questions: Map<string, Question>; approvals: Map<string, Approval>; actions: Map<string, ProjectMessage>; rules: Map<string, DoctrineInboxItem>; keyTrims: Map<string, KeyTrimProposal>; stepups: Map<string, StepupRequest> }
export const emptySources = (): DeskSources => ({ questions: new Map(), approvals: new Map(), actions: new Map(), rules: new Map(), keyTrims: new Map(), stepups: new Map() })
/** Upstream owners supply their native, permission-checked flows here. No
 * guessed endpoints, boolean verification bypass, or Q&A fallback is accepted. */
export interface ProtectedDeskAdapters {
  // AEON-455 owns user verification and the approval write together.
  phoneApproval?: (approval: Approval, decision: 'approved' | 'denied', reason: string) => Promise<void>
  // AEON-436's exact generation, revision and native permission contract.
  tiers?: { read: (pending?: readonly DeskProjectionItem[]) => Promise<{ items: DeskItem[]; warnings: string[] }>; allowed: (item: DeskItem) => boolean; decide: (item: DeskItem, draft: DeskDraft, requestId: string) => Promise<DeskItem> }
  // AEON-1048: the browser's passkey prompt and navigation, injectable for tests.
  stepup?: StepupDevices
}
interface TierRequest { id: string; session_id: string; tier: 'default' | 'fast' | 'fastest'; reason: string; state: 'pending' | 'approved' | 'declined'; created_at: string }
interface SessionTier { session_id: string; revision: number; read_only: boolean; read_only_reason?: string; requests: TierRequest[] }
async function tierBody(response: Response): Promise<SessionTier> {
  const maxBytes = 2 * 1024 * 1024
  if (Number(response.headers.get('Content-Length')) > maxBytes) { await response.body?.cancel(); throw new Error('Tier response is too large.') }
  if (!response.body) throw new Error('Tier response is empty.')
  const reader = response.body.getReader(), decoder = new TextDecoder()
  let bytes = 0, text = ''
  try {
    for (;;) {
      const chunk = await reader.read()
      if (chunk.done) break
      bytes += chunk.value.byteLength
      if (bytes > maxBytes) { await reader.cancel(); throw new Error('Tier response is too large.') }
      text += decoder.decode(chunk.value, { stream: true })
    }
    text += decoder.decode()
  } finally { reader.releaseLock() }
  const body = JSON.parse(text)
  if (!response.ok) throw new APIError(response.status, body.error || 'Tier requests are unavailable.', body)
  return body
}
export function nativeTierAdapter(sessions: () => Promise<readonly HarnessSession[]>, can: (permission: string, project?: string) => boolean, detail?: (project: string, id: string) => Promise<HarnessSession | undefined>): NonNullable<ProtectedDeskAdapters['tiers']> {
  let records = new Map<string, { session: HarnessSession; tier: SessionTier; request: TierRequest }>()
  return {
    async read(pending = []) {
      const available = (await sessions()).filter(session => session.advertised_capabilities.includes('service_tier_v1'))
      const warnings: string[] = available.length > 30 ? ['Tier history is limited to the first 30 visible sessions; canonical pending requests are read separately.'] : []
      const selected = new Map<string, { project: string; session: HarnessSession | undefined }>(available.slice(0, 30).map(session => [session.id, { project: session.project_id, session }]))
      const targets = new Map<string, string>()
      // Source paths identify the exact session; never execute a supplied URL.
      // All reads use the native permission-checked API and admitted ledger.
      const canonical = pending.filter(row => row.kind === 'tier_request')
      if (canonical.length > 1000) warnings.push('Only 1,000 canonical tier requests can be hydrated in one read.')
      for (const row of canonical.slice(0, 1000)) {
        const match = /^\/api\/projects\/([a-zA-Z0-9-]{1,80})\/harness-sessions\/([a-zA-Z0-9-]{1,80})\/tier$/.exec(row.source)
        if (!match || match[1] !== row.project_id) { warnings.push('A tier source mapping could not be confirmed. Retry before deciding.'); continue }
        targets.set(match[2]!, match[1]!)
      }
      for (const [id, project] of targets) if (!selected.has(id)) {
        const known = available.find(session => session.id === id && session.project_id === project)
        selected.set(id, { project, session: known })
      }
      const next = new Map<string, { session: HarnessSession; tier: SessionTier; request: TierRequest }>()
      const entries = [...selected.entries()], reads: PromiseSettledResult<{ session: HarnessSession; tier: SessionTier }>[] = []
      for (let at = 0; at < entries.length; at += 5) reads.push(...await Promise.allSettled(entries.slice(at, at + 5).map(async ([id, target]) => {
        const session = target.session ?? await detail?.(target.project, id)
        if (!session || session.id !== id || session.project_id !== target.project || !session.advertised_capabilities.includes('service_tier_v1')) throw new Error('Tier session identity could not be confirmed.')
        const tier = await tierBody(await readTierResponse(target.project, id))
        if (tier.session_id !== id || !Number.isSafeInteger(tier.revision) || tier.revision < 0 || !Array.isArray(tier.requests)) throw new Error('Tier source identity could not be confirmed.')
        return { session, tier }
      })))
      const items: DeskItem[] = []
      for (const read of reads) {
        if (read.status === 'rejected') continue
        const { session, tier } = read.value
        const requests = [...tier.requests.filter(request => request.state === 'pending'), ...tier.requests.filter(request => request.state !== 'pending')]
        if (requests.length > 100) warnings.push('Only the first 100 tier requests per session are shown, with pending requests first.')
        for (const request of requests.slice(0, 100)) {
          if (request.session_id !== session.id) { warnings.push('A tier request belongs to a different session. Retry before deciding.'); continue }
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
export interface DeskRead {
  items: DeskItem[]; sources: DeskSources; warnings: string[]; hasMore: Record<QuestionState, boolean>
  questionPositions: Record<QuestionState, QuestionPosition | undefined>; projectNames: Map<string, string>
  nextKeyTrims: KeyTrimCursors
  canonical?: boolean
}
/** Continue only the requested list. Refresh uses loadDesk to recheck all
 * sources from the beginning; callers keep this snapshot bound to its owner.
 * Read the latest snapshot after the request so recorded decisions survive. */
export async function loadMoreQuestions(previous: DeskRead, state: QuestionState, latest: () => DeskRead = () => previous): Promise<DeskRead> {
  const position = previous.questionPositions[state]
  if (!previous.hasMore[state] || !position || position.pages >= MAX_QUESTION_PAGES) return previous
  const result = await readQuestions(state === 'answered' ? { state, cursor: position.nextCursor } : { state, offset: position.pages * 100 })
  const nextPosition = pagePosition(state, result, position)
  const current = latest()
  const sources = { ...current.sources, questions: new Map(current.sources.questions), actions: new Map(current.sources.actions) }
  const items = new Map(current.items.filter(item => item.kind === 'question' || item.kind === 'handover').map(item => [item.id, item]))
  for (const question of result.items) {
    const item = questionItem(question, current.projectNames.get(question.project_id) ?? 'Project name unavailable')
    sources.questions.set(item.id, question); items.set(item.id, item)
  }
  if (current.canonical) {
    const seen = new Set(current.items.map(item => item.id))
    // History pagination must retain the server's open membership and order.
    // Native Open pages can hydrate existing records, but cannot add new ones.
    return { ...current, sources, items: [
      ...current.items.map(item => items.get(item.id) ?? item),
      ...[...items.values()].filter(item => item.decided && !seen.has(item.id)),
    ], hasMore: { ...current.hasMore, [state]: result.has_more },
    questionPositions: { ...current.questionPositions, [state]: nextPosition } }
  }
  const projected = new Set([...sources.questions.values()].flatMap(question => [question.input.source_request_id, ...question.askers.map(asker => asker.input.source_request_id)]).filter(Boolean))
  for (const [id, message] of sources.actions) if (projected.has(message.id)) sources.actions.delete(id)
  const protectedItems = current.items.filter(item => item.kind !== 'question' && item.kind !== 'handover' && !(item.kind === 'action' && projected.has(item.id.slice(2))))
  return { ...current, sources, items: [...items.values(), ...protectedItems], hasMore: { ...current.hasMore, [state]: result.has_more },
    questionPositions: { ...current.questionPositions, [state]: nextPosition } }
}
export async function loadDesk(pages = { open: 1, answered: 1 }, adapters: ProtectedDeskAdapters = {}, trimCursorsOrProjection: KeyTrimCursors | DeskProjection | null = {}, suppliedProjection?: DeskProjection | null): Promise<DeskRead> {
  const projection = trimCursorsOrProjection === null || 'items' in trimCursorsOrProjection ? trimCursorsOrProjection : suppliedProjection
  const trimCursors: KeyTrimCursors = trimCursorsOrProjection && !('items' in trimCursorsOrProjection) ? trimCursorsOrProjection : {}
  const sources = emptySources(), warnings: string[] = [], items: DeskItem[] = []
  const [projectsResult, openResult, answeredResult, approvalsResult, rulesResult, trimsResult, stepupsResult] = await Promise.allSettled([getProjects(), readQuestionPages('open', pages.open), readQuestionPages('answered', pages.answered), listApprovals(), getDoctrineInbox(), readKeyTrims(trimCursors), readStepups()])
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
    const projected = new Set([...sources.questions.values()].flatMap(question => [question.input.source_request_id, ...question.askers.map(asker => asker.input.source_request_id)]).filter(Boolean))
    for (const message of read.value.page.items.filter(message => message.is_action_request && message.status === 'held' && !message.human_resolution_outcome && !projected.has(message.id))) {
      const item = actionItem(message, read.value.project.id, read.value.project.title); sources.actions.set(item.id, message); items.push(item)
    }
  }
  if (trimsResult.status === 'fulfilled') {
    warnings.push(...trimsResult.value.warnings)
    for (const proposal of trimsResult.value.items) { const item = keyTrimItem(proposal); sources.keyTrims.set(item.id, proposal); items.push(item) }
  } else warnings.push('Key trim proposals could not be read; key management rights are required.')
  if (stepupsResult.status === 'fulfilled') {
    warnings.push(...stepupsResult.value.warnings)
    for (const request of stepupsResult.value.items) { const item = stepupItem(request, request.project_id ? name(request.project_id) : 'Workspace'); sources.stepups.set(item.id, request); items.push(item) }
  } else warnings.push('Step-up approvals could not be read.')
  if (adapters.tiers) {
    try {
      const read = await adapters.tiers.read(projection?.items)
      const pending = read.items.filter(item => !item.decided), history = read.items.filter(item => item.decided)
      items.push(...[...pending, ...history.slice(0, 100)].map(item => ({ ...item, kind: 'tier' as const })))
      warnings.push(...read.warnings)
      if (history.length > 100) warnings.push('Only the first 100 tier history records are shown; pending requests have a separate budget.')
    }
    catch { warnings.push('Tier requests could not be read.') }
  }
  // Canonical membership suppresses every linked source, even beyond the
  // question pages loaded for history. One source has one position and count.
  if (projection !== undefined) {
    const history = items.filter(item => item.decided), byID = new Map(items.map(item => [item.id, item]))
    const missing = (projection?.items ?? []).filter(row => ['question', 'approval', 'action_request'].includes(row.kind) && !byID.has(deskItemID(row)))
    if (missing.length > 100) warnings.push('Only 100 additional source details are read at once. Some items need more source pages before review.')
    // Hydrate canonical sources outside their first page in bounded batches,
    // through their permission-checked native reads.
    for (let at = 0; at < Math.min(100, missing.length); at += 5) {
      const reads = await Promise.allSettled(missing.slice(at, at + 5).map(async row => {
        if (row.kind === 'approval') {
          const approval = await readApproval(row.id)
          if (approval.id !== row.id) throw new Error('Approval identity changed')
          const item = { ...approvalItem(approval, row.project_id ? name(row.project_id) : 'Workspace'), projectId: row.project_id ?? '' }
          sources.approvals.set(item.id, approval); return item
        }
        if (row.kind === 'action_request' && row.project_id) {
          const page = await listMessages(row.project_id, { thread: row.id, pending: true, newest_first: false, limit: 100 })
          const message = page.items.find(message => message.id === row.id && message.is_action_request && message.status === 'held' && !message.human_resolution_outcome)
          if (!message) throw new Error('Held request changed')
          const item = actionItem(message, row.project_id, name(row.project_id)); sources.actions.set(item.id, message); return item
        }
        const question = await readQuestion(row.id)
        if (question.id !== row.id) throw new Error('Question identity changed')
        const item = questionItem(question, name(question.project_id)); sources.questions.set(item.id, question); return item
      }))
      for (const read of reads) if (read.status === 'fulfilled') {
        byID.set(read.value.id, read.value)
      }
    }
    const open = (projection?.items ?? []).map(row => {
      const id = deskItemID(row), item = byID.get(id)
      if (item && !item.decided) return { ...item, held: row.held, source: row.source,
        unavailable: row.can_decide === false ? 'This person may read this request but cannot decide it.' : item.unavailable }
      warnings.push('Some source details could not be confirmed. Retry remains available.')
      return { ...base(id, row.kind === 'question' ? 'question' : row.kind === 'action_request' ? 'action' : row.kind === 'doctrine' ? 'rule' : row.kind === 'tier_request' ? 'tier' : row.kind === 'key_trim' ? 'key_trim' : row.kind === 'stepup' ? 'stepup' : 'approval', row.title, '', []),
        projectId: row.project_id ?? '', projectName: row.project_id ? name(row.project_id) : 'Workspace', revision: row.revision, createdAt: row.created_at,
        held: row.held, source: row.source, unavailable: 'Source details are unavailable or changed. Close and Retry before deciding.' }
    })
    if (!projection) warnings.push('The canonical desk could not be read. Retry before deciding; the count is unknown.')
    else if (projection.has_more || open.length !== projection.counts.open) warnings.push('The canonical desk is partial or changed while reading. Retry for current coverage.')
    items.splice(0, items.length, ...open, ...history.filter(item => !open.some(row => row.id === item.id)))
  }
  return { items, sources, warnings: [...new Set(warnings)], hasMore: { open: openResult.status === 'fulfilled' && openResult.value.has_more, answered: answeredResult.status === 'fulfilled' && answeredResult.value.has_more },
    questionPositions: { open: openResult.status === 'fulfilled' ? openResult.value.position : undefined, answered: answeredResult.status === 'fulfilled' ? answeredResult.value.position : undefined },
    projectNames: new Map(projects.map(project => [project.id, project.title])), nextKeyTrims: trimsResult.status === 'fulfilled' ? trimsResult.value.next : {}, canonical: projection !== undefined }
}
export function decisionPermission(item: DeskItem, sources: DeskSources, can: (permission: string, project?: string) => boolean, adapters: ProtectedDeskAdapters = {}): boolean {
  if (item.kind === 'question' || item.kind === 'handover') return can('questions.read', item.projectId) && can('questions.decide', item.projectId)
  if (item.kind === 'key_trim') return can('keys.manage')
  if (item.kind === 'stepup') return !!item.stepup && can(item.stepup.permission, item.stepup.project_id)
  if (item.kind === 'approval') { const approval = sources.approvals.get(item.id); return !!approval && canDecideApproval(approval, can) }
  if (item.kind === 'action') return can('inbox.manage', item.projectId)
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
  if (item.kind === 'stepup') {
    const source = sources.stepups.get(item.id)
    if (!source || !item.stepup || source.request_digest !== item.stepup.request_digest || source.revision !== item.revision || source.state !== 'pending' || item.decided) throw new Error('The step-up request changed. Reopen the memo.')
    if (draft.optionId !== 'approve' && draft.optionId !== 'decline') throw new Error('Choose Approve or Decline.')
    const updated = draft.optionId === 'approve' ? await approveStepup(source, adapters.stepup) : await declineStepup(source, adapters.stepup?.signal)
    sources.stepups.set(item.id, updated)
    return stepupItem(updated, item.projectName)
  }
  if (item.kind === 'question' || item.kind === 'handover') {
    const question = sources.questions.get(item.id)
    if (!question || question.revision !== item.revision) throw new Error('The question changed. Reopen it before deciding.')
    const updated = await decideQuestion(question.id, item.revision, draft, requestId, item.doctrine)
    sources.questions.set(item.id, updated)
    return questionItem(updated, item.projectName)
  }
  if (item.kind === 'approval') {
    const approval = sources.approvals.get(item.id)
    if (!approval) throw new Error('The approval is no longer accessible.')
    if (phone) {
      if (item.decided) throw new Error('This protected decision cannot be replaced here.')
      await (adapters.phoneApproval ?? nativePhoneApproval)(approval, draft.optionId === 'approved' ? 'approved' : 'denied', draft.reason.trim())
      sources.approvals.set(item.id, { ...approval, decision: draft.optionId === 'approved' ? 'approved' : 'denied' })
      return { ...item, decided: true, answer: answerFor(item, draft), optionId: draft.optionId, reason: draft.reason, delivery: 'Recorded by the verified approval workflow.' }
    }
    if (item.decided) throw new Error('This protected decision cannot be replaced here.')
    const expected = draft.optionId === 'approved' ? 'approved' : 'denied'
    const updated = await decideApproval(approval.id, expected, draft.reason.trim())
    if (updated.id !== approval.id || updated.decision !== expected) throw new Error('The native approval decision was not confirmed. Refresh before trying again.')
    sources.approvals.set(item.id, { ...approval, ...updated })
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
    if (sources.rules.get(item.id)?.outdated && draft.optionId === 'propose') throw new Error('The rule changed since this was proposed. Edit against the current rule first.')
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
