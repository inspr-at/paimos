// SPDX-License-Identifier: AGPL-3.0-only
import type { ProjectMessage } from '../../lib/agents.ts'

// last_event is the newest sent_event_id folded into the group (read watermarks).
export type MessageGroup = ProjectMessage & { count: number; last_event: number }
export const belongsToSession = (message: ProjectMessage, sessionId: string) =>
  message.recipient_session_id === sessionId || message.sender_session_id === sessionId

// Collapse presentation only: every durable message and its receipt stays intact.
// The window is measured from the first post, so a chain cannot hide older posts.
export function collapseMessages(messages: ProjectMessage[]): MessageGroup[] {
  const groups: MessageGroup[] = []
  const latest = new Map<string, MessageGroup>()
  for (const message of messages) {
    const key = JSON.stringify([message.sender_principal_id, message.recipient_principal_id,
      message.sender_session_id, message.recipient_session_id, message.sender_label, message.from, message.to, message.body,
      message.reply_to, message.delivery_level, message.is_action_request, message.expects_reply,
      message.reply_obligation, message.human_resolution_outcome])
    const previous = latest.get(key)
    const elapsed = Date.parse(message.created_at ?? '') - Date.parse(previous?.created_at ?? '')
    if (previous && elapsed >= 0 && elapsed <= 60_000) { previous.count++; previous.last_event = Math.max(previous.last_event, message.sent_event_id) }
    else {
      const group = { ...message, count: 1, last_event: message.sent_event_id }
      groups.push(group)
      latest.set(key, group)
    }
  }
  return groups
}

export function historicalSender(message: ProjectMessage, me: string): string {
  if (message.sender_principal_id === me) return 'You'
  if (message.sender_label && message.sender_label !== message.from) return message.sender_label
  return message.from?.replace(/^[^:]+:/, '') || 'Agent'
}
