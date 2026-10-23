// SPDX-License-Identifier: AGPL-3.0-only
import { test, visualCases, checkVisual } from './checkpoint'
import { startAgent } from './fixtures'

for (const checkpoint of visualCases('start-agent')) {
  test(checkpoint.id, async ({ browser }, info) => {
    await checkVisual(browser, info, checkpoint, startAgent)
  })
}
