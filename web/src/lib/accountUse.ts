// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1054: which accounts may work in which work context (concept AEON-1044
// §2.5). The server is the boundary: every write carries the matrix revision it
// was made on, a competing change answers 409 and nothing is saved. Undo sends
// the inverse cells with the revision its own save returned.
import { api, APIError } from './api.ts'

export const PERMISSION = 'account.use.manage'
export type AllowAsk = 'allow' | 'ask'
export type NewProjects = 'default' | 'holding'
export type NewModels = 'allow' | 'shipped_only' | 'deny'
export interface RuleValues { new_accounts: AllowAsk; new_contexts: AllowAsk; new_projects: NewProjects; new_models: NewModels }
export interface Rules extends RuleValues { revision: number; enforced_at: string | null; confirmation_required: boolean; confirmed_at: string | null }
export interface UseAccount { id: string; label: string; harness: string; plan: string | null; billing_mode: string; owner_id: string | null }
export interface WorkContext { id: string; name: string; kind: 'default' | 'regular' | 'holding'; new_accounts_override: 'deny' | null; archived_at: string | null; revision: number }
export interface UseCell { account_id: string; context_id: string; allowed: boolean }
export interface RunningOutside { run_id: string; account_id: string; node_id: string | null }
export interface Matrix {
  rules: Rules; accounts: UseAccount[]; contexts: WorkContext[]; cells: UseCell[]
  next_account: string | null; next_context: string | null
  running_outside: RunningOutside[]; running_outside_truncated: boolean
}
export interface CellResult { revision: number; changes: UseCell[]; undo: UseCell[] }
export type Bulk = { scope: 'all'; allowed: boolean } | { scope: 'account'; account_id: string; allowed: boolean } | { scope: 'context'; context_id: string; allowed: boolean }
export interface ProjectContext { project_id: string; context_id: string; revision: number }

/** Server bounds (api/areas/account-use.yaml): one page and one save. */
export const PAGE = 200, MAX_CELLS = 1000
const MAX_BYTES = 4 * 1024 * 1024

async function call<T>(path: string, method: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await api(path, { method, ...(signal ? { signal } : {}), ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({})) as Record<string, unknown>
    const reason = typeof data.error === 'string' && data.error ? data.error : typeof data.message === 'string' && data.message ? data.message : `Request failed (${response.status})`
    throw new APIError(response.status, reason, data)
  }
  if (Number(response.headers.get('Content-Length')) > MAX_BYTES) { await response.body?.cancel(); throw new Error('The matrix answer is too large.') }
  const text = await response.text()
  if (text.length > MAX_BYTES) throw new Error('The matrix answer is too large.')
  return JSON.parse(text) as T
}

const isRules = (r: unknown): r is Rules => {
  const v = r as Rules
  return !!v && ['allow', 'ask'].includes(v.new_accounts) && ['allow', 'ask'].includes(v.new_contexts) && ['default', 'holding'].includes(v.new_projects)
    && ['allow', 'shipped_only', 'deny'].includes(v.new_models) && Number.isSafeInteger(v.revision) && v.revision >= 1 && typeof v.confirmation_required === 'boolean'
}
/** Rejects anything but a complete, bounded matrix page; a partial answer is never shown as the matrix. */
export function validMatrix(m: unknown): m is Matrix {
  const v = m as Matrix
  return !!v && isRules(v.rules) && Array.isArray(v.accounts) && v.accounts.length <= PAGE && Array.isArray(v.contexts) && v.contexts.length <= PAGE
    && Array.isArray(v.cells) && v.cells.length <= PAGE * PAGE && Array.isArray(v.running_outside) && v.running_outside.length <= PAGE
    && typeof v.running_outside_truncated === 'boolean'
    && v.accounts.every(a => typeof a?.id === 'string' && typeof a.label === 'string' && typeof a.harness === 'string')
    && v.contexts.every(c => typeof c?.id === 'string' && typeof c.name === 'string' && ['default', 'regular', 'holding'].includes(c.kind))
    && v.cells.every(c => typeof c?.account_id === 'string' && typeof c.context_id === 'string')
}

export async function readMatrix(signal?: AbortSignal, limit = PAGE): Promise<Matrix> {
  const page = await call<unknown>(`/account-use?limit=${limit}`, 'GET', undefined, signal)
  if (!validMatrix(page)) throw new Error('The matrix answer was not in the expected form. Nothing is shown rather than a partial matrix.')
  return page
}
export const saveCells = (expected_revision: number, change: { changes: UseCell[] } | { bulk: Bulk }, signal?: AbortSignal) =>
  call<CellResult>('/account-use/cells', 'PATCH', { expected_revision, ...change }, signal)
export const saveRules = (expected_revision: number, values: RuleValues, signal?: AbortSignal) =>
  call<Rules>('/account-use/rules', 'PUT', { expected_revision, ...values }, signal)
export const confirmMatrix = (expected_revision: number, signal?: AbortSignal) =>
  call<Rules>('/account-use/confirm', 'POST', { expected_revision }, signal)
export const createContext = (expected_revision: number, name: string, signal?: AbortSignal) =>
  call<{ context: WorkContext; revision: number }>('/work-contexts', 'POST', { expected_revision, name }, signal)
export const updateContext = (context: WorkContext, expected_revision: number, change: { name?: string; new_accounts_override?: 'deny' | null; archived?: boolean }, signal?: AbortSignal) =>
  call<{ context: WorkContext; revision: number }>(`/work-contexts/${encodeURIComponent(context.id)}`, 'PATCH', {
    expected_revision, name: change.name ?? context.name,
    new_accounts_override: change.new_accounts_override === undefined ? context.new_accounts_override : change.new_accounts_override,
    archived: change.archived ?? false,
  }, signal)
/** null: the project has no mapping, so no account may work there (never the default context). */
export async function readProjectContext(projectId: string, signal?: AbortSignal): Promise<ProjectContext | null> {
  try { return await call<ProjectContext>(`/projects/${encodeURIComponent(projectId)}/work-context`, 'GET', undefined, signal) } catch (error) {
    if (error instanceof APIError && error.status === 404) return null
    throw error
  }
}
export const saveProjectContext = (projectId: string, expected_revision: number, context_id: string, signal?: AbortSignal) =>
  call<ProjectContext>(`/projects/${encodeURIComponent(projectId)}/work-context`, 'PUT', { expected_revision, context_id }, signal)

export const cellKey = (accountId: string, contextId: string) => `${accountId}:${contextId}`
export function allowedSet(cells: readonly UseCell[]): Set<string> {
  return new Set(cells.filter(c => c.allowed).map(c => cellKey(c.account_id, c.context_id)))
}
export function applyCells(set: ReadonlySet<string>, cells: readonly UseCell[]): Set<string> {
  const next = new Set(set)
  for (const c of cells) { if (c.allowed) next.add(cellKey(c.account_id, c.context_id)); else next.delete(cellKey(c.account_id, c.context_id)) }
  return next
}
/** Columns a person can tick: the default first, then regular contexts by name. Unassigned (holding) never takes ticks. */
export function tickable(contexts: readonly WorkContext[]): WorkContext[] {
  return contexts.filter(c => c.kind !== 'holding' && !c.archived_at)
    .sort((a, b) => (a.kind === 'default' ? 0 : 1) - (b.kind === 'default' ? 0 : 1) || a.name.localeCompare(b.name, 'en') || a.id.localeCompare(b.id))
}
export type TriState = 'all' | 'some' | 'none'
export function triState(keys: readonly string[], set: ReadonlySet<string>): TriState {
  const n = keys.filter(k => set.has(k)).length
  return keys.length && n === keys.length ? 'all' : n ? 'some' : 'none'
}
export const rowState = (account: string, contexts: readonly WorkContext[], set: ReadonlySet<string>) => triState(contexts.map(c => cellKey(account, c.id)), set)
export const columnState = (context: string, accounts: readonly UseAccount[], set: ReadonlySet<string>) => triState(accounts.map(a => cellKey(a.id, context)), set)
/** A tri-state box always resolves to one value: anything but fully allowed becomes allowed. */
export const nextFromTri = (state: TriState) => state !== 'all'

/** Where an account added now would be ticked by the rule (trigger T3): never under "ask first", never in a column set to "never". */
export function predictedForNewAccount(rules: Pick<Rules, 'new_accounts'>, contexts: readonly WorkContext[]): WorkContext[] {
  return rules.new_accounts === 'allow' ? tickable(contexts).filter(c => c.new_accounts_override !== 'deny') : []
}

export interface SwitchChoice<V extends string> { value: V; label: string }
export interface SwitchDef<K extends keyof RuleValues = keyof RuleValues> { key: K; title: string; hint: string; choices: SwitchChoice<RuleValues[K]>[] }
// D1 (decided 2026-10-09). Two values each; a migrated workspace keeps its third
// model value visible, but it is never offered as a fresh choice.
export const SWITCHES: SwitchDef[] = [
  { key: 'new_accounts', title: 'New accounts', hint: 'Also covers a new harness', choices: [{ value: 'allow', label: 'Allow automatically' }, { value: 'ask', label: 'Ask first' }] },
  { key: 'new_contexts', title: 'New contexts', hint: 'Which accounts a new context starts with', choices: [{ value: 'allow', label: 'Allow automatically' }, { value: 'ask', label: 'Ask first' }] },
  { key: 'new_projects', title: 'New projects', hint: 'Ask first puts them in Unassigned', choices: [{ value: 'default', label: 'Follow automatically' }, { value: 'holding', label: 'Ask first' }] },
  { key: 'new_models', title: 'New model versions', hint: 'New vendor lines always need a person', choices: [{ value: 'allow', label: 'Allow automatically' }, { value: 'deny', label: 'Ask first' }] },
]
export function switchChoices(def: SwitchDef, current: string): SwitchChoice<string>[] {
  const extra = def.key === 'new_models' && current === 'shipped_only' ? [{ value: 'shipped_only', label: 'Only with PAIMOS updates' }] : []
  return [...def.choices, ...extra]
}
export const ruleValues = (r: RuleValues): RuleValues => ({ new_accounts: r.new_accounts, new_contexts: r.new_contexts, new_projects: r.new_projects, new_models: r.new_models })

export function billingLabel(mode: string): string {
  return mode === 'subscription' ? 'Subscription' : mode === 'api' ? 'API billing' : 'Billing unknown'
}
/** Words for a failed matrix write. A 409 means nothing was saved and the matrix is read again. */
export function failureText(error: unknown, what = 'The change'): string {
  if (error instanceof APIError) {
    if (error.status === 409) return `Someone changed the matrix meanwhile. ${what} was not saved; the current state is shown.`
    if (error.status === 403) return 'You need permission to manage where accounts may work.'
    if (error.status === 413) return 'That change covers more than 1,000 cells. Change rows or columns one at a time.'
    if (error.status === 404) return `${what} was not saved: the account or context no longer exists.`
  }
  return `${what} was not saved. Try again.`
}
