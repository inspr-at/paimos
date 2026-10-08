// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { firstChoiceChanges, moveSetupCard, setupAnswers, setupCards, setupPatch, setupProviderAllowed, setupUndo } from '../src/lib/modelsSetup'
import { boardFixture } from './models-board-fixtures'

// Risk: setup could mutate own columns, forget personal exclusions or loosen
// the workspace provider limit. Same answers must always create the same patch.
it('starts from the current board and maps identical answers without touching own columns or exclusions', () => {
  const board = boardFixture(), workspace = boardFixture(), backend = board.columns.find(column => column.column === 'backend')!
  workspace.profile.template = 'save'; workspace.profile.usage = 'careful'; workspace.profile.thinking = 'max'; workspace.residency.effective = 'local'
  backend.source = 'own'; backend.list.reverse()
  const other = board.columns.find(column => column.column === 'other')!
  other.not.push(other.list.pop()!)
  const original = structuredClone(board), answers = setupAnswers(board, workspace)
  expect(answers).toMatchObject({ template: 'save', usage: 'careful', thinking: 'max', not: ['anthropic:fable'] })
  answers.rank = moveSetupCard(answers.rank, 'anthropic:opus', 0)
  expect(setupPatch(answers)).toEqual(setupPatch(structuredClone(answers)))
  expect(setupPatch(answers)).toMatchObject({ other_order: { rank: ['anthropic:opus', 'openai:sol', 'anthropic:sonnet', 'openai:astra'], not: ['anthropic:fable'] } })
  expect(board).toEqual(original); expect(setupPatch(answers)).not.toHaveProperty('replace_own')
  expect(setupProviderAllowed('eu', workspace)).toBe(false); expect(setupProviderAllowed('local', workspace)).toBe(true); expect(setupProviderAllowed(null, workspace)).toBe(true)
  const previous = { rank: ['openai:sol'], not: ['anthropic:opus'] }
  expect(setupUndo(board.profile, null)).toMatchObject({ template: null, thinking: null, usage: null, residency: null, other_order: null })
  expect(setupUndo(board.profile, previous).other_order).toEqual(previous)
  const heldRank = other.list.map(card => card.line)
  other.not = other.list.map(card => ({ ...card, lock: { kind: 'residency', value: 'not', who: null, why: 'No route', at: null, scope: 'workspace' } })); other.list = []
  expect(setupCards(board).map(card => card.line)).toEqual(heldRank)
  expect(firstChoiceChanges([{ column: 'backend', before: ['a', 'b'], after: ['a'] }, { column: 'other', before: ['a'], after: [] }])).toEqual([{ column: 'other', before: ['a'], after: [] }])
})
