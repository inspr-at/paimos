// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import type { ListItem } from '../src/lib/api.ts'
import { isJourneyLeaf, parentLeafGroups, workParentId } from '../src/lib/journeyWork.ts'
import { isWorkLeaf } from '../src/lib/workVocabulary.ts'

const row = (id: string, parent: string | null, leaf: boolean, kind = 'work') => ({ id, kind_slug: kind, is_leaf: leaf, parent: parent ? { id: parent, kind_slug: 'work' } : null } as ListItem)
test('canonical journey parent groups count nested leaves once and exclude non-work children', () => {
  const rows = [row('parent', null, false), row('nested', 'parent', false), row('leaf', 'nested', true), row('sibling', 'parent', true), row('memory', 'parent', true, 'memory')]
  assert.deepEqual(rows.filter(isWorkLeaf).map(row => row.id), ['leaf', 'sibling'])
  assert.equal(workParentId(rows[2]), 'nested')
  const groups = parentLeafGroups(rows)
  assert.deepEqual(groups.get('parent')?.map(row => row.id), ['leaf', 'sibling'])
  assert.deepEqual(groups.get('nested')?.map(row => row.id), ['leaf'])
})
test('canonical journey grouping bounds cycles and retains legacy epic projection', () => {
  const cycle = [row('a', 'b', false), row('b', 'a', false), row('leaf', 'a', true)]
  assert.equal(parentLeafGroups(cycle).get('a')?.length, 1)
  const legacy = { id: 'old', kind_slug: 'ticket', epic: { id: 'epic' } } as ListItem
  assert.equal(workParentId(legacy), 'epic')
  assert.equal(isWorkLeaf(legacy), true)
})

test('journey legacy ticket counts retain subdivisions until the canonical migration', () => {
  const parent = { id: 'epic', kind_slug: 'epic' } as ListItem
  const ticket = { id: 'ticket', kind_slug: 'ticket', epic: { id: parent.id } } as ListItem
  const task = { id: 'task', kind_slug: 'task', epic: { id: parent.id } } as ListItem
  assert.deepEqual([parent, ticket, task].filter(isJourneyLeaf).map(row => row.id), ['ticket'])
  assert.deepEqual(parentLeafGroups([parent, ticket, task]).get(parent.id)?.map(row => row.id), ['ticket'])
})
