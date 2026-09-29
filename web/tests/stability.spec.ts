// SPDX-License-Identifier: AGPL-3.0-only
// Pages hold still while they load: the loading state has the loaded page's shape,
// reads that land one after another appear together, and measured layouts are
// measured before the first paint. Cumulative layout shift stays under 0.05 on the
// pages that used to jump (agents, a session, the profile, quotes, customers and
// the customer's quote page), on a phone and on a desktop.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'
import { crmData, mockCRM } from './crm-fixtures'
import { mockPublicQuote, mockQuotes, quoteWorld } from './quote-list-fixtures'

const world = {
  me: me.id, projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' },
  tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' },
  nodes: {
    'p-pharos': { key: 'PRJ-17', title: 'Pharos' }, 'p-aeon': { key: 'PRJ-35', title: 'Aeon' }, 'p-frozen': { key: 'PRJ-26', title: 'Studio infrastructure' },
    'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning' }, 'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector' },
    'n-a1': { key: 'AEON-1', title: 'Aeon foundation' }, 'n-5': { key: 'PHAROS-15', title: 'Beacon health probes' }, 'n-6': { key: 'PHAROS-16', title: 'Retire the old dashboard' },
  },
}

// Sums every layout shift without recent input from the first paint on.
async function watchShifts(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as { __cls: number; __shifts: string[] }
    w.__cls = 0; w.__shifts = []
    new PerformanceObserver(list => {
      for (const entry of list.getEntries() as unknown as { value: number; hadRecentInput: boolean; sources?: { node: Node | null }[] }[]) {
        if (entry.hadRecentInput) continue
        w.__cls += entry.value
        w.__shifts.push(`${entry.value.toFixed(3)} ${(entry.sources ?? []).map(s => (s.node as Element | null)?.className ?? '?').join(', ')}`)
      }
    }).observe({ type: 'layout-shift', buffered: true })
  })
}
async function settledShift(page: Page) {
  await page.waitForTimeout(600)
  return page.evaluate(() => { const w = window as unknown as { __cls: number; __shifts: string[] }; return { cls: w.__cls, shifts: w.__shifts } })
}
async function expectStill(page: Page) {
  const { cls, shifts } = await settledShift(page)
  expect(cls, shifts.join('\n')).toBeLessThan(0.05)
}
async function base(page: Page) {
  await watchShifts(page)
  await mockWork(page, fixtures())
  await mockAgents(page, agentData(world))
  await mockSettings(page, settingsData({ photo: true }))
  await mockReleases(page, releaseHistory())
}
async function quotes(page: Page) {
  await watchShifts(page)
  await mockWork(page, fixtures())
  await mockReleases(page, releaseHistory())
  await mockCRM(page, crmData())
  await mockQuotes(page, quoteWorld())
}

for (const width of [390, 1440]) {
  test.describe(`at ${width}px`, () => {
    test.beforeEach(async ({ page }) => { await page.setViewportSize({ width, height: width === 390 ? 844 : 900 }) })

    test('the agents page holds still while its reads land', async ({ page }) => {
      await base(page)
      await page.goto('/agents')
      await expect(page.locator('.agents-page [data-row^="s:"]').first()).toBeVisible()
      await expect(page.getByRole('list', { name: 'Requests waiting for you' })).toBeVisible()
      await expectStill(page)
    })

    test('an open session holds still while runs and messages land', async ({ page }) => {
      await base(page)
      await page.goto('/agents/5e000000-0000-4000-8000-000000000001')
      await expect(page.locator('.session-panel .facts')).toBeVisible()
      await expectStill(page)
    })

    test('the profile loads in the shape of its form', async ({ page }) => {
      await base(page)
      await page.goto('/settings/personal')
      await expect(page.locator('#profile-first_name')).toBeVisible()
      await expectStill(page)
    })

    test('the quote list, its notice and its columns land together', async ({ page }) => {
      await quotes(page)
      await page.goto('/business/quotes')
      await expect(page.locator('table.quotes tbody .row, ul.cards .card-row:not(.ghost)').first()).toBeVisible()
      await expect(page.locator('.acceptance-notices')).toBeVisible()
      await expectStill(page)
    })

    test('the customer list fits its columns before the first paint', async ({ page }) => {
      await quotes(page)
      await page.goto('/business/customers')
      await expect(page.locator('table.customers tbody .row, ul.cards .card-row:not(.ghost)').first()).toBeVisible()
      await expectStill(page)
    })

    test('the customer’s quote page fits the paper before the first paint', async ({ page }) => {
      await watchShifts(page)
      await mockWork(page, fixtures())
      await mockPublicQuote(page, { acceptable: true })
      await page.goto('/offers/sel-demo/tok-example')
      await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
      await expectStill(page)
    })
  })
}

test('the approval queue never says nothing waits before it knows', async ({ page }) => {
  await base(page)
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/approvals?**', async route => { await held; await route.fallback() })
  await page.goto('/agents')
  // Needs you shows only once it knows something waits (AEON-299): no early card, no all-clear.
  await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Needs you' })).toHaveCount(0)
  await expect(page.getByText('Nothing waits on you')).toHaveCount(0)
  release()
  await expect(page.getByRole('list', { name: 'Requests waiting for you' })).toBeVisible()
})
