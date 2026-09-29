// SPDX-License-Identifier: AGPL-3.0-only
// Host → harness → account → model → thinking for starting an agent (AEON-226).
// Choices come only from GET /api/agent-accounts/catalog. A missing or unreadable
// catalog stays empty. Models are the account's granted profiles; an empty grant
// is not filled from the tenant model list. account_key and other secrets are
// never read or shown.
import { api } from './api.ts'
import { duration, harnessLabel } from './agentState.ts'

export const WORK_ROLES = ['scout', 'mechanical', 'build', 'build-hard', 'review-gate'] as const
export type WorkRole = typeof WORK_ROLES[number]
export const AUTHOR_FAMILIES = ['openai', 'anthropic', 'xai', 'cursor'] as const
export type AuthorFamily = typeof AUTHOR_FAMILIES[number]
export const UNAVAILABLE_REASONS = ['state', 'probe', 'capacity', 'allowance', 'models'] as const
export type UnavailableReason = typeof UNAVAILABLE_REASONS[number]
export type CascadeStep = 'host' | 'harness' | 'account' | 'model' | 'effort'
const STEPS: CascadeStep[] = ['host', 'harness', 'account', 'model', 'effort']
const STEP_KEY = { host: 'hostId', harness: 'harness', account: 'accountId', model: 'modelKey', effort: 'profileId' } as const
const EFFORT_ORDER = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']
const EFFORT_LABEL: Record<string, string> = {
  off: 'Off', none: 'None', minimal: 'Minimal', low: 'Low', medium: 'Medium', high: 'High', xhigh: 'Extra high', max: 'Max',
}
const FAMILY_LABEL: Record<AuthorFamily, string> = { openai: 'OpenAI', anthropic: 'Anthropic', xai: 'xAI', cursor: 'Cursor' }
const HOUR = 3_600_000
const DAY = 24 * HOUR
const NAMED_SPANS = [
  { ms: 5 * HOUR, tol: 5 * 60_000, label: '5-hour' },
  { ms: DAY, tol: 20 * 60_000, label: 'Daily' },
  { ms: 7 * DAY, tol: 2 * HOUR, label: 'Weekly' },
  { ms: 30 * DAY, tol: 12 * HOUR, label: 'Monthly' },
]
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const SECRET = /token|secret|password|credential|api[-_]?key|authorization|codex_home|claude_config|sk-[a-z0-9]|-----begin|bearer\s/i
const UNITS = ['requests', 'tokens', 'cost_micros']
const PACES = ['steady', 'frontload', 'unrestricted']

export interface CatalogEffort { effort: string; model_profile_id: string; version: string }
export interface CatalogModel { model: string; family: string; efforts: CatalogEffort[] }
export interface CatalogWindow {
  id: string; account_id?: string; starts_at: string; ends_at: string
  unit: 'requests' | 'tokens' | 'cost_micros'
  allowance: number; used: number; reserved: number
  pace_model: 'steady' | 'frontload' | 'unrestricted'; burst_ratio: number; provisional: boolean
  remaining: number; pace_remaining: number
}
export interface CatalogAccount {
  id: string; label: string; plan: string; registered_by_principal_id: string
  state: 'available' | 'draining' | 'unavailable'
  last_probe_at: string | null; last_probe_ok: boolean | null
  available: boolean; unavailable_reasons: UnavailableReason[]
  remaining_fraction: number | null; windows: CatalogWindow[]; models: CatalogModel[]
  default_model_profile_id: string | null
}
export interface CatalogHarness { harness: string; accounts: CatalogAccount[]; default_account_id: string | null }
export interface CatalogHost { daemon_id: string; label: string; harnesses: CatalogHarness[] }
export interface AgentAccountCatalog { as_of: string; role: string; hosts: CatalogHost[] }
export type CatalogGap = 'failed' | 'forbidden' | 'family' | null
export interface CatalogResult { catalog: AgentAccountCatalog | null; gap: CatalogGap; message: string }
export interface CascadeChoice { hostId: string; harness: string; accountId: string; modelKey: string; profileId: string }
export interface CascadeTouch { host: boolean; harness: boolean; account: boolean; model: boolean; effort: boolean }
export interface CascadeOption { value: string; label: string }
export interface RequestedRun { host: string; harness: string; account: string; model: string; thinking: string }
export interface CascadeNotes { host: string; harness: string; account: string; model: string; effort: string }
export interface CascadeStatus { label: string; detail: string; tone: 'neutral' | 'warn' }
export interface CascadePresentation {
  hosts: CascadeOption[]; harnesses: CascadeOption[]; accounts: CascadeOption[]
  models: CascadeOption[]; efforts: CascadeOption[]; notes: CascadeNotes
  roleNote: string; profileId: string; agentId: string; status: CascadeStatus; requested: RequestedRun
}
export interface CascadeContext {
  catalog: AgentAccountCatalog | null
  role: { role: WorkRole; source: 'ticket' | 'default' }
  authorFamily: AuthorFamily | ''
}

export const emptyChoice = (): CascadeChoice => ({ hostId: '', harness: '', accountId: '', modelKey: '', profileId: '' })
export const emptyTouch = (): CascadeTouch => ({ host: false, harness: false, account: false, model: false, effort: false })
export const effortLabel = (effort: string) => EFFORT_LABEL[effort] ?? (displayText(effort) ?? 'Unrecognized thinking level')
const MODEL_PART = /^[A-Za-z0-9][A-Za-z0-9._:-]*$/
export const familyLabel = (family: string) => (AUTHOR_FAMILIES as readonly string[]).includes(family) ? FAMILY_LABEL[family as AuthorFamily] : 'Unrecognized family'
export const modelKey = (model: Pick<CatalogModel, 'family' | 'model'>) => `${model.family}\t${model.model}`

const isRecord = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const isUuid = (value: unknown): value is string => typeof value === 'string' && UUID.test(value)
const isNullableString = (value: unknown) => value === null || typeof value === 'string'
const isNullableBool = (value: unknown) => value === null || typeof value === 'boolean'
const isNumber = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value)

function isWindow(value: unknown): value is CatalogWindow {
  if (!isRecord(value) || typeof value.id !== 'string' || typeof value.starts_at !== 'string' || typeof value.ends_at !== 'string') return false
  return UNITS.includes(String(value.unit)) && PACES.includes(String(value.pace_model))
    && [value.allowance, value.used, value.reserved, value.remaining, value.pace_remaining, value.burst_ratio].every(isNumber)
    && typeof value.provisional === 'boolean'
}
function isEffort(value: unknown): value is CatalogEffort {
  return isRecord(value) && typeof value.effort === 'string' && isUuid(value.model_profile_id) && typeof value.version === 'string'
}
function isModel(value: unknown): value is CatalogModel {
  return isRecord(value) && typeof value.model === 'string' && typeof value.family === 'string'
    && Array.isArray(value.efforts) && value.efforts.every(isEffort)
}
function isAccount(value: unknown): value is CatalogAccount {
  if (!isRecord(value) || !isUuid(value.id) || typeof value.label !== 'string' || typeof value.plan !== 'string' || !isUuid(value.registered_by_principal_id)) return false
  return ['available', 'draining', 'unavailable'].includes(String(value.state))
    && isNullableString(value.last_probe_at) && isNullableBool(value.last_probe_ok) && typeof value.available === 'boolean'
    && Array.isArray(value.unavailable_reasons) && value.unavailable_reasons.every(reason => (UNAVAILABLE_REASONS as readonly string[]).includes(String(reason)))
    && (value.remaining_fraction === null || isNumber(value.remaining_fraction))
    && Array.isArray(value.windows) && value.windows.every(isWindow)
    && Array.isArray(value.models) && value.models.every(isModel)
    && (value.default_model_profile_id === null || isUuid(value.default_model_profile_id))
}
function isHarness(value: unknown): value is CatalogHarness {
  return isRecord(value) && typeof value.harness === 'string' && (value.default_account_id === null || isUuid(value.default_account_id))
    && Array.isArray(value.accounts) && value.accounts.every(isAccount)
}
function isHost(value: unknown): value is CatalogHost {
  return isRecord(value) && typeof value.daemon_id === 'string' && value.daemon_id.length > 0 && typeof value.label === 'string'
    && Array.isArray(value.harnesses) && value.harnesses.every(isHarness)
}

export function isAccountCatalog(value: unknown): value is AgentAccountCatalog {
  return isRecord(value) && typeof value.as_of === 'string' && typeof value.role === 'string'
    && Array.isArray(value.hosts) && value.hosts.every(isHost)
}

export async function fetchAccountCatalog(role: WorkRole, authorFamily: AuthorFamily | '' = ''): Promise<CatalogResult> {
  if (role === 'review-gate' && !authorFamily) {
    return { catalog: null, gap: 'family', message: 'Review work needs the author model family before accounts can be listed.' }
  }
  const params = new URLSearchParams({ role })
  if (authorFamily) params.set('author_family', authorFamily)
  try {
    const response = await api(`/agent-accounts/catalog?${params}`)
    if (response.status === 401 || response.status === 403) {
      const data: unknown = await response.json().catch(() => null)
      const error = isRecord(data) && typeof data.error === 'string' ? displayText(data.error) : null
      return { catalog: null, gap: 'forbidden', message: error ?? 'You do not have permission to read accounts.' }
    }
    if (!response.ok) return failed()
    const data: unknown = await response.json()
    return isAccountCatalog(data) ? { catalog: data, gap: null, message: '' } : failed()
  } catch {
    return failed()
  }
}
const failed = (): CatalogResult => ({ catalog: null, gap: 'failed', message: 'The account catalog did not load. Retry to choose a host, account and model.' })

export function displayText(value: string | null | undefined, max = 128): string | null {
  const text = value?.trim() ?? ''
  if (!text || text.length > max || SECRET.test(text) || /[/\\~]/.test(text)) return null
  return text
}
// Public registry ids such as anthropic/claude-opus-5. Separate from displayText,
// which still hides paths and secrets in account, host and plan labels.
export function publicModelId(value: string | null | undefined, max = 128): string | null {
  const text = value?.trim() ?? ''
  if (!text || text.length > max || SECRET.test(text) || text.includes('\\') || text.includes('~')) return null
  const parts = text.split('/')
  if (parts.some(part => part === '' || part === '.' || part === '..' || !MODEL_PART.test(part))) return null
  return text
}
export function allowanceKnown(account: { remaining_fraction: number | null; windows: { provisional: boolean; allowance: number }[] }): boolean {
  if (account.windows.length === 0 || account.windows.some(window => window.provisional)) return false
  if (!account.windows.some(window => !window.provisional && window.allowance > 0)) return false
  const fraction = account.remaining_fraction
  return typeof fraction === 'number' && Number.isFinite(fraction) && fraction >= 0 && fraction <= 1
}
export function accountName(account: { label?: string | null }): string {
  return displayText(account.label) ?? 'Unlabeled account'
}
export function accountPlan(account: { plan?: string | null }): string | null {
  return displayText(account.plan)
}
export function allowanceWindowLabel(window: { starts_at: string; ends_at: string }): string {
  const span = Date.parse(window.ends_at) - Date.parse(window.starts_at)
  if (!Number.isFinite(span) || span <= 0) return 'Window timing unknown'
  return NAMED_SPANS.find(item => Math.abs(span - item.ms) <= item.tol)?.label ?? `${duration(span)} window`
}

export function workRoleFor(ticket: { fields?: Record<string, unknown> } | null): { role: WorkRole; source: 'ticket' | 'default' } {
  const raw = ticket?.fields?.work_role
  if (typeof raw === 'string' && (WORK_ROLES as readonly string[]).includes(raw)) return { role: raw as WorkRole, source: 'ticket' }
  return { role: 'build', source: 'default' }
}
export function authorFamilyFor(ticket: { fields?: Record<string, unknown> } | null): AuthorFamily | '' {
  const raw = ticket?.fields?.author_family
  return typeof raw === 'string' && (AUTHOR_FAMILIES as readonly string[]).includes(raw) ? raw as AuthorFamily : ''
}

export function chooseStep(choice: CascadeChoice, touch: CascadeTouch, step: CascadeStep, value: string) {
  const nextChoice: CascadeChoice = { ...choice, [STEP_KEY[step]]: value }
  const nextTouch: CascadeTouch = { ...touch, [step]: true }
  for (const later of STEPS.slice(STEPS.indexOf(step) + 1)) {
    nextTouch[later] = false
    nextChoice[STEP_KEY[later]] = ''
  }
  return { choice: nextChoice, touch: nextTouch }
}

function hostOf(catalog: AgentAccountCatalog, id: string) {
  return catalog.hosts.find(host => host.daemon_id === id)
}
function harnessOf(host: CatalogHost | undefined, harness: string) {
  return host?.harnesses.find(item => item.harness === harness)
}
function accountOf(harness: CatalogHarness | undefined, id: string) {
  return harness?.accounts.find(account => account.id === id)
}
function modelOf(account: CatalogAccount | undefined, key: string) {
  return account?.models.find(model => modelKey(model) === key)
}
function defaultAccount(harness: CatalogHarness | undefined) {
  return harness?.accounts.find(account => account.id === harness.default_account_id && account.available)
}
function knownFraction(account: CatalogAccount | undefined) {
  if (!account || !allowanceKnown(account) || account.remaining_fraction == null) return null
  return account.remaining_fraction
}
function preferredHarness(host: CatalogHost | undefined) {
  let best: { harness: string; fraction: number; id: string } | null = null
  for (const item of host?.harnesses ?? []) {
    const account = defaultAccount(item)
    const fraction = knownFraction(account)
    if (!account || fraction == null) continue
    if (!best || fraction > best.fraction || (fraction === best.fraction && account.id < best.id)) best = { harness: item.harness, fraction, id: account.id }
  }
  return best?.harness ?? ''
}
function preferredHost(catalog: AgentAccountCatalog) {
  let best: { host: string; fraction: number; id: string } | null = null
  for (const host of catalog.hosts) {
    const harness = harnessOf(host, preferredHarness(host))
    const account = defaultAccount(harness)
    const fraction = knownFraction(account)
    if (!account || fraction == null) continue
    if (!best || fraction > best.fraction || (fraction === best.fraction && account.id < best.id)) best = { host: host.daemon_id, fraction, id: account.id }
  }
  return best?.host ?? ''
}
function routedModel(account: CatalogAccount | undefined) {
  const id = account?.default_model_profile_id
  if (!id) return undefined
  return account.models.find(model => model.efforts.some(effort => effort.model_profile_id === id))
}

export function fillDefaults(catalog: AgentAccountCatalog | null, choice: CascadeChoice, touch: CascadeTouch): CascadeChoice {
  if (!catalog) return emptyChoice()
  const next = { ...choice }
  const hosts = catalog.hosts
  if (!touch.host || !hosts.some(host => host.daemon_id === next.hostId)) {
    next.hostId = touch.host ? '' : (preferredHost(catalog) || (hosts.length === 1 ? hosts[0].daemon_id : ''))
  }
  const host = hostOf(catalog, next.hostId)
  const harnesses = host?.harnesses ?? []
  if (!touch.harness || !harnesses.some(item => item.harness === next.harness)) {
    next.harness = touch.harness ? '' : (preferredHarness(host) || (harnesses.length === 1 ? harnesses[0].harness : ''))
  }
  const harness = harnessOf(host, next.harness)
  const accounts = harness?.accounts ?? []
  if (!touch.account || !accounts.some(account => account.id === next.accountId)) {
    const preferred = defaultAccount(harness)?.id
    next.accountId = touch.account ? '' : (preferred || (accounts.length === 1 ? accounts[0].id : ''))
  }
  const account = accountOf(harness, next.accountId)
  const models = visibleModels(account)
  const routed = routedModel(account)
  const routedVisible = routed && publicModelId(routed.model) ? routed : undefined
  if (!touch.model || !models.some(model => modelKey(model) === next.modelKey)) {
    next.modelKey = touch.model ? '' : (routedVisible ? modelKey(routedVisible) : (models.length === 1 ? modelKey(models[0]) : ''))
  }
  const model = models.find(item => modelKey(item) === next.modelKey)
  const efforts = model?.efforts ?? []
  const routedEffort = efforts.find(effort => effort.model_profile_id === account?.default_model_profile_id)
  if (!touch.effort || !efforts.some(effort => effort.model_profile_id === next.profileId)) {
    next.profileId = touch.effort ? '' : (routedEffort?.model_profile_id ?? (efforts.length === 1 ? efforts[0].model_profile_id : ''))
  }
  return next
}

function visibleModels(account: CatalogAccount | undefined) {
  return (account?.models ?? []).filter(model => publicModelId(model.model) && model.efforts.some(effort => effort.model_profile_id))
}
function tightestWindow(account: CatalogAccount) {
  let best: { window: CatalogWindow; ratio: number } | null = null
  for (const window of account.windows) {
    if (window.provisional || !Number.isSafeInteger(window.allowance) || window.allowance <= 0 || !Number.isSafeInteger(window.remaining) || window.remaining < 0) continue
    const ratio = window.remaining / window.allowance
    if (!best || ratio < best.ratio) best = { window, ratio }
  }
  return best?.window ?? null
}
function allowanceSentence(account: CatalogAccount) {
  if (!allowanceKnown(account) || account.remaining_fraction == null) return 'Allowance is unknown.'
  const pct = Math.round(Math.min(1, Math.max(0, account.remaining_fraction)) * 100)
  const window = tightestWindow(account)
  return window ? `${pct}% of the ${allowanceWindowLabel(window)} window is left.` : `${pct}% is left.`
}
function allowancePhrase(account: CatalogAccount) {
  if (!allowanceKnown(account) || account.remaining_fraction == null) return 'allowance unknown'
  const pct = `${Math.round(Math.min(1, Math.max(0, account.remaining_fraction)) * 100)}%`
  const window = tightestWindow(account)
  return window ? `${pct} of ${allowanceWindowLabel(window)} left` : `${pct} left`
}
function reasonText(account: CatalogAccount, reason: UnavailableReason) {
  if (reason === 'state') return account.state === 'draining' ? 'This account is draining.' : 'This account is unavailable.'
  if (reason === 'probe') return 'The sign-in probe is missing or stale.'
  if (reason === 'capacity') return 'This account is at its parallel run limit.'
  if (reason === 'allowance') return 'This account has no allowance headroom.'
  return 'This account grants no model for this work.'
}
function reasonLabel(account: CatalogAccount) {
  const reason = account.unavailable_reasons[0]
  if (reason === 'state') return account.state === 'draining' ? 'Account is draining' : 'Account is unavailable'
  if (reason === 'probe') return 'Sign-in probe is stale'
  if (reason === 'capacity') return 'Account is at capacity'
  if (reason === 'allowance') return 'No allowance left'
  if (reason === 'models') return 'No model granted'
  return 'Account is not ready'
}
function hostLabel(host: CatalogHost) {
  return displayText(host.label) ?? displayText(host.daemon_id) ?? 'Unlabeled host'
}
function modelLabel(model: CatalogModel, models: CatalogModel[]) {
  const name = publicModelId(model.model) ?? 'Unrecognized model'
  const family = displayText(model.family)
  const duplicate = models.filter(item => item.model === model.model).length > 1
  return duplicate && family ? `${name} · ${family}` : name
}
function accountOptionLabel(account: CatalogAccount) {
  const plan = accountPlan(account)
  const extra = account.available ? allowancePhrase(account) : account.unavailable_reasons.map(reason => reasonText(account, reason).replace(/\.$/, '')).join('; ')
  return [accountName(account), plan, extra].filter(Boolean).join(' · ')
}

export function presentCascade(input: CascadeContext, choice: CascadeChoice, touch: CascadeTouch): CascadePresentation {
  const catalog = input.catalog
  const host = catalog ? hostOf(catalog, choice.hostId) : undefined
  const harness = harnessOf(host, choice.harness)
  const accounts = harness?.accounts ?? []
  const account = accountOf(harness, choice.accountId)
  const models = visibleModels(account)
  const modelVisible = models.some(item => modelKey(item) === choice.modelKey)
  const model = modelVisible ? modelOf(account, choice.modelKey) : undefined
  const efforts = [...(model?.efforts ?? [])].sort((a, b) => {
    const rank = EFFORT_ORDER.indexOf(a.effort) - EFFORT_ORDER.indexOf(b.effort)
    return rank || a.effort.localeCompare(b.effort)
  })
  const selectedEffort = efforts.find(effort => effort.model_profile_id === choice.profileId)
  const hosts = (catalog?.hosts ?? []).map(item => ({ value: item.daemon_id, label: hostLabel(item) }))
  const requestedAccount = account ? [accountName(account), accountPlan(account)].filter(Boolean).join(' · ') : 'Not selected'
  const defaultId = harness?.default_account_id
  const roleNote = input.role.source === 'default'
    ? 'This ticket does not name a work type, so build is the default.'
    : input.role.role === 'review-gate'
      ? (input.authorFamily ? `Review work excludes the ${familyLabel(input.authorFamily)} family.` : 'Review work needs the author model family before accounts can be listed.')
      : ''
  return {
    hosts,
    harnesses: (host?.harnesses ?? []).map(item => ({ value: item.harness, label: harnessLabel(item.harness) })),
    accounts: accounts.map(item => ({ value: item.id, label: accountOptionLabel(item) })),
    models: models.map(item => ({ value: modelKey(item), label: modelLabel(item, models) })),
    efforts: efforts.map(effort => ({ value: effort.model_profile_id, label: effortLabel(effort.effort) })),
    notes: {
      host: hostNote(catalog, hosts, choice, touch),
      harness: harnessNote(choice, host),
      account: accountNote(choice, touch, accounts, account, defaultId),
      model: modelNote(choice, touch, account, models, input.role.role),
      effort: effortNote(choice, model, efforts, selectedEffort, account),
    },
    roleNote,
    profileId: selectedEffort?.model_profile_id ?? '',
    agentId: account?.registered_by_principal_id ?? '',
    status: statusFor(account, accounts),
    requested: {
      host: hosts.find(item => item.value === choice.hostId)?.label || 'Not selected',
      harness: choice.harness ? harnessLabel(choice.harness) : 'Not selected',
      account: requestedAccount,
      model: model ? modelLabel(model, models) : 'Not selected',
      thinking: selectedEffort ? effortLabel(selectedEffort.effort) : 'Not selected',
    },
  }
}

function hostNote(catalog: AgentAccountCatalog | null, hosts: CascadeOption[], choice: CascadeChoice, touch: CascadeTouch) {
  if (!catalog) return ''
  if (!hosts.length) return 'No host has an enrolled account for this work.'
  if (hosts.length === 1) return 'Only one host has an enrolled account.'
  if (!touch.host && choice.hostId) {
    const account = accountOf(harnessOf(hostOf(catalog, choice.hostId), choice.harness), choice.accountId)
    if (account && allowanceKnown(account)) return 'Chosen because it has the account with the most allowance left.'
    return 'Allowance is unknown, so this host is not ranked by remaining allowance.'
  }
  return ''
}
function harnessNote(choice: CascadeChoice, host: CatalogHost | undefined) {
  if (!choice.hostId || !host) return ''
  if (!host.harnesses.length) return 'No harness has an enrolled account on this host.'
  if (host.harnesses.length === 1) return 'Only one harness is enrolled on this host.'
  return 'These harnesses have an enrolled account on this host.'
}
function accountNote(choice: CascadeChoice, touch: CascadeTouch, accounts: CatalogAccount[], account: CatalogAccount | undefined, defaultId: string | null | undefined) {
  if (!choice.harness) return ''
  if (!accounts.length) return 'No account is enrolled for this harness on this host.'
  if (!account) return 'Choose an account. An unavailable account stays listed with the reason it cannot be the default.'
  if (!account.available) return account.unavailable_reasons.map(reason => reasonText(account, reason)).join(' ')
  const allowance = allowanceSentence(account)
  if (accounts.length === 1) return `Only one account is enrolled here. ${allowance}`
  if (!touch.account && account.id === defaultId && allowanceKnown(account)) return `Selected because it has the most allowance left. ${allowance}`
  return `Only this account will be used. ${allowance}`
}
function modelNote(choice: CascadeChoice, touch: CascadeTouch, account: CatalogAccount | undefined, models: CatalogModel[], role: WorkRole) {
  if (!account) return ''
  if (!models.length) return 'No granted model is available to choose.'
  if (!account.default_model_profile_id && !choice.modelKey) return 'This role has no routed model. Choose one this account allows.'
  if (models.length === 1) return 'Only one model is granted for this account.'
  if (!touch.model && choice.modelKey && account.default_model_profile_id) return `Chosen for ${role} work.`
  return ''
}
function effortNote(choice: CascadeChoice, model: CatalogModel | undefined, efforts: CatalogEffort[], selected: CatalogEffort | undefined, account: CatalogAccount | undefined) {
  if (!model) return ''
  if (!efforts.length) return 'No thinking level is granted for this model.'
  if (!selected && !account?.default_model_profile_id) return 'Choose a thinking level this model allows.'
  if (efforts.length === 1) return 'This model grants one thinking level.'
  if (selected && selected.model_profile_id === account?.default_model_profile_id && choice.profileId) return 'The thinking level is the one routed for this work.'
  return ''
}
function statusFor(account: CatalogAccount | undefined, accounts: CatalogAccount[]): CascadeStatus {
  if (!accounts.length) return { label: 'No enrolled account', detail: 'There is no account to pin for this host and harness.', tone: 'warn' }
  if (!account) return { label: 'Choose an account', detail: 'The run is pinned to the account you choose. It does not fall back to another one.', tone: 'warn' }
  if (!account.available) {
    return { label: reasonLabel(account), detail: `${account.unavailable_reasons.map(reason => reasonText(account, reason)).join(' ')} The run can still queue. Routing rechecks this account and does not switch to another.`, tone: 'warn' }
  }
  return { label: 'Account available', detail: 'The daemon rechecks pacing and capacity, then uses only this account.', tone: 'neutral' }
}

export interface SessionReport {
  model: string; account: string; thinking: string
  modelKnown: boolean; accountKnown: boolean; thinkingKnown: boolean; differs: boolean
}
export function sessionReport(session: { model?: string | null; account_label?: string | null; reasoning_effort?: string | null }, requested: RequestedRun): SessionReport {
  const model = publicModelId(session.model)
  const account = displayText(session.account_label)
  const effort = session.reasoning_effort?.trim() ?? ''
  const thinkingKnown = effort !== '' && (!!EFFORT_LABEL[effort] || displayText(effort) !== null)
  const thinking = thinkingKnown ? effortLabel(effort) : 'Unknown'
  const sameAccount = !!account && (requested.account === account || requested.account.startsWith(`${account} · `))
  return {
    model: model ?? 'Unknown',
    account: account ?? 'Unknown',
    thinking,
    modelKnown: model !== null,
    accountKnown: account !== null,
    thinkingKnown,
    differs: (model !== null && requested.model !== model) || (account !== null && !sameAccount) || (thinkingKnown && requested.thinking !== thinking),
  }
}
