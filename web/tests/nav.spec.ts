// SPDX-License-Identifier: AGPL-3.0-only
// The header's three places (Projects · Agents · Business), the breadcrumb that
// continues from the active one, the g-sequences, and the Business tabs.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { businessData, mockBusiness, type BusinessMockOptions } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

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
  await expect(places(page).getByRole('link')).toHaveText(['Projects', 'Agents', 'Business'])
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
  await expect(crumbs(page)).toHaveText('/PHAROSPharos')

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
  await expect(places(page).getByRole('link')).toHaveText(['Projects', 'Agents'])
})

test('g then b crosses the release menu g opened; another menu, a stale arm or a field does not', async ({ page }) => {
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
  // The arm has expired; g inside Status must not re-arm, so p still stays put.
  await page.waitForTimeout(1600)
  await page.keyboard.press('g')
  await page.keyboard.press('p')
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-11')
  await expect(status).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(status).toHaveCount(0)
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
  await expect(page.getByRole('link', { name: 'Open Business setup' })).toBeVisible()
  await page.getByRole('link', { name: 'Quotes', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Quotes is not enabled for this workspace' })).toBeVisible()
  await page.getByRole('link', { name: 'Open Business setup' }).click()
  await expect(page).toHaveURL('/business')
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
    const cut = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.app-header *, .biz-tabs *')].filter(el => {
      const r = el.getBoundingClientRect()
      // Visually hidden text (a 1px box for screen readers) is not clipped content.
      if (r.width <= 1 || r.height <= 1 || getComputedStyle(el).display === 'none') return false
      return r.left < -0.5 || r.right > innerWidth + 0.5 || el.scrollWidth > el.clientWidth + 1
    }).map(el => el.className || el.tagName))
    expect(cut, path).toEqual([])
  }
})

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
