// SPDX-License-Identifier: AGPL-3.0-only
import { verifySupervisor } from '../playwright-global-setup.mjs'
import { e2eStackTarget, waitForStack } from './stack-ready.mjs'

// e2e serves the app from the Go process. The workflow curl is the first
// gate; this one fails the run if that process is gone before the first spec.
export async function runE2eStackSetup({ env = process.env, fetch, sleep, now, schedule, verify = verifySupervisor } = {}) {
  verify(undefined, env)
  return waitForStack({ ...e2eStackTarget(env), fetch, sleep, now, schedule })
}

export default async function e2eStackSetup() {
  await runE2eStackSetup()
}
