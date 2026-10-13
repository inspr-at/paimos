// SPDX-License-Identifier: AGPL-3.0-only
// Settings › Models › Model registry (AEON-1012, grouped in AEON-1163): every model PAIMOS can run, by provider.
// A line ("Claude Opus") holds its versions; a version ("Opus 5.5") holds its routes (Claude Code, Cursor, pi),
// each with its own native levels and source. Everything is derived from the registry's immutable profiles
// (one profile per route and thinking level).
import { api } from './api'
import { StaleScopeError } from './identityScope'
import { compareModelVersions, profileLine } from './modelPrefs'
import { fullName, modelLine, providerLabel, providerRank } from './modelsSimple'
import { validPiModel } from './piModel'

export type Harness = 'codex' | 'claude' | 'grok' | 'gemini' | 'cursor' | 'pi' | 'opencode'
export const HARNESS_ORDER: Harness[] = ['codex', 'claude', 'grok', 'gemini', 'cursor', 'pi', 'opencode']
export const HARNESS_NAME: Record<Harness, string> = { codex: 'Codex', claude: 'Claude Code', grok: 'Grok', gemini: 'Gemini', cursor: 'Cursor', pi: 'pi', opencode: 'OpenCode' }
const isHarness = (value: string): value is Harness => (HARNESS_ORDER as string[]).includes(value)

export interface RegistryProfile {
  id: string; slug: string; version: string; harness: string; family: string; model: string; effort: string; tier: string; enabled: boolean; created_at: string
  display_name?: string; short_name?: string; model_version?: string; note?: string
  /** Immutable origin; '' or absent on servers that only tell auto from manual. */
  source?: 'auto' | 'manual'; origin?: 'shipped' | 'provider' | 'harness' | 'manual' | ''; retire_at?: string | null; retired?: boolean; effort_level?: number | null; provider?: string
  /** Canonical provider:model identity shared across routes (AEON-1145). */
  identity?: string
  /** Best-effort lifecycle evidence; null or absent when unknown. */
  lifecycle?: Lifecycle | null
  /** The active pause of this harness/model route, shared by its native levels; absent when it runs. */
  pause?: ModelPause | null
}
export interface Lifecycle { state: string | null; date: string | null; successor: string | null; reason: string; source: string }
export interface ModelPause { until: string | null; reason: string; by: string | null; at: string }
export interface RefreshSettings { agent_reports_enabled: boolean; auto_add_profiles: boolean; api_enabled: boolean; interval_minutes: number }
export interface DiscoverySourceResult { state?: string; seen?: number; vendor?: string; account_id?: string }
export interface CheckResult { new_lines?: string[]; added?: number; proposed?: number; accepted_models?: string[]; sources?: DiscoverySourceResult[] }
/** A model a harness or vendor reported that waits for a person (auto-add off): accepting it enables it. */
export interface Observation { harness: string; model: string; effort: string; pending: boolean; failures: number; suppressed_until: string | null; last_working_at: string | null }
export interface RefreshStatus {
  settings: RefreshSettings; last_run_at: string | null; last_result?: CheckResult
  /** Accounts with a saved vendor discovery key. */
  sources?: { account_id: string; vendor: string }[]
  observations?: Observation[]
}
export interface LinePick { line: string | null; effort: string | null; harness?: string; model?: string }
/** One use of a line. situation (AEON-1163): the order's own situation; a rule, which applies to its whole row, has none. */
export interface LineUse { column: string; layer: 'default' | 'person' | 'workspace' | 'project'; situation?: string; person?: string; replacement: LinePick }
export interface LineUsage { used_by: LineUse[]; replacement: LinePick; revision: string; incomplete: boolean; line_profiles?: RegistryProfile[] }
export interface LineWrite { display_name: string; note: string; efforts: string[]; route: string; model?: string; revision?: string }

/** A failed request. `retryAfter` is the manual-check cooldown in seconds (429). */
export class RegistryError extends Error {
  readonly status: number
  readonly retryAfter: number | null
  constructor(status: number, message: string, retryAfter: number | null = null) { super(message); this.status = status; this.retryAfter = retryAfter }
}

async function request<T>(path: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await api(path, { method, ...(signal ? { signal } : {}), ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  if (!response.ok) {
    const data = await response.json().catch(() => ({})) as { error?: unknown; message?: unknown; retry_after?: unknown }
    const header = Number(response.headers.get('Retry-After'))
    const after = typeof data.retry_after === 'number' ? data.retry_after : Number.isFinite(header) && header > 0 ? header : null
    throw new RegistryError(response.status, typeof data.error === 'string' ? data.error : typeof data.message === 'string' ? data.message : `Request failed (${response.status})`, response.status === 429 ? after : null)
  }
  return response.json() as Promise<T>
}

// The registry holds a few hundred profiles; anything else is not an answer this page can show honestly.
const MAX_PROFILES = 2048
export async function listProfiles(signal?: AbortSignal): Promise<RegistryProfile[]> {
  const list = await request<unknown>('/models', 'GET', undefined, signal)
  if (!Array.isArray(list) || list.length > MAX_PROFILES) throw new RegistryError(502, 'The model list was not in the expected form')
  return list as RegistryProfile[]
}
export async function getRefreshStatus(signal?: AbortSignal): Promise<RefreshStatus> {
  const status = await request<RefreshStatus>('/models/refresh', 'GET', undefined, signal)
  if (typeof status?.settings?.auto_add_profiles !== 'boolean' || typeof status.settings.interval_minutes !== 'number') throw new RegistryError(502, 'The refresh status was not in the expected form')
  return status
}
// The whole settings object is written back so the parked settings (agent reports, discovery, interval) keep their values.
// A change of auto_add_profiles is the "New model versions" rule (AEON-1044):
// it carries the account-use matrix revision and needs account.use.manage.
export const putRefreshSettings = (settings: RefreshSettings, accountUseRevision?: number) =>
  request<RefreshSettings>('/models/refresh/settings', 'PUT', accountUseRevision ? { ...settings, account_use_revision: accountUseRevision } : settings)
export const checkNow = (signal?: AbortSignal) => request<CheckResult>('/models/refresh', 'POST', undefined, signal)
export const createProfile = (body: Record<string, unknown>, signal?: AbortSignal) => request<RegistryProfile>('/models', 'POST', body, signal)
const linePath = (harness: string, model: string) => `/models/lines/${encodeURIComponent(harness)}/${encodeURIComponent(model)}`
export const editLine = (harness: string, model: string, body: LineWrite, signal?: AbortSignal) => request<{ profiles: RegistryProfile[]; revision: string }>(linePath(harness, model), 'PUT', body, signal)
/**
 * Where a line is used and what takes over. With profiles it previews everything removed in the same step (AEON-1163),
 * every route of one version, so a route that goes with it is never named as the fallback.
 */
export const previewRemoval = (harness: string, model: string, signal?: AbortSignal, profiles: readonly string[] = []) =>
  request<LineUsage>(`${linePath(harness, model)}/usage${profiles.length ? `?${profiles.map(id => `profile=${encodeURIComponent(id)}`).join('&')}` : ''}`, 'GET', undefined, signal)
export const retireProfile = (id: string, reason: string, signal?: AbortSignal) => request<{ profile_id: string; retired: boolean }>(`/models/${encodeURIComponent(id)}/retire`, 'POST', { reason }, signal)
export const restoreProfile = (id: string, signal?: AbortSignal) => request<{ profile_id: string; retired: boolean }>(`/models/${encodeURIComponent(id)}/retire`, 'DELETE', undefined, signal)
/** Pauses every native level of one harness/model route until a time, or until resumed (null). */
export const putPause = (harness: string, model: string, until: string | null, reason: string, signal?: AbortSignal) =>
  request<{ harness: string; model: string; pause: ModelPause | null }>(`${linePath(harness, model)}/pause`, 'PUT', { until, reason }, signal)
export const deletePause = (harness: string, model: string, signal?: AbortSignal) =>
  request<{ harness: string; model: string; pause: ModelPause | null }>(`${linePath(harness, model)}/pause`, 'DELETE', undefined, signal)
/** Saves (or, with an empty key, removes) the vendor key an account uses to read its vendor's model list. */
export const putDiscoveryKey = (accountId: string, vendor: string, key: string, signal?: AbortSignal) =>
  request<unknown>(`/models/refresh/credentials/${encodeURIComponent(accountId)}`, 'PUT', { vendor, api_key: key }, signal)
export const acceptProposal = (observation: Pick<Observation, 'harness' | 'model' | 'effort'>, signal?: AbortSignal) =>
  request<RegistryProfile>('/models/proposals/accept', 'POST', { harness: observation.harness, model: observation.model, effort: observation.effort }, signal)

// ---------- lines ----------

export interface RegistryLine {
  key: string; harness: Harness; model: string; name: string; displayName: string; note: string; efforts: string[]; route: string; source: 'auto' | 'manual'; origin?: RegistryProfile['origin']
  enabled: boolean; isNew: boolean; retireAt: string | null; took: { at: string; was: string } | null; profileIds: string[]
}
export const lineKey = (harness: string, model: string) => `${harness}\u0000${model}`
const lineVersion = (profile: RegistryProfile) => profileLine(profile).version

/** The provider name the server expects for this model (it must match the model's namespace). */
export function routeOf(harness: Harness, model: string): string {
  const fixed: Partial<Record<Harness, string>> = { codex: 'openai', claude: 'anthropic', grok: 'xai', cursor: 'cursor', gemini: 'google' }
  if (fixed[harness]) return fixed[harness]!
  return model.includes('/') ? model.slice(0, model.indexOf('/')).toLowerCase() : 'unknown'
}
const ROUTE_LABEL: Record<string, string> = { openai: 'OpenAI', anthropic: 'Anthropic', xai: 'xAI', google: 'Google', cursor: 'Cursor', openrouter: 'OpenRouter', ollama: 'Local' }
export const routeLabel = (route: string) => ROUTE_LABEL[route] ?? ''

/** Routes the form offers per harness. Single-route harnesses have no choice to make. */
export function routesFor(harness: Harness): string[] {
  if (harness === 'pi') return ['openrouter', 'anthropic', 'openai', 'ollama']
  if (harness === 'opencode') return ['openrouter', 'ollama']
  return [routeOf(harness, '')]
}

export function buildLines(profiles: RegistryProfile[]): RegistryLine[] {
  const groups = new Map<string, RegistryProfile[]>()
  for (const profile of profiles) {
    if (profile.retired || !isHarness(profile.harness)) continue
    const key = lineKey(profile.harness, profile.model)
    const group = groups.get(key)
    if (group) group.push(profile); else groups.set(key, [profile])
  }
  const lines: RegistryLine[] = []
  for (const [key, group] of groups) {
    const ordered = [...group].sort((a, b) => (a.effort_level ?? 99) - (b.effort_level ?? 99) || a.created_at.localeCompare(b.created_at) || a.effort.localeCompare(b.effort))
    const first = ordered[0]!, harness = first.harness as Harness
    const retiring = group.map(profile => profile.retire_at).filter((at): at is string => !!at).sort()[0] ?? null
    const accepted = group.filter(profile => profile.version.endsWith('-accepted')).map(profile => profile.created_at).sort()[0]
    const seen = new Set<string>()
    const enabled = group.some(profile => profile.enabled), source = group.some(profile => profile.source === 'manual') ? 'manual' : 'auto'
    lines.push({
      key, harness, model: first.model, source, origin: first.origin, enabled, retireAt: retiring,
      name: fullName(first),
      displayName: first.display_name || first.model,
      note: group.find(profile => profile.note)?.note ?? '',
      efforts: ordered.map(profile => profile.effort).filter(effort => !seen.has(effort) && !!seen.add(effort)),
      route: routeOf(harness, first.model),
      isNew: !enabled && source === 'auto',
      took: accepted ? { at: accepted, was: predecessorVersion(first, profiles) } : null,
      profileIds: ordered.map(profile => profile.id),
    })
  }
  // Newest version first inside a harness, then by name, so a line and its successors read together.
  return lines.sort((a, b) => HARNESS_ORDER.indexOf(a.harness) - HARNESS_ORDER.indexOf(b.harness)
    || compareModelVersions(versionOfLine(b, profiles), versionOfLine(a, profiles)) || a.name.localeCompare(b.name))
}
function versionOfLine(line: RegistryLine, profiles: RegistryProfile[]): string {
  const profile = profiles.find(candidate => candidate.harness === line.harness && candidate.model === line.model)
  return profile ? lineVersion(profile) : ''
}
// The version this line replaced: the newest lower version of the same vendor line, retired or not.
function predecessorVersion(taken: RegistryProfile, profiles: RegistryProfile[]): string {
  const mine = profileLine(taken)
  if (!mine.version) return ''
  let best = ''
  for (const candidate of profiles) {
    if (candidate.harness !== taken.harness || candidate.family !== taken.family || candidate.model === taken.model) continue
    const other = profileLine(candidate)
    if (other.line !== mine.line || !other.version || other.version === 'alias') continue
    if (compareModelVersions(other.version, mine.version) < 0 && (!best || compareModelVersions(other.version, best) > 0)) best = other.version
  }
  return best
}

// ---------- the registry: provider → line → version, routes as sub-rows (AEON-1163) ----------

/** One way to run a model version: a harness with its model id, the route's own native levels and where they came from. */
export interface RegistryRoute {
  key: string; harness: Harness
  /** The id the harness is given. Cursor's per-level ids show as one, with ‹level› where the level goes. */
  id: string
  /** Every model id behind the route: one, or one per level for Cursor. */
  models: string[]
  /** The provider pi and OpenCode reach it through ("OpenRouter", "Local"); empty when the harness has one. */
  via: string
  /** Native level names, lowest first. */
  efforts: string[]
  /** The levels are thinking-token budgets (Gemini 2.5), not named levels. */
  budget: boolean
  source: FactSource | null
  /** Added by hand: the only routes an admin edits. Provider and harness facts are read-only. */
  hand: boolean
  /** Set when every id of the route is paused; partlyPaused when only some are. */
  pause: ModelPause | null; partlyPaused: boolean
  /** The registry lines (harness + model id) behind the route, for Edit, the remove preview and removal. */
  lineKeys: string[]
  profileIds: string[]
  enabled: boolean
}
/** One version of a line, with every route that runs it. */
export interface RegistryModel {
  key: string; line: string; version: string; provider: string; family: string
  /** "5.5"; empty when the registry names no version. */
  versionLabel: string
  /** "Opus 5.5": the model's name, said the same way on its row, in the picker, the tray and the sheet (fullName, AEON-1240). */
  name: string
  note: string; routes: RegistryRoute[]; source: FactSource | null; lifecycle: Lifecycle | null
  /** Superseded, deprecated, gone from its source, or retiring with a named successor. */
  old: boolean
  enabled: boolean; isNew: boolean; retireAt: string | null; took: { at: string; was: string } | null
  profileIds: string[]
}
/** A row of the registry: a line in its current version, its older versions folded under it. */
export interface RegistryRow { key: string; model: RegistryModel; older: RegistryModel[] }
export interface ProviderGroup { provider: string; label: string; rows: RegistryRow[] }
export interface Registry { groups: ProviderGroup[]; models: number; routes: number; rows: number }

const harnessRank = (harness: string) => { const index = (HARNESS_ORDER as string[]).indexOf(harness); return index < 0 ? HARNESS_ORDER.length : index }
const NAMED_LEVEL: Record<string, number> = { off: 0, none: 0, default: 0, minimal: 1, low: 1, medium: 2, high: 3, xhigh: 4, max: 5, ultra: 5 }
const levelRank = (profile: RegistryProfile) => profile.effort_level ?? NAMED_LEVEL[profile.effort] ?? 2
const BUDGET = /^\d+k?$/i
// Cursor names a level in the model id ("grok-4.7-high", "grok-4.7-high-fast"); those ids are one route.
const CURSOR_LEVEL = /^(.+?)(?:-(?:low|medium|high|xhigh|max|ultra))?(-fast)?$/
function routeOfProfile(profile: RegistryProfile): { key: string; base: string; fast: string } {
  if (profile.harness !== 'cursor') return { key: lineKey(profile.harness, profile.model), base: profile.model, fast: '' }
  const match = CURSOR_LEVEL.exec(profile.model)
  const base = match?.[1] ?? profile.model, fast = match?.[2] ?? ''
  return { key: `cursor\u0000${base}${fast}`, base, fast }
}
const isHand = (profile: RegistryProfile) => profileSource(profile) === 'manual'
const OLD_STATES = ['superseded', 'deprecated', 'vanished']
/** Old: its source or the version rule says a newer version replaces it, or it retires with a successor named. */
export const isOld = (lifecycle: Lifecycle | null) => !!lifecycle?.state && (OLD_STATES.includes(lifecycle.state) || (lifecycle.state === 'retiring' && !!lifecycle.successor))

function routesOf(items: RegistryProfile[]): RegistryRoute[] {
  const groups = new Map<string, { profiles: RegistryProfile[]; base: string; fast: string }>()
  for (const profile of items) {
    const { key, base, fast } = routeOfProfile(profile)
    const group = groups.get(key)
    if (group) group.profiles.push(profile); else groups.set(key, { profiles: [profile], base, fast })
  }
  const routes: RegistryRoute[] = []
  for (const [key, { profiles, base, fast }] of groups) {
    const first = profiles[0]!, harness = first.harness as Harness
    const ordered = [...profiles].sort((a, b) => levelRank(a) - levelRank(b) || (Number.parseFloat(a.effort) || 0) - (Number.parseFloat(b.effort) || 0) || a.effort.localeCompare(b.effort))
    const efforts = [...new Set(ordered.map(profile => profile.effort))]
    const models = [...new Set(profiles.map(profile => profile.model))]
    const pauses = models.map(model => profiles.find(profile => profile.model === model && profile.pause)?.pause ?? null)
    const label = harness === 'pi' || harness === 'opencode' ? routeLabel(routeOf(harness, first.model)) : ''
    routes.push({
      key, harness, id: models.length > 1 ? `${base}-‹level›${fast}` : first.model, models, via: label, efforts,
      budget: efforts.length > 0 && efforts.every(effort => BUDGET.test(effort)) && efforts.some(effort => effort !== '0'),
      source: profiles.map(profileSource).find(Boolean) ?? null, hand: profiles.some(isHand),
      pause: pauses.every(Boolean) ? pauses[0]! : null, partlyPaused: pauses.some(Boolean) && !pauses.every(Boolean),
      lineKeys: models.map(model => lineKey(harness, model)), profileIds: ordered.map(profile => profile.id), enabled: profiles.some(profile => profile.enabled),
    })
  }
  // Array sort is stable: routes of one harness keep the registry's order.
  return routes.sort((a, b) => harnessRank(a.harness) - harnessRank(b.harness))
}

function modelOf(line: string, version: string, items: RegistryProfile[], lines: Map<string, RegistryLine>, now: Date): RegistryModel {
  const ordered = [...items].sort((a, b) => harnessRank(a.harness) - harnessRank(b.harness))
  // A concrete route names the model: an alias ("opus") names no version, and Cursor's own display can be a raw id.
  const named = ordered.find(profile => modelLine(profile).version !== 'alias') ?? ordered[0]!
  const lifecycle = [named, ...ordered].map(profile => profile.lifecycle).find(found => !!found?.state) ?? ordered.map(profile => profile.lifecycle).find(Boolean) ?? null
  const scheduled = items.map(profile => profile.retire_at).filter((at): at is string => !!at && Date.parse(at) > now.getTime())
  if (lifecycle?.state === 'retiring' && lifecycle.date && !lifecycle.successor && Date.parse(lifecycle.date) > now.getTime()) scheduled.push(lifecycle.date)
  const enabled = items.some(profile => profile.enabled), source = profileSource(named)
  return {
    key: `${line}@${version}`, line, version, provider: named.provider || named.family || 'unknown', family: named.family,
    versionLabel: named.model_version || (version === 'alias' ? '' : version), name: fullName(named),
    note: ordered.map(profile => profile.note ?? '').find(note => note.trim()) ?? '',
    routes: routesOf(items), source, lifecycle, old: isOld(lifecycle),
    enabled, isNew: !enabled && source !== 'manual', retireAt: scheduled.sort()[0] ?? null,
    took: lines.get(lineKey(named.harness, named.model))?.took ?? null,
    profileIds: items.map(profile => profile.id),
  }
}

/**
 * The registry as the page shows it: providers in their fixed order, one row per line in its current version
 * (the newest that runs and is not old), older versions folded under it. A newer version that does not run yet
 * ("New · off until used") keeps a row of its own, so it can be paused or removed. Lines keep the registry's order.
 */
export function buildRegistry(profiles: RegistryProfile[], now = new Date()): Registry {
  const lines = new Map(buildLines(profiles).map(line => [line.key, line] as const))
  const byLine = new Map<string, { order: number; versions: Map<string, RegistryProfile[]> }>()
  profiles.forEach((profile, order) => {
    if (profile.retired || !isHarness(profile.harness)) return
    const { line, version } = modelLine(profile)
    const entry = byLine.get(line) ?? { order, versions: new Map<string, RegistryProfile[]>() }
    entry.versions.set(version, [...(entry.versions.get(version) ?? []), profile])
    byLine.set(line, entry)
  })
  const rows: { provider: string; order: number; row: RegistryRow }[] = []
  let models = 0, routes = 0
  for (const [line, { order, versions }] of byLine) {
    const alias = versions.get('alias') ?? []
    const concrete = [...versions.keys()].filter(version => version !== 'alias').sort((a, b) => compareModelVersions(b, a))
    if (!concrete.length) concrete.push('alias')
    const built = concrete.map(version => modelOf(line, version, versions.get(version) ?? [], lines, now))
    let current = built.findIndex(model => model.enabled && !model.old)
    if (current < 0) current = built.findIndex(model => model.enabled)
    if (current < 0) current = 0
    // An alias follows the current version: it is one more route of it.
    if (alias.length && concrete[0] !== 'alias') built[current] = modelOf(line, concrete[current]!, [...(versions.get(concrete[current]!) ?? []), ...alias], lines, now)
    built.forEach((model, index) => {
      models++; routes += model.routes.length
      if (index < current) rows.push({ provider: model.provider, order, row: { key: model.key, model, older: [] } })
    })
    const model = built[current]!
    rows.push({ provider: model.provider, order, row: { key: line, model, older: built.slice(current + 1) } })
  }
  const groups = new Map<string, ProviderGroup>()
  for (const { provider, row } of rows.sort((a, b) => providerRank(a.provider) - providerRank(b.provider) || a.order - b.order)) {
    const group = groups.get(provider) ?? { provider, label: providerLabel(provider), rows: [] }
    group.rows.push(row)
    groups.set(provider, group)
  }
  return { groups: [...groups.values()], models, routes, rows: rows.length }
}

/** "29 models · 35 routes · 17 lines": the size of the registry above the list. */
export function registryCount(registry: Registry): string {
  const n = (count: number, one: string) => `${count} ${one}${count === 1 ? '' : 's'}`
  return [n(registry.models, 'model'), n(registry.routes, 'route'), n(registry.rows, 'line')].join(' · ')
}

/** The level chips' names: a token budget says so once ("1024 · 8192 tokens"). */
export const levelUnit = (route: Pick<RegistryRoute, 'budget'>) => route.budget ? 'tokens' : ''

const STATE_TEXT: Record<string, string> = { active: 'Active', preview: 'Preview', superseded: 'Superseded', deprecated: 'Deprecated', vanished: 'No longer in its source’s model list' }
/** The lifecycle fact in words, or null when nobody reported one (the page then says "unknown"). */
export function lifecycleText(lifecycle: Lifecycle | null, now = new Date()): string | null {
  if (!lifecycle?.state) return null
  const date = lifecycle.date ? shortDate(lifecycle.date, now) : ''
  if (lifecycle.state === 'retiring') return date ? `Retiring ${date}` : 'Retiring'
  if (lifecycle.state === 'deprecated' && date) return `Deprecated · retires ${date}`
  return STATE_TEXT[lifecycle.state] ?? lifecycle.state
}
/** Why an older version is old, in one plain sentence; the version rule when its source said nothing. */
export function olderWhy(model: RegistryModel, successorName: string, now = new Date()): string {
  if (!model.lifecycle?.state) return 'Older version in the same line'
  return outdatedWhy({ ...model.lifecycle }, successorName, now)
}
/** The model's name at a level ("Opus 5 · xhigh", "Gemini 2.5 Pro · 32k budget"); no level, just the name. */
export function atLevel(name: string, effort: string | null | undefined): string {
  if (!effort) return name
  return `${name} · ${BUDGET.test(effort) && effort !== '0' ? `${effort} budget` : effort}`
}

// ---------- pause ----------

export const PAUSED_REASON = 'Paused in Settings › Models'
export type PauseChoice = 'tomorrow' | 'week' | 'open'
const DAY = 86_400_000
/** When a pause ends: tomorrow at 04:00 local time, a week from now, or never by itself (null). */
export function pauseUntil(choice: PauseChoice, now = new Date()): string | null {
  if (choice === 'open') return null
  if (choice === 'week') return new Date(now.getTime() + 7 * DAY).toISOString()
  const at = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 4, 0, 0, 0)
  return at.toISOString()
}
export interface PauseOption { choice: PauseChoice | 'resume'; label: string; detail: string }
export function pauseOptions(paused: boolean, now = new Date()): PauseOption[] {
  return [
    ...(paused ? [{ choice: 'resume' as const, label: 'Resume now', detail: 'It can run again at once.' }] : []),
    { choice: 'tomorrow', label: 'Until tomorrow, 04:00', detail: 'It runs again by itself' },
    { choice: 'week', label: 'For a week', detail: `Until ${shortDate(pauseUntil('week', now)!, now)}` },
    { choice: 'open', label: 'Until I resume it', detail: 'No end date' },
  ]
}
/** "until 04:00", "until tomorrow, 04:00", "until 18 Oct" or "until resumed". */
export function untilText(until: string | null, now = new Date()): string {
  if (!until) return 'until resumed'
  const at = new Date(until)
  if (Number.isNaN(at.getTime())) return 'until resumed'
  const time = `${two(at.getHours())}:${two(at.getMinutes())}`
  if (at.toDateString() === now.toDateString()) return `until ${time}`
  if (at.toDateString() === new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1).toDateString()) return `until tomorrow, ${time}`
  return `until ${shortDate(until, now)}`
}
/** How a model's pause reads on its row: every route, one named route, or how many of them. */
export function pauseTag(model: RegistryModel, now = new Date()): string {
  const paused = model.routes.filter(route => route.pause)
  if (!paused.length) return model.routes.some(route => route.partlyPaused) ? 'Partly paused' : ''
  if (paused.length === model.routes.length) {
    // The model runs again as soon as its first route does.
    const ends = paused.map(route => route.pause!.until)
    const dated = ends.filter((until): until is string => !!until).sort()
    return `Paused ${untilText(dated.length === ends.length ? dated[0]! : dated.length ? dated[0]! : null, now)}`
  }
  return paused.length === 1 ? `${HARNESS_NAME[paused[0]!.harness]} paused` : `${paused.length} routes paused`
}
/** The ids one route's pause is written to. The server covers Cursor's Grok levels from any one of them (pause.go). */
export function pauseTargets(route: RegistryRoute, family: string): string[] {
  return route.harness === 'cursor' && family === 'xai' ? route.models.slice(0, 1) : route.models
}
export interface PauseStep { harness: Harness; model: string; before: ModelPause | null }
/**
 * Pauses (until, or null for until resumed) or resumes (resume) every route of a model, one id at a time, and says
 * how far it got. Each step remembers the route's pause before, so Undo can put exactly that back.
 */
export async function pauseModel(model: RegistryModel, family: string, change: { until: string | null } | 'resume', scope?: WriteScope): Promise<{ done: PauseStep[]; error: string | null }> {
  const done: PauseStep[] = []
  for (const route of model.routes) {
    for (const id of pauseTargets(route, family)) {
      try {
        stillCurrent(scope)
        if (change === 'resume') await deletePause(route.harness, id, scope?.signal)
        else await putPause(route.harness, id, change.until, PAUSED_REASON, scope?.signal)
        done.push({ harness: route.harness, model: id, before: route.pause })
      } catch (error) {
        stale(error, scope)
        return { done, error: error instanceof Error ? error.message : 'It could not be changed.' }
      }
    }
  }
  return { done, error: null }
}
/** Puts back what each step found: the earlier pause while it still lies ahead, otherwise no pause. */
export async function undoPause(steps: PauseStep[], now = new Date(), scope?: WriteScope): Promise<string | null> {
  let failed = 0
  for (const step of steps) {
    try {
      stillCurrent(scope)
      const before = step.before
      if (before && (before.until === null || Date.parse(before.until) > now.getTime())) await putPause(step.harness, step.model, before.until, before.reason || PAUSED_REASON, scope?.signal)
      else await deletePause(step.harness, step.model, scope?.signal)
    } catch (error) {
      stale(error, scope)
      failed++
    }
  }
  return failed ? `${failed} of ${steps.length} routes could not be put back` : null
}

// ---------- copy helpers ----------

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const two = (value: number) => String(value).padStart(2, '0')
/** "3 Oct", with the year when it is not this year. */
export function shortDate(iso: string, now = new Date()): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) return ''
  return `${at.getDate()} ${MONTHS[at.getMonth()]}${at.getFullYear() === now.getFullYear() ? '' : ` ${at.getFullYear()}`}`
}
/** "06:12" today, "3 Oct, 06:12" otherwise. */
export function lastChecked(iso: string | null, now = new Date()): string {
  if (!iso) return ''
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) return ''
  const time = `${two(at.getHours())}:${two(at.getMinutes())}`
  return at.toDateString() === now.toDateString() ? time : `${shortDate(iso, now)}, ${time}`
}
export const intervalLabel = (minutes: number) => minutes >= 60 && minutes % 60 === 0 ? `${minutes / 60} h` : `${minutes} min`
export const cooldownLabel = (seconds: number) => `again in ${Math.max(1, Math.ceil(seconds / 60))} min`

export function takenText(line: Pick<RegistryLine, 'took'>, now = new Date()): string {
  if (!line.took) return ''
  const date = shortDate(line.took.at, now)
  return line.took.was ? `was ${line.took.was}, taken on ${date}` : `taken on ${date}`
}
export type FactSource = 'shipped' | 'provider' | 'harness' | 'manual'
export const SOURCE_NAME: Record<FactSource, string> = { shipped: 'Shipped', provider: 'From provider', harness: 'From harness', manual: 'Added by hand' }
export const SOURCE_TIP: Record<FactSource, string> = {
  shipped: 'In the catalog PAIMOS ships with; no provider or harness has confirmed it yet.',
  provider: 'Read from the provider’s own model list. Read-only.',
  harness: 'Reported by the harness that runs it. Read-only.',
  manual: 'Added by an admin. Editable.',
}
/**
 * Where a profile came from. Older servers only tell auto from manual, and "auto" cannot say whether a provider,
 * a harness or the shipped catalog reported it, so it stays unknown rather than guessed.
 */
export function profileSource(profile: Pick<RegistryProfile, 'origin' | 'source'>): FactSource | null {
  if (profile.origin) return profile.origin
  return profile.source === 'manual' ? 'manual' : null
}

// ---------- the inline form ----------

export interface LineDraft { name: string; harness: Harness; route: string; slug: string; efforts: string[]; note: string; level: string }
export interface DraftErrors { name?: string; slug?: string; efforts?: string }
export const emptyDraft = (): LineDraft => ({ name: '', harness: 'pi', route: 'openrouter', slug: '', efforts: [], note: '', level: '' })
export const draftOf = (line: RegistryLine): LineDraft => ({ name: line.displayName, harness: line.harness, route: line.route, slug: line.model, efforts: [...line.efforts], note: line.note, level: '' })

const NAMESPACED: Harness[] = ['pi', 'opencode']
const SLUG = /^[A-Za-z0-9][A-Za-z0-9._:/-]*$/
const LEVEL = /^[A-Za-z0-9][A-Za-z0-9._:-]*$/
/** The model id that is stored: pi and OpenCode ids carry their route as the first segment. */
export function modelSlug(draft: Pick<LineDraft, 'harness' | 'route' | 'slug'>): string {
  const slug = draft.slug.trim()
  if (!NAMESPACED.includes(draft.harness) || !slug) return slug
  return slug.toLowerCase().startsWith(`${draft.route}/`) ? `${draft.route}/${slug.slice(draft.route.length + 1)}` : `${draft.route}/${slug}`
}
/** Level names typed or pasted, split on commas and whitespace, without repeats. */
export function splitLevels(text: string, existing: string[] = []): string[] {
  const out = [...existing]
  for (const name of text.split(/[\s,]+/)) if (name && !out.includes(name)) out.push(name)
  return out
}

export function validateDraft(draft: LineDraft, mode: 'new' | 'manual' | 'auto'): DraftErrors {
  const errors: DraftErrors = {}
  const name = draft.name.trim()
  if (!name) errors.name = 'Give it a name.'
  else if (name.length > 128 || /[\u0000-\u001f]/.test(name)) errors.name = 'Use a name of up to 128 characters, on one line.'
  if (mode !== 'auto') {
    const typed = draft.slug.trim(), stored = modelSlug(draft)
    if (!typed) errors.slug = 'The model slug is needed.'
    else if (!SLUG.test(typed)) errors.slug = 'Letters, digits and . _ : / - only.'
    else if (/^sk-/i.test(typed)) errors.slug = 'That looks like a key, not a model slug.'
    else if (stored.length > 128) errors.slug = 'Use at most 128 characters.'
    else if (NAMESPACED.includes(draft.harness)) {
      // OpenRouter slugs are themselves vendor/model, so only other routes can be mistaken for a prefix.
      const other = draft.route === 'openrouter' ? undefined : routesFor(draft.harness).find(route => route !== draft.route && typed.toLowerCase().startsWith(`${route}/`))
      const rest = stored.slice(draft.route.length + 1)
      if (other) errors.slug = `This slug belongs to ${routeLabel(other)}; choose that route.`
      else if (!rest) errors.slug = 'The model slug is needed.'
      else if (!validPiModel(draft.route, rest)) errors.slug = draft.route === 'openrouter' ? 'OpenRouter slugs look like openrouter/vendor/model.' : 'Letters, digits and . _ : / - only.'
    }
  }
  const levels = splitLevels(draft.level, draft.efforts)
  if (!levels.length) errors.efforts = 'Add at least one level (e.g. off).'
  else if (levels.length > 16) errors.efforts = 'Use at most 16 levels.'
  else if (levels.some(level => level.length > 32 || !LEVEL.test(level))) errors.efforts = 'Level names use letters, digits and . _ : - only.'
  return errors
}
export const hasErrors = (errors: DraftErrors) => Object.keys(errors).length > 0

export function isDirty(draft: LineDraft, line: RegistryLine | null): boolean {
  const base = line ? draftOf(line) : emptyDraft()
  return draft.name !== base.name || draft.slug !== base.slug || draft.note !== base.note || draft.route !== base.route || draft.harness !== base.harness
    || draft.level.trim() !== '' || draft.efforts.join('\u0000') !== base.efforts.join('\u0000')
}

// ---------- writes ----------

/** The run that started a chained write: every request uses its signal and is refused once the captured person or workspace is gone. */
export interface WriteScope { signal?: AbortSignal; live?: () => boolean }
const current = (scope?: WriteScope) => !scope?.signal?.aborted && scope?.live?.() !== false
function stillCurrent(scope?: WriteScope) {
  if (!current(scope)) throw new StaleScopeError()
}
function stale(error: unknown, scope?: WriteScope) {
  if (error instanceof StaleScopeError || !current(scope)) throw error instanceof StaleScopeError ? error : new StaleScopeError()
}

const FAMILY_BY_NAMESPACE: Record<string, string> = { openai: 'openai', anthropic: 'anthropic', xai: 'xai', google: 'google', ollama: 'local' }
const CURSOR_FAMILIES: [string, string][] = [['gpt-', 'openai'], ['claude-', 'anthropic'], ['grok-', 'xai'], ['gemini-', 'google'], ['composer-', 'cursor']]
/** Mirrors the server's family check; a mismatch is rejected there, never trusted here. */
export function familyOf(harness: Harness, model: string): string {
  const fixed: Partial<Record<Harness, string>> = { codex: 'openai', claude: 'anthropic', grok: 'xai', gemini: 'google' }
  if (fixed[harness]) return fixed[harness]!
  if (harness === 'cursor') return CURSOR_FAMILIES.find(([prefix]) => model.startsWith(prefix) && model.length > prefix.length)?.[1] ?? 'unknown'
  const cut = model.indexOf('/')
  if (cut < 0 || cut === model.length - 1) return 'unknown'
  return FAMILY_BY_NAMESPACE[model.slice(0, cut)] ?? 'unknown'
}
const pinSlug = (harness: string, model: string, effort: string) => `hand-${harness}-${model}-${effort}`.toLowerCase().replace(/[^a-z0-9_-]+/g, '-').replace(/-+/g, '-').replace(/-$/, '').slice(0, 128).replace(/-$/, '')
const pinVersion = (now: Date) => now.toISOString().replace(/[-:]/g, '').replace('Z', '')

/** Result of Add: the profiles that exist afterwards, and what could not be saved. */
export interface AddResult { profiles: RegistryProfile[]; failed: string | null }

/**
 * A new model is one profile per thinking level. The first is created by POST;
 * the full ordered list is then registered in one step, which also keeps the
 * order of the person's own level names. A failure after the first step leaves
 * a line with its first level; the caller reports it, it is never called saved.
 */
export async function addLine(draft: LineDraft, now = new Date(), scope?: WriteScope): Promise<AddResult> {
  const model = modelSlug(draft), efforts = splitLevels(draft.level, draft.efforts), name = draft.name.trim(), note = draft.note.trim()
  stillCurrent(scope)
  const first = await createProfile({
    slug: pinSlug(draft.harness, model, efforts[0]!), version: pinVersion(now), harness: draft.harness, family: familyOf(draft.harness, model), model, effort: efforts[0], tier: 'standard',
    display_name: name, short_name: name, ...(note ? { note } : {}),
  }, scope?.signal)
  if (efforts.length === 1) return { profiles: [first], failed: null }
  try {
    stillCurrent(scope)
    const edited = await editLine(draft.harness, model, { display_name: name, note, efforts, route: routeOf(draft.harness, model) }, scope?.signal)
    return { profiles: edited.profiles, failed: null }
  } catch (error) {
    stale(error, scope)
    return { profiles: [first], failed: error instanceof Error ? error.message : 'The remaining levels could not be saved.' }
  }
}

export function lineWriteOf(draft: LineDraft, line: RegistryLine): LineWrite {
  // Only a route added by hand changes its id; any other keeps the one its source reported.
  const model = profileSource(line) === 'manual' ? modelSlug(draft) : line.model
  return { display_name: draft.name.trim(), note: draft.note.trim(), efforts: splitLevels(draft.level, draft.efforts), route: routeOf(line.harness, model), ...(model !== line.model ? { model } : {}) }
}

export function changeOf(line: RegistryLine, next: LineWrite): boolean {
  return next.display_name !== line.displayName || next.note !== line.note || next.efforts.join('\u0000') !== line.efforts.join('\u0000') || (next.model ?? line.model) !== line.model
}

// Stored with each retirement and shown in the registry's history.
export const REMOVED_REASON = 'Removed in Settings › Models'
export const UNDONE_REASON = 'Undone in Settings › Models'
/** Retire every level of a line; stops at the first failure and says how far it got. */
export async function removeLine(ids: string[], reason = REMOVED_REASON, scope?: WriteScope): Promise<{ done: string[]; error: string | null }> {
  const done: string[] = []
  for (const id of ids) {
    try { stillCurrent(scope); await retireProfile(id, reason, scope?.signal); done.push(id) } catch (error) {
      stale(error, scope)
      return { done, error: error instanceof Error ? error.message : 'Could not remove it.' }
    }
  }
  return { done, error: null }
}
export async function restoreLine(ids: string[], scope?: WriteScope): Promise<string | null> {
  let failed = 0
  for (const id of ids) {
    try { stillCurrent(scope); await restoreProfile(id, scope?.signal) } catch (error) {
      stale(error, scope)
      failed++
    }
  }
  return failed ? `${failed} of ${ids.length} levels could not be restored` : null
}

/** Distinct models the server accepted. A missing list means acceptance was not established; duplicates count once. */
export function acceptedModelCount(models: string[] | undefined): number {
  if (!Array.isArray(models)) return 0
  const seen = new Set<string>()
  for (const id of models) if (typeof id === 'string' && id && !seen.has(id)) seen.add(id)
  return seen.size
}

/** One read of a line: the revision and the profiles it was read with, fixed together so a later read can never pair its revision with these fields. */
export interface LineSnapshot { readonly revision: string; readonly line: Readonly<RegistryLine> }

/** The line and revision read together by one usage answer, or null when that answer did not bring its own profiles. */
export function snapshotFromUsage(current: RegistryProfile[], harness: string, model: string, usage: LineUsage): LineSnapshot | null {
  if (!Array.isArray(usage.line_profiles) || !usage.revision) return null
  const fresh = usage.line_profiles.filter(profile => profile && profile.harness === harness && profile.model === model && !profile.retired)
  if (!fresh.length) return null
  const kept = current.filter(profile => profile.retired || profile.harness !== harness || profile.model !== model)
  const line = buildLines(kept.concat(fresh)).find(candidate => candidate.harness === harness && candidate.model === model)
  if (!line) return null
  return Object.freeze({ revision: usage.revision, line: Object.freeze({ ...line, efforts: Object.freeze([...line.efforts]) as string[], profileIds: Object.freeze([...line.profileIds]) as string[] }) })
}

export function draftsMatch(a: LineDraft, b: LineDraft): boolean {
  return a.name === b.name && a.note === b.note && a.slug === b.slug && a.harness === b.harness && a.route === b.route && a.level === b.level && a.efforts.join('\u0000') === b.efforts.join('\u0000')
}

/** What Check now may say. A 200 can still be a partial or stale discovery, and a version taken on is not a new line. `added` counts profiles, so it is not acceptance. */
export function checkReport(result: CheckResult, apiEnabled: boolean): string {
  const sources = Array.isArray(result.sources) ? result.sources : []
  const limited = sources.some(source => source?.state === 'limited')
  const staleSource = sources.some(source => source?.state === 'stale')
  const found = result.new_lines?.length ?? 0
  const added = typeof result.added === 'number' && result.added > 0 ? result.added : 0
  const accepted = acceptedModelCount(result.accepted_models)
  const sentences: string[] = []
  if (found > 0) sentences.push(`${found} new ${found === 1 ? 'model' : 'models'} found`)
  else if (accepted > 0) sentences.push(accepted === 1 ? 'A newer version already in use was taken on' : `${accepted} newer versions already in use were taken on`)
  else if (added > 0) sentences.push(added === 1 ? '1 profile was added' : `${added} profiles were added`)
  else sentences.push(apiEnabled ? 'Up to date · nothing new' : 'Checked · vendor model lists are off, so nothing new could be found')
  if (limited || staleSource) {
    const discovery = limited && staleSource
      ? 'Discovery was partial: some lists were incomplete and some could not be checked'
      : limited
        ? 'Discovery was partial: not every model could be read'
        : 'Discovery was incomplete: some model lists could not be checked'
    if (sentences.length === 1 && (sentences[0]!.startsWith('Up to date') || sentences[0]!.startsWith('Checked · vendor'))) sentences[0] = discovery
    else sentences.push(discovery)
  }
  return sentences.join('. ')
}

function fallbackText(pick: LinePick | undefined, nameOf: (pick: LinePick) => string): string {
  const named = pick?.model ? nameOf(pick) : pick?.line ?? ''
  if (!named) return 'no qualified fallback'
  return pick?.effort ? `${named} at ${pick.effort} takes over` : `${named} takes over, with no effort named`
}

/** One sentence per visible use. Each use keeps its own replacement; a null replacement is said plainly. */
export function removalText(name: string, source: 'auto' | 'manual', usage: LineUsage, kinds: Record<string, string>, me: string, nameOf: (pick: LinePick) => string): string {
  const auto = source === 'auto' ? ' Auto-update won’t add it back.' : ''
  const hidden = usage.incomplete ? ' Other people’s choices are not shown.' : ''
  if (!usage.used_by.length) return `Remove ${name}? ${usage.incomplete ? 'Nothing you can see uses it.' : 'Nothing uses it.'}${usage.incomplete ? hidden : ''}${auto}`
  const parts = usage.used_by.map(use => `${useText(use, kinds, me)} — ${fallbackText(use.replacement, nameOf)}`)
  return `${name} is in use: ${parts.join('; ')}.${hidden}${auto}`
}

// ---------- the remove preview ----------

const columnName = (column: string, kinds: Record<string, string>) => column === 'other' ? 'Default · all work' : column === 'concept' ? 'Concepts' : column.startsWith('review:') ? 'Reviews' : kinds[column] ?? column
const layerName = (use: LineUse, me: string) => use.layer === 'person' ? (use.person && use.person === me ? 'yours' : 'another person') : use.layer === 'project' ? 'a project' : 'for everyone'
/** A use names its situation the way the outdated sheet does ("Review · Default"); the first build is the row itself. */
export function useText(use: LineUse, kinds: Record<string, string>, me: string) {
  const where = use.situation && use.situation !== 'first' ? `${SITUATION[use.situation] ?? use.situation} · ${use.column === 'other' ? 'Default' : kindName(use.column, kinds)}` : columnName(use.column, kinds)
  return `${where} (${layerName(use, me)})`
}

// ---------- outdated versions: the tray and the review sheet (AEON-1164) ----------

/** One use that moves. from_effort/to_effort (AEON-1163): the level it runs the old version at and the one it moves to; absent from older servers. */
export interface OutdatedUse { label: string; who: string; from: string; to: string | null; from_effort?: string | null; to_effort?: string | null }
export interface OutdatedVersion {
  version: string; line: string; state: string | null; date: string | null; successor: string | null; reason: string; source: string
  uses: OutdatedUse[]; incomplete: boolean
}
export interface OutdatedDocument { versions: OutdatedVersion[]; revision: string; next_cursor: string; incomplete: boolean }
export interface OutdatedRetired { moved: OutdatedUse[]; removed: string[]; undo_token: string }

// One page is one batch: the server retires at most fifty versions in one step.
export const OUTDATED_LIMIT = 50
// The server reads the outdated list only while the registry holds at most 512 profiles, so eleven pages of fifty reach every version in it.
export const OUTDATED_PAGES = 11
const isUse = (use: unknown): use is OutdatedUse => !!use && typeof (use as OutdatedUse).label === 'string' && typeof (use as OutdatedUse).who === 'string' && typeof (use as OutdatedUse).from === 'string'
export async function readOutdated(signal?: AbortSignal, cursor = ''): Promise<OutdatedDocument> {
  const doc = await request<OutdatedDocument>(`/models/outdated?limit=${OUTDATED_LIMIT}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`, 'GET', undefined, signal)
  const valid = !!doc && Array.isArray(doc.versions) && doc.versions.length <= OUTDATED_LIMIT && typeof doc.revision === 'string' && doc.revision.length === 64
    && typeof doc.next_cursor === 'string' && doc.next_cursor.length <= 200
    && doc.versions.every(version => typeof version?.version === 'string' && Array.isArray(version.uses) && version.uses.every(isUse))
  if (!valid) throw new RegistryError(502, 'The outdated list was not in the expected form')
  return doc
}
/**
 * Reads on past the first page until the list holds a full batch the sheet can offer, so versions without a usable
 * successor on early pages never hide later ones. A retirement is bound to one revision: a page read at another one,
 * or a page that cannot be read, ends the walk, and the cursor left over says more old versions exist.
 */
export async function readOutdatedBatch(first: OutdatedDocument, profiles: RegistryProfile[], signal?: AbortSignal): Promise<OutdatedDocument> {
  let doc = first
  for (let pages = 1; doc.next_cursor && pages < OUTDATED_PAGES && outdatedEntries(doc, profiles).length < OUTDATED_LIMIT; pages++) {
    let next: OutdatedDocument
    try { next = await readOutdated(signal, doc.next_cursor) } catch (error) { if (signal?.aborted) throw error; break }
    if (next.revision !== first.revision) break
    doc = { versions: [...doc.versions, ...next.versions], revision: first.revision, next_cursor: next.next_cursor, incomplete: doc.incomplete || next.incomplete }
  }
  return doc
}
/** Moves every use of the versions to their successors and removes them, in one step; the token undoes the whole step. */
export async function retireOutdated(versions: string[], revision: string, signal?: AbortSignal): Promise<OutdatedRetired> {
  const done = await request<OutdatedRetired>('/models/outdated/retire', 'POST', { versions, revision }, signal)
  if (!done || !Array.isArray(done.removed) || !Array.isArray(done.moved)) throw new RegistryError(502, 'The registry did not say what was removed')
  return done
}
export const undoOutdated = (token: string, signal?: AbortSignal) => request<{ event_id: number; restored: boolean }>('/models/outdated/undo', 'POST', { undo_token: token }, signal)
/**
 * The server answered and refused, so nothing was written. A timeout, a lost or unreadable answer and a gateway
 * error are not refusals: the write may have happened.
 */
export const refused = (error: unknown): error is RegistryError => error instanceof RegistryError && error.status >= 400 && error.status < 500

/** An old version as the page shows it: names instead of identities, the reason in plain words, and every use that moves. */
export interface OutdatedEntry {
  version: string; name: string; successor: string; successorName: string; harness: Harness | null; route: string
  routes: string[]; why: string; source: FactSource | null; uses: OutdatedUse[]; incomplete: boolean
  /** Where each use moves, by name as everywhere else: its successor, or the version the server names for it. */
  targets: string[]
}

const identityOf = (profile: RegistryProfile) => profile.identity ?? ''
const modelPart = (identity: string) => identity.slice(identity.indexOf(':') + 1)
// The tray and the sheet name a version as the picker and the registry rows do (fullName); a concrete route names it.
function nameOfIdentity(identity: string, profiles: RegistryProfile[]): string {
  const found = profiles.filter(candidate => identityOf(candidate) === identity).sort((a, b) => harnessRank(a.harness) - harnessRank(b.harness))
  const profile = found.find(candidate => modelLine(candidate).version !== 'alias') ?? found[0]
  return profile ? fullName(profile) : modelPart(identity)
}

/** Where a lifecycle fact came from. A version rule or a source nobody named has no badge. */
export function lifecycleSource(source: string): FactSource | null {
  if (/^https?:\/\//i.test(source)) return 'provider'
  if (source.startsWith('agent-report:')) return 'harness'
  if (source === 'offline-bootstrap') return 'shipped'
  return null
}
const SAME_LINE_RULE = 'aeon_model_line/aeon_model_version_newer'
/** Why a version is old, in one plain sentence. */
export function outdatedWhy(version: Pick<OutdatedVersion, 'state' | 'date' | 'source' | 'reason'>, successorName: string, now = new Date()): string {
  const date = version.date ? shortDate(version.date, now) : ''
  const past = !!version.date && new Date(version.date).getTime() <= now.getTime()
  if (version.state === 'retiring') return date ? `${past ? 'Retired' : 'Retires'} on ${date}` : 'Its source says it is retiring'
  if (version.state === 'deprecated') return date ? `Deprecated ${past ? 'since' : 'from'} ${date}` : 'Its source marks it deprecated'
  if (version.state === 'vanished') return 'No longer in its source’s model list'
  if (version.source === SAME_LINE_RULE) return 'Newer version in the same line'
  if (version.reason === 'Source reports a successor') return `Its source names ${successorName} as the next version`
  return 'Its source marks it superseded'
}

/**
 * The versions the tray and sheet offer: each needs a successor that is registered, and one that
 * can run when something still uses the old version, because every use moves there first.
 */
export function outdatedEntries(doc: OutdatedDocument, profiles: RegistryProfile[], now = new Date()): OutdatedEntry[] {
  const active = profiles.filter(profile => !profile.retired && isHarness(profile.harness))
  const entries: OutdatedEntry[] = []
  for (const version of doc.versions) {
    if (!version.successor || version.successor === version.version) continue
    const next = active.filter(profile => identityOf(profile) === version.successor)
    if (!next.length || (version.uses.length > 0 && !next.some(profile => profile.enabled))) continue
    const mine = active.filter(profile => identityOf(profile) === version.version)
    const successorName = nameOfIdentity(version.successor, active)
    const routes = [...new Set(mine.map(profile => `${HARNESS_NAME[profile.harness as Harness]} · ${profile.model}`))]
    const first = mine[0]
    entries.push({
      version: version.version, name: nameOfIdentity(version.version, mine.length ? mine : profiles), successor: version.successor, successorName,
      harness: first ? first.harness as Harness : null, route: first ? routeOf(first.harness as Harness, first.model) : '',
      routes, why: outdatedWhy(version, successorName, now), source: lifecycleSource(version.source), uses: version.uses, incomplete: version.incomplete,
      targets: version.uses.map(use => !use.to || use.to === version.successor ? successorName : nameOfIdentity(use.to, active)),
    })
  }
  return entries
}
/** What the tray and the sheet offer: at most one batch the server retires in one step, and whether more old versions remain. */
export function outdatedBatch(doc: OutdatedDocument, profiles: RegistryProfile[], now = new Date()): { entries: OutdatedEntry[]; more: boolean } {
  const all = outdatedEntries(doc, profiles, now)
  return { entries: all.slice(0, OUTDATED_LIMIT), more: !!doc.next_cursor || all.length > OUTDATED_LIMIT }
}

export interface OutdatedTray { named: { from: string; to: string }[]; count: string; unused: string }
/** The tray line: the two successors that take over most uses, how many old versions there are, and whether anything uses them. */
export function outdatedTray(entries: OutdatedEntry[], more: boolean, incomplete: boolean): OutdatedTray {
  const named = entries.map((entry, index) => ({ entry, index })).sort((a, b) => b.entry.uses.length - a.entry.uses.length || a.index - b.index)
    .slice(0, 2).map(({ entry }) => ({ from: entry.name, to: entry.successorName }))
  const n = entries.length
  const used = entries.some(entry => entry.uses.length > 0)
  const hidden = incomplete || entries.some(entry => entry.incomplete)
  // The number stays on the line of its noun.
  return { named, count: `${n}${more ? '+' : ''}\u00a0old version${n === 1 && !more ? '' : 's'}`, unused: used ? '' : hidden ? ', nothing you can see uses them' : `, nothing uses ${n === 1 && !more ? 'it' : 'them'}` }
}

const SITUATION: Record<string, string> = { small: 'First build', fix: 'Fix rounds', stuck: 'Stuck', review: 'Review' }
const kindName = (column: string, kinds: Record<string, string>) => column === 'other' ? 'all work' : column === 'concept' ? 'Concepts' : column.startsWith('review:') ? 'Reviews' : kinds[column] ?? column
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
/** People named in uses, so the sheet can ask for their names; Default and empty mean the workspace. */
export const peopleInUses = (uses: OutdatedUse[]) => [...new Set(uses.map(use => use.who).filter(who => UUID.test(who)))]

/**
 * One use in plain words. The server sends its own labels ("Default · other · first",
 * "lock · top · backend"); the page writes what a person reads. An unknown label is shown as sent.
 */
export function outdatedUseText(use: OutdatedUse, kinds: Record<string, string>, names: ReadonlyMap<string, string>, me: string): { title: string; who: string } {
  const person = (id: string) => id === me ? 'yours' : names.get(id) ?? 'another person'
  const owner = !use.who || use.who === 'Default' ? 'for everyone' : person(use.who)
  const parts = use.label.split(' · ')
  const [kind, a = '', b = ''] = parts
  const row = (column: string) => column === 'other' ? 'Default · all work' : kindName(column, kinds)
  const fallbackFor = (column: string) => `Fallback for ${column === 'other' ? 'the default' : kindName(column, kinds)}`
  const situated = (column: string, situation: string) => `${SITUATION[situation] ?? situation} · ${column === 'other' ? 'Default' : kindName(column, kinds)}`
  if ((kind === 'Default' || kind === 'Except for') && parts.length === 3) return b === 'first' ? { title: row(a), who: owner } : { title: situated(a, b), who: `${owner} · situation` }
  if (kind === 'fallback' && parts.length === 3) return b === 'first' ? { title: fallbackFor(a), who: owner } : { title: `Fallback for ${situated(a, b)}`, who: `${owner} · situation` }
  if (kind === 'never' && parts.length === 2) return { title: `Never for ${kindName(a, kinds)}`, who: `${owner} · the rule moves with it` }
  if (kind === 'lock' && parts.length === 3) {
    const setBy = UUID.test(use.who) ? ` · set by ${use.who === me ? 'you' : names.get(use.who) ?? 'another person'}` : ''
    if (a === 'top') return { title: row(b), who: `locked; the lock moves with it${setBy}` }
    if (a === 'bottom') return { title: fallbackFor(b), who: `last step; it moves with it${setBy}` }
    if (a === 'not') return { title: `Never for ${kindName(b, kinds)}`, who: `the rule moves with it${setBy}` }
  }
  if (kind === 'fallback' && parts.length === 2) return { title: `Older fallback list · ${a}`, who: owner }
  if (kind === 'pinned' && parts.length === 2) return { title: `Pinned pick · ${a}`, who: owner }
  return { title: use.label, who: owner }
}
