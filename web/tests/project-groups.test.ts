// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  ARCHIVED, NO_GROUP, addGroup, bucket, groupDefs, hiddenOf, newGroupId, nameProblem, placeOf, placementsOf, planMove, readPrefs, removeGroup,
  reorderGroup, replaceGroup, restoreGroup, setHidden, sharedIndex, showsHeaders, stepGroup, toggleCollapsed, withPlacements, type GroupPrefs, type SharedGroup,
} from '../src/lib/projectGroups.ts'
import { DEFAULT_PROJECT_COLUMNS, chosenProjectColumns, fittingProjectColumns, moveProjectColumn, projectColumnOrder, projectProgressPercent, projectProgressTip } from '../src/lib/projectColumns.ts'

const shared: SharedGroup[] = [
  { id: 'b0000000-0000-4000-8000-000000000002', name: 'Internal', position: 1, project_ids: ['p3'] },
  { id: 'a0000000-0000-4000-8000-000000000001', name: 'Clients', position: 0, project_ids: ['p1', 'p2'] },
]
const clients = 's:a0000000-0000-4000-8000-000000000001'
const internal = 's:b0000000-0000-4000-8000-000000000002'
const prefs: GroupPrefs = { groups: [{ id: 'g:paused', name: 'Paused' }, { id: 'g:focus', name: 'Focus' }], place: { p2: 'g:paused', p4: 'g:gone' } }

test('groups read in the person’s order; new ones join before No group and Archived', () => {
  const ids = (p: GroupPrefs) => groupDefs(p, shared).map(d => d.id)
  assert.deepEqual(ids(prefs), ['g:paused', 'g:focus', clients, internal, NO_GROUP, ARCHIVED])
  assert.deepEqual(ids({ ...prefs, order: [ARCHIVED, 'g:focus', NO_GROUP] }), [ARCHIVED, 'g:focus', 'g:paused', clients, internal, NO_GROUP])
  assert.deepEqual(groupDefs({}, []).map(d => d.id), [NO_GROUP, ARCHIVED])
  const defs = groupDefs(prefs, shared)
  assert.deepEqual(defs.map(d => d.kind), ['personal', 'personal', 'shared', 'shared', 'none', 'archived'])
  assert.equal(defs.find(d => d.id === NO_GROUP)!.name, 'No group')
  assert.ok(defs[0].hue && defs[2].hue)
  assert.equal(defs[4].hue, null)
})

test('a project shows in exactly one group: archived, own placement, shared, else none', () => {
  const index = sharedIndex(shared)
  const known = new Set(groupDefs(prefs, shared).map(d => d.id))
  const where = (id: string, archived = false) => placeOf({ id, archived }, prefs, index, known)
  assert.equal(where('p1'), clients)
  assert.equal(where('p2'), 'g:paused') // own placement wins over shared
  assert.equal(where('p3'), internal)
  assert.equal(where('p4'), NO_GROUP) // placed in a group that is gone
  assert.equal(where('p5'), NO_GROUP)
  assert.equal(where('p1', true), ARCHIVED)
  const projects = ['p1', 'p2', 'p3', 'p4', 'p5'].map(id => ({ id, archived: id === 'p5' }))
  const sections = bucket(projects, groupDefs(prefs, shared), p => where(p.id, p.archived))
  assert.deepEqual(sections.map(s => [s.group.id, s.items.map(p => p.id)]), [
    ['g:paused', ['p2']], ['g:focus', []], [clients, ['p1']], [internal, ['p3']], [NO_GROUP, ['p4']], [ARCHIVED, ['p5']],
  ])
})

test('the preference reads leniently and hides Archived by default', () => {
  assert.deepEqual(readPrefs(null), {})
  assert.deepEqual(readPrefs([1]), {})
  assert.deepEqual(readPrefs({ groups: [{ id: 'g:a', name: 'A' }, { id: 'x', name: 'bad id' }, 'junk'], order: ['g:a', 'g:a', 3], place: { p1: 'g:a', p2: 7 } }), { groups: [{ id: 'g:a', name: 'A' }], order: ['g:a'], place: { p1: 'g:a' } })
  assert.deepEqual([...hiddenOf({})], [ARCHIVED])
  assert.deepEqual([...hiddenOf({ hidden: [] })], [])
  assert.deepEqual(setHidden({}, [ARCHIVED], false).hidden, [])
  assert.deepEqual(setHidden({}, ['g:paused'], true).hidden, [ARCHIVED, 'g:paused'])
})

test('headers show once groups exist or archived projects are shown', () => {
  assert.equal(showsHeaders(groupDefs({}, []), hiddenOf({}), 3), false)
  assert.equal(showsHeaders(groupDefs({}, []), new Set(), 0), false)
  assert.equal(showsHeaders(groupDefs({}, []), new Set(), 2), true)
  assert.equal(showsHeaders(groupDefs(prefs, []), hiddenOf({}), 0), true)
})

test('adding, renaming, removing, sharing, hiding, collapsing and reordering groups', () => {
  const defs = groupDefs(prefs, shared)
  const added = addGroup(prefs, { id: 'g:new', name: 'New' }, defs)
  assert.deepEqual(groupDefs(added, shared).map(d => d.id), ['g:paused', 'g:focus', clients, internal, 'g:new', NO_GROUP, ARCHIVED])
  const removed = removeGroup({ ...prefs, hidden: ['g:paused'], collapsed: ['g:paused'] }, 'g:paused')
  assert.deepEqual(removed.groups, [{ id: 'g:focus', name: 'Focus' }])
  assert.deepEqual(removed.place, { p4: 'g:gone' })
  assert.deepEqual(removed.hidden, [])
  const swapped = replaceGroup({ ...prefs, order: ['g:paused', NO_GROUP], hidden: ['g:paused'] }, 'g:paused', clients)
  assert.deepEqual(swapped.order, [clients, NO_GROUP])
  assert.deepEqual(swapped.hidden, [clients])
  assert.equal(swapped.place?.p2, undefined)
  assert.deepEqual(toggleCollapsed(toggleCollapsed({}, 'g:a'), 'g:a').collapsed, [])
  assert.deepEqual(reorderGroup(prefs, defs, ARCHIVED, 'g:paused').order, [ARCHIVED, 'g:paused', 'g:focus', clients, internal, NO_GROUP])
  assert.deepEqual(reorderGroup(prefs, defs, 'g:paused', null).order, ['g:focus', clients, internal, NO_GROUP, ARCHIVED, 'g:paused'])
  assert.deepEqual(stepGroup(prefs, defs, 'g:focus', -1).order, ['g:focus', 'g:paused', clients, internal, NO_GROUP, ARCHIVED])
  assert.equal(stepGroup(prefs, defs, 'g:paused', -1), prefs)
  assert.deepEqual(withPlacements(prefs, { p2: null, p9: 'g:focus' }).place, { p4: 'g:gone', p9: 'g:focus' })
  assert.match(newGroupId(() => 0.5), /^g:[0-9a-z]{8}$/)
})

test('undo puts back one deleted group, or some placements, and leaves later changes alone', () => {
  const before: GroupPrefs = { ...prefs, order: ['g:focus', 'g:paused', NO_GROUP], hidden: ['g:paused'], collapsed: ['g:paused'] }
  // Meanwhile Focus was hidden as well; undoing the delete keeps that.
  const after = setHidden(removeGroup(before, 'g:paused'), ['g:focus'], true)
  const back = restoreGroup(after, before, 'g:paused')
  assert.deepEqual(back.groups, prefs.groups)
  assert.deepEqual(back.order, ['g:focus', 'g:paused', NO_GROUP])
  assert.deepEqual(new Set(back.hidden), new Set(['g:paused', 'g:focus']))
  assert.deepEqual(back.collapsed, ['g:paused'])
  assert.equal(back.place?.p2, 'g:paused')
  assert.equal(restoreGroup(after, before, 'g:nope'), after)
  assert.deepEqual(placementsOf(prefs, ['p2', 'p9']), { p2: 'g:paused', p9: null })
})

test('names are required, at most 60 characters and unique regardless of case', () => {
  const defs = groupDefs(prefs, shared)
  assert.equal(nameProblem('  ', defs), 'A group needs a name.')
  assert.match(nameProblem('x'.repeat(61), defs), /up to 60/)
  assert.equal(nameProblem('clients', defs), 'There is already a group called “clients”.')
  assert.equal(nameProblem('No Group', defs), 'There is already a group called “No Group”.')
  assert.equal(nameProblem('Paused', defs, 'g:paused'), '')
  assert.equal(nameProblem('Later', defs), '')
})

test('moves: archive, own groups, shared groups for admins, and No group', () => {
  const item = (id: string, current: string, extra: Partial<{ archived: boolean; shared: string | null }> = {}) => ({ id, current, archived: false, shared: null, ...extra })
  // Archived archives; leaving it restores.
  assert.deepEqual(planMove([item('p1', clients, { shared: clients })], ARCHIVED, false), { place: {}, assign: null, archive: ['p1'], restore: [], moved: ['p1'] })
  assert.deepEqual(planMove([item('p5', ARCHIVED, { archived: true })], 'g:focus', false), { place: { p5: 'g:focus' }, assign: null, archive: [], restore: ['p5'], moved: ['p5'] })
  // Own groups are placements, for anyone; shared membership stays.
  assert.deepEqual(planMove([item('p1', clients, { shared: clients }), item('p5', NO_GROUP)], 'g:focus', false), { place: { p1: 'g:focus', p5: 'g:focus' }, assign: null, archive: [], restore: [], moved: ['p1', 'p5'] })
  // Shared groups: admins change membership; others may only return a project to its own shared group.
  assert.deepEqual(planMove([item('p3', internal, { shared: internal }), item('p2', 'g:paused', { shared: clients })], clients, true), { place: { p3: null, p2: null }, assign: { group: clients.slice(2), projects: ['p3'] }, archive: [], restore: [], moved: ['p3', 'p2'] })
  assert.deepEqual(planMove([item('p2', 'g:paused', { shared: clients })], clients, false), { place: { p2: null }, assign: null, archive: [], restore: [], moved: ['p2'] })
  assert.deepEqual(planMove([item('p5', NO_GROUP)], clients, false), { refused: 'Only a workspace admin can add projects to a shared group.' })
  // No group: admins take projects out of their shared group; others place them there for themselves.
  assert.deepEqual(planMove([item('p1', clients, { shared: clients })], NO_GROUP, true), { place: { p1: null }, assign: { group: null, projects: ['p1'] }, archive: [], restore: [], moved: ['p1'] })
  assert.deepEqual(planMove([item('p1', clients, { shared: clients })], NO_GROUP, false), { place: { p1: NO_GROUP }, assign: null, archive: [], restore: [], moved: ['p1'] })
  assert.deepEqual(planMove([item('p2', 'g:paused')], NO_GROUP, false), { place: { p2: null }, assign: null, archive: [], restore: [], moved: ['p2'] })
  // Nothing to do.
  assert.deepEqual(planMove([item('p1', clients)], clients, true), { refused: 'It is already in that group.' })
})

test('project columns: Key and Project lead; the rest follow the person’s order and the width', () => {
  assert.deepEqual(chosenProjectColumns(null), DEFAULT_PROJECT_COLUMNS)
  assert.deepEqual(projectColumnOrder({ order: ['activity', 'open'] }), ['activity', 'open', 'doing', 'done', 'progress', 'people'])
  assert.deepEqual(chosenProjectColumns({ order: ['activity', 'open'], visible: ['open', 'people', 'activity'] }), ['activity', 'open', 'people'])
  assert.deepEqual(fittingProjectColumns(1400, { visible: ['open', 'doing', 'done', 'progress', 'people', 'activity'] }), ['open', 'doing', 'done', 'progress', 'people', 'activity'])
  // 368 fixed + 40 + 3×96 + 170 + 112 + 128 = 1106: People steps aside first, then Last activity.
  assert.deepEqual(fittingProjectColumns(1000, { visible: ['open', 'doing', 'done', 'progress', 'people', 'activity'] }), ['open', 'doing', 'done', 'progress', 'activity'])
  assert.deepEqual(fittingProjectColumns(880, null), ['open', 'doing', 'done', 'progress'])
  // Without a saved choice, wide lists add People.
  assert.deepEqual(fittingProjectColumns(1600, null), ['open', 'doing', 'done', 'progress', 'people', 'activity'])
  assert.deepEqual(fittingProjectColumns(1600, { visible: ['open'] }), ['open'])
  assert.deepEqual(moveProjectColumn(['open', 'doing', 'done'], 'doing', 1), ['open', 'done', 'doing'])
  assert.deepEqual(moveProjectColumn(['open', 'doing'], 'open', -1), ['open', 'doing'])
})

test('progress is done out of open, doing and done, so archived work does not lower it', () => {
  // 2 done, 5 open, 3 doing, 1 cancelled, 1 archived: 2/10 is 20, not 2/11.
  assert.equal(projectProgressPercent(5, 3, 2), 20)
  assert.equal(projectProgressPercent(3, 2, 1), 17)
  assert.equal(projectProgressPercent(0, 0, 0), 0)
  assert.equal(projectProgressTip(5, 3, 2, 1), '2 of 10 done · 1 cancelled')
  assert.equal(projectProgressTip(3, 2, 1, 0), '1 of 6 done')
})
