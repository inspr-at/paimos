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
  await expect(sections(page).getByRole('link')).toHaveText([/^Personal/, /^Theme/, /^Developer/, /^Workspace/, /^Vocabulary/, /^Arten von Arbeit/, /^Access/, /^Policies/, /^Models/, /^Agents/, /^Autopilot/, /^Business/, /^Product portal/])
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

test('members see Personal, Developer and Policies; an admin section explains itself', async ({ page }) => {
  await setup(page, { role: 'member' })
  await page.goto('/settings')
  await expect(page).toHaveURL('/settings/personal')
  // Members get per-person settings, vocabulary and read-only Policies.
  await expect(sections(page).getByRole('link')).toHaveText([/^Personal/, /^Theme/, /^Developer/, /^Vocabulary/, /^Policies/])
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
  // Kinds of work sits with Workspace once models.read is granted, beside Models,
  // Agent rules and Accounts. The count is the whole granted list, in section order.
  await expect(nav.getByRole('link')).toHaveText([/^Personal/, /^Theme/, /^Developer/, /^Workspace/, /^Vocabulary/, /^Arten von Arbeit/, /^Access/, /^Policies/, /^Models/, /^Agents/, /^Agent rules/, /^Accounts/, /^Autopilot/, /^Business/, /^Product portal/])
  await expect(nav.getByRole('link')).toHaveCount(15)
  await expect(nav.getByRole('link', { name: /^Arten von Arbeit/ })).toBeVisible()
  await expect(nav.getByRole('link', { name: /^Models/ })).toBeVisible()
  await expect(nav.getByRole('link', { name: /^Agent rules/ })).toBeVisible()
  await expect(nav.getByRole('link', { name: /^Accounts/ })).toBeVisible()
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

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`members can read long German Vocabulary names without overflow at ${width}px in ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await setup(page, { role: 'member' })
    // Include unbroken compounds at the 60-character limit as well as words
    // separated by spaces: all valid names must remain readable in the card.
    const names = [
      'Arbeitsvorbereitungsbesprechungsdokumentationsverantwortlichkeit',
      'Qualitätssicherungsmaßnahmenkoordination und Arbeitsplanung',
      'Projektentwicklungszusammenarbeitsvereinbarungsdokumentationen',
    ].map(name => name.slice(0, 60))
    await page.route('**/api/settings/work-vocabulary', route => route.fulfill({ json: {
      revision: 1, leaf: { name: names[0], icon: '' },
      levels: names.slice(1).map(name => ({ name, icon: '' })),
    } }))
    await page.goto('/settings/vocabulary')
    await page.evaluate(value => document.documentElement.setAttribute('data-theme', value), theme)
    const card = page.locator('#work-vocabulary')
    await expect(card.locator('.read-only-levels dd')).toHaveText(names)
    await expect(card.locator('input, select, .actions')).toHaveCount(0)
    await expect(card.locator('.preview')).toHaveText(`${names[1]} / ${names[2]} / ${names[0]}`)

    const bounds = await card.evaluate(el => {
      const cardBox = el.getBoundingClientRect()
      return [...el.querySelectorAll('.read-only-levels, .read-only-levels > div, dt, dd, .preview')].map(child => {
        const box = child.getBoundingClientRect()
        return {
          label: `${child.tagName}: ${child.textContent}`,
          width: box.width, left: box.left - cardBox.left, right: cardBox.right - box.right,
          overflow: child.scrollWidth - child.clientWidth,
        }
      })
    })
    expect(bounds).toHaveLength(11)
    for (const bound of bounds) {
      expect(bound.width, bound.label).toBeGreaterThan(0)
      expect(bound.left, bound.label).toBeGreaterThanOrEqual(-.5)
      expect(bound.right, bound.label).toBeGreaterThanOrEqual(-.5)
      expect(bound.overflow, bound.label).toBeLessThanOrEqual(1)
    }
    for (const container of [card, page.locator('.body'), page.locator('main'), page.locator('html')]) {
      expect(await container.evaluate(el => el.scrollWidth - el.clientWidth), 'container horizontal overflow').toBeLessThanOrEqual(1)
    }
    const picker = page.getByRole('button', { name: /^Section:/ })
    const navigation = sections(page)
    const guard = await controlStability(page, width <= 720 ? { picker } : { navigation, vocabulary: navigation.getByRole('link', { name: /^Vocabulary/ }) })
    if (width <= 720) {
      await guard.check(async () => { await picker.click(); await expect(navigation).toBeVisible() })
      await guard.check(async () => { await picker.press('Escape'); await expect(navigation).toBeHidden() })
    } else {
      await guard.check(() => navigation.getByRole('link', { name: /^Vocabulary/ }).hover())
    }
    guard.done()
    await mkdir('test-results/aeon-694-fix3', { recursive: true })
    await page.screenshot({ path: `test-results/aeon-694-fix3/vocabulary-member-${width}-${theme}.png`, fullPage: true })
  })
}

test('old workspace policy bookmarks open the moved card', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/workspace#estimates')
  await expect(page).toHaveURL('/settings/agents#estimates')
  await expect(page.locator('#estimates')).toBeVisible()
  await page.goto('/settings/workspace#status-autopilot')
  await expect(page).toHaveURL('/settings/autopilot#status-autopilot')
  await expect(page.locator('#status-autopilot')).toBeVisible()
})

test('old workspace bookmarks redirect during section and hash changes inside Settings', async ({ page }) => {
  await setup(page)
  await page.goto('/settings/personal')
  // Bookmark arrival deliberately scrolls to its card and rings it. The card
  // renders only once its data is in, and the arrival watcher polls for it, so
  // the scroll trails the card by up to one poll. The ring is the barrier: only
  // after it shows has the scroll happened, and resetting afterwards compares
  // the sticky controls at the same scroll position (a reset before the scroll
  // let the scroll land between reset and measurement; CI saw navigation.y
  // off by 192 px, AEON-724 PR #299).
  async function arrivedThenReset(id: string) {
    await expect(page.locator(`#${id}`)).toBeVisible()
    await expect(page.locator(`#${id}.arrived`)).toBeVisible()
    await page.locator('main').evaluate(el => el.scrollTo(0, 0))
    await expect.poll(() => page.locator('main').evaluate(el => el.scrollTop)).toBe(0)
  }
  const guard = await controlStability(page, { navigation: sections(page), personal: sections(page).getByRole('link', { name: /^Personal/ }) })
  for (const anchor of ['estimates', 'agent-activity', 'silent-sessions']) {
    await guard.check(async () => {
      await page.evaluate(id => import('/src/router.ts').then(({ router }) => router.push(`/settings/workspace?source=bookmark#${id}`)), anchor)
      await expect(page).toHaveURL(`/settings/agents?source=bookmark#${anchor}`)
      await arrivedThenReset(anchor)
    })
  }
  await guard.check(async () => {
    await sections(page).getByRole('link', { name: /^Workspace/ }).click()
    await expect(page).toHaveURL('/settings/workspace')
    await page.evaluate(() => import('/src/router.ts').then(({ router }) => router.push({ hash: '#status-autopilot' })))
    await expect(page).toHaveURL('/settings/autopilot#status-autopilot')
    await arrivedThenReset('status-autopilot')
  })
  await guard.check(async () => {
    await page.evaluate(() => import('/src/router.ts').then(({ router }) => router.push('/settings/workspace#work-vocabulary')))
    await expect(page).toHaveURL('/settings/vocabulary#work-vocabulary')
    await arrivedThenReset('work-vocabulary')
  })
  guard.done()
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
