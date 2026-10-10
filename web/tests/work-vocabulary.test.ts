// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { workLevel, workLabel, workIcon, workNoun, vocabularyRows, vocabularyChain, vocabularyLabel, listKeyTarget, workIconChoice } from '../src/lib/workVocabulary.ts'
import { apiParams, filtersFromQuery, filtersToQuery, filtersFromView, viewShape, facetOptions, valueLabel, groupRows } from '../src/lib/ticketList.ts'
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
  assert.equal(facetOptions('type').find(k => k.value === 'work')?.label, 'Ticket')
  assert.equal(workLabel({ kind_slug: 'work', is_leaf: false, depth: 2 }, { revision: 0, leaf: { name: 'Schritt', icon: '' }, levels: [{ name: 'Vorhaben', icon: '' }, { name: 'Abschnitt', icon: '' }] }), 'Abschnitt')
})
test('type facets, selected chips and groups display the workspace leaf name', () => {
  const context = { workName: 'Arbeitsschritt' }
  assert.equal(facetOptions('type', { work: 3 }, [], new Map(), undefined, context).find(k => k.value === 'work')?.label, 'Arbeitsschritt')
  assert.equal(valueLabel('type', 'work', context), 'Arbeitsschritt')
  const row = { kind_slug: 'work' } as ListItem
  assert.equal(groupRows([row], 'type', {}, context)[0].label, 'Arbeitsschritt')
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
