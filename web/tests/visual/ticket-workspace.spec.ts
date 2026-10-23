// SPDX-License-Identifier: AGPL-3.0-only
import { test, visualCases, checkVisual } from './checkpoint'
import { ticketWorkspace } from './fixtures'

for (const checkpoint of visualCases('ticket-workspace')) {
  test(checkpoint.id, async ({ browser }, info) => {
    await checkVisual(browser, info, checkpoint, ticketWorkspace)
  })
}
