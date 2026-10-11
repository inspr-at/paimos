// SPDX-License-Identifier: AGPL-3.0-only
// The minimal Models page (AEON-1011). Display and editing only: the server owns eligibility, account
// qualification and the automatic fallback. Everything here is pure so the card can be tested without a browser.
import { compareModelVersions, profileLine, type PrefProfile } from './modelPrefs'
import { harnessLabel } from './agentState'

export type Scope = 'me' | 'default'
export interface SimplePick { line: string | null; effort: string | null; harness?: string; model?: string }
export interface SimpleLock { by: string | null; at: string | null; reason: string }
export interface SimpleRow extends SimplePick { column: string; mine: boolean; lock: SimpleLock | null }
export interface SimpleTrace { line: string; stage: 'column' | 'default' | 'role'; role?: string; reason: string; selected: boolean }
export interface SimpleNext { column?: string; line?: string | null; effort?: string | null; ticket?: string; reviewer?: SimplePick; trace?: SimpleTrace[] }
export interface SimpleUnavailable { column: string; line: string; runs_instead: string | null; effort: string | null; reason: string; ticket: string | null }
export interface SimpleNewLine { line: string; can_do: string[] }
export interface SimpleDocument {
  all: SimpleRow; exceptions: SimpleRow[]; reviews: { mode: 'cross_family' }; next: SimpleNext | null
  unavailable: SimpleUnavailable[]; new_lines: SimpleNewLine[]; revision: number; rules_revision: number; person_id: string | null
  /** Workspace situation orders (for=default only); a missing cell follows its row, or automatic review. */
  situations?: SimpleSituations; quick?: SimpleQuick
}
/** The part of the ranked board this page needs: the stored order behind each first pick, and what a column cannot do. */
export interface TailCard { line: string; lock?: unknown }
/** The order a first-pick rewrite must keep, including lines the resolved list omits. */
export interface StoredOrder { rank: string[]; not: string[] }
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
export type RegistryProfile = PrefProfile & { effort_level?: number | null; note?: string; source?: 'auto' | 'manual'; retire_at?: string | null; retired?: boolean; short_name?: string; created_at?: string; version?: string; slug?: string }

export const ALL_COLUMN = 'other'
/** The shared effort scale, used only when the server reports no level for a name. */
const NAMED_LEVEL: Record<string, number> = { off: 0, default: 0, minimal: 1, low: 1, medium: 2, high: 3, xhigh: 4, max: 5 }
const HARNESS_ORDER = ['codex', 'claude', 'grok', 'gemini', 'cursor', 'pi', 'opencode']

export interface EffortName { name: string; level: number | null }
export interface ModelEntry {
  line: string; harness: string; name: string; slug: string; route: string
  efforts: EffortName[]; note: string; retiring: string | null; source: 'auto' | 'manual' | ''
}
const harnessRank = (harness: string) => { const index = HARNESS_ORDER.indexOf(harness); return index < 0 ? HARNESS_ORDER.length : index }
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
 * One entry per model line in its latest version, as the server resolves it: the newest enabled version, its
 * active profiles, and the levels of the harness that runs it first. Every entry is offered, whatever it can do.
 */
export function buildEntries(profiles: RegistryProfile[], now = Date.now()): ModelEntry[] {
  const lines = new Map<string, { profile: RegistryProfile; version: string }[]>()
  for (const profile of profiles) {
    const { line, version } = profileLine(profile), id = `${profile.family}:${line}`
    lines.set(id, [...(lines.get(id) ?? []), { profile, version }])
  }
  const out: { entry: ModelEntry; order: number }[] = []
  for (const [id, items] of lines) {
    const enabled = items.filter(item => item.profile.enabled)
    if (!enabled.length) continue
    const latest = enabled.reduce((version, item) => compareModelVersions(item.version, version) > 0 ? item.version : version, '')
    const active = items.some(item => item.version === latest && item.profile.enabled && !item.profile.retired)
    const current = items.filter(item => item.version === latest && !(active && (item.profile.retired || !item.profile.enabled)))
    if (!current.length) continue
    const harness = current.map(item => item.profile.harness).sort((a, b) => harnessRank(a) - harnessRank(b))[0]!
    const here = current.filter(item => item.profile.harness === harness), first = here[0]!.profile
    const efforts: EffortName[] = []
    for (const { profile } of here) if (!efforts.some(effort => effort.name === profile.effort)) efforts.push({ name: profile.effort, level: profile.effort_level ?? null })
    efforts.sort((a, b) => levelValue(a) - levelValue(b) || a.name.localeCompare(b.name))
    const retire = here.map(item => item.profile.retire_at).filter((at): at is string => !!at && Date.parse(at) > now).sort()[0] ?? null
    out.push({ order: harnessRank(harness), entry: {
      line: id, harness, name: fullName(first), slug: first.model,
      route: first.model.startsWith('openrouter/') ? 'OpenRouter' : '',
      efforts, note: here.map(item => item.profile.note ?? '').find(note => note.trim()) ?? '', retiring: retire,
      source: first.source ?? '',
    } })
  }
  // Array sort is stable: models keep the registry's own order inside their harness.
  return out.sort((a, b) => a.order - b.order).map(item => item.entry)
}

/** Entries grouped under their harness, in the fixed harness order. */
export function groupByHarness(entries: ModelEntry[]): { harness: string; label: string; entries: ModelEntry[] }[] {
  const groups = new Map<string, ModelEntry[]>()
  for (const entry of entries) groups.set(entry.harness, [...(groups.get(entry.harness) ?? []), entry])
  return [...groups].sort((a, b) => harnessRank(a[0]) - harnessRank(b[0])).map(([harness, items]) => ({ harness, label: harnessLabel(harness), entries: items }))
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
    }
  })
  // "+ Different model for…" starts a row with the default's model and level; nothing is stored until a pick.
  if (draft && !rows.some(row => row.column === draft)) {
    const base = rows[0]!
    rows.push({ ...base, key: draft, column: draft, label: labels.get(draft) ?? draft, isDefault: false, mine: false, reset: false, remove: false, editable: canEdit, lock: null, unavailable: null, draft: true })
  }
  return rows
}

/** The stored order behind a column. The resolved list drops incapable lines and used to drop locks; neither is the saved tail. */
export function orderBody(column: TailColumn): OrderBody {
  if (column.stored && Array.isArray(column.stored.rank) && Array.isArray(column.stored.not)) return { rank: [...column.stored.rank], not: [...column.stored.not] }
  return { rank: column.list.map(card => card.line), not: column.not.map(card => card.line) }
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

/** A sentence in plain text for the "Can't run" line, from the server's own words. */
export const reasonText = (reason: string) => {
  const words: Record<string, string> = {
    allowance: 'the account is at its allowance or floor',
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

export type Part = { text: string; strong?: boolean }
export interface WhyView { title: string; steps: Part[][] }
export interface NextView { parts: Part[]; why: WhyView | null }
/** The one quiet line under the rows, and the numbered trace behind "Why?". */
export function nextView(input: { next: SimpleNext | null; rows: RowView[]; entries: ModelEntry[]; labels: Map<string, string> }): NextView {
  const { next, rows, entries, labels } = input
  const name = (line: string | null | undefined, effort: string | null | undefined) => pickText(entryFor(entries, line), effort ?? null, lineFallback(line))
  if (!next || (!next.ticket && !next.line)) return { parts: [{ text: 'No work is queued right now.' }], why: null }
  if (!next.line) {
    const skipped = (next.trace ?? []).filter(step => step.role !== 'review-gate' && step.reason)
    const steps = skipped.map(step => [{ text: `Skipped ${entryFor(entries, step.line)?.name ?? lineFallback(step.line)}: ${reasonText(step.reason)}.` }])
    if (!steps.length) steps.push([{ text: 'Nothing in the order can run it.' }])
    const ticket = next.ticket ?? 'Work'
    return { parts: [{ text: `${ticket} is queued, but nothing available can run it.` }], why: { title: `Why is ${ticket} waiting?`, steps } }
  }
  const column = next.column ?? ALL_COLUMN, kind = column === ALL_COLUMN ? 'work' : labels.get(column) ?? column
  const trace = (next.trace ?? []).filter(step => step.role !== 'review-gate'), skipped = trace.filter(step => !step.selected && step.reason)
  const runs = name(next.line, next.effort), reviewer = next.reviewer?.line ? entryFor(entries, next.reviewer.line)?.name ?? lineFallback(next.reviewer.line) : ''
  const wanted = skipped[0] ? entryFor(entries, skipped[0].line)?.name ?? lineFallback(skipped[0].line) : ''
  const parts: Part[] = [{ text: `The next ${kind} runs on ` }, { text: runs, strong: true }]
  if (wanted) parts.push({ text: ` instead of ${wanted}` })
  if (reviewer) parts.push({ text: ', reviewed by ' }, { text: reviewer, strong: true })
  parts.push({ text: '.' })
  const own = rows.find(row => row.column === column && !row.isDefault), basis = own ?? rows.find(row => row.isDefault)
  const first: Part[] = own
    ? [{ text: `${kind} has its own model: ` }, { text: name(basis?.line, basis?.effort), strong: true }, { text: '.' }]
    : [{ text: `${kind} has no override, so it uses the default: ` }, { text: name(basis?.line, basis?.effort), strong: true }, { text: '.' }]
  const steps: Part[][] = [first]
  for (const step of skipped) steps.push([{ text: `Skipped ${entryFor(entries, step.line)?.name ?? lineFallback(step.line)}: ${reasonText(step.reason)}.` }])
  if (skipped.length) steps.push([{ text: `Next in its order that can do ${kind}: ` }, { text: runs, strong: true }, { text: '.' }])
  if (reviewer) steps.push([{ text: 'Reviews always use another family: ' }, { text: reviewer, strong: true }, { text: '.' }])
  return { parts, why: { title: `Why ${entryFor(entries, next.line)?.name ?? lineFallback(next.line)}?`, steps } }
}

// ----- Quick jobs and the situation matrix (Models v2 W3, AEON-1162) -----------------------------------------------
/** The matrix columns. "First build" is the small-work order; the row itself stays the first-build order. */
export type SituationKey = 'small' | 'fix' | 'stuck' | 'review'
export interface SituationStep { line: string; effort: string | null }
export interface SimpleSituation extends SimplePick { chain: SituationStep[]; end: 'wait' | 'row' | 'auto'; /** The order's saved exclusions. */ not?: string[] }
export type SimpleSituations = Record<string, Partial<Record<SituationKey, SimpleSituation>>>
export interface SimpleQuick { line: string | null; effort: string | null; harness: string | null; model?: string; trace: SimpleTrace[] }
/** A situation order as the order endpoint takes it: rank = [pick, …chain]; an omitted end or not keeps the stored one. */
export interface SituationOrder { rank: string[]; efforts: Record<string, string>; end?: 'wait' | 'row' | 'auto'; not?: string[] }
export interface SituationLimitsView { small_hours: number; fix_rounds: number }

export const SITUATIONS: readonly { key: SituationKey; label: string }[] = [
  { key: 'small', label: 'First build' }, { key: 'fix', label: 'Fix rounds' }, { key: 'stuck', label: 'Stuck' }, { key: 'review', label: 'Review' },
]
/** The limits come from Settings › Kinds of work; without them the hint names the situation in words only. */
export function situationHint(key: SituationKey, limits: SituationLimitsView | null): string {
  if (key === 'small') return limits ? `${limits.small_hours} h or less` : 'small work'
  if (key === 'fix') return 'after findings'
  if (key === 'stuck') return limits ? `after ${limits.fix_rounds} ${limits.fix_rounds === 1 ? 'round' : 'rounds'}` : 'after the fix rounds'
  return 'another family'
}
export interface SituationRow { column: string; label: string; isDefault: boolean; line: string | null; effort: string | null; entry: ModelEntry | null; cells: Record<SituationKey, SimpleSituation | null> }
/**
 * The matrix rows: the Default, every row of the workspace, then any kind that has a situation order but no row of its
 * own, so a count never names a cell the page does not show. Kinds keep the order of Settings › Kinds of work.
 */
export function situationRows(input: { doc: SimpleDocument; entries: ModelEntry[]; labels: Map<string, string>; order: string[] }): SituationRow[] {
  const { doc, entries, labels, order } = input, cells = doc.situations ?? {}
  const cellsOf = (column: string) => Object.fromEntries(SITUATIONS.map(({ key }) => [key, cells[column]?.[key]?.line ? cells[column]![key]! : null])) as Record<SituationKey, SimpleSituation | null>
  const view = (row: SimpleRow): SituationRow => ({ column: row.column, label: row.column === ALL_COLUMN ? 'Default' : labels.get(row.column) ?? row.column, isDefault: row.column === ALL_COLUMN, line: row.line, effort: row.effort, entry: entryFor(entries, row.line), cells: cellsOf(row.column) })
  const rows = [doc.all, ...doc.exceptions].map(view)
  const extra = Object.keys(cells).filter(column => !rows.some(row => row.column === column) && SITUATIONS.some(({ key }) => cells[column]?.[key]?.line))
    .sort((a, b) => (order.indexOf(a) + 1 || order.length + 1) - (order.indexOf(b) + 1 || order.length + 1) || a.localeCompare(b))
  return [...rows, ...extra.map(column => view({ ...doc.all, column }))]
}
export const situationCount = (rows: SituationRow[]) => rows.reduce((count, row) => count + SITUATIONS.filter(({ key }) => row.cells[key]).length, 0)
export const situationSteps = (cell: SimpleSituation | null | undefined): SituationStep[] => cell?.line ? [{ line: cell.line, effort: cell.effort }, ...cell.chain] : []
/**
 * An order body for these steps; no steps means the cell goes back to the row (or to automatic review). The exclusions
 * are sent only when given, to write a whole stored order back; otherwise the server keeps the ones it has.
 */
export function situationOrder(steps: SituationStep[], end?: SituationOrder['end'], not?: string[]): SituationOrder | null {
  if (!steps.length) return null
  const efforts: Record<string, string> = {}
  for (const step of steps) if (step.effort) efforts[step.line] = step.effort
  return { rank: steps.map(step => step.line), efforts, ...(end ? { end } : {}), ...(not ? { not: [...not] } : {}) }
}
/**
 * Reviews run at the model's highest native level, which the server picks and refuses any other (review_floor). So a
 * review step sends no level, whatever level was clicked; the cell then shows the level the server chose.
 */
export const situationStep = (situation: SituationKey, entry: ModelEntry, effort: string | null): SituationStep =>
  ({ line: entry.line, effort: situation === 'review' ? null : effort })
export const situationEndText = (situation: SituationKey, cell: SimpleSituation) =>
  cell.end === 'wait' ? 'Then wait.' : situation === 'review' ? 'Then automatic, always another family.' : 'Then the row’s fallback.'
export const situationEmptyText = (situation: SituationKey) => situation === 'review' ? 'Automatic: the first qualified model of another family.' : 'Same as the row: its pick, then its fallback.'
/** Refusals of a situation write, in plain words; the server sends codes. */
export function situationFailure(code: string): string {
  const words: Record<string, string> = {
    review_floor: 'Reviews need a strong model at its highest level. That model can’t review here.',
    invalid_effort: 'That level isn’t available for this model.', effort_not_registered: 'That level isn’t available for this model.',
    unknown_column: 'That kind of work no longer exists. Reload to see the current kinds.', invalid_end: 'That ending isn’t allowed for this situation.',
  }
  return words[code] ?? ''
}

export interface QuickView { lead: string; steps: Part[][]; foot: string }
/** The Quick jobs "Why?": the rule, what was skipped and why, what runs now, and what runs when nothing qualifies. */
export function quickView(input: { quick: SimpleQuick | null | undefined; fallback: RowView | null | undefined; entries: ModelEntry[] }): QuickView {
  const { quick, fallback, entries } = input
  const name = (line: string | null | undefined, effort: string | null | undefined) => pickText(entryFor(entries, line), effort ?? null, lineFallback(line))
  const said: Record<string, string> = { 'Quick jobs requires fast tier': 'it isn’t a fast model', 'No tools in PAIMOS': 'it has no tools in PAIMOS' }
  const steps: Part[][] = [[{ text: 'PAIMOS takes the cheapest model that qualifies: a fast model with tools and an account with room.' }]]
  const skipped = (quick?.trace ?? []).filter(step => step.role !== 'review-gate' && !step.selected && step.reason)
  for (const step of skipped.slice(0, 3)) {
    const reason = step.reason.split(';').map(part => said[part.trim()] ?? part).join(';')
    steps.push([{ text: `Skipped ${entryFor(entries, step.line)?.name ?? lineFallback(step.line)}: ${reasonText(reason)}.` }])
  }
  if (skipped.length > 3) steps.push([{ text: `Skipped ${skipped.length - 3} more that can’t run right now.` }])
  if (quick?.line) steps.push([{ text: 'Right now: ' }, { text: name(quick.line, quick.effort), strong: true }, { text: quick.harness ? ` through ${harnessLabel(quick.harness)}.` : '.' }])
  else if (quick) steps.push([{ text: 'Right now nothing qualifies, so quick jobs wait.' }])
  if (fallback?.line) steps.push([{ text: 'If none qualifies, the default runs: ' }, { text: name(fallback.line, fallback.effort), strong: true }, { text: '.' }])
  return { lead: 'Scout and mechanical work: reading code, triage, small changes that are fully described.', steps, foot: 'Agents still ask by role name (scout, mechanical); the answer comes from this page.' }
}
