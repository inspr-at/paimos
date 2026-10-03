// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'

test('worker guidance permits downward growth and scopes fixed-frame measurements to pattern B', () => {
  const agents = readFileSync(new URL('../../AGENTS.md', import.meta.url), 'utf8')
  const rule = agents.slice(agents.indexOf('- UI stability (AEON-541):'), agents.indexOf('- Keyboard convention (AEON-541):'))
  assert.match(rule, /Rule 7 governs the choice of layout/)
  assert.match(rule, /Top-anchored frames may grow downward/)
  assert.match(rule, /Assert frame height only for pattern B/)
  assert.match(rule, /named actions, selectors, selector groups and the clicked row/)
  assert.match(rule, /at least one interaction and positive-size samples/)
  assert.doesNotMatch(rule, /Apply rules 1 → 2 → 3 first|measure named selectors, groups, dialog frames/)
  const rollout = JSON.parse(readFileSync(new URL('../../scripts/rules-bootstrap/rollout.json', import.meta.url), 'utf8'))
  assert.equal(rollout.state, 'prepared')
  assert.equal(rollout.existing_agents_sha256, createHash('sha256').update(agents).digest('hex'))
})
