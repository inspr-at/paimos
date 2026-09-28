// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'

const shots = '/private/tmp/claude-501/-Users-markus-Code-aeon/a4527da9-f872-45f5-a2f2-48dde0ce2ce5/scratchpad/shots/aeon-262-eta'

function world() {
  const data = fixtures()
  const ticket = data.nodes.find(node => node.key === 'PHAROS-11')!
  ticket.eta = {
    eta_ready_at: new Date(Date.now() + 25 * 60_000).toISOString(),
    progress_pct: 40,
    ready_by: 'Ada',
    ready_reported_at: new Date().toISOString(),
  }
  data.preferences['eta-display'] = { mode: 'both' }
  return data
}

const row = (page: Page, key: string) => page.locator('tr.ticket-row:not(.ghost)').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })

test('the ETA column shows a reported estimate and stays empty when none was reported', async ({ page }) => {
  mkdirSync(shots, { recursive: true })
  await page.setViewportSize({ width: 1600, height: 900 })
  await mockWork(page, world())
  const sorts: string[] = []
  page.on('request', request => {
    const url = new URL(request.url())
    if (request.method() === 'GET' && url.pathname === '/api/nodes' && url.searchParams.get('sort')?.includes('eta_ready')) sorts.push(url.searchParams.get('sort')!)
  })
  await page.goto('/p/PHAROS')
  await expect(page.getByRole('button', { name: 'ETA' })).toBeVisible()
  const reported = row(page, 'PHAROS-11').locator('.eta-cell')
  await expect(reported).toContainText(/~\d+ min/)
  await expect(reported).toContainText('40%')
  await expect(reported.locator('.hover')).toHaveText(/^\d{2}:\d{2}$/)
  await expect(row(page, 'PHAROS-14').locator('.eta-cell')).toHaveCount(0)
  await reported.hover()
  await page.screenshot({ path: `${shots}/list-1600.png` })
  await page.getByRole('button', { name: 'ETA' }).click()
  await expect.poll(() => sorts.some(sort => sort.split(',').includes('eta_ready'))).toBe(true)

  await page.setViewportSize({ width: 390, height: 844 })
  await expect(row(page, 'PHAROS-11').locator('.eta-cell')).toBeVisible()
  await expect(row(page, 'PHAROS-11').locator('.c-eta')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
  await page.screenshot({ path: `${shots}/list-390.png` })
})
