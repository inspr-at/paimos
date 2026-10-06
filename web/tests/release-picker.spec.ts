// SPDX-License-Identifier: AGPL-3.0-only
// AEON-227: add existing tickets from Plan, and add a selection to a new release
// from the ticket list. The membership API is RM1's; these specs mock that contract.
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, test, type Page, type Route } from '@playwright/test'
import { fixtures, mockWork, watchErrors, type Call, type MockNode } from './work-fixtures'
import { journeyWorld, mockJourney, PROJECT, type JourneyWorld } from './journey-fixtures'
import { expectStableControls } from './helpers/stable'

const shots = resolve('..', '.agent-shots')
type Option = {
  ticket_node_id: string; key: string; title: string; status: string; type: string
  feature_node_id: string | null; release_node_id: string | null; release_title: string | null
  availability: 'addable' | 'included' | 'closed' | 'released' | 'other_release' | 'active_release' | 'unsupported'
}
const OPTIONS: Option[] = [
  { ticket_node_id: 'n-21', key: 'PHAROS-21', title: 'Publish the status page', status: 'backlog', type: 'ticket', feature_node_id: 'n-epic', release_node_id: null, release_title: null, availability: 'addable' },
  { ticket_node_id: 'n-22', key: 'PHAROS-22', title: 'Page the on-call rotation', status: 'new', type: 'ticket', feature_node_id: 'n-epic', release_node_id: null, release_title: null, availability: 'addable' },
  { ticket_node_id: 'n-23', key: 'PHAROS-23', title: 'Write the deploy runbook', status: 'backlog', type: 'ticket', feature_node_id: null, release_node_id: null, release_title: null, availability: 'addable' },
  { ticket_node_id: 'n-19', key: 'PHAROS-19', title: 'Rotate the on-call roster', status: 'backlog', type: 'task', feature_node_id: 'n-epic', release_node_id: null, release_title: null, availability: 'unsupported' },
  { ticket_node_id: 'n-5', key: 'PHAROS-15', title: 'Beacon health probes', status: 'done', type: 'ticket', feature_node_id: 'n-epic', release_node_id: 'r-1', release_title: '260901120000.0.0', availability: 'released' },
  { ticket_node_id: 'n-6', key: 'PHAROS-16', title: 'Retire the old dashboard', status: 'cancelled', type: 'ticket', feature_node_id: null, release_node_id: null, release_title: null, availability: 'closed' },
  { ticket_node_id: 'n-24', key: 'PHAROS-24', title: 'Move the billing epic', status: 'backlog', type: 'ticket', feature_node_id: 'n-epic-2', release_node_id: 'r-9', release_title: 'Release 9', availability: 'other_release' },
  { ticket_node_id: 'n-25', key: 'PHAROS-25', title: 'Keep the building release', status: 'in_progress', type: 'ticket', feature_node_id: null, release_node_id: 'r-4', release_title: 'Release 4', availability: 'active_release' },
]
const KNOWN: Record<string, { key: string; title: string; feature: string | null; type: string; status: string }> = {
  'n-1': { key: 'PHAROS-11', title: 'Connect Hetzner Cloud for managed provisioning', feature: 'n-epic', type: 'ticket', status: 'in-progress' },
  'n-2': { key: 'PHAROS-12', title: 'Add an Oracle Cloud connector', feature: 'n-epic', type: 'ticket', status: 'backlog' },
  // PHAROS-13 is a task in work-fixtures. Membership rejects that kind.
  'n-3': { key: 'PHAROS-13', title: 'Run the disposable Hetzner end-to-end check', feature: 'n-epic', type: 'task', status: 'qa' },
  'n-4': { key: 'PHAROS-14', title: 'Visual acceptance of the version pill', feature: null, type: 'ticket', status: 'new' },
  'n-21': { key: 'PHAROS-21', title: 'Publish the status page', feature: 'n-epic', type: 'ticket', status: 'backlog' },
}
const CLOSED_STATE = new Set(['accepted', 'delivered', 'done', 'cancelled', 'canceled', 'archived', 'closed'])
type NativeMember = { release_node_id: string | null; release_title: string | null; release_state: string | null }

function epicOf(node: MockNode, nodes: MockNode[]): string | null {
  const parent = nodes.find(item => item.id === node.parent_id)
  if (!parent || parent.kind_slug === 'project') return null
  if (parent.kind_slug === 'epic') return parent.id
  const above = nodes.find(item => item.id === parent.parent_id)
  return above?.kind_slug === 'epic' ? above.id : null
}

// A node the fixture already typed wins. Unknown ids are not silently retitled as tickets.
function catalog(id: string, nodes: MockNode[] = []): Option {
  const preset = OPTIONS.find(option => option.ticket_node_id === id)
  const node = nodes.find(item => item.id === id)
  const known = KNOWN[id]
  const type = node?.kind_slug ?? preset?.type ?? known?.type ?? 'unknown'
  const status = node?.state ?? preset?.status ?? known?.status ?? 'backlog'
  const availability = type !== 'ticket' ? 'unsupported' : CLOSED_STATE.has(status) || CLOSED_STATE.has(status.replaceAll('-', '_')) ? 'closed' : preset?.availability ?? 'addable'
  return {
    ticket_node_id: id, key: node?.key ?? preset?.key ?? known?.key ?? id, title: node?.title ?? preset?.title ?? known?.title ?? id, status, type,
    feature_node_id: node ? epicOf(node, nodes) : preset?.feature_node_id ?? known?.feature ?? null,
    release_node_id: preset?.release_node_id ?? null, release_title: preset?.release_title ?? null, availability,
  }
}

function requestHash(body: Record<string, unknown>): string {
  const ids = Array.isArray(body.ticket_node_ids) ? body.ticket_node_ids.map(String).sort() : []
  return JSON.stringify({ action: body.action ?? null, expected_revision: body.expected_revision ?? null, release_id: body.release_id ?? null, ticket_node_ids: ids })
}

async function install(page: Page, world: JourneyWorld, hooks: { rejectCreate?: string; loseCreate?: boolean; createScript?: Array<'lose' | number>; holdCreate?: Promise<void>; nodes?: MockNode[]; native?: Map<string, NativeMember> } = {}) {
  const calls: Call[] = []
  const native = hooks.native ?? new Map<string, NativeMember>()
  // Reads and both writes begin with the same memberships as the seeded walkers.
  for (const walker of Object.values(world.walkers)) {
    const release = world.releases.find(item => item.id === walker.release_node_id)
    for (const ticket of walker.tickets.filter(item => item.included)) {
      native.set(ticket.ticket_node_id, { release_node_id: walker.release_node_id, release_title: release?.title ?? 'Release', release_state: walker.state })
    }
  }
  // These picker-only rows explicitly model other releases outside the current walker.
  for (const option of OPTIONS) {
    if (!option.release_node_id || native.has(option.ticket_node_id)) continue
    native.set(option.ticket_node_id, {
      release_node_id: option.release_node_id, release_title: option.release_title,
      release_state: option.availability === 'released' ? 'released' : option.availability === 'active_release' ? 'building' : 'planning',
    })
  }
  const receipts = new Map<string, string>()
  const nodes = hooks.nodes ?? []
  const lose = { create: hooks.loseCreate === true }
  const script = hooks.createScript ? [...hooks.createScript] : []
  let undoMembership: (() => void) | null = null
  const membershipError = (ids: string[], target: string, confirmMove: boolean): { status: number; error: string } | null => {
    for (const id of ids) {
      const option = catalog(id, nodes)
      if (option.type !== 'ticket') return { status: 404, error: 'ticket not found in project' }
      if (CLOSED_STATE.has(option.status)) return { status: 409, error: 'closed tickets cannot be added' }
      const member = native.get(id)
      if (!member?.release_node_id) continue
      if (member.release_node_id === target) return { status: 409, error: 'ticket is already included' }
      const state = world.walkers[member.release_node_id]?.state ?? member.release_state
      if (state !== 'planning') return { status: 409, error: 'released or active tickets cannot move' }
      if (!confirmMove) return { status: 409, error: 'confirm_move required to move a ticket from another release' }
    }
    return null
  }
  const remember = (ids: string[], releaseId: string, title: string) => {
    for (const id of ids) {
      native.set(id, { release_node_id: releaseId, release_title: title, release_state: 'planning' })
      for (const walker of Object.values(world.walkers)) {
        for (const ticket of walker.tickets.filter(item => item.ticket_node_id === id)) ticket.included = walker.release_node_id === releaseId
      }
    }
  }
  await page.route('**/api/**', async (route: Route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    let body: Record<string, unknown> = {}
    try { body = request.postDataJSON() ?? {} } catch { body = {} }
    const record = () => calls.push({ path, method, query: url.searchParams, body, headers: request.headers() })
    const memberships = path.match(/^\/api\/projects\/([^/]+)\/release-memberships$/)
    if (memberships && method === 'GET') {
      record()
      const ids = url.searchParams.getAll('ticket_node_id')
      if (ids.length < 1 || ids.length > 100) return route.fulfill({ status: 400, json: { error: 'ticket_node_id must be 1..100 unique ids' } })
      return route.fulfill({
        headers: { 'cache-control': 'no-store' },
        json: { tickets: ids.map(id => ({ ticket_node_id: id, ...(native.get(id) ?? { release_node_id: null, release_title: null, release_state: null }) })) },
      })
    }
    if (path === '/api/events/81/undo' && method === 'POST') {
      record()
      undoMembership?.()
      undoMembership = null
      return route.fulfill({ status: 201, json: { id: 82, type: 'journey.release_membership_changed' } })
    }
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
      const rejection = membershipError(ids, releaseId, body.confirm_move === true)
      if (rejection) return route.fulfill({ status: rejection.status, json: { error: rejection.error } })
      const before = new Map(ids.map(id => [id, native.get(id)]))
      const walkersBefore = structuredClone(world.walkers)
      undoMembership = () => {
        for (const [id, member] of before) {
          if (member) native.set(id, member)
          else native.delete(id)
        }
        world.walkers = walkersBefore
      }
      for (const id of ids) {
        const option = catalog(id, nodes)
        const found = walker.tickets.find(ticket => ticket.ticket_node_id === id)
        if (found) found.included = true
        else walker.tickets.push({ ticket_node_id: id, key: option.key, title: option.title, feature_node_id: option.feature_node_id, included: true, position: walker.tickets.length, estimated_hours: null, screen_node_ids: [] })
      }
      walker.revision += 1
      const release = world.releases.find(item => item.id === releaseId)
      remember(ids, releaseId, release?.title ?? 'Release')
      return route.fulfill({ json: { walker, event_id: 81, leaf_node_ids: ids } })
    }
    if (path === `/api/projects/${PROJECT}/journey/actions` && method === 'POST' && (body.action === 'plan_next_release' || body.action === 'open_first_release') && Array.isArray(body.ticket_node_ids)) {
      record()
      const key = String(body.idempotency_key ?? '')
      const hash = requestHash(body)
      const step = script.shift()
      // ensureJourney runs before lookupReceipt. A deleted project is 404, and a
      // denial or throttle can arrive there too. None of those read the receipt,
      // so none of them prove an earlier lost response failed or create another release.
      if (typeof step === 'number') {
        const error = step === 429 ? 'too many requests' : step === 404 ? 'project not found' : step === 401 ? 'sign in required' : 'project access required'
        return route.fulfill({ status: step, json: { error } })
      }
      const known = key ? receipts.get(key) : undefined
      if (known) {
        if (known !== hash) return route.fulfill({ status: 409, json: { error: 'idempotency key was used for a different action' } })
        return route.fulfill({ json: JSON.parse(JSON.stringify(world.journey)) })
      }
      if (body.expected_revision !== world.journey.revision) return route.fulfill({ status: 409, json: { error: 'journey revision is stale' } })
      if (body.action === 'plan_next_release' && body.release_id && body.release_id !== world.journey.current_release_id) return route.fulfill({ status: 409, json: { error: 'release does not match the current release' } })
      if (body.action === 'open_first_release' && body.release_id) return route.fulfill({ status: 409, json: { error: 'release does not match the current release' } })
      if (typeof body.idempotency_key !== 'string' || !body.idempotency_key) return route.fulfill({ status: 400, json: { error: 'invalid idempotency key' } })
      if (hooks.rejectCreate) return route.fulfill({ status: 409, json: { error: hooks.rejectCreate } })
      const ids = body.ticket_node_ids as string[]
      const chosen = ids.map(id => catalog(id, nodes))
      const releaseId = body.action === 'open_first_release' ? 'r-1' : 'r-3'
      // A rejected member rolls the entire action back, including its receipt.
      const rejection = membershipError(ids, releaseId, false)
      if (rejection) return route.fulfill({ status: rejection.status, json: { error: rejection.error } })
      const title = body.action === 'open_first_release' ? 'Release 1' : 'Release 3'
      const created = new Date().toISOString()
      world.releases.push({ id: releaseId, key: 'PHAROS-40', kind_id: 'k-release', title, body: '', fields: {}, state: 'backlog', parent_id: PROJECT, position: '2', created_at: created, updated_at: created, deleted_at: null })
      world.walkers[releaseId] = {
        release_node_id: releaseId, project_node_id: PROJECT, state: 'planning', revision: 2, features: world.walkers['r-2']?.features ?? [],
        tickets: chosen.map((option, position) => ({ ticket_node_id: option.ticket_node_id, key: option.key, title: option.title, feature_node_id: option.feature_node_id, included: true, position, estimated_hours: null, screen_node_ids: [] })),
      }
      world.journey.current_release_id = releaseId
      world.journey.stage = 'plan'
      world.journey.revision += 2
      world.journey.next_action = { key: 'start_build', label: 'Start build', stage: 'plan', available: false, reason: 'Build start needs an approved gate.', approval_request_id: null }
      world.journey.stages = world.journey.stages.map(stage => ({ ...stage, state: stage.key === 'plan' ? 'current' : ['inspire', 'shape', 'requirements'].includes(stage.key) ? 'done' : 'later' }))
      remember(ids, releaseId, title)
      if (key) receipts.set(key, hash)
      if (lose.create || step === 'lose') { lose.create = false; return route.abort('failed') }
      if (hooks.holdCreate) await hooks.holdCreate
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
  await expect(dialog.getByRole('checkbox', { name: 'Select PHAROS-25' })).toBeDisabled()
  await expect(dialog.getByText('In Release 4 · not planning')).toBeVisible()
  await expect(dialog.getByRole('checkbox', { name: 'Select PHAROS-19' })).toBeDisabled()
  await expect(dialog.getByText('Not a release ticket')).toBeVisible()
  await search.fill('page')
  await expect(options).toHaveCount(2)
  await search.fill('')
  await expect(options).toHaveCount(OPTIONS.length)
  await dialog.getByLabel('Status').selectOption('backlog')
  await expect(options).toHaveCount(4)
  await dialog.getByLabel('Type').selectOption('ticket')
  await expect(options).toHaveCount(3)
  await expect(dialog.getByText('Rotate the on-call roster')).toHaveCount(0)
  await dialog.getByLabel('Status').selectOption('')
  await dialog.getByLabel('Type').selectOption('')
  for (const key of ['PHAROS-21', 'PHAROS-22', 'PHAROS-23']) {
    await dialog.getByRole('checkbox', { name: `Select ${key}` }).check()
  }
  await dialog.getByRole('button', { name: 'Add selected' }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Added 3 leaves to release 2.' })).toBeVisible()
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

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`retained existing picker refreshes revision and keeps controls stable ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await page.addInitScript(value => {
      const apply = () => { document.documentElement.dataset.theme = value }
      if (document.documentElement) apply()
      else document.addEventListener('DOMContentLoaded', apply, { once: true })
    }, theme)
    const errors = watchErrors(page)
    await mockWork(page, fixtures())
    const world = journeyWorld('plan')
    await mockJourney(page, world)
    const calls = await install(page, world)
    let release!: () => void, started!: () => void
    const hold = new Promise<void>(resolve => { release = resolve })
    const requested = new Promise<void>(resolve => { started = resolve })
    await page.route('**/releases/r-2/plan', async route => {
      started()
      await hold
      const included = new Set(route.request().postDataJSON().included_ticket_ids)
      const walker = world.walkers['r-2']
      walker.tickets = walker.tickets.map(ticket => ({ ...ticket, included: included.has(ticket.ticket_node_id) }))
      walker.revision = 20
      world.journey.revision++
      await route.fulfill({ json: walker })
    })
    try {
      await page.goto('/p/PHAROS/journey')
      await page.getByRole('checkbox', { name: /^Guarded multi-cloud provisioning: 2 of 3/ }).click()
      await requested
      await page.getByRole('button', { name: 'Add existing', exact: true }).click()
      const picker = page.getByRole('dialog', { name: 'Add existing tickets' })
      const list = picker.getByRole('listbox', { name: 'Existing tickets' })
      const row = list.getByRole('option').filter({ hasText: 'PHAROS-21' })
      const add = picker.getByRole('button', { name: 'Add selected' })
      await expect(row).toBeVisible()
      await expectStableControls({
        controls: { add, cancel: picker.getByRole('button', { name: 'Cancel' }), search: picker.getByLabel('Find a ticket'),
          status: picker.getByLabel('Status', { exact: true }), epic: picker.getByLabel('Epic', { exact: true }),
          type: picker.getByLabel('Type', { exact: true }), filters: picker.locator('.filters'), list, row,
          ...(width === 390 ? { frame: picker } : {}) },
        scrollAreas: { picker, list },
        interactions: [
          { name: 'retain a selection', run: async () => { await row.click(); await expect(row).toHaveAttribute('aria-selected', 'true') } },
          { name: 'refresh the owner and options', run: async () => {
            const refreshed = page.waitForResponse(response => response.url().includes('/ticket-options') && response.request().method() === 'GET')
            release()
            await refreshed
            await expect(row).toHaveAttribute('aria-selected', 'true')
            await expect(add).toBeEnabled()
          } },
        ],
      })
      const evidence = resolve('test-results/aeon-706-planbind')
      mkdirSync(evidence, { recursive: true })
      await page.screenshot({ path: resolve(evidence, `picker-refresh-${width}-${theme}.png`), animations: 'disabled' })
      await add.click()
      await expect(picker).toHaveCount(0)
      expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership')).map(call => call.body)).toEqual([
        { expected_revision: 20, ticket_node_ids: ['n-21'], confirm_move: false },
      ])
      await expect(page.locator('.toast').filter({ hasText: 'Added 1 leaf to release 2.' })).toBeVisible()
      expect(errors).toEqual([])
    } finally { release() }
  })
}

test('existing picker recovers a revision conflict without losing the selection', async ({ page }) => {
  const errors = watchErrors(page)
  await mockWork(page, fixtures())
  const world = journeyWorld('plan')
  await mockJourney(page, world)
  const calls = await install(page, world)
  await page.goto('/p/PHAROS/journey')
  await page.getByRole('button', { name: 'Add existing', exact: true }).click()
  const picker = page.getByRole('dialog', { name: 'Add existing tickets' })
  const selected = picker.getByRole('checkbox', { name: 'Select PHAROS-21' })
  const add = picker.getByRole('button', { name: 'Add selected' })
  await selected.check()
  world.walkers['r-2'].revision = 20
  const refreshed = page.waitForResponse(response => response.url().includes('/ticket-options') && response.request().method() === 'GET')
  await add.click()
  await refreshed
  await expect(picker.getByRole('alert')).toContainText('The release changed')
  await expect(selected).toBeChecked()
  await expect(page.locator('.toast').filter({ hasText: /Added .* to release/ })).toHaveCount(0)
  await add.click()
  await expect(picker).toHaveCount(0)
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership')).map(call => call.body)).toEqual([
    { expected_revision: 7, ticket_node_ids: ['n-21'], confirm_move: false },
    { expected_revision: 20, ticket_node_ids: ['n-21'], confirm_move: false },
  ])
  await expect(page.locator('.toast').filter({ hasText: 'Added 1 leaf to release 2.' })).toBeVisible()
  expect(errors).toEqual([])
})

for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark'] as const) {
  test(`existing parent picker counts leaves ${width} ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.emulateMedia({ colorScheme: theme })
    await page.addInitScript(value => {
      const apply = () => { document.documentElement.dataset.theme = value }
      if (document.documentElement) apply()
      else document.addEventListener('DOMContentLoaded', apply, { once: true })
    }, theme)
    const errors = watchErrors(page)
    await mockWork(page, fixtures())
    const world = journeyWorld('plan')
    await mockJourney(page, world)
    const calls = await install(page, world)
    const parent = { ...OPTIONS[0], ticket_node_id: 'parent', key: 'PHAROS-91', type: 'work', is_parent: true,
      title: 'Wiederkehrende Sicherheitsprüfung und Veröffentlichung der abgestimmten Verbesserungen' }
    const overlap = { ...OPTIONS[1], ticket_node_id: 'leaf-b', type: 'work' }
    await page.route('**/ticket-options*', route => route.fulfill({ json: { expected_revision: 7, tickets: [parent, overlap] } }))
    const writes: Record<string, unknown>[] = []
    await page.route('**/releases/*/membership', async route => {
      const body = route.request().postDataJSON()
      writes.push(body)
      if (theme === 'dark' && !body.confirm_move) return route.fulfill({ status: 409, json: { error: 'confirm_move required' } })
      return route.fulfill({ json: { walker: world.walkers['r-2'], event_id: 81, leaf_node_ids: ['leaf-a', 'leaf-b', 'leaf-c', 'leaf-b'] } })
    })
    await page.goto('/p/PHAROS/journey')
    await page.getByRole('button', { name: 'Add existing' }).click()
    const picker = page.getByRole('dialog', { name: 'Add existing tickets' })
    const list = picker.getByRole('listbox', { name: 'Existing tickets' })
    const parentRow = list.getByRole('option').filter({ hasText: 'PHAROS-91' })
    const leafRow = list.getByRole('option').filter({ hasText: overlap.key })
    const add = picker.getByRole('button', { name: 'Add selected' })
    await expect(parentRow).toBeVisible()
    await expectStableControls({
      controls: { add, cancel: picker.getByRole('button', { name: 'Cancel' }), search: picker.getByLabel('Find a ticket'),
        status: picker.getByLabel('Status', { exact: true }), epic: picker.getByLabel('Epic', { exact: true }),
        type: picker.getByLabel('Type', { exact: true }), list, parentRow, leafRow,
        selectAll: picker.getByRole('button', { name: 'Select all 2' }), ...(width === 390 ? { frame: picker } : {}) },
      scrollAreas: { picker, list },
      interactions: [
        { name: 'select parent', run: async () => { await parentRow.click(); await expect(parentRow).toHaveAttribute('aria-selected', 'true') } },
        { name: 'select overlapping leaf', run: async () => { await leafRow.click(); await expect(leafRow).toHaveAttribute('aria-selected', 'true') } },
        { name: 'remove overlapping leaf', run: async () => { await leafRow.click(); await expect(leafRow).toHaveAttribute('aria-selected', 'false') } },
      ],
    })
    if (width !== 390) await leafRow.click()
    const evidence = resolve('test-results/aeon-653-wn-fix8')
    mkdirSync(evidence, { recursive: true })
    await page.screenshot({ path: resolve(evidence, `existing-parent-picker-${width}-${theme}.png`), animations: 'disabled' })
    await add.click()
    if (theme === 'dark') {
      const confirm = page.getByRole('dialog', { name: 'Move into this release?' })
      await confirm.getByRole('button', { name: 'Move into Release 2' }).click()
    }
    await expect(picker).toHaveCount(0)
    await expect(page.locator('.toast').filter({ hasText: 'Added 3 leaves to release 2.' })).toBeVisible()
    expect(writes).toHaveLength(theme === 'dark' ? 2 : 1)
    expect(writes.at(-1)).toMatchObject({ ticket_node_ids: width === 390 ? ['parent'] : ['parent', 'leaf-b'], confirm_move: theme === 'dark' })
    await page.screenshot({ path: resolve(evidence, `existing-parent-added-${width}-${theme}.png`), animations: 'disabled' })
    expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))).toHaveLength(0)
    expect(errors).toEqual([])
  })
}

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
  await dialog.getByRole('button', { name: 'Add selected' }).click()
  const confirm = page.getByRole('dialog', { name: 'Move PHAROS-24?' })
  await expect(confirm).toContainText('Release 9')
  await confirm.getByRole('button', { name: 'Leave them' }).click()
  expect(calls.filter(call => call.path.endsWith('/membership'))).toHaveLength(0)
  await dialog.getByRole('button', { name: 'Add selected' }).click()
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
  const stamp = data.nodes[0].created_at
  const fresh: MockNode[] = [61, 62, 63, 64, 65].map(number => ({
    id: `n-${number}`, key: `PHAROS-${number}`, kind_slug: 'ticket', title: `Fresh release ticket ${number}`,
    body: '', state: 'backlog', fields: {}, parent_id: 'p-pharos', project: 'p-pharos', created_at: stamp, updated_at: stamp,
  }))
  data.nodes.push(...fresh)
  await mockWork(page, data)
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world, { nodes: data.nodes })
  await page.goto('/p/PHAROS')
  const row = (key: string) => page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
  // All five are real tickets with no membership in the existing released walkers.
  for (const { key } of fresh) {
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
  expect(action[0].body).toMatchObject({
    action: 'plan_next_release', expected_revision: 12, release_id: 'r-2',
    ticket_node_ids: fresh.map(ticket => ticket.id),
  })
  expect(action[0].body.idempotency_key).toEqual(expect.any(String))
  expect(action[0].body).not.toHaveProperty('confirm_move')
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))).toHaveLength(0)
  await page.goto('/p/PHAROS/journey')
  await expect(page.getByRole('region', { name: 'Decision: Release 3' }).locator('.j-stat').first()).toContainText('5')
  for (const { key } of fresh) {
    await expect(page.getByRole('checkbox', { name: `${key} in the release` })).toBeChecked()
  }
  expect(errors).toEqual([])
})

test('a task in the batch rolls the new release back and keeps the tickets out', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  const data = fixtures()
  await mockWork(page, data)
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world, { nodes: data.nodes })
  await page.goto('/p/PHAROS')
  const row = (key: string) => page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
  for (const key of ['PHAROS-12', 'PHAROS-13']) {
    await row(key).hover()
    await row(key).getByRole('checkbox', { name: `Select ${key}` }).check()
  }
  await page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' }).click()
  await page.getByRole('dialog', { name: 'Release for 2 tickets' }).getByRole('option', { name: /Release 3/ }).click()
  const toast = page.locator('.toast').filter({ hasText: 'The new release was not opened' })
  await expect(toast).toContainText('ticket not found in project')
  await expect(page.locator('.toast').filter({ hasText: /^Added / })).toHaveCount(0)
  const action = calls.filter(call => call.path.endsWith('/journey/actions'))
  expect(action).toHaveLength(1)
  expect(action[0].body).toMatchObject({ action: 'plan_next_release', ticket_node_ids: ['n-2', 'n-3'] })
  expect(catalog('n-3', data.nodes).type).toBe('task')
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))).toHaveLength(0)
  expect(world.releases.map(release => release.id)).not.toContain('r-3')
  expect(world.journey.current_release_id).toBe('r-2')
  expect(world.journey.revision).toBe(12)
  expect(world.journey.stage).toBe('live')
  await page.goto('/p/PHAROS/PHAROS-12')
  await expect(page.getByRole('complementary', { name: 'Ticket details' }).getByRole('button', { name: 'Release: none. Change release' })).toBeVisible()
  await page.goto('/p/PHAROS/journey')
  await expect(page.getByRole('region', { name: 'Decision: Release 3' })).toHaveCount(0)
  await expect(page.getByRole('list', { name: 'Releases, newest first' }).getByRole('button', { name: /^Release 3\b/ })).toHaveCount(0)
  expect(errors).toEqual([])
})

test('a rejected new release is not left open', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world, { rejectCreate: 'closed tickets cannot be added' })
  await page.goto('/p/PHAROS')
  const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })
  await row.hover()
  await row.getByRole('checkbox', { name: 'Select PHAROS-12' }).check()
  await page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' }).click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /Release 3/ }).click()
  const toast = page.locator('.toast').filter({ hasText: 'The new release was not opened' })
  await expect(toast).toContainText('closed tickets cannot be added')
  await expect(page.locator('.toast').filter({ hasText: 'is open, but' })).toHaveCount(0)
  await expect(page.locator('.toast').filter({ hasText: /^Added / })).toHaveCount(0)
  const action = calls.filter(call => call.path.endsWith('/journey/actions'))
  expect(action).toHaveLength(1)
  expect(action[0].body).toMatchObject({ action: 'plan_next_release', expected_revision: 12, release_id: 'r-2', ticket_node_ids: ['n-2'] })
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))).toHaveLength(0)
  expect(world.releases.map(release => release.id)).not.toContain('r-3')
  expect(world.journey.current_release_id).toBe('r-2')
  expect(world.journey.stage).toBe('live')
  expect(world.journey.revision).toBe(12)
  await page.goto('/p/PHAROS/journey')
  await expect(page.getByRole('region', { name: 'Next: Release 3' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Decision: Release 3' })).toHaveCount(0)
  await expect(page.getByRole('list', { name: 'Releases, newest first' }).getByRole('button', { name: /^Release 3\b/ })).toHaveCount(0)
  expect(errors).toEqual([])
})

for (const sourceState of ['released', 'building']) for (const target of ['new', 'existing']) {
  test(`${target} release rejects a ${sourceState} member and preserves the whole batch`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    const data = fixtures()
    await mockWork(page, data)
    const world = journeyWorld(target === 'new' ? 'live' : 'plan')
    const original = world.walkers['r-2'].tickets.find(ticket => ticket.ticket_node_id === 'n-1')!
    world.walkers['r-2'].tickets = world.walkers['r-2'].tickets.filter(ticket => ticket.ticket_node_id !== original.ticket_node_id)
    world.releases.push({ ...world.releases[0], id: 'r-held', key: 'PHAROS-900', title: 'Held release' })
    world.walkers['r-held'] = { ...world.walkers['r-2'], release_node_id: 'r-held', state: sourceState, tickets: [original] }
    const before = structuredClone({ journey: world.journey, releases: world.releases, walkers: world.walkers })
    await mockJourney(page, world)
    const calls = await install(page, world, { nodes: data.nodes })
    await page.goto('/p/PHAROS')
    // The unassigned ticket comes first: rejection must still roll back the batch.
    for (const key of ['PHAROS-12', 'PHAROS-11']) {
      const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: new RegExp(`^${key}$`) }) })
      await row.hover()
      await row.getByRole('checkbox', { name: `Select ${key}` }).check()
    }
    await page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' }).click()
    await page.getByRole('dialog', { name: 'Release for 2 tickets' }).getByRole('option', { name: target === 'new' ? /^Release 3/ : /^Release 2/ }).click()
    await expect(page.locator('.toast').filter({ hasText: 'released or active tickets cannot move' })).toBeVisible()
    await expect(page.locator('.toast').filter({ hasText: /^Added / })).toHaveCount(0)
    expect(calls.filter(call => call.method === 'POST' && call.path.endsWith(target === 'new' ? '/journey/actions' : '/membership'))).toHaveLength(1)
    expect({ journey: world.journey, releases: world.releases, walkers: world.walkers }).toEqual(before)
    await page.goto('/p/PHAROS/PHAROS-11')
    await expect(page.getByRole('complementary', { name: 'Ticket details' }).getByRole('button', { name: 'Release: Held release. Change release' })).toBeVisible()
    await page.goto('/p/PHAROS/PHAROS-12')
    await expect(page.getByRole('complementary', { name: 'Ticket details' }).getByRole('button', { name: 'Release: none. Change release' })).toBeVisible()
  })
}

test('the ticket release field follows native membership after reload, reopen and undo', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const data = fixtures()
  data.nodes.find(node => node.id === 'n-2')!.fields.release = { id: 5668, label: 'v4.7.8' }
  await mockWork(page, data)
  const world = journeyWorld('plan')
  await mockJourney(page, world)
  const calls = await install(page, world)
  const panel = page.getByRole('complementary', { name: 'Ticket details' })
  await page.goto('/p/PHAROS/PHAROS-12')
  await expect(panel.getByRole('button', { name: 'Release: none. Change release' })).toBeVisible()
  await expect(panel.getByText('v4.7.8')).toHaveCount(0)
  await page.goto('/p/PHAROS')
  const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })
  await row.hover()
  await row.getByRole('checkbox', { name: 'Select PHAROS-12' }).check()
  await page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' }).click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 2/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Added 1 ticket to Release 2' })).toBeVisible()
  await row.getByText('Add an Oracle Cloud connector').click()
  await expect(panel.getByRole('button', { name: 'Release: Release 2. Change release' })).toBeVisible()
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(panel.getByRole('button', { name: 'Release: none. Change release' })).toBeVisible()
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(panel.getByRole('button', { name: 'Release: Release 2. Change release' })).toBeVisible()
  await page.goto('/p/PHAROS')
  await row.hover()
  await row.getByRole('checkbox', { name: 'Select PHAROS-12' }).check()
  await page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' }).click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 2/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Added 1 ticket to Release 2' })).toBeVisible()
  await page.goto('/p/PHAROS/PHAROS-12')
  await expect(panel.getByRole('button', { name: 'Release: Release 2. Change release' })).toBeVisible()
  await page.goto('/p/PHAROS/PHAROS-12')
  await expect(panel.getByRole('button', { name: 'Release: Release 2. Change release' })).toBeVisible()
  await expect(panel.getByText('v4.7.8')).toHaveCount(0)
  expect(calls.filter(call => call.method === 'PATCH')).toHaveLength(0)
  expect(data.nodes.find(node => node.id === 'n-2')?.fields.release).toMatchObject({ label: 'v4.7.8' })
})

test('a lost new-release response is retried with the same key and no membership write', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world, { loseCreate: true })
  await page.goto('/p/PHAROS')
  const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })
  await row.hover()
  await row.getByRole('checkbox', { name: 'Select PHAROS-12' }).check()
  const add = page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' })
  await add.click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 3/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'not confirmed' })).toBeVisible()
  await expect(page.locator('.toast').filter({ hasText: 'was not opened' })).toHaveCount(0)
  await add.click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 3/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Added 1 ticket to Release 3' })).toBeVisible()
  const action = calls.filter(call => call.path.endsWith('/journey/actions'))
  expect(action).toHaveLength(2)
  expect(action[1].body).toEqual(action[0].body)
  expect(action[0].body.idempotency_key).toEqual(action[1].body.idempotency_key)
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))).toHaveLength(0)
  expect(world.releases.filter(release => release.id === 'r-3')).toHaveLength(1)
})

function advanceCurrentRelease(world: JourneyWorld) {
  const created = new Date().toISOString()
  world.releases.push({ id: 'r-4', key: 'PHAROS-41', kind_id: 'k-release', title: 'Release 4', body: '', fields: {}, state: 'backlog', parent_id: PROJECT, position: '3', created_at: created, updated_at: created, deleted_at: null })
  world.walkers['r-4'] = { release_node_id: 'r-4', project_node_id: PROJECT, state: 'planning', revision: 1, features: [], tickets: [] }
  world.journey.current_release_id = 'r-4'
  world.journey.stage = 'plan'
  world.journey.revision += 2
}

async function storedJourneys(page: Page) {
  return page.evaluate(() => {
    const root = document.querySelector('#app') as { __vue_app__?: { config: { globalProperties: { $pinia?: { state: { value: { journey?: { journeys?: Record<string, { current_release_id?: string | null; project_node_id?: string }> } } } } } } } } | null
    const journeys = root?.__vue_app__?.config.globalProperties.$pinia?.state.value.journey?.journeys ?? {}
    return {
      aeon: journeys['p-aeon']?.project_node_id ?? null,
      pharos: journeys['p-pharos']?.current_release_id ?? null,
    }
  })
}

test('a lost create stays unconfirmed through a denial and replays the same key', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world, { createScript: ['lose', 403] })
  await page.goto('/p/PHAROS')
  const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })
  await row.hover()
  await row.getByRole('checkbox', { name: 'Select PHAROS-12' }).check()
  const add = page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' })
  const openNew = async () => {
    await add.click()
    await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 3/ }).click()
  }
  await openNew()
  await expect(page.locator('.toast').filter({ hasText: 'not confirmed' })).toBeVisible()
  await expect(page.locator('.toast').filter({ hasText: 'was not opened' })).toHaveCount(0)
  await openNew()
  await expect(page.locator('.toast').filter({ hasText: 'was not opened' })).toHaveCount(0)
  await expect(page.locator('.toast').filter({ hasText: 'not confirmed' })).toBeVisible()
  await openNew()
  await expect(page.locator('.toast').filter({ hasText: 'Added 1 ticket to Release 3' })).toBeVisible()
  await expect(page.locator('.toast').filter({ hasText: 'Release 4' })).toHaveCount(0)
  const action = calls.filter(call => call.path.endsWith('/journey/actions'))
  expect(action).toHaveLength(3)
  expect(action[1].body).toEqual(action[0].body)
  expect(action[2].body).toEqual(action[0].body)
  expect(world.releases.filter(release => release.id === 'r-3')).toHaveLength(1)
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))).toHaveLength(0)
})

test('a lost create stays unconfirmed through a deleted project and replays the same key', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world, { createScript: ['lose', 404] })
  await page.goto('/p/PHAROS')
  const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })
  await row.hover()
  await row.getByRole('checkbox', { name: 'Select PHAROS-12' }).check()
  const add = page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' })
  const openNew = async () => {
    await add.click()
    await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 3/ }).click()
  }
  await openNew()
  await expect(page.locator('.toast').filter({ hasText: 'not confirmed' })).toBeVisible()
  await expect(page.locator('.toast').filter({ hasText: 'was not opened' })).toHaveCount(0)
  await openNew()
  await expect(page.locator('.toast').filter({ hasText: 'was not opened' })).toHaveCount(0)
  await expect(page.locator('.toast').filter({ hasText: 'not confirmed' })).toBeVisible()
  await openNew()
  await expect(page.locator('.toast').filter({ hasText: 'Added 1 ticket to Release 3' })).toBeVisible()
  await expect(page.locator('.toast').filter({ hasText: 'Release 4' })).toHaveCount(0)
  const action = calls.filter(call => call.path.endsWith('/journey/actions'))
  expect(action).toHaveLength(3)
  expect(action[1].body).toEqual(action[0].body)
  expect(action[2].body).toEqual(action[0].body)
  expect(world.releases.filter(release => release.id === 'r-3')).toHaveLength(1)
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))).toHaveLength(0)
})

test('a replay after the project moved on names the tickets’ release, not the later one', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world, { loseCreate: true })
  await page.goto('/p/PHAROS')
  const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })
  await row.hover()
  await row.getByRole('checkbox', { name: 'Select PHAROS-12' }).check()
  const add = page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' })
  await add.click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 3/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'not confirmed' })).toBeVisible()
  advanceCurrentRelease(world)
  const before = calls.filter(call => call.path.endsWith('/release-memberships')).length
  await add.click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 4/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'Added 1 ticket to Release 3' })).toBeVisible()
  await expect(page.locator('.toast').filter({ hasText: 'Release 4' })).toHaveCount(0)
  const action = calls.filter(call => call.path.endsWith('/journey/actions'))
  expect(action).toHaveLength(2)
  expect(action[1].body).toEqual(action[0].body)
  expect(world.journey.current_release_id).toBe('r-4')
  expect(world.releases.filter(release => release.id === 'r-3')).toHaveLength(1)
  expect(calls.filter(call => call.path.includes('/releases/r-4/walker'))).toHaveLength(0)
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))).toHaveLength(0)
  const reads = calls.filter(call => call.path.endsWith('/release-memberships'))
  expect(reads.length).toBeGreaterThan(before)
  expect(reads.at(-1)?.query.getAll('ticket_node_id')).toContain('n-2')
})

test('a replay does not invent a count when membership no longer matches', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const native = new Map<string, NativeMember>()
  const calls = await install(page, world, { loseCreate: true, native })
  await page.goto('/p/PHAROS')
  const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })
  await row.hover()
  await row.getByRole('checkbox', { name: 'Select PHAROS-12' }).check()
  const add = page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' })
  await add.click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 3/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'not confirmed' })).toBeVisible()
  native.delete('n-2')
  world.walkers['r-3'].tickets.find(ticket => ticket.ticket_node_id === 'n-2')!.included = false
  advanceCurrentRelease(world)
  await add.click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 4/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'not all in one release' })).toBeVisible()
  await expect(page.locator('.toast').filter({ hasText: /^Added / })).toHaveCount(0)
  await expect(page.locator('.toast').filter({ hasText: 'Release 4' })).toHaveCount(0)
  const action = calls.filter(call => call.path.endsWith('/journey/actions'))
  expect(action).toHaveLength(2)
  expect(action[1].body).toEqual(action[0].body)
  expect(world.releases.filter(release => release.id === 'r-3')).toHaveLength(1)
})

test('parent replay with a backlog leaf does not claim full placement', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const data = fixtures()
  await mockWork(page, data)
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world, { loseCreate: true })
  await page.route('**/api/projects/*/release-memberships*', async route => {
    const ids = new URL(route.request().url()).searchParams.getAll('ticket_node_id')
    await route.fulfill({ json: { tickets: ids.map(id => ({
      ticket_node_id: id, is_parent: id === 'n-2', release_count: 1,
      release_node_id: 'r-3', release_title: 'Release 3', release_state: 'planning',
      leaf_node_ids: id === 'n-2' ? ['assigned', 'backlog'] : [id], assigned_leaf_count: 1,
    })) } })
  })
  await page.goto('/p/PHAROS')
  const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })
  await row.hover()
  await row.getByRole('checkbox', { name: 'Select PHAROS-12' }).check()
  const add = page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' })
  await add.click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 3/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'not confirmed' })).toBeVisible()
  advanceCurrentRelease(world)
  await add.click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 4/ }).click()
  await expect(page.locator('.toast').filter({ hasText: 'not all in one release' })).toBeVisible()
  await expect(page.locator('.toast').filter({ hasText: /^Added / })).toHaveCount(0)
  const actions = calls.filter(call => call.path.endsWith('/journey/actions'))
  expect(actions).toHaveLength(2)
  expect(actions[1].body).toEqual(actions[0].body)
  expect(calls.filter(call => call.method === 'POST' && call.path.endsWith('/membership'))).toHaveLength(0)
})

test('a delayed create on one project does not update the project opened meanwhile', async ({ page }) => {
  let releaseHold!: () => void
  const holdCreate = new Promise<void>(resolve => { releaseHold = resolve })
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world, { holdCreate })
  await page.goto('/p/PHAROS')
  const row = page.locator('tr.ticket-row').filter({ has: page.locator('.key', { hasText: /^PHAROS-12$/ }) })
  await row.hover()
  await row.getByRole('checkbox', { name: 'Select PHAROS-12' }).check()
  await page.getByRole('toolbar', { name: /selected ticket/ }).getByRole('button', { name: 'Add to release' }).click()
  await page.getByRole('dialog', { name: 'Release for 1 ticket' }).getByRole('option', { name: /^Release 3/ }).click()
  await expect.poll(() => calls.filter(call => call.path.endsWith('/journey/actions')).length).toBe(1)
  await page.evaluate(async () => {
    const root = document.querySelector('#app') as { __vue_app__?: { config: { globalProperties: { $router?: { push: (path: string) => Promise<unknown> } } } } } | null
    await root?.__vue_app__?.config.globalProperties.$router?.push('/p/AEON')
  })
  await expect(page.getByText('Aeon foundation')).toBeVisible()
  releaseHold()
  await expect.poll(() => storedJourneys(page)).toEqual({ aeon: null, pharos: 'r-3' })
  await expect(page.locator('.toast').filter({ hasText: 'Added' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Journey:/ })).toHaveCount(0)
  expect(calls.filter(call => call.path.startsWith('/api/projects/p-aeon/release-memberships') && call.query.getAll('ticket_node_id').includes('n-2'))).toHaveLength(0)
})

test('a delayed create from the ticket panel does not update the project opened meanwhile', async ({ page }) => {
  let releaseHold!: () => void
  const holdCreate = new Promise<void>(resolve => { releaseHold = resolve })
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const world = journeyWorld('live')
  await mockJourney(page, world)
  const calls = await install(page, world, { holdCreate })
  await page.goto('/p/PHAROS/PHAROS-12')
  const panel = page.getByRole('complementary', { name: 'Ticket details' })
  await panel.getByRole('button', { name: 'Release: none. Change release' }).click()
  await page.getByRole('dialog', { name: 'Release for PHAROS-12' }).getByRole('option', { name: /^Release 3/ }).click()
  await expect.poll(() => calls.filter(call => call.path.endsWith('/journey/actions')).length).toBe(1)
  await page.evaluate(async () => {
    const root = document.querySelector('#app') as { __vue_app__?: { config: { globalProperties: { $router?: { push: (path: string) => Promise<unknown> } } } } } | null
    await root?.__vue_app__?.config.globalProperties.$router?.push('/p/AEON')
  })
  await expect(page.getByText('Aeon foundation')).toBeVisible()
  releaseHold()
  await expect.poll(() => storedJourneys(page)).toEqual({ aeon: null, pharos: 'r-3' })
  await expect(page.locator('.toast').filter({ hasText: 'Added PHAROS-12' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Journey:/ })).toHaveCount(0)
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
    await page.locator('#existing-options [role="option"]').evaluateAll(nodes => {
      let previous = 0
      for (const node of nodes) {
        const box = node.getBoundingClientRect()
        const mark = node.querySelector('.mark')
        const markBox = mark?.getBoundingClientRect()
        if (box.top < previous - 1) throw new Error(`ticket row overlaps the row above: ${node.textContent}`)
        if (markBox && (markBox.bottom > box.bottom + 4 || markBox.right > box.right + 4)) {
          const title = node.querySelector('.title')?.getBoundingClientRect()
          const copy = node.querySelector('.copy')?.getBoundingClientRect()
          throw new Error(`availability mark overflows by right ${Math.round(markBox.right - box.right)} bottom ${Math.round(markBox.bottom - box.bottom)} title ${Math.round(title?.height ?? 0)} copy ${Math.round(copy?.height ?? 0)} option ${Math.round(box.height)}: ${node.textContent}`)
        }
        previous = box.bottom
      }
    })
  })
  test(`screenshot release picker ${width} ${theme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: theme })
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.addInitScript(value => { document.documentElement.dataset.theme = value }, theme)
    await mockWork(page, fixtures())
    const world = journeyWorld('live')
    await mockJourney(page, world)
    await install(page, world)
    await page.goto('/p/PHAROS/PHAROS-12')
    await page.getByRole('complementary', { name: 'Ticket details' }).getByRole('button', { name: /Release:/ }).click()
    await expect(page.getByRole('option', { name: /Release 3/ })).toBeVisible()
    await shot(page, 'rpu1-release-field', theme, width)
  })
}
