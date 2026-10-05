// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { workLevel, workLabel, workIcon, workNoun } from '../src/lib/workVocabulary.ts'
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
