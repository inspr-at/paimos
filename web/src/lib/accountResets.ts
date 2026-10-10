// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1037: the words of the Settings › Accounts Resets card (AEON-1030 draft 6).
// Credits and plans come only from the vendor-backed overview (AEON-1035); nothing is inferred here.
import { clockLabel, dayLabel } from './capacity.ts'

export type ResetPolicy = 'suggest' | 'auto_before_expiry'
export interface ResetCredits { count: number; expires_at: string[]; source: 'vendor' }
export interface ResetPlan { planned_at: string; raised_pace_points: number; raised_pace_until: string }
export interface AccountResetState {
  account_id: string; reset_policy: ResetPolicy; revision: number; binding_revision: number
  resets: ResetCredits | null; reset_plan: ResetPlan | null; undo_supported: boolean
}

const MAX_GROUPS = 3

/** "2 resets · 1 expires Sun, 1 on 6 Nov"; equal days count together, later ones are summed. */
export function resetsSummary(credits: ResetCredits, now: number, timezone?: string): string {
  if (credits.count <= 0) return 'No resets left'
  const groups: { day: string; n: number }[] = []
  for (const at of credits.expires_at) {
    const day = dayLabel(at, now, timezone), last = groups[groups.length - 1]
    if (last?.day === day) last.n++
    else groups.push({ day, n: 1 })
  }
  const head = `${credits.count} reset${credits.count === 1 ? '' : 's'}`
  if (!groups.length) return head
  const [first, ...rest] = groups
  const words = [`${first!.n} expire${first!.n === 1 ? 's' : ''} ${first!.day}`, ...rest.slice(0, MAX_GROUPS - 1).map(g => `${g.n} ${g.day === 'tomorrow' ? '' : 'on '}${g.day}`)]
  const later = rest.slice(MAX_GROUPS - 1).reduce((n, g) => n + g.n, 0)
  if (later) words.push(`${later} later`)
  return `${head} · ${words.join(', ')}`
}

/** What the switch means right now: only suggesting, the planned moment, or nothing planned yet. */
export function resetPlanLine(policy: ResetPolicy, plan: ResetPlan | null, now: number, timezone?: string): { lead: string; rest: string } {
  if (policy !== 'auto_before_expiry') return { lead: '', rest: 'Off: PAIMOS only suggests.' }
  if (!plan) return { lead: '', rest: 'On: nothing planned yet. PAIMOS plans the moment once the window is nearly used.' }
  const points = Math.round(plan.raised_pace_points)
  const lead = `Planned: ${dayLabel(plan.planned_at, now, timezone)} ~${clockLabel(plan.planned_at, timezone)}`
  return { lead, rest: points > 0 ? `, then +${points} percentage point${points === 1 ? '' : 's'} a day until ${dayLabel(plan.raised_pace_until, now, timezone)}.` : '.' }
}

/** Every shape of the plan line at its widest ("tomorrow" is the longest day label). The card
 * reserves the tallest, so switching never moves the account controls below it (AEON-541). */
export const RESET_PLAN_WIDEST: readonly { lead: string; rest: string }[] = [
  resetPlanLine('suggest', null, 0),
  resetPlanLine('auto_before_expiry', null, 0),
  { lead: 'Planned: tomorrow ~00:00', rest: ', then +100 percentage points a day until tomorrow.' },
]
