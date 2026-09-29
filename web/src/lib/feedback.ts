// SPDX-License-Identifier: AGPL-3.0-only
// Feedback from the help menu goes to a workspace owner as an inbox message
// (GET /api/inbox/feedback-recipient, then POST /api/inbox/messages).
import { api } from './api.ts'

export interface FeedbackRecipient { principal_id: string; name: string }

// null: there is no other owner (the caller is the only one).
export async function feedbackRecipient(): Promise<FeedbackRecipient | null> {
  const response = await api('/inbox/feedback-recipient')
  if (response.status === 404) return null
  if (!response.ok) throw new Error(`Feedback is unavailable (${response.status})`)
  return response.json() as Promise<FeedbackRecipient>
}

// One key per written message, so a retry after a lost answer never sends it twice.
export async function sendFeedback(recipient: string, body: string, key: string): Promise<void> {
  const response = await api('/inbox/messages', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ recipient_principal_id: recipient, body, idempotency_key: key }),
  })
  if (!response.ok) throw new Error(`Feedback was not sent (${response.status})`)
}
