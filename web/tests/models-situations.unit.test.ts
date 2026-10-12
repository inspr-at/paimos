// SPDX-License-Identifier: AGPL-3.0-only
// Models v2 W3 (AEON-1162): Quick jobs and the situation matrix. Risk: a cell writes an order the server refuses or
// that drops what the page showed (a review level other than the highest, a lost chain or exclusion), the matrix hides
// a cell it counts, or the Quick jobs "Why?" claims a model the server did not choose.
import { describe, expect, it } from 'vitest'
import {
  buildEntries, buildRows, quickView, situationCount, situationHint, situationOrder, situationRows, situationStep, situationSteps,
  type SimpleDocument, type SimpleRow,
} from '../src/lib/modelsSimple'
import { registry } from './models-simple-fixtures'

const entries = buildEntries(registry())
const entry = (line: string) => entries.find(item => item.line === line)!
const row = (column: string, line: string, effort: string): SimpleRow => ({ column, line, effort, mine: false, lock: null })
const labels = new Map([['design', 'UI design'], ['backend', 'Backend build'], ['concept', 'Concepts']])
const doc = (situations: SimpleDocument['situations']): SimpleDocument => ({
  all: row('other', 'openai:sol', 'xhigh'), exceptions: [row('design', 'anthropic:opus', 'xhigh')], reviews: { mode: 'cross_family' }, next: null,
  unavailable: [], new_lines: [], revision: 5, rules_revision: 2, person_id: null, situations,
})

describe('situation matrix', () => {
  it('shows every row with a cell, counts only what it shows, and writes orders the server accepts', () => {
    const stuck = { line: 'openai:astra', effort: 'xhigh', chain: [{ line: 'anthropic:opus', effort: 'max' }, { line: 'cursor:composer', effort: null }], end: 'wait' as const }
    const rows = situationRows({ doc: doc({ other: { stuck }, backend: { fix: { line: 'xai:grok', effort: 'high', chain: [], end: 'row' } } }), entries, labels, order: ['design', 'backend', 'concept'] })
    // Backend has no row of its own, but its Fix rounds order exists: it gets a row, labelled and in kind order.
    expect(rows.map(item => [item.column, item.label])).toEqual([['other', 'Default'], ['design', 'UI design'], ['backend', 'Backend build']])
    expect(rows[1]!.cells).toEqual({ small: null, fix: null, stuck: null, review: null })
    expect(situationCount(rows)).toBe(2)
    // Taking a step out keeps the other steps' levels and leaves the ending as stored; a step without a level sends none.
    const left = situationSteps(rows[0]!.cells.stuck).filter(step => step.line !== 'anthropic:opus')
    expect(situationOrder(left)).toEqual({ rank: ['openai:astra', 'cursor:composer'], efforts: { 'openai:astra': 'xhigh' } })
    // Undo writes back exactly the order the page showed, ending and saved exclusions included; no steps means "same as the row".
    expect(situationOrder(situationSteps(stuck), stuck.end, ['xai:grok'])).toEqual({ rank: ['openai:astra', 'anthropic:opus', 'cursor:composer'], efforts: { 'openai:astra': 'xhigh', 'anthropic:opus': 'max' }, end: 'wait', not: ['xai:grok'] })
    expect(situationOrder(situationSteps(stuck), stuck.end, [])).toHaveProperty('not', [])
    expect(situationOrder([])).toBeNull()
    expect(situationStep('fix', entry('anthropic:opus'), 'high')).toEqual({ line: 'anthropic:opus', effort: 'high' })
    // A document without situations (a person's own read) has no matrix to show.
    expect(situationRows({ doc: doc(undefined), entries, labels, order: [] }).every(item => Object.values(item.cells).every(cell => cell === null))).toBe(true)
    expect([situationHint('small', { small_hours: 2, fix_rounds: 3 }), situationHint('stuck', { small_hours: 2, fix_rounds: 1 }), situationHint('small', null)]).toEqual(['2 h or less', 'after 1 round', 'small work'])
  })
})

describe('a review step', () => {
  it('sends no level, so the server runs the highest native one instead of refusing a lower one', () => {
    // Reviews run at the model's highest native level and the server refuses any other: Opus has max, so xhigh would
    // be refused (review_floor). A review step sends no level at all, and its order carries none.
    expect(entry('anthropic:opus').efforts.map(effort => effort.name)).toEqual(['high', 'xhigh', 'max'])
    for (const level of ['high', 'xhigh', 'max']) expect(situationStep('review', entry('anthropic:opus'), level)).toEqual({ line: 'anthropic:opus', effort: null })
    expect(situationOrder([situationStep('review', entry('anthropic:opus'), 'xhigh')], 'auto')).toEqual({ rank: ['anthropic:opus'], efforts: {}, end: 'auto' })
  })
})

describe('Quick jobs', () => {
  it('names what was skipped in plain words, what runs now, and the default it falls back to', () => {
    const fallback = buildRows({ doc: doc({}), workspace: null, scope: 'default', canEdit: true, entries, labels })[0]
    const view = quickView({ fallback, entries, quick: { line: 'openai:luna', effort: 'low', harness: 'codex', trace: [
      { role: 'scout', line: 'cursor:composer', stage: 'column', reason: 'Quick jobs requires fast tier', selected: false },
      { role: 'scout', line: 'anthropic:sonnet', stage: 'column', reason: 'daily_limit', selected: false },
      { role: 'scout', line: 'openai:luna', stage: 'column', reason: '', selected: true },
    ] } })
    const text = view.steps.map(step => step.map(part => part.text).join(''))
    expect(text).toEqual([
      'PAIMOS takes the cheapest model that qualifies: a fast model with tools and an account with room.',
      'Skipped Composer 2.5: it isn’t a fast model.',
      'Skipped Sonnet 5.5: the account is at its daily cap.',
      'Right now: GPT-6 Luna · low through Codex.',
      'If none qualifies, the default runs: GPT-6.1 Sol · xhigh.',
    ])
    expect(view.steps[3]!.find(part => part.strong)?.text).toBe('GPT-6 Luna · low')
    // Nothing qualifies: it says the work waits, never names a model that is not running.
    const waiting = quickView({ fallback, entries, quick: { line: null, effort: null, harness: null, trace: [] } }).steps.map(step => step.map(part => part.text).join(''))
    expect(waiting).toContain('Right now nothing qualifies, so quick jobs wait.')
    expect(waiting.some(line => line.startsWith('Right now:'))).toBe(false)
  })
})
