// SPDX-License-Identifier: AGPL-3.0-only
// The INSPR doctrine as a git-backed, read-only rule layer (AEON-318). Git is
// the source of truth: Aeon shows each configured repository at its pinned
// commit. Edits become PRs; the pinned view stays unchanged. Every rule's text
// is the exact bytes of its lines at that commit and links there; TL;DRs live
// in git alongside the rule.
import { api, RequestFailure } from './api.ts'
import { sessionGone } from './authz.ts'

export interface DoctrineTldr { en: string; de?: string; check?: boolean }
export interface DoctrineRule {
  key: string
  identity: string
  set: string
  heading_path: string
  anchor: string
  start_line: number
  end_line: number
  /** The exact bytes of lines start_line..end_line at the pinned commit. */
  source: string
  sha256: string
  text: string
  strength: 'normal' | 'locked'
  url: string
  tldr?: DoctrineTldr
}
export interface DoctrineSet { set: string; title: string; anchor: string; tldr?: DoctrineTldr }
export interface DoctrineSidecar { path: string; url: string; problem?: string; unmatched?: string[] }
export interface DoctrineFile {
  path: string
  blob_sha: string
  sha256: string
  bytes: number
  kind: string
  layer: string
  url: string
  tldr?: DoctrineTldr
  sets: DoctrineSet[]
  rules: DoctrineRule[]
  sidecar?: DoctrineSidecar
  problem?: string
}
export interface DoctrineSkip { path: string; reason: string }
export type DoctrineState = 'ready' | 'not_indexed' | 'failed'
export interface DoctrineSource {
  id: string
  repository: string
  visibility: 'public' | 'private'
  ref?: string
  commit: string
  committed_at?: string
  pinned_at: string
  paths: string[]
  credential_ref?: string
  url: string
  state: DoctrineState
  error?: string
  indexed_at?: string
  files: DoctrineFile[]
  skipped: DoctrineSkip[]
}
export interface DoctrineLayer { sources: DoctrineSource[]; proposals_enabled?: boolean; proposals_disabled_reason?: string }
export interface DoctrineSourceInput {
  repository?: string
  visibility: 'public' | 'private'
  ref?: string
  commit?: string
  paths?: string[]
  credential_ref?: string
}

export class DoctrineError extends Error {
  readonly status: number
  readonly code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

async function send(path: string, method = 'GET', body?: unknown): Promise<DoctrineLayer> {
  const response = await api(path, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (response.status === 401) sessionGone()
  if (!response.ok) {
    const failure = await response.json().catch(() => ({})) as { error?: string; code?: string }
    throw new DoctrineError(response.status, failure.code ?? '', failure.error || `Request failed (${response.status})`)
  }
  const layer = await response.json() as DoctrineLayer
  return { ...layer, sources: layer.sources ?? [] }
}

export const getDoctrine = () => send('/rules/doctrine')
export const addDoctrineSource = (input: DoctrineSourceInput) => send('/rules/doctrine/sources', 'POST', input)
export const pinDoctrineSource = (id: string, input: DoctrineSourceInput) => send(`/rules/doctrine/sources/${encodeURIComponent(id)}`, 'PUT', input)
export const removeDoctrineSource = (id: string) => send(`/rules/doctrine/sources/${encodeURIComponent(id)}`, 'DELETE')
export const indexDoctrineSource = (id: string) => send(`/rules/doctrine/sources/${encodeURIComponent(id)}/index`, 'POST')

/** What a failed doctrine request tells a person. A write that timed out may still land. */
export function doctrineMessage(error: unknown): string {
  if (error instanceof RequestFailure && error.kind === 'timeout') return 'Still reading the repository. Reload in a moment to see the result.'
  if (error instanceof DoctrineError) {
    if (error.code === 'forbidden') return 'You do not have permission for that.'
    return error.message
  }
  return error instanceof Error ? error.message : 'The doctrine request failed.'
}

// ---------- Display helpers ----------

export const shortSha = (sha: string) => sha.slice(0, 7)
export const repoName = (repository: string) => repository.slice(repository.indexOf('/') + 1)
export const fileName = (path: string) => path.slice(path.lastIndexOf('/') + 1)

const day = (value: string) => new Date(value).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' })

/** "v260922101217.0.0 · 21b8140 · pinned since 22 Sep 2026" — the release first when there is one. */
export function pinLine(source: Pick<DoctrineSource, 'ref' | 'commit' | 'pinned_at'>): string {
  const parts = source.ref ? [source.ref, shortSha(source.commit)] : [`commit ${shortSha(source.commit)}`]
  parts.push(`pinned since ${day(source.pinned_at)}`)
  return parts.join(' · ')
}

/** "L7" or "L7–9": the lines a rule covers, as its link reads. */
export const lineLabel = (rule: Pick<DoctrineRule, 'start_line' | 'end_line'>) =>
  rule.end_line > rule.start_line ? `L${rule.start_line}–${rule.end_line}` : `L${rule.start_line}`

/** A rule's exact source without the trailing line break, which pre-wrap would show as an empty line. */
export const sourceText = (rule: Pick<DoctrineRule, 'source'>) => rule.source.replace(/(\r\n|\r|\n)$/, '')

/** The last heading of a set title: the one a reader sees above its rules. */
export function setHeading(title: string): string {
  const cut = title.lastIndexOf(' / ')
  return cut < 0 ? title : title.slice(cut + 3)
}

export interface RuleGroup { set: DoctrineSet; rules: DoctrineRule[] }
/** Rules grouped under their heading, in document order. */
export function groupRules(file: Pick<DoctrineFile, 'sets' | 'rules'>): RuleGroup[] {
  const groups = new Map<string, RuleGroup>()
  for (const set of file.sets) groups.set(set.set, { set, rules: [] })
  for (const rule of file.rules) {
    let group = groups.get(rule.set)
    if (!group) {
      group = { set: { set: rule.set, title: rule.set, anchor: rule.anchor }, rules: [] }
      groups.set(rule.set, group)
    }
    group.rules.push(rule)
  }
  return [...groups.values()].filter(group => group.rules.length)
}

/** "18 rules · 5 locked" */
export function fileSummary(file: Pick<DoctrineFile, 'rules'>): string {
  const count = file.rules.length
  const locked = file.rules.filter(rule => rule.strength === 'locked').length
  const parts = [`${count} ${count === 1 ? 'rule' : 'rules'}`]
  if (locked) parts.push(`${locked} locked`)
  return parts.join(' · ')
}

/** One line for a source that has nothing to list yet, or null when it has files. */
export function stateLine(source: Pick<DoctrineSource, 'state' | 'error' | 'files'>): string | null {
  if (source.state === 'failed') return `Could not read this commit: ${source.error || 'the repository did not answer'}`
  if (source.state === 'not_indexed') return 'Not read yet.'
  if (!source.files.length) return 'No doctrine file matches the configured paths at this commit.'
  return null
}

/** Paths as the form edits them: one pattern per line. */
export const pathsText = (paths: string[]) => paths.join('\n')
export const parsePaths = (text: string) => [...new Set(text.split(/[\n,]/).map(part => part.trim()).filter(Boolean))]

const SHA = /^[0-9a-f]{40}$/
/** The pin a person typed: a full commit SHA, or a tag or branch to resolve. */
export function pinInput(value: string): Pick<DoctrineSourceInput, 'ref' | 'commit'> {
  const pin = value.trim()
  return SHA.test(pin) ? { commit: pin } : { ref: pin }
}

export interface DoctrineProposal {
  id: string; source_id: string; repository: string; path: string; rule_key: string
  state: 'proposed' | 'in_review' | 'merged' | 'released' | 'pinned' | 'closed' | 'pending' | 'dismissed' | 'promoted'
  head_sha: string; pr_number: number; pr_url: string; proposed_by: string; approved_by?: string
  merge_commit?: string; release?: string; release_commit?: string; release_url?: string
  release_requested?: boolean; gate_ready: boolean; gate_reason?: string; pinned_machines: number; created_at: string
  branch?: string; orphaned?: boolean
  draft?: boolean; automatic?: boolean
  /** Doctrine inbox (AEON-444). */
  inbox?: boolean; ticket?: string; submitted_by?: string; edited_by?: string
  dismissed_by?: string; dismiss_reason?: string; promoted_commit?: string
}
export interface DoctrineProposalInput {
  request_id: string; source_id: string; path: string; rule_key: string; rule_sha256: string
  source: string; tldr: { en: string; de?: string }; explanation: string
}
async function proposalRequest<T>(path: string, method = 'POST', body?: unknown): Promise<T> {
  const response = await api(`/rules/doctrine/proposals${path}`, {
    method, headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (response.status === 401) sessionGone()
  if (!response.ok) {
    const failure = await response.json().catch(() => ({})) as { code?: string; error?: string }
    throw new DoctrineError(response.status, failure.code ?? '', failure.error || `Request failed (${response.status})`)
  }
  return response.json() as Promise<T>
}
export const proposeDoctrineChange = (input: DoctrineProposalInput) => proposalRequest<DoctrineProposal>('', 'POST', input)
export const getDoctrineProposals = async () => (await proposalRequest<{ proposals: DoctrineProposal[] }>('', 'GET')).proposals ?? []
export const refreshDoctrineProposal = (id: string) => proposalRequest<DoctrineProposal>(`/${encodeURIComponent(id)}/refresh`)
export const approveDoctrineProposal = (id: string, head: string) => proposalRequest<DoctrineProposal>(`/${encodeURIComponent(id)}/approve`, 'POST', { head_sha: head })
export function proposalState(p: Pick<DoctrineProposal, 'state' | 'pinned_machines'> & { orphaned?: boolean; pr_number?: number; draft?: boolean }): string {
  if (p.orphaned && !p.pr_number) return 'Branch left on GitHub'
  if (p.draft && (p.state === 'proposed' || p.state === 'in_review')) return 'Draft'
  if (p.state === 'pinned') return `Pinned on ${p.pinned_machines} reported ${p.pinned_machines === 1 ? 'machine' : 'machines'}`
  return { proposed: 'Proposed', in_review: 'In review', merged: 'Merged', released: 'Released', closed: 'Closed', pending: 'Waiting', dismissed: 'Dismissed', promoted: 'In the pinned doctrine' }[p.state]
}

// ---------- Doctrine inbox (AEON-444) ----------
// Agents propose a rule change; it waits here until a person sends it to git
// as a pull request, edits it first, or dismisses it with a reason.

export interface DiffPart { op: 'eq' | 'del' | 'ins'; text: string }
export interface DoctrineInboxItem extends DoctrineProposal {
  heading: string; strength?: 'normal' | 'locked'; label: string
  /** The rule at the current pin, and the waiting text. */
  base: string; base_sha256?: string; proposed?: string
  tldr?: DoctrineTldr; why?: string; diff: DiffPart[]; outdated?: boolean
  proposer: string; proposer_kind: 'person' | 'agent' | ''; ticket_href?: string
}
export interface DoctrineInboxHeadline { id: string; label: string; created_at: string }
export interface DoctrineInboxEdit { source?: string; tldr?: { en: string; de?: string }; why?: string; rule_sha256?: string }

async function inboxRequest<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await api(`/rules/doctrine/inbox${path}`, {
    method, headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (response.status === 401) sessionGone()
  if (!response.ok) {
    const failure = await response.json().catch(() => ({})) as { code?: string; error?: string }
    throw new DoctrineError(response.status, failure.code ?? '', failure.error || `Request failed (${response.status})`)
  }
  return response.json() as Promise<T>
}
export async function getDoctrineInbox(): Promise<{ pending: number; items: DoctrineInboxItem[] }> {
  const out = await inboxRequest<{ pending: number; items: DoctrineInboxItem[] }>('')
  return { pending: out.pending ?? 0, items: out.items ?? [] }
}
export async function getDoctrineInboxSummary(): Promise<{ pending: number; items: DoctrineInboxHeadline[] }> {
  const out = await inboxRequest<{ pending: number; items: DoctrineInboxHeadline[] }>('/summary')
  return { pending: out.pending ?? 0, items: out.items ?? [] }
}
/** Propose PR; with edits it is "edit then propose". */
export const submitDoctrineInbox = (id: string, edit: DoctrineInboxEdit = {}) => inboxRequest<DoctrineProposal>(`/${encodeURIComponent(id)}/pull-request`, 'POST', edit)
export const dismissDoctrineInbox = (id: string, reason: string) => inboxRequest<DoctrineProposal>(`/${encodeURIComponent(id)}/dismiss`, 'POST', { reason })

/** "Estimate before work" — a headline short enough for a toast. */
export function inboxLabel(label: string, max = 60): string {
  const text = label.trim().replace(/\s+/g, ' ').replace(/[.。]$/, '')
  return text.length > max ? `${text.slice(0, max - 1).trimEnd()}…` : text
}

export interface DoctrineMetric {
  name: string; rules_version: string; samples: number; value: number; from: string; until: string
}
export interface DoctrineFinding {
  id: string; pattern: string; title: string; count: number; rules_version: string; harness: string; ticket_kind: string
  status: 'pending' | 'draft' | 'awaiting_use' | 'internal_note' | 'observed' | 'closed'; reason?: string
  proposal_id?: string; pr_url?: string; rule_label?: string; created_at: string
  evidence: { id: string; ticket_id: string; ticket_key: string; href: string; kind: string }[]
  before: DoctrineMetric; after?: DoctrineMetric; delta?: number; metrics?: DoctrineMetric[]
}
export async function getDoctrineFindings(): Promise<DoctrineFinding[]> {
  const response = await api('/rules/doctrine/analysis')
  if (response.status === 401) sessionGone()
  if (response.status === 404) return [] // mixed-version rollout
  if (!response.ok) throw new DoctrineError(response.status, '', 'Outcome proposals could not be loaded.')
  return ((await response.json()) as { findings: DoctrineFinding[] }).findings ?? []
}
export const findingState = (f: DoctrineFinding) => ({ pending: 'Queued', draft: 'Draft', awaiting_use: 'Awaiting outcomes', internal_note: 'Internal note', observed: 'Measured', closed: 'Closed' })[f.status]
export function outcomeMetric(m: DoctrineMetric): string {
  if (m.name === 'fix_rounds' || m.name === 'review_rounds') return `${Number(m.value.toFixed(1))} rounds`
  if (m.name === 'time_to_done') return `${Number((m.value / 60).toFixed(1))} min`
  return `${Number((m.value * 100).toFixed(1))}%`
}
export function outcomePopulation(m: DoctrineMetric): string {
  if (m.name.startsWith('gate:')) return 'review verdicts'
  if (m.name.startsWith('learning:')) return 'learning tickets'
  if (m.name === 'ci_failures') return 'CI results'
  return 'tickets'
}
const outcomeMetricNames: Record<string, string> = { review_rounds: 'Review rounds', fix_rounds: 'Fix rounds', ci_failures: 'CI failures', reverts: 'Reverted tickets', exception_votes: 'Rework requests', time_to_done: 'Time to done' }
export const outcomeMetricName = (m: DoctrineMetric): string => outcomeMetricNames[m.name] ?? 'Recorded outcomes'
export function outcomeDelta(f: DoctrineFinding): string {
  if (f.delta === undefined) return ''
  const sign = f.delta > 0 ? '+' : f.delta < 0 ? '−' : ''
  const amount = Math.abs(f.delta)
  if (f.before.name === 'fix_rounds') return `${sign}${Number(amount.toFixed(1))} rounds`
  if (f.before.name === 'time_to_done') return `${sign}${Number((amount / 60).toFixed(1))} min`
  return `${sign}${Number((amount * 100).toFixed(1))} percentage points`
}
