// SPDX-License-Identifier: AGPL-3.0-only
// Settings › Models v2 fallback (AEON-1161). Risk: a chain edit that writes the wrong order, person or revision, turns
// an inherited level into a stored one, cannot be undone to what was shown, or reports success when a lock refused it.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError } from '../src/lib/api'
import {
  buildEntries, buildRows, causeShort, chainEfforts, defaultTail, fallbackSentence, lineRoutes, moveItem, neverWho, routeNote, sameOrder, stepStates,
  type RegistryProfile, type SimpleDocument, type SimpleRow, type TailBoard,
} from '../src/lib/modelsSimple'
import { deleteNever, getRegistry, getSimple, getTail, getWorkspaceRules, putChain, putEffort, putNever, putOrder, putWorkspaceRules, resetOrder } from '../src/lib/modelsSimpleApi'
import { useModelsSimple } from '../src/lib/useModelsSimple'
import { resetToasts, toasts } from '../src/lib/toast'
import { KINDS, registry, simplePerson } from './models-simple-fixtures'

const state = vi.hoisted(() => ({
  session: { identity: { tenant: { id: 'tenant' }, principal: { id: 'person', name: 'Markus', kind: 'person' } } as { tenant: { id: string }; principal: { id: string; name: string; kind: string } } | null, authenticationCurrent: () => true },
  cleanups: [] as (() => void)[], manage: true,
}))
vi.mock('../src/stores/session', () => ({ useSession: () => state.session }))
vi.mock('../src/lib/authz', () => ({ can: (permission: string) => permission === 'model_prefs.manage' ? state.manage : permission !== 'members.read', onAccessChange: () => () => {} }))
vi.mock('vue', async original => ({ ...await original<typeof import('vue')>(), onBeforeUnmount: (cleanup: () => void) => state.cleanups.push(cleanup) }))
vi.mock('../src/lib/modelsSimpleApi', async original => ({ ...await original<typeof import('../src/lib/modelsSimpleApi')>(), getSimple: vi.fn(), getTail: vi.fn(), getRegistry: vi.fn(), getWorkspaceRules: vi.fn(), putChain: vi.fn(), putOrder: vi.fn(), putEffort: vi.fn(), resetOrder: vi.fn(), putNever: vi.fn(), deleteNever: vi.fn(), putWorkspaceRules: vi.fn() }))

const SOL = 'openai:sol', OPUS = 'anthropic:opus', SONNET = 'anthropic:sonnet', FABLE = 'anthropic:fable', GROK = 'xai:grok'
const settle = async () => { for (let index = 0; index < 20; index++) await Promise.resolve() }
const labels = new Map(KINDS)
const entries = buildEntries(registry())
const never = { line: GROK, by: simplePerson, at: '2026-10-02T09:00:00Z', why: 'From the earlier model board.' }
function row(column: string, line: string, effort: string, extra: Partial<SimpleRow> = {}): SimpleRow {
  return { column, line, effort, harness: 'codex', model: 'm', mine: false, lock: null, chain: [], end: column === 'other' ? 'wait' : 'default', never: [], ...extra }
}
function doc(overrides: Partial<SimpleDocument> = {}): SimpleDocument {
  return {
    all: row('other', SOL, 'xhigh', { chain: [{ line: OPUS, effort: 'max' }] }),
    exceptions: [row('design', OPUS, 'xhigh', { chain: [{ line: SONNET, effort: 'xhigh' }], lock: { by: simplePerson, at: '2026-10-02T09:00:00Z', reason: 'Design stays on Opus.' }, never: [never] }), row('concept', FABLE, 'max', { end: 'wait' })],
    reviews: { mode: 'cross_family' }, next: null, unavailable: [], new_lines: [], revision: 3, rules_revision: 2, person_id: simplePerson, ...overrides,
  }
}
function tail(own: string[] = [], effort: Record<string, string | null> = {}): TailBoard {
  const column = (id: string, rank: string[]) => ({ column: id, label: labels.get(id) ?? id, fixed: id === 'other', hidden: false, source: own.includes(id) ? 'own' as const : 'default' as const, list: rank.map(line => ({ line })), not: [], cant: [], stored: { rank, not: [] }, ...(own.includes(id) ? { stored_effort: effort[id] ?? null } : {}) })
  return { person_id: simplePerson, revision: 3, profile: { dismissed_lines: [] }, columns: [column('other', [SOL, OPUS]), column('design', [OPUS, SONNET]), column('concept', [FABLE]), column('backend', [SOL])] }
}
const views = (input: SimpleDocument, scope: 'me' | 'default' = 'me') => buildRows({ doc: input, workspace: null, scope, canEdit: true, entries, labels })
/** The Default with Fable kept last for everyone by a pin to the bottom, migrated from the old board (AEON-1158). */
const PIN = { kind: 'rule', value: 'bottom', scope: 'workspace' }
const pinned = (): TailBoard => { const base = tail(['other']); return { ...base, columns: base.columns.map(column => column.column === 'other' ? { ...column, list: [{ line: SOL }, { line: OPUS }, { line: FABLE, lock: PIN }] } : column) } }
const pinnedDoc = (overrides: Partial<SimpleDocument> = {}) => doc({ all: row('other', SOL, 'xhigh', { chain: [{ line: OPUS, effort: 'max' }, { line: FABLE, effort: 'high' }] }), ...overrides })

describe('the fallback in words', () => {
  it('reads the pick, every step and the ending; the default always ends in Wait', () => {
    const [all, design, concept] = views(doc(), 'default')
    expect(fallbackSentence(all!, entries)).toEqual({ pick: 'GPT-6.1 Sol', steps: ['Opus 5.5', 'Wait'] })
    expect(fallbackSentence(design!, entries)).toEqual({ pick: 'Opus 5.5', steps: ['Sonnet 5.5', 'the default’s fallback'] })
    expect(fallbackSentence(concept!, entries)).toEqual({ pick: 'Fable 5.1', steps: ['Wait'] })
    expect(defaultTail(all, entries)).toEqual(['GPT-6.1 Sol', 'Opus 5.5', 'Wait'])
  })
  it('treats a row from an older server as an empty order, and a draft as a new exception', () => {
    const old = doc({ all: { column: 'other', line: SOL, effort: 'xhigh', mine: false, lock: null }, exceptions: [] })
    expect(views(old)[0]).toMatchObject({ chain: [], end: 'wait', never: [] })
    const draft = buildRows({ doc: doc(), workspace: null, scope: 'me', canEdit: true, entries, labels, draft: 'backend' }).at(-1)!
    expect(draft).toMatchObject({ draft: true, chain: [], end: 'default', never: [] })
  })
  it('lists each line’s routes in registry order, from its latest enabled version only', () => {
    const extra: RegistryProfile[] = [
      { ...registry().find(profile => profile.model === 'claude-opus-5-5')!, id: 'cursor-opus', harness: 'cursor', model: 'claude-opus-5.5', effort: 'high' },
      { ...registry().find(profile => profile.model === 'claude-opus-5-5')!, id: 'pi-opus', harness: 'pi', model: 'anthropic/claude-opus-5-5', effort: 'high' },
      { ...registry().find(profile => profile.model === 'claude-opus-5-5')!, id: 'pi-old', harness: 'pi', model: 'anthropic/claude-opus-5-4', model_version: '5.4', effort: 'high' },
      { ...registry().find(profile => profile.model === 'claude-sonnet-5-5')!, id: 'off', harness: 'cursor', model: 'claude-sonnet-5.5', enabled: false },
    ]
    const routes = lineRoutes([...registry(), ...extra])
    expect(routes.get(OPUS)).toEqual(['claude', 'cursor', 'pi']); expect(routes.get(SONNET)).toEqual(['claude']); expect(routes.get(SOL)).toEqual(['codex'])
    expect(routeNote(routes.get(OPUS))).toBe('Claude, then 2 more'); expect(routeNote(routes.get(OPUS), 'cursor')).toBe('Cursor, then 2 more'); expect(routeNote(routes.get(SOL))).toBe('Codex'); expect(routeNote(undefined)).toBe('')
  })
  it('says one cause per step in words, with its time, and never guesses about steps without evidence', () => {
    const now = Date.parse('2026-10-11T08:00:00Z')
    expect(causeShort({ cause: 'daily_cap', until: '2026-10-12T04:00:00Z' }, now)).toMatch(/^daily cap until \d\d:\d\d$/)
    expect(causeShort({ cause: 'paused', until: '2026-10-20T00:00:00Z' }, now)).toBe('paused until 20 Oct')
    expect(causeShort({ cause: 'no_tools', until: null }, now)).toBe('no tools in PAIMOS'); expect(causeShort({ until: null }, now)).toBe('can’t run right now')
    const design = views(doc())[1]!
    const capped = { column: 'design', line: OPUS, runs_instead: SONNET, effort: 'xhigh', reason: 'x', ticket: null, cause: 'daily_cap' as const, until: '2026-10-20T00:00:00Z', route: 'claude', waits: false }
    expect([...stepStates(design, [capped], now)]).toEqual([[OPUS, { text: 'daily cap until 20 Oct', runs: false }], [SONNET, { text: 'runs now', runs: true }]])
    expect([...stepStates(design, [{ ...capped, runs_instead: OPUS }], now)]).toEqual([[OPUS, { text: 'runs now, not through Claude', runs: true }]])
    expect(stepStates(design, [{ ...capped, column: 'concept' }], now).size).toBe(0)
  })
  it('stores step levels, keeps an inherited pick level inherited and leaves unknown lines automatic', () => {
    const known = (line: string) => line !== 'unknown:x'
    expect(chainEfforts({ pick: OPUS, pickEffort: 'xhigh', ownPick: false, steps: [{ line: SONNET, effort: 'max' }, { line: 'unknown:x', effort: 'high' }, { line: FABLE, effort: null }], known })).toEqual({ [SONNET]: 'max' })
    expect(chainEfforts({ pick: OPUS, pickEffort: 'xhigh', ownPick: true, steps: [], known })).toEqual({ [OPUS]: 'xhigh' })
    expect(moveItem([1, 2, 3], 2, 0)).toEqual([3, 1, 2]); expect(moveItem([1, 2], 0, 5)).toEqual([1, 2])
    expect(sameOrder({ line: SOL, effort: 'high', chain: [{ line: OPUS, effort: 'max' }], end: 'wait' }, { line: SOL, effort: 'high', chain: [{ line: OPUS, effort: 'high' }], end: 'wait' })).toBe(false)
    expect(neverWho(never, () => 'Ada')).toBe('Ada, 2 Oct'); expect(neverWho({ line: GROK, by: null, at: null, why: 'Saved exclusion' }, () => 'Ada')).toBe('saved exclusion')
  })
})

describe('chain writes', () => {
  beforeEach(() => {
    state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []; vi.resetAllMocks(); resetToasts(); state.manage = true
    vi.mocked(getSimple).mockResolvedValue(doc()); vi.mocked(getTail).mockResolvedValue(tail()); vi.mocked(getRegistry).mockResolvedValue(registry())
    vi.mocked(getWorkspaceRules).mockResolvedValue({ revision: 2, rules: [] })
    vi.mocked(putChain).mockImplementation(async (_scope, _column, _body, revision) => ({ revision: revision + 1, person_id: simplePerson }))
    vi.mocked(putOrder).mockImplementation(async (_scope, _column, _body, revision) => ({ revision: revision + 1, person_id: simplePerson }))
    vi.mocked(putWorkspaceRules).mockImplementation(async (_column, _body, revision) => ({ revision: revision + 1, rules: [] }))
    vi.mocked(resetOrder).mockImplementation(async (_scope, _column, revision) => ({ revision: revision + 1, person_id: simplePerson }))
    vi.mocked(putEffort).mockImplementation(async (_scope, _column, _effort, revision) => ({ revision: revision + 1, person_id: simplePerson }))
    vi.mocked(deleteNever).mockImplementation(async (_column, _line, revision) => ({ revision: revision + 1, rules: [] }))
    vi.mocked(putNever).mockImplementation(async (_column, _line, _why, revision) => ({ revision: revision + 1, rules: [] }))
  })
  async function page(scope: 'me' | 'default' = 'me') { const model = useModelsSimple(); await settle(); if (scope === 'default') { model.setScope('default'); await settle() } return model }
  const find = (model: Awaited<ReturnType<typeof page>>, key: string) => model.rows.value.find(item => item.key === key)!
  /** The server after one write: the same document at the next revision, so Undo may run against it. */
  const after = (revision: number, board = tail(['other'])) => { const current = vi.mocked(getSimple).getMockImplementation(); vi.mocked(getSimple).mockImplementation(async (...args) => ({ ...(current ? await current(...args) : doc()), revision })); vi.mocked(getTail).mockResolvedValue({ ...board, revision }) }

  it('moves a step with one rank-only write, says where it went, and Undo writes the order back', async () => {
    vi.mocked(getTail).mockResolvedValue(tail(['other']))
    vi.mocked(getSimple).mockResolvedValue(doc({ all: row('other', SOL, 'xhigh', { chain: [{ line: OPUS, effort: 'max' }, { line: SONNET, effort: 'xhigh' }] }) }))
    const model = await page()
    after(4)
    expect(await model.editChain(find(model, 'all'), { kind: 'move', from: 1, to: 0 })).toBe(true)
    expect(putChain).toHaveBeenCalledWith('me', 'other', { rank: [SOL, SONNET, OPUS] }, 3, simplePerson)
    expect(model.announcement.value).toBe('Sonnet 5.5 moved to step 2 of 3.')
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(putChain).toHaveBeenLastCalledWith('me', 'other', { rank: [SOL, OPUS, SONNET] }, 4, simplePerson)
  })
  it('adds a step with every step’s level; the pick’s level is written only when the row holds its own', async () => {
    let model = await page('default')
    after(4, tail())
    expect(await model.editChain(find(model, 'concept'), { kind: 'add', line: SONNET, effort: 'high' })).toBe(true)
    // Concepts has no stored level and Fable max is not the default's xhigh: its own, so it is kept.
    expect(putChain).toHaveBeenLastCalledWith('default', 'concept', { rank: [FABLE, SONNET], efforts: { [FABLE]: 'max', [SONNET]: 'high' } }, 3, simplePerson)
    expect(resetOrder).not.toHaveBeenCalled()
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(resetOrder).toHaveBeenCalledWith('default', 'concept', 4, simplePerson)
    state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []; vi.mocked(putChain).mockClear(); vi.mocked(getSimple).mockResolvedValue(doc())
    // A pick level that only follows the default (Opus at the default's xhigh, nothing stored) stays inherited.
    vi.mocked(getTail).mockResolvedValue(tail(['design'], { design: null }))
    model = await page('default')
    await model.editChain(find(model, 'design'), { kind: 'step', index: 0, line: FABLE, effort: 'high' })
    expect(putChain).toHaveBeenLastCalledWith('default', 'design', { rank: [OPUS, FABLE], efforts: { [FABLE]: 'high' } }, 3, simplePerson)
  })
  it('a member’s edit of a workspace row writes their own order with their person; Undo resets it', async () => {
    const model = await page()
    after(4, tail())
    expect(await model.editChain(find(model, 'concept'), { kind: 'end', end: 'default' })).toBe(true)
    expect(putChain).toHaveBeenCalledWith('me', 'concept', { rank: [FABLE], end: 'default' }, 3, simplePerson)
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(resetOrder).toHaveBeenCalledWith('me', 'concept', 4, simplePerson)
  })
  it('an own order edited back to the workspace’s follows it again; Undo brings the own order back as it was', async () => {
    const shared = doc(), mine = doc({ exceptions: [shared.exceptions[0]!, row('concept', FABLE, 'max', { end: 'default', mine: true })] })
    vi.mocked(getSimple).mockImplementation(async scope => scope === 'me' ? mine : shared); vi.mocked(getTail).mockResolvedValue(tail(['concept'], { concept: 'max' }))
    const model = await page()
    after(4, tail(['concept'], { concept: 'max' }))
    expect(await model.editChain(find(model, 'concept'), { kind: 'end', end: 'wait' })).toBe(true)
    expect(putChain).not.toHaveBeenCalled(); expect(resetOrder).toHaveBeenCalledWith('me', 'concept', 3, simplePerson)
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(putChain).toHaveBeenCalledWith('me', 'concept', { rank: [FABLE], efforts: { [FABLE]: 'max' }, end: 'default' }, 4, simplePerson)
    expect(putEffort).toHaveBeenCalledWith('me', 'concept', 'max', 5, simplePerson)
  })
  it('writes the order’s own level back after a write with levels, so rows that follow it keep following it', async () => {
    vi.mocked(getTail).mockResolvedValue(tail(['other'], { other: 'xhigh' }))
    const model = await page('default')
    after(4, tail(['other'], { other: 'xhigh' })); vi.mocked(getSimple).mockResolvedValue(doc({ revision: 5 })); vi.mocked(getTail).mockResolvedValue({ ...tail(['other'], { other: 'xhigh' }), revision: 5 })
    expect(await model.editChain(find(model, 'all'), { kind: 'add', line: SONNET, effort: 'high' })).toBe(true)
    expect(putChain).toHaveBeenCalledWith('default', 'other', { rank: [SOL, OPUS, SONNET], efforts: { [SOL]: 'xhigh', [OPUS]: 'max', [SONNET]: 'high' } }, 3, simplePerson)
    expect(putEffort).toHaveBeenCalledWith('default', 'other', 'xhigh', 4, simplePerson)
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(putChain).toHaveBeenLastCalledWith('default', 'other', { rank: [SOL, OPUS], efforts: { [SOL]: 'xhigh', [OPUS]: 'max' } }, 5, simplePerson)
    expect(putEffort).toHaveBeenLastCalledWith('default', 'other', 'xhigh', 6, simplePerson)
  })
  it('refuses a locked row, a draft, a line already in the order and an ending on the default, without a write', async () => {
    const model = await page()
    expect(await model.editChain(find(model, 'design'), { kind: 'remove', index: 0 })).toBe(false)
    expect(await model.editChain(find(model, 'all'), { kind: 'add', line: OPUS, effort: 'max' })).toBe(false)
    expect(await model.editChain(find(model, 'all'), { kind: 'add', line: SOL, effort: 'max' })).toBe(false)
    expect(await model.editChain(find(model, 'all'), { kind: 'end', end: 'default' })).toBe(false)
    model.startException('backend'); expect(await model.editChain(model.rows.value.at(-1)!, { kind: 'add', line: OPUS, effort: 'max' })).toBe(false)
    expect(putChain).not.toHaveBeenCalled(); expect(resetOrder).not.toHaveBeenCalled()
  })
  it('says a lock refused the change, refreshes, and offers no Undo', async () => {
    vi.mocked(putChain).mockRejectedValue(new APIError(423, 'column_locked'))
    const model = await page()
    expect(await model.editChain(find(model, 'concept'), { kind: 'add', line: SONNET, effort: 'high' })).toBe(false)
    expect(model.error.value).toBe('This row is locked for everyone; the change was not saved.'); expect(toasts).toHaveLength(0)
    expect(getSimple).toHaveBeenCalledTimes(4)
  })
  it('allows a Never line again only for admins on For everyone, against the rules revision; Undo sets it with its reason', async () => {
    let model = await page()
    expect(model.canAllow(find(model, 'design'), never)).toBe(false); expect(await model.allowAgain(find(model, 'design'), never)).toBe(false)
    state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []
    model = await page('default')
    expect(model.canAllow(find(model, 'design'), never)).toBe(true)
    vi.mocked(getSimple).mockResolvedValue(doc({ revision: 3, rules_revision: 3 }))
    expect(await model.allowAgain(find(model, 'design'), never)).toBe(true)
    expect(deleteNever).toHaveBeenCalledWith('design', GROK, 2); expect(model.announcement.value).toBe('Grok 4.7 is allowed again for UI design.')
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(putNever).toHaveBeenCalledWith('design', GROK, 'From the earlier model board.', 3)
  })
  it('Reset Undo writes the own order back whole: rank, exclusions, its Wait ending and each fallback’s level, then its own level', async () => {
    const stored = { rank: [FABLE, SONNET, OPUS], not: [GROK], end: 'wait', efforts: { [FABLE]: 'max', [SONNET]: 'high', [OPUS]: 'xhigh' } }
    const board = (): TailBoard => { const base = tail(['concept'], { concept: 'max' }); return { ...base, columns: base.columns.map(column => column.column === 'concept' ? { ...column, stored } : column) } }
    vi.mocked(getTail).mockResolvedValue(board())
    vi.mocked(getSimple).mockResolvedValue(doc({ exceptions: [doc().exceptions[0]!, row('concept', FABLE, 'max', { mine: true, end: 'wait', chain: [{ line: SONNET, effort: 'high' }, { line: OPUS, effort: 'xhigh' }] })] }))
    const model = await page()
    after(4, board())
    expect(await model.clear(find(model, 'concept'))).toBe(true)
    expect(resetOrder).toHaveBeenCalledWith('me', 'concept', 3, simplePerson)
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(putOrder).toHaveBeenCalledWith('me', 'concept', { rank: [FABLE, SONNET, OPUS], not: [GROK], efforts: { [FABLE]: 'max', [SONNET]: 'high', [OPUS]: 'xhigh' }, end: 'wait' }, 4, simplePerson)
    expect(putEffort).toHaveBeenLastCalledWith('me', 'concept', 'max', 5, simplePerson)
  })
  it('Remove of a row without an order of its own lifts its lock; Undo sets the lock again and invents no order', async () => {
    vi.mocked(getWorkspaceRules).mockResolvedValue({ revision: 2, rules: [{ scope: 'workspace', project_id: null, column: 'design', line: OPUS, lock: 'top', position: 0, why: 'Design stays on Opus.', set_by: simplePerson, set_at: '2026-10-02T09:00:00Z' }] })
    const model = await page('default')
    vi.mocked(getSimple).mockResolvedValue(doc({ revision: 4, rules_revision: 3 })); vi.mocked(getTail).mockResolvedValue({ ...tail(), revision: 4 })
    expect(await model.clear(find(model, 'design'))).toBe(true)
    expect(resetOrder).toHaveBeenCalledWith('default', 'design', 3, simplePerson); expect(putWorkspaceRules).toHaveBeenCalledWith('design', { top: [], bottom: [], not: {} }, 2)
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(putWorkspaceRules).toHaveBeenLastCalledWith('design', { top: [{ line: OPUS, why: 'Design stays on Opus.' }], bottom: [], not: {} }, 3)
    expect(putOrder).not.toHaveBeenCalled(); expect(putChain).not.toHaveBeenCalled()
  })
  it('on Just me a step a workspace pin keeps last stays there, and a new step joins before it', async () => {
    vi.mocked(getTail).mockResolvedValue(pinned()); vi.mocked(getSimple).mockResolvedValue(pinnedDoc())
    const model = await page()
    expect(model.fixedSteps(find(model, 'all'))).toBe(1)
    expect(await model.editChain(find(model, 'all'), { kind: 'remove', index: 1 })).toBe(false)
    expect(await model.editChain(find(model, 'all'), { kind: 'move', from: 1, to: 0 })).toBe(false)
    expect(await model.editChain(find(model, 'all'), { kind: 'move', from: 0, to: 1 })).toBe(false)
    expect(getWorkspaceRules).not.toHaveBeenCalled(); expect(putChain).not.toHaveBeenCalled()
    after(4, pinned())
    expect(await model.editChain(find(model, 'all'), { kind: 'add', line: SONNET, effort: 'high' })).toBe(true)
    expect(putChain).toHaveBeenCalledWith('me', 'other', { rank: [SOL, OPUS, SONNET, FABLE], efforts: { [SOL]: 'xhigh', [OPUS]: 'max', [SONNET]: 'high', [FABLE]: 'high' } }, 3, simplePerson)
    expect(putWorkspaceRules).not.toHaveBeenCalled()
  })
  it('an admin on For everyone takes a pinned step out, or moves it, with its pin lifted in the same change; Undo sets the pin again', async () => {
    const pin = { scope: 'workspace' as const, project_id: null, column: 'other', line: FABLE, lock: 'bottom' as const, position: 0, why: 'Last resort', set_by: simplePerson, set_at: '2026-09-01T08:00:00Z' }
    vi.mocked(getTail).mockResolvedValue(pinned()); vi.mocked(getSimple).mockResolvedValue(pinnedDoc()); vi.mocked(getWorkspaceRules).mockResolvedValue({ revision: 2, rules: [pin] })
    const model = await page('default')
    expect(model.fixedSteps(find(model, 'all'))).toBe(0)
    // The server after the change: the next revisions, so Undo may run against them.
    vi.mocked(getSimple).mockResolvedValue(pinnedDoc({ revision: 4, rules_revision: 3 })); vi.mocked(getTail).mockResolvedValue({ ...pinned(), revision: 4 })
    expect(await model.editChain(find(model, 'all'), { kind: 'remove', index: 1 })).toBe(true)
    expect(putWorkspaceRules).toHaveBeenCalledWith('other', { top: [], bottom: [], not: {} }, 2)
    expect(putChain).toHaveBeenCalledWith('default', 'other', { rank: [SOL, OPUS] }, 3, simplePerson)
    expect(vi.mocked(putWorkspaceRules).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(putChain).mock.invocationCallOrder[0]!)
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(putWorkspaceRules).toHaveBeenLastCalledWith('other', { top: [], bottom: [{ line: FABLE, why: 'Last resort' }], not: {} }, 3)
    expect(putChain).toHaveBeenLastCalledWith('default', 'other', { rank: [SOL, OPUS, FABLE], efforts: { [SOL]: 'xhigh', [OPUS]: 'max', [FABLE]: 'high' } }, 4, simplePerson)
    // Moving it up lifts its pin as well; the order then keeps the place it shows.
    vi.mocked(putChain).mockClear(); vi.mocked(putWorkspaceRules).mockClear(); vi.mocked(getWorkspaceRules).mockResolvedValue({ revision: 4, rules: [pin] })
    vi.mocked(getSimple).mockResolvedValue(pinnedDoc({ revision: 5, rules_revision: 4 })); vi.mocked(getTail).mockResolvedValue({ ...pinned(), revision: 5 }); await model.load()
    expect(await model.editChain(find(model, 'all'), { kind: 'move', from: 1, to: 0 })).toBe(true)
    expect(putWorkspaceRules).toHaveBeenCalledWith('other', { top: [], bottom: [], not: {} }, 4)
    expect(putChain).toHaveBeenCalledWith('default', 'other', { rank: [SOL, FABLE, OPUS] }, 5, simplePerson)
    expect(model.announcement.value).toBe('Fable 5.1 moved to step 2 of 3.')
  })
})
