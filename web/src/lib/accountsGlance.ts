// SPDX-License-Identifier: AGPL-3.0-only
// What the Agents page says about Accounts and computers (AEON-782): the state
// in one status line, and the few things that block agents. The lists, panels
// and editors live in Settings (AEON-686); this derives no new facts. It reads
// the same sign-in statuses and the account readiness projection, so both pages agree.
import { agentUpdateAdvice, describeComputerStatus, describeEnrollmentStatus, type PairingView } from './agentPairing.ts'
import type { OverviewAccount, SignInReference } from './accountsOverview.ts'
import { readiness } from './computerAccounts.ts'
import { when, type AccountRow, type CapacityWindow } from './capacity.ts'
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
// "need repair" is singular on purpose: "Profile permissions need repair" is a blocker.
const BLOCKING = /failed|sign in|unavailable|stalled|needs? (attention|repair)|pin (missing|incomplete|changed|invalid|unsafe)|not set up|timed out/i
// Canonical reasons that settle on their own. Every other reported reason blocks, including a code this build does not name yet.
const TRANSIENT_REASONS = new Set(['repin_pending', 'starting', 'probe_pending', 'capacity_capture'])

/** The reason the computer reported for this sign-in, when that report is about this account. */
function enrollmentReason(s: SignInReference): string | undefined {
  const detail = s.computer.harness_details?.[s.enrollment.harness]
  const reported = s.computer.harness_statuses?.[s.enrollment.harness]
  if (!detail || (reported && detail.state !== reported)) return undefined
  const listed = detail.attention_accounts ?? []
  if (listed.length) {
    const hit = listed.find(item => item.account_id === s.enrollment.account_id)
    if (hit) return hit.reason
    const complete = detail.state === 'ready' && detail.attention_truncated !== true && (detail.attention_count ?? listed.length) === listed.length
    if (complete) return undefined
    return detail.reason || 'needs_attention'
  }
  if (detail.state === 'blocked' || detail.state === 'login_required') return detail.reason || 'needs_attention'
  return undefined
}

/** A connected sign-in whose verification ran out or whose reported status says agents cannot start with it. */
export function isProblemSignin(s: SignInReference): boolean {
  if (s.computer.computer_state !== 'connected' || s.enrollment.state !== 'connected') return false
  // A computer that stopped reporting shows its last report, not a fresh fault: its own column says offline.
  if (verificationExpired(s) || s.computer.connectivity !== 'online') return verificationExpired(s)
  const reason = enrollmentReason(s)
  return (!!reason && !TRANSIENT_REASONS.has(reason)) || BLOCKING.test(signinStatus(s.computer, s.enrollment.account_id))
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
    const window = account.windows.filter(w => quotaWindowWarns(w, thresholds, now)).sort((a, b) => a.remaining_percent - b.remaining_percent)[0]
    if (!window) continue
    const left = Math.round(window.remaining_percent), urgent = window.remaining_percent <= thresholds.urgent_percent
    const resets = window.reading.resets_at ? ` Resets ${when(window.reading.resets_at, now)}.` : ''
    low.push({ left: window.remaining_percent, item: { id: `quota:${account.id}`, kind: 'quota', name: `${account.vendor} is low: ${left}% left ${windowWords(window)}`, detail: `${urgent ? 'Urgent' : 'Early'} warning, below ${urgent ? thresholds.urgent_percent : thresholds.early_percent}%.${resets}`, accountId: account.records[0]?.id ?? account.id } })
  }
  return [...items, ...low.sort((a, b) => a.left - b.left).map(l => l.item)]
}

export const onlineComputers = (computers: PairingView[]) => computers.filter(c => c.computer_state === 'connected' && c.connectivity === 'online').length

// The server's quota warning: a measured reading, at most ten minutes old, whose reset is still ahead.
// The snapshot's freshness label is not rechecked against the clock, so it is not the authority here.
const QUOTA_READING_MAX_AGE_MS = 10 * 60 * 1000
export function quotaWindowWarns(w: CapacityWindow, thresholds: QuotaWarningSettings, now: number): boolean {
  if (w.reading.source === 'estimate') return false
  const read = Date.parse(w.reading.read_at)
  if (!Number.isFinite(read) || read > now || now - read > QUOTA_READING_MAX_AGE_MS) return false
  const reset = Date.parse(w.reading.resets_at)
  if (!Number.isFinite(reset) || reset <= now) return false
  return w.remaining_percent <= thresholds.early_percent
}

/** A sign-in with no account row yet: the readiness projection still has something to judge. */
function bareRow(s: SignInReference): AccountRow {
  return {
    id: s.enrollment.account_id, name: s.enrollment.label, host: s.computer.computer_name, harness: s.enrollment.harness, state: 'unread',
    primary: null, five: null, schedule: null, plan: '', limitingReset: '', awaitingReading: false, fingerprint: '', groupId: '', groupName: '',
    hosts: [s.computer.computer_name], sameQuotaAs: '', ...(s.enrollment.state === 'draining' ? { disconnecting: true } : {}),
  }
}
/**
 * One aggregated account (a shared login is one). Ready when no sign-in needs a person and
 * the readiness projection says at least one door can start agents. A draining account or a Hold
 * is not ready, even while its sign-in still says Ready.
 */
function accountReady(account: OverviewAccount, now: number): boolean {
  if (!account.signins.length || account.signins.some(isProblemSignin)) return false
  const rows = new Map(account.rows.map(row => [row.id, row]))
  return account.signins.some(signin => {
    const row = rows.get(signin.enrollment.account_id)
    return readiness(row ? { ...bareRow(signin), ...row } : bareRow(signin), signin.computer, now).kind === 'ready'
  })
}

export interface GlanceSummary { tone: 'ok' | 'warn'; text: string; ready: number; total: number; more: number }
/**
 * The status line: "All 3 ready · 2 computers online", or "2 of 3 ready ·
 * Claude needs verifying on mbp2607 · +1" naming the first thing and counting
 * the rest. The count is the readiness projection over aggregated accounts.
 */
export function glanceSummary(accounts: OverviewAccount[], computers: PairingView[], items: GlanceItem[], now: number): GlanceSummary {
  const total = accounts.length
  const ready = accounts.filter(account => accountReady(account, now)).length
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
