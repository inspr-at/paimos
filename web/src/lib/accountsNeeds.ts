// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1171: what needs the person in Settings › Accounts, Computers and Capacity
// (AEON-1149 spec c, "Needs you, per part"). Each part lists only its own items:
// Accounts a low quota, a new account that asks first and projects waiting in
// Unassigned; Computers the sign-ins with a problem and pending cleanup; Capacity
// only the one-time plan card. "At today's limit" and "over pace" are the plan
// working, never an item. The nav shows each part's count; the footer the open one's.
import type { NeedsYouItem } from '../components/settings/NeedsYouList.vue'
import { quotaWindowWarns, windowLabel } from './accountsGlance.ts'
import type { OverviewAccount } from './accountsOverview.ts'
import type { QuotaWarningSettings } from './quotaWarnings.ts'

export const ACCOUNTS_PARTS = ['accounts', 'computers', 'capacity'] as const
export type AccountsPart = typeof ACCOUNTS_PARTS[number]
export const isAccountsPart = (value: unknown): value is AccountsPart => ACCOUNTS_PARTS.includes(value as AccountsPart)
export const PART_LABEL: Readonly<Record<AccountsPart, string>> = { accounts: 'Accounts', computers: 'Computers', capacity: 'Capacity' }

/** At most this many projects waiting in Unassigned are named (the server's bound, spec c3). */
export const HOLDING_FIRST = 20
export interface AwaitingAccount { id: string; label: string; harness: string }
export interface HoldingProject { project_id: string; key: string; name: string }
/** The two facts GET /account-use adds for Needs you (spec c3); absent until the server reports them. */
export interface MatrixNeedFacts { awaiting: AwaitingAccount[]; holding: { count: number; first: HoldingProject[] } }
export const NO_MATRIX_FACTS: Readonly<MatrixNeedFacts> = Object.freeze({ awaiting: [], holding: Object.freeze({ count: 0, first: [] }) })

const text = (value: unknown, max = 200) => typeof value === 'string' && value.length <= max ? value : ''
/**
 * The Needs-you facts of one matrix answer: accounts flagged `awaiting` (no allowed cell, added while new
 * accounts ask first) and `holding_projects`. A field the server does not send yet, or sends malformed,
 * counts as nothing waiting rather than as a guess.
 */
export function matrixNeedFacts(matrix: unknown): MatrixNeedFacts {
  const m = matrix as { accounts?: unknown; holding_projects?: unknown } | null
  const accounts = Array.isArray(m?.accounts) ? m.accounts as Record<string, unknown>[] : []
  const awaiting = accounts.filter(a => a?.awaiting === true && text(a.id, 64)).map(a => ({ id: text(a.id, 64), label: text(a.label), harness: text(a.harness, 64) }))
  const h = m?.holding_projects as { count?: unknown; first?: unknown } | undefined
  const first = (Array.isArray(h?.first) ? h.first as Record<string, unknown>[] : []).slice(0, HOLDING_FIRST)
    .filter(p => text(p?.project_id, 64) && text(p.name)).map(p => ({ project_id: text(p.project_id, 64), key: text(p.key, 64), name: text(p.name) }))
  const count = typeof h?.count === 'number' && Number.isSafeInteger(h.count) && h.count > 0 ? Math.max(h.count, first.length) : first.length
  return { awaiting, holding: { count, first } }
}

export interface AccountsNeedsInput {
  accounts: readonly OverviewAccount[]
  thresholds: QuotaWarningSettings
  now: number
  facts: MatrixNeedFacts
  /** Owners and admins choose where accounts work; others read the item and open the account. */
  chooses: boolean
  vendorOf?: (harness: string) => string
}
/** The Accounts part: low quota, new accounts that ask first, projects waiting in Unassigned. */
export function accountsPartNeeds(input: AccountsNeedsInput): NeedsYouItem[] {
  const items: NeedsYouItem[] = []
  for (const a of input.accounts) {
    const low = a.windows.find(w => quotaWindowWarns(w, input.thresholds, input.now))
    if (low) items.push({ id: `quota:${a.id}`, name: `${a.vendor} quota is low`, detail: `${Math.round(low.remaining_percent)}% left · ${windowLabel(low)}`, count: 1, icon: 'gauge', action: { label: 'Details' } })
  }
  for (const a of input.facts.awaiting) {
    const name = a.label || input.vendorOf?.(a.harness) || 'an account'
    items.push({ id: `new:${a.id}`, name: `New account found: ${name}`, detail: 'New accounts ask first, so it runs nothing until you tick where it may work.', count: 1, icon: 'sparkle', action: { label: input.chooses ? 'Choose where' : 'Details', fixesProblem: input.chooses } })
  }
  const { count, first } = input.facts.holding
  if (count > 0) {
    const one = count === 1 && first[0]
    items.push({
      id: 'holding', count: 1, icon: 'folder',
      name: one ? `${first[0]!.name} waits in Unassigned` : `${count.toLocaleString('en-GB')} projects wait in Unassigned`,
      detail: one ? 'New projects wait there until they get a context. No account works on it yet.' : 'New projects wait there until they get a context. No account works on them yet.',
      action: { label: input.chooses && first[0] ? (one ? 'Choose its context' : 'Choose their contexts') : 'Details', fixesProblem: input.chooses && !!first[0] },
    })
  }
  return items
}

/** How many things need the person in a part: the sum of its items' counts (calm rows count zero). */
export const needCount = (items: readonly Pick<NeedsYouItem, 'count'>[]) => items.reduce((total, item) => total + Math.max(0, item.count), 0)
export const partCounts = (needs: Readonly<Record<AccountsPart, readonly Pick<NeedsYouItem, 'count'>[]>>): Record<AccountsPart, number> =>
  ({ accounts: needCount(needs.accounts), computers: needCount(needs.computers), capacity: needCount(needs.capacity) })
/** Where "N need you" leads: the open part when it has items, else the first part that has some. */
export function needsPart(counts: Readonly<Partial<Record<AccountsPart, number>>>, open: string): AccountsPart | null {
  if (isAccountsPart(open) && (counts[open] ?? 0) > 0) return open
  return ACCOUNTS_PARTS.find(part => (counts[part] ?? 0) > 0) ?? null
}
/** The project settings card where a project waiting in Unassigned gets its context. */
export const workContextLink = (project: Pick<HoldingProject, 'key'>) => `/p/${encodeURIComponent(project.key)}/settings#project-work-context`
