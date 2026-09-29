// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, liveAgent, mockWork, type Fixtures } from './work-fixtures'

const shots = '/private/tmp/claude-501/-Users-markus-Code-aeon/a4527da9-f872-45f5-a2f2-48dde0ce2ce5/scratchpad/shots/aeon-316-list-columns'
const at = (minutes: number) => new Date(Date.parse('2026-09-23T12:00:00Z') + minutes * 60_000).toISOString()
const row = (page: Page, key: string) => page.locator('tr.ticket-row:not(.ghost)').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const ticket = (id: string, key: string, title: string) => ({ id, key, title, project_id: 'p-pharos' })

function world(): Fixtures {
  const data = fixtures()
  const set = (key: string, eta: Record<string, unknown>) => { data.nodes.find(node => node.key === key)!.eta = eta }
  set('PHAROS-11', { eta_ready_at: at(25), progress_pct: 45, ready_by: 'Ada', ready_reported_at: at(-2) })
  set('PHAROS-12', { eta_ready_at: at(-5), progress_pct: 90, ready_by: 'Beau', ready_reported_at: at(-2) })
  set('PHAROS-13', { eta_ready_at: at(40), progress_pct: 10, ready_by: 'Cleo', ready_reported_at: at(-40), ready_stale: true, eta_stale: true })
  // PHAROS-14 has no stored assignee. Two live workers: Kai started first, Uma later.
  // A stopped Aaa and an earlier coordinator Bea must not become the sort name.
  data.live.push(
    liveAgent({ project_id: 'p-pharos', session_id: 's-bea', name: 'Bea', role: 'coordinator', ticket: ticket('n-4', 'PHAROS-14', 'Visual acceptance of the version pill') }, 40),
    liveAgent({ project_id: 'p-pharos', session_id: 's-kai', name: 'Zed', display_label: 'Kai', ticket: ticket('n-4', 'PHAROS-14', 'Visual acceptance of the version pill') }, 20),
    liveAgent({ project_id: 'p-pharos', session_id: 's-uma', name: 'Uma', ticket: ticket('n-4', 'PHAROS-14', 'Visual acceptance of the version pill') }, 5),
    liveAgent({ project_id: 'p-pharos', session_id: 's-aaa', name: 'Aaa', phase: 'stopped', stopped_at: at(-10), stop_reason: 'completed', ticket: ticket('n-4', 'PHAROS-14', 'Visual acceptance of the version pill') }, 50),
  )
  // PHAROS-12 is unassigned and has no live worker. PHAROS-11 is Markus, PHAROS-13 is Mira.
  return data
}

const keys = (page: Page) => page.locator('tr.ticket-row:not(.ghost) .key').allTextContents()

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

test('assignee sort uses the live worker when nobody is stored, and progress and ETA stay compact', async ({ page }) => {
  const data = world()
  const sorts: string[] = []
  page.on('request', request => {
    const url = new URL(request.url())
    if (request.method() === 'GET' && url.pathname === '/api/nodes' && url.searchParams.get('sort')) sorts.push(url.searchParams.get('sort')!)
  })
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=assignee')
  await expect(page.getByRole('columnheader', { name: 'Progress' })).toBeVisible()
  await expect(page.getByRole('columnheader', { name: 'ETA' })).toBeVisible()
  // Stored names sort as written: Markus (PHAROS-11) before Mira (PHAROS-13).
  // Kai is the lead worker on PHAROS-14. Unassigned PHAROS-12 stays after them.
  await expect.poll(async () => (await keys(page)).slice(0, 4)).toEqual(['PHAROS-14', 'PHAROS-11', 'PHAROS-13', 'PHAROS-12'])

  const fresh = row(page, 'PHAROS-11')
  await expect(fresh.locator('.progress-read .pct')).toHaveText('45%')
  await expect(fresh.locator('.progress-read')).toHaveAttribute('aria-label', '45% done')
  await expect(fresh.locator('.progress-read .bar > i')).toHaveAttribute('style', /width:\s*45%/)
  await expect(fresh.locator('.eta-cell .when')).toHaveText(/^~2\d min$/)
  await expect(fresh.locator('.eta-cell')).toHaveAttribute('data-tip', /45% done/)
  await expect(fresh.locator('.eta-cell .pct')).toBeHidden()
  const overdue = row(page, 'PHAROS-12')
  await expect(overdue.locator('.progress-read .pct')).toHaveText('90%')
  await expect(overdue.locator('.eta-cell')).toHaveClass(/overdue/)
  await expect(overdue.locator('.c-progress .empty')).toHaveCount(0)
  const stale = row(page, 'PHAROS-13')
  await expect(stale.locator('.progress-read')).toHaveClass(/stale/)
  await expect(stale.locator('.progress-read')).toHaveAttribute('aria-label', /^10% done, estimate stale since \d{2}:\d{2}$/)
  await expect(stale.locator('.eta-cell')).toHaveClass(/stale/)
  await expect(stale.locator('.eta-cell svg')).toHaveCount(1)
  await expect(row(page, 'PHAROS-14').locator('.progress-read')).toHaveCount(0)
  await expect(row(page, 'PHAROS-14').locator('.c-assignee .worker-name')).toHaveText('Kai')

  await page.getByRole('columnheader', { name: 'Assignee' }).getByRole('button', { name: 'Assignee' }).click()
  await expect.poll(async () => (await keys(page)).slice(0, 4)).toEqual(['PHAROS-13', 'PHAROS-11', 'PHAROS-14', 'PHAROS-12'])

  await page.getByRole('columnheader', { name: 'Progress' }).getByRole('button', { name: 'Progress' }).click()
  await expect.poll(() => sorts.some(sort => sort.split(',').includes('progress'))).toBe(true)
  await page.getByRole('button', { name: 'Display: Display' }).click()
  const names = page.getByRole('dialog', { name: 'Display options' }).locator('.columns .name')
  const order = await names.allTextContents()
  expect(order.indexOf('Progress')).toBeGreaterThan(-1)
  expect(order.indexOf('Progress')).toBeLessThan(order.indexOf('ETA'))
})

test('progress names a stale estimate when the ETA column is hidden', async ({ page }) => {
  const data = world()
  data.preferences['list:p-pharos'] = { visible: ['status', 'priority', 'assignee', 'updated', 'progress'] }
  await mockWork(page, data)
  await page.setViewportSize({ width: 1600, height: 900 })
  await page.goto('/p/PHAROS?sort=assignee')
  await expect(page.getByRole('columnheader', { name: 'ETA' })).toHaveCount(0)
  await expect(row(page, 'PHAROS-13').locator('.progress-read')).toHaveAttribute('aria-label', /^10% done, estimate stale since \d{2}:\d{2}$/)
  await expect(row(page, 'PHAROS-11').locator('.progress-read')).toHaveAttribute('aria-label', '45% done')
  await expect(row(page, 'PHAROS-11').locator('.c-eta')).toHaveCount(0)
})

for (const width of [1600, 390] as const) {
  for (const theme of ['light', 'dark'] as const) {
    test(`progress and ETA columns at ${width}px ${theme}`, async ({ page }) => {
      const data = world()
      data.preferences.theme = { choice: theme }
      await page.addInitScript(value => { document.documentElement.dataset.theme = value }, theme)
      await mockWork(page, data)
      await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
      await page.goto('/p/PHAROS?sort=assignee')
      await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
      await expect(row(page, 'PHAROS-11').locator('.progress-read .pct')).toHaveText('45%')
      await expect(row(page, 'PHAROS-11').locator('.eta-cell .when')).toBeVisible()
      await expect(row(page, 'PHAROS-13').locator('.eta-cell')).toHaveClass(/stale/)
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)
      expect(overflow).toBe(true)
      mkdirSync(shots, { recursive: true })
      await page.screenshot({ path: join(shots, `list-columns__${width}__${theme}.png`) })
    })
  }
}
