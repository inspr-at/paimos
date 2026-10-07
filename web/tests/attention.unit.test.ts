// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { attentionFolds, attentionGrouping, orderAttentionGroups, orderAttentionRows, attentionIdentity, refreshAttentionRelease, type AttentionGroup, type AttentionItem, type AttentionIdentity, type AttentionResult } from '../src/lib/attention'

const identity = (event_id: number, extra: Partial<AttentionIdentity> = {}): AttentionIdentity => ({ event_id, node_id: `node-${event_id}`, revision: '2026-10-05T08:00:00Z', release_id: 'release', release_revision: 7, release_project_revision: 4, ...extra })
const receipt: AttentionResult = { event_id: 1, ok: true, release_id: 'release', previous_release_revision: 7, release_revision: 8, previous_release_project_revision: 4, release_project_revision: 5 }
it('refreshes matching sibling identities and sends both resource revisions on the next action', () => {
 const rows = [identity(1), identity(2), identity(3, { release_id: 'other' }), identity(4, { release_revision: 6 }), identity(5, { release_project_revision: 3 })]
 refreshAttentionRelease(rows, receipt)
 expect(rows.map(row => [row.release_revision, row.release_project_revision])).toEqual([[8,5], [8,5], [7,4], [6,4], [7,3]])
 expect(attentionIdentity(rows[1]!)).toMatchObject({ release_revision: 8, release_project_revision: 5 })
 refreshAttentionRelease(rows, { ...receipt, previous_release_revision: 8, release_revision: 9, previous_release_project_revision: 5, release_project_revision: 6 })
 expect(rows[1]).toMatchObject({ release_revision: 9, release_project_revision: 6 })
})
it('ignores failed or incomplete release receipts', () => {
 for (const result of [{ ...receipt, ok: false }, { ...receipt, release_revision: undefined }, { ...receipt, previous_release_project_revision: undefined }]) {
  const row = identity(1); refreshAttentionRelease([row], result)
  expect(row).toEqual(identity(1))
 }
})

it('orders every group by open flags and every row by kind then newest without mutating the visit', () => {
 const group = (id: string, total: number, key = id): AttentionGroup => ({ id, key, total, counts: {}, applicable: total, editable: total, can_manage: false })
 const groups = [group('last', 2), group('beta', 8), group('alpha', 8)]
 expect(orderAttentionGroups(groups, 'project').map(group => group.id)).toEqual(['alpha', 'beta', 'last'])
 expect(groups.map(group => group.id)).toEqual(['last', 'beta', 'alpha'])
 expect(orderAttentionGroups(['missed', 'triage', 'proposed', 'cancel', 'blocked'].map(id => group(id, 1)), 'kind').map(group => group.id)).toEqual(['proposed', 'triage', 'cancel', 'blocked', 'missed'])
 const item = (event_id: number, kind: AttentionItem['kind'], at: string) => ({ event_id, kind, at } as AttentionItem)
 expect(orderAttentionRows([item(3, 'missed', '2026-10-07'), item(1, 'triage', '2026-10-05'), item(2, 'triage', '2026-10-07')]).map(row => row.event_id)).toEqual([2, 1, 3])
})
it('uses saved per-group folds before big-list defaults and opens search matches without changing saved folds', () => {
 const groups = ['a', 'b', 'c'].map(id => ({ id } as AttentionGroup))
 expect(attentionGrouping(undefined)).toBe('project')
 expect(attentionGrouping('kind')).toBe('kind')
 expect(attentionGrouping('none')).toBe('none')
 expect([...attentionFolds(groups, 50)]).toEqual([])
 expect([...attentionFolds(groups, 51)]).toEqual(['b', 'c'])
 const saved = { a: true, b: false }
 expect([...attentionFolds(groups, 51, saved)]).toEqual(['a', 'c'])
 expect([...attentionFolds(groups, 51, saved, true)]).toEqual([])
 expect(saved).toEqual({ a: true, b: false })
})
