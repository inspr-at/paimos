// SPDX-License-Identifier: AGPL-3.0-only
import document from '../../../internal/nodes/status_definitions.json' with { type: 'json' }

export interface StatusRule { enabled: boolean; days?: number }
export interface StatusDefinition { state: string; label: string; meaning: string; hint: string; set_by: string; exit: boolean; rules: string[] }
export interface StatusHelp {
  definitions: StatusDefinition[]
  queued: { label: string; meaning: string; is_status: false }
  recurring?: { label: string; meaning: string; is_status: false }
  autopilot: { enabled: boolean; effective_enabled: boolean; project_mode: 'inherit' | 'on' | 'off'; rules: Record<string, StatusRule> }
  triage: { mode: 'off' | 'suggest' | 'apply'; available: boolean }
  limits_source: 'defaults' | 'workspace'
  project_id?: string; project_name?: string
}
export function defaultStatusHelp(): StatusHelp { return structuredClone(document) as StatusHelp }
export const days = (count: number) => `${count} ${count === 1 ? 'day' : 'days'}`
export function ruleLive(help: StatusHelp, key: string): boolean { return help.autopilot.effective_enabled && help.autopilot.rules[key]?.enabled === true }
export function statusHint(help: StatusHelp, state: string): string {
  if (state === 'accepted') return ruleLive(help, 'accept')
    ? `Confirmed by a person or customer, or automatically ${days(help.autopilot.rules.accept.days!)} after delivery`
    : 'Confirmed by a person or customer'
  return help.definitions.find(def => def.state === state)?.hint ?? ''
}
export function ruleCopy(key: string): { before: string; after: string; event?: string; arrowAfterLimit?: boolean; to?: string } {
  switch (key) {
    case 'new': return { before: 'Untriaged', after: 'triage list', arrowAfterLimit: true }
    case 'backlog': return { before: 'Untouched', after: 'suggest Cancelled', arrowAfterLimit: true }
    case 'blocked': return { before: 'Reminder after', after: '' }
    case 'progress': return { before: 'No session, branch or PR activity for', after: 'Open, with a comment', arrowAfterLimit: true }
    case 'done': return { before: 'Merged but not released after', after: '“missed release” flag', arrowAfterLimit: true }
    case 'publish': return { before: '', after: '', event: 'Set when a release with the ticket is published' }
    case 'accept': return { before: '', after: 'without objection', to: 'Accepted' }
    default: return { before: '', after: '' }
  }
}
