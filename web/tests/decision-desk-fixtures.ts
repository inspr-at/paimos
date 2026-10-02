// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import type { Question } from '../src/lib/decisionDeskApi'
import { fixtures, mockWork, PNG } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'

export function sampleQuestion(id = 'question-1', input: Partial<Question['input']> = {}): Question {
  return {
    id, project_id: 'p-aeon', revision: 1, state: 'open', suggested_outcome: 'once', suggestion_reason: 'ticket_default',
    created_at: '2026-10-02T08:00:00Z', updated_at: '2026-10-02T08:00:00Z',
    input: { request_id: id, question: 'Which migration should carry the index?', context: 'The ticket asks for fast lookup without changing tenant isolation.', findings: 'A partial index keeps the common path small.\nThe existing query already filters by tenant.',
      options: [{ id: 'partial', title: 'Use the partial index', description: 'Keep the common path small.', answer: 'Use the partial index.' }, { id: 'full', title: 'Use a full index', description: 'Cover every row.', answer: 'Use a full index.' }], recommend: 'partial', why: 'It matches the query that runs most often.', meanwhile: 'parked', meanwhile_text: 'The agent is writing the read tests.', ticket_id: 'n-a1', ...input },
    askers: [{ id: 'asker-1', principal_id: 'agent-1', reply_root_id: 'root-1', comment_node_id: 'n-a1', input: { request_id: id, question: 'Which index?', options: [], meanwhile: 'parked' } }], pending: [],
  }
}
export async function mockDecisionDesk(page: Page, options: { denied?: boolean; long?: boolean; short?: boolean; theme?: 'light' | 'dark'; tier?: boolean; phone?: boolean; html?: boolean } = {}) {
  const data = fixtures()
  data.projects.find(project => project.id === 'p-aeon')!.title = 'Paimos Aeon'
  if (options.theme) data.preferences.theme = { choice: options.theme }
  const ticket = data.nodes.find(node => node.id === 'n-a1')!
  ticket.fields.acceptance_criteria = 'Preserve tenant isolation.\nUse bounded queries.'
  ticket.fields.pr_url = 'https://github.com/inspr-at/paimos/pull/181'
  await mockWork(page, data)
  const q = sampleQuestion()
  if (options.short) { q.input.ticket_id = undefined; q.input.findings = ''; q.input.context = 'The existing migration can carry this index.' }
  if (options.long) q.input.context = Array(70).fill('Long background: the agent compared the index with the existing tenant-scoped query.').join('\n')
  const questions = [q, sampleQuestion('question-2', { question: 'Should the successor continue the review?', source_handover_id: 'handover-1' })]
  const future = new Date(Date.now() + 3600_000).toISOString()
  const approval = { id: 'approval-1', agent_principal_id: 'agent-1', agent_name: 'Codex', scope: 'nodes.read', resource_kind: 'node', resource_id: 'n-a1', rationale: 'Read the ticket to continue the review.', expires_at: future, proposed_at: '2026-10-02T08:01:00Z', decision: null, risk: 'low' }
  const action = { id: 'action-1', sender_principal_id: 'd2c76909-751c-4408-82bf-d8e7a2495509', recipient_principal_id: 'person', to: 'person', body: 'The reviewer needs guidance before trying again.', sender_session_id: 'original-generation', sent_event_id: 1, is_action_request: true, expects_reply: true, delivery_level: 'simple', status: 'held', reply_obligation: 'open', created_at: '2026-10-02T08:02:00Z' }
  const rule = { id: 'rule-1', label: 'Keep mutation checks in the transaction', why: 'The earlier permission check can become stale.', proposed: 'Authorize inside the mutation transaction.', base: 'Authorize before the write.', created_at: '2026-10-02T08:03:00Z', ticket: 'AEON-1', pr_url: 'https://github.com/inspr-at/inspr-modules/pull/123', state: 'pending' }
  const ownership = { daemon_id: 'daemon-1', generation: 'generation-1', process_id: 'process-1', root_pid: 1234, group_id: 1234, started_at: '2026-10-02T08:00:00Z' }
  const tierRequest = { id: 'tier-request-1', session_id: 'tier-session', tier: 'default', reason: 'Return this run to the default tier.', state: 'pending', created_at: '2026-10-02T08:04:00Z' }
  const tier = { session_id: 'tier-session', revision: 2, read_only: false, active_tier: 'default', pending: null, reports: [], requests: [tierRequest] }
  const calls: { path: string; body: Record<string, unknown> }[] = []
  const reads: string[] = []
  const control = { questions, calls, reads, denyPermission: !!options.denied, denyWrite: false, failQuestions: false, denyContext: false, hold: undefined as undefined | Promise<void>, approval, action, rule, ownership, tier }
  await page.route('**/api/**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname, method = request.method()
    if (method === 'GET') reads.push(new URL(request.url()).pathname + new URL(request.url()).search)
    if (path === '/api/me/phone-approvals') return route.fulfill({ status: options.phone ? 200 : 404, json: options.phone ? { available: true } : { error: 'Not found' } })
    if (path === '/api/me/permissions') {
      const permissions = mockEffectivePermissions('admin', new URL(request.url()).searchParams.get('project_id') ?? undefined)
      permissions.workspace.permissions.push('questions.read', 'questions.decide', 'rules.write')
      if (control.denyPermission) {
        permissions.workspace.permissions = permissions.workspace.permissions.filter(permission => permission !== 'questions.decide')
        if (permissions.project) permissions.project.permissions = permissions.project.permissions.filter(permission => permission !== 'questions.decide')
      }
      return route.fulfill({ json: permissions })
    }
    if (path === '/api/decision-desk') {
      const params = new URL(request.url()).searchParams, state = params.get('state'), offset = Number(params.get('offset') || 0)
      const selected = questions.filter(question => !state || question.state === state)
      return route.fulfill({ status: control.failQuestions ? 403 : 200, json: control.failQuestions ? { error: 'Forbidden' } : { items: selected.slice(offset, offset + 100), has_more: selected.length > offset + 100 } })
    }
    if (options.tier && path === '/api/harness-sessions') return route.fulfill({ json: { items: [{ id: 'tier-session', project_id: 'p-aeon', agent_principal_id: 'agent-1', ticket_node_id: 'n-a1', harness: 'codex', host: 'test', phase: 'working', activity: 'busy', heartbeat_at: new Date().toISOString(), created_at: '2026-10-02T08:00:00Z', management_mode: 'managed', advertised_capabilities: ['managed_control_v1', 'service_tier_v1'], process_ownership: ownership, revision: 1, row_version: 1, project: { id: 'p-aeon', key: 'AEON', title: 'Paimos Aeon' } }], next_cursor: null } })
    if (options.tier && path.endsWith('/tier')) return route.fulfill({ json: tier })
    if (options.tier && path.endsWith('/tier/requests/tier-request-1/decision')) {
      const body = request.postDataJSON(); calls.push({ path, body })
      if (control.denyWrite) return route.fulfill({ status: 409, json: { error: 'Process ownership changed. Reopen the request.' } })
      tierRequest.state = body.decision === 'approve' ? 'approved' : 'declined'; tier.revision++
      return route.fulfill({ json: tier })
    }
    if (options.phone && path.startsWith('/api/phone-approvals/approval/approval-1')) {
      if (method === 'GET') return route.fulfill({ json: { kind: 'approval', request_id: approval.id, request_hash: 'bound-request-hash', pending: true, approval } })
      const body = request.postDataJSON(); calls.push({ path, body })
      if (path.endsWith('/options')) return route.fulfill({ json: { challenge_id: 'challenge-bound-to-choice', publicKey: { challenge: 'AQID', rpId: '127.0.0.1', userVerification: 'required' } } })
      return route.fulfill({ json: { kind: 'approval', request_id: approval.id, request_hash: 'bound-request-hash', pending: false, approval: { ...approval, decision: body.decision } } })
    }
    if (path.startsWith('/api/questions/')) {
      const question = questions.find(question => question.id === path.split('/')[3])!
      if (method === 'GET') return route.fulfill({ json: question })
      const body = request.postDataJSON(); calls.push({ path, body })
      if (control.hold) await control.hold
      if (control.denyWrite) return route.fulfill({ status: 409, json: { code: 'revision_conflict', error: 'The question changed. Reopen it before deciding.' } })
      const previous = question.answer, revision = question.revision + 1
      question.revision = revision; question.state = 'answered'; question.answer = { id: `answer-${revision}`, revision, answer: body.answer || question.input.options.find(option => option.id === body.option_id)!.answer,
        option_id: body.option_id, reason: body.reason, outcome: body.outcome, decided_by: 'person', created_at: new Date().toISOString(), deliver_after: new Date(Date.now() + 10_000).toISOString(), ...(previous && question.pending.some(effect => effect.state === 'delivered') ? { replaces: previous.id } : {}) }
      question.pending = [{ id: `pending-${revision}`, revision, kind: 'inbox', state: 'pending', deliver_after: question.answer.deliver_after }]
      return route.fulfill({ json: question })
    }
    if (path === '/api/outcomes') return route.fulfill({ json: { outcomes: [{ id: 'outcome-ci', kind: 'ci_result', ticket_key: 'AEON-1', rules_version: null, release_title: null, recorded_at: '2026-10-02T08:00:00Z', payload: { result: 'pass', name: 'Tenant isolation', repo: 'inspr-at/paimos', number: 181 } }] } })
    if (path === '/api/approvals') return route.fulfill({ json: [approval] })
    if (path.startsWith('/api/approvals/') && method === 'POST') { const body = request.postDataJSON(); calls.push({ path, body }); return route.fulfill({ status: control.denyWrite ? 409 : 200, json: control.denyWrite ? { error: 'Approval expired.' } : { ...approval, decision: body.decision } }) }
    if (path === '/api/rules/doctrine/inbox') return route.fulfill({ json: { items: [rule], pending: 1 } })
    if (path.startsWith('/api/rules/doctrine/inbox/') && method === 'POST') { calls.push({ path, body: request.postDataJSON() }); return route.fulfill({ json: { ...rule, state: 'proposed' } }) }
    if (path.endsWith('/messages') && method === 'GET') return route.fulfill({ json: { items: path.includes('/p-aeon/') ? [action] : [], next_after: 1 } })
    if (path.endsWith('/message-targets')) return route.fulfill({ json: [{ id: 'target-1', principal_id: 'agent-1', address: 'agent:reviewer', enabled: true }] })
    if ((path.endsWith('/messages') || path.endsWith('/resolution')) && method === 'POST') {
      const body = request.postDataJSON(); calls.push({ path, body })
      if (path.endsWith('/messages') && body.to !== action.sender_principal_id) return route.fulfill({ status: 404, json: { error: 'Original sender not found' } })
      return route.fulfill({ json: { ...action, status: 'accepted' } })
    }
    if (path === '/api/nodes/n-a1/attachments') return route.fulfill({ status: control.denyContext ? 403 : 200, json: { items: [{ id: 'image-1', node_id: 'n-a1', name: options.html ? 'fragment.html' : 'index-plan.png', content_type: options.html ? 'text/html; charset=utf-8' : 'image/png', size: 70, sha256: 'test', width: options.html ? null : 1, height: options.html ? null : 1, thumbnail_kind: options.html ? 'html-text' : undefined, caption: 'Index plan', position: 0, created_by: { id: 'person', name: 'Markus Barta' }, created_at: '2026-10-02T08:00:00Z' }] } })
    if (path === '/api/attachments/image-1/preview') return route.fulfill({ json: { available: true, url: 'https://preview.example.org/preview/' + 'A'.repeat(43), expires_at: new Date(Date.now() + 60_000).toISOString() } })
    if (path === '/api/attachments/image-1/content') return route.fulfill({ contentType: 'image/png', body: PNG })
    if (control.denyContext && path === '/api/nodes/n-a1') return route.fulfill({ status: 403, json: { error: 'Forbidden' } })
    return route.fallback()
  })
  return control
}
