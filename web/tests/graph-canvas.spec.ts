// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'

test('the shared canvas consumes a ticket adapter and honors its FPS prop', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/projects')
  await page.evaluate(async () => { const { mountTicketGraph } = await import('/tests/graph-canvas-harness.ts'); await mountTicketGraph() })
  const graph = page.locator('#ticket-graph-harness .graph-surface')
  await expect(graph).toHaveAttribute('data-ready', 'true', { timeout: 15000 })
  await expect(graph).toHaveAttribute('data-fps', '30')
  await graph.press('ArrowRight'); await graph.press('Enter')
  await expect(page.getByLabel('Opened ticket')).toHaveText('/p/AEON/AEON-194')
  await page.getByRole('button', { name: 'Set parent FPS' }).click()
  await expect(graph).toHaveAttribute('data-fps', '60')
})
