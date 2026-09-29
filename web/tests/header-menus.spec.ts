// SPDX-License-Identifier: AGPL-3.0-only
// AEON-312: the header's gear menu (the app and the workspace) and avatar menu
// (me), for an admin, a member and a guest, on wide screens and phones.
//
// HEADER_SHOTS=<dir> also writes screenshots of every menu at 375, 390 and 1600,
// light and dark, for review by eye.
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { mockGuestPermissions } from './authz-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'

type Role = 'admin' | 'member' | 'guest'
interface World { sent: Record<string, unknown>[]; ready: number; owner: boolean }

async function signIn(page: Page, role: Role, world: Partial<World> = {}) {
  const state: World = { sent: [], ready: 200, owner: false, ...world }
  await mockWork(page, fixtures(), role === 'admin' ? { admin: true } : role === 'guest' ? { readOnly: true } : {})
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
  await mockReleases(page, history)
  await page.route('**/api/health', route => route.fulfill({ json: { status: 'ok', db: 'ok' } }))
  await page.route('**/api/ready', route => route.fulfill({ status: state.ready, json: { status: state.ready === 200 ? 'ready' : 'unavailable' } }))
  await page.route('**/api/inbox/feedback-recipient', route => state.owner
    ? route.fulfill({ status: 404, json: { code: 'not_found', message: 'not found' } })
    : route.fulfill({ json: { principal_id: '99999999-9999-4999-8999-999999999999', name: 'Ada Lindqvist' } }))
  await page.route('**/api/inbox/messages', route => {
    if (route.request().method() !== 'POST') return route.fallback()
    state.sent.push(route.request().postDataJSON())
    return route.fulfill({ status: 201, json: { id: 'm1' } })
  })
  return state
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

test.describe('gear menu', () => {
  test('an admin sees the workspace, agents, help and a healthy system', async ({ page }) => {
    const errors = watchErrors(page)
    await signIn(page, 'admin')
    await home(page)
    const menu = await openGear(page)
    const items = await names(page, 'App and workspace')
    expect(items.slice(0, 6)).toEqual(['Workspace settings', 'Connect a computer', 'Agent keys', expect.stringMatching(/^Release history/), 'Keyboard shortcuts', 'Help & feedback'])
    expect(items[6]).toMatch(/^System status, Operational, version \d{12}\.\d+\.\d+, deployed 50 min ago$/)
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
})

test.describe('avatar menu', () => {
  test('me: personal settings, theme in sync with the moon, sign out and the version', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'light' })
    await signIn(page, 'member')
    await home(page)
    const menu = await openAccount(page)
    await expect(menu.getByRole('menuitem', { name: 'Personal settings' })).toBeFocused()
    await expect(menu.getByRole('menuitemradio', { name: 'System' })).toHaveAttribute('aria-checked', 'true')
    await expect(menu.getByRole('menuitem', { name: /^Copy version \d{12}/ })).toBeVisible()
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
    await expect(menu.getByRole('menuitem', { name: /^Copy version/ })).toBeFocused()
    await page.keyboard.press('ArrowDown')
    await expect(menu.getByRole('menuitem', { name: 'Release history' })).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(/[?&]releases=all/)
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
  const cases: { name: string; role: Role; act: (page: Page) => Promise<void> }[] = [
    { name: 'header', role: 'admin', act: async () => {} },
    { name: 'gear-admin', role: 'admin', act: async page => { await openGear(page) } },
    { name: 'gear-member', role: 'member', act: async page => { await openGear(page) } },
    { name: 'gear-guest', role: 'guest', act: async page => { await openGear(page) } },
    { name: 'help-member', role: 'member', act: async page => {
      await openGear(page); await page.getByRole('menuitem', { name: 'Help & feedback' }).click()
      await expect(page.getByLabel('Feedback for Ada Lindqvist')).toBeEnabled()
    } },
    { name: 'avatar', role: 'member', act: async page => { await openAccount(page) } },
  ]
  for (const width of [375, 390, 1600]) for (const theme of ['light', 'dark'] as const) for (const shot of cases) {
    test(`${shot.name} ${width} ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width < 600 ? 844 : 900 })
      await page.emulateMedia({ colorScheme: theme, reducedMotion: 'reduce' })
      await signIn(page, shot.role)
      await home(page)
      await shot.act(page)
      await page.waitForTimeout(150)
      mkdirSync(SHOTS!, { recursive: true })
      await page.screenshot({ path: join(SHOTS!, `${shot.name}-${width}-${theme}.png`), fullPage: false })
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width)
    })
  }
})
