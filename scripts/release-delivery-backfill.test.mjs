// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { latestMerges, planBackfill, main } from './release-delivery-backfill.mjs'

// Risk: a dry-run overclaims historical acceptance or ships a later follow-up.
test('release backfill keeps newest main merge, human checks and live evidence', () => {
  const merges = latestMerges('later\x1fMerge pull request from work/aeon-2\x1fAEON-2: follow-up\x1eshipped\x1fMerge pull request from work/aeon-2\x1fAEON-2: first work\x1eold\x1fAEON-1: original\x1f\x1ehuman\x1fAEON-3: native check\x1f\x1e')
  assert.equal(merges.get('AEON-2'), 'later')
  const release = { version: '261016060843.0.0', name: 'Teal Timer' }
  const nodes = [1, 2, 3, 4].map(i => ({ key: `AEON-${i}`, state: 'done', is_leaf: true, fields: {} }))
  nodes[2].human_check = 'Touch ID'
  const input = { nodes, merges, membership: new Map([['old', release], ['shipped', release], ['human', release]]), live: [{ version: release.version, since: '2026-10-16T08:00:00Z' }], now: '2026-10-17T08:00:00Z', settings: { enabled: true, rules: { publish: { enabled: true }, accept: { enabled: true, days: 1 } } } }
  const plan = planBackfill(input)
  assert.equal(plan[0].action, 'Delivered → Accepted (objection recheck required)')
  assert.equal(plan[1].action, 'unchanged')
  assert.match(plan[2].reason, /Human check: Touch ID/)
  assert.equal(plan[3].action, 'unchanged')
  assert.equal(planBackfill({ ...input, live: [] }).filter(r => r.action.startsWith('Delivered')).length, 0)
  assert.equal(planBackfill({ ...input, now: '2026-10-17T07:59:59Z' })[0].action, 'Delivered')
  assert.throws(() => main(['--apply', 'yes']), /no apply mode/)
})
