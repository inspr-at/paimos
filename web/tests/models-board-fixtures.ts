// SPDX-License-Identifier: AGPL-3.0-only
import type { BoardCard, BoardColumn, ModelBoardDocument, ModelRule } from '../src/lib/modelsBoard'
export const boardPerson = '11111111-1111-4111-8111-111111111111'
export const boardCards: BoardCard[] = [
  { line: 'openai:sol', version: '6.1', harness: 'codex', effort: 'high' }, { line: 'anthropic:sonnet', version: '5.5', harness: 'claude', effort: 'high' },
  { line: 'anthropic:opus', version: '5.5', harness: 'claude', effort: 'high' }, { line: 'openai:astra', version: '6', harness: 'codex', effort: 'high' },
  { line: 'anthropic:fable', version: '5.1', harness: 'claude', effort: 'high' }, { line: 'xai:grok', version: '4.7', harness: 'grok', effort: 'xhigh' },
]
export const boardPin: ModelRule = { scope: 'workspace', project_id: null, column: 'frontend', line: 'anthropic:sonnet', lock: 'top', position: 0, why: 'Approved UI build model', set_by: 'Workspace admin', set_at: '2026-10-07T11:00:00Z' }
export function boardFixture(german = false): ModelBoardDocument {
  const kinds = [
    ['frontend', 'Frontend build', 'Frontend-Build', 'Vue code that implements an approved design.', 'Vue-Code, der ein freigegebenes Design umsetzt.'],
    ['backend', 'Backend build', 'Backend-Build', 'Go code, APIs, SQL and migrations behind the app.', 'Go-Code, APIs, SQL und Migrationen hinter der App.'],
    ['design', 'UI design', 'UI-Design', 'Screens and interaction, designed as an HTML mock before any code.', 'Screens und Interaktion, als HTML-Mock gestaltet, bevor es Code gibt.'],
    ['other', 'Everything else', 'Alles andere', 'Any ticket no other kind describes, and every kind without its own column.', 'Jedes Ticket, das keine andere Art beschreibt, und jede Art ohne eigene Spalte.'],
    ['review:openai', 'Review of Codex', 'Prüfung Codex', 'Checks what a Codex model wrote, before it merges.', 'Prüft, was ein Codex-Modell geschrieben hat, bevor es zusammengeführt wird.'],
    ['review:anthropic', 'Review of Claude', 'Prüfung Claude', 'Checks what a Claude model wrote, before it merges.', 'Prüft, was ein Claude-Modell geschrieben hat, bevor es zusammengeführt wird.'],
    ['review:xai', 'Review of Grok', 'Prüfung Grok', 'Checks what a Grok model wrote, before it merges.', 'Prüft, was ein Grok-Modell geschrieben hat, bevor es zusammengeführt wird.'],
    ['concept', 'Concepts', 'Konzepte', 'Product or architecture concepts and ADRs, written only on request. Not UI designs.', 'Produkt- oder Architekturkonzepte und ADRs, nur auf Anfrage geschrieben. Keine UI-Designs.'],
  ]
  const columns = kinds.map(([slug, en, de, sentenceEn, sentenceDe]): BoardColumn => {
    const column = slug!, review = column.startsWith('review:'), author = column.split(':')[1]
    const eligible = boardCards.filter(card => !review ? column === 'concept' || card.harness !== 'grok' : ['openai:sol', 'anthropic:opus', 'anthropic:fable', 'xai:grok'].includes(card.line))
    const excluded = review ? eligible.filter(card => card.line.startsWith(`${author}:`)).map(card => ({ ...card, effort: 'xhigh', lock: { kind: 'cross_family' as const, value: 'not' as const, who: null, why: 'A review must use another model family.', at: null, scope: 'policy' } })) : []
    if (review) eligible.sort((a, b) => ['openai:sol', 'xai:grok', 'anthropic:opus', 'anthropic:fable'].indexOf(a.line) - ['openai:sol', 'xai:grok', 'anthropic:opus', 'anthropic:fable'].indexOf(b.line))
    if (column === 'design') eligible.sort((a, b) => ['anthropic:opus', 'anthropic:sonnet', 'openai:sol', 'anthropic:fable', 'openai:astra'].indexOf(a.line) - ['anthropic:opus', 'anthropic:sonnet', 'openai:sol', 'anthropic:fable', 'openai:astra'].indexOf(b.line))
    const list = eligible.filter(card => !excluded.some(value => value.line === card.line)).map(card => ({ ...card, ...(review ? { effort: 'xhigh' } : {}) }))
    if (column === 'frontend') { const pinned = list.splice(list.findIndex(card => card.line === boardPin.line), 1)[0]!; list.unshift({ ...pinned, lock: { kind: 'rule', value: 'top', who: boardPin.set_by, at: boardPin.set_at, scope: 'workspace', why: boardPin.why } }) }
    return { column, label: (german ? de : en)!, short: (german ? de : en)!, sentence: (german ? sentenceDe : sentenceEn)!, fixed: column === 'other' || review || column === 'concept', hidden: false, source: 'template', thinking: { word: 'standard', source: 'default' }, list, top: list.filter(card => card.lock?.value === 'top'), free: list.filter(card => !card.lock), bottom: [], not: excluded, cant: review ? boardCards.filter(card => !eligible.some(value => value.line === card.line)).map(card => ({ line: card.line, reason: 'Review requires a qualified strong or frontier model at xhigh' })) : column !== 'concept' ? [{ line: 'xai:grok', reason: german ? 'Keine Werkzeuge für diese Arbeit' : 'No tools for this work' }] : [] }
  })
  columns.sort((a, b) => ['design', 'frontend', 'backend', 'other', 'review:openai', 'review:anthropic', 'review:xai', 'concept'].indexOf(a.column) - ['design', 'frontend', 'backend', 'other', 'review:openai', 'review:anthropic', 'review:xai', 'concept'].indexOf(b.column))
  return { person_id: boardPerson, layer: 'mine', situation: 'first', revision: 3, profile: { scope: 'person', person_id: boardPerson, revision: 3, template: null, thinking: null, usage: null, residency: null, hidden_kinds: [], dismissed_lines: [] }, columns, tray: [{ line: 'openai:nova', version: '1', harness: 'codex', effort: 'high', introduced_at: '2026-10-07T12:00:00Z' }], residency: { own: null, effective: 'any' }, needs_you: [] }
}
