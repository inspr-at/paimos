// SPDX-License-Identifier: AGPL-3.0-only
// Settings › Models, minimal (AEON-1011). Risk: a pick, a lock or a reset is written against the wrong person,
// scope or revision, or reports success it did not have. Writes are bound to what the page showed; Undo only runs
// against the state it undid; every refusal is said out loud.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, api } from '../src/lib/api'
import {
  buildEntries, buildRows, cantSentence, groupByProvider, movePin, nearestEffort, nextView, orderWithFirst, pinFirst, reasonText, rulesBody, ticketWhy, unpin, untilText,
  type RegistryProfile, type SimpleDocument, type SimpleUnavailable, type TailBoard,
} from '../src/lib/modelsSimple'
import { getMembers } from '../src/lib/access'
import { dismissLine, getRegistry, getSimple, getTail, getTicketResolution, getWorkspaceRules, putDismissed, putEffort, putOrder, putSituationOrder, putWorkspaceRules, resetOrder, resetSituationOrder } from '../src/lib/modelsSimpleApi'
import { useModelsSimple } from '../src/lib/useModelsSimple'
import { resetToasts, toasts } from '../src/lib/toast'
import { KINDS, MANY, NEW_LINE, OPUS_CURSOR, registry, simplePerson } from './models-simple-fixtures'

const state = vi.hoisted(() => ({
  session: { identity: { tenant: { id: 'tenant' }, principal: { id: 'person', name: 'Markus', kind: 'person' } } as { tenant: { id: string }; principal: { id: string; name: string; kind: string } } | null, authenticationCurrent: () => true },
  cleanups: [] as (() => void)[], access: undefined as (() => void) | undefined, manage: true, members: true,
}))
vi.mock('../src/stores/session', () => ({ useSession: () => state.session }))
vi.mock('../src/lib/authz', () => ({ can: (permission: string) => permission === 'model_prefs.manage' ? state.manage : permission === 'members.read' ? state.members : true, onAccessRevalidate: (actions: { reset: () => void; refresh: () => void }) => {
  const owner = state.session.identity && `${state.session.identity.tenant.id}/${state.session.identity.principal.id}`
  state.access = () => {
    const current = state.session.identity && `${state.session.identity.tenant.id}/${state.session.identity.principal.id}`
    if (current !== owner) actions.reset(); else actions.refresh()
  }
  return () => {}
} }))
vi.mock('vue', async original => ({ ...await original<typeof import('vue')>(), onBeforeUnmount: (cleanup: () => void) => state.cleanups.push(cleanup) }))
vi.mock('../src/lib/api', async original => ({ ...await original<typeof import('../src/lib/api')>(), api: vi.fn() }))
vi.mock('../src/lib/access', async original => ({ ...await original<typeof import('../src/lib/access')>(), getMembers: vi.fn() }))
vi.mock('../src/lib/modelsSimpleApi', async original => ({ ...await original<typeof import('../src/lib/modelsSimpleApi')>(), getSimple: vi.fn(), getTail: vi.fn(), getRegistry: vi.fn(), getTicketResolution: vi.fn(), getWorkspaceRules: vi.fn(), putOrder: vi.fn(), resetOrder: vi.fn(), putEffort: vi.fn(), putWorkspaceRules: vi.fn(), putSituationOrder: vi.fn(), resetSituationOrder: vi.fn(), dismissLine: vi.fn(), putDismissed: vi.fn() }))

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
  const column = (id: string, source: 'own' | 'default' | 'template', list: string[], locked?: string) => ({ column: id, label: label.get(id) ?? id, fixed: id === 'other', hidden: false, source, list: list.map(line => ({ line, ...(line === locked ? { lock: { kind: 'rule' } } : {}) })), not: [{ line: 'unknown:x' }], cant: [], stored: { rank: [...list], not: ['unknown:x'] }, ...(id === 'design' ? { stored_effort: 'xhigh' } : {}) })
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
    expect(reasonText('column: allowance; default: allowance; role: daily_limit; daily_limit')).toBe('the account has no room left until its limit resets; the account is at its daily cap')
    expect(reasonText('Claude account is at its daily cap until 04:00; role: Claude account is at its daily cap until 04:00')).toBe('claude account is at its daily cap until 04:00')
  })
})

describe('orders and locks', () => {
  it('moves one line to the front of the stored order and keeps everything else, never writing a rule', () => {
    const column = tailBoard().columns.find(item => item.column === 'design')!
    expect(orderWithFirst(column, 'anthropic:sonnet')).toEqual({ rank: ['anthropic:sonnet', 'anthropic:opus'], not: ['unknown:x'] })
    const other = tailBoard().columns.find(item => item.column === 'other')!
    expect(orderWithFirst(other, 'anthropic:sonnet')).toEqual({ rank: ['anthropic:sonnet', 'openai:sol', 'anthropic:opus'], not: ['unknown:x'] })
  })
  it('keeps a locked line and a fallback the resolved list omitted', () => {
    const column = tailBoard().columns.find(item => item.column === 'design')!
    const hidden = { ...column, list: column.list.filter(card => card.line !== 'xai:grok'), stored: { rank: ['anthropic:opus', 'xai:grok', 'anthropic:sonnet'], not: ['unknown:x', 'openai:luna'] } }
    expect(orderWithFirst(hidden, 'anthropic:sonnet')).toEqual({ rank: ['anthropic:sonnet', 'anthropic:opus', 'xai:grok'], not: ['unknown:x', 'openai:luna'] })
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
  it('keeps a queued ticket that nothing can run, and the skip trace behind Why', () => {
    const blocked = nextView({ next: { ticket: 'AEON-1011', column: 'backend', line: null, trace: [{ role: 'build', line: 'anthropic:opus', stage: 'column', reason: 'no qualified account', selected: false }] }, rows, entries, labels })
    expect(blocked.parts.map(part => part.text).join('')).toBe('AEON-1011 is queued, but nothing available can run it.')
    expect(blocked.why!.title).toBe('Why is AEON-1011 waiting?')
    expect(blocked.why!.steps.map(step => step.map(part => part.text).join(''))).toEqual(['Skipped Opus 5.5: no qualified account.'])
  })
})

// Models v2 W1 (AEON-1160). Risk: the picker hides a model's other routes or offers an older version as if a pick could
// run it, or a row explains a pick it can't run with codes, repeats or "allowance" instead of one sentence.
describe('the picker: provider → line, newest first, older versions folded', () => {
  const many = buildEntries(registry([OPUS_CURSOR, ...MANY]))
  it('groups lines under their provider, one entry per line with every route, and older versions under the provider', () => {
    const groups = groupByProvider(many)
    expect(groups.map(group => group.label)).toEqual(['OpenAI', 'Anthropic', 'xAI', 'Google', 'Cursor', 'Unknown provider'])
    const of = (label: string) => groups.find(group => group.label === label)!
    expect(of('OpenAI').entries.map(entry => entry.name)).toEqual(['GPT-6.1 Sol', 'GPT-6.1 Astra', 'GPT-6.1 Luna', 'GPT-6.1 Terra'])
    expect(of('OpenAI').older.map(entry => [entry.name, entry.replacedBy])).toEqual([['GPT-6 Sol', 'GPT-6.1 Sol'], ['GPT-6 Astra', 'GPT-6.1 Astra'], ['GPT-6 Luna', 'GPT-6.1 Luna'], ['GPT-6 Terra', 'GPT-6.1 Terra']])
    expect(of('Anthropic').entries.map(entry => entry.name)).toEqual(['Opus 5.5', 'Sonnet 5.5', 'Fable 5.1', 'Haiku 4.5'])
    expect(of('Anthropic').older.map(entry => entry.name)).toEqual(['Opus 4.8', 'Sonnet 5', 'Fable 5'])
    expect(of('xAI').older.map(entry => entry.name)).toEqual(['Grok 4.6', 'Grok 4.5'])
    // About 35 routes stay calm: one entry per line, and a line's older versions never become entries of their own.
    expect(many.length).toBeLessThanOrEqual(20); expect(new Set(many.map(entry => entry.line)).size).toBe(many.length)
    // Opus through Claude Code and Cursor is one model with two routes, each with its own native levels; the first route leads.
    const opus = many.find(entry => entry.line === 'anthropic:opus')!
    expect(opus.routes.map(route => [route.harness, route.efforts.map(effort => effort.name)])).toEqual([['claude', ['high', 'xhigh', 'max']], ['cursor', ['high', 'max']]])
    expect(opus.efforts.map(effort => effort.name)).toEqual(['high', 'xhigh', 'max'])
    expect(many.find(entry => entry.line === 'xai:grok')!.routes.map(route => route.harness)).toEqual(['grok', 'cursor'])
    expect(opus.older[0]).toMatchObject({ key: 'anthropic:opus@4.8', line: 'anthropic:opus', replacedBy: 'Opus 5.5' })
  })
  it('filters lines and older versions alike, keeping the provider of a match', () => {
    const groups = groupByProvider(many, entry => entry.name.includes('Opus'))
    expect(groups.map(group => [group.label, group.entries.map(entry => entry.name), group.older.map(entry => entry.name)])).toEqual([['Anthropic', ['Opus 5.5'], ['Opus 4.8']]])
    expect(groupByProvider(many, entry => entry.name === 'Grok 4.5').map(group => [group.entries.length, group.older.map(entry => entry.name)])).toEqual([[0, ['Grok 4.5']]])
  })
})

describe('the can’t-run sentence', () => {
  const entries = buildEntries(registry([OPUS_CURSOR])), now = Date.parse('2026-10-11T22:00:00Z')
  const at = '2026-10-12T04:00:00Z', time = new Date(at).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  const item = (extra: Partial<SimpleUnavailable>): SimpleUnavailable => ({ column: 'design', line: 'anthropic:opus', runs_instead: 'openai:sol', effort: 'high', reason: 'raw server words', ticket: null, cause: 'daily_cap', until: at, route: 'claude', waits: false, runs_via: 'codex', ...extra })
  const said = (extra: Partial<SimpleUnavailable>) => { const sentence = cantSentence(item(extra), entries, now)!; return `${sentence.cause} → ${sentence.then}.` }
  it('says one cause and what happens instead, in words', () => {
    expect(said({ runs_instead: 'anthropic:opus', effort: 'max', runs_via: 'cursor' })).toBe(`Claude account is at its daily cap until ${time} → Opus 5.5 runs through Cursor instead.`)
    expect(said({})).toBe(`Claude account is at its daily cap until ${time} → GPT-6.1 Sol · high runs instead.`)
    expect(said({ line: 'anthropic:fable', runs_instead: null, effort: null, waits: true, runs_via: null })).toBe(`Claude account is at its daily cap until ${time} → work waits until then.`)
    expect(said({ cause: 'paused', until: '2026-10-14T12:00:00Z' })).toBe('Opus 5.5 is paused until 14 Oct → GPT-6.1 Sol · high runs instead.')
    expect(said({ cause: 'paused', until: null })).toBe('Opus 5.5 is paused → GPT-6.1 Sol · high runs instead.')
    expect(said({ line: 'xai:grok', cause: 'no_tools', until: null, route: 'grok' })).toBe('Grok 4.7 has no tools in PAIMOS → GPT-6.1 Sol · high runs instead.')
    expect(said({ cause: 'no_account', until: null })).toBe('No Claude account can run Opus 5.5 right now → GPT-6.1 Sol · high runs instead.')
    expect(said({ cause: 'retired', until: null, route: null })).toBe('Opus 5.5 has been retired → GPT-6.1 Sol · high runs instead.')
    expect(said({ cause: 'residency', until: null, runs_instead: null, waits: true, runs_via: null })).toBe('Data residency rules out Opus 5.5 → work waits.')
    // A reset that has passed is not promised again; an entry without a cause reads as the server's own fallback, one sentence, never its joined reasons (AEON-1240).
    expect(said({ until: '2026-10-11T21:00:00Z' })).toBe('Claude account is at its daily cap → GPT-6.1 Sol · high runs instead.')
    expect(said({ cause: undefined, until: null, reason: 'column: allowance; default: daily_limit' })).toBe('No Claude account can run Opus 5.5 right now → GPT-6.1 Sol · high runs instead.')
    expect(cantSentence(null, entries, now)).toBeNull()
  })
  it('never says a code, "allowance", a list or the same word twice, and names a reset time once', () => {
    const causes = ['daily_cap', 'paused', 'no_tools', 'no_account', 'retired', 'residency'] as const
    for (const cause of causes) for (const extra of [{}, { waits: true, runs_instead: null }, { runs_instead: 'anthropic:opus', runs_via: 'cursor' }]) {
      const text = said({ cause, reason: 'allowance; daily_limit', ...extra })
      expect(text, cause).not.toMatch(/allowance|;|_|\b(\w+) \1\b/)
      expect((text.match(new RegExp(time, 'g')) ?? []).length, cause).toBeLessThanOrEqual(1)
    }
  })
  it('names a reset within a day by its time and a later one by its date', () => {
    expect(untilText(at, now)).toBe(time)
    expect(untilText('2026-10-14T12:00:00Z', now)).toBe('14 Oct')
    expect(untilText('2027-01-14T12:00:00Z', now)).toBe('14 Jan 2027')
    expect(untilText(null, now)).toBe(''); expect(untilText('not a date', now)).toBe('')
  })
  it('lets the next line and its Why say the route, never “Opus instead of Opus”', () => {
    const doc = simpleDoc({ exceptions: [{ ...simpleDoc().exceptions[0]! }], unavailable: [item({ runs_instead: 'anthropic:opus', effort: 'max', runs_via: 'cursor' })] })
    const rows = buildRows({ doc, workspace: null, scope: 'me', canEdit: true, entries, labels: label })
    const next = nextView({ now, rows, entries, labels: label, next: { ticket: 'AEON-1011', column: 'design', line: 'anthropic:opus', effort: 'max', reviewer: { line: 'openai:sol', effort: 'xhigh' }, trace: [{ line: 'anthropic:opus', stage: 'column', role: 'build', reason: 'Claude account is at its daily cap', selected: false }, { line: 'anthropic:opus', stage: 'column', role: 'build', reason: '', selected: true }] } })
    expect(next.parts.map(part => part.text).join('')).toBe('The next UI design runs on Opus 5.5 · max through Cursor, reviewed by GPT-6.1 Sol.')
    expect(next.why!.steps.map(step => step.map(part => part.text).join(''))).toEqual([
      'UI design has its own model: Opus 5.5 · xhigh.', `Skipped Opus 5.5 through Claude Code: Claude account is at its daily cap until ${time}.`, 'It takes its next route, Cursor: Opus 5.5 · max.', 'Reviews always use another family: GPT-6.1 Sol.',
    ])
  })
})

describe('Why? for the ticket a person clicked', () => {
  const profiles = registry([OPUS_CURSOR]), entries = buildEntries(profiles)
  const profile = (model: string, effort: string) => profiles.find(item => item.model === model && item.effort === effort)!
  it('explains that ticket’s own walk: what it skipped, and where it runs', () => {
    const why = ticketWhy({ ticket: 'AEON-7', profiles, entries, resolution: { owner_required: false, profile: profile('claude-opus-5.5', 'max'), ladder: [
      { profile_id: profile('claude-sonnet-5-5', 'high').id, selected: false, skip_reasons: ['daily_limit'] }, { profile_id: profile('claude-opus-5-5', 'xhigh').id, selected: false, skip_reasons: ['column: allowance'] },
      { profile_id: 'gone', selected: false, skip_reasons: ['retired'] }, { profile_id: profile('claude-opus-5.5', 'max').id, selected: true, skip_reasons: [] }] } })
    expect(why.title).toBe('Why Opus 5.5 for AEON-7?')
    expect(why.steps.map(step => step.map(part => part.text).join(''))).toEqual([
      'Skipped Sonnet 5.5: the account is at its daily cap.', 'Skipped Opus 5.5 through Claude Code: the account has no room left until its limit resets.',
      'Skipped A model no longer in the registry: the model has been retired.', 'AEON-7 runs on Opus 5.5 · max through Cursor.',
    ])
  })
  it('says the ticket waits when nothing runs it, and cuts a long walk honestly', () => {
    const skips = Array.from({ length: 11 }, () => ({ profile_id: profile('claude-sonnet-5-5', 'high').id, selected: false, skip_reasons: ['daily_limit'] }))
    const why = ticketWhy({ ticket: 'AEON-7', profiles, entries, resolution: { owner_required: true, profile: null, ladder: skips } })
    expect(why.title).toBe('Why is AEON-7 waiting?')
    expect(why.steps).toHaveLength(10)
    expect(why.steps.slice(-2).map(step => step.map(part => part.text).join(''))).toEqual(['3 more skipped for the same kinds of reasons.', 'Nothing in the order of AEON-7 can run it right now, so it waits.'])
  })
})

/** A stand-in for the server's effort rule: an order with no stored effort runs Default's, a stored one wins; JSON null is the only clear. */
function inheritanceServer(stored: string | null) {
  const server = { revision: 3, defaultEffort: 'xhigh', concept: { rank: ['anthropic:opus', 'openai:sol'], not: ['unknown:x'], effort: stored } as { rank: string[]; not: string[]; effort: string | null } | null }
  const guard = (revision: number) => { if (revision !== server.revision) throw new APIError(409, 'stale_revision') }
  const write = () => ({ revision: ++server.revision, person_id: simplePerson })
  vi.mocked(getSimple).mockImplementation(async () => {
    const base = simpleDoc()
    return { ...base, revision: server.revision, all: { ...base.all, effort: server.defaultEffort }, exceptions: base.exceptions.flatMap(row => row.column !== 'concept' ? [row] : server.concept ? [{ ...row, line: server.concept.rank[0]!, effort: server.concept.effort ?? server.defaultEffort }] : []) }
  })
  vi.mocked(getTail).mockImplementation(async () => {
    const base = tailBoard()
    return { ...base, revision: server.revision, columns: base.columns.map(column => column.column !== 'concept' ? column : server.concept ? { ...column, source: 'own' as const, stored: { rank: [...server.concept.rank], not: [...server.concept.not] }, stored_effort: server.concept.effort } : column) }
  })
  vi.mocked(putOrder).mockImplementation(async (_scope, _column, body, revision) => { guard(revision); server.concept = { rank: body.rank, not: body.not, effort: server.concept?.effort ?? null }; return write() })
  vi.mocked(putEffort).mockImplementation(async (_scope, _column, effort, revision) => { guard(revision); if (!server.concept) throw new APIError(404, 'no_order'); server.concept.effort = effort; return write() })
  vi.mocked(resetOrder).mockImplementation(async (_scope, _column, revision) => { guard(revision); server.concept = null; return write() })
  return server
}

describe('the card’s writes', () => {
  beforeEach(() => {
    state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []; vi.resetAllMocks(); resetToasts()
    state.session.identity = { tenant: { id: 'tenant' }, principal: { id: 'person', name: 'Markus', kind: 'person' } }; state.manage = true; state.members = true
    vi.mocked(getMembers).mockResolvedValue({ people: [{ principal_id: simplePerson, name: 'Ada Admin' }] } as unknown as Awaited<ReturnType<typeof getMembers>>)
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
  it('names who set a lock only for people who may read the member list', async () => {
    let model = await page(); expect(model.names.value.get(simplePerson)).toBe('Ada Admin'); expect(getMembers).toHaveBeenCalledTimes(1)
    state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []; vi.mocked(getMembers).mockClear(); state.members = false
    model = await page(); expect(model.names.value.size).toBe(0); expect(getMembers).not.toHaveBeenCalled()
    state.members = true; vi.mocked(getMembers).mockRejectedValue(new Error('down'))
    state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []
    model = await page(); expect(model.rows.value.map(row => row.key)).toEqual(['all', 'design', 'concept']); expect(model.names.value.size).toBe(0)
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
  it('does not write the level again when the stored native effort is already the one selected', async () => {
    vi.mocked(getTail).mockResolvedValue(tailBoard({ columns: tailBoard().columns.map(column => column.column === 'concept' ? { ...column, source: 'own' as const, stored_effort: 'high' } : column) }))
    const model = await page(), concept = model.rows.value.find(row => row.key === 'concept')!
    await model.pick(concept, model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!)
    expect(putOrder).toHaveBeenCalledTimes(1); expect(putEffort).not.toHaveBeenCalled()
  })
  it('writes the selected level when an existing order has no stored native effort', async () => {
    vi.mocked(getTail).mockResolvedValue(tailBoard({ columns: tailBoard().columns.map(column => column.column === 'concept' ? { ...column, source: 'own' as const, stored_effort: null } : column) }))
    const model = await page(), concept = model.rows.value.find(row => row.key === 'concept')!, sonnet = model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!
    expect(await model.pick(concept, sonnet)).toBe(true)
    expect(putEffort).toHaveBeenCalledWith('me', 'concept', 'high', 4, simplePerson)
  })
  it('writes a stored tail the resolved list hid, including its locked line', async () => {
    const columns = tailBoard().columns.map(column => column.column === 'concept' ? { ...column, source: 'own' as const, list: [{ line: 'anthropic:opus' }], stored: { rank: ['anthropic:opus', 'xai:grok', 'openai:sol'], not: ['unknown:x', 'openai:luna'] } } : column)
    vi.mocked(getTail).mockResolvedValue(tailBoard({ columns }))
    const model = await page(), concept = model.rows.value.find(row => row.key === 'concept')!
    await model.pick(concept, model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!, 'max')
    expect(putOrder).toHaveBeenCalledWith('me', 'concept', { rank: ['anthropic:sonnet', 'anthropic:opus', 'xai:grok', 'openai:sol'], not: ['unknown:x', 'openai:luna'] }, 3, simplePerson)
  })
  it('restores a saved workspace order on Undo, including its rank, exclusions and effort', async () => {
    // The board reports a saved For everyone order as "own". A fixture that says "default" hides a DELETE.
    const saved = { rank: ['anthropic:opus', 'xai:grok', 'openai:sol'], not: ['unknown:x', 'openai:luna'] }
    const columns = tailBoard().columns.map(column => column.column === 'concept' ? { ...column, source: 'own' as const, stored: saved, stored_effort: 'high' } : column)
    vi.mocked(getTail).mockResolvedValue(tailBoard({ columns }))
    const model = await page(); model.setScope('default'); await settle()
    vi.mocked(getSimple).mockImplementation(async () => simpleDoc({ revision: 5 })); vi.mocked(getTail).mockResolvedValue(tailBoard({ revision: 5, columns }))
    const concept = model.rows.value.find(row => row.key === 'concept')!
    expect(await model.pick(concept, model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!, 'max')).toBe(true)
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(resetOrder).not.toHaveBeenCalled()
    expect(putOrder).toHaveBeenLastCalledWith('default', 'concept', saved, 5, simplePerson)
    expect(putEffort).toHaveBeenCalledWith('default', 'concept', 'high', 6, simplePerson)
  })
  for (const stored of [null, 'high']) it(`Undo of a pick puts back the stored effort (${stored ?? 'none'}), so a later Default effort change ${stored ? 'leaves the row alone' : 'still reaches the row'}`, async () => {
    const server = inheritanceServer(stored), model = await page()
    expect(model.rows.value.find(row => row.key === 'concept')!.effort).toBe(stored ?? 'xhigh')
    expect(await model.pick(model.rows.value.find(row => row.key === 'concept')!, model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!, 'max')).toBe(true)
    expect(server.concept).toMatchObject({ rank: ['anthropic:sonnet', 'anthropic:opus', 'openai:sol'], effort: 'max' })
    toasts.at(-1)!.actions[0]!.run(); await settle()
    // The effort on screen was Default's xhigh; writing it back would turn inheritance into an override.
    expect(server.concept).toMatchObject({ rank: ['anthropic:opus', 'openai:sol'], not: ['unknown:x'], effort: stored })
    expect(putEffort).toHaveBeenLastCalledWith('me', 'concept', stored, 6, simplePerson)
    server.defaultEffort = 'max'; await model.load()
    expect(model.rows.value.find(row => row.key === 'concept')!.effort).toBe(stored ?? 'max')
  })
  it('Undo of a reset does not turn an inherited effort into a stored one', async () => {
    const server = inheritanceServer(null), model = await page()
    expect(await model.clear(model.rows.value.find(row => row.key === 'concept')!)).toBe(true)
    expect(server.concept).toBeNull()
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(server.concept).toMatchObject({ rank: ['anthropic:opus', 'openai:sol'], not: ['unknown:x'], effort: null })
    expect(putEffort).not.toHaveBeenCalled()
    server.defaultEffort = 'high'; await model.load()
    expect(model.rows.value.find(row => row.key === 'concept')!.effort).toBe('high')
  })
  it('Undo of a reset brings a stored effort back', async () => {
    const server = inheritanceServer('high'), model = await page()
    expect(await model.clear(model.rows.value.find(row => row.key === 'concept')!)).toBe(true)
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(server.concept).toMatchObject({ rank: ['anthropic:opus', 'openai:sol'], effort: 'high' })
    expect(putEffort).toHaveBeenCalledWith('me', 'concept', 'high', 5, simplePerson)
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
  it('explains the clicked ticket for the person on screen and drops the answer after they change', async () => {
    const profiles = registry([]), runs = profiles.find(item => item.model === 'claude-opus-5-5' && item.effort === 'xhigh')!
    vi.mocked(getRegistry).mockResolvedValue(profiles)
    let model = await page()
    vi.mocked(getTicketResolution).mockResolvedValueOnce({ owner_required: false, profile: runs, ladder: [] })
    expect((await model.explain('AEON-7'))?.title).toBe('Why Opus 5.5 for AEON-7?'); expect(getTicketResolution).toHaveBeenCalledWith('AEON-7')
    const held = deferred<Awaited<ReturnType<typeof getTicketResolution>>>(); vi.mocked(getTicketResolution).mockReturnValueOnce(held.promise)
    const asking = model.explain('AEON-8'); await settle()
    state.session.identity = { tenant: { id: 'tenant' }, principal: { id: 'other', name: 'Other', kind: 'person' } }; state.access?.(); await settle()
    held.resolve({ owner_required: false, profile: runs, ladder: [] })
    expect(await asking).toBeNull(); expect(model.error.value).not.toContain('AEON-8')
    state.cleanups.forEach(cleanup => cleanup()); state.cleanups = []
    state.session.identity = { tenant: { id: 'tenant' }, principal: { id: 'person', name: 'Markus', kind: 'person' } }
    model = await page(); vi.mocked(getTicketResolution).mockRejectedValueOnce(new APIError(404, 'not_found'))
    expect(await model.explain('AEON-9')).toBeNull(); expect(model.error.value).toBe('Couldn’t load why AEON-9 runs on its model. Open it again from the ticket.')
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
    expect(putOrder).toHaveBeenCalledWith('default', 'design', { rank: ['anthropic:sonnet', 'anthropic:opus'], not: ['unknown:x'] }, 3, simplePerson)
    expect(putWorkspaceRules).toHaveBeenCalledWith('design', { top: [{ line: 'anthropic:sonnet', why: 'Design mocks stay on Opus.' }], bottom: [], not: {} }, 2)
    expect(vi.mocked(putWorkspaceRules).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(putOrder).mock.invocationCallOrder[0]!)
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
  it('moves a locked pin before the replacement effort, so the old model is not what rejects it', async () => {
    const model = await page(); model.setScope('default'); await settle()
    const calls: string[] = []
    vi.mocked(putWorkspaceRules).mockImplementation(async (_column, _body, revision) => { calls.push('rules'); return { ...rulesDoc(), revision: revision + 1 } })
    vi.mocked(putOrder).mockImplementation(async (_scope, _column, _body, revision) => { calls.push('order'); return { revision: revision + 1, person_id: simplePerson } })
    vi.mocked(putEffort).mockImplementation(async (_scope, _column, _effort, revision) => {
      if (!calls.includes('rules')) throw new APIError(422, 'effort_not_registered', { error: 'effort_not_registered' })
      calls.push('effort'); return { revision: revision + 1, person_id: simplePerson }
    })
    const design = model.rows.value.find(row => row.key === 'design')!, sonnet = model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!
    expect(await model.pick(design, sonnet, 'max')).toBe(true)
    expect(calls).toEqual(['rules', 'order', 'effort'])
  })
  it('drops a workspace lock, a locked pick and a locked removal when Just me is selected before the rules return', async () => {
    const model = await page(); model.setScope('default'); await settle()
    const held = deferred<ReturnType<typeof rulesDoc>>()
    vi.mocked(getWorkspaceRules).mockReturnValueOnce(held.promise)
    const locking = model.lock(model.rows.value.find(row => row.key === 'concept')!, true); await settle()
    model.setScope('me'); await settle(); held.resolve(rulesDoc())
    expect(await locking).toBe(false); expect(putWorkspaceRules).not.toHaveBeenCalled(); expect(model.scope.value).toBe('me')
    model.setScope('default'); await settle()
    const pickHeld = deferred<ReturnType<typeof rulesDoc>>()
    vi.mocked(getWorkspaceRules).mockReturnValueOnce(pickHeld.promise)
    const saving = model.pick(model.rows.value.find(row => row.key === 'design')!, model.entries.value.find(entry => entry.line === 'anthropic:sonnet')!, 'max'); await settle()
    model.setScope('me'); await settle(); pickHeld.resolve(rulesDoc()); expect(await saving).toBe(false)
    expect(putOrder).not.toHaveBeenCalled(); expect(putWorkspaceRules).not.toHaveBeenCalled()
    model.setScope('default'); await settle()
    const clearHeld = deferred<ReturnType<typeof rulesDoc>>()
    vi.mocked(getWorkspaceRules).mockReturnValueOnce(clearHeld.promise)
    const removing = model.clear(model.rows.value.find(row => row.key === 'design')!); await settle()
    model.setScope('me'); await settle(); clearHeld.resolve(rulesDoc()); expect(await removing).toBe(false)
    expect(resetOrder).not.toHaveBeenCalled(); expect(putWorkspaceRules).not.toHaveBeenCalled()
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
  it('Undo of “Same as the row” writes the whole situation order back, its saved exclusions included', async () => {
    // A workspace server: DELETE removes the stored order, exclusions and all; only the Undo body can bring them back.
    const fix = { line: 'anthropic:sonnet', effort: 'xhigh', chain: [{ line: 'openai:sol', effort: 'high' }], end: 'wait' as const, not: ['xai:grok'] }
    const server = { revision: 5, situations: { design: { fix } } as NonNullable<SimpleDocument['situations']> }
    vi.mocked(getSimple).mockImplementation(async scope => scope === 'me' ? simpleDoc() : simpleDoc({ revision: server.revision, situations: server.situations }))
    vi.mocked(getTail).mockImplementation(async scope => tailBoard({ revision: scope === 'me' ? 3 : server.revision }))
    vi.mocked(resetSituationOrder).mockImplementation(async (column, key, revision) => { delete server.situations[column]![key]; server.revision = revision + 1; return { revision: server.revision } })
    vi.mocked(putSituationOrder).mockImplementation(async (column, key, body, revision) => {
      server.situations[column] = { ...server.situations[column], [key]: { line: body.rank[0]!, effort: body.efforts[body.rank[0]!] ?? null, chain: body.rank.slice(1).map(line => ({ line, effort: body.efforts[line] ?? null })), end: body.end ?? 'row', not: body.not ?? [] } }
      server.revision = revision + 1; return { revision: server.revision }
    })
    const model = await page(); model.setScope('default'); await settle()
    expect(await model.situation('design', 'fix', null, 5)).toBe(true)
    expect(resetSituationOrder).toHaveBeenCalledWith('design', 'fix', 5); expect(server.situations.design!.fix).toBeUndefined()
    toasts.at(-1)!.actions[0]!.run(); await settle()
    expect(putSituationOrder).toHaveBeenCalledWith('design', 'fix', { rank: ['anthropic:sonnet', 'openai:sol'], efforts: { 'anthropic:sonnet': 'xhigh', 'openai:sol': 'high' }, end: 'wait', not: ['xai:grok'] }, 6)
    expect(server.situations.design!.fix).toEqual(fix); expect(model.doc.value?.situations?.design?.fix).toEqual(fix)
  })
})

describe('the wire', () => {
  it('sends every personal write with the person it was read for and no header for everyone', async () => {
    const wire = await vi.importActual<typeof import('../src/lib/modelsSimpleApi')>('../src/lib/modelsSimpleApi')
    vi.mocked(api).mockImplementation(async () => new Response('{"revision":1}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await wire.putOrder('me', 'design', { rank: ['a'], not: [] }, 3, simplePerson); await wire.resetOrder('me', 'design', 4, simplePerson); await wire.putEffort('me', 'design', 'max', 5, simplePerson)
    await wire.dismissLine('me', 'xai:x', 6, simplePerson); await wire.putDismissed('me', [], 7, simplePerson); await wire.putOrder('default', 'design', { rank: ['a'], not: [] }, 3, simplePerson)
    await wire.putEffort('me', 'design', null, 8, simplePerson)
    const calls = vi.mocked(api).mock.calls
    expect(calls.map(([path, init]) => [new Headers(init!.headers).get('If-Prefs-Person'), init!.method, path])).toEqual([
      [simplePerson, 'PUT', '/model-preferences/orders/design/first?for=me'], [simplePerson, 'DELETE', '/model-preferences/orders/design/first?for=me&revision=4'],
      [simplePerson, 'PUT', '/model-preferences/orders/design/first/thinking?for=me'], [simplePerson, 'POST', '/model-preferences/tray/xai%3Ax/dismiss?for=me'],
      [simplePerson, 'PUT', '/model-preferences/profile?for=me'], [null, 'PUT', '/model-preferences/orders/design/first?for=default'], [simplePerson, 'PUT', '/model-preferences/orders/design/first/thinking?for=me'],
    ])
    expect(JSON.parse(calls[0]![1]!.body as string)).toEqual({ rank: ['a'], not: [], revision: 3 }); expect(JSON.parse(calls[2]![1]!.body as string)).toEqual({ effort: 'max', revision: 5 })
    // JSON null is the only way to clear a stored effort; an omitted key keeps it.
    expect(calls[6]![1]!.body).toBe('{"effort":null,"revision":8}')
  })
  it('turns a refusal into the status and the server’s words, and an answer that is not a list into an error', async () => {
    const wire = await vi.importActual<typeof import('../src/lib/modelsSimpleApi')>('../src/lib/modelsSimpleApi')
    vi.mocked(api).mockResolvedValueOnce(new Response('{"error":"stale_revision"}', { status: 409, headers: { 'Content-Type': 'application/json' } }))
    await expect(wire.putOrder('me', 'design', { rank: [], not: [] }, 1, simplePerson)).rejects.toMatchObject({ status: 409, message: 'stale_revision' })
    vi.mocked(api).mockResolvedValueOnce(new Response('{"items":[]}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await expect(wire.getRegistry()).rejects.toThrow('Invalid model catalog')
  })
})
