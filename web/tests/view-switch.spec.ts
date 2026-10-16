// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1453: one ViewSwitch. Projects List · Cards opens on the view used last, kept on the
// server; right-click or Shift+F10 makes one the default, marked by the pin without moving
// a segment, and a link wins once. A project's ticket list opens on its default (a mode or
// a saved view, read from the older list:<id>.defaultView until replaced) or the mode used
// last. The Releases sheet's language moves from this browser to the server once.
import { test, expect, type Page } from '@playwright/test'
import { controlStability } from './control-stability'
import { fixtures, me, mockView, mockWork, watchErrors, type Fixtures } from './work-fixtures'
import { groupsWorld, mockProjectGroups } from './project-groups-fixtures'
import { mockReleases } from './releases-fixtures'
import { historicHistory } from './releases-historic-fixtures'

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

const MINE = '11111111-aaaa-4aaa-8aaa-000000000001'
const menuItem = (page: Page, name: string) => page.getByRole('menuitem', { name, exact: true })
// A PUT of this preference key landed: the barrier before a reload.
const saved = (page: Page, key: string) => page.waitForResponse(response => response.request().method() === 'PUT' && decodeURIComponent(new URL(response.url()).pathname) === `/api/preferences/${key}`)

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
  await mockWork(page, data)
  await mockReleases(page, historicHistory())
  const local = `aeon.release-history.lang.${me.id}`
  await page.addInitScript(key => { if (!sessionStorage.getItem('aeon-1453-seeded')) { localStorage.setItem(key, 'de'); sessionStorage.setItem('aeon-1453-seeded', '1') } }, local)
  const sheet = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
  const radio = (name: string) => sheet.getByRole('radio', { name, exact: true })
  let put = saved(page, 'view:releases.lang')
  await page.goto('/releases')
  await expect(radio('DE')).toHaveAttribute('aria-checked', 'true')
  await put
  expect(data.preferences['view:releases.lang']).toEqual({ last: 'de' })
  // The next visit reads the server and clears what this browser held.
  await page.reload()
  await expect(radio('DE')).toHaveAttribute('aria-checked', 'true')
  await expect.poll(() => page.evaluate(key => localStorage.getItem(key), local)).toBeNull()

  // Details, chosen once, opens the next visit without it in the address.
  put = saved(page, 'view:releases')
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
