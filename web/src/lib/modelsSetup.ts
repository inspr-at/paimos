// SPDX-License-Identifier: AGPL-3.0-only
import type { BoardCard, BoardProfile, BoardWriteResult, ModelBoardDocument, OrderBody } from './modelsBoard'
export interface SetupAnswers {
  template: NonNullable<BoardProfile['template']>
  usage: NonNullable<BoardProfile['usage']>
  thinking: NonNullable<BoardProfile['thinking']>
  residency: BoardProfile['residency']
  rank: string[]
  not: string[]
}
export type SetupPatch = Pick<BoardProfile, 'template' | 'usage' | 'thinking' | 'residency'> & { other_order: OrderBody | null }
export interface SetupPreview { key: string; revision: number; person: string; body: SetupPatch; before: SetupPatch; moved: BoardWriteResult['moved'] }
export function setupCards(board: ModelBoardDocument): BoardCard[] {
  const column = board.columns.find(value => value.column === 'other')
  // Residency can hold the entire list. Keep those cards in the draft so
  // tightening providers does not destroy the order when no route qualifies.
  return [...(column?.list ?? []), ...(column?.not.filter(card => card.lock?.kind === 'residency') ?? [])].filter(card => !card.lock || card.lock.kind === 'residency').map(card => ({ ...card, lock: undefined }))
}
export function setupAnswers(board: ModelBoardDocument, workspace: ModelBoardDocument): SetupAnswers {
  return {
    template: board.profile.template ?? workspace.profile.template ?? 'balanced',
    usage: board.profile.usage ?? workspace.profile.usage ?? 'balanced',
    thinking: board.profile.thinking ?? workspace.profile.thinking ?? 'standard',
    residency: board.residency.own === 'eu' || board.residency.own === 'local' ? board.residency.own : null,
    rank: setupCards(board).map(card => card.line),
    not: board.columns.find(column => column.column === 'other')?.not.filter(card => !card.lock).map(card => card.line) ?? [],
  }
}
export function setupPatch(answers: SetupAnswers): SetupPatch {
  return { template: answers.template, usage: answers.usage, thinking: answers.thinking, residency: answers.residency, other_order: { rank: [...answers.rank], not: [...answers.not] } }
}
export function setupUndo(profile: BoardProfile, previous: OrderBody | null): SetupPatch {
  return { template: profile.template, usage: profile.usage, thinking: profile.thinking, residency: profile.residency, other_order: previous ? { rank: [...previous.rank], not: [...previous.not] } : null }
}
export function firstChoiceChanges(moved: BoardWriteResult['moved']) { return moved.filter(change => change.before[0] !== change.after[0]) }
export function moveSetupCard(rank: string[], line: string, index: number): string[] {
  if (!rank.includes(line)) return [...rank]
  const result = rank.filter(value => value !== line)
  result.splice(Math.max(0, Math.min(index, result.length)), 0, line)
  return result
}
export function setupProviderAllowed(provider: SetupAnswers['residency'], workspace: ModelBoardDocument): boolean {
  return provider === null || ({ any: 0, eu: 1, local: 2 }[provider ?? 'any'] >= { any: 0, eu: 1, local: 2 }[workspace.residency.effective])
}
