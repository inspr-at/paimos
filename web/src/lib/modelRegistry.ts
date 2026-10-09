// SPDX-License-Identifier: AGPL-3.0-only
// Settings › Models › Model registry (AEON-1012): one line per model, derived
// from the registry's immutable profiles (one profile per thinking level).
import { api } from './api'
import { StaleScopeError } from './identityScope'
import { compareModelVersions, profileLine } from './modelPrefs'
import { validPiModel } from './piModel'
import { fullModelName } from './planning'

export type Harness = 'codex' | 'claude' | 'grok' | 'gemini' | 'cursor' | 'pi' | 'opencode'
export const HARNESS_ORDER: Harness[] = ['codex', 'claude', 'grok', 'gemini', 'cursor', 'pi', 'opencode']
export const HARNESS_NAME: Record<Harness, string> = { codex: 'Codex', claude: 'Claude', grok: 'Grok', gemini: 'Gemini', cursor: 'Cursor', pi: 'pi', opencode: 'OpenCode' }
const isHarness = (value: string): value is Harness => (HARNESS_ORDER as string[]).includes(value)

export interface RegistryProfile {
  id: string; slug: string; version: string; harness: string; family: string; model: string; effort: string; tier: string; enabled: boolean; created_at: string
  display_name?: string; short_name?: string; model_version?: string; note?: string
  source?: 'auto' | 'manual'; retire_at?: string | null; retired?: boolean; effort_level?: number | null; provider?: string
}
export interface RefreshSettings { agent_reports_enabled: boolean; auto_add_profiles: boolean; api_enabled: boolean; interval_minutes: number }
export interface DiscoverySourceResult { state?: string; seen?: number; vendor?: string; account_id?: string }
export interface CheckResult { new_lines?: string[]; added?: number; proposed?: number; sources?: DiscoverySourceResult[] }
export interface RefreshStatus { settings: RefreshSettings; last_run_at: string | null; last_result?: CheckResult }
export interface LinePick { line: string | null; effort: string | null; harness?: string; model?: string }
export interface LineUse { column: string; layer: 'default' | 'person' | 'workspace' | 'project'; person?: string; replacement: LinePick }
export interface LineUsage { used_by: LineUse[]; replacement: LinePick; revision: string; incomplete: boolean }
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
export const putRefreshSettings = (settings: RefreshSettings) => request<RefreshSettings>('/models/refresh/settings', 'PUT', settings)
export const checkNow = (signal?: AbortSignal) => request<CheckResult>('/models/refresh', 'POST', undefined, signal)
export const createProfile = (body: Record<string, unknown>, signal?: AbortSignal) => request<RegistryProfile>('/models', 'POST', body, signal)
const linePath = (harness: string, model: string) => `/models/lines/${encodeURIComponent(harness)}/${encodeURIComponent(model)}`
export const editLine = (harness: string, model: string, body: LineWrite, signal?: AbortSignal) => request<{ profiles: RegistryProfile[]; revision: string }>(linePath(harness, model), 'PUT', body, signal)
export const previewRemoval = (harness: string, model: string, signal?: AbortSignal) => request<LineUsage>(`${linePath(harness, model)}/usage`, 'GET', undefined, signal)
export const retireProfile = (id: string, reason: string, signal?: AbortSignal) => request<{ profile_id: string; retired: boolean }>(`/models/${encodeURIComponent(id)}/retire`, 'POST', { reason }, signal)
export const restoreProfile = (id: string, signal?: AbortSignal) => request<{ profile_id: string; retired: boolean }>(`/models/${encodeURIComponent(id)}/retire`, 'DELETE', undefined, signal)

// ---------- lines ----------

export interface RegistryLine {
  key: string; harness: Harness; model: string; name: string; displayName: string; note: string; efforts: string[]; route: string; source: 'auto' | 'manual'
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
      key, harness, model: first.model, source, enabled, retireAt: retiring,
      name: fullModelName({ ...first, label: first.model }),
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

export function takenText(line: RegistryLine, now = new Date()): string {
  if (!line.took) return ''
  const date = shortDate(line.took.at, now)
  return line.took.was ? `was ${line.took.was}, taken on ${date}` : `taken on ${date}`
}
export function metaParts(line: RegistryLine, now = new Date()): string[] {
  const label = routeLabel(line.route)
  return [HARNESS_NAME[line.harness], ...(label && label !== HARNESS_NAME[line.harness] ? [label] : []), line.model, line.source === 'auto' ? 'Auto-discovered' : 'Added by hand', ...(line.took ? [takenText(line, now)] : [])]
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
  const model = line.source === 'auto' ? line.model : modelSlug(draft)
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

/** What Check now may say. A 200 can still be a partial or stale discovery, and a version taken on is not a new line. */
export function checkReport(result: CheckResult, apiEnabled: boolean): string {
  const sources = Array.isArray(result.sources) ? result.sources : []
  const limited = sources.some(source => source?.state === 'limited')
  const staleSource = sources.some(source => source?.state === 'stale')
  const found = result.new_lines?.length ?? 0
  const added = typeof result.added === 'number' && result.added > 0 ? result.added : 0
  const sentences: string[] = []
  if (found > 0) sentences.push(`${found} new ${found === 1 ? 'model' : 'models'} found`)
  else if (added > 0) sentences.push(added === 1 ? 'A newer version already in use was taken on' : `${added} newer versions already in use were taken on`)
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
export function useText(use: LineUse, kinds: Record<string, string>, me: string) { return `${columnName(use.column, kinds)} (${layerName(use, me)})` }
