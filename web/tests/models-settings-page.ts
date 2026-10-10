// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Page } from '@playwright/test'
import { mockModels, type MockOptions } from './models-simple-fixtures'
export const mockModelsSettings = (page: Page, options: MockOptions = {}) => mockModels(page, options)
export async function openModelsSettings(page: Page, query = '') {
  await page.goto(`/tests/models-settings-harness.html${query}`)
  await expect(page.locator('[data-models-ready="true"]')).toBeVisible()
}
