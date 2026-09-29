// SPDX-License-Identifier: AGPL-3.0-only
import { APIError } from './api.ts'
import { benefitIssues, benefitTextKeys, completedTicketState } from './ticketBenefits.ts'
import { statusMeta } from './work.ts'

export const benefitRequiredCode = 'benefit_required'

export interface BenefitText {
  pill_en: string
  pill_de: string
  benefit_en: string
  benefit_de: string
  hide_from_release_notes?: boolean
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

// End of a benefit step-through. `still` is the status the skipped tickets keep.
export function benefitStepSummary(done: number, skipped: number, still: string): string {
  if (skipped > 0 && done > 0) return `${done} done · ${skipped} skipped (still ${still})`
  if (skipped > 0) return `${skipped} skipped (still ${still})`
  return `${done} done`
}

export function skippedStatusLabel(labels: string[]): string {
  const unique = [...new Set(labels)]
  if (unique.length === 1) return unique[0]
  if (unique.length === 2) return `${unique[0]} or ${unique[1]}`
  return 'an earlier status'
}

// The four texts replace only themselves. A hide flag stays absent until someone sets it.
export function completionFields(existing: Record<string, unknown> | null | undefined, text: BenefitText): Record<string, unknown> {
  const next: Record<string, unknown> = {
    ...(existing ?? {}),
    pill_en: text.pill_en.trim(),
    pill_de: text.pill_de.trim(),
    benefit_en: text.benefit_en.trim(),
    benefit_de: text.benefit_de.trim(),
  }
  if (text.hide_from_release_notes === true) next.hide_from_release_notes = true
  else if (text.hide_from_release_notes === false) {
    if (existing && 'hide_from_release_notes' in existing) next.hide_from_release_notes = false
    else delete next.hide_from_release_notes
  }
  return next
}

// After a rejected write, the dialog opens again with what was just typed.
export function benefitRetryFields(base: Record<string, unknown> | null | undefined, typed: Record<string, unknown>): Record<string, unknown> {
  const next: Record<string, unknown> = { ...(base ?? {}) }
  for (const key of benefitTextKeys) next[key] = typeof typed[key] === 'string' ? typed[key] : ''
  if (typeof typed.hide_from_release_notes === 'boolean') next.hide_from_release_notes = typed.hide_from_release_notes
  return next
}
