// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import type { ListItem } from '../src/lib/api.ts'
import { flattenOutline } from '../src/lib/outline.ts'
import { needsBenefitPrompt } from '../src/lib/doneGate.ts'

test('Outline renders 1800 ancestors and 1800 matching descendants within its budgets', () => {
  const count = 3600
  const nodes = Array.from({ length: count }, (_, i) => ({
    id: String(i), key: `PRJ-${i + 1}`, kind_slug: 'work', title: String(i),
    parent_id: i ? String(i - 1) : 'project', is_leaf: i === count - 1,
  } as ListItem))
  const rows = flattenOutline({
    rootId: 'project', epics: ['0'], loose: [], looseHasMore: false, looseLoading: false,
    node: id => nodes[Number(id)],
    children: id => ({ ids: Number(id) < count - 1 ? [String(Number(id) + 1)] : [], hasMore: false, loading: false }),
    hasChildren: id => Number(id) < count - 1, expanded: () => true,
    dimmed: id => Number(id) < 1800, stats: () => null, noEpicCollapsed: false, createUnder: null,
  })
  assert.equal(rows.length, count)
  for (const [i, entry] of rows.entries()) {
    assert.equal(entry.type, 'row')
    if (entry.type !== 'row') throw new Error('expected a row')
    assert.equal(entry.key, String(i))
    assert.equal(entry.tree.depth, i)
    assert.equal(entry.tree.parentId, i ? String(i - 1) : 'project')
    assert.equal(entry.tree.dimmed, i < 1800)
    assert.equal(entry.tree.guides.length, Math.max(0, i - 1))
    assert.equal(entry.tree.last, true)
  }
})

test('migrated work prompts for benefits on every successful completion transition', () => {
  const fields = { pill_en: 'Clear release notes', pill_de: 'Klare Release Notes', benefit_en: 'Work explains its benefit.', benefit_de: 'Arbeit erklärt ihren Nutzen.' }
  for (const next of ['done', 'accepted', 'delivered']) {
    const work = { kind_slug: 'work', state: 'open', fields: {} }
    assert.equal(needsBenefitPrompt(work, next), true, next)
    assert.equal(needsBenefitPrompt({ ...work, fields: { hide_from_release_notes: true } }, next), true, `hidden ${next}`)
    assert.equal(needsBenefitPrompt({ ...work, fields }, next), false)
    assert.equal(needsBenefitPrompt({ ...work, fields: { ...fields, benefit_de: '' } }, next), true)
    for (const before of ['done', 'accepted', 'delivered']) assert.equal(needsBenefitPrompt({ ...work, state: before }, next), false)
  }
  for (const next of ['open', 'qa', 'cancelled', 'archived']) assert.equal(needsBenefitPrompt({ kind_slug: 'work', state: 'open' }, next), false)
})

test('iterative Outline keeps create rows first and pagination after all descendants', () => {
  const children: Record<string, string[]> = { root: ['first', 'second'], first: ['grandchild'] }
  const entries = flattenOutline({
    rootId: 'project', epics: ['root', 'sibling'], loose: ['loose'], looseHasMore: true, looseLoading: true,
    node: id => ({ id, key: id, title: id, kind_slug: 'work' } as ListItem),
    children: id => children[id] ? { ids: children[id], hasMore: true, loading: false } : null,
    hasChildren: id => !!children[id], expanded: () => true, dimmed: () => false,
    stats: () => null, noEpicCollapsed: false, createUnder: 'root',
  })
  assert.deepEqual(entries.map(e => e.key), ['root', 'create-root', 'first', 'grandchild', 'more-first', 'second', 'more-root', 'sibling', 'no-epic', 'loose', 'more-root'])
  const grandchild = entries.find(e => e.key === 'grandchild')!
  assert.equal(grandchild.type, 'row')
  if (grandchild.type !== 'row') throw new Error('expected grandchild row')
  assert.equal(grandchild.tree.depth, 2)
  assert.deepEqual(grandchild.tree.guides, [true])
  assert.equal(grandchild.tree.last, false)
})
