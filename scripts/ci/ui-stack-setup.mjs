// SPDX-License-Identifier: AGPL-3.0-only
import { verifySupervisor } from '../playwright-global-setup.mjs'
import { uiStackTarget, waitForStack } from './stack-ready.mjs'

// UI shards do not start paimos serve. The default mode probes once and
// continues when :8080 is closed. AEON_REQUIRE_BACKEND=required fails the
// shard before specs when the backend never accepts.
export async function runUiStackSetup({ env = process.env, cwd = process.cwd(), fetch, sleep, now, verify = verifySupervisor } = {}) {
  verify(undefined, env)
  return waitForStack({ ...uiStackTarget(env, cwd), fetch, sleep, now })
}

export default async function uiStackSetup() {
  await runUiStackSetup()
}
