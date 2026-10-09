// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import { me, mockView } from './work-fixtures'
import { headerStorageKey } from '../src/lib/projectHeader'

test.use({ viewport: { width: 1600, height: 1000 }, launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] } })
test.setTimeout(60_000)
const canvas = (page: Page) => page.locator('.ticket-graph-canvas')
const expectNodes = (page: Page, total: number, parents: number) => expect(canvas(page)).toHaveAttribute('aria-label', new RegExp(`${total - parents} ${total - parents === 1 ? 'leaf' : 'leaves'} · ${parents} ${parents === 1 ? 'parent' : 'parents'} ·`))
const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const views = (page: Page) => page.getByRole('tablist', { name: 'Ticket views' })
const ready = async (page: Page) => {
  await expect(canvas(page)).toHaveAttribute('data-ready', 'true', { timeout: 20000 })
  await expect(page.locator('.tg-state')).toHaveCount(0)
}
const frames = (page: Page) => page.evaluate(() => new Promise<void>(done => {
  let count = 8
  const frame = () => --count ? requestAnimationFrame(frame) : done()
  requestAnimationFrame(frame)
}))

test('Graph is the third Tickets view; URL, reload, List and Outline keep their place', async ({ page }) => {
  const { calls } = await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets')
  await expect(views(page).getByRole('tab')).toHaveText(['List', 'Outline', 'Graph'])
  await views(page).getByRole('tab', { name: 'Graph', exact: true }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=graph')
  await ready(page)
  expect(calls[0].get('project_id')).toBe('p-pharos')
  await expect(canvas(page)).toHaveAttribute('data-fps', '60')
  await expect(canvas(page)).toHaveAttribute('data-labels', 'smart')
  await page.reload(); await ready(page)
  await expect(views(page).getByRole('tab', { name: 'Graph', exact: true })).toHaveAttribute('aria-selected', 'true')
  await views(page).getByRole('tab', { name: 'Outline', exact: true }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=outline')
  await expect(canvas(page)).toHaveCount(0)
  await views(page).getByRole('tab', { name: 'List', exact: true }).click()
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=list')
})

test('one bubble click opens the existing panel; Escape and browser Back keep the mounted graph', async ({ page }) => {
  const world = ticketGraphWorld()
  world.graph = { nodes: [world.graph.nodes[1]], links: [], truncated: false }
  await mockTicketGraph(page, world)
  await page.goto('/p/PHAROS/tickets?view=graph'); await ready(page)
  const original = await canvas(page).elementHandle()
  await page.getByRole('button', { name: 'Fit graph to view' }).click(); await frames(page)
  const box = (await canvas(page).boundingBox())!
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await expect(page.getByRole('tooltip')).toContainText('Preview rollout changes')
  await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2)
  await expect(page).toHaveURL('/p/PHAROS/PHAROS-101?view=graph')
  await expect(panel(page).getByRole('heading', { name: 'Preview rollout changes', exact: true })).toBeVisible()
  await frames(page)
  // Wait for the renderer's ResizeObserver, not merely the panel's first DOM.
  await expect.poll(() => canvas(page).evaluate(el => Math.abs(el.clientWidth - el.querySelector('canvas')!.getBoundingClientRect().width))).toBeLessThan(2)
  const narrowed = (await canvas(page).boundingBox())!
  expect(narrowed.width).toBeLessThan(box.width - 200)
  await page.keyboard.press('Escape')
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=graph')
  await expect(panel(page)).toHaveCount(0)
  await expect(canvas(page)).toBeFocused()
  // Keyboard selection followed by Enter uses the same panel route.
  await canvas(page).press('Enter')
  await expect(panel(page)).toBeVisible()
  await page.goBack()
  await expect(page).toHaveURL('/p/PHAROS/tickets?view=graph')
  await expect(panel(page)).toHaveCount(0)
  expect(await original!.evaluate(el => el.isConnected)).toBe(true)
})

test('Hide closed reloads the graph projection; the legend and bounded-result notice are visible', async ({ page }) => {
  const world = ticketGraphWorld(); world.graph.truncated = true
  const { calls } = await mockTicketGraph(page, world)
  await page.goto('/p/PHAROS/tickets?view=graph'); await ready(page)
  await expectNodes(page, 50, 5)
  expect(calls[0].get('include_closed')).toBe('true')
  const legend = page.getByRole('group', { name: 'Ticket graph legend' })
  for (const label of ['Open', 'In progress', 'Closed', 'Parent hub', 'Parent', 'Blocks', 'Relates', 'Implements', 'Duplicates']) await expect(legend.getByText(label, { exact: true })).toBeVisible()
  await expect(page.getByText('This project’s graph is truncated; some tickets or links are not shown.')).toBeVisible()
  await page.getByRole('checkbox', { name: 'Hide closed', exact: true }).uncheck()
  await expect(page).toHaveURL(/closed=1/)
  await expectNodes(page, 60, 5)
  expect(calls.at(-1)!.get('include_closed')).toBe('true')
  await page.getByRole('checkbox', { name: 'Hide closed', exact: true }).check()
  await expectNodes(page, 50, 5)
})

test('status, priority, type and search use the Tickets query', async ({ page }) => {
  const { calls } = await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets?view=graph'); await ready(page)
  await expectNodes(page, 50, 5)
  await expect(page.getByRole('button', { name: /^Assignee/ })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^Display:/ })).toHaveCount(0)
  await page.getByRole('button', { name: 'Filter by more' }).click()
  await expect(page.getByRole('menu', { name: 'Filter by' }).getByRole('menuitem')).toHaveText(['Status', 'Priority', 'Parents / Leaves', 'Depth', 'Legacy type'])
  await page.keyboard.press('Escape')
  await page.locator('.facet-btn[data-dim="status"]').click()
  await page.getByRole('checkbox', { name: /^In progress/ }).check()
  await page.keyboard.press('Escape')
  await expectNodes(page, 20, 5)
  await page.locator('.facet-btn[data-dim="priority"]').click()
  await page.getByRole('checkbox', { name: /^High/ }).check()
  await page.keyboard.press('Escape')
  await page.locator('.facet-btn[data-dim="type"]').click()
  await page.getByRole('checkbox', { name: /^Epic/ }).check()
  await page.keyboard.press('Escape')
  await expectNodes(page, 5, 5)
  await page.getByRole('searchbox', { name: 'Search tickets in this project' }).fill('Reliable')
  await expectNodes(page, 1, 1)
  expect(calls.length).toBeGreaterThan(1)
  await expect(page).toHaveURL(/q=Reliable/)
  await views(page).getByRole('tab', { name: 'List', exact: true }).click()
  await expect(page).toHaveURL(/status=in_progress/)
})

test('saved assignee, type, priority, status and body search match the header context', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.addInitScript(({ key }) => localStorage.setItem(key, JSON.stringify({ density: 'comfortable', roomy: 'comfortable' })), { key: headerStorageKey('t1', me.id) })
  const world = ticketGraphWorld(), owner = world.work.people[0].id
  const view = mockView({ id: '11111111-1111-4111-8111-111111111111', name: 'My planned work', filters: { assignee: owner, type: 'ticket', priority: 'high', status: 'in_progress', q: 'acceptance' } })
  world.work.views.push(view)
  for (const node of world.work.nodes) if (node.project === 'p-pharos') {
    node.fields.assignee = Number(node.key.split('-')[1]) < 148 ? owner : world.work.people[1].id
    node.body = 'Acceptance evidence for this change'
  }
  const matching = world.graph.nodes.filter(node => node.type === 'ticket' && node.priority === 'high')
  for (let i = 1; i < matching.length; i++) world.graph.links.push({ source: matching[i - 1].id, target: matching[i].id, kind: 'relates' })
  await mockTicketGraph(page, world)
  await page.goto('/p/PHAROS/tickets')
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveAttribute('data-shown', '50', { timeout: 20_000 })
  await page.getByRole('link', { name: 'My planned work', exact: true }).click()
  await expect(page.locator('[data-header-glimpse="on"]')).toHaveAttribute('data-shown', '12', { timeout: 20_000 })
  await page.locator('.project-head').hover()
  await page.getByRole('button', { name: 'Open graph', exact: true }).click()
  await ready(page)
  await expectNodes(page, 12, 0)
  await expect(page).toHaveURL(/v=11111111-1111-4111-8111-111111111111/)
  await page.locator('.facet-btn[data-dim="status"]').click()
  await expect(page.locator('.facet-option').filter({ has: page.getByRole('checkbox', { name: /^In progress/ }) }).locator('.count')).toHaveText('12')
})

test('mobile filters contain only supported dimensions and Hide closed', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets?view=graph'); await ready(page)
  await page.getByRole('button', { name: 'Filters', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Filters', exact: true })
  await expect(sheet.locator('.sheet-section > .eyebrow')).toHaveText(['Display', 'Status', 'Priority', 'Parents / Leaves', 'Depth', 'Legacy type'])
  await sheet.getByRole('checkbox', { name: 'Hide closed tickets' }).uncheck()
  await expect(sheet.getByRole('button', { name: 'Show 60 tickets' })).toBeVisible()
  await sheet.getByRole('button', { name: 'Show 60 tickets' }).click()
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390)
})

test('focus, labels and motion use core controls; opening a ticket leaves the focus trap', async ({ page }) => {
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets?view=graph'); await ready(page)
  await page.getByRole('combobox', { name: 'Graph labels' }).selectOption('off')
  await expect(canvas(page)).toHaveAttribute('data-labels', 'off')
  await page.getByRole('button', { name: 'Resume motion' }).click()
  await expect(canvas(page)).toHaveAttribute('data-motion', 'on')
  await page.getByRole('button', { name: 'Pause motion' }).click()
  await page.getByRole('button', { name: 'Maximize graph' }).click()
  await expect(page).toHaveURL(/focus=1/)
  await canvas(page).press('Escape')
  await expect(page).not.toHaveURL(/focus=1/)
  await page.getByRole('button', { name: 'Maximize graph' }).click()
  await canvas(page).press('ArrowRight'); await canvas(page).press('Enter')
  await expect(panel(page)).toBeVisible()
  await expect(page).not.toHaveURL(/focus=1/)
  expect(await panel(page).evaluate(el => !!el.closest('[inert]'))).toBe(false)
})

test('failed and empty responses explain themselves and a retry recovers', async ({ page }) => {
  const { graph } = await mockTicketGraph(page)
  let fail = true
  await page.route('**/api/tickets/graph?*', route => route.fulfill(fail ? { status: 503, json: { error: 'unavailable' } } : { json: graph }))
  await page.goto('/p/PHAROS/tickets?view=graph')
  await expect(page.getByRole('alert')).toContainText('The ticket graph could not be loaded')
  fail = false
  await page.getByRole('button', { name: 'Try again', exact: true }).click(); await ready(page)
  await page.getByRole('searchbox', { name: 'Search tickets in this project' }).fill('no matching ticket')
  await expect(page.getByRole('heading', { name: 'No tickets match these filters' })).toBeVisible()
})
