// SPDX-License-Identifier: AGPL-3.0-only
// P6 presentation model. Wire contracts live in decisionDeskApi.ts.
export type DeskOutcome = 'once' | 'always' | 'requirement' | 'doctrine'
export type DeskKind = 'question' | 'handover' | 'approval' | 'action' | 'rule' | 'tier'
// Outside the server option-ID alphabet, so an agent option cannot collide.
export const CUSTOM_ANSWER = '@custom'
export interface DeskChoice { id: string; title: string; description: string; answer: string; field?: boolean }
export interface DeskItem {
  id: string; kind: DeskKind; projectId: string; projectName: string; ticketId?: string; ticketKey?: string
  title: string; context: string; findings: string; meanwhile: string; destination: string
  choices: DeskChoice[]; recommended?: string; why: string; outcome: DeskOutcome; suggestion: string
  revision: number; createdAt: string; expiresAt?: string; held: boolean; decided: boolean
  answer?: string; optionId?: string; reason?: string; delivery?: string; fromRecord?: string
  unavailable?: string
}
export interface DeskDraft { optionId: string; answer: string; reason: string; outcome: DeskOutcome; dirty: boolean }
export const outcomeLabels: Record<DeskOutcome, string> = { once: 'Once', always: 'Always', requirement: 'Requirement', doctrine: 'Doctrine' }
export const kindLabels: Record<DeskKind, string> = { question: 'Question', handover: 'Handover question', approval: 'Approval', action: 'Action request', rule: 'Rule change', tier: 'Tier request' }
export function draftFor(item: DeskItem): DeskDraft {
  return { optionId: item.optionId ?? item.recommended ?? item.choices[0]?.id ?? '', answer: item.answer ?? '', reason: item.reason ?? '', outcome: item.outcome, dirty: false }
}
export function answerFor(item: DeskItem, draft: DeskDraft): string {
  const choice = item.choices.find(option => option.id === draft.optionId)
  return (choice?.field ? draft.answer : choice?.answer ?? draft.answer).trim()
}
export function outcomeUnavailable(item: DeskItem, outcome: DeskOutcome): string {
  if (item.kind !== 'question' && item.kind !== 'handover') return 'This action uses its own protected decision flow.'
  // TODO AEON-565: enable only after the outcome contract offers this capability.
  if (outcome !== 'once') return `${outcomeLabels[outcome]} publishing is not available yet.`
  return ''
}
/** IDs freeze at an explicit round boundary. Refresh only updates arrivals. */
export function newRound(items: DeskItem[]): string[] { return items.map(item => item.id) }
export function arrivals(items: DeskItem[], round: string[]): DeskItem[] {
  const ids = new Set(round)
  return items.filter(item => !ids.has(item.id) && !item.decided)
}
export function roundCounts(items: DeskItem[], round: string[], skipped: Set<string>) {
  const byId = new Map(items.map(item => [item.id, item]))
  let decided = 0, open = 0, skip = 0
  for (const id of round) {
    if (byId.get(id)?.decided) decided++
    else if (skipped.has(id)) skip++
    else open++
  }
  return { decided, open, skipped: skip }
}
export function fieldTarget(target: EventTarget | null): target is HTMLElement {
  return target instanceof HTMLElement && (!!target.closest('input, textarea, select, [contenteditable="true"], [role="textbox"]'))
}
export function macPlatform(platform: string): boolean { return /Mac|iPhone|iPad|iPod/i.test(platform) }
export function submitModifier(event: Pick<KeyboardEvent, 'metaKey' | 'ctrlKey' | 'altKey' | 'shiftKey'>, mac: boolean): boolean {
  return !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)
}
