import { api } from '@/api/client'

export interface AgentMessage {
  cursor: number
  message_id: string
  context_id: string
  task_id?: string
  from: string
  to: string
  thread_id: string
  reply_to?: string
  hop: number
  parts: Array<{ kind: 'text'; text: string }>
  delivered: boolean
  held_reason?: string
  is_action_request: boolean
  expects_reply: boolean
  human_resolution_outcome?: 'resolved' | 'dismissed'
  created_at: string
}

export type HumanResolutionOutcome = 'resolved' | 'dismissed'

export interface HumanResolutionResult {
  message_id: string
  outcome: HumanResolutionOutcome
}

export function loadIssueAgentMessages(issueId: number): Promise<AgentMessage[]> {
  return api.get<AgentMessage[]>(`/v2/issues/${issueId}/messages`)
}

export function resolveHeldAgentMessage(
  projectId: number,
  messageId: string,
  outcome: HumanResolutionOutcome,
  idempotencyKey: string,
): Promise<HumanResolutionResult> {
  return api.post<HumanResolutionResult>(
    `/projects/${projectId}/messages/${encodeURIComponent(messageId)}/resolution`,
    { outcome },
    { headers: { 'Idempotency-Key': idempotencyKey } },
  )
}
