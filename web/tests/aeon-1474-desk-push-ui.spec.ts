// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import { writeFile } from 'node:fs/promises'
import { mockDecisionDesk } from './decision-desk-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { controlStability } from './control-stability'

const endpoint = 'https://push.example.test/this-browser'
const hash = createHash('sha256').update(JSON.stringify(endpoint)).digest('hex')
const device = { id: 'device-current', created_at: '2026-10-01T12:00:00Z', endpoint_hash: hash, desk_enabled: false }
const other = { id: 'device-other', created_at: '2026-10-01T12:00:00Z', endpoint_hash: 'f'.repeat(64), desk_enabled: true }
async function setup(page: Page, options: { permission?: NotificationPermission; ios?: boolean; available?: boolean; on?: boolean; supported?: boolean; theme?: 'light' | 'dark'; de?: boolean } = {}) {
  await mockDecisionDesk(page, { theme: options.theme })
  await mockSettings(page, settingsData())
  // The app language policy currently keeps app surfaces English. Exercise the
  // complete German copy here without changing that production-wide policy.
  if (options.de) await page.route('**/src/lib/displayLanguage.ts', route => route.fulfill({ contentType: 'application/javascript', body: "export function displayLanguage() { return 'de' }" }))
  await page.addInitScript(({ endpoint, permission, ios, supported }) => {
    const state = { permission, permissionCalls: 0, registrations: 0, subscriptions: 0 }
    Object.assign(window, { deskPushTest: state })
    if (ios) {
      Object.defineProperty(navigator, 'userAgent', { value: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)' })
      Object.defineProperty(navigator, 'standalone', { value: false })
    }
    Object.defineProperty(window, 'Notification', { configurable: true, value: class {
      static get permission() { return state.permission }
      static async requestPermission() { state.permissionCalls++; state.permission = 'granted'; return 'granted' }
    } })
    if (!supported) { Reflect.deleteProperty(window, 'PushManager'); return }
    Object.defineProperty(window, 'PushManager', { configurable: true, value: class {} })
    const subscription = { endpoint, toJSON: () => ({ endpoint, expirationTime: null, keys: { auth: 'AQID', p256dh: 'BAUG' } }) }
    let subscribed = false
    const registration = { pushManager: {
      getSubscription: async () => subscribed ? subscription : null,
      subscribe: async () => { state.subscriptions++; subscribed = true; return subscription },
    } }
    // A granted existing browser subscription must be identifiable on load.
    if (permission === 'granted') subscribed = true
    Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: {
      getRegistration: async () => registration,
      register: async () => { state.registrations++; return registration }, ready: Promise.resolve(registration),
    } })
  }, { endpoint, permission: options.permission ?? (options.on ? 'granted' : 'default'), ios: !!options.ios, supported: options.supported !== false })
  const settings = { available: true, push_available: options.available !== false, vapid_public_key: 'AQID',
    preferences: { enabled: true, time_zone: 'Europe/Vienna', quiet_start: 1320, quiet_end: 420, escalation_minutes: 15 },
    passkeys: [], subscriptions: [{ ...device, desk_enabled: !!options.on }, { ...other }] }
  const posts: Record<string, unknown>[] = [], deletes: string[] = []
  const control = { fail: false }
  await page.route('**/api/me/phone-approvals**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname
    if (request.method() === 'POST' && path.endsWith('/subscriptions')) {
      posts.push(request.postDataJSON())
      if (control.fail) return route.fulfill({ status: 503, json: { error: 'Unavailable' } })
      const enabled = request.postDataJSON().desk_enabled
      settings.subscriptions = [{ ...device, desk_enabled: enabled }, ...settings.subscriptions.filter(sub => sub.id !== device.id)]
      return route.fulfill({ json: { ...device, desk_enabled: enabled } })
    }
    if (request.method() === 'DELETE') {
      deletes.push(path.split('/').at(-1)!)
      settings.subscriptions = settings.subscriptions.filter(sub => sub.id !== path.split('/').at(-1))
      return route.fulfill({ status: 204 })
    }
    return route.fulfill({ json: settings })
  })
  return { settings, posts, deletes, control }
}
const browserState = (page: Page) => page.evaluate(() => (window as unknown as { deskPushTest: { permissionCalls: number; registrations: number; subscriptions: number } }).deskPushTest)
const cardOf = (page: Page) => page.locator('#notifications')

test('AEON-1474 Settings prompts only on click and independently enables disables and removes Desk devices', async ({ page }) => {
  const world = await setup(page)
  await page.goto('/settings/personal#notifications')
  const card = cardOf(page), toggle = card.getByRole('switch')
  await expect(toggle).toBeEnabled()
  expect((await browserState(page)).permissionCalls).toBe(0)
  const guard = await controlStability(page, { toggle, row: card.locator('.switch-row') })
  await guard.check(async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-checked', 'true') })
  expect(world.posts[0]).toEqual({ endpoint, keys: { auth: 'AQID', p256dh: 'BAUG' }, desk_enabled: true })
  await expect(card.getByRole('listitem').first()).toContainText('This browser')
  expect((await browserState(page)).permissionCalls).toBe(1)
  await guard.check(async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-checked', 'false') })
  expect(world.posts[1]?.desk_enabled).toBe(false)
  expect(world.settings.preferences.enabled).toBe(true)
  expect((await browserState(page)).permissionCalls).toBe(1)
  const row = card.getByRole('listitem').filter({ hasText: 'Device' })
  await expect(row.getByRole('button')).toHaveAttribute('title', 'Removes all notifications for this device, phone approvals included')
  await row.getByRole('button', { name: 'Remove' }).click()
  await expect(row).toHaveCount(0)
  expect(world.deletes).toEqual([other.id])
  await guard.check(async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-checked', 'true') })
  const current = card.getByRole('listitem').filter({ hasText: 'This browser' })
  await current.getByRole('button', { name: 'Remove' }).click()
  await expect(current).toHaveCount(0)
  await expect(toggle).toHaveAttribute('aria-checked', 'false')
  expect(world.deletes).toEqual([other.id, device.id])
  guard.done()
})

test('AEON-1474 unavailable blocked unsupported and iOS states never prompt or expose a dead iPhone switch', async ({ context }) => {
  for (const state of [
    { available: false, line: "Browser notifications aren't available on this instance." },
    { permission: 'denied' as const, line: "Notifications are blocked for this site. Allow them in the browser's site settings." },
    { ios: true, line: 'On iPhone, add PAIMOS to the Home Screen first (Share › Add to Home Screen), then turn this on there.' },
    { supported: false, line: 'This browser does not support push notifications.' },
  ]) {
    const page = await context.newPage()
    await setup(page, state)
    await page.goto('/settings/personal#notifications')
    const card = cardOf(page)
    await expect(card.getByText(state.line, { exact: true }).filter({ visible: true })).toBeVisible()
    if (state.ios) await expect(card.getByRole('switch')).toHaveCount(0)
    else await expect(card.getByRole('switch')).toBeDisabled()
    expect((await browserState(page)).permissionCalls).toBe(0)
    await page.goto('/decision-desk')
    await expect(page.getByRole('heading', { name: 'Decision Desk', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Turn on', exact: true })).toHaveCount(0)
    await page.close()
  }
})

test('AEON-1474 Desk dismissal persists and an enabled browser gets no offer', async ({ page, context }) => {
  await setup(page)
  await page.goto('/decision-desk')
  const dismiss = page.getByRole('button', { name: 'Not now', exact: true })
  await expect(dismiss).toBeVisible()
  expect((await browserState(page)).permissionCalls).toBe(0)
  const guard = await controlStability(page, { header: page.locator('.dp-head'), first: page.locator('[data-testid^="desk-row-"]').first() })
  await guard.check(async () => { await dismiss.click(); await expect(dismiss).toHaveCount(0) })
  guard.done()
  await page.reload()
  await expect(page.getByRole('heading', { name: 'Decision Desk', exact: true })).toBeVisible()
  await expect(dismiss).toHaveCount(0)
  expect(await page.evaluate(() => localStorage.getItem('aeon.desk-push.dismissed'))).toBe('1')
  await page.evaluate(() => localStorage.removeItem('aeon.desk-push.dismissed'))
  const enabledPage = await context.newPage()
  await setup(enabledPage, { on: true })
  const loaded = enabledPage.waitForResponse('**/api/me/phone-approvals')
  await enabledPage.goto('/decision-desk')
  await loaded
  await expect(enabledPage.getByRole('heading', { name: 'Decision Desk', exact: true })).toBeVisible()
  await expect(enabledPage.getByRole('button', { name: 'Turn on', exact: true })).toHaveCount(0)
  await enabledPage.close()
})

test('AEON-1474 rejected writes retain the previous state and never announce success', async ({ page }) => {
  const world = await setup(page)
  world.control.fail = true
  await page.goto('/settings/personal#notifications')
  const card = cardOf(page), toggle = card.getByRole('switch')
  await expect(toggle).toBeEnabled()
  await toggle.click()
  await expect(card.getByRole('alert')).toContainText('could not be updated')
  await expect(toggle).toHaveAttribute('aria-checked', 'false')
  await page.goto('/decision-desk')
  await page.getByRole('button', { name: 'Turn on', exact: true }).click()
  await expect(page.locator('.notice-feedback')).toContainText('could not be enabled')
  await expect(page.getByRole('status').filter({ hasText: 'Notifications on for this browser' })).toHaveCount(0)
})

test('AEON-1474 Settings and Desk remain still with EN and DE copy at phone tablet and desktop widths in both themes', async ({ context }, testInfo) => {
  test.setTimeout(120_000)
  const checks: unknown[] = []
  for (const de of [false, true]) for (const theme of ['light', 'dark'] as const) for (const width of [1440, 1024, 390]) {
    const page = await context.newPage()
    await page.setViewportSize({ width, height: 1000 })
    await setup(page, { theme, de })
    await page.goto('/settings/personal#notifications')
    const card = cardOf(page), toggle = card.getByRole('switch')
    await expect(toggle).toBeEnabled()
    await expect(toggle).toContainText(de ? 'In diesem Browser benachrichtigen' : 'Notify me in this browser')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await card.screenshot({ path: testInfo.outputPath(`settings-${de ? 'de' : 'en'}-${width}-${theme}.png`) })
    const guard = await controlStability(page, { toggle, row: card.locator('.switch-row') })
    await guard.check(async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-checked', 'true') })
    await card.screenshot({ path: testInfo.outputPath(`settings-enabled-${de ? 'de' : 'en'}-${width}-${theme}.png`) })
    await guard.check(async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-checked', 'false') })
    guard.done()
    expect(await card.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    for (const button of await card.getByRole('button').all()) expect((await button.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    await page.goto('/decision-desk')
    const turnOn = page.getByRole('button', { name: de ? 'Einschalten' : 'Turn on', exact: true })
    await expect(turnOn).toBeVisible()
    for (const button of await page.locator('.notice-actions').getByRole('button').all()) {
      const box = (await button.boundingBox())!
      expect(box.height).toBeGreaterThanOrEqual(44); expect(box.width).toBeGreaterThanOrEqual(44)
    }
    const header = page.locator('.dp-head'), first = page.locator('[data-testid^="desk-row-"]').first()
    const before = { header: await header.boundingBox(), first: await first.boundingBox() }
    const deskGuard = await controlStability(page, { header, first })
    await page.screenshot({ path: testInfo.outputPath(`desk-${de ? 'de' : 'en'}-${width}-${theme}.png`) })
    await deskGuard.check(async () => {
      await turnOn.click()
      await expect(page.getByRole('status').filter({ hasText: de ? 'Benachrichtigungen für diesen Browser an' : 'Notifications on for this browser' })).toBeVisible()
    })
    deskGuard.done()
    const after = { header: await header.boundingBox(), first: await first.boundingBox() }
    const movementPx = Math.max(...(['header', 'first'] as const).map(key => Math.abs(after[key]!.y - before[key]!.y)))
    expect(movementPx).toBe(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
    checks.push({ language: de ? 'de' : 'en', theme, width, movementPx, overflow: false, minimumTargetHeight: 44 })
    await page.close()
  }
  const evidence = JSON.stringify(checks, null, 2)
  await writeFile(testInfo.outputPath('checks.json'), evidence)
  // The guarded remote runner transfers PNG/MD files; retain the same checks
  // as Markdown for artifact transport, then collect checks.json locally.
  await writeFile(testInfo.outputPath('checks.md'), evidence)
})
