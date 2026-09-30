// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { assessAgentState } from '../src/lib/agentSignals.ts'

process.env.TZ = 'UTC'

test('a vendor stop stays calm and names the real reset, then becomes stopped', () => {
  const now = Date.parse('2026-09-29T12:00:00Z')
  const session = { phase: 'stopped', activity: 'throttled', stopped_at: new Date(now).toISOString(), stop_reason: 'vendor_limit', run_status: 'failed', has_problem: false, vendor_limited: true, limit_window: '5h', limit_resets_at: '2026-09-29T14:10:00Z' }
  const state = assessAgentState(session, now)
  assert.equal(state.state, 'throttled')
  assert.match(state.label, /^Throttled · 5-hour limit until /)
  assert.equal(assessAgentState(session, Date.parse(session.limit_resets_at)).state, 'stopped')
  const blind = assessAgentState({ ...session, limit_resets_at: null, limit_window: undefined }, now)
  assert.equal(blind.label, 'Throttled · vendor limit')
  assert.equal(assessAgentState({ ...session, vendor_limited: false, has_problem: true }, now).state, 'problem')
  const later = assessAgentState({ ...session, limit_resets_at: '2026-10-03T14:10:00Z' }, now)
  assert.match(later.label, /3 Oct/)
  assert.doesNotMatch(later.label, /until 14:10$/)
})

test('status line copy names whose computer it is', async () => {
  const { claudeStatuslineCopy } = await import('../src/lib/agents.ts')
  assert.equal(claudeStatuslineCopy('own', 'Aeon'), 'Show Aeon in your Claude status line')
  assert.equal(claudeStatuslineCopy('workspace', 'Aeon'), "Show Aeon in this Claude account's status line")
})
