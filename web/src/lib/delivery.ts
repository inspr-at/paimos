// SPDX-License-Identifier: AGPL-3.0-only
import { displayLanguage } from './displayLanguage.ts'
import { api, APIError } from './api.ts'

export type DeliveryState = 'built' | 'reviewed' | 'pushed' | 'ci_green' | 'in_queue' | 'merged' | 'queue_failed' | 'held'
export type DeliveryLanguage = 'en' | 'de'
export const deliverySteps = ['built', 'reviewed', 'pushed', 'ci_green', 'in_queue', 'merged'] as const
export interface DeliveryItem {
  id: string; project_id: string | null; ticket_node_id: string | null; repository: string; pull_request: number | null
  branch: string; head_sha: string; state: DeliveryState; state_since: string; owner: string; deadline_at: string | null
  held_reason: string | null; held_from_state: DeliveryState | null; required_checks_passed: number; required_checks_total: number
  link_source: string | null; updated_at: string
}
export interface DeliveryPage { items: DeliveryItem[]; next_cursor: string | null; available: boolean }
async function request<T>(path: string, method: string, signal?: AbortSignal, revision?: string): Promise<T> {
  const response = await api(path, { method, signal, ...(revision ? { headers: { 'If-Unmodified-Since': revision } } : {}) })
  if (!response.ok) { const data = await response.json().catch(() => ({})); throw new APIError(response.status, data.error || 'Delivery status could not be loaded.') }
  return response.json()
}
export const readDelivery = (nodeId: string, signal?: AbortSignal, after?: string) => request<DeliveryPage>(`/nodes/${encodeURIComponent(nodeId)}/delivery?limit=100${after ? `&after=${encodeURIComponent(after)}` : ''}`, 'GET', signal)
export const liftDeliveryHold = (item: DeliveryItem, signal?: AbortSignal) => request<DeliveryItem>(`/delivery/${encodeURIComponent(item.id)}/hold`, 'DELETE', signal, item.updated_at)
export const deliveryLanguage = (locale?: string | null): DeliveryLanguage => displayLanguage(locale)
export function stepIndex(state: DeliveryState, heldFrom?: DeliveryState | null): number {
  if (state === 'held') return heldFrom && heldFrom !== 'held' ? stepIndex(heldFrom) : -1
  return state === 'queue_failed' ? 4 : deliverySteps.indexOf(state as typeof deliverySteps[number])
}
export function ownerLabel(owner: string, lead: string, lang: DeliveryLanguage): string {
  return ({ coordinator: lead, ci: 'CI', queue: lang === 'de' ? 'Merge-Queue' : 'Merge queue', builder: 'Builder', person: lang === 'de' ? 'Eine Person' : 'A person' } as Record<string, string>)[owner] ?? ''
}
export function stateLabel(state: DeliveryState, lang: DeliveryLanguage): string {
  return (lang === 'de'
    ? { built: 'Gebaut', reviewed: 'Geprüft', pushed: 'Gepusht', ci_green: 'CI grün', in_queue: 'Eingereiht', merged: 'Gemergt', queue_failed: 'In der Queue gescheitert', held: 'Angehalten' }
    : { built: 'Built', reviewed: 'Reviewed', pushed: 'Pushed', ci_green: 'CI green', in_queue: 'In queue', merged: 'Merged', queue_failed: 'Failed in queue', held: 'On hold' })[state]
}
export function nextAction(item: DeliveryItem, lang: DeliveryLanguage): string {
  return (lang === 'de'
    ? { built: 'modellübergreifende Prüfung einholen', reviewed: 'geprüften Stand pushen', pushed: `Pflichtprüfungen laufen (${item.required_checks_passed} von ${item.required_checks_total} bestanden)`, ci_green: 'in die Merge-Queue einreihen', in_queue: 'mergt, sobald die Queue-Prüfungen bestehen', merged: 'Nichts mehr zu tun', queue_failed: 'Korrektur pushen, um erneut einzureihen', held: 'Halt aufheben' }
    : { built: 'get a cross-family review', reviewed: 'push the reviewed head', pushed: `required checks are running (${item.required_checks_passed} of ${item.required_checks_total} passed)`, ci_green: 'add it to the merge queue', in_queue: 'merges once its queue checks pass', merged: 'Nothing left to do', queue_failed: 'push a fix to queue again', held: 'lift the hold' })[item.state]
}
export const minutesSince = (at: string, now: number) => Math.max(0, Math.floor((now - Date.parse(at)) / 60_000))
export const overdue = (item: DeliveryItem, now: number) => item.state !== 'held' && item.state !== 'merged' && !!item.deadline_at && Date.parse(item.deadline_at) < now
export function sortDelivery(items: DeliveryItem[], now: number): DeliveryItem[] {
  const rank = (item: DeliveryItem) => item.state === 'merged' ? 2 : overdue(item, now) || ['queue_failed', 'held'].includes(item.state) ? 0 : 1
  return items.filter(item => item.pull_request !== null).sort((a, b) => rank(a) - rank(b) || (Date.parse(a.deadline_at ?? '') || Infinity) - (Date.parse(b.deadline_at ?? '') || Infinity) || a.id.localeCompare(b.id))
}
export function pullURL(item: DeliveryItem): string | undefined {
  return /^[\w.-]+\/[\w.-]+$/.test(item.repository) && Number.isSafeInteger(item.pull_request) && item.pull_request! > 0 ? `https://github.com/${item.repository}/pull/${item.pull_request}` : undefined
}
