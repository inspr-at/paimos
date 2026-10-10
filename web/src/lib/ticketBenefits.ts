// SPDX-License-Identifier: AGPL-3.0-only
import { normaliseState } from './work.ts'

export const benefitTextKeys = ['pill_en', 'pill_de', 'benefit_en', 'benefit_de'] as const
// Same split as strings.Fields: a non-breaking space is whitespace. Empty is zero words.
export function pillWords(value: string): number {
  const trimmed = value.trim()
  return trimmed ? trimmed.split(/\s+/u).length : 0
}
// Mirrors aeon_work_status_category: configured categories override the fixed
// vocabulary; unknown categories fall back to it. Cancellation is not success.
export function completedTicketState(state: string, schema?: Record<string, unknown>): boolean {
  const normal = normaliseState(state)
  const entries = Array.isArray(schema?.states) ? schema.states : []
  const configured = entries.find(entry => entry && typeof entry === 'object' && typeof entry.state === 'string' && normaliseState(entry.state) === normal && typeof entry.category === 'string' && entry.category.trim())
  const category = configured ? normaliseState(configured.category) : ''
  if (['open', 'doing', 'progress', 'in_progress', 'blocked', 'done', 'cancelled', 'canceled', 'archived'].includes(category)) return category === 'done'
  return ['done', 'accepted', 'delivered'].includes(normal)
}
export function benefitIssues(fields: Record<string, unknown>): string[] {
  const issues: string[] = []
  for (const key of benefitTextKeys) {
    const value = fields[key]
    if (typeof value !== 'string' || !value.trim()) issues.push(`${key} is required`)
    else if (key.startsWith('pill_')) {
      const count = pillWords(value)
      if (count < 2 || count > 4) issues.push(`${key} must contain 2–4 words`)
    }
  }
  if ('hide_from_release_notes' in fields && typeof fields.hide_from_release_notes !== 'boolean') issues.push('hide_from_release_notes must be a boolean')
  if ('no_release_needed' in fields && typeof fields.no_release_needed !== 'boolean') issues.push('no_release_needed must be a boolean')
  return issues
}
const benefitFieldNames: Record<(typeof benefitTextKeys)[number], string> = {
  pill_en: 'Pill · English',
  pill_de: 'Pill · Deutsch',
  benefit_en: 'Benefit · English',
  benefit_de: 'Benefit · Deutsch',
}
// The first incomplete benefit field, as one short line a person can act on.
export function firstBenefitGap(fields: Record<string, unknown>): { key: (typeof benefitTextKeys)[number]; line: string } | null {
  const issues = benefitIssues(fields)
  const key = benefitTextKeys.find(item => issues.some(issue => issue.startsWith(`${item} `)))
  if (!key) return null
  const issue = issues.find(item => item.startsWith(`${key} `)) ?? ''
  const name = benefitFieldNames[key]
  return { key, line: issue.includes('2–4') ? `${name} needs 2–4 words.` : `${name} is required.` }
}
export function benefitDraft(fields: Record<string, unknown>) {
  return {
    pill_en: typeof fields.pill_en === 'string' ? fields.pill_en : '',
    pill_de: typeof fields.pill_de === 'string' ? fields.pill_de : '',
    benefit_en: typeof fields.benefit_en === 'string' ? fields.benefit_en : '',
    benefit_de: typeof fields.benefit_de === 'string' ? fields.benefit_de : '',
    hide_from_release_notes: fields.hide_from_release_notes === true,
    no_release_needed: fields.no_release_needed === true,
  }
}
