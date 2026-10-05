// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { attentionIdentity, refreshAttentionRelease, type AttentionIdentity, type AttentionResult } from '../src/lib/attention'

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
