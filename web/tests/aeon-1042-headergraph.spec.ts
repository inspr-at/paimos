// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1042: the project-header graph is off by default and only Settings ›
// Developer turns it on. Risks: an earlier Display-menu "on" still shows it; the
// header leaves room for a graph that is off; turning it on or off moves a
// header control (AEON-541).
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { me } from './work-fixtures'
import { headerStorageKey } from '../src/lib/projectHeader'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import { controlStability } from './control-stability'

test.use({
  reducedMotion: 'no-preference',
  launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] },
})
test.setTimeout(90_000)

const shots = 'test-results/aeon-1042-headergraph'
const errors = new WeakMap<Page, string[]>()
test.beforeEach(async ({ page }) => {
  const failures: string[] = []; errors.set(page, failures)
  page.on('pageerror', error => failures.push(error.message))
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
})
test.afterEach(async ({ page }) => { expect(errors.get(page), 'no browser runtime errors').toEqual([]) })

async function settled(page: Page) { await page.evaluate(async () => { await document.fonts.ready; await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))) }) }
async function capture(page: Page, name: string) { await settled(page); mkdirSync(shots, { recursive: true }); await page.screenshot({ path: `${shots}/${name}.png` }) }
// Absence only counts once the developer choice has been read and rendered.
async function openProject(page: Page) {
  const read = page.waitForResponse(response => new URL(response.url()).pathname === '/api/preferences/developer-ui')
  await page.goto('/p/PHAROS/tickets')
  await read
  await expect(page.getByRole('heading', { name: 'Pharos', exact: true })).toBeVisible()
  await expect(page.getByRole('searchbox', { name: 'Search tickets in this project' })).toBeVisible()
  await settled(page)
}
async function switchGraph(page: Page, world: ReturnType<typeof ticketGraphWorld>, on: boolean) {
  await page.goto('/settings/developer')
  const graph = page.getByRole('switch', { name: 'Graph in project header', exact: true })
  await expect(graph).toBeChecked({ checked: !on })
  await graph.setChecked(on)
  await expect.poll(() => (world.work.preferences['developer-ui'] as { show_header_graph?: boolean } | undefined)?.show_header_graph).toBe(on)
  await expect(graph).toBeEnabled()
}

for (const width of [1440, 400]) for (const theme of ['light', 'dark'] as const) {
  test(`header graph off by default, developer switch keeps the header still ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.emulateMedia({ colorScheme: theme })
    // The roomy header is the graph's designed home; an earlier Display "on" is ignored.
    await page.addInitScript(({ key }) => localStorage.setItem(key, JSON.stringify({ density: 'comfortable', roomy: 'comfortable' })), { key: headerStorageKey('t1', me.id) })
    const world = ticketGraphWorld()
    delete world.work.preferences['developer-ui']
    world.work.preferences.theme = { choice: theme }
    world.work.preferences['list:display'] = { density: 'comfortable', headerGraph: true }
    await mockTicketGraph(page, world)
    await openProject(page)
    await expect(page.locator('[data-header-glimpse]')).toHaveCount(0)
    await capture(page, `off-${width}-${theme}`)

    const head = page.locator('.project-head')
    const guard = await controlStability(page, {
      head,
      title: page.locator('#project-title'),
      counts: page.getByRole('group', { name: 'Filter tickets by status' }),
      sections: page.getByRole('tablist', { name: 'Project sections' }),
      search: page.getByRole('searchbox', { name: 'Search tickets in this project' }),
      create: page.getByRole('button', { name: 'New ticket', exact: true }),
    })
    await guard.check(async () => {
      await switchGraph(page, world, true)
      await openProject(page)
      // Wide windows draw the graph; narrow ones mount it switched off by width.
      if (width >= 1280) await expect(page.locator('.header-glimpse-canvas')).toHaveAttribute('data-ready', 'true', { timeout: 30_000 })
      else await expect(page.locator('[data-header-glimpse="off"]')).toBeAttached()
    })
    await capture(page, `on-${width}-${theme}`)
    await guard.check(async () => {
      await switchGraph(page, world, false)
      await openProject(page)
      await expect(page.locator('[data-header-glimpse]')).toHaveCount(0)
    })
    guard.done()
    await page.goto('/settings/developer')
    await expect(page.getByRole('switch', { name: 'Graph in project header', exact: true })).not.toBeChecked()
    await capture(page, `settings-${width}-${theme}`)
  })
}
