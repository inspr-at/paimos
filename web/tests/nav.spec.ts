// SPDX-License-Identifier: AGPL-3.0-only
// The header's places (Projects · Agents · Business · Decision Desk, AEON-1214),
// the breadcrumb that continues from the active one, the g-sequences, and the
// Business tabs.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { businessData, mockBusiness, NOW, type BusinessMockOptions } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { expectStableControls } from './helpers/stable'

const places = (page: Page) => page.getByRole('navigation', { name: 'Places' })
const crumbs = (page: Page) => page.getByRole('navigation', { name: 'Breadcrumb' })
async function setup(page: Page, options: BusinessMockOptions = {}) {
  await mockWork(page, fixtures())
  await mockBusiness(page, businessData(options), options)
  await mockSettings(page, settingsData())
}

test('three places in order of use; the active one is highlighted and the breadcrumb continues from it', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await page.goto('/')
  await expect(places(page).getByRole('link')).toHaveText(['Projects', 'Agents', 'Business', /^Decision Desk/])
  await expect(places(page).getByRole('link', { name: 'Projects' })).toHaveAttribute('aria-current', 'page')
  await expect(crumbs(page)).toHaveCount(0)

  await places(page).getByRole('link', { name: 'Business' }).click()
  await expect(page).toHaveURL('/business')
  await expect(places(page).getByRole('link', { name: 'Business' })).toHaveAttribute('aria-current', 'page')
  await page.getByRole('navigation', { name: 'Business' }).getByRole('link', { name: 'Hours' }).click()
  await expect(places(page).getByRole('link', { name: 'Business' })).toHaveAttribute('aria-current', 'true')
  await expect(crumbs(page)).toHaveText('/Hours')

  await page.goto('/p/PHAROS')
  await expect(places(page).getByRole('link', { name: 'Projects' })).toHaveAttribute('aria-current', 'true')
  await expect(crumbs(page).getByRole('link', { name: 'PHAROS Pharos', exact: true })).toHaveAttribute('href', '/p/PHAROS')
  await expect(crumbs(page).getByRole('link', { name: 'PHAROS Pharos', exact: true })).toHaveText('PHAROSPharos')
  await expect(crumbs(page).locator('.phone-header-fold')).toBeHidden()

  // Settings belongs to no place: none is highlighted, the breadcrumb starts at Settings.
  await page.goto('/settings/workspace')
  await expect(places(page).locator('[aria-current]')).toHaveCount(0)
  await expect(crumbs(page)).toHaveText('Settings/Workspace')
  expect(errors).toEqual([])
})

test('Business shows only while one of its parts is open', async ({ page }) => {
  await setup(page, { enabled: [] })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(places(page).getByRole('link')).toHaveText(['Projects', 'Agents', /^Decision Desk/])
})

test('g then b crosses the release menu g opened; another menu blocks a new arm, and an expired arm or a field stays on the ticket', async ({ page }) => {
  await setup(page)
  await page.goto('/p/PHAROS/PHAROS-11')
  const ticket = page.locator('.ticket-ws')
  await expect(ticket).toBeVisible()
  await page.locator('main').focus()
  const release = page.getByRole('dialog', { name: 'Release for PHAROS-11' })
  await page.keyboard.press('g')
  await expect(release).toBeVisible()
  await page.keyboard.press('b')
  await expect(page).toHaveURL('/business')
  await expect(release).toHaveCount(0)

  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(ticket).toBeVisible()
  await page.locator('main').focus()
  await page.keyboard.press('g')
  await expect(release).toBeVisible()
  await page.getByRole('button', { name: 'Status: In progress. Change status of PHAROS-11' }).click()
  const status = page.getByRole('menu', { name: 'Status of PHAROS-11' })
  await expect(status).toBeVisible()
  await expect(release).toHaveCount(0)
  await page.keyboard.press('p')
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11')
  await expect(status).toBeVisible()
  // Status is another menu, so g inside it never arms and p stays on the ticket.
  // The expired arm is separate: g opens Release, the clock jumps 1.6s, and b stays.
  await page.keyboard.press('g')
  await page.keyboard.press('p')
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11')
  await expect(status).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(status).toHaveCount(0)
  await page.locator('main').focus()
  await page.keyboard.press('g')
  await expect(release).toBeVisible()
  await page.clock.setFixedTime(NOW.getTime() + 1_600)
  await page.keyboard.press('b')
  // The URL is already the ticket, so a router.push would pass an immediate check.
  await page.waitForTimeout(1_000)
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11')
  await page.keyboard.press('Escape')
  await expect(release).toHaveCount(0)
  await page.locator('main').focus()
  await page.keyboard.press('g')
  await expect(release).toBeVisible()
  const comment = ticket.getByLabel('Add a comment')
  await comment.click()
  await expect(release).toHaveCount(0)
  await page.keyboard.press('p')
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11')
  await expect(comment).toHaveValue('p')
})

test('g p, g a and g b go to the places; typing in a field never does', async ({ page }) => {
  await setup(page)
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(page.locator('.ticket-ws')).toBeVisible()
  await page.locator('main').focus()
  await page.keyboard.press('g'); await page.keyboard.press('b')
  await expect(page).toHaveURL('/business')
  await page.keyboard.press('g'); await page.keyboard.press('a')
  await expect(page).toHaveURL('/agents')
  await page.keyboard.press('g'); await page.keyboard.press('p')
  await expect(page).toHaveURL('/')
  await page.getByRole('searchbox', { name: 'Filter projects' }).click()
  await page.keyboard.type('gb')
  await expect(page).toHaveURL('/')
  await expect(page.getByRole('searchbox', { name: 'Filter projects' })).toHaveValue('gb')
})

test('the palette offers the places with their keys and every settings section by name', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/personal')
  await expect(page.getByRole('heading', { name: 'Greeting' })).toBeVisible()
  await page.keyboard.press('Control+k')
  const actions = page.getByRole('dialog', { name: 'Search and commands' }).getByRole('group', { name: 'Actions' })
  await expect(actions.getByRole('option', { name: /Go to Projects/ })).toContainText('gp')
  await expect(actions.getByRole('option', { name: /Go to Business/ })).toContainText('gb')
  // Already in Settings: the Settings action steps aside, the sections are found by name.
  await expect(actions.getByRole('option', { name: /Workspace settings/ })).toHaveCount(0)
  await page.keyboard.type('workspace settings')
  await actions.getByRole('option', { name: /Workspace settings/ }).click()
  await expect(page).toHaveURL('/settings/workspace')
})

test('Business tabs: a closed Customers or Quotes says how to open it; earlier links lead there', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await page.goto('/business/customers')
  await expect(page.getByRole('navigation', { name: 'Business' }).getByRole('link')).toHaveText(['Overview', 'Customers', 'Quotes', 'Hours', 'Rates'])
  await expect(page.getByRole('navigation', { name: 'Business' }).getByRole('link', { name: 'Customers' })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByRole('heading', { name: 'Customers is not enabled for this workspace' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Set up in Settings › Business' })).toBeVisible()
  await page.getByRole('link', { name: 'Quotes', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Quotes is not enabled for this workspace' })).toBeVisible()
  // AEON-1189: every setup path ends in Settings › Business.
  await page.getByRole('link', { name: 'Set up in Settings › Business' }).click()
  await expect(page).toHaveURL('/settings/business#parts')
  await expect(page.locator('#parts').getByRole('checkbox', { name: 'Quotes enabled' })).toBeVisible()
  await page.goto('/crm')
  await expect(page).toHaveURL('/business/customers')
  expect(errors).toEqual([])
})

test('members see the Business tabs without the admin settings links', async ({ page }) => {
  await setup(page, { role: 'member' })
  await page.goto('/business/quotes')
  await expect(page.getByRole('heading', { name: 'Quotes is not enabled for this workspace' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Quote settings' })).toHaveCount(0)
})

test('on phones the places are round buttons and nothing in the header is cut', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  for (const path of ['/', '/p/PHAROS/PHAROS-11?view=full', '/business/customers', '/settings/workspace', '/agents']) {
    await page.goto(path)
    await expect(places(page)).toBeVisible()
    for (const link of await places(page).getByRole('link').all()) {
      const box = (await link.boundingBox())!
      expect(box.width).toBeGreaterThanOrEqual(44); expect(box.height).toBeGreaterThanOrEqual(44)
    }
    // The header measures its breadcrumb over animation frames after navigation
    // and font changes. Check the settled layout, retaining every clipping check.
    await expect.poll(() => page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.app-header *, .biz-tabs *')].filter(el => {
      const r = el.getBoundingClientRect()
      // Visually hidden text (a 1px box for screen readers) is not clipped content.
      if (r.width <= 1 || r.height <= 1 || getComputedStyle(el).display === 'none') return false
      return r.left < -0.5 || r.right > innerWidth + 0.5 || el.scrollWidth > el.clientWidth + 1
    }).map(el => ({ tag: el.tagName, class: el.className, text: el.textContent, width: el.clientWidth, contentWidth: el.scrollWidth, html: el.outerHTML }))), { message: path }).toEqual([])
  }
})

// Each layout has its own fixture and budget; none of the phone clipping or
// German-label stability checks share one cumulative 30 s deadline.
for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`Business tab labels fit and controls stay still at ${width} ${theme}`, async ({ page }, testInfo) => {
    await setup(page)
    // AEON-1313: platform font differences hid the clipped Customers label on
    // macOS. Exercise intrinsic label room with German length stress as well.
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/business/customers')
    const tabs = page.getByRole('navigation', { name: 'Business' })
    await expect(tabs.getByRole('link')).toHaveCount(5)
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await tabs.locator('.biz-tab > span').evaluateAll(elements => {
      const labels = ['Übersicht', 'Kundenverwaltung', 'Angebotsverwaltung', 'Zeiterfassung', 'Verrechnungssätze']
      elements.forEach((element, index) => { element.textContent = labels[index]! })
    })
    await page.evaluate(() => document.fonts.ready)
    const cut = await tabs.locator('.biz-tab > span').evaluateAll(elements => elements.filter(element => element.scrollWidth > element.clientWidth).map(element => ({ text: element.textContent, width: element.clientWidth, contentWidth: element.scrollWidth })))
    expect(cut, `${width} ${theme}: complete Business tab labels`).toEqual([])
    const links = await tabs.getByRole('link').all()
    await expectStableControls({
      controls: { tabs, ...Object.fromEntries(links.map((link, index) => [`tab ${index}`, link])) },
      interactions: links.map((link, index) => ({ name: `hover tab ${index}`, run: () => link.hover() })),
    })
    await page.screenshot({ path: testInfo.outputPath(`business-nav-${width}-${theme}.png`) })
  })
}

test('at 390 the ticket fits: previous and next fold into More, chips and screens wrap', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  // Without the business mock: its fixed clock trips Vue's event-timestamp guard on the
  // ticket panel's capture listener, which no real browser clock does.
  await mockWork(page, fixtures())
  for (const path of ['/p/PHAROS/PHAROS-11?view=full', '/p/PHAROS/PHAROS-11']) {
    await page.goto(path)
    await expect(page.locator('.ticket-ws .props.row')).toBeVisible()
    await page.waitForTimeout(300)
    const cut = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.ticket-ws *')].filter(el => {
      const r = el.getBoundingClientRect(), c = getComputedStyle(el)
      if (r.width <= 1 || r.height <= 1 || c.display === 'none' || el.closest('svg')) return false
      return r.left < -0.5 || r.right > innerWidth + 0.5 || ((c.overflowX === 'auto' || c.overflowX === 'scroll') && el.scrollWidth > el.clientWidth + 1)
    }).map(el => `${el.tagName}.${el.className}`))
    expect(cut, path).toEqual([])
  }
  await expect(page.getByRole('button', { name: 'Next ticket' })).toBeHidden()
  await page.getByRole('button', { name: 'More actions' }).click()
  await page.getByRole('menuitem', { name: 'Next ticket' }).click()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-12')
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: header places and the business tabs in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    await setup(page)
    await page.goto('/business/quotes')
    await expect(page.getByRole('heading', { name: 'Quotes is not enabled for this workspace' })).toBeVisible()
    await page.waitForTimeout(250)
    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
    const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')} — ${n.failureSummary?.split('\n').slice(1, 2).join(' ').trim()}`).join('\n')}`)
    expect(summary, summary.join('\n')).toEqual([])
  })
}
