// SPDX-License-Identifier: AGPL-3.0-only
// Sign-in, the account menu, 404 and the global error page.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

const person = { principal: { id: 'person-1', name: 'mba', roles: ['member'] }, tenant: { id: 'tenant-1', name: 'INSPR Studio' }, identity: { email: 'markus@barta.com', display_name: 'Markus Barta' } }

interface SignInOptions { devMode?: boolean | null; loginStatus?: number; meStatus?: number }
async function mockSignIn(page: Page, options: SignInOptions = {}) {
  const state = { signedIn: false, meStatus: options.meStatus ?? 0 }
  const calls: { path: string; body: string | null }[] = []
  await page.route('**/api/**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname
    calls.push({ path, body: request.postData() })
    if (path === '/api/version') return route.fulfill({ json: { version: '260923120000.0.0', scheme: 'inspr-calendar-v2' } })
    if (path === '/api/me') {
      if (state.meStatus) return route.fulfill({ status: state.meStatus, json: { error: 'unavailable' } })
      if (state.signedIn) return route.fulfill({ json: person })
      const devMode = options.devMode === undefined ? false : options.devMode
      return route.fulfill({ status: 401, json: { error: 'unauthorized', ...(devMode === null ? {} : { dev_mode: devMode }) } })
    }
    if (path === '/api/auth/dev-login') {
      if (options.loginStatus === -1) return route.abort('failed')
      if (options.loginStatus) return route.fulfill({ status: options.loginStatus, json: { error: 'no' } })
      state.signedIn = true
      return route.fulfill({ status: 204 })
    }
    if (path === '/api/projects' || path === '/api/kinds' || path === '/api/views') return route.fulfill({ json: { items: [] } })
    if (path === '/api/nodes') return route.fulfill({ json: { items: [], next_cursor: null } })
    return route.fulfill({ status: 404, json: { error: 'Unmocked route' } })
  })
  return { calls, state }
}

test.describe('sign-in', () => {
  test('one way in: the INSPR ID button on a bare, calm page', async ({ page }) => {
    const errors = watchErrors(page)
    const { calls } = await mockSignIn(page)
    await page.goto('/p/PHAROS')
    await expect(page).toHaveURL('/signin')
    await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible()
    await expect(page.getByRole('link', { name: 'Sign in with INSPR ID' })).toHaveAttribute('href', '/api/auth/login')
    await expect(page.locator('.app-header')).toHaveCount(0)
    await expect(page.getByRole('alert')).toHaveCount(0)
    await expect(page.getByLabel('Email address')).toHaveCount(0)
    // The server said dev_mode is off, so the page never asks the development route.
    expect(calls.filter(call => call.path === '/api/auth/dev-login')).toEqual([])
    await expect(page.locator('.card-foot')).toContainText('PAIMOS AEON')
    expect(errors).toEqual([])
  })

  test('the email form follows dev_mode and never probes the development route', async ({ page }) => {
    const { calls } = await mockSignIn(page, { devMode: true })
    await page.goto('/signin')
    await expect(page.getByLabel('Email address')).toBeVisible()
    await page.getByRole('button', { name: 'Switch to dark theme' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    expect(calls.filter(call => call.path === '/api/auth/dev-login')).toEqual([])
  })

  test('a server that does not report dev_mode shows no email form', async ({ page }) => {
    const { calls } = await mockSignIn(page, { devMode: null })
    await page.goto('/signin')
    await expect(page.getByRole('link', { name: 'Sign in with INSPR ID' })).toBeVisible()
    await expect(page.getByLabel('Email address')).toHaveCount(0)
    expect(calls.filter(call => call.path === '/api/auth/dev-login')).toEqual([])
  })

  const flows: [string, string, string][] = [
    ['denied', 'Sign-in was cancelled', 'Access was declined at INSPR ID'],
    ['expired', 'Your session ended', 'For your security'],
    ['not_member', 'Not a member of this workspace yet', 'Ask its owner for an invitation'],
    ['unavailable', 'Sign-in is not available right now', 'try again in a moment'],
    ['failed', 'Sign-in didn’t finish', 'could not be completed'],
    ['something-new', 'Sign-in didn’t finish', 'could not be completed'],
  ]
  for (const [code, title, body] of flows) {
    test(`?error=${code} explains itself with a retry`, async ({ page }) => {
      await mockSignIn(page)
      await page.goto(`/signin?error=${code}`)
      const alert = page.getByRole('alert')
      await expect(alert).toContainText(title)
      await expect(alert).toContainText(body)
      await expect(alert.getByRole('link', { name: 'Try again' })).toHaveAttribute('href', '/api/auth/login')
      await alert.getByRole('button', { name: 'Dismiss' }).click()
      await expect(page).toHaveURL('/signin')
      await expect(page.getByRole('alert')).toHaveCount(0)
    })
  }

  test('a session that ends while working lands on sign-in as expired', async ({ page }) => {
    await mockWork(page, fixtures())
    await page.goto('/')
    await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
    await page.route('**/api/me', route => route.fulfill({ status: 401, json: { error: 'unauthorized', dev_mode: false } }))
    await page.getByRole('link', { name: /Pharos/ }).first().click()
    await expect(page).toHaveURL('/signin?error=expired&return=/p/PHAROS')
    await expect(page.getByRole('alert')).toContainText('Your session ended')
  })

  test('an unreachable server says so and retries in place', async ({ page }) => {
    const { state } = await mockSignIn(page, { meStatus: 503 })
    await page.goto('/signin')
    const alert = page.getByRole('alert')
    await expect(alert).toContainText('We can’t reach the server')
    state.meStatus = 0
    await alert.getByRole('button', { name: 'Try again' }).click()
    await expect(page.getByRole('alert')).toHaveCount(0)
    await expect(page.getByRole('link', { name: 'Sign in with INSPR ID' })).toBeVisible()
  })

  const devErrors: [number, string][] = [
    [404, 'Development sign-in is switched off on this server.'],
    [400, 'That does not look like an email address.'],
    [500, 'Sign in didn’t complete. Check your email and try again.'],
    [-1, 'We could not reach the server. Check your connection and try again.'],
  ]
  for (const [status, message] of devErrors) {
    test(`development sign-in explains a ${status === -1 ? 'network failure' : status}`, async ({ page }) => {
      await mockSignIn(page, { devMode: true, loginStatus: status })
      await page.goto('/signin')
      await page.getByLabel('Email address').fill('markus@barta.com')
      await page.getByRole('button', { name: 'Continue with email' }).click()
      await expect(page.getByRole('alert')).toHaveText(message)
      await expect(page).toHaveURL('/signin')
    })
  }

  test('development sign-in reaches Projects', async ({ page }) => {
    await mockSignIn(page, { devMode: true })
    await page.goto('/signin')
    await page.getByLabel('Email address').fill('markus@barta.com')
    await page.getByRole('button', { name: 'Continue with email' }).click()
    await expect(page).toHaveURL('/')
    await expect(page.getByRole('button', { name: 'Account for Markus Barta' })).toBeVisible()
  })
})

test.describe('account menu', () => {
  async function openMenu(page: Page) {
    await mockWork(page, fixtures())
    await page.route('**/api/me', route => route.fulfill({ json: person }))
    await page.goto('/')
    await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
    await page.getByRole('button', { name: 'Account for Markus Barta' }).click()
    const menu = page.getByRole('menu', { name: 'Account' })
    await expect(menu).toBeVisible()
    return menu
  }

  test('shows who and where, with theme, version and sign out; shortcuts live in the gear', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'light' })
    const menu = await openMenu(page)
    await expect(menu.locator('.account-name')).toHaveText('Markus Barta')
    await expect(menu.locator('.account-email')).toHaveText('markus@barta.com')
    await expect(menu.locator('.account-tenant')).toHaveText('Workspace: INSPR Studio')
    await expect(menu.locator('[data-version-view="pretty"]')).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: 'Personal settings' })).toBeFocused()
    const system = menu.getByRole('menuitemradio', { name: 'System' })
    await expect(system).toHaveAttribute('aria-checked', 'true')
    await menu.getByRole('menuitemradio', { name: 'Dark' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await expect(menu.getByRole('menuitemradio', { name: 'Dark' })).toHaveAttribute('aria-checked', 'true')
    await menu.getByRole('menuitemradio', { name: 'Light' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
    await menu.getByRole('menuitemradio', { name: 'System' }).click()
    await expect(page.locator('html')).not.toHaveAttribute('data-theme', /.+/)
    await page.getByRole('button', { name: 'App and workspace' }).click()
    await expect(menu).toBeHidden()
    await page.getByRole('menu', { name: 'App and workspace' }).getByRole('menuitem', { name: /Keyboard shortcuts/ }).click()
    await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible()
  })

  test('arrow keys walk the menu and Escape returns to the avatar', async ({ page }) => {
    const menu = await openMenu(page)
    await expect(menu.getByRole('menuitem', { name: 'Personal settings' })).toBeFocused()
    await page.keyboard.press('ArrowDown')
    await expect(menu.getByRole('menuitemradio', { name: 'System' })).toBeFocused()
    await page.keyboard.press('ArrowLeft')
    await expect(menu.getByRole('menuitemradio', { name: 'Dark' })).toBeFocused()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await page.keyboard.press('ArrowRight')
    await expect(menu.getByRole('menuitemradio', { name: 'System' })).toHaveAttribute('aria-checked', 'true')
    await page.keyboard.press('ArrowDown')
    await expect(menu.getByRole('menuitem', { name: 'Sign out' })).toBeFocused()
    await page.keyboard.press('Home')
    await expect(menu.getByRole('menuitem', { name: 'Personal settings' })).toBeFocused()
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()
    await expect(page.getByRole('button', { name: 'Account for Markus Barta' })).toBeFocused()
  })
})

test.describe('404 and errors', () => {
  test('404 names the path and leads back to Projects', async ({ page }) => {
    const errors = watchErrors(page)
    await mockWork(page, fixtures())
    await page.goto('/nope/here')
    await expect(page.getByRole('heading', { name: 'A little off the path.' })).toBeVisible()
    await expect(page.getByRole('main')).toContainText('/nope/here')
    await page.getByRole('link', { name: 'Back to projects' }).click()
    await expect(page).toHaveURL('/')
    await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
    expect(errors).toEqual([])
  })

  test('a page that fails to load shows the error page, never a stack', async ({ page }) => {
    await mockWork(page, fixtures())
    await page.route(url => url.pathname.endsWith('/src/views/AgentsView.vue'), route => route.fulfill({ contentType: 'application/javascript', body: 'throw new TypeError("The agents view could not start")' }))
    await page.goto('/agents')
    await expect(page.getByRole('heading', { name: 'This page stumbled.' })).toBeVisible()
    await expect(page.locator('.app-header')).toBeVisible()
    await page.getByText('Details for support').click()
    const details = page.locator('details')
    await expect(details).toContainText(/Reference\s*E-[0-9A-Z]+/)
    await expect(details).toContainText('TypeError: The agents view could not start')
    await expect(page.getByRole('main')).not.toContainText(/\bat\s+\S+\s*\(|\.vue:\d+|\.js:\d+/)
    await page.getByRole('button', { name: 'Back to projects' }).click()
    await expect(page).toHaveURL('/')
    await expect(page.getByRole('heading', { name: 'Projects', level: 1 })).toBeVisible()
  })

  test('a chunk from an older release reads as an update with Reload', async ({ page }) => {
    await mockWork(page, fixtures())
    await page.goto('/')
    await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
    const chunk = (url: URL) => url.pathname.endsWith('/src/views/ProjectView.vue')
    await page.route(chunk, route => route.abort('failed'))
    await page.getByRole('link', { name: /Pharos/ }).first().click()
    await expect(page.getByRole('heading', { name: 'PAIMOS AEON was updated.' })).toBeVisible()
    await page.unroute(chunk)
    await page.getByRole('button', { name: 'Reload' }).click()
    await expect(page).toHaveURL('/p/PHAROS')
    await expect(page.locator('tr.ticket-row:not(.ghost)')).toHaveCount(5)
  })
})
