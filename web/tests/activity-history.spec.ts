// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Browser, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { reviewShots } from './review-shots'

const SHOTS = reviewShots('aeon-336-system-principal')
const hour = (h: number) => new Date(Date.parse('2026-09-23T12:00:00Z') - h * 3_600_000).toISOString()

function withHistory() {
  const data = fixtures()
  data.activity['n-4'] = [
    { id: '44', at: hour(1), type: 'change', author: { id: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', name: 'System', automatic: true, job: 'learning-tagger', reason: 'method learning tagger' }, changes: [{ field: 'tags', from: 'ops', to: 'ops, process-learning' }] },
    { id: '43', at: hour(4), type: 'change', author: { id: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', name: 'System' }, changes: [{ field: 'priority', from: 'low', to: 'medium' }] },
    { id: '42', at: hour(8), type: 'comment', author: { id: me.id, name: me.name }, body_markdown: 'The probe list is the part to keep.' },
    { id: '41', at: hour(40), type: 'created', author: { id: me.id, name: me.name } },
  ]
  return data
}

async function openTicket(page: Page) {
  await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z'))
  await mockWork(page, withHistory())
  await page.goto('/p/PHAROS/PHAROS-14')
  const activity = page.getByRole('region', { name: 'Activity' })
  await expect(activity.getByRole('radio', { name: 'All' })).toBeVisible()
  await activity.scrollIntoViewIfNeeded()
  return activity
}

test('automatic history is Aeon, and a person named System stays a person', async ({ page }) => {
  const errors = watchErrors(page)
  const activity = await openTicket(page)
  const auto = activity.locator('.entry.automatic')
  await expect(auto).toHaveCount(1)
  await expect(auto).toContainText('AEON (automatic)')
  await expect(auto).toContainText('changed labels')
  await expect(auto.locator('.auto svg')).toHaveCount(1)
  await expect(auto.locator('strong')).toHaveAttribute('data-tip', 'learning-tagger · method learning tagger')
  await expect(activity.locator('.entry.changes').filter({ hasText: 'changed priority' })).toContainText('System')
  await expect(activity.locator('.entry.changes').filter({ hasText: 'changed priority' })).not.toContainText('automatic')

  await activity.getByRole('radio', { name: 'Automatic' }).click()
  await expect(activity.locator('.entry.automatic')).toHaveCount(1)
  await expect(activity.locator('.entry.comment')).toHaveCount(0)
  await expect(activity.locator('.entry.changes').filter({ hasText: 'System' })).toHaveCount(0)

  await activity.getByRole('radio', { name: 'People and agents' }).click()
  await expect(activity.locator('.entry.automatic')).toHaveCount(0)
  await expect(activity.getByText('(automatic)')).toHaveCount(0)
  await expect(activity.locator('.entry.comment')).toContainText('The probe list is the part to keep.')
  await expect(activity.locator('.entry.changes')).toContainText('System')
  await expect(activity.locator('.entry.created')).toContainText(me.name)

  await activity.getByRole('radio', { name: 'All' }).click()
  await expect(activity.locator('.entry')).toHaveCount(4)
  expect(errors).toEqual([])
})

async function shoot(browser: Browser, theme: 'light' | 'dark', width: number) {
  const context = await browser.newContext({
    viewport: { width, height: width === 390 ? 844 : 1000 },
    colorScheme: theme,
    reducedMotion: 'reduce',
  })
  const page = await context.newPage()
  const activity = await openTicket(page)
  const overflow = await page.evaluate(() => {
    const root = document.documentElement
    const region = document.querySelector('[aria-label="Activity"]')
    const pageWide = root.scrollWidth > root.clientWidth + 1
    const regionWide = !!region && region.scrollWidth > region.clientWidth + 1
    return pageWide || regionWide ? `${root.scrollWidth}/${root.clientWidth} region ${region?.scrollWidth}/${region?.clientWidth}` : ''
  })
  expect(overflow, `${width} ${theme}`).toBe('')
  mkdirSync(SHOTS, { recursive: true })
  await activity.screenshot({ path: join(SHOTS, `history__${width}__${theme}.png`) })
  await page.screenshot({ path: join(SHOTS, `ticket__${width}__${theme}.png`) })
  await context.close()
}

test('history screenshots at 390 and 1600, light and dark', async ({ browser }) => {
  for (const theme of ['light', 'dark'] as const) {
    for (const width of [1600, 390]) await shoot(browser, theme, width)
  }
})
