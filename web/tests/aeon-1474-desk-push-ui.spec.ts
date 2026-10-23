// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import { writeFile } from 'node:fs/promises'
import { controlStability } from './control-stability'
import { mockSettings, settingsData } from './settings-fixtures'
import { fixtures, mockWork } from './work-fixtures'

// One open projection row, every other Desk source empty. Kept here rather
// than in decision-desk-fixtures.ts, whose importers sit at the helper impact bound.
async function mockDesk(page: Page, theme?: 'light' | 'dark') {
  const data = fixtures()
  if (theme) data.preferences.theme = { choice: theme }
  await mockWork(page, data)
  const row = { id: 'accounts-1474', kind: 'account_matrix', revision: 1, title: 'Confirm the shared agent accounts', created_at: '2026-10-17T13:00:00Z', held: false, can_decide: true, href: '/settings/accounts', source: '/api/settings/accounts' }
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url()), path = url.pathname, read = route.request().method() === 'GET'
    if (path === '/api/decision-desk/projection') return route.fulfill({ json: { items: [row], counts: { open: 1, held: 0, chores: 0 }, has_more: false, as_of: '2026-10-17T13:00:00Z' } })
    if (path === '/api/decision-desk') return route.fulfill({ json: { items: [], has_more: false } })
    if (path === '/api/approvals') return route.fulfill({ json: [] })
    if (path === '/api/rules/doctrine/inbox') return route.fulfill({ json: { items: [], pending: 0 } })
    if (read && (path === '/api/stepup-requests' || path === '/api/key-trim-proposals')) return route.fulfill({ json: { items: [], has_more: false } })
    if (path === '/api/harness-sessions') return route.fulfill({ json: { items: [], next_cursor: null } })
    if (read && path.endsWith('/messages')) return route.fulfill({ json: { items: [], next_after: 0 } })
    return route.fallback()
  })
}

const endpoint = 'https://push.example.test/this-browser'
const hash = createHash('sha256').update(JSON.stringify(endpoint)).digest('hex')
const device = { id: 'device-current', created_at: '2026-10-01T12:00:00Z', endpoint_hash: hash, desk_enabled: false }
const other = { id: 'device-other', created_at: '2026-10-01T12:00:00Z', endpoint_hash: 'f'.repeat(64), desk_enabled: true }
async function setup(page: Page, options: { permission?: NotificationPermission; answer?: NotificationPermission; ios?: boolean; available?: boolean; on?: boolean; supported?: boolean; theme?: 'light' | 'dark'; de?: boolean } = {}) {
  await mockDesk(page, options.theme)
  await mockSettings(page, settingsData())
  // The app language policy currently keeps app surfaces English. Exercise the
  // complete German copy here without changing that production-wide policy.
  if (options.de) await page.route('**/src/lib/displayLanguage.ts', route => route.fulfill({ contentType: 'application/javascript', body: "export function displayLanguage() { return 'de' }" }))
  await page.addInitScript(({ endpoint, permission, answer, ios, supported }) => {
    const state = { permission, permissionCalls: 0, registrations: 0, subscriptions: 0 }
    Object.assign(window, { deskPushTest: state })
    if (ios) {
      Object.defineProperty(navigator, 'userAgent', { value: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)' })
      Object.defineProperty(navigator, 'standalone', { value: false })
    }
    Object.defineProperty(window, 'Notification', { configurable: true, value: class {
      static get permission() { return state.permission }
      static async requestPermission() { state.permissionCalls++; state.permission = answer; return answer }
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
  }, { endpoint, permission: options.permission ?? (options.on ? 'granted' : 'default'), answer: options.answer ?? 'granted', ios: !!options.ios, supported: options.supported !== false })
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
  await expect(card.getByRole('status')).toHaveText('Off for this browser')
  expect((await browserState(page)).permissionCalls).toBe(0)
  const guard = await controlStability(page, { toggle, row: card.locator('.switch-row'), slot: card.locator('.state-slot') })
  await guard.check(async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-checked', 'true') })
  await expect(card.getByRole('status')).toHaveText('Notifications on for this browser')
  expect(world.posts[0]).toEqual({ endpoint, keys: { auth: 'AQID', p256dh: 'BAUG' }, desk_enabled: true })
  // Day, short month and year read the same everywhere; 10/1/2026 does not (Tick).
  await expect(card.getByRole('listitem').first()).toContainText('This browser · added 1 Oct 2026')
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
    { ios: true, line: 'On iPhone, add PAIMOS to the Home Screen first (in the Share menu: Add to Home Screen), then turn this on there.' },
    { supported: false, line: 'This browser does not support push notifications.' },
  ]) {
    const page = await context.newPage()
    await setup(page, state)
    await page.goto('/settings/personal#notifications')
    const card = cardOf(page)
    await expect(card.getByText(state.line, { exact: true }).filter({ visible: true })).toBeVisible()
    if (state.ios) await expect(card.getByRole('switch')).toHaveCount(0)
    else await expect(card.getByRole('switch')).toBeDisabled()
    // Only an iPhone outside the Home Screen reserves room for the iPhone hint.
    await expect(card.locator('.state-size').filter({ hasText: 'Home Screen' })).toHaveCount(state.ios ? 1 : 0)
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

// The tabs below the subtitle, less the header's own section gap: 0 means no band (AEON-1448).
const band = (page: Page) => page.evaluate(() => {
  const note = [...document.querySelectorAll('p')].find(p => p.textContent === 'Agents keep working unless something is held up.')!
  const tabs = document.querySelector('[role="radiogroup"][aria-label="Show"]')!
  return tabs.getBoundingClientRect().top - note.getBoundingClientRect().bottom - parseFloat(getComputedStyle(note.closest('header')!).marginBottom)
})
const T0 = Date.parse('2026-10-21T08:00:00Z')

// Risks: an empty band reserved above the tabs when no offer comes (AEON-1448); an offer that
// arrives after the tabs show and pushes them down; a hung read that keeps the Desk hidden.
test('AEON-1474 the Desk decides its offer before the tabs show, without a band or a late offer', async ({ context }) => {
  const declined = await context.newPage()
  await setup(declined, { available: false })
  const read = declined.waitForResponse('**/api/me/phone-approvals')
  await declined.goto('/decision-desk')
  await read
  await expect(declined.getByTestId('desk-tab-open')).toBeVisible()
  await expect(declined.getByRole('button', { name: 'Turn on', exact: true })).toHaveCount(0)
  expect(Math.abs(await band(declined)), 'no band, the gap is the section gap').toBeLessThanOrEqual(0.5)
  await declined.close()

  // While the read is held, only the title shows; the offer and the tabs then appear together.
  const page = await context.newPage()
  await setup(page)
  let answer!: () => void
  const answered = new Promise<void>(resolve => { answer = resolve })
  await page.route('**/api/me/phone-approvals', async route => { if (route.request().method() === 'GET') await answered; return route.fallback() })
  await page.goto('/decision-desk')
  await expect(page.getByRole('heading', { name: 'Decision Desk', exact: true })).toBeVisible()
  await expect(page.locator('main.decision-desk')).toHaveAttribute('aria-busy', 'true')
  await expect(page.getByTestId('desk-tab-open')).toBeAttached()
  await expect(page.getByTestId('desk-tab-open')).toBeHidden()
  answer()
  await expect(page.getByTestId('desk-tab-open')).toBeVisible()
  expect(await page.getByRole('button', { name: 'Turn on', exact: true }).isVisible(), 'the offer shows with the tabs').toBe(true)
  await page.close()

  // A read that never answers: the bound on the page's clock shows the Desk without the offer and drops the read.
  const hung = await context.newPage()
  await setup(hung)
  await hung.route('**/api/me/phone-approvals', route => route.request().method() === 'GET' ? undefined : route.fallback())
  await hung.clock.install({ time: T0 })
  await hung.goto('/')
  await expect(hung.getByRole('heading', { level: 1, name: 'Projects' })).toBeVisible()
  await hung.clock.pauseAt(T0 + 10_000)
  const dropped = hung.waitForEvent('requestfailed', request => new URL(request.url()).pathname === '/api/me/phone-approvals')
  await hung.getByRole('navigation', { name: 'Places' }).getByRole('link', { name: /^Decision Desk/ }).click()
  await expect(hung.getByRole('heading', { name: 'Decision Desk', exact: true })).toBeVisible()
  await expect(hung.locator('main.decision-desk')).toHaveAttribute('aria-busy', 'true')
  await expect(hung.getByTestId('desk-tab-open')).toBeAttached()
  await expect(hung.getByTestId('desk-tab-open')).toBeHidden()
  await hung.clock.runFor(800)
  await dropped
  await expect(hung.getByTestId('desk-tab-open')).toBeVisible()
  await expect(hung.locator('main.decision-desk')).not.toHaveAttribute('aria-busy', 'true')
  await expect(hung.getByRole('button', { name: 'Turn on', exact: true })).toHaveCount(0)
  expect(Math.abs(await band(hung)), 'no band after the bound').toBeLessThanOrEqual(0.5)
  await hung.close()
})

// Risk: a failed or refused Turn on that reports success, pushes the Desk down with an extra line, or drops the
// offer (and its retry) once the browser reports the refusal.
test('AEON-1474 rejected writes and refused prompts keep the previous state in place and never announce success', async ({ context }) => {
  const page = await context.newPage()
  const world = await setup(page)
  world.control.fail = true
  await page.goto('/settings/personal#notifications')
  const card = cardOf(page), toggle = card.getByRole('switch')
  await expect(toggle).toBeEnabled()
  const settingsGuard = await controlStability(page, { toggle, slot: card.locator('.state-slot') })
  await settingsGuard.check(async () => { await toggle.click(); await expect(card.getByRole('alert')).toHaveText('Notifications could not be updated. Please try again.') })
  settingsGuard.done()
  await expect(toggle).toHaveAttribute('aria-checked', 'false')
  await page.goto('/decision-desk')
  const turnOn = page.getByRole('button', { name: 'Turn on', exact: true }), notNow = page.getByRole('button', { name: 'Not now', exact: true })
  const sentence = page.getByTestId('desk-notice-sentence')
  await expect(turnOn).toBeVisible()
  const still = { header: page.locator('.dp-head'), tabs: page.getByTestId('desk-tab-open'), first: page.locator('[data-testid^="desk-row-"]').first() }
  const errorGuard = await controlStability(page, { ...still, turnOn, notNow })
  await errorGuard.check(async () => { await turnOn.click(); await expect(sentence).toHaveText('Notifications could not be turned on.') })
  errorGuard.done()
  await expect(page.getByRole('status').filter({ hasText: 'Notifications on for this browser' })).toHaveCount(0)
  await expect(page.locator('.notice-feedback')).toHaveCount(0)
  // Turn on retries in place.
  world.control.fail = false
  const retryGuard = await controlStability(page, still)
  await retryGuard.check(async () => { await turnOn.click(); await expect(page.getByRole('status').filter({ hasText: 'Notifications on for this browser' })).toBeVisible() })
  retryGuard.done()
  expect(world.posts.map(post => post.desk_enabled)).toEqual([true, true, true])
  await page.close()

  // A refused prompt: Settings shows the blocked line in the same slot; the Desk keeps its offer with the failure.
  const refused = await context.newPage()
  const refusedWorld = await setup(refused, { answer: 'denied' })
  await refused.goto('/settings/personal#notifications')
  const refusedCard = cardOf(refused), refusedToggle = refusedCard.getByRole('switch')
  await expect(refusedToggle).toBeEnabled()
  const blockedGuard = await controlStability(refused, { toggle: refusedToggle, slot: refusedCard.locator('.state-slot') })
  await blockedGuard.check(async () => {
    await refusedToggle.click()
    await expect(refusedCard.getByText("Notifications are blocked for this site. Allow them in the browser's site settings.", { exact: true }).filter({ visible: true })).toBeVisible()
  })
  blockedGuard.done()
  await expect(refusedToggle).toBeDisabled()
  await refused.goto('/decision-desk')
  const retry = refused.getByRole('button', { name: 'Turn on', exact: true })
  await expect(retry).toBeVisible()
  const refusedGuard = await controlStability(refused, { header: refused.locator('.dp-head'), turnOn: retry, first: refused.locator('[data-testid^="desk-row-"]').first() })
  await refusedGuard.check(async () => { await retry.click(); await expect(refused.getByTestId('desk-notice-sentence')).toHaveText('Notifications could not be turned on.') })
  refusedGuard.done()
  await expect(refused.getByRole('button', { name: 'Not now', exact: true })).toBeVisible()
  expect(refusedWorld.posts).toEqual([])
  expect((await browserState(refused)).permissionCalls).toBe(1)
  await refused.close()
})

// Risk: a person who already allowed notifications waits for a read that cannot change their Desk (up to the bound).
test('AEON-1474 a browser that already allows notifications shows the Desk without waiting for the offer read', async ({ page }) => {
  await setup(page, { permission: 'granted' })
  // The read never answers; only not waiting for it can show the tabs.
  await page.route('**/api/me/phone-approvals', route => route.request().method() === 'GET' ? undefined : route.fallback())
  await page.clock.install({ time: T0 })
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1, name: 'Projects' })).toBeVisible()
  // The page's clock stands still, so the read bound can never be what shows the tabs.
  await page.clock.pauseAt(T0 + 10_000)
  await page.getByRole('navigation', { name: 'Places' }).getByRole('link', { name: /^Decision Desk/ }).click()
  await expect(page.getByRole('heading', { name: 'Decision Desk', exact: true })).toBeVisible()
  await expect(page.getByTestId('desk-tab-open')).toBeVisible()
  await expect(page.locator('main.decision-desk')).not.toHaveAttribute('aria-busy', 'true')
  await expect(page.getByRole('button', { name: 'Turn on', exact: true })).toHaveCount(0)
  expect(Math.abs(await band(page)), 'no band').toBeLessThanOrEqual(0.5)
})

// Below the tabs: the first row's and the queue card's distance from the tabs' bottom edge (Tick item 7).
const belowTabs = (page: Page) => page.evaluate(() => {
  const tabs = document.querySelector('[role="radiogroup"][aria-label="Show"]')!.getBoundingClientRect()
  const top = (selector: string) => Math.round(document.querySelector(selector)!.getBoundingClientRect().top - tabs.bottom)
  return { tabsBottom: Math.round(tabs.bottom), tabsToQueuePx: top('[data-testid="desk-list"]'), tabsToFirstRowPx: top('[data-testid^="desk-row-"]') }
})
// Computed colours of the link and muted ink tokens, to compare the offer's actions with.
const inks = (page: Page) => page.evaluate(() => {
  const probe = document.body.appendChild(document.createElement('span'))
  const ink = (token: string) => { probe.style.color = `var(${token})`; return getComputedStyle(probe).color }
  const result = { link: ink('--primary-ink'), muted: ink('--ink-3') }
  probe.remove()
  return result
})

test('AEON-1474 Settings and Desk remain still with EN and DE copy at phone tablet and desktop widths in both themes', async ({ context }, testInfo) => {
  test.setTimeout(240_000)
  const checks: unknown[] = []
  for (const de of [false, true]) for (const theme of ['light', 'dark'] as const) for (const width of [1440, 1024, 390]) {
    const name = `${de ? 'de' : 'en'}-${width}-${theme}`
    const page = await context.newPage()
    await page.setViewportSize({ width, height: 1000 })
    const world = await setup(page, { theme, de })
    await page.goto('/settings/personal#notifications')
    const card = cardOf(page), toggle = card.getByRole('switch')
    await expect(toggle).toBeEnabled()
    await expect(toggle).toHaveText(de ? 'In diesem Browser über wartende Entscheidungen benachrichtigen, auch auf verbundenen Instanzen' : 'Notify me in this browser when a decision waits, including connected instances')
    await expect(card.getByRole('status')).toHaveText(de ? 'Für diesen Browser aus' : 'Off for this browser')
    await expect(card.getByRole('listitem').first()).toContainText(de ? 'hinzugefügt am 1. Okt. 2026' : 'added 1 Oct 2026')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await card.screenshot({ path: testInfo.outputPath(`settings-off-${name}.png`) })
    // Off, on and a failed update share the slot: the switch, the slot and the list below stay put.
    const guard = await controlStability(page, { toggle, row: card.locator('.switch-row'), slot: card.locator('.state-slot') })
    await guard.check(async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-checked', 'true') })
    await card.screenshot({ path: testInfo.outputPath(`settings-on-${name}.png`) })
    world.control.fail = true
    await guard.check(async () => { await toggle.click(); await expect(card.getByRole('alert')).toBeVisible() })
    world.control.fail = false
    await guard.check(async () => { await toggle.click(); await expect(toggle).toHaveAttribute('aria-checked', 'false') })
    guard.done()
    expect(await card.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true)
    for (const button of await card.getByRole('button').all()) expect((await button.boundingBox())!.height).toBeGreaterThanOrEqual(44)

    await page.goto('/decision-desk')
    const turnOn = page.getByRole('button', { name: de ? 'Einschalten' : 'Turn on', exact: true })
    const notNow = page.getByRole('button', { name: de ? 'Nicht jetzt' : 'Not now', exact: true })
    await expect(turnOn).toBeVisible()
    // AEON-1617: the offer is the longer sentence, so Turn on sits on its line at 1440 instead of floating past a reserved error.
    let offerGapPx: number | undefined
    if (width === 1440) {
      const place = await page.evaluate(() => {
        const textOf = (el: Element) => { const range = document.createRange(); range.selectNodeContents(el); return range.getBoundingClientRect() }
        const sizes = [...document.querySelectorAll('.notice-sentence .notice-size')]
        const offer = textOf(sizes[0]), failed = textOf(sizes[1])
        const shown = textOf(document.querySelector('[data-testid="desk-notice-sentence"]')!)
        const button = document.querySelector('.notice-on')!.getBoundingClientRect()
        return { offerWidth: offer.width, failedWidth: failed.width, shownHeight: shown.height, gap: button.left - shown.right, sameLine: button.top < shown.bottom && button.bottom > shown.top }
      })
      expect(place.offerWidth, `${name} offer is the longer sentence`).toBeGreaterThan(place.failedWidth)
      expect(place.shownHeight, `${name} offer stays one line`).toBeLessThan(28)
      expect(place.sameLine, `${name} Turn on is on the offer line`).toBe(true)
      expect(place.gap, `${name} Turn on sits next to the offer`).toBeGreaterThan(8)
      expect(place.gap, `${name} Turn on sits next to the offer`).toBeLessThan(24)
      offerGapPx = place.gap
    }
    let minimumTarget = Infinity
    for (const button of [turnOn, notNow]) {
      const box = (await button.boundingBox())!
      expect(box.height).toBeGreaterThanOrEqual(44); expect(box.width).toBeGreaterThanOrEqual(44)
      minimumTarget = Math.min(minimumTarget, box.height, box.width)
    }
    // Quiet actions: Turn on in link ink, Not now muted, both in regular weight.
    const ink = await inks(page)
    const look = (button: typeof turnOn) => button.evaluate(el => ({ color: getComputedStyle(el).color, weight: getComputedStyle(el).fontWeight }))
    expect(await look(turnOn)).toEqual({ color: ink.link, weight: '400' })
    expect(await look(notNow)).toEqual({ color: ink.muted, weight: '400' })
    const header = page.locator('.dp-head'), first = page.locator('[data-testid^="desk-row-"]').first()
    const before = { header: await header.boundingBox(), first: await first.boundingBox() }
    const below = await belowTabs(page)
    await page.screenshot({ path: testInfo.outputPath(`desk-${name}.png`) })
    // The failure takes the offer's own sentence; both actions stay where they were and Turn on retries.
    const errorGuard = await controlStability(page, { header, turnOn, notNow, first })
    world.control.fail = true
    await errorGuard.check(async () => {
      await turnOn.click()
      await expect(page.getByTestId('desk-notice-sentence')).toHaveText(de ? 'Benachrichtigungen konnten nicht eingeschaltet werden.' : 'Notifications could not be turned on.')
    })
    errorGuard.done()
    await page.screenshot({ path: testInfo.outputPath(`desk-error-${name}.png`) })
    world.control.fail = false
    const deskGuard = await controlStability(page, { header, first })
    await deskGuard.check(async () => {
      await turnOn.click()
      await expect(page.getByRole('status').filter({ hasText: de ? 'Benachrichtigungen für diesen Browser an' : 'Notifications on for this browser' })).toBeVisible()
    })
    deskGuard.done()
    const after = { header: await header.boundingBox(), first: await first.boundingBox() }
    const movementPx = Math.max(...(['header', 'first'] as const).map(key => Math.abs(after[key]!.y - before[key]!.y)))
    expect(movementPx).toBe(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
    checks.push({ language: de ? 'de' : 'en', theme, width, settingsShiftPx: 0, deskErrorShiftPx: 0, movementPx, overflow: false, minimumTargetPx: Math.floor(minimumTarget), offer: true, ...(offerGapPx === undefined ? {} : { offerGapPx: Math.round(offerGapPx * 10) / 10 }), ...below })
    await page.close()
  }
  // The same Desk without the offer: its distance below the tabs is the Desk's own layout (Tick item 7).
  for (const width of [1440, 1024, 390]) {
    const page = await context.newPage()
    await page.setViewportSize({ width, height: 1000 })
    await setup(page, { available: false })
    await page.goto('/decision-desk')
    await expect(page.getByTestId('desk-tab-open')).toBeVisible()
    await expect(page.locator('[data-testid^="desk-row-"]').first()).toBeVisible()
    checks.push({ language: 'en', theme: 'light', width, offer: false, ...(await belowTabs(page)) })
    await page.close()
  }
  const evidence = JSON.stringify(checks, null, 2)
  await writeFile(testInfo.outputPath('checks.json'), evidence)
  // The guarded remote runner transfers PNG/MD files; retain the same checks
  // as Markdown for artifact transport, then collect checks.json locally.
  await writeFile(testInfo.outputPath('checks.md'), evidence)
})
