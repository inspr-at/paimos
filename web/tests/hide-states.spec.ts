// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page, type Locator } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { fixtures, mockView, mockWork } from './work-fixtures'
import { mockTicketGraph, ticketGraphWorld } from './ticket-graph-fixtures'
import { expectStableControls } from './helpers/stable'

test.use({ launchOptions: { args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader'] } })
const rows = (page: Page) => page.getByRole('grid', { name: 'Tickets' }).locator('tr.ticket-row:not(.ghost)')
const row = (page: Page, key: string) => rows(page).filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
const gear = (page: Page) => page.getByRole('button', { name: 'Choose what Hide hides', exact: true })
const hide = (page: Page) => page.locator('.closed-switch input')
// AEON-1559 hides the labelled switch once the navigation row takes the pill.
const rowHide = (page: Page) => page.locator('#project-view-settings .closed-switch input:visible, #project-view-settings .closed-pill:visible')
const chooser = (page: Page, width: number) => width === 390 ? page.getByRole('dialog', { name: 'Filters', exact: true }) : page.getByRole('dialog', { name: 'What Hide hides', exact: true })
async function shot(page: Page, name: string) { await page.evaluate(() => document.fonts.ready); await page.screenshot({ path: test.info().outputPath(`${name}.png`) }) }
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
        : { hide: rowHide(page), gear: gear(page), display: page.locator('.project-view-settings .display-btn'), search: page.getByRole('searchbox', { name: 'Search tickets in this project' }) }),
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
    if (width !== 390) {
      const step = Number(await page.locator('#project-view-settings .view-settings').getAttribute('data-navigation-step'))
      const toggle = rowHide(page)
      await expect(toggle).toHaveAccessibleName(step >= 2 ? 'Hide Accepted tickets' : 'Hide Accepted')
      if (step < 2) await expect(page.locator('.closed-switch .hide-label-slot > .label:not(.measure)')).toHaveText('Hide Accepted')
      else if (step === 2) await expect(page.locator('.closed-pill .hide-label-slot > .label:not(.measure)')).toHaveText('Hide Accepted')
      await expect(gear(page)).toBeFocused()
    }
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
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
    test(`default and Reset Graph Hide honour kind categories at ${width}px ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 })
      const world = ticketGraphWorld()
      world.work.preferences.theme = { choice: theme }
      world.work.preferences['list:display'] = { headerGraph: false }
      const accepted = world.graph.nodes.find(node => node.status_category === 'done')!
      const qa = world.graph.nodes.find(node => node.type === 'ticket' && node.status_category === 'open')!
      accepted.status = 'accepted'; qa.status = 'qa'; qa.status_category = 'doing'
      world.work.nodes.find(node => node.id === accepted.id)!.state = 'accepted'
      world.work.nodes.find(node => node.id === qa.id)!.state = 'qa'
      const { calls } = await mockTicketGraph(page, world, { workBuckets: { ticket: { accepted: 'open', qa: 'done' } } })
      await page.goto('/p/PHAROS?view=graph')
      const canvas = page.locator('.ticket-graph-canvas')
      const membership = async () => {
        await expect(page.locator('.graph-heading [role="status"]')).toHaveText(/45 leaves · 5 parents ·/)
        await expect(canvas).toHaveAttribute('data-ready', 'true')
        await expect(canvas.locator(`[data-node-id="${accepted.id}"]`)).toHaveCount(1)
        await expect(canvas.locator(`[data-node-id="${qa.id}"]`)).toHaveCount(0)
        expect(calls.at(-1)!.get('include_closed')).toBe('true')
      }
      await membership()
      await shot(page, `${width}-${theme}-graph-default`)
      const panel = await open(page, width)
      const options = panel.getByRole('region', { name: 'What Hide hides', exact: true })
      const done = options.getByRole('checkbox', { name: 'Done', exact: true })
      const reset = options.getByRole('button', { name: 'Reset', exact: true })
      await expectStableControls({ controls: {
        done, reset, accepted: options.getByRole('checkbox', { name: 'Accepted', exact: true }),
        group: options.getByRole('group', { name: 'Finished' }), row: options.locator('.hide-option[data-state="done"]'),
        ...(width === 390 ? { frame: panel, action: panel.locator('footer button') }
          : { hide: page.locator('.hide-control').locator('.closed-switch input:visible, .closed-pill:visible'), gear: gear(page) }),
      }, scrollAreas: { body: width === 390 ? panel.locator('.sheet-scroll') : panel }, interactions: [
        { name: 'include Done-bucket work', run: async () => {
          await done.uncheck()
          await expect(page.locator('.graph-heading [role="status"]')).toHaveText(/55 leaves · 5 parents ·/)
          await expect(canvas.locator(`[data-node-id="${accepted.id}"]`)).toHaveCount(1)
          await expect(canvas.locator(`[data-node-id="${qa.id}"]`)).toHaveCount(1)
        } },
        { name: 'Reset preserves kind-aware default membership', run: async () => {
          await reset.click()
          await expect(page).not.toHaveURL(/hide_states=/)
          await expect(done).toBeChecked()
          await membership()
        } },
      ] })
      await close(page, width)
      await shot(page, `${width}-${theme}-graph-reset`)
    })
  }

  test('Graph and Outline use the chosen Hide membership', async ({ page }) => {
    const world = ticketGraphWorld()
    const finished = world.graph.nodes.filter(node => node.status_category === 'done')
    finished[0]!.status = 'accepted'; finished[1]!.status = 'archived'
    for (const node of finished.slice(0, 2)) world.work.nodes.find(item => item.id === node.id)!.state = node.status
    const { calls } = await mockTicketGraph(page, world)
    await page.goto('/p/PHAROS?view=graph&hide_states=accepted')
    await expect(page.locator('.graph-heading [role="status"]')).toHaveText(/54 leaves · 5 parents ·/)
    expect(calls.some(query => query.get('include_closed') === 'true')).toBe(true)
    const panel = await open(page, 1440)
    await panel.getByRole('checkbox', { name: 'Archived', exact: true }).check()
    await panel.getByRole('checkbox', { name: 'Done', exact: true }).check()
    await expect(page.locator('.graph-heading [role="status"]')).toHaveText(/45 leaves · 5 parents ·/)
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

// AEON-1559: teleported controls must fit their navigation row.
{
const started = new WeakMap<Page, number>()
test.beforeEach(async ({ page }) => { started.set(page, performance.now()) })
test.afterEach(async ({ page }, testInfo) => {
  await writeFile(testInfo.outputPath('aeon1559-timing.md'), JSON.stringify({ name: testInfo.title, status: testInfo.status, seconds: (performance.now() - started.get(page)!) / 1000 }) + '\n')
})

const navigation = (page: Page) => page.locator('.project-navigation')
const settings = (page: Page) => page.locator('#project-view-settings .view-settings')
const step = async (page: Page) => Number(await settings(page).getAttribute('data-navigation-step'))

async function setup(page: Page, theme = 'light', pane = false) {
  await page.setViewportSize({ width: pane ? 2200 : 1600, height: 1000 })
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  data.preferences['list:display'] = { headerGraph: false }
  data.projects[0]!.title = 'Pharos · Berechtigungsverwaltung'
  data.projects[0]!.description = 'Überprüfung der mandantenübergreifenden Berechtigungsverwaltung für sämtliche verantwortlichen Personen und Agenten.'
  await mockWork(page, data)
  await page.route('**/api/queue?*', route => route.fulfill({ json: { items: [], manual_order: false, capacity: { queued_hours: 0, parallel_runs: 0, work_hours: 0, warning: false } } }))
  await page.goto(pane ? '/p/PHAROS/PHAROS-11' : '/p/PHAROS')
  await expect(settings(page)).toBeVisible()
  if (pane) {
    await expect(page.getByRole('complementary', { name: 'Ticket details' })).toBeVisible()
    // The same user-selected dock width at every size. Resize the viewport to
    // produce the requested list width, rather than faking the navigation size.
    await page.locator('.project-page').evaluate(el => (el as HTMLElement).style.setProperty('--panel-user-w', '560px'))
  }
  await page.evaluate(() => document.fonts.ready)
}

async function settle(page: Page) {
  // ResizeObserver delivers after animation frames; sample after its render.
  await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
}

async function fitRow(page: Page, width: number) {
  for (let attempt = 0; attempt < 6; attempt++) {
    await settle(page)
    const actual = (await navigation(page).boundingBox())!.width
    if (Math.abs(actual - width) < 0.5) break
    await page.setViewportSize({ width: Math.round(page.viewportSize()!.width + width - actual), height: 1000 })
  }
  await settle(page)
  expect((await navigation(page).boundingBox())!.width).toBeCloseTo(width, 0)
}

async function geometry(page: Page) {
  return navigation(page).evaluate(row => {
    const rect = (el: Element) => {
      const r = el.getBoundingClientRect()
      return { x: r.x, y: r.y, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
    }
    const visible = (el: Element) => { const r = rect(el); return r.width > 0 && r.height > 0 && getComputedStyle(el).visibility !== 'hidden' }
    const intersects = (a: ReturnType<typeof rect>, b: ReturnType<typeof rect>) => a.x < b.right && a.right > b.x && a.y < b.bottom && a.bottom > b.y
    const group = row.querySelector('.view-settings')!
    const controls = [...group.querySelectorAll('.closed-switch, .closed-pill, .hide-gear, .display-btn')].filter(visible).map(rect)
    const frame = rect(row)
    const switchLabel = group.querySelector('.closed-switch')!
    const track = switchLabel.querySelector('input')!
    const trackBox = rect(track)
    const labelBox = rect(switchLabel.querySelector('.hide-label-slot')!)
    const pseudo = getComputedStyle(track, '::after')
    const transform = pseudo.transform === 'none' ? new DOMMatrix() : new DOMMatrix(pseudo.transform)
    const knobWidth = parseFloat(pseudo.width), knobHeight = parseFloat(pseudo.height)
    const left = pseudo.left === 'auto' ? trackBox.width - parseFloat(pseudo.right) - knobWidth : parseFloat(pseudo.left)
    const knob = { x: trackBox.x + left + transform.m41, y: trackBox.y + parseFloat(pseudo.top) + transform.m42, width: knobWidth, height: knobHeight }
    const switchShown = visible(track)
    const pill = group.querySelector('.closed-pill')!
    const renderedStep = switchShown ? visible(group.querySelector('.display-label')!) ? 0 : 1
      : visible(pill) ? visible(pill.querySelector('.hide-label-slot')!) ? 2 : 3 : 4
    const children = [...row.children].filter(visible).map(rect)
    return {
      step: Number(group.getAttribute('data-navigation-step')),
      renderedStep,
      controls: controls.length,
      controlOverlaps: controls.flatMap((a, i) => controls.slice(i + 1).filter(b => intersects(a, b))),
      outside: controls.filter(r => r.x < frame.x || r.right > frame.right),
      wrapped: children.some(r => Math.abs((r.y + r.height / 2) - (children[0]!.y + children[0]!.height / 2)) > 1),
      trackLabelIntersect: switchShown && intersects(trackBox, labelBox),
      gap: switchShown ? labelBox.x - trackBox.right : null,
      knobInside: !switchShown || (Number.isFinite(knob.x) && knob.width > 0 && knob.height > 0 && knob.x >= trackBox.x && knob.x + knob.width <= trackBox.right && knob.y >= trackBox.y && knob.y + knob.height <= trackBox.bottom),
    }
  })
}

async function assertGeometry(page: Page) {
  const result = await geometry(page)
  expect(result.renderedStep, 'rendered navigation form matches the measured step').toBe(result.step)
  expect(result.controls).toBeGreaterThan(0)
  expect(result.controlOverlaps).toEqual([])
  expect(result.outside).toEqual([])
  expect(result.wrapped).toBe(false)
  expect(result.trackLabelIntersect).toBe(false)
  expect(result.knobInside).toBe(true)
  if (result.gap !== null) expect(result.gap).toBe(6)
  return result
}

async function stableToggle(page: Page) {
  const form = await step(page)
  if (form === 4) await settings(page).locator('.display-btn').click()
  const toggle = form === 4 ? page.locator('.menu-switch input') : form < 2 ? settings(page).locator('.closed-switch input') : settings(page).locator('.closed-pill')
  const controls: Record<string, Locator> = { sections: navigation(page).locator('.sections'), attention: navigation(page).locator('.attention-view-link'), display: settings(page).locator('.display-btn'), toggle }
  // Sample every currently visible row action, including the clicked label,
  // the gear and saved-view links, while ignoring inert/overflowed content.
  const actions = navigation(page).locator('button, a, .closed-switch, .hide-control')
  for (let i = 0; i < await actions.count(); i++) if (await actions.nth(i).isVisible()) {
    await actions.nth(i).evaluate((el, index) => el.setAttribute('data-aeon1559-probe', String(index)), i)
    controls[`row-${i}`] = navigation(page).locator(`[data-aeon1559-probe="${i}"]`)
  }
  const boxes = async () => Object.fromEntries(await Promise.all(Object.entries(controls).map(async ([name, el]) => [name, await el.boundingBox()])))
  const before = await boxes()
  await expectStableControls({ controls, scrollAreas: { row: navigation(page) }, interactions: [false, true].map(hidden => ({ name: hidden ? 'hide closed' : 'show closed', run: async () => {
    await toggle.click()
    if (form < 2 || form === 4) await expect(toggle).toBeChecked({ checked: hidden })
    else await expect(toggle).toHaveAttribute('aria-pressed', String(hidden))
    await expect(page).toHaveURL(hidden ? /^(?!.*[?&]closed=1)/ : /[?&]closed=1/)
    await expect(settings(page)).toHaveAttribute('data-navigation-step', String(form))
    await assertGeometry(page)
    expect(await boxes(), 'every row control moves exactly 0 px').toEqual(before)
  } })) })
  if (form === 4) await page.keyboard.press('Escape')
}

// Keep each geometry scenario in its own timeout budget. The full matrix and
// every control sample still run on slower CI Chromium workers.
for (const theme of ['light', 'dark']) for (const pane of [false, true]) for (const width of [1440, 1280, 1100, 1000, 900, 760]) {
  test(`AEON-1559 navigation settings fit ${width}px ${pane ? 'docked' : 'undocked'} in ${theme}`, async ({ page }, testInfo) => {
    await setup(page, theme, pane)
    await fitRow(page, width)
    const result = await assertGeometry(page)
    if (width === 1000) expect(result.step).toBeGreaterThanOrEqual(1)
    if (width === 1000) expect(result.step).toBeLessThanOrEqual(2)
    if (width === 760) expect(result.step).toBeGreaterThanOrEqual(3)
    await stableToggle(page)
    if ([1440, 1000, 760].includes(width)) await page.screenshot({ path: testInfo.outputPath(`aeon1559-${width}-${pane ? 'open' : 'closed'}-${theme}.png`) })
    const json = JSON.stringify({ width, pane, theme, ...result, movementPx: 0 }, null, 2)
    await writeFile(testInfo.outputPath(`aeon1559-checks-${theme}.json`), json)
    await testInfo.attach(`aeon1559-checks-${theme}`, { body: json, contentType: 'application/json' })
    // The guarded remote runner returns Markdown as well as PNG artifacts.
    await writeFile(testInfo.outputPath(`aeon1559-checks-${theme}.md`), `\`\`\`json\n${json}\n\`\`\`\n`)
  })
}

for (const [index, threshold] of [1100, 960, 820, 720].entries()) {
  test(`AEON-1559 forms ${index} and ${index + 1} have stable toggles and 16 px resize hysteresis`, async ({ page }) => {
    await setup(page)
    // Each threshold has a 16px band [threshold, threshold + 16). Neither
    // approach changes form within it: ±8px around its centre cannot flap.
    await fitRow(page, threshold + 40)
    await expect(settings(page)).toHaveAttribute('data-navigation-step', String(index))
    for (const width of [threshold + 16, threshold + 8, threshold, threshold + 8]) {
      await fitRow(page, width)
      expect(await step(page)).toBe(index)
    }
    await fitRow(page, threshold - 1)
    expect(await step(page)).toBe(index + 1)
    for (const width of [threshold, threshold + 8, threshold + 15]) {
      await fitRow(page, width)
      expect(await step(page)).toBe(index + 1)
    }
    await stableToggle(page)
    await fitRow(page, threshold + 16)
    expect(await step(page)).toBe(index)
  })
}

test('AEON-1559 geometry rejects the old teleported toolbar form', async ({ page }) => {
  // Negative control: remove the scoped navigation fix from the real
  // teleported DOM. The old toolbar/viewport rules must fail this same probe.
  await setup(page, 'light', true)
  await fitRow(page, 760)
  await assertGeometry(page)
  await settings(page).evaluate(el => el.classList.remove('navigation-settings'))
  try { await expect(assertGeometry(page)).rejects.toThrow(/rendered navigation form/) }
  finally { await settings(page).evaluate(el => el.classList.add('navigation-settings')) }
  await assertGeometry(page)
})

test('AEON-1559 non-teleported toolbar retains its 800 px container swap', async ({ page }) => {
  await setup(page)
  await page.evaluate(async () => {
    const path = '/src/components/work/ListToolbar.vue', filtersPath = '/src/lib/ticketList.ts'
    const compiled = await (await fetch(path)).text()
    const vuePath = /from ["']([^"']*\/vue\.js[^"']*)["']/.exec(compiled)![1]!
    const { createApp, h } = await import(vuePath)
    const { default: Toolbar } = await import(path)
    const { filtersFromQuery } = await import(filtersPath)
    const host = document.createElement('div')
    host.id = 'toolbar-regression'
    host.style.cssText = 'position:fixed;top:500px;left:0;width:820px;container:toolbar / inline-size'
    document.body.append(host)
    const root = document.querySelector('#app') as Element & { __vue_app__: { config: { globalProperties: { $pinia: unknown } } } }
    const app = createApp({ render: () => h(Toolbar, { filters: filtersFromQuery({}), options: () => [], label: () => '', total: 0, loading: false, density: 'compact', stuck: false, view: 'list' }) })
    app.use(root.__vue_app__.config.globalProperties.$pinia)
    app.mount(host)
  })
  const toolbar = page.locator('#toolbar-regression .toolbar')
  await expect(toolbar.locator('.view-settings')).toBeVisible()
  await expect(toolbar.locator('.view-settings')).not.toHaveClass(/navigation-settings/)
  for (const [width, pill] of [[820, false], [799, true], [820, false]] as const) {
    await toolbar.locator('..').evaluate((el, width) => { (el as HTMLElement).style.width = `${width}px` }, width)
    await settle(page)
    await expect(toolbar.locator('.closed-pill')).toBeVisible({ visible: pill })
    await expect(toolbar.locator('.closed-switch')).toBeVisible({ visible: !pill })
  }
})

for (const theme of ['light', 'dark']) test(`AEON-1559 phone and standard viewport evidence in ${theme}`, async ({ page }, testInfo) => {
  await setup(page, theme)
  for (const width of [1440, 1024, 390]) {
    await page.setViewportSize({ width, height: 1000 })
    await settle(page)
    await expect(settings(page)).toBeVisible()
    const overflow = await navigation(page).evaluate(el => ({ extra: el.scrollWidth - el.clientWidth, width: el.clientWidth, children: [...el.children].map(child => ({ name: child.className, x: child.getBoundingClientRect().x - el.getBoundingClientRect().x, width: child.getBoundingClientRect().width, scroll: child.scrollWidth, css: getComputedStyle(child).cssText })) }))
    expect(overflow.extra, JSON.stringify(overflow)).toBeLessThanOrEqual(1)
    if (width === 390) {
      expect(await step(page)).toBe(4)
      await settings(page).locator('.display-btn').click()
      await expect(page.locator('.menu-switch input')).toBeVisible()
      await page.keyboard.press('Escape')
    }
    await page.screenshot({ path: testInfo.outputPath(`aeon1559-viewport-${width}-${theme}.png`) })
  }
})

}
