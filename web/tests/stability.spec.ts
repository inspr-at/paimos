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
import { POLICY_TABS } from '../src/lib/policies'

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
    const w = window as unknown as { __cls: number; __shifts: string[]; __clsObserver?: PerformanceObserver }
    // Repeated viewport/theme navigations retain init scripts. Install one
    // observer per document so each shift contributes exactly once.
    if (w.__clsObserver) return
    w.__cls = 0; w.__shifts = []
    w.__clsObserver = new PerformanceObserver(list => {
      for (const entry of list.getEntries() as unknown as { value: number; hadRecentInput: boolean; sources?: { node: Node | null; previousRect: DOMRectReadOnly; currentRect: DOMRectReadOnly }[] }[]) {
        if (entry.hadRecentInput) continue
        w.__cls += entry.value
        w.__shifts.push(`${entry.value.toFixed(3)} ${(entry.sources ?? []).map(s => {
          const rect = (r: DOMRectReadOnly) => [r.x, r.y, r.width, r.height].map(n => Math.round(n))
          return `${(s.node as Element | null)?.className ?? '?'} ${JSON.stringify(rect(s.previousRect))}→${JSON.stringify(rect(s.currentRect))}`
        }).join(', ')}`)
      }
    })
    w.__clsObserver.observe({ type: 'layout-shift', buffered: true })
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
// AEON-1204: role ladders moved to Settings › Models. The Ladders tab holds one
// line with a link there and never reaches the retired ladder endpoints.
function ladderRequests(page: Page) {
  const seen: string[] = []
  page.on('request', request => { if (/\/api\/models\/routes(\?|$)/.test(request.url())) seen.push(`${request.method()} ${new URL(request.url()).pathname}`) })
  return seen
}
for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`Policies controls and detail sheet stay put at ${width}px ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    const mock = await mockPolicies(page, theme)
    const ladders = ladderRequests(page)
    await page.goto('/settings/policies')
    const panel = page.locator('.policies')
    const tabs = panel.getByTestId('policies-tab')
    const tab = (id: string) => panel.locator(`#policy-tab-${id}`)
    const detail = panel.getByTestId('policies-row-link')
    const moved = panel.getByTestId('policies-ladders-moved')
    const models = panel.getByRole('link', { name: 'Settings, Models' })
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
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(moved).toContainText('Role ladders moved to')
    await expect(models).toHaveAttribute('href', '/settings/models')
    await expect(detail).toHaveCount(0)
    await expect(panel.getByRole('group', { name: 'Model role' })).toHaveCount(0)
    await expect(panel.getByRole('list', { name: 'Configured ladder' })).toHaveCount(0)
    await expect(content).toHaveText('')
    await capture('ladders')
    const common = Object.fromEntries(POLICY_TABS.map((item, i) => [`policies-tab-${item.id}`, tabs.nth(i)]))
    await expectStableControls({
      controls: common, scrollAreas: { policies: panel, page: page.locator('main') },
      interactions: POLICY_TABS.flatMap(from => POLICY_TABS.filter(to => to.id !== from.id).map(to => ({
        name: `tab ${from.id} to ${to.id}`, run: async () => {
          await tab(from.id).click(); await loaded()
          await tab(to.id).click(); await loaded()
          await expect(tab(to.id)).toHaveAttribute('aria-selected', 'true')
        },
      }))),
    })
    await tab('ladders').click(); await loaded()
    await expectStableControls({
      controls: { ...common, 'policies-ladders-moved': moved, 'policies-ladders-models': models }, scrollAreas: { policies: panel },
      interactions: [{ name: 'moved link through hover and focus', run: async () => { await models.hover(); await models.focus(); await expect(models).toBeFocused() } }],
    })
    // Keys and Rules elsewhere share the detail link position.
    await tab('keys').click(); await loaded()
    await expect(panel.getByText('rules.publish', { exact: true })).toBeVisible()
    await expect(panel.getByText('models.read', { exact: true })).toHaveCount(0)
    await capture('agent-key-limits')
    const withDetail = { ...common, 'policies-row-link': detail }
    await expectStableControls({
      controls: withDetail, scrollAreas: { policies: panel, page: page.locator('main') },
      interactions: [
        { name: 'keys to elsewhere', run: async () => { await tab('elsewhere').click(); await loaded(); await expect(tab('elsewhere')).toHaveAttribute('aria-selected', 'true') } },
        { name: 'elsewhere to keys', run: async () => { await tab('keys').click(); await loaded(); await expect(tab('keys')).toHaveAttribute('aria-selected', 'true') } },
      ],
    })
    // Hold real registry reads at a barrier; no timing sleeps determine transitions.
    mock.data.mode = 'failed'
    const failure = mock.holdNext()
    await tab('elsewhere').click(); await loaded()
    await tab('keys').click(); await failure.started
    await expectStableControls({ controls: withDetail, scrollAreas: { policies: panel }, interactions: [
      { name: 'loading to error', run: async () => { await expect(content).toHaveAttribute('aria-busy', 'true'); failure.release(); await expect(content).toContainText('could not be loaded'); await loaded() } },
    ] })
    mock.data.mode = 'denied'
    const denial = mock.holdNext()
    await tab('elsewhere').click(); await loaded()
    await tab('keys').click(); await denial.started
    // A late server refusal clears private rows but preserves the controls
    // already on screen. Measure the whole top block across that transition.
    await expectStableControls({ controls: withDetail, scrollAreas: { policies: panel }, interactions: [
      { name: 'loading to cannot see', run: async () => { denial.release(); await expect(content).toHaveText("You can't see this: it needs See roles and a person session."); await expect(detail).toBeVisible() } },
    ] })
    mock.data.mode = 'loaded'
    await tab('elsewhere').click(); await loaded()
    const owners = panel.getByTestId('policies-owner-link')
    await expect(owners).toHaveCount(6)
    await expectStableControls({
      controls: { ...common, ...Object.fromEntries(await Promise.all(Array.from({ length: 6 }, async (_, i) => [`policies-owner-link-${i}`, owners.nth(i)] as const))) },
      scrollAreas: { policies: panel },
      interactions: [{ name: 'owner links through hover and focus', run: async () => { await owners.last().hover(); await owners.first().focus() } }],
    })
    await expect(panel.getByRole('article').filter({ hasText: 'Merge queue and repository ownership' })).toContainText('Advisory')
    const roleLadders = panel.getByRole('article').filter({ hasText: 'Role ladders moved to the Models page.' })
    await expect(roleLadders.getByRole('link', { name: 'Open Models' })).toHaveAttribute('href', '/settings/models?layer=rules')
    await expect(panel.getByRole('button', { name: /Open here/ })).toHaveCount(0)
    await capture('elsewhere')
    await expectStableControls({ controls: withDetail, scrollAreas: { policies: panel }, interactions: [
      { name: 'open and close detail', run: async () => { await detail.click(); await expect(panel.getByRole('dialog')).toBeVisible(); await panel.getByTestId('policies-sheet-close').click(); await expect(panel.getByRole('dialog')).not.toBeVisible(); await expect(detail).toBeFocused() } },
    ] })
    await detail.click()
    // A short phone viewport makes the real detail overflow, so the scrolling
    // assertion cannot pass merely because assigning scrollTop was a no-op.
    if (width === 390) await page.setViewportSize({ width, height: 400 })
    const sheet = panel.getByRole('dialog'), close = panel.getByTestId('policies-sheet-close')
    await expect(sheet).toContainText('Model preferences and role order are changed in')
    await capture('detail-sheet')
    if (width === 390) {
      const frame = (await sheet.boundingBox())!
      for (const [key, expected] of Object.entries({ x: 0, y: 0, width, height: 400 })) {
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
    expect(mock.data.calls.every(path => path === '/api/authz/permissions')).toBe(true)
    expect(ladders).toEqual([])
  })
}

test('Policies Ladders tab links to Settings › Models and never calls the ladder endpoints', async ({ page }) => {
  const mock = await mockPolicies(page, 'light')
  const ladders = ladderRequests(page)
  await page.goto('/settings/policies')
  const panel = page.locator('.policies'), content = panel.locator('.policy-content')
  await expect(content).toHaveAttribute('aria-busy', 'false')
  await expect(panel.getByTestId('policies-ladders-moved')).toBeVisible()
  // No edit, Save or Undo control remains anywhere on Policies.
  for (const id of ['policy-edit', 'policy-save', 'policy-cancel', 'policy-undo', 'policy-reload']) await expect(page.getByTestId(id)).toHaveCount(0)
  // A same-person focus refresh and every tab leave the ladder endpoints alone.
  const answer = page.waitForResponse(response => response.url().includes('/api/me/permissions') && response.request().method() === 'GET')
  await page.evaluate(() => window.dispatchEvent(new Event('focus'))); await answer
  for (const item of [...POLICY_TABS, POLICY_TABS[0]]) { await panel.locator(`#policy-tab-${item.id}`).click(); await expect(content).toHaveAttribute('aria-busy', 'false') }
  await expect(panel.getByTestId('policies-ladders-moved')).toBeVisible()
  await panel.getByRole('link', { name: 'Settings, Models' }).click()
  await expect(page).toHaveURL(/\/settings\/models$/)
  expect(ladders).toEqual([])
  expect(mock.data.calls.filter(path => path !== '/api/authz/permissions')).toEqual([])
})

test('Policies settings-only reader sees permission sentences and static ownership', async ({ page }) => {
  const mock = await mockPolicies(page, 'light', true)
  await page.goto('/settings/policies')
  const panel = page.locator('.policies')
  await expect(panel.locator('.policy-content')).toHaveText("You can't see this: it needs See models.")
  await expect(panel.getByTestId('policies-ladders-moved')).toHaveCount(0)
  await expect(panel.getByTestId('policies-row-link')).toHaveCount(0)
  await panel.locator('#policy-tab-keys').click()
  await expect(panel.locator('.policy-content')).toHaveText("You can't see this: it needs See roles and a person session.")
  await panel.locator('#policy-tab-elsewhere').click()
  await expect(panel.getByText('Merge queue and repository ownership', { exact: true })).toBeVisible()
  await expect(panel.getByTestId('policies-owner-link')).toHaveCount(0)
  expect(mock.data.calls).toEqual([])
})

test('Policies lazy tab responses cannot overwrite a later tab', async ({ page }) => {
  const mock = await mockPolicies(page, 'light')
  await page.goto('/settings/policies')
  const panel = page.locator('.policies')
  await expect(panel.getByTestId('policies-ladders-moved')).toBeVisible()
  mock.data.mode = 'failed'
  const held = mock.holdNext()
  await panel.locator('#policy-tab-keys').click(); await held.started
  await panel.locator('#policy-tab-elsewhere').click()
  held.release()
  await expect(panel.getByText('Merge queue and repository ownership', { exact: true })).toBeVisible()
  await expect(panel.getByText(/could not be loaded/)).toHaveCount(0)
})

test('Policies tabs always reference an existing panel', async ({ page }) => {
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
  expect(await panel.locator('.policy-tabs small').evaluateAll(elements => elements.every(el => parseFloat(getComputedStyle(el).fontSize) >= 12))).toBe(true)
  await panel.locator('#policy-tab-ladders').click()
  expect(await panel.getByTestId('policies-ladders-moved').evaluate(el => parseFloat(getComputedStyle(el).fontSize))).toBeGreaterThanOrEqual(13)
})

test('Policies announces state transitions through a persistent live region', async ({ page }) => {
  const mock = await mockPolicies(page, 'light')
  await page.goto('/settings/policies')
  const panel = page.locator('.policies'), status = panel.getByTestId('policies-announcement')
  await expect(panel.getByTestId('policies-ladders-moved')).toBeVisible()
  await expect(status).toHaveCount(1)
  const original = await status.elementHandle()
  const initial = mock.holdNext()
  await panel.locator('#policy-tab-keys').click(); await initial.started
  await expect(status).toContainText('Loading the rules')
  initial.release()
  await expect(panel.getByText('rules.publish', { exact: true })).toBeVisible()
  await expect(status).toHaveCount(1)
  await expect(status).toHaveAttribute('aria-live', 'polite')
  await expect(status).toHaveAttribute('aria-atomic', 'true')
  expect(await status.evaluate((element, before) => element === before, original)).toBe(true)
  mock.data.mode = 'denied'
  const denial = mock.holdNext()
  await panel.locator('#policy-tab-elsewhere').click()
  await panel.locator('#policy-tab-keys').click(); await denial.started
  await expect(status).toContainText('Loading the rules')
  denial.release()
  await expect(status).toHaveText("You can't see this: it needs See roles and a person session.")
  expect(await status.evaluate((element, before) => element === before, original)).toBe(true)
  mock.data.mode = 'failed'
  await panel.locator('#policy-tab-elsewhere').click()
  await panel.locator('#policy-tab-keys').click()
  await expect(status).toContainText('could not be loaded')
  expect(await status.evaluate((element, before) => element === before, original)).toBe(true)
})
