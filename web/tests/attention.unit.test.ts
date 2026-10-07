// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { attentionFolds, attentionGrouping, orderAttentionGroups, orderAttentionRows, attentionIdentity, refreshAttentionRelease, mergeAttentionRows, collectAttentionGroups, finishAttentionGroups, foldAttentionGroups, ATTENTION_GROUP_CAP, ATTENTION_GROUP_PAGE_CAP, type AttentionGroup, type AttentionItem, type AttentionIdentity, type AttentionPage, type AttentionResult, type AttentionFilters } from '../src/lib/attention'

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

const attentionItem = (event_id: number, kind: AttentionItem['kind'], at: string, project_id = `p-${event_id}`): AttentionItem => ({
 event_id, node_id: `node-${event_id}`, revision: '2026-10-05T08:00:00Z', key: `AEON-${event_id}`, title: `Ticket ${event_id}`,
 project_id, kind, from: 'new', to: 'backlog', reason: 'because', at, editable: true, applicable: true,
})
const attentionPage = (items: AttentionItem[], next_cursor: string | null, counts: AttentionPage['counts'] = {}): AttentionPage => ({
 items, total: items.length, counts, next_cursor, facets: { projects: [], assignees: [] }, facets_truncated: false,
})
const noFilters: AttentionFilters = { kind: '', project_id: '', assignee: '', q: '' }

it('keeps server order across a live cursor and sorts the loaded window only when the cursor ends', () => {
 const first = [attentionItem(1, 'missed', '2026-10-07', 'p'), attentionItem(2, 'triage', '2026-10-01', 'p')]
 const page = attentionPage(first, 'cursor')
 const open = mergeAttentionRows([], page)
 expect(open.map(row => row.event_id)).toEqual([1, 2])
 expect(page.items.map(row => row.event_id)).toEqual([1, 2])
 const done = mergeAttentionRows(open, attentionPage([
  attentionItem(3, 'proposed', '2026-10-06', 'p'), attentionItem(4, 'missed', '2026-10-02', 'p'),
 ], null))
 expect(done.map(row => row.event_id)).toEqual([3, 2, 1, 4])
 expect(mergeAttentionRows([], attentionPage(first, null)).map(row => row.event_id)).toEqual([2, 1])
})

it('builds capped groups from the list cursor and stops at the page cap', async () => {
 const many = Array.from({ length: ATTENTION_GROUP_CAP + 1 }, (_, index) => attentionItem(index + 1, 'triage', '2026-10-07', `p-${index}`))
 const folded = foldAttentionGroups(many, 'project')
 expect(folded.groups).toHaveLength(ATTENTION_GROUP_CAP)
 expect(folded.truncated).toBe(true)
 expect(many).toHaveLength(ATTENTION_GROUP_CAP + 1)

 let calls = 0
 const walked = await collectAttentionGroups('project', async () => {
  calls += 1
  return attentionPage([attentionItem(calls, 'triage', '2026-10-07', `p-${calls}`)], 'next')
 })
 expect(calls).toBe(ATTENTION_GROUP_PAGE_CAP)
 expect(walked.pages).toBe(ATTENTION_GROUP_PAGE_CAP)
 expect(walked.truncated).toBe(true)
 expect(walked.groups).toHaveLength(ATTENTION_GROUP_PAGE_CAP)

 const cursors: Array<string | undefined> = []
 let once = 0
 const capped = await collectAttentionGroups('project', async after => {
  cursors.push(after)
  once += 1
  return attentionPage([1, 2, 3].map(id => attentionItem(id, 'triage', '2026-10-07', `p-${id}`)), 'next')
 }, { cap: 2, pageCap: 4 })
 expect(once).toBe(1)
 expect(cursors).toEqual([undefined])
 expect(capped.groups.map(group => group.id)).toEqual(['p-1', 'p-2'])
 expect(capped.truncated).toBe(true)

 const followed: Array<string | undefined> = []
 await collectAttentionGroups('kind', async after => {
  followed.push(after)
  const id = followed.length
  return attentionPage([attentionItem(id, id === 1 ? 'triage' : 'cancel', '2026-10-07')], id === 1 ? 'page-2' : null)
 }, { pageCap: 2 })
 expect(followed).toEqual([undefined, 'page-2'])
})

it('fills kind totals from the list counts and project names from facets', () => {
 const scanned = foldAttentionGroups([attentionItem(1, 'triage', '2026-10-07', 'p-aeon')], 'kind')
 const counts = attentionPage([attentionItem(1, 'triage', '2026-10-07', 'p-aeon')], null, { triage: 4, cancel: 2 })
 const finished = finishAttentionGroups({ ...scanned, truncated: true }, counts, 'kind', noFilters)
 expect(finished.groups.map(group => [group.id, group.total])).toEqual([['triage', 4], ['cancel', 2]])
 expect(finished.groups.find(group => group.id === 'cancel')?.editable).toBe(1)
 const filtered = finishAttentionGroups(scanned, counts, 'kind', { ...noFilters, kind: 'triage' })
 expect(filtered.groups.map(group => group.id)).toEqual(['triage'])
 const named = finishAttentionGroups(foldAttentionGroups([attentionItem(1, 'triage', '2026-10-07', 'p-aeon')], 'project'), {
  ...attentionPage([], null), facets: { projects: [{ id: 'p-aeon', label: 'AEON Aeon' }], assignees: [] },
 }, 'project', noFilters)
 expect(named.groups[0]).toMatchObject({ id: 'p-aeon', key: 'AEON', title: 'Aeon' })
})
