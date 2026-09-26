// SPDX-License-Identifier: AGPL-3.0-only
// Typed client for GET /api/tickets/graph. The graph view arrives in a later
// package; this module only fetches the bounded, body-free projection.
import { api } from './api.ts'

export type TicketGraphType = 'ticket' | 'epic'
export type TicketStatusCategory = 'open' | 'doing' | 'done'
export type TicketGraphLinkKind = 'blocks' | 'relates' | 'implements' | 'duplicates' | 'parent'

export interface TicketGraphNode {
  id: string
  key: string
  title: string
  type: TicketGraphType
  status: string
  status_category: TicketStatusCategory
  priority: string | null
  parent_id: string | null
  release_id: string | null
  updated_at: string
  link_count: number
}
export interface TicketGraphLink {
  source: string
  target: string
  kind: TicketGraphLinkKind
}
export interface TicketGraph {
  nodes: TicketGraphNode[]
  links: TicketGraphLink[]
  truncated: boolean
}

export function ticketGraphPath(projectId: string, includeClosed = false): string {
  const params = new URLSearchParams({ project_id: projectId })
  if (includeClosed) params.set('include_closed', 'true')
  return `/tickets/graph?${params}`
}

export async function fetchTicketGraph(projectId: string, includeClosed = false, signal?: AbortSignal): Promise<TicketGraph> {
  const timeout = AbortSignal.timeout(20_000)
  const response = await api(ticketGraphPath(projectId, includeClosed), {
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  })
  if (!response.ok) throw new Error('The ticket graph could not be loaded. Please try again.')
  return response.json()
}
