// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { test, expect, type Page, type Locator } from '@playwright/test'
import { fixtures, mockView, mockWork } from './work-fixtures'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import { expectStableControls } from './helpers/stable'

test.use({ launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] } })
const shots = 'test-results/aeon-646'
const rows = (page: Page) => page.getByRole('grid', { name: 'Tickets' }).locator('tr.ticket-row:not(.ghost)')
const row = (page: Page, key: string) => rows(page).filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const gear = (page: Page) => page.getByRole('button', { name: 'Choose what Hide hides', exact: true })
const hide = (page: Page) => page.locator('.closed-switch input')
const chooser = (page: Page, width: number) => width === 390 ? page.getByRole('dialog', { name: 'Filters', exact: true }) : page.getByRole('dialog', { name: 'What Hide hides', exact: true })
async function shot(page: Page, name: string) { await page.evaluate(() => document.fonts.ready); mkdirSync(shots, { recursive: true }); await page.screenshot({ path: `${shots}/${name}.png` }) }
async function setup(page: Page, width = 1440, theme = 'light') {
  await page.setViewportSize({ width, height: 900 })
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences['list:display'] = { headerGraph: false }
  data.projects[0]!.title = 'Pharos · Betriebsübersicht'
  data.projects[0]!.description = 'Überprüfung der mandantenübergreifenden Berechtigungsverwaltung und außergewöhnlich langer Projektbeschreibungen für sämtliche verantwortlichen Personen und Agenten.'
  const seed = data.nodes.find(node => node.id === 'n-5')!
  for (const [index, state] of ['delivered', 'accepted', 'archived', 'canceled'].entries()) data.nodes.push({ ...seed, fields: {}, id: `hide-${index}`, key: `PHAROS-${30 + index}`, state, title: `Berechtigungsverwaltung ${state}` })
  data.views.push(mockView({ id: '44444444-4444-4444-8444-444444444444', name: 'Archivierte Vorgänge ausblenden', filters: { hide_states: 'archived' } }))
  const queries: URLSearchParams[] = []
  page.on('request', request => { const url = new URL(request.url()); if (url.pathname === '/api/nodes' && url.searchParams.has('within')) queries.push(url.searchParams) })
  await mockWork(page, data)
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
  await page.goto('/p/PHAROS')
  await expect(row(page, 'PHAROS-11')).toBeVisible()
  return { data, queries }
}
async function open(page: Page, width: number) {
  if (width === 390) await page.getByRole('button', { name: 'Filters', exact: true }).click()
  else await gear(page).click()
  const panel = chooser(page, width)
  await expect(panel.getByRole('heading', { name: 'What Hide hides', exact: true })).toBeVisible()
  return panel
}
async function close(page: Page, width: number) {
  if (width === 390) await chooser(page, width).locator('footer button').click()
  else await page.keyboard.press('Escape')
  await expect(chooser(page, width)).toBeHidden()
}
for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
  test(`Hide choices and stable controls at ${width}px ${theme}`, async ({ page }) => {
    const errors: string[] = []; page.on('pageerror', error => errors.push(error.message))
    const { queries } = await setup(page, width, theme)
    await expect(row(page, 'PHAROS-15')).toHaveCount(0)
    await shot(page, `${width}-${theme}-default`)
    const panel = await open(page, width)
    const options = panel.getByRole('region', { name: 'What Hide hides', exact: true })
    const check = (name: string) => options.getByRole('checkbox', { name, exact: true })
    const controls: Record<string, Locator> = {
      reset: options.getByRole('button', { name: 'Reset', exact: true }), group: options.getByRole('group', { name: 'Finished' }),
      done: check('Done'), delivered: check('Delivered'), accepted: check('Accepted'), cancelled: check('Cancelled'), archived: check('Archived'),
      row: options.locator('.hide-option[data-state="cancelled"]'),
      ...(width === 390 ? { frame: panel, header: panel.locator('header'), action: panel.locator('footer button'), toggle: panel.locator('.switch input').first() }
        : { hide: hide(page), label: page.locator('.closed-switch'), gear: gear(page), display: page.locator('.project-view-settings .display-btn'), search: page.getByRole('searchbox', { name: 'Search tickets in this project' }) }),
    }
    await expectStableControls({ controls, scrollAreas: width === 390 ? { body: panel.locator('.sheet-scroll') } : { body: panel, page: page.locator('.project-page') }, interactions: [
      { name: 'finished set', run: async () => { await check('Cancelled').uncheck(); await check('Archived').uncheck(); await expect(page).toHaveURL(/hide_states=done,delivered,accepted/); await expect(options.locator('.hide-result')).toHaveText('Hides 3 tickets now'); await shot(page, `${width}-${theme}-finished`) } },
      { name: 'icons identify a custom set', run: async () => { await check('Done').uncheck(); await expect(page).toHaveURL(/hide_states=delivered,accepted/); await expect(options.locator('.hide-result')).toHaveText('Hides 2 tickets now'); await shot(page, `${width}-${theme}-custom`) } },
      { name: 'one status cannot be unchecked', run: async () => { await check('Delivered').uncheck(); await expect(check('Accepted')).toBeDisabled(); await expect(check('Accepted')).toBeChecked(); await expect(page).toHaveURL(/hide_states=accepted/) } },
      { name: 'reset', run: async () => { await options.getByRole('button', { name: 'Reset', exact: true }).click(); await expect(page).not.toHaveURL(/hide_states=/); await expect(options.locator('.hide-result')).toHaveText('Hides 6 tickets now') } },
    ] })
    // Each individual interaction is guarded too, rather than only the final set.
    for (const name of ['Done', 'Delivered', 'Accepted', 'Cancelled', 'Archived']) {
      await expectStableControls({ controls, interactions: [{ name: `remove ${name}`, run: async () => { await check(name).uncheck(); await expect(check(name)).not.toBeChecked() } }, { name: `restore ${name}`, run: async () => { await check(name).check(); await expect(check(name)).toBeChecked() } }] })
    }
    await check('Done').uncheck(); await check('Delivered').uncheck(); await check('Cancelled').uncheck(); await check('Archived').uncheck()
    await close(page, width)
    await expect(row(page, 'PHAROS-15')).toBeVisible()
    await expect(row(page, 'PHAROS-31')).toHaveCount(0)
    await expect(row(page, 'PHAROS-33')).toBeVisible()
    expect(queries.some(query => query.get('hide_closed') === 'true' && query.get('hide_states') === 'accepted')).toBe(true)
    if (width !== 390) { await expect(hide(page)).toHaveAccessibleName('Hide Accepted'); await expect(gear(page)).toBeFocused() }
    await shot(page, `${width}-${theme}-accepted`)
    await page.reload(); await expect(row(page, 'PHAROS-15')).toBeVisible(); await expect(row(page, 'PHAROS-31')).toHaveCount(0)
    await open(page, width); await expect(chooser(page, width).getByRole('checkbox', { name: 'Accepted', exact: true })).toBeChecked()
    if (width !== 390) { await page.getByRole('searchbox', { name: 'Search tickets in this project' }).click(); await expect(chooser(page, width)).toBeHidden() }
    else await close(page, width)
    expect(errors).toEqual([])
  })
}

test('custom Hide and automatic restoration persist with a saved view and manual toggles win', async ({ page }) => {
  const { data } = await setup(page)
  await page.getByRole('link', { name: 'Archivierte Vorgänge ausblenden', exact: true }).click()
  await expect(page).toHaveURL(/hide_states=archived/)
  await expect(row(page, 'PHAROS-31')).toBeVisible(); await expect(row(page, 'PHAROS-32')).toHaveCount(0)
  await expect(hide(page)).toHaveAccessibleName('Hide Archived')
  await page.getByRole('radio', { name: 'Comfortable project header', exact: true }).click()
  const archived = page.locator('.project-status-counts [data-state="archived"]')
  const accepted = page.locator('.project-status-counts [data-state="accepted"]')
  await expect(archived).toHaveClass(/is-hidden/); await expect(accepted).not.toHaveClass(/is-hidden/)
  await accepted.click(); await expect(hide(page)).toBeChecked(); await expect(row(page, 'PHAROS-31')).toBeVisible()
  await archived.click(); await expect(hide(page)).not.toBeChecked(); await expect(page).toHaveURL(/hide_restore=1/)
  await page.reload(); await expect(hide(page)).not.toBeChecked(); await expect(row(page, 'PHAROS-32')).toBeVisible()
  await page.getByRole('button', { name: 'Remove Status filter', exact: true }).click()
  await expect(hide(page)).toBeChecked(); await expect(row(page, 'PHAROS-32')).toHaveCount(0)
  await archived.click(); await expect(hide(page)).not.toBeChecked()
  const panel = await open(page, 1440)
  await panel.getByRole('checkbox', { name: 'Accepted', exact: true }).check()
  await panel.getByRole('checkbox', { name: 'Archived', exact: true }).uncheck()
  await expect(hide(page)).toBeChecked(); await expect(page).not.toHaveURL(/hide_restore=1/)
  await close(page, 1440)
  await hide(page).uncheck(); await page.getByRole('button', { name: 'Remove Status filter', exact: true }).click()
  await expect(hide(page)).not.toBeChecked(); await expect(row(page, 'PHAROS-31')).toBeVisible()
  const saved = page.waitForResponse(response => response.request().method() === 'PATCH' && new URL(response.url()).pathname.startsWith('/api/views/'))
  await page.getByRole('button', { name: 'Save changes to the view', exact: true }).click()
  expect((await saved).status()).toBe(200)
  const stored = data.views.find(view => view.name === 'Archivierte Vorgänge ausblenden')!
  expect(stored.filters).toMatchObject({ hide_states: 'accepted', closed: '1' })
  await page.getByRole('link', { name: 'All tickets', exact: true }).click()
  await expect(row(page, 'PHAROS-31')).toHaveCount(0)
  await page.getByRole('link', { name: stored.name, exact: true }).click()
  await expect(hide(page)).not.toBeChecked(); await expect(hide(page)).toHaveAccessibleName('Hide Accepted')
  await expect(row(page, 'PHAROS-31')).toBeVisible()
})

test.describe('shared view membership', () => {
  test('Graph and Outline use the chosen Hide membership', async ({ page }) => {
    const world = ticketGraphWorld()
    const finished = world.graph.nodes.filter(node => node.status_category === 'done')
    finished[0]!.status = 'accepted'; finished[1]!.status = 'archived'
    for (const node of finished.slice(0, 2)) world.work.nodes.find(item => item.id === node.id)!.state = node.status
    const { calls } = await mockTicketGraph(page, world)
    await page.goto('/p/PHAROS?view=graph&hide_states=accepted')
    await expect(page.locator('.graph-heading [role="status"]')).toHaveText(/59 tickets · 5 epics/)
    expect(calls.some(query => query.get('include_closed') === 'true')).toBe(true)
    const panel = await open(page, 1440)
    await panel.getByRole('checkbox', { name: 'Archived', exact: true }).check()
    await panel.getByRole('checkbox', { name: 'Done', exact: true }).check()
    await expect(page.locator('.graph-heading [role="status"]')).toHaveText(/50 tickets/)
    await close(page, 1440)
    await page.getByRole('tab', { name: 'Outline', exact: true }).click()
    await expect(page).toHaveURL(/hide_states=done,accepted,archived/)
    const outline = page.getByRole('treegrid', { name: 'Ticket outline' })
    for (const epic of world.graph.nodes.filter(node => node.type === 'epic')) await outline.getByRole('button', { name: `Expand ${epic.key}`, exact: true }).click()
    await expect(outline.locator('tr.ticket-row:not(.ghost)')).toHaveCount(50)
    for (const node of finished) await expect(outline.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${node.key}$`) }) })).toHaveCount(0)
    await page.getByRole('tab', { name: 'List', exact: true }).click()
    await expect(rows(page)).toHaveCount(50)
    for (const node of finished) await expect(row(page, node.key)).toHaveCount(0)
  })
})

test('missing summary counts are honest and Hide off reports zero', async ({ page }) => {
  await setup(page)
  await page.route('**/api/projects**', route => route.fulfill({ json: { items: [{ id: 'p-pharos', key: 'PHAROS', title: 'Pharos', state: 'active', total: 12, open: 6, in_progress: 0, done: 3, cancelled: 2, archived_count: 1, last_activity: '', status_counts: [], status_counts_truncated: true }] } }))
  await page.reload()
  const panel = await open(page, 1440)
  await expect(panel.locator('.hide-result')).toHaveText('Hidden count unavailable')
  await expect(panel.locator('.hide-option b').first()).toHaveText('—')
  await close(page, 1440); await hide(page).uncheck(); await open(page, 1440)
  await expect(panel.locator('.hide-result')).toHaveText('Hides 0 tickets now')
})
