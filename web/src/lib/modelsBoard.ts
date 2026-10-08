// SPDX-License-Identifier: AGPL-3.0-only
// Display and editing only: the server owns eligibility and resolution.
export type BoardLayer = 'mine' | 'default' | 'rules'
export type BoardSituation = 'first' | 'fix' | 'stuck'
export type BoardMode = 'auto' | 'simple' | 'expert'
export type ThinkingWord = 'lean' | 'standard' | 'deep' | 'max'
export type BoardZone = 'list' | 'top' | 'free' | 'bottom' | 'not'
export interface BoardContext { layer: BoardLayer; project?: string; situation: BoardSituation }
export interface BoardLock { kind: 'rule' | 'cross_family' | 'residency'; value: 'top' | 'bottom' | 'not'; who: string | null; why: string; at: string | null; scope: string }
export interface BoardCard { line: string; version: string; harness: string; effort: string; lock?: BoardLock; introduced_at?: string }
export interface BoardColumn {
  column: string; label: string; short: string; sentence: string; fixed: boolean; hidden: boolean
  source: 'own' | 'default' | 'template' | 'follows'; follows_first?: boolean
  thinking: { word: ThinkingWord; source: string; from_column?: boolean; own?: ThinkingWord | null }
  list: BoardCard[]; top: BoardCard[]; free: BoardCard[]; bottom: BoardCard[]; not: BoardCard[]; cant: { line: string; reason: string }[]
}
export interface BoardProfile {
  scope: 'person' | 'workspace'; person_id: string | null; revision: number
  template: 'best' | 'balanced' | 'save' | null; thinking: 'lean' | 'standard' | 'deep' | 'max' | null
  usage: 'careful' | 'balanced' | 'maxout' | null; residency: 'any' | 'eu' | 'local' | null
  hidden_kinds: string[]; dismissed_lines: string[]
}
export interface ModelBoardDocument {
  person_id: string | null; layer: BoardLayer; situation: BoardSituation; revision: number; profile: BoardProfile
  columns: BoardColumn[]; tray: BoardCard[]; residency: { own: string | null; effective: 'any' | 'eu' | 'local' }; needs_you: string[]
}
export interface BoardWriteResult { person_id: string | null; revision: number; profile: BoardProfile; dry_run: boolean; moved: { column: string; before: string[]; after: string[] }[]; running_outside?: string[] }
export interface ModelRule { scope: 'workspace' | 'project'; project_id: string | null; column: string; line: string; lock: 'top' | 'bottom' | 'not'; position: number; why: string; set_by: string | null; set_at: string }
export interface RulesDocument { revision: number; rules: ModelRule[] }
export interface OrderBody { rank: string[]; not: string[] }
export interface RulesBody { top: { line: string; why: string }[]; bottom: { line: string; why: string }[]; not: Record<string, string> }
export const zonesFor = (layer: BoardLayer): BoardZone[] => layer === 'rules' ? ['top', 'free', 'bottom', 'not'] : ['list', 'not']
export const contextKey = (context: BoardContext) => JSON.stringify([context.layer, context.project ?? '', context.situation])
export const columnContext = (context: BoardContext, column: string): BoardContext => ({ ...context, situation: column.startsWith('review:') || column === 'concept' ? 'first' : context.situation })
export function canMove(card: BoardCard, context: BoardContext): boolean {
  if (!card.lock) return true
  return context.layer === 'rules' && card.lock.kind === 'rule' && card.lock.scope === (context.project ? 'project' : 'workspace')
}
export function lineName(card: Pick<BoardCard, 'line' | 'version'>): string {
  if (card.line === 'openai:sol') return `GPT-${card.version || '6.1'} Sol`
  if (card.line === 'openai:astra') return `GPT-${card.version || '6'} Astra`
  const names: Record<string, string> = { 'anthropic:opus': 'Opus', 'anthropic:sonnet': 'Sonnet', 'anthropic:fable': 'Fable', 'xai:grok': 'Grok' }
  const base = names[card.line] ?? card.line.split(':').at(-1) ?? card.line
  return card.version ? `${base} ${card.version}` : base
}
export const boardText = (de: boolean, en: string, german: string) => de ? german : en
export function orderBody(column: BoardColumn): OrderBody {
  return { rank: column.list.filter(card => !card.lock).map(card => card.line), not: column.not.filter(card => !card.lock).map(card => card.line) }
}
export function moveOrder(column: BoardColumn, line: string, zone: 'list' | 'not', index: number): OrderBody {
  const result = orderBody(column)
  result.rank = result.rank.filter(id => id !== line); result.not = result.not.filter(id => id !== line)
  const list = zone === 'list' ? result.rank : result.not
  list.splice(Math.max(0, Math.min(index, list.length)), 0, line)
  return result
}
export function stepTarget(column: BoardColumn, line: string, direction: number, context: BoardContext): { zone: BoardZone; index: number } | null {
  const zones = zonesFor(context.layer)
  const source = zones.find(zone => column[zone].some(card => card.line === line))
  if (!source) return null
  const cards = column[source].filter(card => canMove(card, context)), index = cards.findIndex(card => card.line === line)
  if (index < 0) return null
  const next = index + direction
  if (next >= 0 && next < cards.length) return { zone: source, index: next }
  const adjacent = zones[zones.indexOf(source) + direction]
  if (!adjacent) return null
  return { zone: adjacent, index: direction < 0 ? column[adjacent].filter(card => canMove(card, context)).length : 0 }
}
export function rulesBody(document: RulesDocument, column: string): RulesBody {
  const byLine = new Map<string, ModelRule>()
  for (const rule of document.rules.filter(rule => rule.column === column)) {
    if (!byLine.has(rule.line) || rule.scope === 'project') byLine.set(rule.line, rule)
  }
  const rows = [...byLine.values()].sort((a, b) => a.position - b.position)
  return { top: rows.filter(rule => rule.lock === 'top').map(({ line, why }) => ({ line, why })), bottom: rows.filter(rule => rule.lock === 'bottom').map(({ line, why }) => ({ line, why })), not: Object.fromEntries(rows.filter(rule => rule.lock === 'not').map(rule => [rule.line, rule.why])) }
}
export function moveRule(before: RulesBody, line: string, zone: BoardZone, index: number, why: string): RulesBody {
  const result: RulesBody = { top: before.top.filter(pin => pin.line !== line), bottom: before.bottom.filter(pin => pin.line !== line), not: { ...before.not } }
  delete result.not[line]
  if (zone === 'top' || zone === 'bottom') result[zone].splice(Math.max(0, Math.min(index, result[zone].length)), 0, { line, why })
  if (zone === 'not') result.not[line] = why
  return result
}

/** Translate shipped defaults only; workspace-authored names and definitions stay intact. */
export function localizeColumn(column: BoardColumn, german: boolean): BoardColumn {
  if (!german) return column
  const words: Record<string, string> = {
    'UI design': 'UI-Design', 'Frontend build': 'Frontend-Build', 'Backend build': 'Backend-Build',
    'Infrastructure': 'Infrastruktur', 'Docs and copy': 'Doku und Texte', 'Security': 'Sicherheit',
    'Everything else': 'Alles andere', 'Concepts': 'Konzepte',
    'Review of Codex': 'Prüfung Codex', 'Review of Claude': 'Prüfung Claude', 'Review of Grok': 'Prüfung Grok',
    'Screens and interaction, designed as an HTML mock before any code.': 'Screens und Interaktion, als HTML-Mock gestaltet, bevor es Code gibt.',
    'Vue code that implements an approved design.': 'Vue-Code, der ein freigegebenes Design umsetzt.',
    'Go code, APIs, SQL and migrations behind the app.': 'Go-Code, APIs, SQL und Migrationen hinter der App.',
    'CI, Nix, deploys and the machines agents run on.': 'CI, Nix, Deploys und die Rechner, auf denen Agenten laufen.',
    'Words people read: READMEs, guides, release notes and interface text.': 'Texte, die Menschen lesen: READMEs, Anleitungen, Release Notes und Oberflächentexte.',
    'Who may do what: sign-in, keys, permissions and audits.': 'Wer was darf: Anmeldung, Schlüssel, Rechte und Prüfprotokolle.',
    'Any ticket no other kind describes, and every kind without its own column.': 'Jedes Ticket, das keine andere Art beschreibt, und jede Art ohne eigene Spalte.',
    'Product or architecture concepts and ADRs, written only on request. Not UI designs.': 'Produkt- oder Architekturkonzepte und ADRs, nur auf Anfrage geschrieben. Keine UI-Designs.',
  }
  for (const name of ['Codex', 'Claude', 'Grok']) words[`Checks what a ${name} model wrote, before it merges.`] = `Prüft, was ein ${name}-Modell geschrieben hat, bevor es gemergt wird. ${name} prüft sich nie selbst.`
  return { ...column, label: words[column.label] ?? column.label, short: words[column.short] ?? column.short, sentence: words[column.sentence] ?? column.sentence }
}
