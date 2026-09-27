// SPDX-License-Identifier: AGPL-3.0-only
// AEON-227: add existing tickets from Plan, and add a selection to a new release
// from the ticket list. The membership API is RM1's; these specs mock that contract.
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test, type Page, type Route } from '@playwright/test'
import { fixtures, mockWork, watchErrors, type Call } from './work-fixtures'
import { journeyWorld, mockJourney, PROJECT, type JourneyWorld } from './journey-fixtures'

const shots = resolve('..', '.agent-shots')
type Option = {
  ticket_node_id: string; key: string; title: string; status: string; type: string
  feature_node_id: string | null; release_node_id: string | null; release_title: string | null
  availability: 'addable' | 'included' | 'closed' | 'released' | 'other_release'
}
const OPTIONS: Option[] = [
  { ticket_node_id: 'n-21', key: 'PHAROS-21', title: 'Publish the status page', status: 'backlog', type: 'ticket', feature_node_id: 'n-epic', release_node_id: null, release_title: null, availability: 'addable' },
  { ticket_node_id: 'n-22', key: 'PHAROS-22', title: 'Page the on-call rotation', status: 'new', type: 'task', feature_node_id: 'n-epic', release_node_id: null, release_title: null, availability: 'addable' },
  { ticket_node_id: 'n-23', key: 'PHAROS-23', title: 'Write the deploy runbook', status: 'backlog', type: 'ticket', feature_node_id: null, release_node_id: null, release_title: null, availability: 'addable' },
  { ticket_node_id: 'n-5', key: 'PHAROS-15', title: 'Beacon health probes', status: 'done', type: 'ticket', feature_node_id: 'n-epic', release_node_id: 'r-1', release_title: '260901120000.0.0', availability: 'released' },
  { ticket_node_id: 'n-6', key: 'PHAROS-16', title: 'Retire the old dashboard', status: 'cancelled', type: 'ticket', feature_node_id: null, release_node_id: null, release_title: null, availability: 'closed' },
  { ticket_node_id: 'n-24', key: 'PHAROS-24', title: 'Move the billing epic', status: 'backlog', type: 'ticket', feature_node_id: 'n-epic-2', release_node_id: 'r-9', release_title: 'Release 9', availability: 'other_release' },
]
const KNOWN: Record<string, { key: string; title: string; feature: string | null }> = {
  'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', feature: 'n-epic' },
  'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector', feature: 'n-epic' },
  'n-3': { key: 'PHAROS-13', title: 'Run the disposable Hetzner end-to-end check', feature: 'n-epic' },
  'n-4': { key: 'PHAROS-14', title: 'Visual acceptance of the version pill', feature: null },
  'n-21': { key: 'PHAROS-21', title: 'Publish the status page', feature: 'n-epic' },
}

function catalog(id: string): Option {
  return OPTIONS.find(option => option.ticket_node_id === id) ?? {
    ticket_node_id: id, key: KNOWN[id]?.key ?? id, title: KNOWN[id]?.title ?? id, status: 'backlog', type: 'ticket',
    feature_node_id: KNOWN[id]?.feature ?? null, release_node_id: null, release_title: null, availability: 'addable',
  }
}

async function install(page: Page, world: JourneyWorld) {
  const calls: Call[] = []
  await page.route('**/api/**', async (route: Route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    let body: Record<string, unknown> = {}
    try { body = request.postDataJSON() ?? {} } catch { body = {} }
    const record = () => calls.push({ path, method, query: url.searchParams, body, headers: request.headers() })
    const options = path.match(/^\/api\/projects\/([^/]+)\/releases\/([^/]+)\/ticket-options$/)
    if (options && method === 'GET') {
      record()
      const q = (url.searchParams.get('q') ?? '').toLowerCase()
      const status = url.searchParams.get('status') ?? ''
      const epic = url.searchParams.get('epic') ?? ''
      const type = url.searchParams.get('type') ?? ''
      const releaseId = options[2]
      const walker = world.walkers[releaseId]
      const tickets = OPTIONS.filter(option => {
        if (q && !`${option.key} ${option.title}`.toLowerCase().includes(q)) return false
        if (status && option.status !== status) return false
        if (epic && option.feature_node_id !== epic) return false
        if (type && option.type !== type) return false
        return true
      })
      return route.fulfill({ json: { expected_revision: walker?.revision ?? 1, tickets } })
    }
    const membership = path.match(/^\/api\/projects\/([^/]+)\/releases\/([^/]+)\/membership$/)
    if (membership && method === 'POST') {
      record()
      const releaseId = membership[2]
      const walker = world.walkers[releaseId]
      if (!walker || walker.state !== 'planning') return route.fulfill({ status: 409, json: { error: 'only a planning release can take tickets' } })
      if (body.expected_revision !== walker.revision) return route.fulfill({ status: 409, json: { error: 'release revision changed' } })
      const ids = body.ticket_node_ids as string[]
      const blocked = ids.map(catalog).filter(option => option.availability === 'closed' || option.availability === 'released')
      if (blocked.length) return route.fulfill({ status: 409, json: { error: 'closed or released tickets cannot be added' } })
      const moving = ids.map(catalog).filter(option => option.availability === 'other_release')
      if (moving.length && body.confirm_move !== true) return route.fulfill({ status: 409, json: { error: 'ticket belongs to another open release', code: 'other_release' } })
      for (const id of ids) {
        const option = catalog(id)
        const found = walker.tickets.find(ticket => ticket.ticket_node_id === id)
        if (found) found.included = true
        else walker.tickets.push({ ticket_node_id: id, key: option.key, title: option.title, feature_node_id: option.feature_node_id, included: true, position: walker.tickets.length, estimated_hours: null, screen_node_ids: [] })
      }
      walker.revision += 1
      return route.fulfill({ json: { walker, event_id: 81 } })
    }
    if (path === `/api/projects/${PROJECT}/journey/actions` && method === 'POST' && body.action === 'plan_next_release') {
      record()
      if (body.expected_revision !== world.journey.revision) return route.fulfill({ status: 409, json: { error: 'journey revision is stale' } })
      world.releases.push({ id: 'r-3', key: 'PHAROS-40', kind_id: 'k-release', title: 'Release 3', body: '', fields: {}, state: 'backlog', parent_id: PROJECT, position: '2', created_at: new Date().toISOString(), updated_at: new Date().toISOString(), deleted_at: null })
      world.walkers['r-3'] = { release_node_id: 'r-3', project_node_id: PROJECT, state: 'planning', revision: 1, features: world.walkers['r-2']?.features ?? [], tickets: [] }
      world.journey.current_release_id = 'r-3'
      world.journey.stage = 'plan'
      world.journey.revision += 1
      world.journey.next_action = { key: 'start_build', label: 'Start build', stage: 'plan', available: false, reason: 'Build start needs an approved gate.', approval_request_id: null }
      world.journey.stages = world.journey.stages.map(stage => ({ ...stage, state: stage.key === 'plan' ? 'current' : ['inspire', 'shape', 'requirements'].includes(stage.key) ? 'done' : 'later' }))
      return route.fulfill({ json: world.journey })
    }
    return route.fallback()
  })
  return calls
}

test.beforeEach(async ({ page }) => { await page.clock.setSystemTime(new Date('2026-09-23T12:00:00Z')) })

test('Plan adds three existing tickets in one go and Start build counts them', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('plan')
  await mockJourney(page, world)
  const calls = await install(page, world)
  await page.goto('/p/PHAROS/journey')
  await page.getByRole('button', { name: 'Add existing' }).click()
  const dialog = page.getByRole('dialog', { name: 'Add existing tickets' })
  const options = dialog.getByRole('listbox', { name: 'Existing tickets' }).getByRole('option')
  const search = dialog.getByLabel('Find a ticket')
  await expect(search).toBeFocused()
  await expect(dialog.getByText('Publish the status page')).toBeVisible()
  await search.press('ArrowDown')
  await search.press('Enter')
  await expect(dialog.getByRole('checkbox', { name: 'Select PHAROS-22' })).toBeChecked()
  await search.press('Enter')
  await expect(dialog.getByRole('checkbox', { name: 'Select PHAROS-22' })).not.toBeChecked()
  await expect(dialog.getByRole('checkbox', { name: 'Select PHAROS-15' })).toBeDisabled()
  await expect(dialog.getByText('Already released')).toBeVisible()
  await expect(dialog.getByRole('checkbox', { name: 'Select PHAROS-16' })).toBeDisabled()
  await expect(dialog.getByText('Closed')).toBeVisible()
  await search.fill('page')
  await expect(options).toHaveCount(2)
  await search.fill('')
  await expect(options).toHaveCount(OPTIONS.length)
  await dialog.getByLabel('Status').selectOption('backlog')
  await expect(options).toHaveCount(3)
  await dialog.getByLabel('Type').selectOption('ticket')
  await expect(dialog.getByText('Page the on-call rotation')).toHaveCount(0)
  await dialog.getByLabel('Status').selectOption('')
  await dialog.getByLabel('Type').selectOption('')
  for (const key of ['PHAROS-21', 'PHAROS-22', 'PHAROS-23']) {
    await dialog.getByRole('checkbox', { name: `Select ${key}` }).check()
  }
  await dialog.getByRole('button', { name: 'Add 3 tickets' }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Added 3 tickets to release 2.' })).toBeVisible()
  await expect(page.locator('.j-count')).toContainText('5 in release')
  for (const key of ['PHAROS-21', 'PHAROS-22', 'PHAROS-23']) {
    await expect(page.getByRole('checkbox', { name: `${key} in the release` })).toBeChecked()
  }
  await expect(page.getByRole('region', { name: 'Decision: Release 2' }).locator('.j-stat').first()).toContainText('5')
  const write = calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))
  expect(write).toHaveLength(1)
  expect(write[0].body).toMatchObject({ expected_revision: 7, confirm_move: false, ticket_node_ids: ['n-21', 'n-22', 'n-23'] })
  expect(errors).toEqual([])
})

test('a ticket in another open release is moved only after confirmation', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('plan')
  await mockJourney(page, world)
  const calls = await install(page, world)
  await page.goto('/p/PHAROS/journey')
  await page.getByRole('button', { name: 'Add existing' }).click()
  const dialog = page.getByRole('dialog', { name: 'Add existing tickets' })
  await dialog.getByRole('checkbox', { name: 'Select PHAROS-24' }).check()
  await dialog.getByRole('button', { name: 'Add 1 ticket' }).click()
  const confirm = page.getByRole('dialog', { name: 'Move PHAROS-24?' })
  await expect(confirm).toContainText('Release 9')
  await confirm.getByRole('button', { name: 'Leave them' }).click()
  expect(calls.filter(call => call.path.endsWith('/membership'))).toHaveLength(0)
  await dialog.getByRole('button', { name: 'Add 1 ticket' }).click()
  await confirm.getByRole('button', { name: 'Move into Release 2' }).click()
  await expect(page.getByRole('checkbox', { name: 'PHAROS-24 in the release' })).toBeChecked()
  const write = calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))
  expect(write).toHaveLength(1)
  expect(write[0].body).toMatchObject({ confirm_move: true, ticket_node_ids: ['n-24'] })
})

test('five selected tickets open a new release and land in its plan', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  const data = fixtures()
  data.nodes.push({ id: 'n-21', key: 'PHAROS-21', kind_slug: 'ticket', title: 'Publish the status page', body: '', state: 'backlog', fields: {}, parent_id: 'p-pharos', project: 'p-pharos', created_at: data.nodes[0].created_at, updated_at: data.nodes[0].updated_at })
  await mockWork(page, data)
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world)
  await page.goto('/p/PHAROS')
  const row = (key: string) => page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
  for (const key of ['PHAROS-11', 'PHAROS-12', 'PHAROS-13', 'PHAROS-14', 'PHAROS-21']) {
    await row(key).hover()
    await row(key).getByRole('checkbox', { name: `Select ${key}` }).check()
  }
  await page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' }).click()
  const menu = page.getByRole('dialog', { name: 'Release for 5 tickets' })
  await expect(menu.getByRole('option', { name: /Release 3/ })).toBeEnabled()
  await menu.getByRole('option', { name: /Release 3/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Added 5 tickets to Release 3' })).toBeVisible()
  const action = calls.filter(call => call.path.endsWith('/journey/actions'))
  expect(action).toHaveLength(1)
  expect(action[0].body).toMatchObject({ action: 'plan_next_release' })
  const write = calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))
  expect(write).toHaveLength(1)
  expect(write[0].path).toContain('/releases/r-3/membership')
  expect(write[0].body).toMatchObject({ expected_revision: 1, confirm_move: false, ticket_node_ids: ['n-1', 'n-2', 'n-3', 'n-4', 'n-21'] })
  await page.goto('/p/PHAROS/journey')
  await expect(page.getByRole('region', { name: 'Decision: Release 3' }).locator('.j-stat').first()).toContainText('5')
  for (const key of ['PHAROS-11', 'PHAROS-12', 'PHAROS-13', 'PHAROS-14', 'PHAROS-21']) {
    await expect(page.getByRole('checkbox', { name: `${key} in the release` })).toBeChecked()
  }
  expect(errors).toEqual([])
})

test('the ticket panel release field offers the same new release', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('live')
  await mockJourney(page, world)
  await install(page, world)
  await page.goto('/p/PHAROS/PHAROS-12')
  const panel = page.getByRole('complementary', { name: 'Ticket details' })
  await panel.getByRole('button', { name: 'Release: none. Change release' }).click()
  await expect(page.getByRole('dialog', { name: 'Release for PHAROS-12' }).getByRole('option', { name: /Release 3/ })).toBeVisible()
})

async function shot(page: Page, name: string, theme: 'light' | 'dark', width: number) {
  mkdirSync(shots, { recursive: true })
  await page.emulateMedia({ colorScheme: theme })
  await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
  await page.evaluate(value => { document.documentElement.dataset.theme = value }, theme)
  await page.evaluate(() => document.fonts.ready)
  await page.screenshot({ path: resolve(shots, `${name}-${width}-${theme}.png`), animations: 'disabled' })
}

for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
  test(`screenshot plan picker ${width} ${theme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.addInitScript(value => { document.documentElement.dataset.theme = value }, theme)
    await mockWork(page, fixtures())
    const world = journeyWorld('plan')
    await mockJourney(page, world)
    await install(page, world)
    await page.goto('/p/PHAROS/journey')
    await page.getByRole('button', { name: 'Add existing' }).click()
    await expect(page.getByRole('dialog', { name: 'Add existing tickets' }).getByText('Publish the status page')).toBeVisible()
    await shot(page, 'rpu1-plan-picker', theme, width)
  })
  test(`screenshot release picker ${width} ${theme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.addInitScript(value => { document.documentElement.dataset.theme = value }, theme)
    await mockWork(page, fixtures())
    const world = journeyWorld('live')
    await mockJourney(page, world)
    await install(page, world)
    await page.goto('/p/PHAROS/PHAROS-11')
    await page.getByRole('complementary', { name: 'Ticket details' }).getByRole('button', { name: /Release:/ }).click()
    await expect(page.getByRole('option', { name: /Release 3/ })).toBeVisible()
    await shot(page, 'rpu1-release-field', theme, width)
  })
}
