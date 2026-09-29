// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readDeployTarget, deployTargetSentence } from '../src/lib/deployTarget.ts'

test('targets are display facts, independent of a digest or approval state', () => {
  const approval = { scope: 'journey.deploy', target: { hosts: ['edge-1'], service: 'aeon', change: 'Upgrade image' } }
  assert.equal(readDeployTarget(approval).where, 'edge-1')
  assert.equal(readDeployTarget(approval).standing, 'named')
  assert.match(deployTargetSentence(approval), /Server: edge-1/)
  assert.equal(readDeployTarget({ scope: 'journey.deploy' }).where, 'Target not named')
  assert.equal(deployTargetSentence({ scope: 'stage.deploy' }), 'Target not named.')
  assert.equal(readDeployTarget({ scope: 'nodes.read', target: approval.target }).applicable, false)
  assert.equal(readDeployTarget({ scope: 'journey.deploy', target: { environment: 'staging' } }).where, 'staging')
})
