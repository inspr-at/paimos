// SPDX-License-Identifier: AGPL-3.0-only
// Settings: Personal for everyone (theme, greeting, keys), and Workspace,
// Business and Projects for admins, read from what the server already has.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { mockSettings, settingsData, type SettingsMockOptions } from './settings-fixtures'
import { accessWorld, mockAccess } from './access-fixtures'
import { controlStability } from './control-stability'
import { mkdir } from 'node:fs/promises'

const sections = (page: Page) => page.getByRole('navigation', { name: 'Settings sections' })
async function setup(page: Page, options: SettingsMockOptions & { role?: 'admin' | 'member' } = {}) {
  await mockWork(page, fixtures())
  await mockBusiness(page, businessData({ role: options.role ?? 'admin' }), { role: options.role ?? 'admin' })
  const data = settingsData(options)
  await mockSettings(page, data, options)
  await page.route('**/api/settings/eta-interval', route => route.fulfill({ json: { interval_minutes: 10 } }))
  await page.route('**/api/settings/heartbeat-lost', route => route.fulfill({ json: { heartbeat_lost_minutes: 15 } }))
  await page.route('**/api/settings/brand', route => route.fulfill({ json: { short_name: '', logo: null, logo_dark: null } }))
  await page.route('**/api/settings/agent-activity', route => route.fulfill({ json: { mode: 'tool_activity' } }))
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
  await expect(sections(page).getByRole('link')).toHaveText([/^Personal/, /^Theme/, /^Developer/, /^Workspace/, /^Vocabulary/, /^Access/, /^Agents/, /^Autopilot/, /^Business/, /^Product portal/])
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
  await expect(sections(page).getByRole('link')).toHaveText([/^Personal/, /^Theme/, /^Developer/, /^Vocabulary/])
  await expect(page.getByRole('heading', { name: 'Greeting' })).toBeVisible()
  await page.goto('/settings/workspace')
  await expect(page.getByRole('heading', { name: 'Workspace settings are for workspace admins' })).toBeVisible()
  await expect(page.getByRole('table')).toHaveCount(0)
  await page.keyboard.press('Control+k')
  await page.keyboard.type('workspace settings')
  await expect(page.getByRole('option', { name: /Workspace settings/ })).toHaveCount(0)
})

test('grouped navigation stays put across section changes and places policy cards in their new homes', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  await mockAccess(page, accessWorld(), { also: ['account.read', 'rules.read', 'models.read'] })
  await page.goto('/settings/workspace')
  const nav = sections(page)
  await expect(nav.getByRole('link')).toHaveCount(12)
  await expect(nav.locator('.nav-group')).toHaveText(['You', 'Workspace', 'Agents and automation', 'Business'])
  await expect(page.locator('.body > .who')).toHaveText('Admins only see and change this section.')
  await expect(page.locator('#work-vocabulary, #model-refresh, #status-autopilot, #members, #estimates')).toHaveCount(0)
  const guard = await controlStability(page, {
    navigation: nav,
    workspace: nav.getByRole('link', { name: /^Workspace/ }),
    vocabulary: nav.getByRole('link', { name: /^Vocabulary/ }),
    agents: nav.getByRole('link', { name: /^Agents/ }),
    autopilot: nav.getByRole('link', { name: /^Autopilot/ }),
  })
  for (const [label, id] of [['Vocabulary', 'work-vocabulary'], ['Agents', 'estimates'], ['Autopilot', 'status-autopilot'], ['Access', 'access'], ['Personal', 'profile']]) {
    await guard.check(async () => {
      await nav.getByRole('link', { name: new RegExp(`^${label}`) }).click()
      await expect(nav.getByRole('link', { name: new RegExp(`^${label}`) })).toHaveAttribute('aria-current', 'page')
      if (id !== 'access') await expect(page.locator(`#${id}`)).toBeVisible()
      await expect(page.locator('.body > .who')).toBeVisible()
    })
  }
  guard.done()
})

test('Workspace shows the workspace and role, with Brand and In-app AI', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/workspace')
  const workspace = page.locator('#workspace')
  await expect(workspace).toContainText('INSPR Studio')
  await expect(workspace.locator('dd').nth(1)).toHaveText('Admin')
  await expect(page.getByRole('heading', { name: 'Brand', exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'In-app AI', exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'People and agents', exact: true })).toHaveCount(0)
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

test('Vocabulary keeps ticket types and old Projects bookmarks', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/projects')
  await expect(page).toHaveURL('/settings/vocabulary')
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

test('phone section picker overlays content, changes sections, and restores keyboard focus', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  await page.goto('/settings/workspace')
  const picker = page.getByRole('button', { name: /^Section:/ })
  const guard = await controlStability(page, { picker, workspaceCard: page.locator('#workspace') })
  await guard.check(async () => { await picker.click(); await expect(sections(page)).toBeVisible() })
  await guard.check(async () => { await picker.press('Escape'); await expect(sections(page)).toBeHidden() })
  await expect(picker).toBeFocused()
  guard.done()
  const pickerGuard = await controlStability(page, { picker })
  await pickerGuard.check(async () => {
    await picker.click()
    await sections(page).getByRole('link', { name: /^Vocabulary/ }).click()
    await expect(page).toHaveURL('/settings/vocabulary')
    await expect(picker).toHaveAccessibleName('Section: Vocabulary. Choose another section')
    await expect(sections(page)).toBeHidden()
  })
  pickerGuard.done()
  await expect(picker).toBeFocused()
  await picker.click()
  await page.getByRole('heading', { name: 'Ticket types' }).click()
  await expect(sections(page)).toBeHidden()
})

test('members read Vocabulary as text and cannot invoke its write actions', async ({ page }) => {
  await setup(page, { role: 'member' })
  const writes: string[] = []
  page.on('request', request => { if (request.method() === 'PUT' && request.url().includes('work-vocabulary')) writes.push(request.url()) })
  await page.goto('/settings/vocabulary')
  await expect(page.locator('.body > .who')).toHaveText('Everyone sees these names. Only admins change them.')
  await expect(page.locator('#work-vocabulary')).toContainText('Ticket')
  await expect(page.locator('#work-vocabulary input, #work-vocabulary select')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Save names|Add level/ })).toHaveCount(0)
  await page.locator('#work-vocabulary').click()
  await page.keyboard.press('Control+Enter')
  expect(writes).toEqual([])
})

test('old workspace policy bookmarks open the moved card', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/workspace#estimates')
  await expect(page).toHaveURL('/settings/agents#estimates')
  await expect(page.locator('#estimates')).toBeVisible()
  await page.goto('/settings/workspace#status-autopilot')
  await expect(page).toHaveURL('/settings/autopilot#status-autopilot')
  await expect(page.locator('#status-autopilot')).toBeVisible()
})

test('settings layout evidence in light and dark at phone, tablet and desktop sizes', async ({ page }) => {
  await setup(page)
  await mockAccess(page, accessWorld(), { also: ['account.read', 'rules.read', 'models.read'] })
  await mkdir('test-results/aeon-694-shots', { recursive: true })
  for (const width of [390, 1024, 1440]) {
    await page.setViewportSize({ width, height: 900 })
    for (const theme of ['light', 'dark']) {
      await page.addInitScript(value => localStorage.setItem('aeon-theme', value), theme)
      for (const section of ['personal', 'theme', 'developer', 'workspace', 'vocabulary', 'access', 'agents', 'agent-rules', 'accounts', 'autopilot', 'business', 'portal']) {
        await page.goto(`/settings/${section}`)
        await expect(page.locator('.body > .who')).toBeVisible()
        await page.evaluate(value => document.documentElement.setAttribute('data-theme', value), theme)
        await page.screenshot({ path: `test-results/aeon-694-shots/${section}-${width}-${theme}.png` })
        expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), `${section} at ${width}`).toBeLessThanOrEqual(1)
      }
    }
  }
})
