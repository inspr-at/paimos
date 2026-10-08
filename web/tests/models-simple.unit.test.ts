// SPDX-License-Identifier: AGPL-3.0-only
// Settings › Models, minimal (AEON-1011). Risk: a pick, a lock or a reset is written against the wrong person,
// scope or revision, or reports success it did not have. Writes are bound to what the page showed; Undo only runs
// against the state it undid; every refusal is said out loud.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, api } from '../src/lib/api'
import {
  buildEntries, buildRows, movePin, nearestEffort, nextView, orderWithFirst, pinFirst, reasonText, rulesBody, unpin,
  type RegistryProfile, type SimpleDocument, type TailBoard,
} from '../src/lib/modelsSimple'
import { dismissLine, getRegistry, getSimple, getTail, getWorkspaceRules, putDismissed, putEffort, putOrder, putWorkspaceRules, resetOrder } from '../src/lib/modelsSimpleApi'
import { useModelsSimple } from '../src/lib/useModelsSimple'
import { resetToasts, toasts } from '../src/lib/toast'
import { KINDS, NEW_LINE, registry, simplePerson } from './models-simple-fixtures'

const state = vi.hoisted(() => ({
  session: { identity: { tenant: { id: 'tenant' }, principal: { id: 'person', name: 'Markus', kind: 'person' } } as { tenant: { id: string }; principal: { id: string; name: string; kind: string } } | null, authenticationCurrent: () => true },
  cleanups: [] as (() => void)[], access: undefined as (() => void) | undefined, manage: true,
}))
vi.mock('../src/stores/session', () => ({ useSession: () => state.session }))
vi.mock('../src/lib/authz', () => ({ can: (permission: string) => permission !== 'model_prefs.manage' || state.manage, onAccessChange: (listener: () => void) => { state.access = listener; return () => {} } }))
vi.mock('vue', async original => ({ ...await original<typeof import('vue')>(), onBeforeUnmount: (cleanup: () => void) => state.cleanups.push(cleanup) }))
vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), api: vi.fn() }))
vi.mock('../src/lib/modelsSimpleApi', async original => ({ ...await original<typeof import('../src/lib/modelsSimpleApi')>(), getSimple: vi.fn(), getTail: vi.fn(), getRegistry: vi.fn(), getWorkspaceRules: vi.fn(), putOrder: vi.fn(), resetOrder: vi.fn(), putEffort: vi.fn(), putWorkspaceRules: vi.fn(), dismissLine: vi.fn(), putDismissed: vi.fn() }))

const settle = async () => { for (let index = 0; index < 20; index++) await Promise.resolve() }
const deferred = <T>() => { let resolve!: (value: T) => void, reject!: (error: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const nameOf = (profiles: RegistryProfile[]) => buildEntries(profiles)
const label = new Map(KINDS)

function simpleDoc(overrides: Partial<SimpleDocument> = {}): SimpleDocument {
  const row = (column: string, line: string, effort: string, extra = {}) => ({ column, line, effort, harness: 'codex', model: 'm', mine: false, lock: null, ...extra })
  return {
    all: row('other', 'openai:sol', 'xhigh'), exceptions: [row('design', 'anthropic:opus', 'xhigh', { lock: { by: simplePerson, at: '2026-10-02T09:00:00Z', reason: 'Locked in Settings › Models by Markus' } }), row('concept', 'anthropic:opus', 'high')],
    reviews: { mode: 'cross_family' }, next: null, unavailable: [], new_lines: [], revision: 3, rules_revision: 2, person_id: simplePerson, ...overrides,
  }
}
function tailBoard(overrides: Partial<TailBoard> = {}): TailBoard {
  const column = (id: string, source: 'own' | 'default' | 'template', list: string[], locked?: string) => ({ column: id, label: label.get(id) ?? id, fixed: id === 'other', hidden: false, source, list: list.map(line => ({ line, ...(line === locked ? { lock: { kind: 'rule' } } : {}) })), not: [{ line: 'unknown:x' }], cant: [] })
  return { person_id: simplePerson, revision: 3, profile: { dismissed_lines: ['xai:old'] }, columns: [column('other', 'template', ['openai:sol', 'anthropic:opus', 'anthropic:sonnet']), column('design', 'default', ['anthropic:opus', 'anthropic:sonnet'], 'anthropic:opus'), column('concept', 'template', ['anthropic:opus', 'openai:sol']), column('backend', 'template', ['openai:sol', 'anthropic:sonnet'])], ...overrides }
}
const rulesDoc = (revision = 2) => ({ revision, rules: [{ scope: 'workspace' as const, project_id: null, column: 'design', line: 'anthropic:opus', lock: 'top' as const, position: 0, why: 'Design mocks stay on Opus.', set_by: simplePerson, set_at: '2026-10-02T09:00:00Z' }] })

describe('the registry as entries', () => {
  it('shows each line once, in its newest version, with the model’s own level names, grouped by harness order', () => {
    const entries = nameOf(registry())
    expect(entries.map(entry => entry.name)).toEqual(['GPT-6.1 Sol', 'GPT-6 Astra', 'GPT-6 Luna', 'Opus 5.5', 'Sonnet 5.5', 'Fable 5.1', 'Grok 4.7', 'Composer 2.5', 'Qwen3 Coder'])
    expect(entries.map(entry => entry.harness)).toEqual(['codex', 'codex', 'codex', 'claude', 'claude', 'claude', 'grok', 'cursor', 'pi'])
    expect(entries.find(entry => entry.line === 'anthropic:opus')!.efforts.map(effort => effort.name)).toEqual(['high', 'xhigh', 'max'])
    expect(entries.find(entry => entry.line === 'cursor:composer')!.efforts.map(effort => effort.name)).toEqual(['default'])
    expect(entries.find(entry => entry.line === 'openai:sol')).toMatchObject({ note: 'strongest for building', slug: 'gpt-6.1-sol' })
    expect(entries.find(entry => entry.line === 'unknown:openrouter/qwen/qwen3-coder')).toMatchObject({ route: 'OpenRouter', source: 'manual' })
  })
  it('follows a newer version of a line by itself and leaves out a retired level', () => {
    const older = registry().filter(profile => profile.model === 'claude-opus-5-5').map(profile => ({ ...profile, id: `${profile.id}/old`, model: 'claude-opus-5-4', model_version: '5.4' }))
    const newer = registry().filter(profile => profile.model === 'claude-opus-5-5').map(profile => ({ ...profile, id: `${profile.id}/new`, model: 'claude-opus-5-6', model_version: '5.6', effort: profile.effort === 'max' ? 'ultra' : profile.effort }))
    expect(nameOf([...registry(), ...older, ...newer]).find(entry => entry.line === 'anthropic:opus')).toMatchObject({ name: 'Opus 5.6', slug: 'claude-opus-5-6' })
    // A retired level of the newest version is not offered while the version still has active levels.
    const retired = registry().map(profile => profile.model === 'claude-opus-5-5' && profile.effort === 'max' ? { ...profile, retired: true } : profile)
    expect(nameOf([...retired, ...older]).find(entry => entry.line === 'anthropic:opus')!.efforts.map(effort => effort.name)).toEqual(['high', 'xhigh'])
  })
  it('announces a scheduled retirement only while it is ahead', () => {
    expect(buildEntries(registry(), Date.parse('2026-10-09T00:00:00Z')).find(entry => entry.line === 'openai:luna')!.retiring).toBe('2099-10-31T00:00:00Z')
    expect(buildEntries(registry().map(profile => profile.model === 'gpt-6-luna' ? { ...profile, retire_at: '2026-10-01T00:00:00Z' } : profile), Date.parse('2026-10-09T00:00:00Z')).find(entry => entry.line === 'openai:luna')!.retiring).toBeNull()
  })
  it('keeps the level name when the model has it, else the nearest by level, ties going up', () => {
    const [sol, opus, composer] = ['openai:sol', 'anthropic:opus', 'cursor:composer'].map(line => nameOf(registry()).find(entry => entry.line === line)!)
    expect(nearestEffort(opus, 'xhigh')).toBe('xhigh')
    expect(nearestEffort(opus, 'medium', 2)).toBe('high')
    expect(nearestEffort(sol, 'max', 5)).toBe('xhigh')
    expect(nearestEffort({ ...opus, efforts: [{ name: 'high', level: 3 }, { name: 'max', level: 5 }] }, 'xhigh', 4)).toBe('max')
    expect(nearestEffort(composer, 'xhigh', 4)).toBe('default')
    expect(nearestEffort(null, 'high')).toBeNull()
  })
})

describe('rows', () => {
  const entries = nameOf(registry())
  it('shows the default first and the rest as exceptions; a locked row is text for people who are not editing for everyone', () => {
    const rows = buildRows({ doc: simpleDoc(), workspace: null, scope: 'me', canEdit: true, entries, labels: label })
    expect(rows.map(row => [row.key, row.label, row.editable, !!row.lock])).toEqual([['all', 'Default · all work', true, false], ['design', 'UI design', false, true], ['concept', 'Concepts', true, false]])
    const everyone = buildRows({ doc: simpleDoc(), workspace: null, scope: 'default', canEdit: true, entries, labels: label })
    expect(everyone.map(row => [row.key, row.editable, row.remove, row.mine, row.reset])).toEqual([['all', true, false, false, false], ['design', true, true, false, false], ['concept', true, true, false, false]])
  })
  it('tells “yours · reset” from a row only the person has', () => {
    const own = simpleDoc({ all: { ...simpleDoc().all, mine: true }, exceptions: [{ ...simpleDoc().exceptions[1]!, mine: true }, { ...simpleDoc().exceptions[1]!, column: 'docs', mine: true }] })
    const rows = buildRows({ doc: own, workspace: simpleDoc(), scope: 'me', canEdit: true, entries, labels: label })
    expect(rows.map(row => [row.key, row.mine, row.reset, row.remove])).toEqual([['all', true, true, false], ['concept', true, true, false], ['docs', true, false, true]])
    // Without the workspace's rows the page cannot tell, and offers the safe word: both delete the person's order.
    expect(buildRows({ doc: own, workspace: null, scope: 'me', canEdit: true, entries, labels: label }).map(row => row.remove)).toEqual([false, false, false])
  })
  it('adds a draft row for the next kind from the default, and nothing for a viewer who may not edit', () => {
    const rows = buildRows({ doc: simpleDoc(), workspace: null, scope: 'me', canEdit: true, entries, labels: label, draft: 'backend' })
    expect(rows.at(-1)).toMatchObject({ key: 'backend', label: 'Backend build', line: 'openai:sol', effort: 'xhigh', draft: true, editable: true })
    expect(buildRows({ doc: simpleDoc(), workspace: null, scope: 'me', canEdit: false, entries, labels: label }).every(row => !row.editable)).toBe(true)
  })
  it('puts the column the server cannot run under its row with the server’s reason', () => {
    const doc = simpleDoc({ unavailable: [{ column: 'concept', line: 'anthropic:opus', runs_instead: 'anthropic:sonnet', effort: 'high', reason: 'default: the Claude account is tied to another profile', ticket: null }] })
    expect(buildRows({ doc, workspace: null, scope: 'me', canEdit: true, entries, labels: label }).find(row => row.key === 'concept')!.unavailable).toMatchObject({ runs_instead: 'anthropic:sonnet' })
    expect(reasonText('default: The Claude account; role: No room')).toBe('the Claude account; No room')
    expect(reasonText('')).toBe('no qualified account has room right now')
  })
})

describe('orders and locks', () => {
  it('moves one line to the front of the stored order and keeps everything else, never writing a rule', () => {
    const column = tailBoard().columns.find(item => item.column === 'design')!
    expect(orderWithFirst(column, 'anthropic:sonnet')).toEqual({ rank: ['anthropic:sonnet'], not: ['unknown:x'] })
    const other = tailBoard().columns.find(item => item.column === 'other')!
    expect(orderWithFirst(other, 'anthropic:sonnet')).toEqual({ rank: ['anthropic:sonnet', 'openai:sol', 'anthropic:opus'], not: ['unknown:x'] })
  })
  it('pins to the top with a reason, moves the pin with the pick and removes only that pin', () => {
    const before = rulesBody({ revision: 2, rules: [...rulesDoc().rules, { ...rulesDoc().rules[0]!, scope: 'project' as const, project_id: 'p', line: 'xai:grok' }, { ...rulesDoc().rules[0]!, column: 'concept', line: 'openai:sol' }] }, 'design')
    expect(before).toEqual({ top: [{ line: 'anthropic:opus', why: 'Design mocks stay on Opus.' }], bottom: [], not: {} })
    expect(movePin(before, 'anthropic:opus', 'anthropic:sonnet').top).toEqual([{ line: 'anthropic:sonnet', why: 'Design mocks stay on Opus.' }])
    expect(pinFirst({ top: [{ line: 'a', why: 'x' }], bottom: [{ line: 'b', why: 'y' }], not: { c: 'z' } }, 'b', 'Locked in Settings › Models by Ada')).toEqual({ top: [{ line: 'b', why: 'Locked in Settings › Models by Ada' }, { line: 'a', why: 'x' }], bottom: [], not: { c: 'z' } })
    expect(unpin({ top: [{ line: 'a', why: 'x' }, { line: 'b', why: 'y' }], bottom: [], not: {} }, 'a').top).toEqual([{ line: 'b', why: 'y' }])
  })
})

describe('what runs next', () => {
  const entries = nameOf(registry()), labels = label
  const rows = buildRows({ doc: simpleDoc(), workspace: null, scope: 'me', canEdit: true, entries, labels })
  const next = { column: 'design', line: 'anthropic:sonnet', effort: 'xhigh', ticket: 'AEON-1', reviewer: { line: 'openai:sol', effort: 'xhigh' }, trace: [
    { role: 'build', line: 'anthropic:opus', stage: 'column' as const, reason: 'the Claude account is tied to another profile', selected: false }, { role: 'build', line: 'anthropic:sonnet', stage: 'column' as const, reason: '', selected: true },
    { role: 'review-gate', line: 'xai:grok', stage: 'role' as const, reason: 'no tools here', selected: false }, { role: 'review-gate', line: 'openai:sol', stage: 'role' as const, reason: '', selected: true }] }
  it('is one sentence with the model after any skip, its level and the reviewer, and a numbered trace of skips', () => {
    const view = nextView({ next, rows, entries, labels })
    expect(view.parts.map(part => part.text).join('')).toBe('The next UI design runs on Sonnet 5.5 · xhigh instead of Opus 5.5, reviewed by GPT-6.1 Sol.')
    expect(view.why!.title).toBe('Why Sonnet 5.5?')
    expect(view.why!.steps.map(step => step.map(part => part.text).join(''))).toEqual([
      'UI design has its own model: Opus 5.5 · xhigh.', 'Skipped Opus 5.5: the Claude account is tied to another profile.',
      'Next in its order that can do UI design: Sonnet 5.5 · xhigh.', 'Reviews always use another family: GPT-6.1 Sol.'])
  })
  it('leaves a reviewer skip out of the build trace, and says plainly when nothing is queued', () => {
    const quiet = nextView({ next: { ...next, line: 'openai:sol', effort: 'high', column: 'backend', trace: [{ role: 'build', line: 'openai:sol', stage: 'default', reason: '', selected: true }] }, rows, entries, labels })
    expect(quiet.parts.map(part => part.text).join('')).toBe('The next Backend build runs on GPT-6.1 Sol · high, reviewed by GPT-6.1 Sol.')
    expect(quiet.why!.steps[0]!.map(part => part.text).join('')).toBe('Backend build has no override, so it uses the default: GPT-6.1 Sol · xhigh.')
    expect(nextView({ next: null, rows, entries, labels })).toEqual({ parts: [{ text: 'No work is queued right now.' }], why: null })
  })
})

describe('the card’s writes', () => {
  beforeEach(() => {
    state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []; vi.resetAllMocks(); resetToasts()
    state.session.identity = { tenant: { id: 'tenant' }, principal: { id: 'person', name: 'Markus', kind: 'person' } }; state.manage = true
    vi.mocked(getSimple).mockImplementation(async scope => scope === 'me' ? simpleDoc() : simpleDoc({ exceptions: simpleDoc().exceptions }))
    vi.mocked(getTail).mockResolvedValue(tailBoard()); vi.mocked(getRegistry).mockResolvedValue(registry([])); vi.mocked(getWorkspaceRules).mockResolvedValue(rulesDoc())
    const advance = (revision: number) => ({ revision, person_id: simplePerson })
    vi.mocked(putOrder).mockImplementation(async (_scope, _column, _body, revision) => advance(revision + 1)); vi.mocked(resetOrder).mockImplementation(async (_scope, _column, revision) => advance(revision + 1))
    vi.mocked(putEffort).mockImplementation(async (_scope, _column, _effort, revision) => advance(revision + 1)); vi.mocked(putDismissed).mockImplementation(async (_scope, _lines, revision) => advance(revision + 1))
    vi.mocked(dismissLine).mockImplementation(async (_scope, _line, revision) => advance(revision + 1)); vi.mocked(putWorkspaceRules).mockImplementation(async (_column, _body, revision) => ({ ...rulesDoc(), revision: revision + 1 }))
  })
  async function page() { const model = useModelsSimple(); await settle(); return model }

  it('loads the stored order beside the pick it belongs to and refuses an answer that is not the contract', async () => {
    let model = await page()
    expect(model.rows.value.map(row => row.key)).toEqual(['all', 'design', 'concept']); expect(model.error.value).toBe(''); expect(getSimple).toHaveBeenCalledWith('me', expect.anything()); expect(getSimple).toHaveBeenCalledWith('default', expect.anything())
    state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []
    vi.mocked(getSimple).mockResolvedValue({ items: [] } as unknown as SimpleDocument)
    model = await page()
    expect(model.doc.value).toBeNull(); expect(model.failed.value).toBe(true); expect(model.error.value).toBe('Models couldn’t load.')
  })
  it('asks again once when a write lands between the two reads, and gives up in words after that', async () => {
    vi.mocked(getTail).mockResolvedValueOnce(tailBoard({ revision: 2 })).mockResolvedValue(tailBoard())
    let model = await page(); expect(model.doc.value?.revision).toBe(3); expect(getTail).toHaveBeenCalledTimes(2)
    state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []
    vi.mocked(getTail).mockResolvedValue(tailBoard({ revision: 9 }))
    model = await page(); expect(model.doc.value).toBeNull(); expect(model.error.value).toBe('Models changed while loading. Try again.')
  })
  it('writes a new order and then its level for a row without an order, carrying the person and each new revision', async () => {
    const model = await page(), concept = model.rows.value.find(row => row.key === 'concept')!, sonnet = model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!
    expect(await model.pick(concept, sonnet, 'max')).toBe(true)
    expect(putOrder).toHaveBeenCalledWith('me', 'concept', { rank: ['anthropic:sonnet', 'anthropic:opus', 'openai:sol'], not: ['unknown:x'] }, 3, simplePerson)
    expect(putEffort).toHaveBeenCalledWith('me', 'concept', 'max', 4, simplePerson)
    expect(toasts.at(-1)!.message).toBe('Saved'); expect(toasts.at(-1)!.actions.map(action => action.label)).toEqual(['Undo'])
  })
  it('does not write the level again when the order already carried it', async () => {
    vi.mocked(getTail).mockResolvedValue(tailBoard({ columns: tailBoard().columns.map(column => column.column === 'concept' ? { ...column, source: 'own' as const } : column) }))
    const model = await page(), concept = model.rows.value.find(row => row.key === 'concept')!
    await model.pick(concept, model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!)
    expect(putOrder).toHaveBeenCalledTimes(1); expect(putEffort).not.toHaveBeenCalled()
  })
  it('undoes with the inverse writes against the revisions the change ended on, and refuses once the page moved on', async () => {
    const model = await page(), concept = model.rows.value.find(row => row.key === 'concept')!
    vi.mocked(getSimple).mockImplementation(async () => simpleDoc({ revision: 5 })); vi.mocked(getTail).mockResolvedValue(tailBoard({ revision: 5 }))
    await model.pick(concept, model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!, 'max')
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(resetOrder).toHaveBeenCalledWith('me', 'concept', 5, simplePerson)
    // The same Undo a second time is a different change; once the revisions moved it must not run.
    vi.mocked(resetOrder).mockClear(); vi.mocked(getSimple).mockImplementation(async () => simpleDoc({ revision: 5 }))
    await model.pick(model.rows.value.find(row => row.key === 'concept')!, model.entries.value.find(entry => entry.line === 'anthropic:fable')!, 'max')
    vi.mocked(getSimple).mockImplementation(async () => simpleDoc({ revision: 8 })); vi.mocked(getTail).mockResolvedValue(tailBoard({ revision: 8 })); await model.load()
    const undo = toasts.at(-1)!.actions[0]!; undo.run(); await settle()
    expect(resetOrder).not.toHaveBeenCalled(); expect(toasts.at(-1)!.message).toBe('The page changed. Undo is no longer available.')
  })
  it('reports a refused write as refused: refreshes, says it was not saved, shows no success and no Undo', async () => {
    const model = await page(), concept = model.rows.value.find(row => row.key === 'concept')!
    vi.mocked(putOrder).mockRejectedValue(new APIError(409, 'stale'))
    expect(await model.pick(concept, model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!, 'high')).toBe(false)
    expect(model.error.value).toBe('Changed elsewhere. The page was refreshed; the change was not saved.'); expect(toasts).toHaveLength(0); expect(putEffort).not.toHaveBeenCalled(); expect(model.busy.value).toBe(false)
    vi.mocked(putOrder).mockRejectedValue(new APIError(403, 'no'))
    await model.pick(concept, model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!, 'high'); expect(model.error.value).toBe('You do not have permission to do this. The change was not saved.')
  })
  it('names a half-finished change when the second write fails, and an unconfirmed revision is never a save', async () => {
    const model = await page(), concept = model.rows.value.find(row => row.key === 'concept')!, sonnet = model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!
    vi.mocked(putEffort).mockRejectedValue(new APIError(422, 'invalid_effort', { error: 'invalid_effort' }))
    await model.pick(concept, sonnet, 'max'); expect(model.error.value).toMatch(/^Only part of the change was saved: /); expect(toasts).toHaveLength(0)
    vi.mocked(putEffort).mockResolvedValue({ revision: 4 }); vi.mocked(putOrder).mockResolvedValue({ revision: 3 })
    await model.pick(concept, sonnet, 'max'); expect(model.error.value).toBe('Could not confirm the save. Reload before making another change; Undo is unavailable.'); expect(toasts).toHaveLength(0)
  })
  it('drops what a held write answers after the person or scope changed', async () => {
    const model = await page(), held = deferred<{ revision: number }>(), concept = model.rows.value.find(row => row.key === 'concept')!
    vi.mocked(putOrder).mockReturnValueOnce(held.promise)
    const saving = model.pick(concept, model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!, 'high'); await settle()
    state.session.identity = { tenant: { id: 'tenant' }, principal: { id: 'other', name: 'Other', kind: 'person' } }; model.setScope('me'); state.access?.(); await settle()
    held.resolve({ revision: 4 }); await saving; await settle()
    expect(toasts).toHaveLength(0); expect(putEffort).not.toHaveBeenCalled()
  })
  it('writes for everyone without a person, moves the pin with the pick and unpins when a locked row goes', async () => {
    const model = await page(); model.setScope('default'); await settle()
    const design = model.rows.value.find(row => row.key === 'design')!, sonnet = model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!
    expect(design.editable).toBe(true)
    await model.pick(design, sonnet, 'xhigh')
    expect(putOrder).toHaveBeenCalledWith('default', 'design', { rank: ['anthropic:sonnet'], not: ['unknown:x'] }, 3, simplePerson)
    expect(putWorkspaceRules).toHaveBeenCalledWith('design', { top: [{ line: 'anthropic:sonnet', why: 'Design mocks stay on Opus.' }], bottom: [], not: {} }, 2)
    vi.mocked(putWorkspaceRules).mockClear()
    await model.clear(model.rows.value.find(row => row.key === 'design')!)
    expect(resetOrder).toHaveBeenCalledWith('default', 'design', expect.any(Number), simplePerson)
    expect(putWorkspaceRules).toHaveBeenCalledWith('design', { top: [], bottom: [], not: {} }, 2)
  })
  it('locks with a generated reason and unlocks with one rule write; a member cannot reach it', async () => {
    const model = await page(); model.setScope('default'); await settle()
    const concept = model.rows.value.find(row => row.key === 'concept')!
    await model.lock(concept, true)
    expect(putWorkspaceRules).toHaveBeenCalledWith('concept', { top: [{ line: 'anthropic:opus', why: 'Locked in Settings › Models by Markus' }], bottom: [], not: {} }, 2)
    vi.mocked(putWorkspaceRules).mockClear(); toasts.length = 0
    state.manage = false; state.access?.(); await settle()
    expect(model.scope.value).toBe('me'); expect(await model.lock(model.rows.value.find(row => row.key === 'concept')!, true)).toBe(false); expect(putWorkspaceRules).not.toHaveBeenCalled()
    model.setScope('default'); expect(model.scope.value).toBe('me')
  })
  it('stops a lock when the rules moved since the page was read', async () => {
    const model = await page(); model.setScope('default'); await settle()
    vi.mocked(getWorkspaceRules).mockResolvedValue(rulesDoc(7))
    expect(await model.lock(model.rows.value.find(row => row.key === 'concept')!, true)).toBe(false)
    expect(putWorkspaceRules).not.toHaveBeenCalled(); expect(model.error.value).toBe('Changed elsewhere. The page was refreshed; the change was not saved.')
  })
  it('a draft row writes nothing until it is picked, and picking the default for everyone stores nothing at all', async () => {
    const model = await page(); model.startException('backend'); expect(model.rows.value.at(-1)).toMatchObject({ key: 'backend', draft: true }); expect(putOrder).not.toHaveBeenCalled()
    model.discardException(); expect(model.rows.value.map(row => row.key)).not.toContain('backend')
    model.setScope('default'); await settle(); model.startException('backend')
    const draft = model.rows.value.at(-1)!
    expect(await model.pick(draft, model.entries.value.find(entry => entry.line === 'openai:sol')!, 'xhigh')).toBe(true)
    expect(putOrder).not.toHaveBeenCalled(); expect(model.draft.value).toBeNull()
  })
  it('hides “not now” in an undoable single write and restores the earlier list on Undo', async () => {
    const model = await page()
    await model.dismiss('xai:grok-preview')
    expect(dismissLine).toHaveBeenCalledWith('me', 'xai:grok-preview', 3, simplePerson)
    vi.mocked(getSimple).mockImplementation(async () => simpleDoc({ revision: 4 })); vi.mocked(getTail).mockResolvedValue(tailBoard({ revision: 4 })); await model.load()
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(putDismissed).toHaveBeenCalledWith('me', ['xai:old'], 4, simplePerson)
    expect(NEW_LINE.id).toBe('xai:grok-preview')
  })
})

describe('the wire', () => {
  it('sends every personal write with the person it was read for and no header for everyone', async () => {
    const wire = await vi.importActual<typeof import('../src/lib/modelsSimpleApi')>('../src/lib/modelsSimpleApi')
    vi.mocked(api).mockImplementation(async () => new Response('{"revision":1}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await wire.putOrder('me', 'design', { rank: ['a'], not: [] }, 3, simplePerson); await wire.resetOrder('me', 'design', 4, simplePerson); await wire.putEffort('me', 'design', 'max', 5, simplePerson)
    await wire.dismissLine('me', 'xai:x', 6, simplePerson); await wire.putDismissed('me', [], 7, simplePerson); await wire.putOrder('default', 'design', { rank: ['a'], not: [] }, 3, simplePerson)
    const calls = vi.mocked(api).mock.calls
    expect(calls.map(([path, init]) => [new Headers(init!.headers).get('If-Prefs-Person'), init!.method, path])).toEqual([
      [simplePerson, 'PUT', '/model-preferences/orders/design/first?for=me'], [simplePerson, 'DELETE', '/model-preferences/orders/design/first?for=me&revision=4'],
      [simplePerson, 'PUT', '/model-preferences/orders/design/first/thinking?for=me'], [simplePerson, 'POST', '/model-preferences/tray/xai%3Ax/dismiss?for=me'],
      [simplePerson, 'PUT', '/model-preferences/profile?for=me'], [null, 'PUT', '/model-preferences/orders/design/first?for=default'],
    ])
    expect(JSON.parse(calls[0]![1]!.body as string)).toEqual({ rank: ['a'], not: [], revision: 3 }); expect(JSON.parse(calls[2]![1]!.body as string)).toEqual({ effort: 'max', revision: 5 })
  })
  it('turns a refusal into the status and the server’s words, and an answer that is not a list into an error', async () => {
    const wire = await vi.importActual<typeof import('../src/lib/modelsSimpleApi')>('../src/lib/modelsSimpleApi')
    vi.mocked(api).mockResolvedValueOnce(new Response('{"error":"stale_revision"}', { status: 409, headers: { 'Content-Type': 'application/json' } }))
    await expect(wire.putOrder('me', 'design', { rank: [], not: [] }, 1, simplePerson)).rejects.toMatchObject({ status: 409, message: 'stale_revision' })
    vi.mocked(api).mockResolvedValueOnce(new Response('{"items":[]}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await expect(wire.getRegistry()).rejects.toThrow('Invalid model catalog')
  })
})
