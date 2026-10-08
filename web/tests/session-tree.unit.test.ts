// SPDX-License-Identifier: AGPL-3.0-only
// AEON-784 risk: a tree of any depth hides what matters. A problem three levels
// down must reach every ancestor, a folded parent must hide its whole subtree,
// and a filter must keep a deep match with its ancestors as context.
import { describe, expect, it } from 'vitest'
import type { SessionBranch, SessionGroup, SessionStatus } from '../src/lib/agentState'
import { below, jumpTarget, shownBelow, treeRows } from '../src/components/agents/sessionTree'

type View = { session: { id: string }; status: SessionStatus; name: string }
type Branch = SessionBranch<View>
function node(id: string, group: SessionGroup, children: Branch[] = []): Branch {
  const status = { group, tone: 'busy', label: group, state: group === 'needs' ? 'waiting' : group === 'unresponsive' ? 'unresponsive' : group === 'problem' ? 'problem' : 'working' } as SessionStatus
  return { view: { session: { id }, status, name: id }, children, group, liveCount: 1, workingCount: 1, count: 1 }
}
// lead ─┬─ a ── a1 ── a1x (problem)
//       └─ b (asks)
// solo
const tree = () => [node('lead', 'working', [node('a', 'working', [node('a1', 'working', [node('a1x', 'problem')])]), node('b', 'needs')]), node('solo', 'working')]
const all = (b: Branch) => b.children

describe('session tree rows', () => {
  it('lays out any depth with levels, sibling positions and guides, and a fold hides the whole subtree', () => {
    const roots = tree()
    const rows = treeRows(roots, { children: all, isOpen: () => true })
    expect(rows.map(r => [r.branch.view.session.id, r.depth, r.posinset, r.setsize, r.open])).toEqual([
      ['lead', 0, 1, 2, true], ['a', 1, 1, 2, true], ['a1', 2, 1, 1, true], ['a1x', 3, 1, 1, false], ['b', 1, 2, 2, false], ['solo', 0, 2, 2, false],
    ])
    // a1x's lines: lead's first child continues below (b follows), deeper levels end.
    expect(rows[3]!.guides).toEqual([true, false, false])
    expect(rows[3]!.parent?.view.session.id).toBe('a1')
    const folded = treeRows(roots, { children: all, isOpen: b => b.view.session.id !== 'a' })
    expect(folded.map(r => r.branch.view.session.id)).toEqual(['lead', 'a', 'b', 'solo'])
    expect(folded[1]).toMatchObject({ foldable: true, open: false })
    expect(shownBelow(roots[0]!.children[0]!, all)).toBe(2)
  })

  it('rolls a deep problem up to every ancestor and counts asks separately', () => {
    const [lead] = tree()
    const a = lead!.children[0]!
    expect(below(lead!, v => v.name)).toEqual({ problem: 1, ask: 1, names: { problem: ['a1x'], ask: ['b'] } })
    expect(below(a, v => v.name).problem).toBe(1)
    expect(below(a.children[0]!, v => v.name).problem).toBe(1)
    expect(below(a.children[0]!.children[0]!, v => v.name).problem).toBe(0)
  })

  it('names a problem hidden by a fold, including a lost heartbeat, and the ancestors that must open', () => {
    const view = (id: string, state: string, parent: string | null) => ({ session: { id, parent_harness_session_id: parent }, status: { state } })
    const views = [view('lead', 'working', null), view('a', 'working', 'lead'), view('a1x', 'problem', 'a'), view('solo', 'working', null)]
    expect(jumpTarget(views, 'problem')).toEqual({ id: 'a1x', ancestors: ['lead', 'a'] })
    expect(jumpTarget([view('lost', 'unresponsive', 'lead'), view('lead', 'working', null)], 'problem')).toEqual({ id: 'lost', ancestors: ['lead'] })
    expect(jumpTarget(views, 'paused')).toBeNull()
    // A fold drops the problem from the rendered rows, which is why searching the page misses it.
    const folded = treeRows(tree(), { children: all, isOpen: branch => branch.view.session.id !== 'a' })
    expect(folded.map(row => row.branch.view.session.id)).not.toContain('a1x')
  })

  it('chooses the folded live problem the footer counted, not an earlier ended failure', () => {
    // The ended failure is the first child. A walk that ignores liveness selects it, then
    // stopped-child filtering hides that row after its ancestors open, so focus does nothing.
    // The live failure sits under `a`, which a fold hides until those ancestors open.
    const view = (id: string, state: string, parent: string | null, ended = false) => ({
      session: { id, parent_harness_session_id: parent, stopped_at: ended ? '2026-10-01T00:00:00Z' : null, phase: ended ? 'stopped' : 'working' },
      status: { state },
    })
    const views = [
      view('lead', 'working', null),
      view('ended', 'problem', 'lead', true),
      view('a', 'working', 'lead'),
      view('live', 'problem', 'a'),
    ]
    expect(jumpTarget(views, 'problem')).toEqual({ id: 'live', ancestors: ['lead', 'a'] })
    const roots = [node('lead', 'working', [node('ended', 'problem'), node('a', 'working', [node('live', 'problem')])])]
    const folded = treeRows(roots, { children: all, isOpen: branch => branch.view.session.id !== 'a' })
    expect(folded.map(row => row.branch.view.session.id)).toEqual(['lead', 'ended', 'a'])
    const opened = treeRows(roots, { children: all, isOpen: () => true })
    expect(opened.map(row => row.branch.view.session.id)).toContain('live')
  })

  it('keeps a deep match with its ancestors as unfolded context, even where a person folded them', () => {
    const rows = treeRows(tree(), { children: () => [], isOpen: () => false, match: v => v.status.group === 'problem' })
    expect(rows.map(r => [r.branch.view.session.id, r.contextOnly, r.open])).toEqual([
      ['lead', true, true], ['a', true, true], ['a1', true, true], ['a1x', false, false],
    ])
  })
})
