// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1210 (design AEON-1196 §3.3–3.4): Sessions grouped by project. Risk: a group moves with
// activity instead of keeping its fixed place, a lead does not head its group, a lead line shows
// for a project that has its lead on the list (or hides one that waits), an ended lead generation
// hides what the current lead does, a count read in part looks complete, the address carries
// unbounded filters, or the folded-groups preference grows past its bound.
import { describe, expect, it } from 'vitest'
import { facetChoices, foldSummary, keyedGroups, matchesFacets, NO_PROJECT, orderProjects, parseFacet, parseGroup, projectGroups, queueHeads, queuedWord, queueNote, MAX_FACET_VALUES } from '../src/components/agents/sessionGroups'
import { foldedProjects, MAX_FOLDED, nextFolded } from '../src/lib/agentsLayout'
import type { ProjectLead } from '../src/lib/lead'

const lead = (state: ProjectLead['state'], session_id: string | null = null): ProjectLead => ({ project_id: 'x', state, reason: '', revision: 1, generation: 1, session_id, process_active: false, automatic_launch_enabled: true })
const root = (id: string, project: string | null, role: 'coordinator' | 'worker' = 'worker', key = project?.toUpperCase() ?? '', stopped_at: string | null = null) =>
  ({ view: { session: { id, project_id: project, role, stopped_at }, projectKey: key, projectTitle: `${key} title` } })
// A lead generation that ended a moment ago: it keeps its place on the list for ten minutes.
const ended = (id: string, project: string) => root(id, project, 'coordinator', project.toUpperCase(), '2026-10-11T10:00:00Z')
const projects = [{ id: 'aeon', key: 'AEON', title: 'Aeon' }, { id: 'ops', key: 'OPS', title: 'Ops' }, { id: 'web', key: 'WEB', title: 'Web' }]

describe('project groups', () => {
  it('keep the person’s own project order, then the rest by key', () => {
    expect(orderProjects([{ id: 'c', key: 'C', title: '' }, { id: 'a', key: 'A', title: '' }, { id: 'b', key: 'B', title: '' }], ['b']).map(p => p.id)).toEqual(['b', 'a', 'c'])
  })

  it('head each group with its lead, whatever order the sessions come in', () => {
    const roots = [root('w1', 'ops'), root('w2', 'aeon'), root('l1', 'aeon', 'coordinator'), root('free', null), root('w3', 'ops')]
    const { groups, loose } = projectGroups({ roots, projects, lead: () => null, queued: () => 0, lines: true })
    expect(groups.map(g => [g.project.key, g.roots.map(r => r.view.session.id), g.leads])).toEqual([['AEON', ['l1', 'w2'], 1], ['OPS', ['w1', 'w3'], 0]])
    expect(loose.map(r => r.view.session.id)).toEqual(['free'])
  })

  it('show a pending lead or the no-lead line only where no lead row heads the group', () => {
    const leads: Record<string, ProjectLead> = { aeon: lead('working', 'l1'), ops: lead('waiting_for_room'), web: lead('none') }
    const queued: Record<string, number> = { aeon: 4, ops: 2, web: 3 }
    const input = { roots: [root('l1', 'aeon', 'worker')], projects, lead: (id: string) => leads[id], queued: (id: string) => queued[id] ?? 0, lines: true }
    const { groups } = projectGroups(input)
    // The lead's own session heads AEON (by the lead's session id, even with a worker role).
    expect(groups.map(g => [g.project.key, g.leads, g.line?.kind ?? null])).toEqual([['AEON', 1, null], ['OPS', 0, 'pending'], ['WEB', 0, 'none']])
    expect(groups[2]!.line).toMatchObject({ kind: 'none', queued: 3 })
    // Nothing queued and no lead: no line. A filter other than project: no lines at all.
    queued.web = 0
    expect(projectGroups(input).groups.map(g => g.project.key)).toEqual(['AEON', 'OPS'])
    expect(projectGroups({ ...input, lines: false }).groups.map(g => g.project.key)).toEqual(['AEON'])
    expect(projectGroups({ ...input, onlyProjects: ['OPS'] }).groups.map(g => g.project.key)).toEqual(['AEON', 'OPS'])
  })

  it('an ended lead generation never hides what the current lead does: starting, removed, paused or running anew', () => {
    const leads: Record<string, ProjectLead> = { aeon: lead('starting') }
    const queued: Record<string, number> = { aeon: 3 }
    const input = { roots: [ended('old', 'aeon')], projects, lead: (id: string) => leads[id], queued: (id: string) => queued[id] ?? 0, lines: true }
    const shape = () => projectGroups(input).groups.map(g => [g.project.key, g.roots.map(r => r.view.session.id), g.line?.kind ?? null])
    // A new lead without a session yet heads the group; the ended generation stays under it.
    expect(shape()).toEqual([['AEON', ['old'], 'pending']])
    // Removed: the queued work says it waits for a lead.
    leads.aeon = lead('none')
    expect(shape()).toEqual([['AEON', ['old'], 'none']])
    // Paused, its own session ended: the lead's state still speaks, not the ended row.
    leads.aeon = lead('paused', 'old')
    expect(shape()).toEqual([['AEON', ['old'], 'pending']])
    // The new generation runs: it heads the group, the ended one follows; no line.
    leads.aeon = lead('working', 'new')
    input.roots = [ended('old', 'aeon'), root('new', 'aeon', 'coordinator')]
    expect(shape()).toEqual([['AEON', ['new', 'old'], null]])
  })

  it('one queue chip per project: on the current lead, never on an ended generation; the first coordinator while the lead is unknown', () => {
    const leads: Record<string, ProjectLead | undefined> = { aeon: lead('working', 'new'), ops: lead('starting') }
    const roots = [ended('old', 'aeon'), root('new', 'aeon', 'coordinator'), ended('gone', 'ops'), root('c1', 'web', 'coordinator'), root('c2', 'web', 'coordinator')]
    expect([...queueHeads(roots, id => leads[id])].sort()).toEqual(['c1', 'new'])
  })

  it('still group sessions of a project the person no longer lists, after the listed ones', () => {
    const { groups } = projectGroups({ roots: [root('z', 'zzz', 'worker', 'ARCH'), root('a', 'aeon')], projects, lead: () => undefined, queued: () => null, lines: true })
    expect(groups.map(g => g.project.key)).toEqual(['AEON', 'ARCH'])
  })

  it('group by a value in a given order, unknown keys after by label', () => {
    const items = ['problem', 'working', 'idle', 'working', 'odd']
    expect(keyedGroups(items, x => x, x => x, ['problem', 'working', 'idle']).map(g => [g.key, g.items.length])).toEqual([['problem', 1], ['working', 2], ['idle', 1], ['odd', 1]])
  })

  it('a count from a queue read in part says it is a lower bound; a queue that can’t be read says so', () => {
    expect([queuedWord(4, false), queuedWord(4, true)]).toEqual(['4', '4+'])
    expect(queueNote({ queueError: '', queuePartial: false })).toBe('')
    expect(queueNote({ queueError: 'queue down', queuePartial: false })).toContain('can’t be read')
    expect(queueNote({ queueError: '', queuePartial: true })).toContain('at least')
  })

  it('a folded lead sums up what it holds', () => {
    expect(foldSummary({ workers: 5, problems: 1, questions: 1 })).toBe('5 workers · 1 problem · 1 question')
    expect(foldSummary({ workers: 1, problems: 0, questions: 2 })).toBe('1 worker · 2 questions')
  })
})

describe('list tools in the address', () => {
  it('read bounded values: each once, short, at most twenty', () => {
    expect(parseFacet('AEON, OPS,AEON,,')).toEqual(['AEON', 'OPS'])
    expect(parseFacet(`ok,${'x'.repeat(65)}`)).toEqual(['ok'])
    expect(parseFacet(Array.from({ length: 40 }, (_, i) => `P${i}`).join(','))).toHaveLength(MAX_FACET_VALUES)
    expect(parseFacet(['AEON'])).toEqual([])
    expect(parseGroup('harness')).toBe('harness')
    expect(parseGroup('nonsense')).toBe('project')
  })

  it('match by project, harness and computer, and count each value', () => {
    const view = (project: string | null, harness: string, host: string, key = project?.toUpperCase() ?? '') => ({ session: { project_id: project, harness, host }, projectKey: key })
    const views = [view('aeon', 'codex', 'build-a'), view('aeon', 'claude', 'build-b'), view(null, 'claude', 'build-b')]
    expect(views.filter(v => matchesFacets(v, { project: ['AEON'], harness: ['claude'], computer: [] }))).toHaveLength(1)
    expect(views.filter(v => matchesFacets(v, { project: [NO_PROJECT], harness: [], computer: [] }))).toHaveLength(1)
    expect(facetChoices(views, 'project', v => v === NO_PROJECT ? 'No project' : v, ['GONE'])).toEqual([
      { value: 'AEON', label: 'AEON', count: 2 }, { value: 'GONE', label: 'GONE', count: 0 }, { value: NO_PROJECT, label: 'No project', count: 1 },
    ])
  })
})

describe('folded groups preference', () => {
  it('keeps well-formed ids once, bounded, and drops projects that are gone', () => {
    expect(foldedProjects({ folded_projects: ['p1', 'p1', 7, 'bad id', 'p2'] })).toEqual(['p1', 'p2'])
    expect(foldedProjects({ folded_projects: 'p1' })).toEqual([])
    expect(foldedProjects({ folded_projects: Array.from({ length: 5000 }, (_, i) => `p${i}`) })).toHaveLength(MAX_FOLDED)
    expect(nextFolded(['p1', 'p2'], ['p3'], ['p1'])).toEqual(['p2', 'p3'])
    expect(nextFolded(['p1', 'gone'], [], [], new Set(['p1']))).toEqual(['p1'])
    expect(nextFolded(Array.from({ length: MAX_FOLDED }, (_, i) => `p${i}`), ['new'], [])).toHaveLength(MAX_FOLDED)
    expect(nextFolded(Array.from({ length: MAX_FOLDED }, (_, i) => `p${i}`), ['new'], []).at(-1)).toBe('new')
  })
})
