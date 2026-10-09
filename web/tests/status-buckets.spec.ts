// SPDX-License-Identifier: AGPL-3.0-only
// AEON-302: project cards and the project header show open, blocked and unknown
// work in the open count, at a phone width and on a wide screen.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, type MockNode } from './work-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const stamp = '2026-09-23T11:00:00Z'

function bucketWorld() {
  const data = fixtures()
  data.preferences.projects = { view: 'cards' }
  data.projects.unshift({
    id: 'p-buckets', key: 'PRJ-90', title: 'Buckets', state: 'active', classic: 'BUCKET',
    description: 'Open, blocked and unknown work.', last: '2026-09-23T11:30:00Z',
  })
  const states = ['new', 'backlog', 'open', 'blocked', 'mystery', 'in_progress', 'active', 'qa', 'done', 'accepted', 'cancelled', 'archived']
  states.forEach((state, index) => {
    const node: MockNode = {
      id: `n-bucket-${index}`, key: `BUCKET-${index + 1}`, kind_slug: 'ticket', title: state, body: '', state,
      fields: {}, parent_id: 'p-buckets', project: 'p-buckets', created_at: stamp, updated_at: stamp,
    }
    data.nodes.push(node)
  })
  data.nodes.push({
    id: 'n-bucket-memory', key: 'MEM-1', kind_slug: 'memory', title: 'Not work', body: '', state: 'open',
    fields: {}, parent_id: 'p-buckets', project: 'p-buckets', created_at: stamp, updated_at: stamp,
  })
  return data
}

async function noHorizontalScroll(page: Page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(1)
}

test('card and header count open, blocked and unknown work at 390 and 1600', async ({ page }) => {
  await mockWork(page, bucketWorld())
  for (const width of [1600, 390]) {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.goto('/')
    const card = page.getByRole('link', { name: /BUCKET Buckets, 8 open, 3 in progress, 2 done/ })
    await expect(card).toBeVisible()
    await expect(card.locator('.k-open .stat-num')).toHaveText('5')
    await expect(card.locator('.k-doing .stat-num')).toHaveText('3')
    await expect(card.locator('.k-done .stat-num')).toHaveText('2')
    await expect(card.locator('.ring .pct')).toHaveText('20%')
    await expect(card.locator('.ring-wrap')).toHaveAttribute('data-tip', '2 of 10 done · 1 cancelled')
    const cardBox = (await card.boundingBox())!
    expect(cardBox.x).toBeGreaterThanOrEqual(0)
    expect(cardBox.x + cardBox.width).toBeLessThanOrEqual(width + 1)
    await noHorizontalScroll(page)
    await page.screenshot({ path: test.info().outputPath(`card-${width}.png`), fullPage: false })

    await page.goto('/p/BUCKET')
    const stats = page.locator('.head-stats')
    await expect(stats).toHaveAttribute('aria-label', '5 open, 3 in progress, 2 done of 12')
    await expect(stats.getByRole('button', { name: 'Filter open tickets: 5', exact: true })).toBeVisible()
    await expect(stats.getByRole('button', { name: 'Filter doing tickets: 3', exact: true })).toBeVisible()
    await expect(stats.getByRole('button', { name: 'Filter done tickets: 2', exact: true })).toBeVisible()
    await expect(stats.locator('.pct')).toHaveText('20%')
    await expect(stats.locator('.progress-line')).toHaveAttribute('data-tip', '2 of 10 done · 1 cancelled')
    await expect(stats.getByRole('button', { name: 'Filter open tickets: 5', exact: true })).toHaveAttribute('data-tip', 'Show only open tickets; click again to clear')
    const statsBox = (await stats.boundingBox())!
    expect(statsBox.x).toBeGreaterThanOrEqual(0)
    expect(statsBox.x + statsBox.width).toBeLessThanOrEqual(width + 1)
    await expect(stats.getByRole('button', { name: 'Filter open tickets: 5', exact: true })).toBeInViewport()
    await page.screenshot({ path: test.info().outputPath(`header-${width}.png`), fullPage: false })
  }
})
