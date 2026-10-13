// SPDX-License-Identifier: AGPL-3.0-only
// Separate spec ownership lets the nightly planner distribute the full audit.
import { test } from '@playwright/test'
import { runUIAudit } from './ui-audit-fixtures'

test('offline route and state audit dark 1280', async ({ browser }, testInfo) => {
  await runUIAudit(browser, testInfo, 'dark', 1280)
})
