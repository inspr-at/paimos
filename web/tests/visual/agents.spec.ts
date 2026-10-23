// SPDX-License-Identifier: AGPL-3.0-only
import { test, visualCases, checkVisual } from './checkpoint'
import { agents } from './fixtures'

for (const checkpoint of visualCases('agents')) {
  test(checkpoint.id, async ({ browser }, info) => {
    await checkVisual(browser, info, checkpoint, agents)
  })
}
