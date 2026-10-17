// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1453: one ViewSwitch. Projects List · Cards opens on the view used last, kept on the
// server; right-click or Shift+F10 makes one the default, marked by the pin without moving
// a segment, and a link wins once. A project's ticket list opens on its default (a mode or
// a saved view, read from the older list:<id>.defaultView until replaced) or the mode used
// last. The Releases sheet's language moves from this browser to the server once. The session
// panel's tabs keep one default, set on the tab or, on a phone, from ⋯ (package B).
import { test, expect, type Locator, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import { fixtures, me, mockView, mockWork, watchErrors, type Fixtures } from './work-fixtures'
import { groupsWorld, mockProjectGroups } from './project-groups-fixtures'
import { mockReleases } from './releases-fixtures'
import { historicHistory } from './releases-historic-fixtures'
import { mockLeadFlow, LEAD_SESSION } from './lead-flow-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const MINE = '11111111-aaaa-4aaa-8aaa-000000000001'
const menuItem = (page: Page, name: string) => page.getByRole('menuitem', { name, exact: true })
// A PUT of this preference key landed: the barrier before a reload.
const saved = (page: Page, key: string) => page.waitForResponse(response => response.request().method() === 'PUT' && decodeURIComponent(new URL(response.url()).pathname) === `/api/preferences/${key}`)
const preferencePut = (key: string) => (request: { method: () => string; url: () => string }) => request.method() === 'PUT' && decodeURIComponent(new URL(request.url()).pathname) === `/api/preferences/${key}`
// The pin's box stays inside the segment's pill. Where the label shows, the pin is centred on it.
async function pinFits(segment: Locator) {
  const box = await segment.evaluate(button => {
    const pin = button.querySelector<HTMLElement>('.view-pin')
    const label = button.querySelector<HTMLElement>('.tab-label')
    const slot = button.querySelector<HTMLElement>('.pin-slot')
    if (!pin || !slot || !label) return null
    const pinBox = pin.getBoundingClientRect(), buttonBox = button.getBoundingClientRect()
    const shown = getComputedStyle(label).display !== 'none' && label.getClientRects().length > 0
    const labelBox = shown ? label.getBoundingClientRect() : null
    return {
      pin: { x: pinBox.x, y: pinBox.y, right: pinBox.right, bottom: pinBox.bottom },
      button: { x: buttonBox.x, y: buttonBox.y, right: buttonBox.right, bottom: buttonBox.bottom },
      label: labelBox ? { y: labelBox.y, right: labelBox.right, bottom: labelBox.bottom } : null,
      badge: getComputedStyle(slot).position === 'absolute',
    }
  })
  expect(box, 'the pin is in its segment').not.toBeNull()
  const placed = box!
  const pad = 0.5
  expect(placed.pin.x, 'pin left').toBeGreaterThanOrEqual(placed.button.x - pad)
  expect(placed.pin.y, 'pin top').toBeGreaterThanOrEqual(placed.button.y - pad)
  expect(placed.pin.right, 'pin right').toBeLessThanOrEqual(placed.button.right + pad)
  expect(placed.pin.bottom, 'pin bottom').toBeLessThanOrEqual(placed.button.bottom + pad)
  const width = placed.button.right - placed.button.x, height = placed.button.bottom - placed.button.y, radius = Math.min(width, height) / 2, slack = 0.75
  for (const [x, y] of [[placed.pin.x, placed.pin.y], [placed.pin.right, placed.pin.y], [placed.pin.x, placed.pin.bottom], [placed.pin.right, placed.pin.bottom]] as const) {
    const onCap = width >= height
      ? x < placed.button.x + radius || x > placed.button.right - radius
      : y < placed.button.y + radius || y > placed.button.bottom - radius
    if (!onCap) continue
    const cx = width >= height ? (x < placed.button.x + radius ? placed.button.x + radius : placed.button.right - radius) : placed.button.x + radius
    const cy = width >= height ? placed.button.y + radius : (y < placed.button.y + radius ? placed.button.y + radius : placed.button.bottom - radius)
    expect((x - cx) ** 2 + (y - cy) ** 2, 'pin corner stays on the pill').toBeLessThanOrEqual((radius + slack) ** 2)
  }
  if (placed.label) {
    expect(placed.badge, 'a labelled segment keeps the inline slot').toBe(false)
    expect(Math.abs(placed.pin.x - placed.label.right - 4), 'four pixels after the label').toBeLessThanOrEqual(1)
    expect(Math.abs((placed.pin.y + placed.pin.bottom) / 2 - (placed.label.y + placed.label.bottom) / 2), 'centred on the label').toBeLessThanOrEqual(1)
  } else expect(placed.badge, 'an icon-only segment badges the pin inside the pill').toBe(true)
}

test('AEON-1453 Projects opens on the view used last or the default; the pin never moves a segment, and a link wins once', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  const data: Fixtures = fixtures()
  // Where List · Cards was kept before its own key: read as the view used last.
  data.preferences.projects = { view: 'cards' }
  await mockWork(page, data)
  await mockProjectGroups(page, data, groupsWorld())
  await page.goto('/')
  const views = page.getByRole('radiogroup', { name: 'View' })
  const list = views.getByRole('radio', { name: 'List view' }), cards = views.getByRole('radio', { name: 'Cards view' })
  await expect(cards).toHaveAttribute('aria-checked', 'true')

  // The view used last: chosen here, opened again after a reload, kept on the server only.
  let put = saved(page, 'view:projects')
  await list.click()
  await put
  expect(data.preferences['view:projects']).toEqual({ last: 'list' })
  await page.reload()
  await expect(list).toHaveAttribute('aria-checked', 'true')

  // Right-click Cards: its menu makes it the default; the pin appears in reserved space.
  const guard = await controlStability(page, { list, cards })
  await guard.check(async () => {
    await cards.click({ button: 'right' })
    put = saved(page, 'view:projects')
    await menuItem(page, 'Open here by default').click()
    await put
    await expect(cards.locator('.view-pin')).toBeVisible()
  })
  guard.done()
  await expect(cards).toHaveAttribute('data-tip', 'Cards · default')
  await expect(list).toHaveAttribute('aria-checked', 'true')
  expect(data.preferences['view:projects']).toEqual({ last: 'list', default: 'cards' })
  // The default wins over the view used last.
  await page.reload()
  await expect(cards).toHaveAttribute('aria-checked', 'true')
  await expect(cards.locator('.view-pin')).toBeVisible()
  await expect(list.locator('.view-pin')).toHaveCount(0)
  // Inside the segment at a desktop, a tablet and a phone. The label's centre applies where it shows.
  for (const width of [1440, 1024, 390]) {
    await page.setViewportSize({ width, height: 900 })
    if (width === 1440) {
      await expect(cards.locator('.tab-label')).toBeVisible()
      await expect(cards).not.toHaveClass(/\bicon-only\b/)
    } else {
      await expect(cards.locator('.tab-label')).toBeHidden()
      await expect(cards).toHaveClass(/\bicon-only\b/)
    }
    await pinFits(cards)
  }
  // Stopping and restoring the default on a phone leaves both segments where they are.
  const narrow = await controlStability(page, { list, cards })
  await narrow.check(async () => {
    await cards.focus()
    await page.keyboard.press('Shift+F10')
    put = saved(page, 'view:projects')
    await menuItem(page, 'Stop opening here by default').click()
    await put
    await expect(cards.locator('.view-pin')).toHaveCount(0)
  })
  await narrow.check(async () => {
    await cards.focus()
    await page.keyboard.press('Shift+F10')
    put = saved(page, 'view:projects')
    await menuItem(page, 'Open here by default').click()
    await put
    await expect(cards.locator('.view-pin')).toBeVisible()
  })
  narrow.done()
  await page.setViewportSize({ width: 1440, height: 900 })
  await expect(cards.locator('.tab-label')).toBeVisible()
  await expect(cards).not.toHaveClass(/\bicon-only\b/)

  // A link opens its view once and changes nothing that is remembered.
  await page.goto('/?view=list')
  await expect(list).toHaveAttribute('aria-checked', 'true')
  await page.goto('/')
  await expect(cards).toHaveAttribute('aria-checked', 'true')
  expect(data.preferences['view:projects']).toEqual({ last: 'list', default: 'cards' })

  // Keyboard: Shift+F10 on the focused segment opens the same menu; stopping returns to the view used last.
  await cards.focus()
  await page.keyboard.press('Shift+F10')
  put = saved(page, 'view:projects')
  await menuItem(page, 'Stop opening here by default').click()
  await put
  await expect(cards).toBeFocused()
  await expect(cards.locator('.view-pin')).toHaveCount(0)
  expect(data.preferences['view:projects']).toEqual({ last: 'list', default: null })
  await page.reload()
  await expect(list).toHaveAttribute('aria-checked', 'true')
  // The older store is never rewritten.
  expect(data.preferences.projects).toEqual({ view: 'cards' })
  expect(errors).toEqual([])
})

test('AEON-1453 the ticket list opens on its default (a saved view or a mode) or the mode used last; a link wins once', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  const data: Fixtures = fixtures()
  data.views.push(mockView({ id: MINE, name: 'Mine', filters: { priority: 'high' } }))
  // "Open PHAROS with it", as it was kept before the ticket list switch had its own key.
  data.preferences['list:p-pharos'] = { defaultView: MINE }
  await mockWork(page, data)
  const bar = page.getByRole('navigation', { name: 'Saved views' })
  const modes = page.getByRole('tablist', { name: 'Ticket views' })
  const mine = bar.getByRole('link', { name: 'Mine' })

  // The older default opens the project, and its tab carries the pin.
  await page.goto('/p/PHAROS')
  await expect(page).toHaveURL(new RegExp(`v=${MINE}`))
  await expect(mine.locator('.view-pin')).toBeVisible()
  await expect(mine).toHaveAttribute('data-tip', 'Mine · default')

  // Right-click Outline: the default becomes that mode; the saved view loses its pin.
  const outline = modes.getByRole('tab', { name: 'Outline' }), list = modes.getByRole('tab', { name: 'List' })
  const guard = await controlStability(page, { outline, list, mine })
  await guard.check(async () => {
    await outline.click({ button: 'right' })
    const put = saved(page, 'view:tickets:p-pharos')
    await menuItem(page, 'Open here by default').click()
    await put
    await expect(outline.locator('.view-pin')).toBeVisible()
    await expect(mine.locator('.view-pin')).toHaveCount(0)
  })
  guard.done()
  expect(data.preferences['view:tickets:p-pharos']).toEqual({ default: 'outline' })
  await page.goto('/p/PHAROS')
  await expect(page).toHaveURL(/\/p\/PHAROS\/tickets\?view=outline$/)
  await expect(outline).toHaveAttribute('aria-selected', 'true')

  // Stop the default: the project opens on the mode used last (List, chosen here).
  let put = saved(page, 'view:tickets:p-pharos')
  await list.click()
  await put
  await outline.click({ button: 'right' })
  put = saved(page, 'view:tickets:p-pharos')
  await menuItem(page, 'Stop opening here by default').click()
  await put
  expect(data.preferences['view:tickets:p-pharos']).toEqual({ default: null, last: 'list' })
  await page.goto('/p/PHAROS')
  await expect(page).toHaveURL(/\/p\/PHAROS\/tickets$/)
  await expect(list).toHaveAttribute('aria-selected', 'true')

  // A saved view becomes the default from its own menu, opened by right-click.
  await mine.click({ button: 'right' })
  put = saved(page, 'view:tickets:p-pharos')
  await page.getByRole('menu', { name: 'View Mine' }).getByRole('menuitem', { name: 'Open here by default' }).click()
  await put
  expect(data.preferences['view:tickets:p-pharos']).toEqual({ default: `saved:${MINE}`, last: 'list' })
  await page.goto('/p/PHAROS')
  await expect(page).toHaveURL(new RegExp(`v=${MINE}`))
  // A link wins once: its own mode, nothing remembered changes.
  await page.goto('/p/PHAROS/tickets?view=outline')
  await expect(outline).toHaveAttribute('aria-selected', 'true')
  await expect(page).not.toHaveURL(new RegExp(`v=${MINE}`))
  expect(data.preferences['view:tickets:p-pharos']).toEqual({ default: `saved:${MINE}`, last: 'list' })
  // The older store is read, never rewritten.
  expect(data.preferences['list:p-pharos']).toEqual({ defaultView: MINE })
  expect(errors).toEqual([])
})

test('AEON-1453 the Releases sheet language leaves this browser for the server once; the view remembers its last choice', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const data: Fixtures = fixtures()
  // The answer waits, so a clear that runs before the server accepts it is visible.
  let releaseLangSave = () => {}
  const langHeld = new Promise<void>(resolve => { releaseLangSave = () => resolve() })
  await mockWork(page, data, { hold: ({ path, method }) => path === '/api/preferences/view:releases.lang' && method === 'PUT' ? { until: langHeld } : undefined })
  await mockReleases(page, historicHistory())
  const local = `aeon.release-history.lang.${me.id}`
  await page.addInitScript(key => { if (!sessionStorage.getItem('aeon-1453-seeded')) { localStorage.setItem(key, 'de'); sessionStorage.setItem('aeon-1453-seeded', '1') } }, local)
  const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
  const radio = (name: string) => sheet.getByRole('radio', { name, exact: true })
  const langPut = saved(page, 'view:releases.lang')
  const requested = page.waitForRequest(preferencePut('view:releases.lang'))
  await page.goto('/releases')
  await expect(radio('DE')).toHaveAttribute('aria-checked', 'true')
  await requested
  await page.evaluate(() => new Promise<void>(resolve => { const channel = new MessageChannel(); channel.port1.onmessage = () => { channel.port1.close(); channel.port2.close(); resolve() }; channel.port2.postMessage(undefined) }))
  await expect.poll(() => page.evaluate(key => localStorage.getItem(key), local)).toBe('de')
  releaseLangSave()
  await langPut
  expect(data.preferences['view:releases.lang']).toEqual({ last: 'de' })
  await expect.poll(() => page.evaluate(key => localStorage.getItem(key), local)).toBeNull()
  // The next visit reads the server and the browser copy stays gone.
  await page.reload()
  await expect(radio('DE')).toHaveAttribute('aria-checked', 'true')
  await expect.poll(() => page.evaluate(key => localStorage.getItem(key), local)).toBeNull()

  // Details, chosen once, opens the next visit without it in the address.
  let put = saved(page, 'view:releases')
  await radio('Details').click()
  await put
  await page.goto('/releases')
  await expect(page).not.toHaveURL(/release_view=/)
  await expect(radio('Details')).toHaveAttribute('aria-checked', 'true')
  expect(data.preferences['view:releases']).toEqual({ last: 'details' })
  // Inside the modal sheet the menu opens too, and its default shows the pin.
  await radio('Highlights').click({ button: 'right' })
  put = saved(page, 'view:releases')
  await menuItem(page, 'Open here by default').click()
  await put
  await expect(radio('Highlights').locator('.view-pin')).toBeVisible()
  // The default is for the next visit: Details stays on screen until then.
  await expect(radio('Details')).toHaveAttribute('aria-checked', 'true')
  await expect(sheet).toBeVisible()
})

// AEON-1453 package B: the session panel's tabs are its view switch, with one default. Risks: the
// old ⋯ pin (agents:lead-panel) is lost, or comes back after the person stopped it; the phone ⋯
// choice and the tab's own menu keep two defaults that disagree; marking a tab moves the tabs; a
// hold also switches the tab; a ?tab= link rewrites what is remembered.
const panelOf = (page: Page) => page.getByRole('complementary', { name: 'Session details', exact: true })
const panelTab = (page: Page, name: string) => panelOf(page).getByRole('tab', { name: new RegExp(`^${name}`) })
const panelMenu = (page: Page) => page.getByRole('menu', { name: 'More session actions' })
const openedOn = (page: Page) => panelOf(page).locator('p.sr-only').filter({ hasText: /^Opened on / })
async function leadPanel(page: Page, preferences: Record<string, Record<string, unknown>> = {}) {
  const world = await mockLeadFlow(page, { lead: 'working' })
  Object.assign(world.data.preferences, preferences)
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    if (['/api/models', '/api/approvals', '/api/agent-accounts', '/api/agent-accounts/capacity', '/api/agent-accounts/capacity/schedule'].includes(path)) return route.fulfill({ json: [] })
    return route.fallback()
  })
  return world
}
const pinOn = async (page: Page) => (await panelOf(page).getByRole('tab').evaluateAll(tabs => tabs.filter(tab => tab.querySelector('.view-pin')).map(tab => tab.getAttribute('aria-label'))))

test('AEON-1453 session panel: the old pin becomes the tab switch default once; right-click sets and stops it in place, and ⋯ keeps no default item on wide screens', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  // The ⋯ pin and the last tab as AEON-1212 kept them, before the switch had its own key.
  const world = await leadPanel(page, { 'agents:lead-panel': { pinned: 'chat', last: 'session' } })
  await page.goto(`/agents/${LEAD_SESSION}`)
  const lead = panelTab(page, 'Lead'), details = panelTab(page, 'Details'), chat = panelTab(page, 'Chat'), events = panelTab(page, 'Events')
  await expect(chat).toHaveAttribute('aria-selected', 'true')
  await expect(openedOn(page)).toHaveText('Opened on Chat: your default')
  expect(await pinOn(page)).toEqual(['Chat'])
  await expect(chat).toHaveAttribute('data-tip', /^Chat · default/)
  await expect(chat).toHaveAttribute('aria-description', 'Opens here by default')
  await expect(lead.locator('.tab-label')).toBeVisible()

  // Wide screens set the default on the tab only: ⋯ has no default item.
  await panelOf(page).getByRole('button', { name: 'More session actions', exact: true }).click()
  await expect(panelMenu(page).getByRole('menuitem', { name: 'Interrupt' })).toBeVisible()
  await expect(panelMenu(page).getByRole('menuitemcheckbox', { name: /by default/ })).toHaveCount(0)
  await page.keyboard.press('Escape')

  // Right-click Lead: the pin moves to Lead in its reserved slot; no tab moves, Chat stays on screen.
  const guard = await controlStability(page, { lead, details, chat, events })
  await guard.check(async () => {
    await lead.click({ button: 'right' })
    const put = saved(page, 'view:lead-panel')
    await menuItem(page, 'Open here by default').click()
    await put
    expect(await pinOn(page)).toEqual(['Lead'])
  })
  await expect(lead).toBeFocused()
  await expect(chat).toHaveAttribute('aria-selected', 'true')
  expect(world.data.preferences['view:lead-panel']).toEqual({ default: 'lead' })
  await page.reload()
  await expect(lead).toHaveAttribute('aria-selected', 'true')
  await expect(openedOn(page)).toHaveText('Opened on Lead: your default')

  // Shift+F10 on the focused tab stops it; the old pin never comes back, the old last tab still counts.
  await guard.check(async () => {
    await lead.focus()
    await page.keyboard.press('Shift+F10')
    const put = saved(page, 'view:lead-panel')
    await menuItem(page, 'Stop opening here by default').click()
    await put
    expect(await pinOn(page)).toEqual([])
  })
  guard.done()
  expect(world.data.preferences['view:lead-panel']).toEqual({ default: null })
  await page.reload()
  await expect(details).toHaveAttribute('aria-selected', 'true')
  await expect(openedOn(page)).toHaveText('Opened on Details: last used · no default set')
  expect(await pinOn(page)).toEqual([])
  // Events cannot be a default: no menu opens on it.
  await events.click({ button: 'right' })
  await expect(page.getByRole('menu', { name: 'Events view' })).toHaveCount(0)
  await page.keyboard.press('Escape')
  // The older store is read, never rewritten.
  expect(world.data.preferences['agents:lead-panel']).toEqual({ pinned: 'chat', last: 'session' })
  expect(errors).toEqual([])
})

test('AEON-1453 phone session panel: the ⋯ choice and a long press on the tab set one default, shown on both', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const errors = watchErrors(page)
  const world = await leadPanel(page)
  await page.goto(`/agents/${LEAD_SESSION}`)
  const lead = panelTab(page, 'Lead'), details = panelTab(page, 'Details'), chat = panelTab(page, 'Chat'), events = panelTab(page, 'Events')
  await expect(lead).toHaveAttribute('aria-selected', 'true')
  const last = saved(page, 'view:lead-panel')
  await chat.click()
  await last
  await expect(chat).toHaveAttribute('aria-selected', 'true')
  const more = panelOf(page).getByRole('button', { name: 'More session actions', exact: true })
  const choice = panelMenu(page).getByRole('menuitemcheckbox', { name: /^Open on Chat by default/ })
  const tabs = await controlStability(page, { lead, details, chat, events })

  // ⋯ "Open on Chat by default" writes the switch's default: the pin shows on the Chat tab.
  await tabs.check(async () => {
    await more.click()
    await expect(choice).toHaveAttribute('aria-checked', 'false')
    const put = saved(page, 'view:lead-panel')
    await choice.click()
    await put
    await expect(choice).toHaveAttribute('aria-checked', 'true')
    await page.keyboard.press('Escape')
    await expect(panelMenu(page)).toHaveCount(0)
    expect(await pinOn(page)).toEqual(['Chat'])
  })
  expect(world.data.preferences['view:lead-panel']).toEqual({ last: 'chat', default: 'chat' })

  // A long press on Lead opens its menu without switching to it; its default shows in ⋯ too.
  await tabs.check(async () => {
    const touch = { pointerType: 'touch', pointerId: 7, isPrimary: true, button: 0, bubbles: true }
    await lead.dispatchEvent('pointerdown', touch)
    await expect(menuItem(page, 'Open here by default')).toBeVisible()
    await lead.dispatchEvent('pointerup', touch)
    await lead.dispatchEvent('click', { detail: 1, bubbles: true })
    await expect(chat).toHaveAttribute('aria-selected', 'true')
    const put = saved(page, 'view:lead-panel')
    await menuItem(page, 'Open here by default').click()
    await put
    expect(await pinOn(page)).toEqual(['Lead'])
  })
  tabs.done()
  expect(world.data.preferences['view:lead-panel']).toEqual({ last: 'chat', default: 'lead' })
  await more.click()
  await expect(choice).toHaveAttribute('aria-checked', 'false')
  await expect(choice).toContainText('Now Lead is your default')
  await page.keyboard.press('Escape')
  await page.reload()
  await expect(lead).toHaveAttribute('aria-selected', 'true')
  await expect(openedOn(page)).toHaveText('Opened on Lead: your default')
  expect(await pinOn(page)).toEqual(['Lead'])
  expect(errors).toEqual([])
})

test('AEON-1453 session panel: a ?tab= link wins once and changes neither the default nor the tab used last', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  const world = await leadPanel(page, { 'view:lead-panel': { last: 'chat', default: 'session' } })
  const writes: string[] = []
  page.on('request', request => { if (preferencePut('view:lead-panel')(request)) writes.push(request.postData() ?? '') })
  await page.goto(`/agents/${LEAD_SESSION}?tab=events`)
  const details = panelTab(page, 'Details'), events = panelTab(page, 'Events')
  await expect(events).toHaveAttribute('aria-selected', 'true')
  await expect(openedOn(page)).toHaveText('Opened on Events: from this link')
  expect(await pinOn(page)).toEqual(['Details'])
  // Without the link, the panel opens on its default again; nothing was written.
  await page.goto(`/agents/${LEAD_SESSION}`)
  await expect(details).toHaveAttribute('aria-selected', 'true')
  await expect(openedOn(page)).toHaveText('Opened on Details: your default')
  expect(writes).toEqual([])
  expect(world.data.preferences['view:lead-panel']).toEqual({ last: 'chat', default: 'session' })
  expect(errors).toEqual([])
})
