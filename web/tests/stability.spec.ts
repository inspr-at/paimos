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
import { mockPolicies } from './policies-fixtures'
import { expectStableControls } from './helpers/stable'
import { POLICY_ROLES, POLICY_TABS, TRUNCATED_LADDER } from '../src/lib/policies'

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

// AEON-616: Policies uses pattern A. Measure controls across all pairs, not
// page/frame height. Only the full-height phone detail sheet uses pattern B.
for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`Policies controls and detail sheet stay put at ${width}px ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    const mock = await mockPolicies(page, theme)
    await page.goto('/settings/policies')
    const panel = page.locator('.policies')
    const tabs = panel.getByTestId('policies-tab')
    const tab = (id: string) => panel.locator(`#policy-tab-${id}`)
    const role = (id: string) => panel.getByTestId('policies-role').filter({ hasText: POLICY_ROLES.find(item => item.id === id)!.label }).first()
    const detail = panel.getByTestId('policies-row-link')
    const content = panel.locator('.policy-content')
    const loaded = () => expect(content).toHaveAttribute('aria-busy', 'false')
    await loaded()
    await expect(panel.getByRole('list', { name: 'Configured ladder' })).toBeVisible()
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(panel.getByText(/out of credit/)).toBeVisible()
    // Lazy, source-specific loads: registry is not fetched with the first tab.
    expect(mock.data.calls.every(path => path === '/api/models/routes')).toBe(true)
    const common = Object.fromEntries(POLICY_TABS.map((item, i) => [`policies-tab-${item.id}`, tabs.nth(i)]))
    await expectStableControls({
      controls: { ...common, 'policies-row-link': detail }, scrollAreas: { policies: panel, page: page.locator('main') },
      interactions: POLICY_TABS.flatMap(from => POLICY_TABS.filter(to => to.id !== from.id).map(to => ({
        name: `tab ${from.id} to ${to.id}`, run: async () => {
          await tab(from.id).click(); await loaded()
          await tab(to.id).click(); await loaded()
          await expect(tab(to.id)).toHaveAttribute('aria-selected', 'true')
        },
      }))),
    })
    await tab('keys').click(); await loaded()
    await expect(panel.getByText('rules.publish', { exact: true })).toBeVisible()
    await expect(panel.getByText('models.read', { exact: true })).toHaveCount(0)
    await tab('elsewhere').click(); await loaded()
    const owners = panel.getByTestId('policies-owner-link')
    await expect(owners).toHaveCount(6)
    await expectStableControls({
      controls: { ...common, ...Object.fromEntries(await Promise.all(Array.from({ length: 6 }, async (_, i) => [`policies-owner-link-${i}`, owners.nth(i)] as const))) },
      scrollAreas: { policies: panel },
      interactions: [{ name: 'owner links through hover and focus', run: async () => { await owners.last().hover(); await owners.first().focus() } }],
    })
    await expect(panel.getByRole('article').filter({ hasText: 'Merge queue and repository ownership' })).toContainText('Advisory')
    await tab('ladders').click(); await loaded()
    const roleControls = { ...common, 'policies-row-link': detail, 'policies-role-group': panel.getByTestId('policies-role-group'), ...Object.fromEntries(POLICY_ROLES.map(item => [`policies-role-${item.id}`, role(item.id)])) }
    await expectStableControls({
      controls: roleControls, scrollAreas: { policies: panel, page: page.locator('main') },
      interactions: POLICY_ROLES.flatMap(from => POLICY_ROLES.filter(to => to.id !== from.id).map(to => ({
        name: `role ${from.id} to ${to.id}`, run: async () => {
          await role(from.id).click(); await loaded()
          await role(to.id).click(); await loaded()
          await expect(role(to.id)).toHaveAttribute('aria-pressed', 'true')
        },
      }))),
    })
    // Hold real requests at a barrier; no timing sleeps determine transitions.
    mock.data.count = 51
    const truncation = mock.holdNext()
    await expectStableControls({
      controls: roleControls, scrollAreas: { policies: panel },
      interactions: [
        { name: 'loaded to loading', run: async () => { await role('scout').click(); await truncation.started; await expect(content).toHaveAttribute('aria-busy', 'true') } },
        { name: 'loading to truncated', run: async () => { truncation.release(); await expect(content.getByText(TRUNCATED_LADDER, { exact: true })).toBeVisible(); await expect(panel.locator('.ladder li')).toHaveCount(50) } },
      ],
    })
    mock.data.count = 50
    const complete = mock.holdNext()
    await role('build').click(); await complete.started
    await expectStableControls({ controls: roleControls, scrollAreas: { policies: panel }, interactions: [
      { name: 'loading to complete', run: async () => { complete.release(); await loaded(); await expect(panel.locator('.ladder li')).toHaveCount(50); await expect(content.getByText(TRUNCATED_LADDER, { exact: true })).toHaveCount(0) } },
    ] })
    mock.data.mode = 'failed'
    const failure = mock.holdNext()
    await role('mechanical').click(); await failure.started
    await expectStableControls({ controls: roleControls, scrollAreas: { policies: panel }, interactions: [
      { name: 'loading to error', run: async () => { failure.release(); await expect(content).toContainText('could not be loaded'); await loaded() } },
    ] })
    mock.data.mode = 'denied'
    const denial = mock.holdNext()
    await role('build-hard').click(); await denial.started
    // Once forbidden, the content is exactly the permission sentence. Its
    // private selector/details disappear; the public tab controls stay put.
    await expectStableControls({ controls: common, scrollAreas: { policies: panel }, interactions: [
      { name: 'loading to cannot see', run: async () => { denial.release(); await expect(content).toHaveText("You can't see this: it needs See models."); await expect(detail).toHaveCount(0); await expect(panel.locator('.ladder')).toHaveCount(0) } },
    ] })
    mock.data.mode = 'loaded'; mock.data.count = 2
    await tab('keys').click(); await loaded()
    await tab('ladders').click(); await loaded()
    await expectStableControls({ controls: { ...common, 'policies-row-link': detail }, scrollAreas: { policies: panel }, interactions: [
      { name: 'open and close detail', run: async () => { await detail.click(); await expect(panel.getByRole('dialog')).toBeVisible(); await panel.getByTestId('policies-sheet-close').click(); await expect(panel.getByRole('dialog')).not.toBeVisible(); await expect(detail).toBeFocused() } },
    ] })
    await detail.click()
    const sheet = panel.getByRole('dialog'), close = panel.getByTestId('policies-sheet-close')
    if (width === 390) {
      await expectStableControls({ controls: { 'policies-sheet-close': close, 'phone-sheet-frame': sheet }, scrollAreas: { body: sheet.locator('.sheet-body') }, interactions: [
        { name: 'scroll long phone detail', run: async () => { await sheet.locator('.sheet-body').evaluate(el => { el.scrollTop = el.scrollHeight }) } },
        { name: 'shorter key detail uses same sheet', run: async () => { await close.click(); await tab('keys').click(); await loaded(); await detail.click(); await expect(sheet).toContainText('Permission registry') } },
        { name: 'static detail uses same sheet', run: async () => { await close.click(); await tab('elsewhere').click(); await loaded(); await detail.click(); await expect(sheet).toContainText('The existing owner decides') } },
      ] })
    }
    await page.keyboard.press('Escape'); await expect(sheet).not.toBeVisible(); await expect(detail).toBeFocused()
    // Arrow keys are local to focused tabs and never replace OS shortcuts.
    await tab('ladders').focus(); await page.keyboard.press('ArrowRight'); await expect(tab('keys')).toBeFocused()
    await page.keyboard.press('Control+ArrowRight'); await expect(tab('keys')).toBeFocused()
    await tab('ladders').click(); await loaded()
    await page.locator('main').evaluate(el => { el.scrollTop = 0 })
    await testInfo.attach(`policies-${width}-${theme}`, { body: await page.screenshot({ fullPage: true }), contentType: 'image/png' })
  })
}

test('Policies settings-only reader sees permission sentences and static ownership', async ({ page }) => {
  const mock = await mockPolicies(page, 'light', true)
  await page.goto('/settings/policies')
  const panel = page.locator('.policies')
  await expect(panel.locator('.policy-content')).toHaveText("You can't see this: it needs See models.")
  await panel.locator('#policy-tab-keys').click()
  await expect(panel.locator('.policy-content')).toHaveText("You can't see this: it needs See roles and a person session.")
  await panel.locator('#policy-tab-elsewhere').click()
  await expect(panel.getByText('Merge queue and repository ownership', { exact: true })).toBeVisible()
  await expect(panel.getByTestId('policies-owner-link')).toHaveCount(0)
  expect(mock.data.calls).toEqual([])
})

test('Policies unseeded registry stays honest and lazy tab responses cannot overwrite a later tab', async ({ page }) => {
  const mock = await mockPolicies(page, 'light')
  mock.data.mode = 'unseeded'
  await page.goto('/settings/policies')
  const panel = page.locator('.policies')
  await expect(panel.getByText('The model registry is not set up yet.', { exact: true })).toBeVisible()
  mock.data.mode = 'failed'
  const held = mock.holdNext()
  await panel.getByTestId('policies-role').filter({ hasText: 'Scout' }).click(); await held.started
  await panel.locator('#policy-tab-elsewhere').click()
  held.release()
  await expect(panel.getByText('Merge queue and repository ownership', { exact: true })).toBeVisible()
  await expect(panel.getByText(/could not be loaded/)).toHaveCount(0)
})
