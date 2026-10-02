// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { browserPolicy, uiShardPolicy } from '../../../web/playwright.policy.ts'
import { verifySupervisor } from '../../playwright-global-setup.mjs'

const [output, inheritedLock] = process.argv.slice(2)
assert.equal(readFileSync(inheritedLock, 'utf8').includes(process.env.AEON_PW_RUN), true)
assert.equal(process.env.PW_WORKERS, '1')
const policy = { ...browserPolicy(process.env, 4, true), ...uiShardPolicy(process.env) }
assert.equal(policy.workers, 1)
assert.equal(policy.fullyParallel, false)
// Prove local global setup accepts the actual lock, without a browser.
delete process.env.CI
verifySupervisor(inheritedLock)
const result = spawnSync(process.execPath, ['-e', 'console.log(JSON.stringify({suites: []}))'], { encoding: 'utf8' })
assert.equal(result.status, 0, result.stderr)
writeFileSync(output, JSON.stringify({ report: JSON.parse(result.stdout), policy, args: process.argv.slice(4) }))
