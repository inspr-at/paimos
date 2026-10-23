// SPDX-License-Identifier: AGPL-3.0-only
import { test, visualCases, checkVisual } from './checkpoint'
import { accountUseMatrix } from './fixtures'

for (const checkpoint of visualCases('account-use-matrix')) {
  test(checkpoint.id, async ({ browser }, info) => {
    await checkVisual(browser, info, checkpoint, accountUseMatrix)
  })
}
