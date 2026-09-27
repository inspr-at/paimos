// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import { mockView } from './work-fixtures'

test.use({ viewport: { width: 1600, height: 1000 }, reducedMotion: 'no-preference', launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] } })
test.setTimeout(60_000)
const glimpse = (page: Page) => page.locator('[data-header-glimpse="on"]')
const ready = (page: Page, count: number) => expect(glimpse(page)).toHaveAttribute('data-shown', String(count), { timeout: 20_000 })

test('Hide closed and status use the Tickets context and Open graph retains it', async ({ page }) => {
  const { calls } = await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets')
  await ready(page, 50)
  expect(calls.at(-1)!.get('include_closed')).toBeNull()
  await expect(page.locator('.glimpse-count')).toHaveText('50 / 60')
  await page.getByRole('checkbox', { name: 'Hide closed', exact: true }).uncheck()
  await ready(page, 60)
  expect(calls.at(-1)!.get('include_closed')).toBe('true')
  await page.getByRole('checkbox', { name: 'Hide closed', exact: true }).check()
  await ready(page, 50)
  await page.locator('.facet-btn[data-dim="status"]').click()
  await page.getByRole('checkbox', { name: /^In progress/ }).check()
  await page.keyboard.press('Escape')
  await ready(page, 20)
  await page.locator('.project-head').hover()
  await page.getByRole('button', { name: 'Open graph', exact: true }).click()
  await expect(page).toHaveURL(/status=in_progress/)
  await expect(page.locator('.ticket-graph-canvas')).toHaveAttribute('aria-label', /20 tickets/)
})

test('saved views, assignee, type, priority and server search determine the glimpse', async ({ page }) => {
  const world = ticketGraphWorld(), owner = world.work.people[0].id
  const view = mockView({ id: '11111111-1111-4111-8111-111111111111', name: 'My planned work', filters: { assignee: owner, type: 'ticket', priority: 'high', status: 'in_progress', q: 'acceptance' } })
  world.work.views.push(view)
  for (const node of world.work.nodes) if (node.project === 'p-pharos') {
    node.fields.assignee = Number(node.key.split('-')[1]) < 148 ? owner : world.work.people[1].id
    // The server search also sees content outside the graph's title projection.
    node.body = 'Acceptance evidence for this change'
  }
  const matching = world.graph.nodes.filter(node => node.type === 'ticket' && node.priority === 'high')
  for (let i = 1; i < matching.length; i++) world.graph.links.push({ source: matching[i - 1].id, target: matching[i].id, kind: 'relates' })
  await mockTicketGraph(page, world)
  await page.goto('/p/PHAROS/tickets')
  await ready(page, 50)
  await page.getByRole('link', { name: 'My planned work', exact: true }).click()
  await ready(page, 12)
  await expect(page).toHaveURL(/v=11111111-1111-4111-8111-111111111111/)
  await expect(page.locator('.glimpse-count')).toHaveText('12 / 60')
  await page.getByRole('link', { name: 'All tickets', exact: true }).click()
  await ready(page, 50)
})

test('a large project with fewer than eight linked matches has no glimpse', async ({ page }) => {
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets?type=epic')
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
  await expect(glimpse(page)).toHaveCount(0)
})

test('scrolling the entire backdrop out of view pauses it and returning resumes it', async ({ page }) => {
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/tickets')
  await ready(page, 50)
  const canvas = page.locator('.header-glimpse-canvas')
  await expect(canvas).toHaveAttribute('data-motion', 'on')
  await page.locator('#main').evaluate(el => { el.scrollTop = 600 })
  await expect(canvas).toHaveAttribute('data-motion', 'still')
  await page.locator('#main').evaluate(el => { el.scrollTop = 0 })
  await expect(canvas).toHaveAttribute('data-motion', 'on')
})


test('other sections keep their own search out of the ticket backdrop', async ({ page }) => {
  await mockTicketGraph(page)
  await page.goto('/p/PHAROS/knowledge?q=not-a-ticket-search')
  await ready(page, 50)
})
