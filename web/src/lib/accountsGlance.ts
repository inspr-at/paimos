// SPDX-License-Identifier: AGPL-3.0-only
// What the Agents page says about Accounts and computers (AEON-782): the state
// in one status line, and the few things that block agents. The lists, panels
// and editors live in Settings (AEON-686); this derives no new facts, it reads
// the same sign-in statuses, so both pages always agree.
import { agentUpdateAdvice, describeComputerStatus, describeEnrollmentStatus, type PairingView } from './agentPairing.ts'
import type { OverviewAccount, SignInReference } from './accountsOverview.ts'
import { when, type CapacityWindow } from './capacity.ts'
import type { QuotaWarningSettings } from './quotaWarnings.ts'

export const DEFAULT_QUOTA_THRESHOLDS: Readonly<QuotaWarningSettings> = Object.freeze({ early_percent: 10, urgent_percent: 3 })

/** The sign-in's words in Settings, e.g. "Ready"; empty when the account is not on that computer. */
export function signinStatus(computer: PairingView, accountId: string): string {
  const enrollment = computer.enrollments.find(e => e.account_id === accountId)
  if (!enrollment) return ''
  if (computer.computer_state === 'revoked' || enrollment.state === 'revoked') return 'Blocked'
  if (computer.computer_state === 'draining' || enrollment.state === 'draining') return 'Draining'
  return describeEnrollmentStatus(computer, enrollment) || 'Not reported'
}

export const verificationExpired = (s: SignInReference) => s.enrollment.verification_state === 'expired' && !s.enrollment.verification_expired_ready

// Words of a sign-in that cannot start agents until someone acts. Transient ones (checking, starting,
// waiting for a repin, verification queued) are not here: they settle on their own.
const BLOCKING = /failed|sign in|unavailable|stalled|needs (attention|repair)|pin (missing|incomplete|changed|invalid|unsafe)|not set up|timed out/i

/** A connected sign-in whose verification ran out or whose reported status says agents cannot start with it. */
export function isProblemSignin(s: SignInReference): boolean {
  if (s.computer.computer_state !== 'connected' || s.enrollment.state !== 'connected') return false
  // A computer that stopped reporting shows its last report, not a fresh fault: its own column says offline.
  return verificationExpired(s) || (s.computer.connectivity === 'online' && BLOCKING.test(signinStatus(s.computer, s.enrollment.account_id)))
}

export function windowLabel(w: CapacityWindow): string {
  const kind = w.reading.window_kind
  return kind === '5h' ? '5-hour window' : kind === 'weekly' ? 'Weekly' : kind === 'monthly' ? 'Monthly allowance' : w.reading.bucket || 'Quota window'
}
/** "this week", "in the 5-hour window": the sentence form of a quota window. */
function windowWords(w: CapacityWindow): string {
  const kind = w.reading.window_kind
  return kind === '5h' ? 'in the 5-hour window' : kind === 'weekly' ? 'this week' : kind === 'monthly' ? 'this month' : `in ${windowLabel(w).toLowerCase()}`
}

export type GlanceKind = 'verify' | 'attention' | 'cleanup' | 'quota'
export interface GlanceItem {
  id: string; kind: GlanceKind; name: string; detail: string
  /** Where Settings opens for this row: the sign-in's computer (and the sign-in), or the account. */
  computerId?: string; signinId?: string; accountId?: string
  /** The sign-in a person with the right may verify again, in place. */
  signin?: SignInReference
}

/**
 * Everything that blocks or soon blocks agents, most urgent first: sign-ins to
 * verify, computers to update, removed computers whose cleanup is pending, then low quota (an
 * account already waiting on a verification is not listed twice).
 */
export function glanceItems(accounts: OverviewAccount[], computers: PairingView[], thresholds: QuotaWarningSettings, now: number, requested = ''): GlanceItem[] {
  const items: GlanceItem[] = []
  const blocked = new Set<string>()
  for (const account of accounts) for (const s of account.signins) {
    const problem = isProblemSignin(s)
    // An approval link (?verify_account=…) asks for a fresh check even where nothing is wrong yet.
    if (!problem && !(requested && s.enrollment.account_id === requested && s.computer.computer_state === 'connected')) continue
    if (problem) blocked.add(account.id)
    const status = signinStatus(s.computer, s.enrollment.account_id)
    // The cause names the row: a check that ran out is verified again; a sign-in or a repair is done on the computer.
    const cause: GlanceKind = !problem || verificationExpired(s) || /^verif/i.test(status) ? 'verify' : 'attention'
    const need = !problem ? `Verify ${account.vendor} on` : cause === 'verify' ? `${account.vendor} needs verifying on` : /sign in/i.test(status) ? `${account.vendor} needs signing in on` : `${account.vendor} needs attention on`
    items.push({
      id: `${cause}:${s.enrollment.account_id}:${s.computer.computer_id}`, kind: cause, name: `${need} ${s.computer.computer_name}`,
      detail: !problem ? 'An approval link asked for a fresh check.' : verificationExpired(s) ? 'Verification expired. New agents wait until the sign-in passes again.' : status,
      computerId: s.computer.computer_id ?? undefined, signinId: s.enrollment.account_id, signin: s,
    })
  }
  // A computer whose helper is too old for this workspace says what to update.
  for (const c of computers.filter(c => c.computer_state === 'connected' && agentUpdateAdvice(c))) {
    items.push({ id: `update:${c.computer_id}`, kind: 'attention', name: `${c.computer_name} needs an update`, detail: agentUpdateAdvice(c), computerId: c.computer_id ?? undefined })
  }
  for (const c of computers.filter(c => c.computer_state === 'revoked' && c.local_cleanup === 'pending')) {
    items.push({ id: `cleanup:${c.computer_id}`, kind: 'cleanup', name: `${c.computer_name} removed · cleanup pending`, detail: 'New work is blocked. Local sign-ins are deleted when the computer comes back online.', computerId: c.computer_id ?? undefined })
  }
  const low: { item: GlanceItem; left: number }[] = []
  for (const account of accounts) {
    if (blocked.has(account.id)) continue
    const window = account.windows.filter(w => w.freshness === 'fresh' && w.remaining_percent <= thresholds.early_percent).sort((a, b) => a.remaining_percent - b.remaining_percent)[0]
    if (!window) continue
    const left = Math.round(window.remaining_percent), urgent = window.remaining_percent <= thresholds.urgent_percent
    const resets = window.reading.resets_at ? ` Resets ${when(window.reading.resets_at, now)}.` : ''
    low.push({ left: window.remaining_percent, item: { id: `quota:${account.id}`, kind: 'quota', name: `${account.vendor} is low: ${left}% left ${windowWords(window)}`, detail: `${urgent ? 'Urgent' : 'Early'} warning, below ${urgent ? thresholds.urgent_percent : thresholds.early_percent}%.${resets}`, accountId: account.records[0]?.id ?? account.id } })
  }
  return [...items, ...low.sort((a, b) => a.left - b.left).map(l => l.item)]
}

export const onlineComputers = (computers: PairingView[]) => computers.filter(c => c.computer_state === 'connected' && c.connectivity === 'online').length

export interface GlanceSummary { tone: 'ok' | 'warn'; text: string; ready: number; total: number; more: number }
/**
 * The status line: "All 3 ready · 2 computers online", or "2 of 3 ready ·
 * Claude needs verifying on mbp2607 · +1" naming the first thing and counting
 * the rest. An account is ready when it has a Ready sign-in and none to verify.
 */
export function glanceSummary(accounts: OverviewAccount[], computers: PairingView[], items: GlanceItem[]): GlanceSummary {
  const total = accounts.length
  const ready = accounts.filter(a => !a.signins.some(isProblemSignin) && a.signins.some(s => signinStatus(s.computer, s.enrollment.account_id) === 'Ready')).length
  const online = onlineComputers(computers)
  const head = ready === total ? `All ${total} ready` : `${ready} of ${total} ready`
  if (items.length) return { tone: 'warn', ready, total, more: items.length - 1, text: `${head} · ${items[0].name}${items.length > 1 ? ` · +${items.length - 1}` : ''}` }
  if (ready < total) return { tone: 'warn', ready, total, more: 0, text: `${head} · ${total - ready} not ready` }
  return { tone: 'ok', ready, total, more: 0, text: `${head} · ${online} ${online === 1 ? 'computer' : 'computers'} online` }
}

/** The grid's column caption: "Busy · 3 of 8 agents", and the phone's short "3 of 8". */
export function computerCaptionParts(c: PairingView): { long: string; short: string; tone: 'ok' | 'wait' | 'blocked' } {
  if (c.computer_state === 'revoked') return { long: 'Removed', short: 'Removed', tone: 'blocked' }
  const state = c.host_capacity?.reason ? 'Busy' : describeComputerStatus(c).stateLabel
  const tone = c.computer_state === 'connected' && c.connectivity === 'online' && !c.host_capacity?.reason ? 'ok' : 'wait'
  const host = c.host_capacity
  if (!host) return { long: state, short: state, tone }
  const max = host.policy.maximum_agents
  const count = max ? `${host.running} of ${max}` : `${host.running}`
  return { long: `${state} · ${count} ${max || host.running !== 1 ? 'agents' : 'agent'}`, short: count, tone }
}
