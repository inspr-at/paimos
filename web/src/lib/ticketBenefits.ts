// SPDX-License-Identifier: AGPL-3.0-only
export const benefitTextKeys = ['pill_en', 'pill_de', 'benefit_en', 'benefit_de'] as const
// Matches ticketbenefits.Completed on the server, not all closed states.
export function completedTicketState(state: string): boolean {
  return state === 'done' || state === 'accepted' || state === 'delivered'
}
export function benefitIssues(fields: Record<string, unknown>): string[] {
  const issues: string[] = []
  for (const key of benefitTextKeys) {
    const value = fields[key]
    if (typeof value !== 'string' || !value.trim()) issues.push(`${key} is required`)
    else if (key.startsWith('pill_') && (value.trim().split(/\s+/u).length < 2 || value.trim().split(/\s+/u).length > 4)) issues.push(`${key} must contain 2–4 words`)
  }
  if ('hide_from_release_notes' in fields && typeof fields.hide_from_release_notes !== 'boolean') issues.push('hide_from_release_notes must be a boolean')
  return issues
}
export function benefitDraft(fields: Record<string, unknown>) {
  return {
    pill_en: typeof fields.pill_en === 'string' ? fields.pill_en : '',
    pill_de: typeof fields.pill_de === 'string' ? fields.pill_de : '',
    benefit_en: typeof fields.benefit_en === 'string' ? fields.benefit_en : '',
    benefit_de: typeof fields.benefit_de === 'string' ? fields.benefit_de : '',
    hide_from_release_notes: fields.hide_from_release_notes === true,
  }
}
