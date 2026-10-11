// SPDX-License-Identifier: AGPL-3.0-only
// The minimal Models page (AEON-1011). Display and editing only: the server owns eligibility, account
// qualification and the automatic fallback. Everything here is pure so the card can be tested without a browser.
import { compareModelVersions, profileLine, type PrefProfile } from './modelPrefs'
import { harnessLabel } from './agentState'
import { HARNESS_NAME as ACCOUNT_NAME } from './capacity'

export type Scope = 'me' | 'default'
export interface SimplePick { line: string | null; effort: string | null; harness?: string; model?: string }
export interface SimpleLock { by: string | null; at: string | null; reason: string }
/** One step of a row's fallback, at its own native level (AEON-1161). */
export interface SimpleStep { line: string; effort: string | null }
/** A line a row never uses, even in someone's own order: a workspace rule (who, when, why) or a saved exclusion. */
export interface SimpleNever { line: string; by: string | null; at: string | null; why: string }
/** What a row does when none of its order can run: wait, or continue with the default's order. */
export type ChainEnd = 'wait' | 'default'
export interface SimpleRow extends SimplePick {
  column: string; mine: boolean; lock: SimpleLock | null
  /** Models v2 (AEON-1159). Absent only on an older server; read as an empty order. */
  chain?: SimpleStep[]; end?: ChainEnd; never?: SimpleNever[]
}
export interface SimpleTrace { line: string; stage: 'column' | 'default' | 'role'; role?: string; reason: string; selected: boolean }
export interface SimpleNext { column?: string; line?: string | null; effort?: string | null; ticket?: string; reviewer?: SimplePick; trace?: SimpleTrace[] }
/** Why a row's pick can't run right now, one cause per entry (AEON-1159). The page writes the words; reason is legacy. */
export type UnavailableCause = 'daily_cap' | 'paused' | 'no_tools' | 'no_account' | 'retired' | 'residency'
export interface SimpleUnavailable {
  column: string; line: string; runs_instead: string | null; effort: string | null; reason: string; ticket: string | null
  /** route is the harness of the skipped route, waits says nothing in the order can run. */
  cause?: UnavailableCause; until?: string | null; route?: string | null; waits?: boolean
  /** Harness of the route that runs instead; tells "runs through Cursor instead" apart from another model. */
  runs_via?: string | null
}
export interface SimpleNewLine { line: string; can_do: string[] }
export interface SimpleDocument {
  all: SimpleRow; exceptions: SimpleRow[]; reviews: { mode: 'cross_family' }; next: SimpleNext | null
  unavailable: SimpleUnavailable[]; new_lines: SimpleNewLine[]; revision: number; rules_revision: number; person_id: string | null
}
/** The part of the ranked board this page needs: the stored order behind each first pick, and what a column cannot do. */
export interface TailCard { line: string; lock?: { kind?: string; value?: string; scope?: string } | null }
/**
 * The order a first-pick rewrite must keep, including lines the resolved list omits. End and efforts are the column's
 * own order's ending and native levels (AEON-1161); absent when it has no order of its own or stores none.
 */
export interface StoredOrder { rank: string[]; not: string[]; end?: string; efforts?: Record<string, string> }
export interface TailColumn {
  column: string; label: string; fixed: boolean; hidden: boolean; source: 'own' | 'default' | 'template' | 'follows'
  list: TailCard[]; not: TailCard[]; cant: { line: string; reason: string }[]
  stored?: StoredOrder
  /** Native effort stored on this column's own order. Absent or null is not proof one was stored. */
  stored_effort?: string | null
}
export interface TailBoard { person_id: string | null; revision: number; profile: { dismissed_lines: string[] }; columns: TailColumn[] }
export interface OrderBody { rank: string[]; not: string[] }
export interface ModelRule { scope: 'workspace' | 'project'; project_id: string | null; column: string; line: string; lock: 'top' | 'bottom' | 'not'; position: number; why: string; set_by: string | null; set_at: string }
export interface RulesDocument { revision: number; rules: ModelRule[] }
export interface RulesBody { top: { line: string; why: string }[]; bottom: { line: string; why: string }[]; not: Record<string, string> }
export interface WriteResult { revision: number; person_id?: string | null }
export interface CatalogLifecycle { state: string | null; date: string | null; successor: string | null; reason: string; source: string }
export type RegistryProfile = PrefProfile & { effort_level?: number | null; note?: string; source?: 'auto' | 'manual'; retire_at?: string | null; retired?: boolean; short_name?: string; created_at?: string; version?: string; slug?: string; identity?: string; lifecycle?: CatalogLifecycle | null }

export const ALL_COLUMN = 'other'
/** The shared effort scale, used only when the server reports no level for a name. */
const NAMED_LEVEL: Record<string, number> = { off: 0, default: 0, minimal: 1, low: 1, medium: 2, high: 3, xhigh: 4, max: 5 }
const HARNESS_ORDER = ['codex', 'claude', 'grok', 'gemini', 'cursor', 'pi', 'opencode']
/** Providers in the picker's fixed order; a maker the registry doesn't know sorts last. */
const PROVIDER_ORDER = ['openai', 'anthropic', 'xai', 'google', 'cursor', 'local', 'unknown']
export const PROVIDER_LABEL: Record<string, string> = { openai: 'OpenAI', anthropic: 'Anthropic', xai: 'xAI', google: 'Google', cursor: 'Cursor', local: 'Local', unknown: 'Unknown provider' }
export const providerLabel = (provider: string) => PROVIDER_LABEL[provider] ?? (provider ? provider.charAt(0).toUpperCase() + provider.slice(1) : PROVIDER_LABEL.unknown!)

export interface EffortName { name: string; level: number | null }
/** One way to run a model: a harness and its model id, with that route's own level names. */
export interface EntryRoute { harness: string; model: string; via: string; efforts: EffortName[] }
export interface ModelEntry {
  /** The provider line ("anthropic:opus") a pick stores; a pick always runs the line's newest version. */
  line: string
  /** Unique per option: the line for its newest version, line@version for an older one. */
  key: string
  harness: string; name: string; slug: string; route: string
  efforts: EffortName[]; note: string; retiring: string | null; source: 'auto' | 'manual' | ''
  provider: string; version: string
  /** Every route of this version, the first one first; Cursor's per-level ids fold into one route. */
  routes: EntryRoute[]
  /** Older registered versions of the line, newest first. Only set on the newest version. */
  older: ModelEntry[]
  /** Set on an older version: the name of the version its line runs instead. */
  replacedBy?: string
}
const harnessRank = (harness: string) => { const index = HARNESS_ORDER.indexOf(harness); return index < 0 ? HARNESS_ORDER.length : index }
const providerRank = (provider: string) => { const index = PROVIDER_ORDER.indexOf(provider); return index < 0 ? PROVIDER_ORDER.length - 1 : index }
const levelValue = (effort: EffortName) => effort.level ?? NAMED_LEVEL[effort.name] ?? 2
/** "GPT-6.1 Sol", "Opus 5.5": the model's own name and version, never a harness or a profile revision. */
export function fullName(profile: RegistryProfile): string {
  if (profile.harness === 'codex') {
    const gpt = /^gpt-([0-9]+(?:\.[0-9]+)*)-(.+)$/.exec(profile.model)
    if (gpt) return `GPT-${gpt[1]} ${gpt[2]!.charAt(0).toUpperCase()}${gpt[2]!.slice(1)}`
  }
  return `${profile.short_name || profile.display_name || profile.model} ${profile.model_version ?? ''}`.trim()
}

/**
 * The server's provider line for a profile (modelsLine in board_resolve.go): Anthropic and OpenAI models are one line
 * whichever harness runs them, so Opus through Claude Code, Cursor and pi is one model with three routes.
 */
export function modelLine(profile: PrefProfile): { line: string; version: string } {
  let identity = profile
  if (profile.family === 'anthropic') identity = { ...profile, harness: 'claude', model: profile.model.replace(/^anthropic\//, '') }
  if (profile.family === 'openai' && profile.model.startsWith('gpt-')) identity = { ...profile, harness: 'codex' }
  const { line, version } = profileLine(identity)
  return { line: `${profile.family}:${line}`, version }
}
const via = (model: string) => model.startsWith('openrouter/') ? 'OpenRouter' : model.startsWith('ollama/') ? 'Local' : ''
const routeKey = (profile: RegistryProfile) => profile.harness === 'cursor' ? `cursor:${profile.model.replace(/-(low|medium|high|xhigh|max|ultra)(-fast)?$/, '$2')}` : `${profile.harness}:${profile.model}`
function routesOf(profiles: RegistryProfile[]): EntryRoute[] {
  const routes = new Map<string, EntryRoute>()
  for (const profile of profiles) {
    const key = routeKey(profile), route = routes.get(key) ?? { harness: profile.harness, model: profile.model, via: via(profile.model), efforts: [] }
    if (!route.efforts.some(effort => effort.name === profile.effort)) route.efforts.push({ name: profile.effort, level: profile.effort_level ?? null })
    routes.set(key, route)
  }
  for (const route of routes.values()) route.efforts.sort((a, b) => levelValue(a) - levelValue(b) || a.name.localeCompare(b.name))
  // Array sort is stable: routes of one harness keep the registry's order.
  return [...routes.values()].sort((a, b) => harnessRank(a.harness) - harnessRank(b.harness))
}
function entryOf(line: string, key: string, profiles: RegistryProfile[], now: number): ModelEntry {
  const routes = routesOf(profiles), first = routes[0]!
  // The name comes from the route that runs first: its registry display is the model's own.
  const ordered = [...profiles].sort((a, b) => harnessRank(a.harness) - harnessRank(b.harness)), named = ordered[0]!
  const retire = profiles.flatMap(profile => [profile.retire_at, profile.lifecycle?.state === 'retiring' ? profile.lifecycle.date : null])
    .filter((at): at is string => !!at && Date.parse(at) > now).sort()[0] ?? null
  return {
    line, key, harness: first.harness, name: fullName(named), slug: first.model, route: first.via,
    efforts: first.efforts, note: ordered.map(profile => profile.note ?? '').find(note => note.trim()) ?? '', retiring: retire,
    source: named.source ?? '', provider: named.provider || named.family || 'unknown', version: modelLine(named).version,
    routes, older: [],
  }
}

/**
 * One entry per provider line in its newest version, as the server resolves it: the newest enabled version that is
 * not retired (an alias follows it), every route of that version, and the levels of the route that runs first.
 * Older versions hang off their line. Every entry is offered, whatever it can do.
 */
export function buildEntries(profiles: RegistryProfile[], now = Date.now()): ModelEntry[] {
  const lines = new Map<string, { profile: RegistryProfile; version: string; order: number }[]>()
  profiles.forEach((profile, order) => {
    const { line, version } = modelLine(profile)
    lines.set(line, [...(lines.get(line) ?? []), { profile, version, order }])
  })
  const out: { entry: ModelEntry; order: number }[] = []
  for (const [line, items] of lines) {
    const enabled = items.filter(item => item.profile.enabled)
    if (!enabled.length) continue
    const live = enabled.filter(item => !item.profile.retired), pool = live.length ? live : enabled
    const latest = pool.reduce((version, item) => item.version !== 'alias' && compareModelVersions(item.version, version) > 0 ? item.version : version, '')
    const current = pool.filter(item => item.version === latest || item.version === 'alias')
    // An alias names no version; the newest concrete version names the line.
    const concrete = current.filter(item => item.version !== 'alias')
    const entry = entryOf(line, line, (concrete.length ? [...concrete, ...current.filter(item => item.version === 'alias')] : current).map(item => item.profile), now)
    const versions = [...new Set(live.filter(item => item.version && item.version !== 'alias' && compareModelVersions(item.version, latest) < 0).map(item => item.version))]
      .sort((a, b) => compareModelVersions(b, a))
    entry.older = versions.map(version => ({ ...entryOf(line, `${line}@${version}`, live.filter(item => item.version === version).map(item => item.profile), now), replacedBy: entry.name }))
    out.push({ entry, order: Math.min(...items.map(item => item.order)) })
  }
  // Providers in their fixed order; lines keep the registry's own order inside their provider.
  return out.sort((a, b) => providerRank(a.entry.provider) - providerRank(b.entry.provider) || a.order - b.order).map(item => item.entry)
}

export interface ProviderGroup { provider: string; label: string; entries: ModelEntry[]; older: ModelEntry[] }
/** Entries under their provider, newest versions first, then the provider's older versions (folded in the picker). */
export function groupByProvider(entries: ModelEntry[], match: (entry: ModelEntry) => boolean = () => true): ProviderGroup[] {
  const groups = new Map<string, ProviderGroup>()
  for (const entry of entries) {
    const shown = match(entry), older = entry.older.filter(match)
    if (!shown && !older.length) continue
    const group = groups.get(entry.provider) ?? { provider: entry.provider, label: providerLabel(entry.provider), entries: [], older: [] }
    if (shown) group.entries.push(entry)
    group.older.push(...older)
    groups.set(entry.provider, group)
  }
  return [...groups.values()].sort((a, b) => providerRank(a.provider) - providerRank(b.provider))
}

/**
 * The level a model runs at for a wish: the same name if it has it, else the nearest by effort level,
 * ties going up. This is the server's rule, repeated so the picker previews what a pick will become.
 */
export function nearestEffort(entry: ModelEntry | null | undefined, wanted: string | null, wantedLevel: number | null = null): string | null {
  if (!entry || !entry.efforts.length) return null
  if (wanted && entry.efforts.some(effort => effort.name === wanted)) return wanted
  const want = wantedLevel ?? (wanted ? NAMED_LEVEL[wanted] : undefined) ?? 2
  let best = entry.efforts[0]!
  for (const effort of entry.efforts) {
    const distance = Math.abs(levelValue(effort) - want), bestDistance = Math.abs(levelValue(best) - want)
    if (distance < bestDistance || distance === bestDistance && levelValue(effort) > levelValue(best)) best = effort
  }
  return best.name
}
export const effortLevel = (entry: ModelEntry | null | undefined, name: string | null) => {
  const effort = entry?.efforts.find(item => item.name === name)
  return effort ? levelValue(effort) : name ? NAMED_LEVEL[name] ?? null : null
}
/** A level is only worth naming when there is a choice. */
export const showsEffort = (entry: ModelEntry | null | undefined) => (entry?.efforts.length ?? 0) > 1

export const entryFor = (entries: ModelEntry[], line: string | null | undefined) => line ? entries.find(entry => entry.line === line) ?? null : null
/** "GPT-6.1 Sol · xhigh"; a model with one level shows no level. */
export const pickText = (entry: ModelEntry | null, effort: string | null, fallback = '') => entry ? `${entry.name}${showsEffort(entry) && effort ? ` · ${effort}` : ''}` : fallback
/**
 * A line the registry has no profile for still reads as a name, never a raw id (AEON-1062):
 * "anthropic:fable" → "Fable", "unknown:openrouter/qwen/qwen3-coder" → "Qwen3 Coder".
 */
export function lineFallback(line: string | null | undefined): string {
  if (!line) return ''
  const id = line.split(':').at(-1)!.split('/').at(-1)!
  return id.split(/[-_\s]+/).filter(Boolean).map(word => word.charAt(0).toUpperCase() + word.slice(1)).join(' ') || line
}
/** The model family of a line ("anthropic:fable" → "anthropic"), for its brand mark when the registry has no profile. */
export const lineFamily = (line: string | null | undefined) => line?.includes(':') ? line.split(':')[0]! : ''

export interface RowView {
  key: string; column: string; label: string; isDefault: boolean
  line: string | null; effort: string | null; entry: ModelEntry | null
  mine: boolean; reset: boolean; remove: boolean; editable: boolean; lock: SimpleLock | null; unavailable: SimpleUnavailable | null
  /** The fallback after the pick, how it ends, and the lines this row never uses (AEON-1161). */
  chain: SimpleStep[]; end: ChainEnd; never: SimpleNever[]
  /** A draft row exists only on this page until a pick is made; nothing is stored for it. */
  draft?: boolean
}
export function buildRows(input: { doc: SimpleDocument; workspace: SimpleDocument | null; scope: Scope; canEdit: boolean; entries: ModelEntry[]; labels: Map<string, string>; draft?: string | null }): RowView[] {
  const { doc, workspace, scope, canEdit, entries, labels, draft } = input
  const workspaceHas = (column: string) => workspace?.exceptions.some(row => row.column === column)
  const rows = [doc.all, ...doc.exceptions].map((row): RowView => {
    const isDefault = row.column === ALL_COLUMN
    // A person's own row is "yours · reset" when the workspace still has a row of its own for the kind, and an × otherwise.
    const own = scope === 'me' && row.mine && !row.lock
    const falls = isDefault || workspaceHas(row.column) !== false
    return {
      key: isDefault ? 'all' : row.column, column: row.column, label: isDefault ? 'Default · all work' : labels.get(row.column) ?? row.column, isDefault,
      line: row.line, effort: row.effort, entry: entryFor(entries, row.line),
      mine: scope === 'me' && row.mine && !row.lock,
      reset: own && falls, remove: !isDefault && (scope === 'default' ? canEdit : own && !falls),
      editable: canEdit && (scope === 'default' || !row.lock), lock: row.lock,
      unavailable: doc.unavailable.find(item => item.column === row.column) ?? null,
      chain: Array.isArray(row.chain) ? row.chain : [], end: row.end === 'wait' || row.end === 'default' ? row.end : isDefault ? 'wait' : 'default', never: Array.isArray(row.never) ? row.never : [],
    }
  })
  // "+ Different model for…" starts a row with the default's model and level; nothing is stored until a pick.
  if (draft && !rows.some(row => row.column === draft)) {
    const base = rows[0]!
    rows.push({ ...base, key: draft, column: draft, label: labels.get(draft) ?? draft, isDefault: false, mine: false, reset: false, remove: false, editable: canEdit, lock: null, unavailable: null, chain: [], end: 'default', never: [], draft: true })
  }
  return rows
}

/** The stored order behind a column. The resolved list drops incapable lines and used to drop locks; neither is the saved tail. */
export function orderBody(column: TailColumn): OrderBody {
  if (column.stored && Array.isArray(column.stored.rank) && Array.isArray(column.stored.not)) return { rank: [...column.stored.rank], not: [...column.stored.not] }
  return { rank: column.list.map(card => card.line), not: column.not.map(card => card.line) }
}
/** Lines a workspace rule keeps last in a column's order: pins to the bottom, migrated from the old board (AEON-1158). */
export const pinnedLast = (column: TailColumn | undefined) => new Set((column?.list ?? []).filter(card => card.lock?.value === 'bottom').map(card => card.line))
/** How many steps at the end of a row's order are kept there by such pins. */
export function pinnedSteps(row: RowView, pinned: Set<string>): number {
  let count = 0
  while (count < row.chain.length && pinned.has(row.chain[row.chain.length - 1 - count]!.line)) count++
  return count
}
export function orderWithFirst(column: TailColumn, line: string): OrderBody {
  const body = orderBody(column)
  return { rank: [line, ...body.rank.filter(id => id !== line)], not: body.not.filter(id => id !== line) }
}

export function rulesBody(document: RulesDocument, column: string): RulesBody {
  const rows = document.rules.filter(rule => rule.scope === 'workspace' && rule.column === column).sort((a, b) => a.position - b.position)
  return {
    top: rows.filter(rule => rule.lock === 'top').map(({ line, why }) => ({ line, why })),
    bottom: rows.filter(rule => rule.lock === 'bottom').map(({ line, why }) => ({ line, why })),
    not: Object.fromEntries(rows.filter(rule => rule.lock === 'not').map(rule => [rule.line, rule.why])),
  }
}
export const lockReason = (person: string) => `Locked in Settings › Models by ${person}`
/** A lock is a workspace pin to the top of the column, written with a generated reason. */
export function pinFirst(before: RulesBody, line: string, why: string): RulesBody {
  return { top: [{ line, why }, ...before.top.filter(pin => pin.line !== line)], bottom: before.bottom.filter(pin => pin.line !== line), not: Object.fromEntries(Object.entries(before.not).filter(([id]) => id !== line)) }
}
export function unpin(before: RulesBody, line: string): RulesBody {
  return { top: before.top.filter(pin => pin.line !== line), bottom: before.bottom, not: before.not }
}
/** The pin follows the row when its pick changes. */
export function movePin(before: RulesBody, from: string, to: string): RulesBody {
  const reason = before.top.find(pin => pin.line === from)?.why ?? ''
  return pinFirst(unpin(before, from), to, reason)
}

const formatDate = (value: string) => new Date(value).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
export const lockTip = (lock: SimpleLock, input: { mine: boolean; name: (id: string) => string; admin: boolean; scope: Scope }) => {
  const when = lock.at ? ` · ${formatDate(lock.at)}` : ''
  const who = input.mine ? 'You locked this for everyone' : `${lock.by ? input.name(lock.by) : 'An admin'} set this for everyone`
  return `${who}${when}${lock.reason && !lock.reason.startsWith('Locked in Settings') ? ` · ${lock.reason}` : ''}${input.admin && input.scope === 'me' ? ' · Change it under For everyone.' : ''}`
}
export const retiringText = (entry: ModelEntry) => entry.retiring ? `Retiring ${formatDate(entry.retiring)}` : ''

/** A trace reason in plain words, for the steps behind "Why?". Rows use cantSentence instead. */
export const reasonText = (reason: string) => {
  const words: Record<string, string> = {
    allowance: 'the account has no room left until its limit resets',
    'account availability': 'the account has no recent successful sign-in check or free agent slot',
    daily_limit: 'the account is at its daily cap',
    daily_limit_unknown: 'the daily cap could not be checked',
    policy: 'the model is disabled in Settings',
    retired: 'the model has been retired',
    'author family': 'the review needs a model from another family',
    'harness filter': 'the model uses another harness',
    'security review policy': 'the model does not meet the security review policy',
  }
  const parts = reason.split(';').map(part => part.trim().replace(/^(column|default|role):\s*/, '').trim()).filter(Boolean)
    .map(part => words[part] ?? part)
  const plain = [...new Set(parts)].join('; ')
  return plain ? plain.charAt(0).toLowerCase() + plain.slice(1) : 'no qualified account has room right now'
}

/** "04:00" within a day, else "14 Oct" (with the year when it differs); nothing for a time that has passed. */
export function untilText(at: string | null | undefined, now = Date.now()): string {
  const time = at ? Date.parse(at) : NaN
  if (!Number.isFinite(time) || time <= now) return ''
  const date = new Date(time)
  if (time - now < 24 * 3_600_000) return date.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  return date.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', ...(date.getFullYear() !== new Date(now).getFullYear() ? { year: 'numeric' } : {}) })
}

export interface CantSentence { cause: string; then: string }
/**
 * Why a row's pick can't run, as one sentence from one cause (AEON-1146): "Claude account is at its daily cap until
 * 04:00", then what happens instead: "GPT-6.1 Sol · high runs instead", "Opus 5.5 runs through Cursor instead" or
 * "work waits until then". The server sends codes; the words are the page's. Never a code, never repeated.
 */
export function cantSentence(item: SimpleUnavailable | null | undefined, entries: ModelEntry[], now = Date.now()): CantSentence | null {
  if (!item) return null
  const name = entryFor(entries, item.line)?.name ?? lineFallback(item.line)
  const until = untilText(item.until, now), by = until ? ` until ${until}` : ''
  // Accounts carry the vendor's name ("Claude account"); routes carry the harness's ("runs through Cursor").
  const vendor = item.route ? ACCOUNT_NAME[item.route] ?? harnessLabel(item.route) : ''
  const account = vendor ? `${vendor} account` : 'The account'
  const causes: Record<UnavailableCause, string> = {
    daily_cap: `${account} is at its daily cap${by}`,
    paused: `${name} is paused${by}`,
    no_tools: `${name} has no tools in PAIMOS`,
    no_account: vendor ? `No ${vendor} account can run ${name} right now` : `No account can run ${name} right now`,
    retired: `${name} has been retired`,
    residency: `Data residency rules out ${name}`,
  }
  const plain = reasonText(item.reason)
  const cause = item.cause && causes[item.cause] ? causes[item.cause] : plain.charAt(0).toUpperCase() + plain.slice(1)
  if (item.waits || !item.runs_instead) return { cause, then: until ? 'work waits until then' : 'work waits' }
  if (item.runs_instead === item.line) return { cause, then: `${name} runs through ${item.runs_via ? harnessLabel(item.runs_via) : 'another route'} instead` }
  return { cause, then: `${pickText(entryFor(entries, item.runs_instead), item.effort, lineFallback(item.runs_instead))} runs instead` }
}

export type Part = { text: string; strong?: boolean }
export interface WhyView { title: string; steps: Part[][] }
export interface NextView { parts: Part[]; why: WhyView | null }
/** The one quiet line under the rows, and the numbered trace behind "Why?". */
export function nextView(input: { next: SimpleNext | null; rows: RowView[]; entries: ModelEntry[]; labels: Map<string, string>; now?: number }): NextView {
  const { next, rows, entries, labels } = input
  const name = (line: string | null | undefined, effort: string | null | undefined) => pickText(entryFor(entries, line), effort ?? null, lineFallback(line))
  // A skipped step says the row's own cause sentence when the page has one for that model, else the trace's words.
  const basisRow = rows.find(row => row.column === (next?.column ?? ALL_COLUMN)) ?? rows.find(row => row.isDefault)
  const knownFor = (line: string) => basisRow?.unavailable?.line === line ? basisRow.unavailable : null
  const skip = (step: SimpleTrace) => {
    const known = knownFor(step.line), cause = known ? cantSentence(known, entries, input.now)?.cause : ''
    // The same model skipped on one route and run on another names the route it skipped.
    const route = known?.route && step.line === next?.line ? ` through ${harnessLabel(known.route)}` : ''
    return `Skipped ${entryFor(entries, step.line)?.name ?? lineFallback(step.line)}${route}: ${cause || reasonText(step.reason)}.`
  }
  if (!next || (!next.ticket && !next.line)) return { parts: [{ text: 'No work is queued right now.' }], why: null }
  if (!next.line) {
    const skipped = (next.trace ?? []).filter(step => step.role !== 'review-gate' && step.reason)
    const steps = skipped.map(step => [{ text: skip(step) }])
    if (!steps.length) steps.push([{ text: 'Nothing in the order can run it.' }])
    const ticket = next.ticket ?? 'Work'
    return { parts: [{ text: `${ticket} is queued, but nothing available can run it.` }], why: { title: `Why is ${ticket} waiting?`, steps } }
  }
  const column = next.column ?? ALL_COLUMN, kind = column === ALL_COLUMN ? 'work' : labels.get(column) ?? column
  const trace = (next.trace ?? []).filter(step => step.role !== 'review-gate'), skipped = trace.filter(step => !step.selected && step.reason)
  const runs = name(next.line, next.effort), reviewer = next.reviewer?.line ? entryFor(entries, next.reviewer.line)?.name ?? lineFallback(next.reviewer.line) : ''
  // Only another model is "instead of"; the same model on another route says the route (never "Opus instead of Opus").
  const other = skipped.find(step => step.line !== next.line), wanted = other ? entryFor(entries, other.line)?.name ?? lineFallback(other.line) : ''
  const via = !wanted && skipped.length ? knownFor(next.line)?.runs_via ?? null : null
  const parts: Part[] = [{ text: `The next ${kind} runs on ` }, { text: runs, strong: true }]
  if (wanted) parts.push({ text: ` instead of ${wanted}` })
  else if (via) parts.push({ text: ` through ${harnessLabel(via)}` })
  if (reviewer) parts.push({ text: ', reviewed by ' }, { text: reviewer, strong: true })
  parts.push({ text: '.' })
  const own = rows.find(row => row.column === column && !row.isDefault), basis = own ?? rows.find(row => row.isDefault)
  const first: Part[] = own
    ? [{ text: `${kind} has its own model: ` }, { text: name(basis?.line, basis?.effort), strong: true }, { text: '.' }]
    : [{ text: `${kind} has no override, so it uses the default: ` }, { text: name(basis?.line, basis?.effort), strong: true }, { text: '.' }]
  const steps: Part[][] = [first]
  for (const step of skipped) steps.push([{ text: skip(step) }])
  if (via) steps.push([{ text: `It takes its next route, ${harnessLabel(via)}: ` }, { text: runs, strong: true }, { text: '.' }])
  else if (skipped.length) steps.push([{ text: `Next in its order that can do ${kind}: ` }, { text: runs, strong: true }, { text: '.' }])
  if (reviewer) steps.push([{ text: 'Reviews always use another family: ' }, { text: reviewer, strong: true }, { text: '.' }])
  return { parts, why: { title: `Why ${entryFor(entries, next.line)?.name ?? lineFallback(next.line)}?`, steps } }
}

// ----- Fallback orders (AEON-1161) ------------------------------------------------------------------------------
// Each row is its pick, then its fallback in order, then Wait or the default's fallback. Nothing outside the order runs.

/** A model by its own name, never a raw id. */
export const lineName = (entries: ModelEntry[], line: string | null | undefined) => entryFor(entries, line)?.name ?? lineFallback(line)
/** "Opus 5.5 · max"; a model with one level shows no level. */
export const stepText = (entries: ModelEntry[], step: SimpleStep) => pickText(entryFor(entries, step.line), step.effort, lineFallback(step.line))
/** How an order ends, in words. The default always ends in Wait. */
export const endText = (end: ChainEnd, isDefault: boolean) => isDefault || end === 'wait' ? 'Wait' : 'the default’s fallback'
/** The sentence under a row: "If GPT-6.1 Sol can’t run → Opus 5.5 → Wait". */
export function fallbackSentence(row: RowView, entries: ModelEntry[]): { pick: string; steps: string[] } {
  return { pick: row.entry?.name ?? lineFallback(row.line), steps: [...row.chain.map(step => lineName(entries, step.line)), endText(row.end, row.isDefault)] }
}
/** Where "the default’s fallback" goes: the default's pick, its order, then Wait. */
export const defaultTail = (base: RowView | undefined, entries: ModelEntry[]) => base?.line ? [lineName(entries, base.line), ...base.chain.map(step => lineName(entries, step.line)), 'Wait'] : ['Wait']

/** One item moved to another place; anything out of range leaves the list as it was. */
export function moveItem<T>(list: readonly T[], from: number, to: number): T[] {
  const next = list.slice()
  if (from === to || from < 0 || to < 0 || from >= list.length || to >= list.length) return next
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item!)
  return next
}
/** The lines an order already holds; a fallback offers only the others. */
export const orderLines = (row: RowView) => [row.line, ...row.chain.map(step => step.line)].filter((line): line is string => !!line)

/** The server's model identity (modelregistry.modelsLine): one line across every harness that runs it. */
export function modelsLineId(profile: RegistryProfile): { line: string; version: string } {
  let harness = profile.harness, model = profile.model
  if (profile.family === 'anthropic') { harness = 'claude'; model = model.replace(/^anthropic\//, '') }
  if (profile.family === 'openai' && model.startsWith('gpt-')) harness = 'codex'
  const { line, version } = profileLine({ ...profile, harness, model })
  return { line: `${profile.family}:${line}`, version }
}
/** The routes (harnesses) each line runs through, in registry order: the enabled routes of its latest version. */
export function lineRoutes(profiles: RegistryProfile[]): Map<string, string[]> {
  const byLine = new Map<string, { profile: RegistryProfile; version: string }[]>()
  for (const profile of profiles) {
    const { line, version } = modelsLineId(profile)
    byLine.set(line, [...(byLine.get(line) ?? []), { profile, version }])
  }
  const out = new Map<string, string[]>()
  for (const [line, items] of byLine) {
    const usable = items.filter(item => item.profile.enabled && !item.profile.retired)
    const latest = usable.filter(item => item.version !== 'alias').reduce((version, item) => compareModelVersions(item.version, version) > 0 ? item.version : version, '')
    const harnesses: string[] = []
    for (const { profile, version } of usable) if ((version === 'alias' || version === latest) && !harnesses.includes(profile.harness)) harnesses.push(profile.harness)
    out.set(line, harnesses)
  }
  return out
}
/** "Claude Code, then 2 more": the route a step tries first, and how many it tries after it. */
export function routeNote(harnesses: string[] | undefined, first?: string | null): string {
  if (!harnesses?.length) return ''
  const lead = first && harnesses.includes(first) ? first : harnesses[0]!
  return harnesses.length > 1 ? `${harnessLabel(lead)}, then ${harnesses.length - 1} more` : harnessLabel(lead)
}

const clock = (iso: string, now: number) => {
  const at = Date.parse(iso)
  if (!Number.isFinite(at)) return ''
  const when = new Date(at)
  return at >= now && at - now < 86_400_000 ? when.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' }) : formatDate(iso)
}
/** A cause in a few words for a step in the order. Never a code. */
export function causeShort(item: Pick<SimpleUnavailable, 'cause' | 'until'>, now = Date.now()): string {
  const until = item.until ? clock(item.until, now) : ''
  switch (item.cause) {
    case 'daily_cap': return until ? `daily cap until ${until}` : 'at its daily cap'
    case 'paused': return until ? `paused until ${until}` : 'paused'
    case 'no_tools': return 'no tools in PAIMOS'
    case 'no_account': return 'no account with room'
    case 'retired': return 'retired'
    case 'residency': return 'not allowed where this work must run'
    default: return 'can’t run right now'
  }
}
export interface StepState { text: string; runs: boolean }
/**
 * What the order says about each step right now, from the row's own evidence only: the pick's first cause, and the
 * step that runs instead. Steps the server did not report on say nothing rather than guess.
 */
export function stepStates(row: RowView, unavailable: SimpleUnavailable[], now = Date.now()): Map<string, StepState> {
  const out = new Map<string, StepState>()
  const first = unavailable.find(item => item.column === row.column && item.line === row.line)
  if (!first || !row.line) return out
  if (first.runs_instead === row.line) {
    out.set(row.line, { text: first.route ? `runs now, not through ${harnessLabel(first.route)}` : 'runs now through another route', runs: true })
    return out
  }
  out.set(row.line, { text: causeShort(first, now), runs: false })
  if (first.runs_instead) out.set(first.runs_instead, { text: 'runs now', runs: true })
  return out
}

/**
 * The native levels a chain write stores: each step's level, and the pick's only when the row holds its own
 * (an inherited level stays inherited). Lines the registry does not know keep the server's automatic level.
 */
export function chainEfforts(input: { pick: string | null; pickEffort: string | null; ownPick: boolean; steps: SimpleStep[]; known: (line: string) => boolean }): Record<string, string> {
  const out: Record<string, string> = {}
  if (input.ownPick && input.pick && input.pickEffort && input.known(input.pick)) out[input.pick] = input.pickEffort
  for (const step of input.steps) if (step.effort && input.known(step.line)) out[step.line] = step.effort
  return out
}
/** Two orders that read the same: pick, level, every step with its level, and the ending. */
export const sameOrder = (a: { line: string | null; effort: string | null; chain: SimpleStep[]; end: ChainEnd }, b: { line: string | null; effort: string | null; chain: SimpleStep[]; end: ChainEnd }) =>
  a.line === b.line && a.effort === b.effort && a.end === b.end && a.chain.length === b.chain.length && a.chain.every((step, index) => step.line === b.chain[index]!.line && step.effort === b.chain[index]!.effort)
/** "Markus, 2 Oct" for a workspace rule; a saved exclusion has no author. */
export const neverWho = (never: SimpleNever, name: (id: string) => string) => never.at ? `${never.by ? name(never.by) : 'An admin'}, ${formatDate(never.at)}` : 'saved exclusion'
/** What /models/resolve says about one ticket: the profile that runs it and every step its walk skipped. */
export interface TicketResolution {
  profile: RegistryProfile | null; owner_required: boolean
  ladder: { stage?: string; profile_id: string; selected: boolean; skip_reasons: string[] }[]
}
const SKIPS_SHOWN = 8
/**
 * "Why?" for the ticket a person clicked (Tick, AEON-1138): its own resolution, never the next queued ticket's.
 * A long walk is cut after eight skipped steps and says how many more there were.
 */
export function ticketWhy(input: { ticket: string; resolution: TicketResolution; profiles: RegistryProfile[]; entries: ModelEntry[] }): WhyView {
  const { ticket, resolution, profiles, entries } = input
  const byId = new Map(profiles.map(profile => [profile.id, profile] as const))
  // A route's own display can be a raw id (Cursor's "claude-opus-5.5"); the line's entry names the version.
  const title = (profile: RegistryProfile) => {
    const { line, version } = modelLine(profile), entry = entryFor(entries, line)
    return [entry, ...(entry?.older ?? [])].find(item => item?.version === version)?.name ?? fullName(profile)
  }
  const named = (profile: RegistryProfile) => `${title(profile)}${showsEffort(entryFor(entries, modelLine(profile).line)) ? ` · ${profile.effort}` : ''}`
  const through = (profile: RegistryProfile) => (entryFor(entries, modelLine(profile).line)?.routes.length ?? 0) > 1 ? ` through ${harnessLabel(profile.harness)}` : ''
  const skipped = resolution.ladder.filter(step => !step.selected && step.skip_reasons.length)
  const steps: Part[][] = skipped.slice(0, SKIPS_SHOWN).map(step => {
    const profile = byId.get(step.profile_id)
    const who = profile ? `${title(profile)}${through(profile)}` : 'A model no longer in the registry'
    return [{ text: `Skipped ${who}: ${reasonText(step.skip_reasons.join('; '))}.` }]
  })
  if (skipped.length > SKIPS_SHOWN) steps.push([{ text: `${skipped.length - SKIPS_SHOWN} more skipped for the same kinds of reasons.` }])
  const runs = resolution.profile
  if (!runs || resolution.owner_required) {
    steps.push([{ text: `Nothing in the order of ${ticket} can run it right now, so it waits.` }])
    return { title: `Why is ${ticket} waiting?`, steps }
  }
  steps.push([{ text: `${ticket} runs on ` }, { text: named(runs), strong: true }, { text: `${through(runs)}.` }])
  return { title: `Why ${title(runs)} for ${ticket}?`, steps }
}
