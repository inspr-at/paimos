// SPDX-License-Identifier: AGPL-3.0-only
import type { ProjectMessage } from '../../lib/agents.ts'

// last_event is the newest sent_event_id folded into the group (read watermarks).
export type MessageGroup = ProjectMessage & { count: number; last_event: number; answered: boolean }

// Delivery acknowledges receipt; only an accepted counterpart reply is an answer.
export function answeredMessages(messages: ProjectMessage[]): Set<string> {
  const byId = new Map(messages.map(m => [m.id, m]))
  const answered = new Set<string>()
  for (const reply of messages) {
    const parent = reply.reply_to ? byId.get(reply.reply_to) : undefined
    if (parent && !reply.is_action_request && reply.status === 'accepted'
      && reply.sender_principal_id === parent.recipient_principal_id
      && reply.recipient_principal_id === parent.sender_principal_id) answered.add(parent.id)
  }
  return answered
}

// Collapse presentation only: every durable message and its receipt stays intact.
// The window is measured from the first post, so a chain cannot hide older posts.
export function collapseMessages(messages: ProjectMessage[]): MessageGroup[] {
  const groups: MessageGroup[] = []
  const answered = answeredMessages(messages)
  let previousKey = ''
  for (const message of messages) {
    const key = JSON.stringify([message.sender_principal_id, message.recipient_principal_id,
      message.sender_session_id, message.recipient_session_id, message.sender_label, message.from, message.to, message.body,
      message.reply_to, message.delivery_level, message.is_action_request, message.expects_reply,
      message.reply_obligation, message.human_resolution_outcome, answered.has(message.id),
      message.optimistic ? message.id : null, message.send_failed ?? false])
    // Only consecutive duplicates collapse: a repeated answer after a person's
    // next question must stay in its chronological place.
    const previous = groups.at(-1)
    const elapsed = Date.parse(message.created_at ?? '') - Date.parse(previous?.created_at ?? '')
    if (previous && key === previousKey && elapsed >= 0 && elapsed <= 60_000) { previous.count++; previous.last_event = Math.max(previous.last_event, message.sent_event_id) }
    else {
      const group = { ...message, count: 1, last_event: message.sent_event_id, answered: answered.has(message.id) }
      groups.push(group)
    }
    previousKey = key
  }
  return groups
}

export function historicalSender(message: ProjectMessage, me: string): string {
  if (message.sender_principal_id === me) return 'You'
  if (message.sender_label && message.sender_label !== message.from) return message.sender_label
  return message.from?.replace(/^[^:]+:/, '') || 'Agent'
}
