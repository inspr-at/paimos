// SPDX-License-Identifier: AGPL-3.0-only
// TG1's read-only adapter. Register TicketGraphView in TICKET_VIEWS; it receives
// project/filters and emits open(key) for ProjectView's existing ticket panel.
// TG0 already mounts the API through nodes.New; no new module/manifest is needed.
import type { GraphData, GraphLink } from './graphRenderer.ts'
import type { TicketGraph, TicketGraphLinkKind, TicketStatusCategory } from './ticketGraph.ts'
import { excluded, included, levelOf, type Dimension, type ListFilters } from './ticketList.ts'
import { normaliseState } from './work.ts'

// The graph API has no assignee, labels or date-range projection. Visible
// membership comes from the shared server-filtered context in headerGlimpse.ts.
export const TICKET_GRAPH_FILTERS: Dimension[] = ['status', 'priority', 'shape', 'depth', 'type']
export const ticketStatusTokens = { open: '--st-new', doing: '--st-progress', done: '--st-closed' } as const satisfies Record<TicketStatusCategory, `--${string}`>
export const ticketLinkStyles = {
  parent: { color: '--ink-3', width: .45, directed: false, curvature: 0 },
  blocks: { color: '--warn', width: 1.4, directed: true, curvature: .12 },
  relates: { color: '--ink-2', width: .8, directed: false, curvature: .08 },
  implements: { color: '--teal', width: 1, directed: true, curvature: -.12 },
  duplicates: { color: '--kind-external-system', width: .65, directed: true, curvature: .28 },
} as const satisfies Record<TicketGraphLinkKind, Pick<GraphLink, 'color' | 'width' | 'directed' | 'curvature'>>

export interface TicketGraphState { data: TicketGraph; visible: TicketGraph; loading: boolean }

function matches(values: string[], value: string, normalise = (v: string) => v.toLowerCase()): boolean {
  const actual = normalise(value), yes = included(values), no = excluded(values)
  return (!yes.length || yes.some(v => normalise(v) === actual)) && !no.some(v => normalise(v) === actual)
}
export function filterTicketGraph(data: TicketGraph, filters: ListFilters): TicketGraph {
  const words = filters.q.trim().toLowerCase().split(/\s+/).filter(Boolean)
  const nodes = data.nodes.filter(node => (filters.showClosed || node.status_category !== 'done')
    && matches(filters.status, node.status, normaliseState)
    && matches(filters.priority, node.priority ?? 'none')
    && matches(filters.type, levelOf({ is_leaf: node.is_leaf, depth: node.depth, kind_slug: node.type }))
    && matches(filters.shape, node.is_leaf === false || (node.is_leaf === undefined && node.type === 'epic') ? 'parent' : 'leaf')
    && matches(filters.depth, String(node.depth ?? 1))
    && words.every(word => `${node.key} ${node.title}`.toLowerCase().includes(word)))
  const ids = new Set(nodes.map(node => node.id))
  return { nodes, links: data.links.filter(link => ids.has(link.source) && ids.has(link.target)), truncated: data.truncated }
}

export function ticketGraphData(data: TicketGraph, projectKey: string): GraphData {
  const byId = new Map(data.nodes.map(node => [node.id, node]))
  // Cluster by work-parent ancestry, including nested work parents.
  const group = (id: string): string => {
    const seen = new Set<string>()
    let node = byId.get(id)
    while (node && !seen.has(node.id)) {
      if ((node.is_leaf === false || (node.is_leaf === undefined && node.type === 'epic'))) return node.id
      seen.add(node.id); node = node.parent_id ? byId.get(node.parent_id) : undefined
    }
    return 'unparented'
  }
  return {
    nodes: data.nodes.map(node => ({
      id: node.id, label: ticketLabel(node.key, node.title), group: group(node.id),
      color: ticketStatusTokens[node.status_category],
      // Sublinear, bounded size keeps busy hubs legible without swallowing peers.
      weight: Math.log2(1 + Math.min(100, Math.max(0, node.link_count))) * .65 + ((node.is_leaf === false || (node.is_leaf === undefined && node.type === 'epic')) ? 4.5 : 0),
      href: `/p/${encodeURIComponent(projectKey)}/${encodeURIComponent(node.key)}`,
    })),
    links: data.links.filter(link => byId.has(link.source) && byId.has(link.target)).map(link => ({ ...link, ...ticketLinkStyles[link.kind] })),
  }
}

function ticketLabel(key: string, title: string): string {
  const chars = Array.from(title.replace(/\s+/g, ' ').trim()), budget = Math.max(8, 30 - key.length - 1)
  return `${key} ${chars.length > budget ? chars.slice(0, budget - 1).join('').trimEnd() + '…' : chars.join('')}`
}
