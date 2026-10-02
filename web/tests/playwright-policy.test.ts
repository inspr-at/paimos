// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { browserPolicy, headlessChromium, uiShardPolicy } from '../playwright.policy.ts'

test('local configs serialize workers and tests; CI keeps its own defaults', () => {
  for (const CI of [undefined, '', '0', 'false']) {
    assert.equal(browserPolicy({ CI }, 4).workers, 1)
    assert.equal(browserPolicy({ CI }, 4).fullyParallel, false)
  }
  assert.equal(browserPolicy({ CI: 'true' }, 4).workers, 4)
  assert.equal(browserPolicy({ CI: 'true' }).workers, undefined)
  assert.equal(browserPolicy({ CI: 'true' }, 1).workers, 1)
  assert.equal(browserPolicy({ CI: 'true' }, 4).fullyParallel, false)
  assert.equal(browserPolicy({ CI: 'true' }, 4, true).fullyParallel, true)
  assert.equal(browserPolicy({}, 4, true).fullyParallel, false)
})

test('smoke and performance CI keep serial tests while UI explicitly opts in', () => {
  assert.equal(browserPolicy({ CI: '1' }).fullyParallel, false)
  assert.equal(browserPolicy({ CI: '1' }, 1).fullyParallel, false)
  assert.equal(browserPolicy({ CI: '1' }, 4, true).fullyParallel, true)
})

test('shard budget overrides UI policy spread even with CI or PW_WORKERS overrides', () => {
  const env = { CI: '1', PW_WORKERS: '4', AEON_PW_SHARD: '1' }
  const policy = { ...browserPolicy(env, 4, true), ...uiShardPolicy(env) }
  assert.equal(policy.workers, 1)
  assert.equal(policy.fullyParallel, false)
  assert.deepEqual(uiShardPolicy({}), {})
})

test('worker override is explicit and invalid overrides fail closed', () => {
  assert.equal(browserPolicy({ PW_WORKERS: '2' }, 4).workers, 2)
  assert.equal(browserPolicy({ CI: '1', PW_WORKERS: '1' }, 4).workers, 1)
  for (const value of ['', '0', '-1', '2.5', '50%', 'wat', '9007199254740992']) {
    assert.throws(() => browserPolicy({ PW_WORKERS: value }), /positive integer/)
  }
})

test('browser policy uses bundled headless Chromium with GPU disabled', () => {
  assert.equal(headlessChromium.browserName, 'chromium')
  assert.equal(headlessChromium.headless, true)
  assert.deepEqual(headlessChromium.launchOptions.args, ['--disable-gpu'])
  assert.equal('channel' in headlessChromium, false)
  assert.equal('projects' in browserPolicy({}), false)
})
