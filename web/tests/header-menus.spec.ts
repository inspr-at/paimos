// SPDX-License-Identifier: AGPL-3.0-only
// AEON-312: the header's gear menu (the app and the workspace) and avatar menu
// (me), for an admin, a member and a guest, on wide screens and phones.
//
// HEADER_SHOTS=<dir> also writes screenshots of every menu at 375, 390 and 1600,
// light and dark, for review by eye.
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page, type Route } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { mockGuestPermissions } from './authz-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

type Role = 'admin' | 'member' | 'guest'
interface World { sent: Record<string, unknown>[]; ready: number | 'fail'; owner: boolean; business: boolean }

async function signIn(page: Page, role: Role, world: Partial<World> = {}): Promise<World & { history: ReturnType<typeof releaseHistory>; releaseState: { server: string } }> {
  const state: World = { sent: [], ready: 200, owner: false, business: false, ...world }
  await mockWork(page, fixtures(), role === 'admin' ? { admin: true } : role === 'guest' ? { readOnly: true } : {})
  if (state.business) { await mockBusiness(page, businessData()); await mockSettings(page, settingsData()) }
  if (role === 'guest') {
    await page.route('**/api/me/permissions*', route => route.fulfill({ json: mockGuestPermissions(new URL(route.request().url()).searchParams.get('project_id') ?? undefined, ['p-pharos']) }))
    await page.route('**/api/me', route => route.fulfill({ json: { principal: { id: me.id, name: me.name, kind: 'person', roles: [] }, tenant: { id: 't1', name: 'INSPR Studio' } } }))
  }
  const history = releaseHistory()
  const current = history.releases.find(release => release.version === history.current)!
  Object.assign(current, {
    notes: {
      source: 'snapshot', snapshot_sha256: 'x', captured_at: new Date().toISOString(), release_revision: 1, gaps: [], hidden: 0,
      items: [
        { id: 'n1', key: 'AEON-279', pill_en: 'Guaranteed inbox delivery', pill_de: '', benefit_en: 'Messages to an agent arrive even when it restarts.', benefit_de: '' },
        { id: 'n2', key: 'AEON-289', pill_en: 'Release notes with benefits', pill_de: '', benefit_en: 'Every release says what it changes for you.', benefit_de: '' },
      ],
    },
  })
  const releaseState = await mockReleases(page, history)
  Object.assign(state, { history, releaseState })
  await page.route('**/api/health', route => route.fulfill({ json: { status: 'ok', db: 'ok' } }))
  await page.route('**/api/ready', route => state.ready === 'fail' ? route.abort() : route.fulfill({ status: state.ready, json: { status: state.ready === 200 ? 'ready' : 'unavailable' } }))
  await page.route('**/api/inbox/feedback-recipient', route => state.owner
    ? route.fulfill({ status: 404, json: { code: 'not_found', message: 'not found' } })
    : route.fulfill({ json: { principal_id: '99999999-9999-4999-8999-999999999999', name: 'Ada Lindqvist' } }))
  await page.route('**/api/inbox/messages', route => {
    if (route.request().method() !== 'POST') return route.fallback()
    state.sent.push(route.request().postDataJSON())
    return route.fulfill({ status: 201, json: { id: 'm1' } })
  })
  return state as World & { history: ReturnType<typeof releaseHistory>; releaseState: { server: string } }
}
async function home(page: Page) {
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
}
const gear = (page: Page) => page.getByRole('button', { name: 'App and workspace' })
const avatar = (page: Page) => page.getByRole('button', { name: `Account for ${me.name}` })
async function openGear(page: Page) {
  await gear(page).click()
  const menu = page.getByRole('menu', { name: 'App and workspace' })
  await expect(menu).toBeVisible()
  await expect(menu.getByRole('menuitem', { name: /^System status, Operational/ })).toBeVisible()
  return menu
}
async function openAccount(page: Page) {
  await avatar(page).click()
  const menu = page.getByRole('menu', { name: 'Account' })
  await expect(menu).toBeVisible()
  return menu
}
const names = (page: Page, menu: string) => page.getByRole('menu', { name: menu }).getByRole('menuitem').evaluateAll(items => items.map(item => (item.getAttribute('aria-label') ?? item.querySelector('.hm-text')?.textContent ?? item.textContent ?? '').trim()))

// The address is `releases=all` only on the way to the running version unless
// the history stays there. Wait until the sheet's own requests have finished,
// then read the settled URL and the heading that is actually on screen.
async function expectFullHistory(page: Page) {
  const history = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
  await expect(history.getByRole('listbox', { name: 'Releases, newest first' })).toBeVisible()
  await page.waitForLoadState('networkidle')
  await expect(page).toHaveURL(/[?&]releases=all(?:&|#|$)/)
  await expect(history.getByRole('heading', { level: 1, name: 'PAIMOS AEON releases' })).toBeVisible()
  await expect(history.getByRole('option', { selected: true })).toHaveCount(0)
  return history
}

// Hold /version and the history until the test lets them answer, so a click can
// land before either is known. Registered after the page's own mocks, which then
// fulfill the request.
async function holdReleaseApis(page: Page, releases?: (route: Route) => Promise<void>) {
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  const wait = async (route: Route) => { await gate; await route.fallback() }
  await page.route('**/api/version**', wait)
  await page.route('**/api/releases**', releases ? async route => { await gate; await releases(route) } : wait)
  return release
}

test.describe('release history before the version has loaded', () => {
  for (const width of [1440, 390]) {
    test(`the footer pill still opens the running release at ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width < 600 ? 844 : 900 })
      const state = await signIn(page, 'member')
      const release = await holdReleaseApis(page)
      await page.goto('/', { waitUntil: 'domcontentloaded' })
      await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
      const pill = page.locator('footer.app-footer .version-pill')
      await expect(pill).toHaveAccessibleName('Release history')
      await pill.click()
      await expect(page).toHaveURL(/[?&]releases=current(?:&|#|$)/)
      const history = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
      await expect(history).toBeVisible()
      await expect(history.locator('[role="option"][aria-selected="true"]')).toHaveCount(0)
      // The list stays up until the running release is known. A phone must not
      // cover it with an empty detail while the history is still on its way.
      if (width < 600) {
        await expect(history.locator('.shell')).not.toHaveClass(/show-detail/)
        await expect(history.getByRole('status', { name: 'Loading the release history' })).toBeVisible()
      }
      release()
      await page.waitForLoadState('networkidle')
      const version = state.history.current
      await expect(page).toHaveURL(new RegExp(`[?&]releases=${version.replace(/\./g, '\\.')}(?:&|#|$)`))
      await expect(history.locator('[role="option"][aria-selected="true"]')).toHaveCount(1)
      await expect(history.locator('[role="option"][aria-selected="true"]')).toHaveAttribute('id', `release-${version.replace(/\./g, '-')}`)
      await expect(history.getByRole('heading', { level: 2, name: version })).toBeVisible()
      if (width < 600) await expect(history.locator('.shell')).toHaveClass(/show-detail/)
    })

    test(`header release history stays on the full list at ${width}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width < 600 ? 844 : 900 })
      await signIn(page, 'member')
      const release = await holdReleaseApis(page)
      await page.goto('/', { waitUntil: 'domcontentloaded' })
      await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
      const menu = await openAccount(page)
      await menu.getByRole('menuitem', { name: 'Release history', exact: true }).click()
      await expect(page).toHaveURL(/[?&]releases=all(?:&|#|$)/)
      release()
      const history = await expectFullHistory(page)
      if (width < 600) await expect(history.locator('.shell')).not.toHaveClass(/show-detail/)
    })
  }

  test('at 390 a footer click keeps the error and retry when the history fails', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await signIn(page, 'member')
    const release = await holdReleaseApis(page, route => route.fulfill({ status: 503, json: { error: 'The release history could not be loaded.' } }))
    await page.goto('/', { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
    const pill = page.locator('footer.app-footer .version-pill')
    await expect(pill).toHaveAccessibleName('Release history')
    await pill.click()
    await expect(page).toHaveURL(/[?&]releases=current(?:&|#|$)/)
    release()
    const history = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
    await expect(history).toBeVisible()
    await expect(history.locator('.shell')).not.toHaveClass(/show-detail/)
    await expect(history.getByRole('heading', { name: 'The release history could not be loaded.' })).toBeVisible()
    await expect(history.getByRole('button', { name: 'Try again' })).toBeVisible()
  })

  test('at 390 a footer click keeps the empty history when this build has none', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await signIn(page, 'member')
    const release = await holdReleaseApis(page, route => route.fulfill({
      json: {
        schema: 'inspr.release-history.v1', product: 'PAIMOS AEON', repository: '', version_scheme: 'inspr-calendar-v2',
        generated_at: '0001-01-01T00:00:00Z', source: 'none', current: 'dev', live_since: new Date().toISOString(), releases: [],
      },
    }))
    await page.goto('/', { waitUntil: 'domcontentloaded' })
    await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
    const pill = page.locator('footer.app-footer .version-pill')
    await expect(pill).toHaveAccessibleName('Release history')
    await pill.click()
    await expect(page).toHaveURL(/[?&]releases=current(?:&|#|$)/)
    release()
    const history = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
    await expect(history).toBeVisible()
    await expect(history.locator('.shell')).not.toHaveClass(/show-detail/)
    await expect(history.getByRole('heading', { name: 'No release history in this build' })).toBeVisible()
  })
})

test.describe('gear menu', () => {
  test('an admin sees the workspace, agents, help and a healthy system', async ({ page }) => {
    const errors = watchErrors(page)
    await signIn(page, 'admin')
    await home(page)
    const menu = await openGear(page)
    const items = await names(page, 'App and workspace')
    expect(items.slice(0, 7)).toEqual(['Workspace settings', 'Connect a computer', 'Agent keys', 'Inbox hooks', expect.stringMatching(/^Release history/), 'Keyboard shortcuts', 'Help & feedback'])
    await expect(menu.getByRole('group', { name: 'Agents' }).getByRole('menuitem')).toHaveCount(3)
    expect(items[7]).toMatch(/^System status, Operational, version \d{12}\.\d+\.\d+, deployed 50 min ago$/)
    await expect(menu.getByRole('menuitem', { name: 'Workspace settings' })).toBeFocused()
    await menu.getByRole('menuitem', { name: 'Workspace settings' }).click()
    await expect(page).toHaveURL('/settings/workspace')
    await expect(menu).toBeHidden()
    expect(errors).toEqual([])
  })

  test('a member has no workspace settings or keys, and connects a computer', async ({ page }) => {
    await signIn(page, 'member')
    await home(page)
    await openGear(page)
    const items = await names(page, 'App and workspace')
    expect(items).not.toContain('Workspace settings')
    expect(items).not.toContain('Agent keys')
    expect(items[0]).toBe('Connect a computer')
    await page.getByRole('menuitem', { name: 'Connect a computer' }).click()
    await expect(page).toHaveURL('/agents/register-agent')
  })

  test('a guest sees only the app: releases, shortcuts, help and status', async ({ page }) => {
    await signIn(page, 'guest')
    await home(page)
    await openGear(page)
    const items = await names(page, 'App and workspace')
    expect(items.slice(0, 3)).toEqual([expect.stringMatching(/^Release history/), 'Keyboard shortcuts', 'Help & feedback'])
    expect(items).toHaveLength(4)
    // No inbox.send: help without the feedback form.
    await page.getByRole('menuitem', { name: 'Help & feedback' }).click()
    const pane = page.getByRole('dialog', { name: 'Help and feedback' })
    await expect(pane.getByRole('heading', { name: 'What’s new' })).toBeVisible()
    await expect(pane.getByRole('heading', { name: 'Send feedback' })).toHaveCount(0)
  })

  test('keys walk the menu, Escape gives focus back to the gear', async ({ page }) => {
    await signIn(page, 'member')
    await home(page)
    await gear(page).focus()
    await page.keyboard.press('ArrowDown')
    const menu = page.getByRole('menu', { name: 'App and workspace' })
    await expect(menu.getByRole('menuitem', { name: 'Connect a computer' })).toBeFocused()
    await page.keyboard.press('ArrowDown')
    await expect(menu.getByRole('menuitem', { name: 'Inbox hooks' })).toBeFocused()
    await page.keyboard.press('ArrowDown')
    await expect(menu.getByRole('menuitem', { name: /^Release history/ })).toBeFocused()
    await page.keyboard.press('End')
    await expect(menu.getByRole('menuitem', { name: /^System status/ })).toBeFocused()
    await page.keyboard.press('ArrowDown')
    await expect(menu.getByRole('menuitem', { name: 'Connect a computer' })).toBeFocused()
    await page.keyboard.press('ArrowUp')
    await expect(menu.getByRole('menuitem', { name: /^System status/ })).toBeFocused()
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()
    await expect(gear(page)).toBeFocused()
    // Keyboard shortcuts from the menu opens the sheet.
    await page.keyboard.press('Enter')
    await page.getByRole('menuitem', { name: 'Keyboard shortcuts' }).click()
    await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible()
  })

  test('the status says so in words when the server is not ready', async ({ page }) => {
    await signIn(page, 'member', { ready: 503 })
    await home(page)
    await gear(page).click()
    const status = page.getByRole('menuitem', { name: /^System status/ })
    await expect(status).toHaveAccessibleName(/^System status, Degraded, The server is not ready for requests\./)
    await expect(status).toContainText('Degraded')
  })

  test('help shows what is new and sends feedback to the owner’s inbox', async ({ page }) => {
    const state = await signIn(page, 'member')
    await home(page)
    await openGear(page)
    await page.getByRole('menuitem', { name: 'Help & feedback' }).click()
    const pane = page.getByRole('dialog', { name: 'Help and feedback' })
    await expect(pane.getByRole('button', { name: 'Back to the menu' })).toBeFocused()
    await expect(pane).toContainText('Guaranteed inbox delivery')
    await expect(pane).toContainText('Every release says what it changes for you.')
    const text = pane.getByLabel('Feedback for Ada Lindqvist')
    await text.fill('The gear menu is lovely.')
    await pane.getByRole('button', { name: 'Send' }).click()
    await expect(pane.getByRole('status')).toHaveText('Sent to Ada Lindqvist. Thank you.')
    expect(state.sent).toHaveLength(1)
    expect(state.sent[0]).toMatchObject({ recipient_principal_id: '99999999-9999-4999-8999-999999999999', idempotency_key: expect.any(String) })
    expect(String(state.sent[0].body)).toMatch(/^Feedback from the app \(on \/, version \d{12}\.\d+\.\d+\)\n\nThe gear menu is lovely\.$/)
    // Back returns to the menu on the Help row; What's new opens the release.
    await pane.getByRole('button', { name: 'Back to the menu' }).click()
    await expect(page.getByRole('menuitem', { name: 'Help & feedback' })).toBeFocused()
    await page.getByRole('menuitem', { name: 'Help & feedback' }).click()
    await page.getByRole('button', { name: 'See this release' }).click()
    await expect(page).toHaveURL(/[?&]releases=\d{12}\.\d+\.\d+/)
  })

  test('the only owner is told feedback comes to them', async ({ page }) => {
    await signIn(page, 'admin', { owner: true })
    await home(page)
    await openGear(page)
    await page.getByRole('menuitem', { name: 'Help & feedback' }).click()
    await expect(page.getByRole('dialog', { name: 'Help and feedback' })).toContainText('You own this workspace, so your team’s feedback comes to you.')
  })

  test('readiness that cannot be read is never Operational', async ({ page }) => {
    await signIn(page, 'member', { ready: 'fail' })
    await home(page)
    await gear(page).click()
    const status = page.getByRole('menuitem', { name: /^System status/ })
    await expect(status).toHaveAccessibleName(/^System status, Unavailable, Readiness could not be checked\./)
    await expect(status).not.toContainText('Operational')
  })

  test('status reads fresh on every open: a deploy since the page loaded shows, and the time ticks', async ({ page }) => {
    await page.clock.install()
    const state = await signIn(page, 'member')
    await home(page)
    const first = await openGear(page)
    await expect(first.getByRole('menuitem', { name: /^System status/ })).toHaveAccessibleName(new RegExp(`version ${state.history.current.replace(/\./g, '\\.')}, deployed 50 min ago$`))
    // Two minutes on, the elapsed time follows without reopening.
    await page.clock.fastForward('02:00')
    await expect(first.getByRole('menuitem', { name: /^System status/ })).toHaveAccessibleName(/deployed 52 min ago$/)
    await page.keyboard.press('Escape')
    // A new version goes live: the next open shows it, not the cached one.
    state.releaseState.server = '260929235900.0.0'
    const browserNow = await page.evaluate(() => Date.now())
    Object.assign(state.history, { current: '260929235900.0.0', live_since: new Date(browserNow - 60_000).toISOString() })
    await gear(page).click()
    await expect(page.getByRole('menuitem', { name: /^System status/ })).toHaveAccessibleName(/version 260929235900\.0\.0, deployed 1 min ago$/)
  })

  test('inbox hooks: the one command per harness, copyable, with the setup guide', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await signIn(page, 'member')
    await home(page)
    await openGear(page)
    await page.getByRole('menuitem', { name: 'Inbox hooks' }).click()
    const pane = page.getByRole('dialog', { name: 'Agent inbox hooks' })
    await expect(pane.getByRole('button', { name: 'Back to the menu' })).toBeFocused()
    await expect(pane).toContainText('Messages reach your agent at every turn')
    await expect(pane.locator('code')).toHaveText(['aeon hook install --harness claude --scope user', 'aeon hook install --harness codex --scope user'])
    await pane.getByRole('button', { name: 'Copy the Codex command' }).click()
    await expect(pane.getByRole('status')).toHaveText('Codex command copied')
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('aeon hook install --harness codex --scope user')
    await expect(pane.getByRole('link', { name: 'Setup guide' })).toHaveAttribute('href', 'https://github.com/inspr-at/aeon/blob/main/docs/AGENT_INTEGRATION.md#operator-installed-turn-boundary-hooks-aeon-281')
    await pane.getByRole('button', { name: 'Back to the menu' }).click()
    await expect(page.getByRole('menuitem', { name: 'Inbox hooks' })).toBeFocused()
  })
})

test.describe('avatar menu', () => {
  test('me: personal settings, theme in sync with the moon, sign out and the version', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'light' })
    await signIn(page, 'member')
    await home(page)
    const menu = await openAccount(page)
    await expect(menu.getByRole('menuitem', { name: 'Personal settings' })).toBeFocused()
    await expect(menu.getByRole('menuitemradio', { name: 'System' })).toHaveAttribute('aria-checked', 'true')
    await expect(menu.getByRole('menuitem', { name: /^\d{12}\.0\.0 · .* — Copy version$/ })).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: 'Release history' })).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: 'Sign out' })).toBeVisible()
    // Workspace things are not here.
    await expect(menu.getByRole('menuitem', { name: /Keyboard shortcuts|Workspace settings/ })).toHaveCount(0)
    await menu.getByRole('menuitemradio', { name: 'Dark' }).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await page.keyboard.press('Escape')
    await expect(avatar(page)).toBeFocused()
    // The moon toggle and the menu show the same choice.
    await page.getByRole('button', { name: 'Switch to light theme' }).click()
    await openAccount(page)
    await expect(page.getByRole('menuitemradio', { name: 'Light' })).toHaveAttribute('aria-checked', 'true')
    await page.getByRole('menuitem', { name: 'Personal settings' }).click()
    await expect(page).toHaveURL('/settings/personal')
  })

  test('arrows walk the rows, left and right choose the theme', async ({ page }) => {
    await signIn(page, 'member')
    await home(page)
    const menu = await openAccount(page)
    await page.keyboard.press('ArrowDown')
    await expect(menu.getByRole('menuitemradio', { name: 'System' })).toBeFocused()
    await page.keyboard.press('ArrowLeft')
    await expect(menu.getByRole('menuitemradio', { name: 'Dark' })).toBeFocused()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
    await page.keyboard.press('ArrowDown')
    await expect(menu.getByRole('menuitem', { name: 'Sign out' })).toBeFocused()
    await page.keyboard.press('ArrowDown')
    await expect(menu.getByRole('menuitem', { name: /— Copy version$/ })).toBeFocused()
    await page.keyboard.press('ArrowDown')
    await expect(menu.getByRole('menuitem', { name: 'Release history' })).toBeFocused()
    await page.keyboard.press('Enter')
    await expectFullHistory(page)
  })

  test('one menu at a time', async ({ page }) => {
    await signIn(page, 'member')
    await home(page)
    await openAccount(page)
    await gear(page).click()
    await expect(page.getByRole('menu', { name: 'Account' })).toBeHidden()
    await expect(page.getByRole('menu', { name: 'App and workspace' })).toBeVisible()
  })
})

test.describe('phones', () => {
  test('release history from the account menu stays on the list', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await signIn(page, 'member')
    await home(page)
    const menu = await openAccount(page)
    await menu.getByRole('menuitem', { name: 'Release history', exact: true }).click()
    const history = await expectFullHistory(page)
    await expect(history.locator('.shell')).not.toHaveClass(/show-detail/)
    await expect(history.getByRole('listbox', { name: 'Releases, newest first' })).toBeVisible()
  })

  // AEON-312: a ticket key stays whole beside the places, search, gear and avatar;
  // the moon stays in the header when there is room and otherwise leads the avatar sheet.
  for (const width of [375, 390]) {
    test(`with three places at ${width} the ticket key stays whole and the moon leads the avatar sheet`, async ({ page }) => {
      await page.setViewportSize({ width, height: 844 })
      await page.emulateMedia({ colorScheme: 'light' })
      await signIn(page, 'admin', { business: true })
      await page.goto('/p/PHAROS/PHAROS-11?view=full')
      await expect(page.getByRole('navigation', { name: 'Places' }).getByRole('link')).toHaveCount(3)
      const key = page.getByRole('navigation', { name: 'Breadcrumb' }).locator('.crumb.current')
      await expect(key).toHaveText('PHAROS-11')
      expect(await key.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
      await expect(page.getByRole('button', { name: 'Switch to dark theme' })).toBeHidden()
      const menu = await openAccount(page)
      await expect(menu.getByRole('menuitem').first()).toHaveAccessibleName('Switch to dark theme')
      await expect(menu.getByRole('menuitem').first()).toBeFocused()
      await page.keyboard.press('Enter')
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
      await expect(menu.getByRole('menuitem').first()).toHaveAccessibleName('Switch to light theme')
      await expect(menu.getByRole('menuitemradio', { name: 'Dark' })).toHaveAttribute('aria-checked', 'true')
    })
  }
  // Measured, not counted: every width × Business on/off × home and a ticket page.
  for (const width of [375, 390]) for (const business of [false, true]) for (const path of ['/', '/p/PHAROS/PHAROS-11?view=full']) {
    const ticket = path !== '/'
    test(`${width} px, Business ${business ? 'on' : 'off'}, ${ticket ? 'ticket' : 'home'}: the key is whole and the moon shows whenever it fits`, async ({ page }) => {
      await page.setViewportSize({ width, height: 844 })
      await page.emulateMedia({ colorScheme: 'light' })
      await signIn(page, 'admin', { business })
      await page.goto(path)
      await expect(page.getByRole('navigation', { name: 'Places' }).getByRole('link')).toHaveCount(business ? 3 : 2)
      if (ticket) {
        const key = page.getByRole('navigation', { name: 'Breadcrumb' }).locator('.crumb.current')
        await expect(key).toHaveText('PHAROS-11')
        await expect.poll(() => key.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
      } else await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
      const moon = page.getByRole('button', { name: 'Switch to dark theme' })
      // Only three places beside a ticket key leave no room for it at these widths.
      if (business && ticket) {
        await expect(moon).toBeHidden()
        // It truly would not fit: the free room is less than a button and its gap.
        const room = await page.evaluate(() => {
          const header = document.querySelector<HTMLElement>('.app-header')!
          return document.querySelector<HTMLElement>('.app-header .spacer')!.getBoundingClientRect().width - (44 + parseFloat(getComputedStyle(header).columnGap))
        })
        expect(room).toBeLessThan(0)
        await openAccount(page)
        await expect(page.getByRole('menu', { name: 'Account' }).getByRole('menuitem').first()).toHaveAccessibleName('Switch to dark theme')
      } else {
        await expect(moon).toBeVisible()
        const box = (await moon.boundingBox())!
        expect(Math.round(box.width)).toBe(44)
        expect(box.x + box.width).toBeLessThanOrEqual(width)
        await openAccount(page)
        await expect(page.getByRole('menu', { name: 'Account' }).getByRole('menuitem').first()).toHaveAccessibleName('Personal settings')
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width)
    })
  }
  test('the moon comes back when the page leaves room for it', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 844 })
    await page.emulateMedia({ colorScheme: 'light' })
    await signIn(page, 'admin', { business: true })
    await page.goto('/p/PHAROS/PHAROS-11?view=full')
    await expect(page.getByRole('button', { name: 'Switch to dark theme' })).toBeHidden()
    await page.getByRole('navigation', { name: 'Places' }).getByRole('link', { name: 'Projects' }).click()
    await expect(page).toHaveURL('/')
    await expect(page.getByRole('button', { name: 'Switch to dark theme' })).toBeVisible()
  })
  // Fonts change the room without resizing the header: a late JetBrains Mono must
  // bring the moon back once it leaves the room for it (Codex round 3).
  test('the moon comes back when a late font leaves room for it', async ({ page }) => {
    let release!: () => void
    const held = new Promise<void>(resolve => { release = resolve })
    await page.route('**/jetbrains*.woff2*', async route => { await held; await route.continue() })
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme: 'light' })
    await signIn(page, 'admin', { business: true })
    await page.goto('/p/PHAROS/PHAROS-11?view=full')
    const moon = page.getByRole('button', { name: 'Switch to dark theme' })
    await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).locator('.crumb.current')).toHaveText('PHAROS-11')
    await expect(moon).toBeHidden()
    expect(await page.evaluate(() => document.fonts.check('12px "JetBrains Mono"'))).toBe(false)
    await page.setViewportSize({ width: 415, height: 844 })
    // Let the resize settle on the fallback font: just short of the room it needs.
    await page.evaluate(() => new Promise(done => requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(done)))))
    await expect(moon).toBeHidden()
    release()
    await page.evaluate(() => document.fonts.ready.then(() => undefined))
    expect(await page.evaluate(() => document.fonts.check('12px "JetBrains Mono"'))).toBe(true)
    await expect(moon).toBeVisible()
    const key = page.getByRole('navigation', { name: 'Breadcrumb' }).locator('.crumb.current')
    await expect.poll(() => key.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
  })
  for (const [width, business] of [[390, false], [440, true], [1600, true]] as const) {
    test(`the moon stays in the header at ${width}${business ? ' with three places' : ''}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 844 })
      await page.emulateMedia({ colorScheme: 'light' })
      await signIn(page, 'admin', { business })
      await page.goto('/p/PHAROS/PHAROS-11?view=full')
      const moon = page.getByRole('button', { name: 'Switch to dark theme' })
      await expect(moon).toBeVisible()
      const box = (await moon.boundingBox())!
      expect(Math.round(box.width)).toBe(width < 600 ? 44 : 34)
      const key = page.getByRole('navigation', { name: 'Breadcrumb' }).locator('.crumb.current')
      expect(await key.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
      await openAccount(page)
      await expect(page.getByRole('menu', { name: 'Account' }).getByRole('menuitem').first()).toHaveAccessibleName('Personal settings')
    })
  }
})

test.describe('phone sheets', () => {
  test.use({ viewport: { width: 375, height: 812 } })
  test('menus are bottom sheets with 44 px rows and no sideways scroll', async ({ page }) => {
    await signIn(page, 'admin')
    await home(page)
    for (const button of [gear(page), avatar(page)]) {
      const box = (await button.boundingBox())!
      expect(Math.round(box.width)).toBe(44)
      expect(Math.round(box.height)).toBe(44)
    }
    const menu = await openGear(page)
    const sheet = (await menu.boundingBox())!
    expect(Math.round(sheet.y + sheet.height)).toBe(812)
    expect(Math.round(sheet.width)).toBe(375)
    for (const item of await menu.getByRole('menuitem').all()) expect((await item.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375)
    // A tap on the scrim closes it.
    await page.mouse.click(180, 40)
    await expect(menu).toBeHidden()
  })
})

// ---------------------------------------------------------------- screenshots
const SHOTS = process.env.HEADER_SHOTS
test.describe('screenshots', () => {
  test.skip(!SHOTS, 'Screenshots only with HEADER_SHOTS=<dir>.')
  const cases: { name: string; role: Role; business?: boolean; path?: string; act: (page: Page) => Promise<void> }[] = [
    { name: 'header', role: 'admin', act: async () => {} },
    { name: 'gear-admin', role: 'admin', act: async page => { await openGear(page) } },
    { name: 'gear-member', role: 'member', act: async page => { await openGear(page) } },
    { name: 'gear-guest', role: 'guest', act: async page => { await openGear(page) } },
    { name: 'help-member', role: 'member', act: async page => {
      await openGear(page); await page.getByRole('menuitem', { name: 'Help & feedback' }).click()
      await expect(page.getByLabel('Feedback for Ada Lindqvist')).toBeEnabled()
    } },
    { name: 'avatar', role: 'member', act: async page => { await openAccount(page) } },
    { name: 'hooks-member', role: 'member', act: async page => { await openGear(page); await page.getByRole('menuitem', { name: 'Inbox hooks' }).click() } },
    { name: 'ticket-header-business', role: 'admin', business: true, path: '/p/PHAROS/PHAROS-11?view=full', act: async () => {} },
    { name: 'avatar-business', role: 'admin', business: true, path: '/p/PHAROS/PHAROS-11?view=full', act: async page => { await openAccount(page) } },
  ]
  for (const width of [375, 390, 1600]) for (const theme of ['light', 'dark'] as const) for (const shot of cases) {
    test(`${shot.name} ${width} ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width < 600 ? 844 : 900 })
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      await signIn(page, shot.role, { business: !!shot.business })
      if (shot.path) { await page.goto(shot.path); await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toBeVisible() }
      else await home(page)
      await shot.act(page)
      await page.waitForTimeout(150)
      mkdirSync(SHOTS!, { recursive: true })
      await page.screenshot({ path: join(SHOTS!, `${shot.name}-${width}-${theme}.png`), fullPage: false })
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width)
    })
  }
})
