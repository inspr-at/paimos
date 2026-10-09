// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'
import { fixtures, mockWork, type MockOptions } from './work-fixtures'
import type { TicketGraph, TicketGraphLink, TicketGraphNode } from '../src/lib/ticketGraph'

const workstreams = [
  ['Reliable deployments', 'Preview rollout changes', 'Check artifact signatures', 'Stage canary instances', 'Measure startup health', 'Pause a failing rollout', 'Keep rollback artifacts', 'Compare host revisions', 'Resume interrupted deploys', 'Record deploy evidence', 'Explain rollout failures', 'Clean expired candidates'],
  ['Fleet visibility', 'Collect service heartbeats', 'Show host availability', 'Group hosts by region', 'Surface expiring certificates', 'Track memory pressure', 'Summarize disk growth', 'Link host activity', 'Inspect offline agents', 'Filter noisy probes', 'Export uptime history', 'Refresh fleet inventory'],
  ['Access and identity', 'Enroll a new operator', 'Scope agent credentials', 'Rotate host certificates', 'Audit role changes', 'Expire temporary access', 'Review project membership', 'Explain denied actions', 'Revoke lost devices', 'Confirm sensitive changes', 'Show active sessions', 'Validate tenant boundaries'],
  ['Operator workspace', 'Keep search in the URL', 'Open tickets beside work', 'Connect related incidents', 'Save project filters', 'Navigate without a mouse', 'Reduce motion on request', 'Read long audit entries', 'Improve mobile navigation', 'Explain empty results', 'Remember panel widths', 'Tune dark theme contrast'],
  ['Release confidence', 'Track acceptance evidence', 'Check database migrations', 'Review recovery steps', 'Exercise retry behavior', 'Verify event ordering', 'Compare release candidates', 'Publish release notes', 'Keep test tenants isolated', 'Bound background jobs', 'Capture browser regressions', 'Confirm production health'],
]

// 60 nodes: five epics, each with eleven plausible tickets and mixed relations.
export function ticketGraphWorld() {
  const work = fixtures()
  // AEON-1042: the header glimpse is a developer opt-in; graph worlds turn it on.
  work.preferences['developer-ui'] = { show_header_graph: true }
  work.nodes = work.nodes.filter(n => n.project !== 'p-pharos')
  const nodes: TicketGraphNode[] = [], links: TicketGraphLink[] = []
  workstreams.forEach((titles, cluster) => titles.forEach((title, index) => {
    const n = cluster * 12 + index + 100
    const id = `tg-${n}`, parent = index ? `tg-${cluster * 12 + 100}` : null
    const category = index && index % 4 === 0 ? 'done' : index % 3 === 0 ? 'doing' : 'open'
    const status = category === 'done' ? 'done' : category === 'doing' ? 'in_progress' : 'backlog'
    const priority = index % 3 === 0 ? 'high' : index % 3 === 1 ? 'medium' : null
    nodes.push({ id, key: `PHAROS-${n}`, title, type: index ? 'ticket' : 'epic', status, status_category: category, priority, parent_id: parent, release_id: null, updated_at: '2026-09-26T12:00:00Z', link_count: 0 })
    work.nodes.push({ id, key: `PHAROS-${n}`, title, kind_slug: index ? 'ticket' : 'epic', state: status, body: `## Acceptance\n\n- [ ] ${title} works for every project member.`, fields: { priority }, parent_id: parent ?? 'p-pharos', project: 'p-pharos', created_at: '2026-09-20T09:00:00Z', updated_at: '2026-09-26T12:00:00Z' })
    if (parent) links.push({ source: parent, target: id, kind: 'parent' })
    if (index > 1) links.push({ source: `tg-${n - 1}`, target: id, kind: (['blocks', 'relates', 'implements', 'duplicates'] as const)[index % 4] })
    if (cluster && index === 2) links.push({ source: `tg-${n - 12}`, target: id, kind: 'blocks' })
  }))
  for (const link of links) for (const id of [link.source, link.target]) nodes.find(n => n.id === id)!.link_count++
  return { work, graph: { nodes, links, truncated: false } as TicketGraph }
}
export async function mockTicketGraph(page: Page, world = ticketGraphWorld(), options: MockOptions = {}) {
  await mockWork(page, world.work, options)
  const calls: URLSearchParams[] = []
  await page.route('**/api/tickets/graph?*', route => {
    const query = new URL(route.request().url()).searchParams; calls.push(query)
    const nodes = world.graph.nodes.filter(n => query.get('include_closed') === 'true' || n.status_category !== 'done')
    const ids = new Set(nodes.map(n => n.id))
    return route.fulfill({ json: { nodes, links: world.graph.links.filter(l => ids.has(l.source) && ids.has(l.target)), truncated: world.graph.truncated } })
  })
  return { ...world, calls }
}
