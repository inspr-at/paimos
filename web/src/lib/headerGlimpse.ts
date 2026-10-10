// SPDX-License-Identifier: AGPL-3.0-only
// Gates for the project-header ticket graph. The glimpse is a decoration:
// wide screens, motion allowed, enough tickets and at least one link.
import { listNodes } from './api.ts'
import { apiParams, hasFilters, type ListFilters } from './ticketList.ts'
import { fetchTicketGraph, type TicketGraph } from './ticketGraph.ts'

// Exported context read path for the header and Tickets Graph integration. Resolve saved views to
// ListFilters before calling. The bounded graph supplies topology; the normal
// Tickets query supplies membership, including assignee and server search.
// Paging matters: restricting to the table's first page would silently hide
// matches. No module or plugin wiring is required; both APIs already exist.
export async function loadTicketGraphContext(projectId: string, filters: ListFilters, signal: AbortSignal): Promise<{ data: TicketGraph; visible: TicketGraph }> {
  signal = AbortSignal.any([signal, AbortSignal.timeout(20_000)])
  // Graph categories are spelling-based; a kind can put Accepted in Open or
  // QA in Done. Keep all topology and let the shared list policy decide Hide
  // membership, including the default five choices and Reset.
  const data = await fetchTicketGraph(projectId, true, signal)
  if ((!hasFilters(filters) && filters.showClosed) || !data.nodes.length) return { data, visible: data }
  const remaining = new Set(data.nodes.map(node => node.id)), matches = new Set<string>()
  let cursor: string | undefined
  const seen = new Set<string>()
  do {
    signal.throwIfAborted()
    const page = await listNodes(apiParams(projectId, filters, { limit: 500, cursor }), { signal })
    for (const node of page.items) if (remaining.delete(node.id)) matches.add(node.id)
    cursor = page.next_cursor ?? undefined
    if (cursor && seen.has(cursor)) throw new Error('The ticket query could not finish. Please try again.')
    if (cursor) seen.add(cursor)
  } while (cursor && remaining.size)
  return { data, visible: {
    nodes: data.nodes.filter(node => matches.has(node.id)),
    links: data.links.filter(link => matches.has(link.source) && matches.has(link.target)),
    truncated: data.truncated,
  } }
}

export const GLIMPSE_MIN_WIDTH = 1280
export const GLIMPSE_MIN_TICKETS = 8
export const GLIMPSE_MIN_LINKS = 1

export function headerGlimpseAllowed(input: { enabled: boolean; reduced: boolean; wide: boolean; tickets: number }): boolean {
  return input.enabled && input.wide && !input.reduced && input.tickets >= GLIMPSE_MIN_TICKETS
}

export function headerGlimpseGraphReady(nodes: number, links: number): boolean {
  return nodes >= GLIMPSE_MIN_TICKETS && links >= GLIMPSE_MIN_LINKS
}

export interface GlimpseBox { x: number; y: number; width: number; height: number }

// The backdrop spans the header; these measured islands stay completely clear,
// including icons and controls beside the text. Resize and content observers
// keep the mask current when a stage, saved view or docked panel changes shape.
export const GLIMPSE_CLEAR_SELECTOR = '.title-line > *, .description, .head-stats .stat, .head-stats .q-warn, .head-stats .progress-line, .head-stats .group-count, .head-stats .status-count, .header-activity > *, .project-tabs, .project-navigation a, .project-navigation button, .project-navigation label, .view-bar .view-tab, .view-bar .tab, .view-bar .changes, .toolbar-wrap'

export function glimpseOverlaps(a: GlimpseBox, b: GlimpseBox, gap = 0): boolean {
  return a.x < b.x + b.width + gap && a.x + a.width + gap > b.x
    && a.y < b.y + b.height + gap && a.y + a.height + gap > b.y
}

export function glimpseControlSpot(width: number, height: number, boxes: GlimpseBox[], control: { width: number; height: number }): GlimpseBox | null {
  // Prefer the lower right, then search the rest of the free header. A small
  // dock can have no room: never place the controls over the project's text.
  for (let y = height - control.height - 12; y >= 8; y -= 8) {
    for (let x = width - control.width - 12; x >= 8; x -= 8) {
      const spot = { x, y, ...control }
      if (!boxes.some(box => glimpseOverlaps(spot, box, 10))) return spot
    }
  }
  return null
}

export function glimpseTextMask(width: number, height: number, boxes: GlimpseBox[]): string {
  const rects = boxes.map(box => `<rect x="${box.x - 6}" y="${box.y - 6}" width="${box.width + 12}" height="${box.height + 12}" fill="black"/>`).join('')
  // A hard inner cutout proves the bounding-box guarantee; the blurred outer
  // cutout lets surrounding connections fade into each island without edges.
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}"><defs><filter id="feather"><feGaussianBlur stdDeviation="8"/></filter><mask id="clear"><rect width="100%" height="100%" fill="white"/><g filter="url(#feather)">${rects}</g>${rects}</mask></defs><rect width="100%" height="100%" fill="white" mask="url(#clear)"/></svg>`
  return `url("data:image/svg+xml,${encodeURIComponent(svg)}")`
}
