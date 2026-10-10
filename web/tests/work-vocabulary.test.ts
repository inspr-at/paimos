// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { workLevel, workLabel, workIcon, workNoun, vocabularyRows, vocabularyChain, vocabularyLabel, listKeyTarget, workIconChoice } from '../src/lib/workVocabulary.ts'
import { apiParams, filtersFromQuery, filtersToQuery, filtersFromView, viewShape, facetOptions, valueLabel, groupRows, offeredDimensions, sameListState, toggleMembers, EMPTY_FILTERS } from '../src/lib/ticketList.ts'
import type { ListItem } from '../src/lib/api.ts'
test('workspace names depend on leaf shape first, then project-relative depth', () => {
  const vocabulary = { revision: 0, leaf: { name: 'Step', icon: 'check' }, levels: [{ name: 'Feature', icon: 'tree' }] }
  assert.deepEqual(workLevel(vocabulary, true, 20), { name: 'Step', icon: 'check' })
  assert.deepEqual(workLevel(vocabulary, false, 1), { name: 'Feature', icon: 'tree' })
  assert.equal(workLevel(vocabulary, false, 2).name, 'Story')
  assert.equal(workLevel(vocabulary, false, 30).name, 'Level 30')
  assert.equal(workLabel({ kind_slug: 'work', level_name: 'Vorhaben' }), 'Vorhaben')
  assert.equal(workIcon({ kind_slug: 'work', is_leaf: false, level_icon: '<svg>' }), 'epic')
})
test('Parents / Leaves and depth survive links, saved views and API translation', () => {
  const filters = filtersFromQuery({ shape: 'leaf,!parent', depth: '2,!3', type: 'epic' })
  const query = filtersToQuery(filters)
  assert.equal(query.shape, 'leaf,!parent'); assert.equal(query.depth, '2,!3')
  const saved = viewShape(filters)
  const restored = filtersFromView({ id: '12345678-1234-4234-8234-123456789abc', ...saved })
  assert.deepEqual(restored.shape, filters.shape); assert.deepEqual(restored.depth, filters.depth)
  assert.deepEqual(apiParams('project', restored).shape, ['leaf', '!parent'])
  assert.deepEqual(apiParams('project', restored).depth, ['2', '!3'])
  assert.deepEqual(filtersFromQuery({ depth: '0,50001,garbage', shape: 'container' }).depth, [])
})
test('internal work types retain default names and custom spelling', () => {
  assert.equal(workLabel({ kind_slug: 'work' }), 'Ticket')
  assert.equal(workLabel({ kind_slug: 'work', is_leaf: false, depth: 1 }), 'Epic')
  assert.equal(workLabel({ kind_slug: 'task' }), 'Task')
  assert.equal(workNoun('Ticket'), 'ticket')
  assert.equal(workNoun('Arbeitsschritt'), 'Arbeitsschritt')
  assert.equal(facetOptions('type').find(k => k.value === 'leaf')?.label, 'Ticket')
  assert.equal(workLabel({ kind_slug: 'work', is_leaf: false, depth: 2 }, { revision: 0, leaf: { name: 'Schritt', icon: '' }, levels: [{ name: 'Vorhaben', icon: '' }, { name: 'Abschnitt', icon: '' }] }), 'Abschnitt')
})
test('type facets, selected chips and groups display the workspace leaf name', () => {
  const context = { workName: 'Arbeitsschritt', vocabulary: { revision: 1, leaf: { name: 'Arbeitsschritt', icon: '' }, levels: [] } }
  assert.equal(facetOptions('type', { leaf: 3 }, [], new Map(), undefined, context).find(k => k.value === 'leaf')?.label, 'Arbeitsschritt')
  assert.equal(valueLabel('type', 'leaf', context), 'Arbeitsschritt')
  const row = { kind_slug: 'work' } as ListItem
  assert.equal(groupRows([row], 'type', {}, context)[0].label, 'Arbeitsschritt')
})
// AEON-974: one Type filter in the workspace's names, over levels (leaf, else a parent's depth).
test('Type offers each level by its workspace name; levels sharing a name are one option', () => {
  const vocabulary = { revision: 2, leaf: { name: 'Aufgabe', icon: 'check' }, levels: [{ name: 'Vorhaben', icon: 'tree' }, { name: '', icon: '' }, { name: 'Aufgabe', icon: '' }] }
  const options = facetOptions('type', { 1: 2, 2: 5, 3: 1, 4: 1, leaf: 9 }, [], new Map(), undefined, { vocabulary })
  // Top level first, the leaf's name last; Level 3 shares the leaf's name and counts with it.
  assert.deepEqual(options.map(o => [o.label, o.count, o.members]), [['Vorhaben', 2, ['1']], ['Story', 5, ['2']], ['Aufgabe', 10, ['3', 'leaf']], ['Level 4', 1, ['4']]])
  assert.equal(options[0].icon, 'tree')
  // Without counts the menu still offers the top level and the leaf in default words.
  assert.deepEqual(facetOptions('type').map(o => o.label), ['Epic', 'Ticket'])
  // A shared name goes on, off and into "not" for all of its levels at once.
  assert.deepEqual(toggleMembers([], ['3', 'leaf'], 'in'), ['3', 'leaf'])
  assert.deepEqual(toggleMembers(['3', 'leaf', '1'], ['3', 'leaf'], 'in'), ['1'])
  assert.deepEqual(toggleMembers(['3', 'leaf'], ['3', 'leaf'], 'out'), ['!3', '!leaf'])
  assert.equal(valueLabel('type', '3', { vocabulary }), 'Aufgabe')
  assert.equal(valueLabel('type', '1'), 'Epic')
  const params = apiParams('p', filtersFromQuery({ type: '2,!leaf' }))
  assert.deepEqual([params.level, params.kind], [['2', '!leaf'], ['work', 'ticket', 'task', 'epic']])
})
test('links and saved views with Legacy type or Parents / Leaves read as Type', () => {
  const view = (filters: Record<string, string>) => filtersFromView({ id: '12345678-1234-4234-8234-123456789abc', filters, sort_keys: [], group_by: 'none', columns: [] })
  // Legacy kinds: epic is the top level, ticket and task the leaf; work was every kind.
  assert.deepEqual(view({ type: 'epic,task' }).type, ['1', 'leaf'])
  assert.deepEqual(view({ type: '!ticket,work' }).type, ['!leaf'])
  assert.deepEqual(filtersFromQuery({ type: 'work' }).type, [])
  // Only leaves, or parents at named depths, fold into Type.
  assert.deepEqual([view({ shape: 'leaf' }).type, view({ shape: 'leaf' }).shape], [['leaf'], []])
  assert.deepEqual(filtersFromQuery({ shape: '!parent' }).type, ['leaf'])
  const parents = filtersFromQuery({ shape: 'parent', depth: '1,2' })
  assert.deepEqual([parents.type, parents.shape, parents.depth], [['1', '2'], [], []])
  // Anything Type cannot say stays as it was, offered while applied.
  const kept = filtersFromQuery({ shape: 'parent' })
  assert.deepEqual([kept.type, kept.shape], [[], ['parent']])
  assert.deepEqual(filtersFromQuery({ shape: 'leaf', depth: '2' }).shape, ['leaf'])
  assert.deepEqual(filtersFromQuery({ type: '1', shape: 'leaf' }).shape, ['leaf'])
  assert.ok(offeredDimensions(kept).some(d => d.key === 'shape'))
  // An open sheet keeps a section it showed, also once its last value is cleared.
  assert.ok(offeredDimensions(EMPTY_FILTERS, undefined, ['shape']).some(d => d.key === 'shape'))
  assert.deepEqual(offeredDimensions(EMPTY_FILTERS).map(d => d.title), ['Status', 'Priority', 'Assignee', 'Type', 'Labels', 'Human check', 'Parent', 'Cost unit', 'Imported release'])
  // A mapped view is unchanged when it loads (no "modified" dot), and saves the new words.
  assert.equal(sameListState(view({ type: 'epic' }), filtersFromQuery({ type: '1' })), true)
  assert.equal(filtersToQuery(view({ shape: 'leaf' })).type, 'leaf')
  assert.deepEqual(filtersFromQuery({ type: '0,50001,leaf,bogus,!' }).type, ['leaf'])
})
test('settings rows read top-down: top level first, the leaf last, with defaults per depth', () => {
  const vocabulary = { revision: 1, leaf: { name: '', icon: '' }, levels: [{ name: 'Vorhaben', icon: 'tree' }, { name: '', icon: '' }, { name: '', icon: '' }] }
  const rows = vocabularyRows(vocabulary)
  assert.deepEqual(rows.map(r => r.label), ['Top level', 'Level 2', 'Level 3', 'Leaf (work item)'])
  assert.deepEqual(rows.map(r => r.depth), [0, 1, 2, 3])
  assert.deepEqual(rows.map(r => r.placeholder), ['Epic', 'Story', 'Level 3', 'Ticket'])
  assert.deepEqual(rows.map(r => r.fallback), ['epic', 'layers', 'layers', 'ticket'])
  // Rows edit the stored objects: levels[0] stays the top level.
  assert.equal(rows[0].level, vocabulary.levels[0]); assert.equal(rows[3].level, vocabulary.leaf)
  assert.equal(vocabularyLabel(0), 'Top level'); assert.equal(vocabularyLabel(8), 'Level 9')
})
test('the preview chain names every configured level top-down, then the leaf', () => {
  const vocabulary = { revision: 1, leaf: { name: 'Schritt', icon: 'check' }, levels: [{ name: 'Vorhaben', icon: 'tree' }, { name: '', icon: '' }, { name: 'Teil', icon: '' }, { name: '', icon: '' }] }
  assert.deepEqual(vocabularyChain(vocabulary), [{ name: 'Vorhaben', icon: 'tree' }, { name: 'Story', icon: 'layers' }, { name: 'Teil', icon: 'layers' }, { name: 'Level 4', icon: 'layers' }, { name: 'Schritt', icon: 'check' }])
  assert.deepEqual(vocabularyChain({ revision: 0, leaf: { name: '', icon: '' }, levels: [] }), [{ name: 'Ticket', icon: 'ticket' }])
})
test('an unknown stored icon draws the level default', () => {
  assert.equal(workIconChoice('box', 'layers'), 'box')
  assert.equal(workIconChoice('', 'epic'), 'epic')
  assert.equal(workIconChoice('<svg>', 'epic'), 'epic')
})
test('list keys move within the options and stop at the ends', () => {
  assert.equal(listKeyTarget('ArrowDown', -1, 9), 0)
  assert.equal(listKeyTarget('ArrowDown', 3, 9), 4)
  assert.equal(listKeyTarget('ArrowDown', 8, 9), 8)
  assert.equal(listKeyTarget('ArrowUp', 0, 9), 0)
  assert.equal(listKeyTarget('ArrowUp', 4, 9), 3)
  assert.equal(listKeyTarget('Home', 4, 9), 0); assert.equal(listKeyTarget('End', 4, 9), 8)
  assert.equal(listKeyTarget('Enter', 4, 9), undefined); assert.equal(listKeyTarget('ArrowDown', 0, 0), undefined)
})
