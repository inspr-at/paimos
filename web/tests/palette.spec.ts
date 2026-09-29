// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

const palette = (page: Page) => page.getByRole('dialog', { name: 'Search and commands' })
const group = (page: Page, name: string) => palette(page).getByRole('group', { name }).getByRole('option')
const rows = (page: Page) => page.locator('tr.ticket-row:not(.ghost)')
const nodeQueries = (calls: Awaited<ReturnType<typeof mockWork>>) => calls.filter(c => c.path === '/api/nodes' && c.method === 'GET' && c.query.get('q'))
const searches = (calls: Awaited<ReturnType<typeof mockWork>>) => calls.filter(c => c.path === '/api/search').map(c => c.query.get('q'))

async function openProjects(page: Page) {
  const calls = await mockWork(page, fixtures())
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  return calls
}

test('an empty palette shows recents and actions for the project it opened in', async ({ page }) => {
  const errors = watchErrors(page)
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS/PHAROS-12')
  const panel = page.getByRole('complementary', { name: 'Ticket details' })
  await expect(panel.getByRole('heading', { name: 'Add an Oracle Cloud connector' })).toBeVisible()
  await page.keyboard.press('Control+k')
  await expect(palette(page)).toBeVisible()
  await expect(palette(page).getByRole('combobox', { name: 'Search in PHAROS' })).toBeFocused()
  await expect(palette(page).locator('.scope-chip')).toContainText('PHAROS')
  await expect(group(page, 'Recent')).toHaveText([/PHAROS-12\s*Add an Oracle Cloud connector/, /PHAROS\s*Pharos/])
  await expect(group(page, 'Actions')).toHaveText([/New ticket in PHAROS/, /Go to Outline/, /Go to Knowledge/, /Go to Projects/, /Go to Agents/, /Settings/, /Switch to (dark|light) theme/, /Release history/, /Keyboard shortcuts/])
  await expect(group(page, 'Recent').first()).toHaveAttribute('aria-selected', 'true')
  // Tab jumps between groups, arrows move one row, Shift+Tab goes back.
  await page.keyboard.press('Tab')
  await expect(group(page, 'Actions').first()).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('ArrowDown')
  await expect(group(page, 'Actions').nth(1)).toHaveAttribute('aria-selected', 'true')
  // Ctrl+K and Ctrl+J move like arrows while typing, and do not reopen the palette.
  await page.keyboard.press('Control+k')
  await expect(group(page, 'Actions').first()).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Control+j')
  await expect(group(page, 'Actions').nth(1)).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Shift+Tab')
  await expect(group(page, 'Recent').first()).toHaveAttribute('aria-selected', 'true')
  // Recents live in memory only.
  expect(await page.evaluate(() => [...Object.keys(localStorage), ...Object.keys(sessionStorage)].filter(key => /recent/i.test(key)))).toEqual([])
  await page.keyboard.press('Escape')
  await expect(palette(page)).toBeHidden()
  await expect(panel).toBeVisible()
  expect(errors).toEqual([])
})

test('words search titles through the list and the hybrid search, once per pause', async ({ page }) => {
  const calls = await openProjects(page)
  await page.keyboard.press('Control+k')
  await expect(palette(page).getByRole('combobox', { name: 'Search tickets, projects and actions' })).toBeFocused()
  await page.keyboard.type('hetzner')
  const tickets = group(page, 'Tickets')
  await expect(tickets).toHaveCount(2)
  await expect(tickets.first()).toContainText('PHAROS-11')
  await expect(tickets.first().locator('.title mark')).toHaveText('Hetzner')
  await expect(tickets.first().locator('.project-chip')).toHaveText('PHAROS')
  expect(searches(calls)).toEqual(['hetzner'])
  expect(nodeQueries(calls).map(c => [c.query.get('q'), c.query.get('sort'), c.query.get('within')])).toEqual([['hetzner', '-updated_at', null]])
  // Hover selects, click opens.
  await tickets.nth(1).hover()
  await expect(tickets.nth(1)).toHaveAttribute('aria-selected', 'true')
  await tickets.nth(1).click()
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-13')
  await expect(palette(page)).toBeHidden()
})

test('a key prefix finds the exact key first and skips the word search', async ({ page }) => {
  const calls = await openProjects(page)
  await page.keyboard.press('Control+k')
  await page.keyboard.type('pharos-13')
  const first = group(page, 'Tickets').first()
  await expect(first).toContainText('PHAROS-13')
  await expect(first.locator('.key mark')).toHaveText('PHAROS-13')
  expect(searches(calls)).toEqual([])
  expect(nodeQueries(calls).at(-1)?.query.get('sort')).toBe('key')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-13')
})

test('a bare project key offers the project first', async ({ page }) => {
  await openProjects(page)
  await page.keyboard.press('Control+k')
  await page.keyboard.type('aeon')
  await expect(palette(page).locator('.group-label').first()).toHaveText('Projects')
  await expect(group(page, 'Projects').first()).toContainText('Aeon')
  await expect(group(page, 'Tickets').first()).toContainText('AEON-1')
  await page.keyboard.press('Enter')
  // Choosing a project opens its tickets section.
  await expect(page).toHaveURL('/p/AEON/tickets')
})

test('Cmd or Ctrl+Enter opens the result in a new tab and keeps the palette', async ({ page, context }) => {
  await context.route('**/api/**', route => route.fulfill({ status: 401, json: { error: 'unauthorized' } }))
  await openProjects(page)
  await page.keyboard.press('Control+k')
  await page.keyboard.type('pharos-11')
  await expect(group(page, 'Tickets').first()).toContainText('PHAROS-11')
  const [tab] = await Promise.all([context.waitForEvent('page'), page.keyboard.press('Control+Enter')])
  await tab.waitForLoadState('domcontentloaded')
  expect(new URL(tab.url()).pathname).toMatch(/^\/(p\/PHAROS\/PHAROS-11|signin)$/)
  await tab.close()
  await expect(palette(page)).toBeVisible()
  await expect(page).toHaveURL('/')
})

test('inside a project the search is scoped; Backspace on an empty input widens it', async ({ page }) => {
  const calls = await mockWork(page, fixtures())
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await page.keyboard.press('Control+k')
  const input = palette(page).getByRole('combobox', { name: 'Search in PHAROS' })
  await expect(input).toBeFocused()
  await page.keyboard.type('aeon')
  await expect(palette(page)).toContainText('Nothing matches “aeon” in PHAROS.')
  await expect(palette(page).locator('.hint .keycap')).toHaveText('PHAROS-296')
  expect(nodeQueries(calls).at(-1)?.query.get('within')).toBe('p-pharos')
  await input.fill('')
  await page.keyboard.press('Backspace')
  await expect(palette(page).locator('.scope-chip')).toHaveCount(0)
  const wide = palette(page).getByRole('combobox', { name: 'Search tickets, projects and actions' })
  await expect(wide).toBeFocused()
  await page.keyboard.type('aeon')
  await expect(group(page, 'Projects').first()).toContainText('Aeon')
  await expect(group(page, 'Tickets').first().locator('.project-chip')).toHaveText('AEON')
})

test('no results suggest a key, and a scoped search can widen with one click', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.goto('/p/AEON')
  await expect(rows(page)).toHaveCount(1)
  await page.keyboard.press('Control+k')
  await page.keyboard.type('oracle')
  await expect(palette(page)).toContainText('Nothing matches “oracle” in AEON.')
  await palette(page).getByRole('button', { name: 'Search everywhere', exact: true }).click()
  await expect(group(page, 'Tickets').first()).toContainText('Add an Oracle Cloud connector')
  await palette(page).getByRole('combobox').fill('zzzz nothing')
  await expect(palette(page)).toContainText('Nothing matches “zzzz nothing”.')
  await expect(palette(page).locator('.hint .keycap')).toHaveText('PHAROS-296')
})

test('actions: outline, new ticket, theme and the shortcut sheet', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' })
  await mockWork(page, fixtures())
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  const runAction = async (typed: string, label: string) => {
    await page.keyboard.press('Control+k')
    await page.keyboard.type(typed)
    await expect(group(page, 'Actions').first()).toContainText(label)
    await expect(palette(page).getByRole('option').first()).toContainText(label)
    await page.keyboard.press('Enter')
    await expect(palette(page)).toBeHidden()
  }
  await runAction('outline', 'Go to Outline')
  // Outline is a view of the tickets section; list is that section without the view.
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=outline')
  await runAction('list', 'Go to List')
  await expect(page).toHaveURL('/p/PHAROS/tickets')
  await expect(rows(page)).toHaveCount(5)
  await runAction('new ticket', 'New ticket in PHAROS')
  await expect(page.getByLabel('New ticket title')).toBeFocused()
  await page.keyboard.press('Escape')
  await runAction('dark', 'Switch to dark theme')
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await runAction('shortcuts', 'Keyboard shortcuts')
  await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible()
})

test('read-only people get no New ticket action', async ({ page }) => {
  await mockWork(page, fixtures(), { readOnly: true })
  await page.goto('/p/PHAROS')
  await expect(rows(page)).toHaveCount(5)
  await page.keyboard.press('Control+k')
  await expect(group(page, 'Actions').first()).toContainText('Go to Outline')
  await expect(palette(page).getByText(/New ticket in/)).toHaveCount(0)
})

test('/ opens the palette only where the page has no list search', async ({ page }) => {
  await openProjects(page)
  await page.keyboard.press('/')
  await expect(page.getByRole('searchbox', { name: 'Filter projects' })).toBeFocused()
  await expect(palette(page)).toBeHidden()
  await page.goto('/somewhere/else')
  await expect(page.getByRole('heading', { name: 'A little off the path.' })).toBeVisible()
  await page.keyboard.press('/')
  await expect(palette(page)).toBeVisible()
  await page.keyboard.press('Escape')
  await page.getByRole('main').getByRole('button', { name: 'Search' }).click()
  await expect(palette(page)).toBeVisible()
})

test('a newer query cancels the one still in flight', async ({ page }) => {
  await openProjects(page)
  const failed: string[] = []
  page.on('requestfailed', request => { if (request.url().includes('/api/nodes')) failed.push(new URL(request.url()).searchParams.get('q') ?? '') })
  await page.route(url => url.pathname === '/api/nodes' && url.searchParams.get('q') === 'hetz', async route => {
    await new Promise(resolve => setTimeout(resolve, 1500))
    await route.fallback().catch(() => undefined)
  })
  await page.keyboard.press('Control+k')
  const sent = page.waitForRequest(request => new URL(request.url()).searchParams.get('q') === 'hetz' && request.url().includes('/api/nodes'))
  await page.keyboard.type('hetz')
  await expect(palette(page).locator('.skeleton-lines')).toBeVisible()
  await sent
  await page.keyboard.type('ner')
  await expect(group(page, 'Tickets')).toHaveCount(2)
  await expect.poll(() => failed).toContain('hetz')
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`on a phone the palette fits the screen in ${colorScheme}`, async ({ page }) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.emulateMedia({ colorScheme })
    await openProjects(page)
    await page.getByRole('button', { name: 'Search everything' }).click()
    await page.keyboard.type('hetzner')
    await expect(group(page, 'Tickets')).toHaveCount(2)
    const box = (await palette(page).locator('.sheet').boundingBox())!
    expect(box.x).toBeGreaterThanOrEqual(0)
    expect(box.x + box.width).toBeLessThanOrEqual(390)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    expect(errors).toEqual([])
  })
}
