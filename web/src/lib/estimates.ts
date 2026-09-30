// SPDX-License-Identifier: AGPL-3.0-only
import type { WorkNode } from './api.ts'

export interface TicketEstimate {
  hours: number | null
  estimated_children: number
  open_children: number
  by?: { id: string; name: string }
}
export function parseEstimate(raw: string): number | null {
  const match = /^(\d+(?:\.\d+)?|\.\d+)(h|m)?$/.exec(raw.trim())
  if (!match) return null
  const hours = Number(match[1]) / (match[2] === 'm' ? 60 : 1)
  return Number.isFinite(hours) && hours > 0 && hours <= 200 ? hours : null
}
export function estimateHours(item: Pick<WorkNode, 'fields' | 'estimate'> & { kind_slug?: string }): number | null {
  const hours = item.kind_slug === 'epic' ? item.estimate?.hours : item.fields.estimate_hours
  return typeof hours === 'number' && Number.isFinite(hours) && hours > 0 && (item.kind_slug === 'epic' || hours <= 200) ? hours : null
}
export function formatEstimate(hours: number): string {
  if (hours < 1 / 60) return '<1m'
  return hours < 1 ? `${Number((hours * 60).toFixed(1))}m` : `~${Number(hours.toFixed(2))}h`
}
export function estimateDisplay(item: Pick<WorkNode, 'fields' | 'estimate'> & { kind_slug?: string }) {
  const hours = estimateHours(item)
  const epic = item.kind_slug === 'epic'
  const agent = !epic && hours !== null && item.fields.estimate_source === 'agent'
  const draft = agent && item.fields.estimate_confirmed !== true
  const points = item.fields.estimate_lp
  const text = hours !== null ? formatEstimate(hours) : !epic && typeof points === 'number' && points > 0 ? `${points} pt` : ''
  const who = item.estimate?.by?.name
  const at = typeof item.fields.estimate_at === 'string' ? Date.parse(item.fields.estimate_at) : NaN
  const when = Number.isFinite(at) ? new Date(at).toLocaleString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''
  const origin = agent ? 'Agent estimate' : 'Estimate'
  const tip = epic && item.estimate ? `${item.estimate.estimated_children} of ${item.estimate.open_children} open children estimated · agent hours` :
    hours !== null ? `${origin}${who ? ` by ${who}` : ''}${when ? `, ${when}` : ''}${agent && !draft ? ' · confirmed by working agent' : ''}\nAgent work time until ready for review` : text ? 'Legacy points estimate' : 'Set agent work hours until ready for review'
  return { hours, text, draft, tip }
}
export function estimateControlLabel(item: Parameters<typeof estimateDisplay>[0]): string {
  const view = estimateDisplay(item)
  if (!view.text) return 'Add estimate'
  const author = item.estimate?.by?.name
  const origin = view.draft ? `, agent draft, not confirmed${author ? `, by ${author}` : ''}` : ''
  return `Estimate: ${view.text}${origin}. Edit estimate`
}
