// SPDX-License-Identifier: AGPL-3.0-only
import { api, APIError } from './api'
import type { IconName } from '../components/AppIcon.vue'
import { assistantCopy } from './assistantsCopy'

// The identity/policy wire contract is A (AEON-1701). Policy is a ceiling,
// never a grant. In particular an empty project list means NO projects.
export interface AssistantPolicy {
  allowed_projects: string[]
  allowed_scopes: string[]
  weekly_budget: { amount: string; unit: 'USD' | 'EUR' | 'tokens' | 'credits' }
  outward_action_rule: 'approval_required'
  never_ask_person_for_secrets_or_manual_steps: true
  approver_person_ids?: string[]
  weekly_reset_day?: number
}
export interface AssistantSnapshot {
  principal_id: string; owner_person_id: string; status: 'active' | 'deactivated'; revision: number; policy: AssistantPolicy
}
export interface Assistant extends AssistantSnapshot {
  kind: 'assistant'; principal_kind: 'agent'; display_name: string; vendor: 'grok' | 'unknown'; messaging_only: boolean
}
export const cloneAssistantPolicy = (policy: AssistantPolicy): AssistantPolicy => ({
  allowed_projects: [...policy.allowed_projects], allowed_scopes: [...policy.allowed_scopes],
  weekly_budget: { ...policy.weekly_budget }, outward_action_rule: policy.outward_action_rule,
  never_ask_person_for_secrets_or_manual_steps: policy.never_ask_person_for_secrets_or_manual_steps,
  ...(policy.approver_person_ids ? { approver_person_ids: [...policy.approver_person_ids] } : {}),
  ...(policy.weekly_reset_day === undefined ? {} : { weekly_reset_day: policy.weekly_reset_day }),
})
export interface AssistantEvent {
  id: number; type: string; at: string; actor_principal_id: string
  before?: Partial<AssistantSnapshot>; after?: Partial<AssistantSnapshot> & { allowed?: boolean; reason?: string; operation?: string }
}
export type AssistantStatus = 'waiting' | 'down' | 'working' | 'idle' | 'paused' | 'setup' | 'unavailable'
export interface AssistantRoutine {
  id: string; name: string; schedule: string; next: string | null; projectId: string
  result?: { label: string; href: string }; paused: boolean; revision: number; lastState?: string; timezone?: string
}

// Presentation facts have explicit unknowns. Unmerged services are never
// probed at invented routes and their absence is never converted to zero/ok.
export interface AssistantFacts {
  adapter: null | { state: 'ok' | 'down' | 'setup'; lastReceipt: string | null; failingSince?: string; error?: string }
  spend: null | { amount: string; daily: { day: string; amount: string }[]; resumes?: string }
  approvals: null | { id: string; title: string; target: string; at: string; href: string; decision?: string; by?: string }[]
  routines: null | AssistantRoutine[]
  approvalsTruncated?: boolean; approvalsError?: string; approvalsCanDecide?: boolean
  routinesTruncated?: boolean; routinesError?: string; activityIncomplete?: boolean
  activity: null | { at: string; text: string; kind: 'messages' | 'runs' | 'approvals'; href?: string; refused?: boolean }[]
  lastActivity: null | { at: string; text: string; policyOnly?: boolean }
  paused: null | { by: string; at: string }
}
export const unavailableAssistantFacts = (): AssistantFacts => ({ adapter: null, spend: null, approvals: null, routines: null, activity: null, lastActivity: null, paused: null })
// Capabilities follow merged contracts. Open approvals use the shared Desk
// projection; adapter health still awaits its permission-checked reader.
export const assistantCapabilities = Object.freeze({ adapter: false, approvals: true, routines: true, pause: false, approvers: false, resetWeekday: false })

export function assistantStatus(item: Assistant, facts: AssistantFacts, now: number): AssistantStatus {
  if (facts.paused || item.status === 'deactivated' || budgetPercent(item.policy, facts) === 100) return 'paused'
  if (facts.approvals?.some(row => !row.decision)) return 'waiting'
  if (facts.adapter?.state === 'down') return 'down'
  if (facts.adapter?.state === 'setup') return 'setup'
  const at = Date.parse(facts.lastActivity?.at ?? '')
  if (!facts.lastActivity?.policyOnly && Number.isFinite(at) && now >= at && now - at < 15 * 60_000) return 'working'
  return facts.adapter === null ? 'unavailable' : 'idle'
}
export const ASSISTANT_STATUS_ICON: Record<AssistantStatus, IconName> = { waiting: 'hand', down: 'alert', working: 'pulse', idle: 'circle-dot', paused: 'pause', setup: 'link', unavailable: 'ring-dashed' }
const statusRank: Record<AssistantStatus, number> = { waiting: 0, down: 1, working: 2, idle: 3, paused: 4, setup: 5, unavailable: 6 }
export function assistantOrder(items: Assistant[], facts: Record<string, AssistantFacts>, now: number): string[] {
  return [...items].sort((a, b) => statusRank[assistantStatus(a, facts[a.principal_id] ?? unavailableAssistantFacts(), now)] - statusRank[assistantStatus(b, facts[b.principal_id] ?? unavailableAssistantFacts(), now)] || a.display_name.localeCompare(b.display_name) || a.principal_id.localeCompare(b.principal_id)).map(item => item.principal_id)
}
export function retainAssistantOrder(items: Assistant[], previous: string[]): string[] {
  const ids = new Set(items.map(item => item.principal_id))
  return [...previous.filter(id => ids.has(id)), ...items.filter(item => !previous.includes(item.principal_id)).map(item => item.principal_id)]
}
export function budgetPercent(policy: AssistantPolicy, facts: AssistantFacts): number | null {
  if (!facts.spend) return null
  const scaled = (value: string): bigint | null => {
    if (!/^[0-9]{1,24}(\.[0-9]{1,6})?$/.test(value)) return null
    const [integer = '0', fraction = ''] = value.split('.')
    return BigInt(integer) * 1_000_000n + BigInt(fraction.padEnd(6, '0'))
  }
  const spend = scaled(facts.spend.amount), limit = scaled(policy.weekly_budget.amount)
  if (spend === null || limit === null) return null
  // Compare exact micro-units before drawing: floating point rounding must not
  // turn a value just below the ceiling into a budget-reached pause.
  return limit === 0n || spend >= limit ? 100 : Number(spend * 10_000n / limit) / 100
}
export function assistantMoney(amount: string, unit: AssistantPolicy['weekly_budget']['unit'], spent = false): string {
  // Preserve the exact decimal sent by the server, including sub-cent spend.
  const [integer, decimal = ''] = amount.split('.')
  const tail = spent ? decimal.padEnd(2, '0') : decimal.replace(/0+$/, '')
  const value = `${integer}${tail ? `.${tail}` : ''}`
  return unit === 'USD' ? `$${value}` : unit === 'EUR' ? `€${value}` : `${value} ${unit}`
}
export function policyGroups(scopes: string[]): string[] {
  const words = assistantCopy()
  const groups = new Set<string>()
  for (const scope of scopes) {
    if (scope === 'nodes.write') groups.add(scopes.includes('nodes.read') ? words.writeTickets : words.writeTicketsOnly)
    else if (scope === 'nodes.read') { if (!scopes.includes('nodes.write')) groups.add(words.readTickets) }
    else if (scope === 'knowledge.write') groups.add(words.writeKnowledge)
    else if (scope === 'knowledge.read') { if (!scopes.includes('knowledge.write')) groups.add(words.readKnowledge) }
    else if (scope === 'inbox.send') groups.add(words.messagePeople)
    else if (scope === 'inbox.read') groups.add(scopes.includes('inbox.receipt') ? words.readInbox : words.readInboxOnly)
    else if (scope === 'inbox.receipt') groups.add(scopes.includes('inbox.read') ? words.readInbox : words.acknowledgeInbox)
    else groups.add(scope) // Unknown registry labels stay explicit, never silently omitted.
  }
  return [...groups]
}
export const assistantDeskLink = (id: string) => `/decision-desk?assistant=${encodeURIComponent(id)}`
export const assistantConnectLink = '/agents/register-agent?kind=assistant'

export async function assistantRequest<T>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await api(path, { method, signal, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new APIError(response.status, typeof data.error === 'string' ? data.error : `Request failed (${response.status})`)
  }
  return response.json()
}
export async function listAssistants(signal?: AbortSignal): Promise<{ items: Assistant[]; truncated: boolean }> {
  const items: Assistant[] = [], cursors = new Set<string>()
  let after = ''
  // Each request is at most 100, with a 1,000-identity / 10-request total bound.
  for (let page = 0; page < 10; page++) {
    const body = await assistantRequest<{ assistants: Assistant[]; next_cursor?: string }>(`/assistants?limit=100${after ? `&after=${encodeURIComponent(after)}` : ''}`, 'GET', undefined, signal)
    if (!Array.isArray(body.assistants) || body.assistants.length > 100) throw new Error(assistantCopy().invalidResponse)
    items.push(...body.assistants)
    if (!body.next_cursor) return { items, truncated: false }
    if (cursors.has(body.next_cursor)) throw new Error(assistantCopy().invalidResponse)
    cursors.add(body.next_cursor); after = body.next_cursor
  }
  return { items, truncated: true }
}
export const saveAssistantPolicy = (id: string, revision: number, policy: AssistantPolicy) => assistantRequest<AssistantSnapshot>(`/assistants/${encodeURIComponent(id)}/policy`, 'PUT', { expected_revision: revision, policy })
export const disconnectAssistant = (id: string) => assistantRequest<Assistant>(`/assistants/${encodeURIComponent(id)}/disable`, 'POST')
async function assistantEvents(types: string, now: number, signal?: AbortSignal): Promise<{ items: AssistantEvent[]; truncated: boolean }> {
  const from = new Date(now - 90 * 86400_000).toISOString(), to = new Date(now).toISOString()
  const query = new URLSearchParams({ type: types, from, to, limit: '100' })
  const body = await assistantRequest<{ items: AssistantEvent[]; next_after: number | null }>(`/events?${query}`, 'GET', undefined, signal)
  if (!Array.isArray(body.items) || body.items.length > 100) throw new Error(assistantCopy().invalidResponse)
  return { items: body.items, truncated: body.next_after !== null }
}
// Successful admissions cannot consume the policy-change query's window.
export const assistantPolicyEvents = (now: number, signal?: AbortSignal) => assistantEvents('assistant.created,assistant.updated,assistant.disabled,assistant.policy_updated', now, signal)
export const assistantDecisionEvents = (now: number, signal?: AbortSignal) => assistantEvents('assistant.policy_evaluated,assistant.delegated', now, signal)
export function policyChange(event: AssistantEvent): string {
  const words = assistantCopy(), before = event.before?.policy, after = event.after?.policy
  if (event.type === 'assistant.delegated') return words.taskDelegated
  if (event.type === 'assistant.policy_evaluated') {
    if (event.after?.allowed === true) return words.policyAllowed
    const reason = event.after?.reason ?? ''
    if (reason.includes('budget')) return words.policyBudgetDenied
    if (reason.includes('project')) return words.policyProjectDenied
    if (reason.includes('scope')) return words.policyScopeDenied
    if (reason.includes('instance')) return words.policyInstanceDenied
    if (reason.includes('grant')) return words.policyGrantDenied
    return words.policyDenied
  }
  if (event.type === 'assistant.created') return words.identityCreated
  if (event.type === 'assistant.updated') return words.identityChanged
  if (event.type === 'assistant.disabled') return words.disconnected
  if (!before || !after) return words.policyChanged
  const changes: string[] = []
  if (JSON.stringify(before.weekly_budget) !== JSON.stringify(after.weekly_budget)) changes.push(`${words.budget}: ${words.budgetChange(assistantMoney(before.weekly_budget.amount, before.weekly_budget.unit), assistantMoney(after.weekly_budget.amount, after.weekly_budget.unit))}`)
  if (JSON.stringify(before.allowed_projects) !== JSON.stringify(after.allowed_projects)) changes.push(words.projectsChanged)
  if (JSON.stringify(before.allowed_scopes) !== JSON.stringify(after.allowed_scopes)) changes.push(words.scopesChanged)
  return changes.join(' · ') || words.policyChanged
}
