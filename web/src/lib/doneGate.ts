// SPDX-License-Identifier: AGPL-3.0-only
import { APIError } from './api.ts'
import { benefitIssues, completedTicketState } from './ticketBenefits.ts'
import { statusMeta } from './work.ts'

export const benefitRequiredCode = 'benefit_required'

export interface BenefitText {
  pill_en: string
  pill_de: string
  benefit_en: string
  benefit_de: string
}

// True only when a ticket is about to enter done, accepted or delivered without
// the four texts. Hidden tickets are not exempt. Already-completed tickets are.
export function needsBenefitPrompt(node: { kind_slug?: string; state: string; fields?: Record<string, unknown> | null }, next: string): boolean {
  if (node.kind_slug !== 'ticket' || completedTicketState(node.state) || !completedTicketState(next)) return false
  return benefitIssues(node.fields ?? {}).length > 0
}

export function benefitGateError(error: unknown): boolean {
  if (!(error instanceof APIError) || error.status !== 422) return false
  return error.body.code === benefitRequiredCode || error.message.startsWith('before done:')
}

export function benefitSkip(skip: { reason?: string; code?: string }): boolean {
  return skip.code === benefitRequiredCode || (typeof skip.reason === 'string' && skip.reason.startsWith('before done:'))
}

export function gateTitle(state: string): string {
  return `Before it's ${statusMeta(state).label.toLowerCase()}: what does the user gain?`
}

export function gateAction(state: string): string {
  return `Mark ${statusMeta(state).label.toLowerCase()}`
}

export function gateProgress(index: number, total: number): string | null {
  return total > 1 ? `${index} of ${total}` : null
}

// The four texts replace only themselves. A hide flag that was never set stays absent.
export function completionFields(existing: Record<string, unknown> | null | undefined, text: BenefitText): Record<string, unknown> {
  return {
    ...(existing ?? {}),
    pill_en: text.pill_en.trim(),
    pill_de: text.pill_de.trim(),
    benefit_en: text.benefit_en.trim(),
    benefit_de: text.benefit_de.trim(),
  }
}
