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
import { POLICY_ROLES, POLICY_TABS, truncatedLadder } from '../src/lib/policies'

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
      for (const entry of list.getEntries() as unknown as { value: number; hadRecentInput: boolean; sources?: { node: Node | null; previousRect: DOMRectReadOnly; currentRect: DOMRectReadOnly }[] }[]) {
        if (entry.hadRecentInput) continue
        w.__cls += entry.value
        w.__shifts.push(`${entry.value.toFixed(3)} ${(entry.sources ?? []).map(s => {
          const rect = (r: DOMRectReadOnly) => [r.x, r.y, r.width, r.height].map(n => Math.round(n))
          return `${(s.node as Element | null)?.className ?? '?'} ${JSON.stringify(rect(s.previousRect))}→${JSON.stringify(rect(s.currentRect))}`
        }).join(', ')}`)
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
async function base(page: Page, theme: 'light' | 'dark' = 'light') {
  await watchShifts(page)
  const work = fixtures()
  work.preferences.theme = { choice: theme }
  await mockWork(page, work)
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
      for (const sampleWidth of width === 390 ? [390, 1024] : [1440]) for (const theme of ['light', 'dark'] as const) {
        await page.setViewportSize({ width: sampleWidth, height: sampleWidth === 390 ? 844 : 900 })
        await base(page, theme)
        let release!: () => void, started!: () => void
        const held = new Promise<void>(resolve => { release = resolve })
        const requested = new Promise<void>(resolve => { started = resolve })
        await page.route('**/api/me/permissions?project_id=p-pharos', async route => { started(); await held; await route.fallback() })
        await page.goto('/agents/5e000000-0000-4000-8000-000000000001')
        await requested
        // A slow project grant cannot paint a partial panel and add its actions later.
        await expect(page.getByRole('complementary', { name: 'Session details' })).toHaveCount(0)
        release()
        await expect(page.locator('.session-panel .facts')).toBeVisible()
        await expect(page.locator('.session-panel .pause-controls')).toBeVisible()
        await expectStill(page)
        await page.screenshot({ path: test.info().outputPath(`session-loaded-${sampleWidth}-${theme}.png`) })
      }
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
    const capture = async (label: string) => {
      if (width === 390) await page.locator('main').evaluate(main => {
        const policies = main.querySelector('.policies')!
        main.scrollTop += policies.getBoundingClientRect().top - main.getBoundingClientRect().top
      })
      const path = testInfo.outputPath(`policies-${width}-${theme}-${label}.png`)
      await page.screenshot({ path, fullPage: true })
      await testInfo.attach(label, { path, contentType: 'image/png' })
    }
    await loaded()
    await expect(panel.getByRole('list', { name: 'Configured ladder' })).toBeVisible()
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(panel.getByText(/out of credit/)).toBeVisible()
    await capture('ladders')
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
    await capture('agent-key-limits')
    await tab('elsewhere').click(); await loaded()
    const owners = panel.getByTestId('policies-owner-link')
    await expect(owners).toHaveCount(6)
    await expectStableControls({
      controls: { ...common, ...Object.fromEntries(await Promise.all(Array.from({ length: 6 }, async (_, i) => [`policies-owner-link-${i}`, owners.nth(i)] as const))) },
      scrollAreas: { policies: panel },
      interactions: [{ name: 'owner links through hover and focus', run: async () => { await owners.last().hover(); await owners.first().focus() } }],
    })
    await expect(panel.getByRole('article').filter({ hasText: 'Merge queue and repository ownership' })).toContainText('Advisory')
    await capture('elsewhere')
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
        { name: 'loading to truncated', run: async () => { truncation.release(); await expect(content.getByText(truncatedLadder(), { exact: true })).toBeVisible(); await expect(panel.locator('.ladder li')).toHaveCount(50) } },
      ],
    })
    mock.data.count = 50
    const complete = mock.holdNext()
    await role('build').click(); await complete.started
    await expectStableControls({ controls: roleControls, scrollAreas: { policies: panel }, interactions: [
      { name: 'loading to complete', run: async () => { complete.release(); await loaded(); await expect(panel.locator('.ladder li')).toHaveCount(50); await expect(content.getByText(truncatedLadder(), { exact: true })).toHaveCount(0) } },
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
    // A late server refusal clears private rows but preserves the controls
    // already on screen. Measure the whole top block across that transition.
    await expectStableControls({ controls: roleControls, scrollAreas: { policies: panel }, interactions: [
      { name: 'loading to cannot see', run: async () => { denial.release(); await expect(content).toHaveText("You can't see this: it needs See models."); await expect(detail).toBeVisible(); await expect(panel.locator('.ladder')).toHaveCount(0) } },
    ] })
    mock.data.mode = 'loaded'; mock.data.count = 2
    await tab('keys').click(); await loaded()
    await tab('ladders').click(); await loaded()
    // Review gate supplies the long qualification detail for the scroll check.
    await role('review-gate').click(); await loaded()
    await expectStableControls({ controls: { ...common, 'policies-row-link': detail }, scrollAreas: { policies: panel }, interactions: [
      { name: 'open and close detail', run: async () => { await detail.click(); await expect(panel.getByRole('dialog')).toBeVisible(); await panel.getByTestId('policies-sheet-close').click(); await expect(panel.getByRole('dialog')).not.toBeVisible(); await expect(detail).toBeFocused() } },
    ] })
    await detail.click()
    // A short phone viewport makes the real detail overflow, so the scrolling
    // assertion cannot pass merely because assigning scrollTop was a no-op.
    if (width === 390) await page.setViewportSize({ width, height: 600 })
    const sheet = panel.getByRole('dialog'), close = panel.getByTestId('policies-sheet-close')
    await capture('detail-sheet')
    if (width === 390) {
      const frame = (await sheet.boundingBox())!
      for (const [key, expected] of Object.entries({ x: 0, y: 0, width, height: 600 })) {
        expect(Math.abs(frame[key as keyof typeof frame] - expected), `phone sheet ${key}`).toBeLessThanOrEqual(0.5)
      }
      expect(await sheet.locator('.sheet-body').evaluate(el => el.scrollHeight > el.clientHeight)).toBe(true)
      await expectStableControls({ controls: { 'policies-sheet-close': close, 'phone-sheet-frame': sheet }, scrollAreas: { body: sheet.locator('.sheet-body') }, interactions: [
        { name: 'scroll long phone detail', run: async () => { expect(await sheet.locator('.sheet-body').evaluate(el => { el.scrollTop = el.scrollHeight; return el.scrollTop })).toBeGreaterThan(0) } },
        { name: 'shorter key detail uses same sheet', run: async () => { await close.click(); await tab('keys').click(); await loaded(); await detail.click(); await expect(sheet).toContainText('Permission registry') } },
        { name: 'static detail uses same sheet', run: async () => { await close.click(); await tab('elsewhere').click(); await loaded(); await detail.click(); await expect(sheet).toContainText('The existing owner decides') } },
      ] })
    }
    await page.keyboard.press('Escape'); await expect(sheet).not.toBeVisible(); await expect(detail).toBeFocused()
    // Arrow keys are local to focused tabs and never replace OS shortcuts.
    await tab('ladders').focus(); await page.keyboard.press('ArrowRight'); await expect(tab('keys')).toBeFocused()
    await page.keyboard.press('Control+ArrowRight'); await expect(tab('keys')).toBeFocused()
    await tab('ladders').click(); await loaded()
  })
}

test('Policies settings-only reader sees permission sentences and static ownership', async ({ page }) => {
  const mock = await mockPolicies(page, 'light', true)
  await page.goto('/settings/policies')
  const panel = page.locator('.policies')
  await expect(panel.locator('.policy-content')).toHaveText("You can't see this: it needs See models.")
  await expect(panel.getByTestId('policies-role')).toHaveCount(0)
  await expect(panel.getByTestId('policies-row-link')).toHaveCount(0)
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

test('Policies review qualifications appear only for Review gate in the page and sheet', async ({ page }) => {
  await mockPolicies(page, 'light')
  await page.goto('/settings/policies')
  const panel = page.locator('.policies'), notes = panel.locator('.routing-notes')
  for (const role of POLICY_ROLES) {
    await panel.getByTestId('policies-role').getByText(role.label, { exact: true }).click()
    await expect(panel.locator('.policy-content')).toHaveAttribute('aria-busy', 'false')
    const count = role.id === 'review-gate' ? 1 : 0
    await expect(notes.getByText(/Built-in review family order/)).toHaveCount(count)
    await expect(notes.getByText(/A reviewer must be from a different family/)).toHaveCount(count)
    await expect(notes.getByText(/Review dispatch requires a frontier/)).toHaveCount(count)
    await expect(panel.locator('.ladder')).not.toContainText('Enforced')
    await expect(panel.locator('.ladder').locator('..').getByText('Enforced', { exact: true })).toHaveCount(1)
    await panel.getByTestId('policies-row-link').click()
    const sheet = panel.getByRole('dialog')
    await expect(sheet.getByText(/built-in review family order/)).toHaveCount(count)
    await expect(sheet.getByText(/A reviewer must be from a different family/)).toHaveCount(count)
    await expect(sheet.getByText(/Review dispatch requires a frontier/)).toHaveCount(count)
    await expect(sheet.getByText('Dispatch checks platform capability and approved accounts for the work, then applies model preferences.', { exact: true })).toHaveCount(1)
    await panel.getByTestId('policies-sheet-close').click()
  }
})

test('Policies tabs always reference an existing panel and phone selectors use two balanced rows', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockPolicies(page, 'light')
  await page.goto('/settings/policies')
  const panel = page.locator('.policies')
  for (const item of POLICY_TABS) {
    for (const target of POLICY_TABS) {
      const button = panel.locator(`#policy-tab-${target.id}`)
      await expect(button).toHaveAttribute('aria-controls', `policy-panel-${target.id}`)
      await expect(panel.locator(`#policy-panel-${target.id}`)).toHaveCount(1)
      await expect(panel.locator(`#policy-panel-${target.id}`)).toHaveAttribute('aria-labelledby', `policy-tab-${target.id}`)
    }
    await panel.locator(`#policy-tab-${item.id}`).click()
    await expect(panel.getByRole('tabpanel')).toHaveCount(1)
    await expect(panel.getByRole('tabpanel')).toHaveAttribute('id', `policy-panel-${item.id}`)
  }
  await panel.locator('#policy-tab-ladders').click()
  const rows = await panel.getByTestId('policies-role').evaluateAll(elements => {
    const counts = new Map<number, number>()
    for (const element of elements) { const y = element.getBoundingClientRect().y; counts.set(y, (counts.get(y) ?? 0) + 1) }
    return [...counts.values()]
  })
  expect(rows).toEqual([3, 2])
  expect(await panel.locator('.policy-tabs small').evaluateAll(elements => elements.every(el => parseFloat(getComputedStyle(el).fontSize) >= 12))).toBe(true)
  expect(await panel.locator('.role-detail').evaluate(el => parseFloat(getComputedStyle(el).fontSize))).toBeGreaterThanOrEqual(13)
})

test('Policies announces state transitions through a persistent live region', async ({ page }) => {
  const mock = await mockPolicies(page, 'light')
  await page.goto('/settings/policies')
  const panel = page.locator('.policies'), status = panel.getByTestId('policies-announcement')
  await expect(panel.locator('.ladder')).toBeVisible()
  await expect(status).toHaveCount(1)
  const original = await status.elementHandle()
  const initial = mock.holdNext()
  await panel.getByTestId('policies-role').getByText('Build', { exact: true }).click(); await initial.started
  await expect(status).toContainText('Loading the rules')
  initial.release()
  await expect(panel.locator('.ladder')).toBeVisible()
  await expect(status).toHaveCount(1)
  await expect(status).toHaveAttribute('aria-live', 'polite')
  await expect(status).toHaveAttribute('aria-atomic', 'true')
  expect(await status.evaluate((element, before) => element === before, original)).toBe(true)
  mock.data.mode = 'denied'
  const denial = mock.holdNext()
  await panel.getByTestId('policies-role').getByText('Mechanical', { exact: true }).click(); await denial.started
  await expect(status).toContainText('Loading the rules')
  denial.release()
  await expect(status).toHaveText("You can't see this: it needs See models.")
  expect(await status.evaluate((element, before) => element === before, original)).toBe(true)
  mock.data.mode = 'failed'
  await panel.getByTestId('policies-role').getByText('Scout', { exact: true }).click()
  await expect(status).toContainText('could not be loaded')
  expect(await status.evaluate((element, before) => element === before, original)).toBe(true)
})
