// SPDX-License-Identifier: AGPL-3.0-only
// Command palette results: pure assembly of groups from the list API (key and title
// matches, project known), the hybrid search API (words, meaning) and local projects
// and actions. Kept free of Vue for unit tests.
import type { ListItem, WorkNode } from './api.ts'
import type { KnowledgeItem, KnowledgeType } from './knowledge.ts'
import type { Recent } from './recents.ts'
import { isWorkItem } from './workVocabulary.ts'

export interface TicketResult { type: 'ticket'; id: string; key: string; title: string; state: string; kind: string; projectKey: string | null }
export interface ProjectResult { type: 'project'; id: string; key: string; title: string; description: string; archived: boolean }
// searchOnly: offered when a search matches it, not in the empty palette's short list.
export interface ActionResult { type: 'action'; id: string; label: string; hint?: string; icon: string; keys?: string[]; searchOnly?: boolean }
export interface KnowledgeResult { type: 'knowledge'; id: string; kind: KnowledgeType; slug: string; title: string; excerpt: string; archived: boolean; projectKey: string | null }
export type Result = TicketResult | ProjectResult | ActionResult | KnowledgeResult
export interface Group { id: 'recent' | 'tickets' | 'knowledge' | 'views' | 'projects' | 'actions'; label: string; items: Result[] }
export interface PaletteProject { id: string; routeKey: string; title: string; description: string; archived: boolean }

const KEY = /^([a-z][a-z0-9]{1,9})-(\d*)$/i
export function keyQuery(q: string): { prefix: string; number: string; exact: boolean } | null {
  const match = KEY.exec(q.trim())
  return match ? { prefix: match[1].toUpperCase(), number: match[2], exact: match[2].length > 0 } : null
}
export function keyPrefixOf(key: string) { return key.split('-')[0]?.toUpperCase() ?? '' }

function words(q: string) { return q.toLowerCase().split(/\s+/).filter(Boolean) }
function matchesAll(text: string, q: string) { const hay = text.toLowerCase(); return words(q).every(word => hay.includes(word)) }

// Tickets: list matches first (the server orders key-prefix matches first), then
// search hits that are work and not already listed. Exact key matches lead.
export function ticketResults(
  q: string, listed: ListItem[], hits: WorkNode[], workKinds: Map<string, string>,
  projectFor: (key: string) => string | null, scopeKey: string | null, limit = 8,
): TicketResult[] {
  const out: TicketResult[] = []
  const seen = new Set<string>()
  const push = (result: TicketResult) => { if (!seen.has(result.id)) { seen.add(result.id); out.push(result) } }
  for (const item of listed) {
    if (!isWorkItem(item)) continue
    push({ type: 'ticket', id: item.id, key: item.key, title: item.title, state: item.state, kind: item.kind_slug, projectKey: projectFor(item.key) })
  }
  for (const node of hits) {
    const kind = workKinds.get(node.kind_id)
    if (!kind || node.deleted_at) continue
    const projectKey = projectFor(node.key)
    if (scopeKey && projectKey !== scopeKey) continue
    push({ type: 'ticket', id: node.id, key: node.key, title: node.title, state: node.state, kind, projectKey })
  }
  const exact = q.trim().toUpperCase()
  out.sort((a, b) => Number(b.key === exact) - Number(a.key === exact))
  return out.slice(0, limit)
}

export function projectResults(q: string, projects: PaletteProject[], limit = 5): ProjectResult[] {
  const needle = q.trim()
  if (!needle) return []
  const exact = needle.toUpperCase()
  return projects
    .filter(project => project.routeKey.toUpperCase() === exact || matchesAll(`${project.routeKey} ${project.title} ${project.description}`, needle))
    .filter(project => !project.archived || project.routeKey.toUpperCase() === exact)
    .sort((a, b) => Number(b.routeKey.toUpperCase() === exact) - Number(a.routeKey.toUpperCase() === exact) || Number(b.routeKey.toUpperCase().startsWith(exact)) - Number(a.routeKey.toUpperCase().startsWith(exact)))
    .slice(0, limit)
    .map(project => ({ type: 'project', id: project.id, key: project.routeKey, title: project.title, description: project.description, archived: project.archived }))
}

export function actionResults(q: string, actions: ActionResult[]): ActionResult[] {
  const needle = q.trim()
  return needle ? actions.filter(action => matchesAll(`${action.label} ${action.hint ?? ''}`, needle)) : actions.filter(action => !action.searchOnly)
}

// A project's saved views, found by name; all of them in the empty palette.
export function viewResults(q: string, views: { id: string; name: string; shared: boolean; mine: boolean; isDefault: boolean }[], limit = 6): ActionResult[] {
  const needle = q.trim()
  return views
    .filter(view => !needle || matchesAll(`${view.name} view`, needle))
    .slice(0, limit)
    .map(view => ({
      type: 'action', id: `view:${view.id}`, label: view.name, icon: 'bookmark',
      hint: [view.mine ? (view.shared ? 'Your view, shared' : 'Your view') : 'Shared view', view.isDefault ? 'opens first' : ''].filter(Boolean).join(' · '),
    }))
}

export function recentResults(recents: Recent[], scopeKey: string | null): Result[] {
  return recents
    .filter(recent => !scopeKey || (recent.type === 'ticket' ? recent.projectKey === scopeKey : recent.key === scopeKey))
    .map(recent => recent.type === 'ticket'
      ? { type: 'ticket', id: `recent-${recent.key}`, key: recent.key, title: recent.title, state: recent.state, kind: recent.kind, projectKey: recent.projectKey }
      : { type: 'project', id: `recent-${recent.key}`, key: recent.key, title: recent.title, description: '', archived: false })
}

// Knowledge: runbooks, guidelines and memory whose title, slug or text match, archived
// ones last; the project comes from the entry's nearest project.
export function knowledgeResults(items: KnowledgeItem[], routeKeyOf: (projectId: string) => string | null, scopeKey: string | null, limit = 5): KnowledgeResult[] {
  return items
    .map(item => ({ type: 'knowledge' as const, id: `k-${item.id}`, kind: item.type, slug: item.slug, title: item.title, excerpt: item.excerpt, archived: item.status === 'archived', projectKey: item.project ? routeKeyOf(item.project.id) : null }))
    .filter(result => result.projectKey && (!scopeKey || result.projectKey === scopeKey))
    .sort((a, b) => Number(a.archived) - Number(b.archived))
    .slice(0, limit)
}

// Group order: a bare project key puts Projects first; otherwise Tickets lead, then Knowledge.
export function assemble(q: string, parts: { recent: Result[]; tickets: TicketResult[]; knowledge?: KnowledgeResult[]; projects: ProjectResult[]; actions: ActionResult[]; views?: ActionResult[] }): Group[] {
  const needle = q.trim()
  const views = parts.views ?? []
  if (!needle) {
    return [
      { id: 'recent', label: 'Recent', items: parts.recent },
      { id: 'views', label: 'Views', items: views },
      { id: 'actions', label: 'Actions', items: parts.actions },
    ].filter(group => group.items.length) as Group[]
  }
  const projectFirst = parts.projects.some(project => project.key.toUpperCase() === needle.toUpperCase())
  const groups: Group[] = [
    { id: 'tickets', label: 'Tickets', items: parts.tickets },
    { id: 'knowledge', label: 'Knowledge', items: parts.knowledge ?? [] },
    { id: 'views', label: 'Views', items: views },
    { id: 'projects', label: 'Projects', items: parts.projects },
    { id: 'actions', label: 'Actions', items: parts.actions },
  ]
  if (projectFirst) groups.unshift(groups.splice(groups.findIndex(group => group.id === 'projects'), 1)[0])
  return groups.filter(group => group.items.length)
}
