// SPDX-License-Identifier: AGPL-3.0-only
// Settings: Personal for everyone (theme, greeting, keys), and Workspace,
// Business and Projects for admins, read from what the server already has.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockSettings, settingsData, type SettingsMockOptions } from './settings-fixtures'
import { accessWorld, mockAccess } from './access-fixtures'

const sections = (page: Page) => page.getByRole('navigation', { name: 'Settings sections' })
async function setup(page: Page, options: SettingsMockOptions & { role?: 'admin' | 'member' } = {}) {
  await mockWork(page, fixtures())
  await mockBusiness(page, businessData({ role: options.role ?? 'admin' }), { role: options.role ?? 'admin' })
  const data = settingsData(options)
  await mockSettings(page, data, options)
  return data
}

test('the account menu opens Settings on Personal: theme, greeting and keys', async ({ page }) => {
  const errors = watchErrors(page)
  const data = await setup(page)
  await page.goto('/')
  await page.getByRole('button', { name: /^Account for / }).click()
  await page.getByRole('menuitem', { name: 'Personal settings' }).click()
  await expect(page).toHaveURL('/settings/personal')
  await expect(page).toHaveTitle(/^Settings · /)
  await expect(sections(page).getByRole('link')).toHaveText([/^Personal/, /^Theme/, /^Developer/, /^Workspace/, /^Access/, /^Business/, /^Projects/, /^Product portal/])
  await expect(sections(page).getByRole('link', { name: /^Personal/ })).toHaveAttribute('aria-current', 'page')

  await page.getByRole('radio', { name: 'Dark' }).click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')

  const greeting = page.getByRole('checkbox', { name: 'Greeting On' })
  await expect(greeting).toBeChecked()
  await greeting.uncheck()
  await expect(page.getByRole('checkbox', { name: 'Greeting Off' })).not.toBeChecked()
  expect(data.patches).toEqual([{ greeting_enabled: false }])
  await expect(page.locator('.toast')).toHaveText(/The greeting is off\./)

  // The profile is editable here (profile.spec covers it in depth).
  await expect(page.getByLabel('First name')).toHaveValue('Markus')
  await page.getByRole('button', { name: 'All shortcuts' }).click()
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible()
  expect(errors).toEqual([])
})

test('a greeting that cannot be saved goes back to how it was', async ({ page }) => {
  await setup(page, { failPatch: true })
  await page.goto('/settings/personal')
  // A click, not uncheck(): the switch flips back as soon as the save fails.
  await page.getByRole('checkbox', { name: 'Greeting On' }).click()
  await expect(page.locator('.toast.error')).toContainText('could not be saved')
  await expect(page.getByRole('checkbox', { name: 'Greeting On' })).toBeChecked()
})

test('members see Personal and Developer; an admin section explains itself', async ({ page }) => {
  await setup(page, { role: 'member' })
  await page.goto('/settings')
  await expect(page).toHaveURL('/settings/personal')
  // Members get both per-person settings sections.
  await expect(sections(page).getByRole('link')).toHaveText([/^Personal/, /^Theme/, /^Developer/])
  await expect(page.getByRole('heading', { name: 'Greeting' })).toBeVisible()
  await page.goto('/settings/workspace')
  await expect(page.getByRole('heading', { name: 'Workspace settings are for workspace admins' })).toBeVisible()
  await expect(page.getByRole('table')).toHaveCount(0)
  await page.keyboard.press('Control+k')
  await page.keyboard.type('workspace settings')
  await expect(page.getByRole('option', { name: /Workspace settings/ })).toHaveCount(0)
})

test('the section nav stays put when switching sections, including wide Access', async ({ page }) => {
  await setup(page)
  await mockAccess(page, accessWorld())
  for (const width of [1440, 1920]) {
    await page.setViewportSize({ width, height: 900 })
    const lefts: number[] = []
    for (const section of ['personal', 'workspace', 'access', 'business', 'projects', 'portal']) {
      await page.goto(`/settings/${section}`)
      const nav = sections(page)
      await expect(nav).toBeVisible()
      lefts.push(Math.round((await nav.boundingBox())!.x))
    }
    expect(new Set(lefts).size, `nav x per section at ${width}px: ${lefts.join(', ')}`).toBe(1)
  }
})

test('Workspace shows the workspace and my role; people and keys moved to Access', async ({ page }) => {
  await setup(page)
  await mockAccess(page, accessWorld({ role: 'admin' }))
  await page.goto('/settings/workspace')
  const workspace = page.locator('#workspace')
  await expect(workspace).toContainText('INSPR Studio')
  // My role comes from /api/me/permissions, not from role strings.
  await expect(workspace.locator('dd').nth(1)).toHaveText('Admin')
  await page.locator('#members').getByRole('link', { name: 'Open Access' }).click()
  await expect(page).toHaveURL('/settings/access')
  await expect(page.getByRole('table', { name: 'People' })).toBeVisible()
})

test('Business shows the parts and the stored quote settings; a deep link rings its card', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/business#quotes')
  const quotes = page.locator('#quotes')
  await expect(quotes).toHaveClass(/arrived/)
  await expect(quotes.getByLabel('Currency')).toHaveValue('EUR')
  await expect(quotes.getByLabel('Company', { exact: true })).toHaveValue('INSPR Studio')
  // The letterhead preview reads the sender as quotes print it.
  await expect(quotes.locator('.paper')).toContainText('INSPR Studio')
  await expect(quotes.locator('.paper')).toContainText('IBAN AT00 0000 0000 0000 0000')
  await expect(page.getByRole('heading', { name: 'Business parts' })).toBeVisible()
  await expect(page.getByRole('checkbox', { name: 'Hours enabled' })).toBeChecked()
})

test('without Quotes the quote settings say so', async ({ page }) => {
  await setup(page, { noQuotes: true })
  await page.goto('/settings/business')
  await expect(page.locator('#quotes')).toContainText('Quote settings show here once Quotes is enabled for this workspace.')
})

test('Projects lists the ticket types with their prefixes', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/projects')
  await expect(page.locator('#ticket-types li')).toHaveCount(3)
  await expect(page.locator('#ticket-types')).toContainText('Epic')
  await expect(page.locator('#ticket-types .prefix')).toHaveText(['EPI', 'TIC', 'TAS'])
})

test('Agents links admins to the agent keys', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockAgents(page, agentData({ me: '11111111-1111-4111-8111-111111111111', projects: { pharos: 'p-pharos', aeon: 'p-aeon', pai: 'p-frozen' }, tickets: { fleet: 'n-1', restore: 'n-2', web: 'n-a1', release: 'n-5', approvals: 'n-6' }, nodes: {}, empty: true }))
  await mockBusiness(page, businessData())
  await mockSettings(page, settingsData())
  await mockAccess(page, accessWorld())
  await page.goto('/agents')
  await page.getByRole('button', { name: 'More agent actions', exact: true }).click()
  await page.getByRole('menuitem', { name: /^Agent keys/ }).click()
  await expect(page).toHaveURL('/settings/access/agents')
  await expect(page.getByRole('list', { name: 'Agents' })).toContainText('aeon-coordinator')
})

test('at 390 the sections sit in a grid and nothing is cut', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  for (const section of ['personal', 'workspace', 'business', 'projects', 'portal']) {
    await page.goto(`/settings/${section}`)
    await expect(page.locator('.settings-card').first()).toBeVisible()
    await page.waitForTimeout(150)
    const cut = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.settings-page *')].filter(el => {
      const r = el.getBoundingClientRect()
      if (r.width <= 1 || r.height <= 1 || getComputedStyle(el).display === 'none') return false
      const c = getComputedStyle(el)
      return r.left < -0.5 || r.right > innerWidth + 0.5 || ((c.overflowX !== 'visible' || c.textOverflow === 'ellipsis') && el.scrollWidth > el.clientWidth + 1)
    }).map(el => el.className || el.tagName))
    expect(cut, section).toEqual([])
  }
})
