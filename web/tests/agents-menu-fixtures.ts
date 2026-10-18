// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test'

export async function openAttachSession(page: Page) {
  await page.getByRole('button', { name: 'New: start a lead, attach a session or connect a machine', exact: true }).click()
  await page.getByRole('menuitem', { name: /Attach a running session/ }).click()
}
