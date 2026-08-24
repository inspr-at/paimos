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
  created_at: string
}

export function loadIssueAgentMessages(issueId: number): Promise<AgentMessage[]> {
  return api.get<AgentMessage[]>(`/issues/${issueId}/messages`)
}
