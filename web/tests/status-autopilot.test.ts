// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildTimeline } from '../src/lib/activity.ts'
import type { ActivityItem } from '../src/lib/api.ts'

test('automatic changes keep their individual reasons and Undo identifiers', () => {
  const base: ActivityItem = { id: '41', type: 'change', at: '2026-10-01T00:00:00Z', author: { id: 'system', name: 'System', automatic: true, job: 'status-autopilot', reason: 'Stalled' }, changes: [{ field: 'status', from: 'in_progress', to: 'open' }], automatic_change: { event_id: 41, node_id: 'ticket', key: 'ORB-1', title: 'A ticket', actor: 'Status autopilot', rule: 'progress', reason: 'Stalled', from: 'in_progress', to: 'open', at: '2026-10-01T00:00:00Z', undone: false, undoable: true } }
  const second = { ...base, id: '42', at: '2026-10-01T00:01:00Z', automatic_change: { ...base.automatic_change!, event_id: 42, rule: 'publish' as const, reason: 'Shipped', from: 'done', to: 'delivered' } }
  const entries = buildTimeline([second, base])
  assert.equal(entries.length, 2); assert.equal(entries[0]!.kind, 'automatic'); assert.equal(entries[1]!.kind, 'automatic')
  if (entries[0]!.kind === 'automatic') { assert.equal(entries[0].change.event_id, 41); assert.equal(entries[0].change.reason, 'Stalled') }
})
