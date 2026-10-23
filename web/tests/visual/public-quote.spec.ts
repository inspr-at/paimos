// SPDX-License-Identifier: AGPL-3.0-only
import { test, visualCases, checkVisual } from './checkpoint'
import { publicQuote } from './fixtures'

for (const checkpoint of visualCases('public-quote')) {
  test(checkpoint.id, async ({ browser }, info) => {
    await checkVisual(browser, info, checkpoint, publicQuote)
  })
}
