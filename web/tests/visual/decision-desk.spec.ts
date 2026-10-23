// SPDX-License-Identifier: AGPL-3.0-only
import { test, visualCases, checkVisual } from './checkpoint'
import { decisionDesk } from './fixtures'

for (const checkpoint of visualCases('decision-desk')) {
  test(checkpoint.id, async ({ browser }, info) => {
    await checkVisual(browser, info, checkpoint, decisionDesk)
  })
}
