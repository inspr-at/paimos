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
export const GLIMPSE_MIN_TICKETS = 8
export const GLIMPSE_MIN_LINKS = 1
export const GLIMPSE_MIN_WIDTH = 1280

export function headerGlimpseAllowed(input: { enabled: boolean; reduced: boolean; wide: boolean; tickets: number }): boolean {
  return input.enabled && input.wide && !input.reduced && input.tickets >= GLIMPSE_MIN_TICKETS
}

// A viewport override can drop the MediaQueryList change, and the list created
// at mount can keep matches true after the layout shrinks. Stay wide only when
// the live query and the layout width both clear the same threshold.
export function headerGlimpseViewportWide(mediaMatches: boolean, innerWidth: number): boolean {
  return mediaMatches === true && Number.isFinite(innerWidth) && innerWidth >= GLIMPSE_MIN_WIDTH
}

export function headerGlimpseWide(layoutWidth: number): boolean {
  return Number.isFinite(layoutWidth) && layoutWidth >= GLIMPSE_MIN_WIDTH
}

// Chromium drops the MediaQueryList change when Playwright shrinks the viewport
// (setViewportSize 1280 → 390). A missing event must follow the layout width,
// which still moved. A delivered change stays authoritative while innerWidth
// catches up by one pixel, so a stale 1279 sample cannot undo a real widen.
// A queued matches:true from that widen can arrive after the next shrink; a
// layout that has already left the threshold (390, 1278) stays narrow.
// The root's box can lay out at 390 while the query and innerWidth still say
// 1600 (PR #743). That box is narrow. A classic scrollbar, a few pixels under
// a still-wide innerWidth, is not.
const GLIMPSE_WIDTH_CATCHUP = 1
const GLIMPSE_BOX_SLOP = 20
export function nextHeaderGlimpseWide(signal: { mediaMatches: boolean | null; layoutWidth: number; boxWidth?: number }): boolean {
  const box = signal.boxWidth
  if (typeof box === 'number' && Number.isFinite(box) && box < GLIMPSE_MIN_WIDTH - GLIMPSE_WIDTH_CATCHUP) {
    const layout = signal.layoutWidth
    const scrollbar = Number.isFinite(layout) && layout >= GLIMPSE_MIN_WIDTH && layout - box <= GLIMPSE_BOX_SLOP
    if (!scrollbar) return false
  }
  if (signal.mediaMatches === null) return headerGlimpseWide(signal.layoutWidth)
  if (!signal.mediaMatches) return false
  if (Number.isFinite(signal.layoutWidth) && signal.layoutWidth < GLIMPSE_MIN_WIDTH - GLIMPSE_WIDTH_CATCHUP) return false
  return true
}

export function headerGlimpseGraphReady(nodes: number, links: number): boolean {
  return nodes >= GLIMPSE_MIN_TICKETS && links >= GLIMPSE_MIN_LINKS
}

export interface GlimpseBox { x: number; y: number; width: number; height: number }

// The backdrop spans the header; these measured islands stay completely clear,
// including icons and controls beside the text. Resize and content observers
// keep the mask current when a stage, saved view or docked panel changes shape.
export const GLIMPSE_CLEAR_SELECTOR = '.title-line > *, .description, .head-stats .stat, .head-stats .q-warn, .head-stats .head-work, .head-stats .group-count, .head-stats .status-count, .header-activity > *, .project-tabs, .project-navigation a, .project-navigation button, .project-navigation label, .view-bar .view-tab, .view-bar .tab, .view-bar .changes, .toolbar-wrap'

export function glimpseOverlaps(a: GlimpseBox, b: GlimpseBox, gap = 0): boolean {
  return a.x < b.x + b.width + gap && a.x + a.width + gap > b.x
    && a.y < b.y + b.height + gap && a.y + a.height + gap > b.y
}

export function glimpseControlSpot(width: number, height: number, boxes: GlimpseBox[], control: { width: number; height: number }, gap = 10, inset = 12, step = 8): GlimpseBox | null {
  // Prefer the lower right, then search the rest of the free header. A small
  // dock can have no room: never place the controls over the project's text.
  if (control.width <= 0 || control.height <= 0 || width <= 0 || height <= 0 || step <= 0) return null
  const y0 = height - control.height - inset, x0 = width - control.width - inset
  for (let y = y0; y >= 8; y -= step) {
    for (let x = x0; x >= 8; x -= step) {
      const spot = { x, y, width: control.width, height: control.height }
      if (!boxes.some(box => glimpseOverlaps(spot, box, gap))) return spot
    }
  }
  return null
}

// The labelled cluster is about 280px. At 1280 the opening beside the project
// key is narrower than that, and it often misses the 8px grid. An unplaced
// bar keeps its static origin and covers the key. Try the coarse clear spot,
// then any non-overlapping pixel, then the icon row, before giving up.
export function glimpsePlaceControls(width: number, height: number, boxes: GlimpseBox[], sizes: { full: { width: number; height: number }; compact: { width: number; height: number } }): { spot: GlimpseBox; compact: boolean } | null {
  const passes = [
    { size: sizes.full, compact: false, gap: 10, inset: 12, step: 8 },
    { size: sizes.full, compact: false, gap: 0, inset: 8, step: 1 },
    { size: sizes.compact, compact: true, gap: 10, inset: 12, step: 8 },
    { size: sizes.compact, compact: true, gap: 0, inset: 8, step: 1 },
  ]
  for (const pass of passes) {
    const spot = glimpseControlSpot(width, height, boxes, pass.size, pass.gap, pass.inset, pass.step)
    if (spot) return { spot, compact: pass.compact }
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
