// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { moveTarget, movableWorker } from '../src/components/agents/sessionMove.ts'
import type { HarnessSession } from '../src/lib/agents'
const worker = { id: 'worker', project_id: 'project', can_reparent: true, role: 'worker', phase: 'working', parent_harness_session_id: 'old' } as HarnessSession
const lead = { ...worker, id: 'lead', role: 'coordinator', parent_harness_session_id: null } as HarnessSession

test('move requires rights to both live sessions in the same project', () => {
  assert.equal(moveTarget(worker, lead, [worker, lead]), true)
  for (const change of [{ can_reparent: false }, { can_reparent: undefined }, { stopped_at: 'now' }, { phase: 'stopped' }, { archived_at: 'now' }, { project_id: 'foreign' }, { role: 'worker' }]) {
    assert.equal(moveTarget(worker, { ...lead, ...change } as HarnessSession, []), false)
  }
  assert.equal(movableWorker({ ...worker, can_reparent: false }), false)
  assert.equal(movableWorker({ ...worker, stopped_at: 'now' }), false)
  assert.equal(moveTarget({ ...worker, parent_harness_session_id: lead.id }, lead, []), false)
})
test('self and descendant targets are rejected', () => {
  assert.equal(moveTarget(worker, { ...lead, id: worker.id }, []), false)
  assert.equal(moveTarget(worker, { ...lead, parent_harness_session_id: worker.id }, [worker]), false)
})
