// SPDX-License-Identifier: AGPL-3.0-only
// Sessions grouped and narrowed for the Agents page (AEON-1210, design AEON-1196 §3.3–3.4).
// By default each project is one group in a fixed order, its lead heading it; a project
// with queued work and no lead is one slim line. Display offers grouping by lead,
// harness, state, computer or none; Filter narrows by project, harness and computer.
// Free of Vue so it can be unit tested; the list renders what this decides.
import type { ProjectLead } from '../../lib/lead'

export type GroupBy = 'project' | 'lead' | 'harness' | 'state' | 'computer' | 'none'
export const GROUP_BY: readonly { value: GroupBy; label: string }[] = [
  { value: 'project', label: 'Project' }, { value: 'lead', label: 'Lead' }, { value: 'harness', label: 'Harness' },
  { value: 'state', label: 'State' }, { value: 'computer', label: 'Computer' }, { value: 'none', label: 'None' },
]
export const DEFAULT_GROUP: GroupBy = 'project'
export const parseGroup = (raw: unknown): GroupBy => GROUP_BY.some(g => g.value === raw) ? raw as GroupBy : DEFAULT_GROUP
/** What the Display button adds to its name when the grouping is not the default. */
export const groupWord = (group: GroupBy) => group === 'project' ? '' : group === 'none' ? 'no groups' : `by ${group}`

// ---------- Facets: Filter › Project, Harness, Computer (the state filter is the head count's) ----------
export type FacetKey = 'project' | 'harness' | 'computer'
export const FACET_KEYS: readonly FacetKey[] = ['project', 'harness', 'computer']
export const FACET_TITLE: Record<FacetKey, string> = { project: 'Project', harness: 'Harness', computer: 'Computer' }
export type Facets = Record<FacetKey, string[]>
/** The project value of a session that belongs to no project (personal and workspace sessions). */
export const NO_PROJECT = 'none'
// Bounds for what the address carries (?project=, ?harness=, ?computer=): read before any work.
export const MAX_FACET_VALUES = 20
export const MAX_FACET_CHARS = 64
const MAX_FACET_PARAM = 1500
export const emptyFacets = (): Facets => ({ project: [], harness: [], computer: [] })
export function parseFacet(raw: unknown): string[] {
  if (typeof raw !== 'string' || !raw) return []
  const out: string[] = []
  for (const part of raw.slice(0, MAX_FACET_PARAM).split(',')) {
    const value = part.trim()
    if (!value || value.length > MAX_FACET_CHARS || out.includes(value)) continue
    out.push(value)
    if (out.length === MAX_FACET_VALUES) break
  }
  return out
}
export const facetParam = (values: readonly string[]) => values.length ? values.join(',') : undefined
export const facetsActive = (facets: Facets) => FACET_KEYS.some(key => facets[key].length > 0)
export const sameFacets = (a: Facets, b: Facets) => FACET_KEYS.every(key => a[key].length === b[key].length && a[key].every((value, i) => value === b[key][i]))

interface Facetable { session: { project_id?: string | null; harness: string; host: string }; projectKey: string }
export function facetValue(view: Facetable, key: FacetKey): string {
  if (key === 'project') return view.session.project_id ? view.projectKey || view.session.project_id : NO_PROJECT
  return key === 'harness' ? view.session.harness : view.session.host
}
export function matchesFacets(view: Facetable, facets: Facets): boolean {
  return FACET_KEYS.every(key => !facets[key].length || facets[key].includes(facetValue(view, key)))
}
export interface FacetChoice { value: string; label: string; count: number }
const collator = new Intl.Collator('en', { numeric: true, sensitivity: 'base' })
/** One choice per value the sessions carry, with its count; a chosen value no session carries stays, so it can be cleared. */
export function facetChoices(views: readonly Facetable[], key: FacetKey, label: (value: string) => string, selected: readonly string[]): FacetChoice[] {
  const counts = new Map<string, number>()
  for (const view of views) { const value = facetValue(view, key); if (value) counts.set(value, (counts.get(value) ?? 0) + 1) }
  for (const value of selected) if (!counts.has(value)) counts.set(value, 0)
  return [...counts].map(([value, count]) => ({ value, label: label(value), count }))
    .sort((a, b) => Number(a.value === NO_PROJECT) - Number(b.value === NO_PROJECT) || collator.compare(a.label, b.label) || a.value.localeCompare(b.value))
}

// ---------- Projects in their fixed order ----------
export interface ProjectRef { id: string; key: string; title: string }
/** The person's own order of projects (their Custom sort on Projects), then the rest by key: activity never moves a group. */
export function orderProjects<P extends ProjectRef>(projects: readonly P[], custom: readonly string[]): P[] {
  const rank = new Map(custom.map((id, i) => [id, i]))
  const at = (p: P) => rank.get(p.id) ?? Number.MAX_SAFE_INTEGER
  return [...projects].sort((a, b) => at(a) - at(b) || collator.compare(a.key, b.key) || a.id.localeCompare(b.id))
}

// ---------- Group by Project (the default) ----------
interface Rootish { view: { session: { id: string; project_id?: string | null; role?: string }; projectKey: string; projectTitle: string } }
/** A project's lead that has no session on this list (starting, waiting for room, paused, can't start), or queued work and no lead. */
export type LeadLine = { kind: 'pending'; lead: ProjectLead } | { kind: 'none'; lead: ProjectLead; queued: number }
export interface ProjectGroup<B> { project: ProjectRef; roots: B[]; leads: number; line: LeadLine | null }
export interface ProjectGrouping<B> {
  /** Live family roots in list order. */
  roots: readonly B[]
  /** Active projects in their fixed order: they may show a lead line without sessions. */
  projects: readonly ProjectRef[]
  lead: (projectId: string) => ProjectLead | null | undefined
  queued: (projectId: string) => number | null
  /** Lead lines show while nothing narrows the list, or for the projects a project filter keeps. */
  lines: boolean
  onlyProjects?: readonly string[]
}
export const isLeadRoot = (root: Rootish, lead: ProjectLead | null | undefined) => root.view.session.role === 'coordinator' || (!!lead?.session_id && lead.session_id === root.view.session.id)
export function projectGroups<B extends Rootish>(input: ProjectGrouping<B>): { groups: ProjectGroup<B>[]; loose: B[] } {
  const byProject = new Map<string, B[]>(), loose: B[] = [], refs = new Map<string, ProjectRef>()
  for (const root of input.roots) {
    const id = root.view.session.project_id
    if (!id) { loose.push(root); continue }
    const list = byProject.get(id)
    if (list) list.push(root)
    else { byProject.set(id, [root]); refs.set(id, { id, key: root.view.projectKey || id, title: root.view.projectTitle }) }
  }
  // Projects this person no longer lists (archived, say) still group their sessions, after the others.
  const known = new Set(input.projects.map(p => p.id))
  const extra = [...refs.values()].filter(p => !known.has(p.id)).sort((a, b) => collator.compare(a.key, b.key) || a.id.localeCompare(b.id))
  const groups: ProjectGroup<B>[] = []
  for (const project of [...input.projects, ...extra]) {
    const lead = input.lead(project.id)
    const own = byProject.get(project.id) ?? []
    // The lead heads its group; the project's other sessions follow in list order.
    const leads = own.filter(root => isLeadRoot(root, lead)), rest = own.filter(root => !isLeadRoot(root, lead))
    let line: LeadLine | null = null
    if (!leads.length && lead && input.lines && (!input.onlyProjects?.length || input.onlyProjects.includes(project.key))) {
      const queued = input.queued(project.id)
      if (lead.state !== 'none') line = { kind: 'pending', lead }
      else if (queued) line = { kind: 'none', lead, queued }
    }
    if (own.length || line) groups.push({ project: known.has(project.id) ? project : refs.get(project.id) ?? project, roots: [...leads, ...rest], leads: leads.length, line })
  }
  return { groups, loose }
}

/** Groups keyed by a value of each item, in a given order of keys (unknown keys after, by label). */
export function keyedGroups<T>(items: readonly T[], keyOf: (item: T) => string, label: (key: string) => string, order: readonly string[] = []): { key: string; label: string; items: T[] }[] {
  const map = new Map<string, T[]>()
  for (const item of items) { const key = keyOf(item); const list = map.get(key); if (list) list.push(item); else map.set(key, [item]) }
  const rank = new Map(order.map((key, i) => [key, i]))
  const at = (key: string) => rank.get(key) ?? Number.MAX_SAFE_INTEGER
  return [...map].map(([key, list]) => ({ key, label: label(key), items: list }))
    .sort((a, b) => at(a.key) - at(b.key) || collator.compare(a.label, b.label) || a.key.localeCompare(b.key))
}

// ---------- A folded lead says what it holds ----------
export interface FoldSummary { workers: number; problems: number; questions: number }
export const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`
/** “5 workers · 1 problem · 1 question”: the line a folded lead adds to what it is doing. */
export function foldSummary({ workers, problems, questions }: FoldSummary): string {
  return [plural(workers, 'worker'), problems ? plural(problems, 'problem') : '', questions ? plural(questions, 'question') : ''].filter(Boolean).join(' · ')
}
