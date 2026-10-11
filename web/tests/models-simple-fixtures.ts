// SPDX-License-Identifier: AGPL-3.0-only
// A small stateful stand-in for the Models server: orders per layer, workspace locks, the registry, what runs next.
// It follows the server's rules that matter to the page (revision checks, the person header, native efforts that
// carry to the nearest level) and records every write.
import type { Page } from '@playwright/test'
import type { ModelRule, RegistryProfile, SimpleDocument, SimpleRow, SimpleTrace, SimpleUnavailable, TailBoard, TailColumn } from '../src/lib/modelsSimple'

export const simplePerson = '11111111-1111-4111-8111-111111111111'
export const otherPerson = '22222222-2222-4222-8222-222222222222'
const LEVEL: Record<string, number> = { off: 0, default: 0, low: 1, medium: 2, high: 3, xhigh: 4, max: 5 }
type Line = { id: string; harness: string; family: string; model: string; efforts: string[]; display: string; short: string; version: string; note?: string; retireAt?: string; source?: 'auto' | 'manual'; tools?: boolean }
export const LINES: Line[] = [
  { id: 'openai:sol', harness: 'codex', family: 'openai', model: 'gpt-6.1-sol', efforts: ['low', 'medium', 'high', 'xhigh'], display: 'Codex Sol', short: 'Sol', version: '6.1', note: 'strongest for building' },
  { id: 'openai:astra', harness: 'codex', family: 'openai', model: 'gpt-6-astra', efforts: ['low', 'medium', 'high', 'xhigh'], display: 'Codex Astra', short: 'Astra', version: '6', note: 'deepest reasoning, slower' },
  { id: 'openai:luna', harness: 'codex', family: 'openai', model: 'gpt-6-luna', efforts: ['low', 'medium', 'high', 'xhigh'], display: 'Codex Luna', short: 'Luna', version: '6', note: 'fast and light', retireAt: '2099-10-31T00:00:00Z' },
  { id: 'anthropic:opus', harness: 'claude', family: 'anthropic', model: 'claude-opus-5-5', efforts: ['high', 'xhigh', 'max'], display: 'Claude Opus', short: 'Opus', version: '5.5', note: 'best for design and concepts' },
  { id: 'anthropic:sonnet', harness: 'claude', family: 'anthropic', model: 'claude-sonnet-5-5', efforts: ['high', 'xhigh', 'max'], display: 'Claude Sonnet', short: 'Sonnet', version: '5.5', note: 'fast everyday work' },
  { id: 'anthropic:fable', harness: 'claude', family: 'anthropic', model: 'claude-fable-5-1', efforts: ['high', 'xhigh', 'max'], display: 'Claude Fable', short: 'Fable', version: '5.1', note: 'careful with long texts' },
  { id: 'xai:grok', harness: 'grok', family: 'xai', model: 'grok-4.7', efforts: ['medium', 'high', 'xhigh'], display: 'Grok', short: 'Grok', version: '4.7', note: 'reviews and concepts', tools: false },
  { id: 'cursor:composer', harness: 'cursor', family: 'cursor', model: 'composer-2.5', efforts: ['default'], display: 'Composer', short: 'Composer', version: '2.5', note: 'quick edits in the editor' },
  { id: 'unknown:openrouter/qwen/qwen3-coder', harness: 'pi', family: 'unknown', model: 'openrouter/qwen/qwen3-coder', efforts: ['off'], display: 'Qwen3 Coder', short: 'Qwen3 Coder', version: '', note: 'trial for small scripts (AEON-1003)', source: 'manual' },
]
export const NEW_LINE: Line = { id: 'xai:grok-preview', harness: 'grok', family: 'xai', model: 'grok-4.8-preview', efforts: ['medium', 'high', 'xhigh'], display: 'Grok 4.8 preview', short: 'Grok 4.8 preview', version: '', note: 'reviews and concepts', tools: false }
export const KINDS: [string, string][] = [['design', 'UI design'], ['frontend', 'Frontend build'], ['backend', 'Backend build'], ['infra', 'Infrastructure'], ['docs', 'Docs and copy'], ['security', 'Security'], ['concept', 'Concepts']]
const REVIEWS = ['review:openai', 'review:anthropic', 'review:xai']

export function registry(extra: Line[] = []): RegistryProfile[] {
  return [...LINES, ...extra].flatMap(line => line.efforts.map((effort, index): RegistryProfile => ({
    id: `${line.model}/${effort}`, slug: line.id, version: '', created_at: '2026-01-01T00:00:00Z', harness: line.harness, family: line.family, model: line.model, effort, tier: 'strong', enabled: true,
    display_name: line.display, short_name: line.short, model_version: line.version, provider: line.family, effort_level: LEVEL[effort] ?? index, note: line.note ?? '',
    source: line.source ?? 'auto', retire_at: line.retireAt ?? null, retired: false,
  })))
}
const nearest = (line: Line, want: string) => line.efforts.includes(want) ? want : [...line.efforts].sort((a, b) => Math.abs((LEVEL[a] ?? 2) - (LEVEL[want] ?? 2)) - Math.abs((LEVEL[b] ?? 2) - (LEVEL[want] ?? 2)) || (LEVEL[b] ?? 0) - (LEVEL[a] ?? 0))[0]!

export interface MockOptions {
  /** The caller may set the workspace default, locks and rules. */
  manage?: boolean
  /** A pick whose account cannot run it: the Claude account is tied to another profile. */
  down?: boolean
  /** Announce a new model. */
  fresh?: boolean
  /** No overrides anywhere. */
  bare?: boolean
  /** The caller is a person with only their own choices (a member) who already overrides concepts. */
  member?: boolean
  noQueue?: boolean
  /** A workspace-authored kind with a very long name. */
  longLabels?: boolean
  /** Another person set the design lock (and the member list names them). */
  lockedByOther?: boolean
  /** Refuse every write with this status. */
  fail?: number
  german?: boolean
  fallback?: boolean
  /** Models v2 orders (AEON-1161): short fallbacks with their own levels and endings, and a Never rule on UI design. */
  v2?: boolean
  /** A pin to the bottom migrated from the old board: Fable stays the last step of the Default for everyone. */
  pinned?: boolean
}
interface Order { rank: string[] | null; not: string[]; effort: string | null; efforts?: Record<string, string>; end?: 'wait' | 'default' }
const TEMPLATE = ['openai:sol', 'anthropic:opus', 'anthropic:sonnet', 'anthropic:fable', 'openai:astra', 'xai:grok', 'openai:luna', 'cursor:composer', 'unknown:openrouter/qwen/qwen3-coder']
const REASON_DOWN = 'the Claude account is tied to another profile (AEON-1000)'
/** A reset far enough ahead to read as a date in any time zone. */
export const CAP_UNTIL = '2099-10-12T04:00:00Z'

export async function mockModels(page: Page, options: MockOptions = {}) {
  const profiles = registry(options.fresh ? [NEW_LINE] : [])
  const line = (id: string | null | undefined) => [...LINES, NEW_LINE].find(item => item.id === id)
  const layers = { mine: new Map<string, Order>(), default: new Map<string, Order>() }
  const revision = { mine: 3, default: 5 }
  const dismissed = { mine: [] as string[], default: [] as string[] }
  let rules: ModelRule[] = [], rulesRevision = 2
  const writes: { method: string; path: string; search: string; body: Record<string, unknown> | null; person?: string }[] = []
  let fail = options.fail, hold: (() => Promise<void>) | undefined, queued = !options.noQueue
  const state = { calls: [] as string[] }
  if (!options.bare) {
    layers.default.set('other', { rank: TEMPLATE, not: [], effort: 'xhigh' })
    layers.default.set('design', { rank: ['anthropic:opus', 'anthropic:sonnet', 'openai:sol'], not: [], effort: 'xhigh' })
    layers.default.set('concept', { rank: ['anthropic:opus', 'openai:sol'], not: [], effort: 'high' })
    rules = [{ scope: 'workspace', project_id: null, column: 'design', line: 'anthropic:opus', lock: 'top', position: 0, why: 'Design mocks stay on Opus while the design gate is tuned (AEON-912).', set_by: options.lockedByOther ? otherPerson : simplePerson, set_at: '2026-10-02T09:00:00Z' }]
  } else layers.default.set('other', { rank: TEMPLATE, not: [], effort: 'xhigh' })
  if (options.member) {
    layers.mine.set('other', { rank: ['openai:sol', ...TEMPLATE.slice(1)], not: [], effort: 'high' })
    layers.mine.set('concept', { rank: ['anthropic:fable', 'anthropic:opus'], not: [], effort: 'high' })
    layers.mine.set('docs', { rank: ['anthropic:sonnet', 'openai:sol'], not: [], effort: 'high' })
  }
  if (options.v2) {
    // The approved mock's workspace: Sol then Opus for all work; UI design locked on Opus, then Sonnet, then the
    // default's fallback, never Grok; Concepts on Fable alone, then wait. A member owns only Default and Docs.
    layers.default.set('other', { rank: ['openai:sol', 'anthropic:opus'], not: [], effort: 'xhigh', efforts: { 'openai:sol': 'xhigh', 'anthropic:opus': 'max' }, end: 'wait' })
    layers.default.set('design', { rank: ['anthropic:opus', 'anthropic:sonnet'], not: [], effort: 'xhigh', efforts: { 'anthropic:opus': 'xhigh', 'anthropic:sonnet': 'xhigh' }, end: 'default' })
    layers.default.set('concept', { rank: ['anthropic:fable'], not: [], effort: 'max', efforts: { 'anthropic:fable': 'max' }, end: 'wait' })
    rules.push({ scope: 'workspace', project_id: null, column: 'design', line: 'xai:grok', lock: 'not', position: 0, why: 'Moved here from the earlier model board', set_by: simplePerson, set_at: '2026-10-02T09:00:00Z' })
    if (options.pinned) rules.push({ scope: 'workspace', project_id: null, column: 'other', line: 'anthropic:fable', lock: 'bottom', position: 0, why: 'Last resort on the earlier model board', set_by: simplePerson, set_at: '2026-09-01T08:00:00Z' })
    layers.mine.clear()
    if (options.member) {
      layers.mine.set('other', { rank: ['openai:sol', 'anthropic:sonnet'], not: [], effort: 'high', efforts: { 'openai:sol': 'high', 'anthropic:sonnet': 'high' }, end: 'wait' })
      layers.mine.set('docs', { rank: ['anthropic:sonnet'], not: [], effort: 'high', end: 'default' })
    }
  }
  const lockOf = (column: string) => rules.find(rule => rule.column === column && rule.lock === 'top')
  // A pin to the bottom keeps its line last in every order of the column, as the server's resolver does.
  const bottomOf = (column: string) => rules.filter(rule => rule.column === column && rule.lock === 'bottom').sort((a, b) => a.position - b.position).map(rule => rule.line)
  const pinnedLast = (column: string, rank: string[]) => [...rank.filter(id => !bottomOf(column).includes(id)), ...bottomOf(column)]
  const own = (layer: 'mine' | 'default', column: string) => layer === 'mine' ? layers.mine.get(column) : layers.default.get(column)
  const inherited = (layer: 'mine' | 'default', column: string): Order => {
    const direct = own(layer, column)
    if (direct?.rank) return direct
    const workspace = layers.default.get(column)
    if (layer === 'mine' && workspace?.rank) return workspace
    return layer === 'mine' ? own('mine', 'other') ?? layers.default.get('other')! : layers.default.get('other')!
  }
  // A lock owns the whole order, so a member sees the workspace's (as the server does).
  const orderFor = (layer: 'mine' | 'default', column: string) => lockOf(column) ? inherited('default', column) : inherited(layer, column)
  // The row's level as the server layers it: the person's column, the workspace column, then the person's and the
  // workspace's default (a lock reads the workspace only). A line's own stored level wins over all of them.
  const layered = (layer: 'mine' | 'default', column: string, locked: boolean) => (locked || layer === 'default'
    ? [layers.default.get(column), layers.default.get('other')]
    : [layers.mine.get(column), layers.default.get(column), layers.mine.get('other'), layers.default.get('other')]).find(order => order?.effort)?.effort ?? 'high'
  const first = (layer: 'mine' | 'default', column: string) => {
    const lock = lockOf(column), order = orderFor(layer, column), id = lock?.line ?? order.rank![0]!, item = line(id)!
    const base = layered(layer, column, !!lock)
    return { id, item, effort: nearest(item, order.efforts?.[id] ?? base), lock, order, base }
  }
  const pick = (layer: 'mine' | 'default', column: string): SimpleRow => {
    const { id, item, effort, lock, order, base } = first(layer, column)
    const chain = pinnedLast(column, order.rank ?? []).filter(step => step !== id).map(step => ({ line: step, effort: line(step) ? nearest(line(step)!, order.efforts?.[step] ?? base) : null }))
    const never = [
      ...rules.filter(rule => rule.column === column && rule.lock === 'not').map(rule => ({ line: rule.line, by: rule.set_by, at: rule.set_at, why: rule.why })),
      ...order.not.map(not => ({ line: not, by: null, at: null, why: 'Saved exclusion' })),
    ]
    return { column, line: id, effort, harness: item.harness, model: item.model, mine: layer === 'mine' && !lock && !!layers.mine.get(column)?.rank, lock: lock ? { by: lock.set_by, at: lock.set_at, reason: lock.why } : null, chain, end: order.end ?? (column === 'other' ? 'wait' : 'default'), never }
  }
  const unavailableFor = (rows: SimpleRow[]): SimpleUnavailable[] => rows.flatMap((row): SimpleUnavailable[] => {
    if (options.down && row.line === 'anthropic:opus') return [{ column: row.column, line: row.line!, runs_instead: 'anthropic:sonnet', effort: nearest(line('anthropic:sonnet')!, row.effort ?? 'high'), reason: REASON_DOWN, ticket: null, cause: 'daily_cap', until: CAP_UNTIL, route: 'claude', waits: false }]
    if (row.line === 'xai:grok' && row.column !== 'concept' && row.column !== 'other') return [{ column: row.column, line: row.line, runs_instead: 'openai:sol', effort: 'high', reason: 'no tools in PAIMOS', ticket: null, cause: 'no_tools', until: null, route: 'grok', waits: false }]
    return []
  })
  function simple(layer: 'mine' | 'default'): SimpleDocument {
    const all = pick(layer, 'other'), exceptions: SimpleRow[] = []
    for (const [column] of KINDS) {
      const row = pick(layer, column), workspace = layers.default.get(column)
      const differs = row.line !== all.line || row.effort !== all.effort
      if (row.lock || row.mine || (workspace?.rank && differs) || (layer === 'mine' && layers.mine.get(column)?.rank)) exceptions.push(row)
    }
    const rows = [all, ...exceptions]
    const down = !!options.down
    const design = { line: 'anthropic:opus', effort: 'xhigh' }
    const next = !queued ? null : down
      ? { column: 'design', line: 'anthropic:sonnet', effort: 'xhigh', ticket: 'AEON-1011', reviewer: { line: 'openai:sol', effort: 'xhigh' }, trace: [
        { role: 'build', line: design.line, stage: 'column', reason: REASON_DOWN, selected: false }, { role: 'build', line: 'anthropic:sonnet', stage: 'column', reason: '', selected: true },
        { role: 'review-gate', line: 'openai:sol', stage: 'role', reason: '', selected: true }] satisfies SimpleTrace[] }
      : { column: 'backend', line: first(layer, 'backend').id, effort: first(layer, 'backend').effort, ticket: 'AEON-1011', reviewer: { line: 'xai:grok', effort: 'xhigh' }, trace: [
        { role: 'build', line: first(layer, 'backend').id, stage: 'default', reason: '', selected: true }, { role: 'review-gate', line: 'xai:grok', stage: 'role', reason: '', selected: true }] satisfies SimpleTrace[] }
    const dis = dismissed[layer]
    return {
      all, exceptions, reviews: { mode: 'cross_family' }, next, unavailable: unavailableFor(rows),
      // A line is new until someone places it in an order or says "not now".
      new_lines: options.fresh && !dis.includes(NEW_LINE.id) && ![...layers.mine.values(), ...layers.default.values()].some(order => order.rank?.includes(NEW_LINE.id)) ? [{ line: NEW_LINE.id, can_do: ['concept'] }] : [],
      revision: revision[layer], rules_revision: rulesRevision, person_id: simplePerson,
    }
  }
  function tail(layer: 'mine' | 'default'): TailBoard {
    const columns: TailColumn[] = [['other', 'Everything else'], ...KINDS.map(([column, label]) => [column, options.longLabels && column === 'design' ? 'UI design for the customer-facing marketing site and every campaign landing page' : label] as [string, string]), ...REVIEWS.map(column => [column, `Review of ${column.split(':')[1]}`] as [string, string])].map(([column, label]) => {
      const order = inherited(layer, column!), lock = lockOf(column!), direct = own(layer, column!)
      const rank = pinnedLast(column!, lock ? [lock.line, ...order.rank!.filter(id => id !== lock.line)] : order.rank!)
      return { column: column!, label: label!, fixed: column === 'other' || column!.startsWith('review:'), hidden: false, source: direct?.rank ? 'own' : 'template',
        list: rank.map(id => ({ line: id, ...(lock && id === lock.line ? { lock: { kind: 'rule' } } : bottomOf(column!).includes(id) ? { lock: { kind: 'rule', value: 'bottom', scope: 'workspace' } } : {}) })), not: order.not.map(id => ({ line: id })),
        stored: { rank: [...(order.rank ?? [])], not: [...order.not], ...(direct?.rank ? { end: direct.end, efforts: direct.efforts } : {}) }, ...(direct ? { stored_effort: direct.effort } : {}),
        cant: column === 'concept' || column === 'other' || column!.startsWith('review:') ? [] : [{ line: 'xai:grok', reason: 'No tools in PAIMOS' }, ...(options.fresh ? [{ line: NEW_LINE.id, reason: 'No tools in PAIMOS' }] : [])] } as TailColumn
    })
    return { person_id: simplePerson, revision: revision[layer], profile: { dismissed_lines: dismissed[layer] }, columns }
  }
  const permissions = ['models.read', 'members.read', ...(options.manage === false || options.member ? [] : ['model_prefs.manage', 'models.manage'])]
  await page.route('**/api/**', async route => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname.replace(/^\/api/, ''), method = request.method()
    const known = path === '/me/permissions' || path === '/members' || path === '/models' || path.startsWith('/model-preferences') || path.startsWith('/model-rules')
    if (options.fallback && !known) return route.fallback()
    state.calls.push(`${method} ${path}${url.search}`)
    if (path === '/me/permissions') return route.fulfill({ json: { workspace: { id: 'board-tenant', role: 'member', permissions }, project: null } })
    if (path === '/projects') return route.fulfill({ json: { items: [], next_cursor: null } })
    if (path === '/members' && method === 'GET') return route.fulfill({ json: { people: [{ principal_id: simplePerson, name: 'Markus' }, { principal_id: otherPerson, name: 'Ada Admin' }], agents: [], invites: [], imported: [], owner_count: 1 } })
    if (path === '/models' && method === 'GET') return route.fulfill({ json: profiles })
    if (path === '/models/refresh' && method === 'GET') return route.fulfill({ json: { settings: { agent_reports_enabled: true, auto_add_profiles: true, api_enabled: false, interval_minutes: 1440 }, last_run_at: null, last_result: { sources: [] }, sources: [], observations: [] } })
    if (path === '/model-rules' && method === 'GET') return route.fulfill({ json: { revision: rulesRevision, rules } })
    const layer = url.searchParams.get('for') === 'default' || url.searchParams.get('layer') === 'default' ? 'default' as const : 'mine' as const
    if (method === 'GET' && path === '/model-preferences/simple') return route.fulfill({ json: simple(url.searchParams.get('for') === 'default' ? 'default' : 'mine') })
    if (method === 'GET' && path === '/model-preferences/board') return route.fulfill({ json: tail(layer) })
    if (method === 'GET') return route.fulfill({ json: { items: [] } })
    const body = (request.postDataJSON() ?? null) as Record<string, unknown> | null, person = request.headers()['if-prefs-person']
    writes.push({ method, path, search: url.search, body, person })
    if (hold) await hold()
    if (fail) return route.fulfill({ status: fail, json: { error: fail === 409 ? 'stale_revision' : 'forbidden' } })
    if (path.startsWith('/model-rules/workspace/')) {
      const column = decodeURIComponent(path.split('/')[3]!), input = body as { top: { line: string; why: string }[]; bottom: { line: string; why: string }[]; not: Record<string, string>; revision: number }
      if (input.revision !== rulesRevision) return route.fulfill({ status: 409, json: { error: 'stale_revision' } })
      const kept = rules.filter(rule => rule.column === column && rule.lock === 'not')
      rules = rules.filter(rule => rule.column !== column).concat(input.top.map((pin, position) => ({ scope: 'workspace' as const, project_id: null, column, line: pin.line, lock: 'top' as const, position, why: pin.why, set_by: simplePerson, set_at: '2026-10-09T09:00:00Z' })))
        .concat(input.bottom.map((pin, position) => ({ scope: 'workspace' as const, project_id: null, column, line: pin.line, lock: 'bottom' as const, position, why: pin.why, set_by: simplePerson, set_at: '2026-10-09T09:00:00Z' })))
        .concat(Object.entries(input.not ?? {}).map(([id, why], position) => ({ ...(kept.find(rule => rule.line === id) ?? { scope: 'workspace' as const, project_id: null, column, line: id, lock: 'not' as const, set_by: simplePerson, set_at: '2026-10-09T09:00:00Z' }), position, why })))
      return route.fulfill({ json: { revision: ++rulesRevision, rules } })
    }
    // Never rules (AEON-1159): workspace only, against the rules revision; PUT needs a reason.
    const neverPath = /^\/model-preferences\/orders\/([^/]+)\/first\/never\/([^/]+)$/.exec(path)
    if (neverPath) {
      const column = decodeURIComponent(neverPath[1]!), id = decodeURIComponent(neverPath[2]!), submitted = method === 'DELETE' ? Number(url.searchParams.get('revision')) : (body as { revision: number }).revision
      if (!permissions.includes('model_prefs.manage')) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
      if (submitted !== rulesRevision) return route.fulfill({ status: 409, json: { error: 'stale_revision' } })
      const before = rules.find(rule => rule.column === column && rule.line === id && rule.lock === 'not')
      rules = rules.filter(rule => rule !== before)
      if (method === 'PUT') rules.push({ scope: 'workspace', project_id: null, column, line: id, lock: 'not', position: 0, why: (body as { why: string }).why, set_by: simplePerson, set_at: '2026-10-11T08:00:00Z' })
      return route.fulfill({ json: { revision: ++rulesRevision, rules } })
    }
    if (layer === 'mine' && !person && path.startsWith('/model-preferences/')) return route.fulfill({ status: 428, json: { error: 'person_precondition_required' } })
    const target = layer, orders = layers[target]
    if (path.startsWith('/model-preferences/orders/')) {
      const column = decodeURIComponent(path.split('/')[3]!), submitted = method === 'DELETE' ? Number(url.searchParams.get('revision')) : (body as { revision: number }).revision
      // A workspace lock freezes the pick and its order for everyone's own writes.
      if (target === 'mine' && lockOf(column)) return route.fulfill({ status: 423, json: { error: 'column_locked' } })
      if (submitted !== revision[target]) return route.fulfill({ status: 409, json: { error: 'stale_revision' } })
      const before = orders.get(column)
      if (method === 'DELETE') orders.delete(column)
      else if (path.endsWith('/thinking')) {
        const effort = (body as { effort: string | null }).effort, item = line(first(target, column).id)!
        if (effort !== null && !item.efforts.includes(effort)) return route.fulfill({ status: 422, json: { error: 'invalid_effort' } })
        const rank = before?.rank ?? inherited(target, column).rank
        orders.set(column, { ...before, rank, not: before?.not ?? [], effort, efforts: { ...before?.efforts, ...(rank?.[0] && effort ? { [rank[0]]: effort } : {}) } })
      } else {
        const input = body as { rank: string[]; not?: string[]; efforts?: Record<string, string>; end?: 'wait' | 'default' }, item = line(input.rank[0])
        // Omitted not, efforts and end keep what is stored; a first personal order copies the workspace's ending and levels.
        const source = before ?? (target === 'mine' ? inherited('default', column) : undefined)
        const kept = Object.fromEntries(Object.entries(source?.efforts ?? {}).filter(([id]) => input.rank.includes(id)))
        if (input.efforts && Object.entries(input.efforts).some(([id, effort]) => !input.rank.includes(id) || !line(id)?.efforts.includes(effort))) return route.fulfill({ status: 422, json: { error: 'invalid_effort' } })
        // A new first line keeps the order's effort name, or the nearest level of the new line.
        orders.set(column, { rank: input.rank, not: input.not ?? before?.not ?? [], effort: input.efforts ? null : before?.effort && item ? nearest(item, before.effort) : null, efforts: input.efforts ?? kept, end: input.end ?? source?.end })
      }
      revision[target]++
      return route.fulfill({ json: { person_id: simplePerson, revision: revision[target], profile: tail(target).profile, dry_run: false, moved: [] } })
    }
    if (path.startsWith('/model-preferences/tray/')) {
      if ((body as { revision: number }).revision !== revision[target]) return route.fulfill({ status: 409, json: { error: 'stale_revision' } })
      dismissed[target].push(decodeURIComponent(path.split('/')[3]!)); revision[target]++
      return route.fulfill({ json: { person_id: simplePerson, revision: revision[target], profile: tail(target).profile, dry_run: false, moved: [] } })
    }
    if (path === '/model-preferences/profile') {
      const input = body as { dismissed_lines?: string[]; revision: number }
      if (input.revision !== revision[target]) return route.fulfill({ status: 409, json: { error: 'stale_revision' } })
      if (input.dismissed_lines) dismissed[target] = input.dismissed_lines
      revision[target]++
      return route.fulfill({ json: { person_id: simplePerson, revision: revision[target], profile: tail(target).profile, dry_run: false, moved: [] } })
    }
    return route.fulfill({ json: {} })
  })
  return {
    writes, calls: state.calls, orders: layers, get rules() { return rules }, revision, setFail: (status?: number) => { fail = status }, setQueued: (value: boolean) => { queued = value },
    holdNext: () => { let release!: () => void; const barrier = new Promise<void>(resolve => { release = resolve }); hold = () => barrier; return () => { hold = undefined; release() } },
  }
}
