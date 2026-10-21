// SPDX-License-Identifier: AGPL-3.0-only
import { verifySupervisor } from '../playwright-global-setup.mjs'
import { uiStackTarget, waitForStack } from './stack-ready.mjs'

// CI waits for backend health before specs. Approved mock-only jobs set
// AEON_REQUIRE_BACKEND=off and do not dial. Local runs probe once and
// continue when :8080 is closed. required fails the shard before specs
// when the backend never accepts.
export async function runUiStackSetup({ env = process.env, cwd = process.cwd(), fetch, sleep, now, schedule, verify = verifySupervisor } = {}) {
  verify(undefined, env)
  return waitForStack({ ...uiStackTarget(env, cwd), fetch, sleep, now, schedule })
}

export default async function uiStackSetup() {
  await runUiStackSetup()
}
