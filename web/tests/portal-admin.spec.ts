// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page, type Route } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

// PORTAL_SHOTS names a directory. Unset, this file creates nothing at import.
const shots = process.env.PORTAL_SHOTS ?? ''

async function capture(page: Page, name: string) {
  if (!shots) return
  mkdirSync(shots, { recursive: true })
  await page.screenshot({ path: `${shots}/${name}`, fullPage: true })
}

interface PortalNode {
  id: string
  key: string
  kind_id: string
  kind_slug: string
  kind_label: string
  title: string
  body: string
  fields: Record<string, string>
  state: string
  parent_id: string | null
  position: string
  created_at: string
  updated_at: string
  deleted_at: null
  priority: null
  assignee: null
  parent: null
  children_count: number
  project: null
}

const kinds = [
  { id: 'k-portal_product', slug: 'portal_product', label: 'Portal product', short_prefix: 'PPR', icon: 'box', allowed_child_kinds: null, field_schema: {} },
  { id: 'k-portal_feature', slug: 'portal_feature', label: 'Portal feature', short_prefix: 'PCF', icon: 'layers', allowed_child_kinds: null, field_schema: {} },
  { id: 'k-portal_wish', slug: 'portal_wish', label: 'Portal wish', short_prefix: 'PWS', icon: 'star', allowed_child_kinds: null, field_schema: {} },
]

function stamp() {
  return '2026-09-29T00:00:00Z'
}

async function install(page: Page) {
  const settings = { enabled: false, slug: 'inspr' }
  const nodes: PortalNode[] = []
  let next = 1
  const market = {
    competitors: [] as { id: string; name: string; published: boolean }[],
    aspects: [] as { id: string; label: string }[],
    cells: [] as { id: string; aspect_id: string; competitor_id: string; stance: string; quote: string; source_url: string; retrieved_on: string; approved: boolean; stale: boolean; recheck: boolean }[],
    history: [] as { cell_id: string; stance: string; quote: string; source_url: string; retrieved_on: string; approved: boolean; at: string }[],
    corrections: [] as { id: string; competitor: string; aspect: string; statement: string }[],
  }
  const pace: {
    project_id?: string
    project_title?: string
    revision?: number
    release_history?: boolean
    releases_30d?: number
    median_release_gap_days?: number
    fulfillments: { wish_id: string; feature_id: string }[]
  } = { fulfillments: [], release_history: false }
  let historyWrites = 0
  const add = (partial: Pick<PortalNode, 'kind_id' | 'kind_slug' | 'kind_label' | 'title' | 'body' | 'state' | 'parent_id'> & { fields?: Record<string, string> }) => {
    const kind = kinds.find(item => item.id === partial.kind_id)!
    const node: PortalNode = {
      id: `pn-${next}`,
      key: `${kind.short_prefix}-${next}`,
      kind_id: partial.kind_id,
      kind_slug: partial.kind_slug,
      kind_label: partial.kind_label,
      title: partial.title,
      body: partial.body,
      fields: partial.fields ?? {},
      state: partial.state,
      parent_id: partial.parent_id,
      position: String(next),
      created_at: stamp(),
      updated_at: stamp(),
      deleted_at: null,
      priority: null,
      assignee: null,
      parent: null,
      children_count: 0,
      project: null,
    }
    next += 1
    nodes.push(node)
    return node
  }
  await page.route('**/api/**', async (route: Route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    const method = request.method()
    if (path === '/api/portal/settings' && method === 'GET') {
      await route.fulfill({ json: settings })
      return
    }
    if (path === '/api/portal/settings' && method === 'PATCH') {
      settings.enabled = Boolean((request.postDataJSON() as { enabled?: boolean }).enabled)
      await route.fulfill({ json: settings })
      return
    }
    if (path === '/api/kinds' && method === 'GET') {
      await route.fulfill({ json: { items: kinds } })
      return
    }
    if (path === '/api/nodes' && method === 'GET') {
      const kind = url.searchParams.get('kind') ?? ''
      if (!kind.startsWith('portal_')) {
        await route.fallback()
        return
      }
      const parent = url.searchParams.get('parent_id')
      const items = nodes.filter(node => node.kind_slug === kind && (!parent || node.parent_id === parent))
      await route.fulfill({ json: { items, next_cursor: null } })
      return
    }
    if (path === '/api/nodes' && method === 'POST') {
      const input = request.postDataJSON() as { kind_id?: string; title?: string; body?: string; state?: string; parent_id?: string | null; fields?: Record<string, string> }
      const kind = kinds.find(item => item.id === input.kind_id)
      if (!kind) {
        await route.fallback()
        return
      }
      const created = add({
        kind_id: kind.id,
        kind_slug: kind.slug,
        kind_label: kind.label,
        title: input.title ?? '',
        body: input.body ?? '',
        state: input.state ?? 'open',
        parent_id: input.parent_id ?? null,
        fields: input.fields ?? {},
      })
      if (kind.slug === 'portal_product') {
        add({ kind_id: 'k-portal_wish', kind_slug: 'portal_wish', kind_label: 'Portal wish', title: 'Quiet wish', body: 'A bell before opening.', state: 'pending', parent_id: created.id })
        add({ kind_id: 'k-portal_wish', kind_slug: 'portal_wish', kind_label: 'Portal wish', title: 'Noisy wish', body: 'Leave this one out.', state: 'pending', parent_id: created.id })
      }
      await route.fulfill({ status: 201, json: created })
      return
    }
    const wishAction = /^\/api\/portal\/wishes\/([^/]+)\/(publish|reject|hide)$/.exec(path)
    if (wishAction && method === 'POST') {
      const node = nodes.find(item => item.id === wishAction[1])
      if (!node) {
        await route.fulfill({ status: 404, json: { error: 'not found' } })
        return
      }
      node.state = wishAction[2] === 'publish' ? 'published' : wishAction[2] === 'hide' ? 'hidden' : 'rejected'
      node.updated_at = stamp()
      await route.fulfill({ json: { id: node.id, title: node.title, summary: node.body, state: node.state, fields: {} } })
      return
    }
    const productEdit = /^\/api\/portal\/products\/([^/]+)$/.exec(path)
    if (productEdit && method === 'PATCH') {
      const node = nodes.find(item => item.id === productEdit[1])
      if (!node) {
        await route.fulfill({ status: 404, json: { error: 'not found' } })
        return
      }
      const patch = request.postDataJSON() as { title?: string; summary?: string; published?: boolean }
      if (patch.title !== undefined) node.title = patch.title
      if (patch.summary !== undefined) node.body = patch.summary
      if (patch.published !== undefined) node.state = patch.published ? 'published' : 'unpublished'
      node.updated_at = stamp()
      await route.fulfill({ json: { id: node.id, title: node.title, summary: node.body, state: node.state, fields: {} } })
      return
    }
    const featureEdit = /^\/api\/portal\/features\/([^/]+)$/.exec(path)
    if (featureEdit && method === 'PATCH') {
      const node = nodes.find(item => item.id === featureEdit[1])
      if (!node) {
        await route.fulfill({ status: 404, json: { error: 'not found' } })
        return
      }
      const patch = request.postDataJSON() as { title?: string; summary?: string; status?: string; legal_basis?: string; decline_reason?: string; live_since?: string }
      if (patch.title !== undefined) node.title = patch.title
      if (patch.summary !== undefined) node.body = patch.summary
      if (patch.status !== undefined) node.state = patch.status
      node.fields = {
        ...(patch.legal_basis ? { legal_basis: patch.legal_basis } : {}),
        ...(patch.decline_reason ? { decline_reason: patch.decline_reason } : {}),
        ...(patch.live_since ? { live_since: patch.live_since } : {}),
      }
      node.updated_at = stamp()
      await route.fulfill({ json: { id: node.id, title: node.title, summary: node.body, state: node.state, fields: node.fields } })
      return
    }
    if (path === '/api/portal/market' && method === 'GET') {
      await route.fulfill({ json: market })
      return
    }
    if (path === '/api/portal/competitors' && method === 'POST') {
      const input = request.postDataJSON() as { name?: string }
      const item = { id: `cmp-${next}`, name: input.name ?? '', published: false }
      next += 1
      market.competitors.push(item)
      await route.fulfill({ json: item })
      return
    }
    const competitorPatch = /^\/api\/portal\/competitors\/([^/]+)$/.exec(path)
    if (competitorPatch && method === 'PATCH') {
      const item = market.competitors.find(row => row.id === competitorPatch[1])
      if (!item) {
        await route.fulfill({ status: 404, json: { error: 'not found' } })
        return
      }
      const patch = request.postDataJSON() as { name?: string; published?: boolean }
      if (patch.name !== undefined) item.name = patch.name
      if (patch.published !== undefined) item.published = patch.published
      await route.fulfill({ json: item })
      return
    }
    if (path === '/api/portal/aspects' && method === 'POST') {
      const input = request.postDataJSON() as { label?: string }
      const item = { id: `asp-${next}`, label: input.label ?? '' }
      next += 1
      market.aspects.push(item)
      await route.fulfill({ json: item })
      return
    }
    if (path === '/api/portal/cells' && method === 'PUT') {
      const input = request.postDataJSON() as { aspect_id: string; competitor_id: string; stance: string; quote?: string; source_url?: string; retrieved_on?: string }
      let cell = market.cells.find(row => row.aspect_id === input.aspect_id && row.competitor_id === input.competitor_id)
      if (!cell) {
        cell = { id: `cell-${next}`, aspect_id: input.aspect_id, competitor_id: input.competitor_id, stance: 'unknown', quote: '', source_url: '', retrieved_on: '', approved: false, stale: false, recheck: false }
        next += 1
        market.cells.push(cell)
      }
      cell.stance = input.stance
      cell.quote = input.quote ?? ''
      cell.source_url = input.source_url ?? ''
      cell.retrieved_on = input.retrieved_on ?? ''
      cell.approved = false
      market.history.unshift({ cell_id: cell.id, stance: cell.stance, quote: cell.quote, source_url: cell.source_url, retrieved_on: cell.retrieved_on, approved: false, at: stamp() })
      await route.fulfill({ json: cell })
      return
    }
    const approve = /^\/api\/portal\/cells\/([^/]+)\/approve$/.exec(path)
    if (approve && method === 'POST') {
      const cell = market.cells.find(row => row.id === approve[1])
      if (!cell) {
        await route.fulfill({ status: 404, json: { error: 'not found' } })
        return
      }
      cell.approved = true
      market.history.unshift({ cell_id: cell.id, stance: cell.stance, quote: cell.quote, source_url: cell.source_url, retrieved_on: cell.retrieved_on, approved: true, at: stamp() })
      await route.fulfill({ json: cell })
      return
    }
    if (path === '/api/portal/pace' && method === 'GET') {
      await route.fulfill({ json: pace })
      return
    }
    if (path === '/api/portal/pace' && method === 'PUT') {
      const input = request.postDataJSON() as { project_id?: string | null; release_history?: boolean; revision?: number }
      if (typeof input.release_history === 'boolean') {
        historyWrites += 1
        const linked = typeof pace.project_id === 'string' && typeof pace.revision === 'number'
        if (!linked || input.project_id !== pace.project_id || input.revision !== pace.revision) {
          await route.fulfill({ status: 409, json: { error: 'The linked project changed, so release history was not changed.' } })
          return
        }
        pace.release_history = input.release_history
        pace.revision += 1
        await route.fulfill({ json: pace })
        return
      }
      if ('project_id' in input) {
        if (!input.project_id) {
          pace.project_id = undefined
          pace.project_title = undefined
          pace.releases_30d = undefined
          pace.median_release_gap_days = undefined
          pace.release_history = false
          pace.revision = undefined
        } else {
          const changed = pace.project_id !== input.project_id
          pace.project_id = input.project_id
          pace.project_title = input.project_id === 'p-pharos' ? 'Pharos' : 'Project'
          pace.releases_30d = 4
          pace.median_release_gap_days = 21
          if (changed) pace.release_history = false
          pace.revision = (pace.revision ?? 0) + 1
        }
      }
      await route.fulfill({ json: pace })
      return
    }
    const fulfillment = /^\/api\/portal\/wishes\/([^/]+)\/fulfillment$/.exec(path)
    if (fulfillment && method === 'PUT') {
      const input = request.postDataJSON() as { feature_id?: string | null }
      pace.fulfillments = pace.fulfillments.filter(row => row.wish_id !== fulfillment[1])
      if (input.feature_id) pace.fulfillments.push({ wish_id: fulfillment[1], feature_id: input.feature_id })
      await route.fulfill({ json: pace })
      return
    }
    const nodePath = /^\/api\/nodes\/([^/]+)$/.exec(path)
    if (nodePath && method === 'PATCH') {
      const node = nodes.find(item => item.id === nodePath[1])
      if (!node) {
        await route.fallback()
        return
      }
      const patch = request.postDataJSON() as { title?: string; body?: string; state?: string; fields?: Record<string, string> }
      if (patch.title !== undefined) node.title = patch.title
      if (patch.body !== undefined) node.body = patch.body
      if (patch.state !== undefined) node.state = patch.state
      if (patch.fields !== undefined) node.fields = patch.fields
      node.updated_at = stamp()
      await route.fulfill({ json: node })
      return
    }
    await route.fallback()
  })
  return {
    retarget(projectId: string, title: string) {
      pace.project_id = projectId
      pace.project_title = title
      pace.release_history = false
      pace.releases_30d = 2
      pace.median_release_gap_days = 11
      pace.revision = (pace.revision ?? 0) + 1
    },
    historyWrites: () => historyWrites,
  }
}

async function expectFits(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
}

for (const width of [1600, 390]) {
  test(`portal settings publish, moderate and hide at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
    await mockWork(page, fixtures())
    await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
    await mockSettings(page, settingsData())
    await install(page)
    await page.goto('/settings/portal')
    await expect(page.getByRole('heading', { name: 'Product portal' })).toBeVisible()
    await expect(page.getByText('The public page is off.')).toBeVisible()
    await expect(page.getByText('No product is published.')).toBeVisible()
    const portalSwitch = page.getByRole('checkbox', { name: /Product portal/ })
    await portalSwitch.check()
    await expect(page.getByRole('link', { name: /\/portal\/inspr$/ })).toBeVisible()
    await page.getByRole('button', { name: 'Copy' }).click()
    await expect(page.getByRole('button', { name: 'Copied' })).toBeVisible()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toMatch(/\/portal\/inspr$/)

    await page.getByRole('button', { name: 'Publish product' }).click()
    await page.getByLabel('Title').fill('Harbour office')
    await page.getByLabel('Summary').fill('Work that is ready before the morning opens.')
    await page.getByRole('button', { name: 'Publish', exact: true }).click()
    await expect(page.getByText('Harbour office', { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Edit' })).not.toHaveClass(/primary/)

    await page.getByRole('button', { name: 'Add a feature' }).click()
    await page.getByLabel('Title').fill('Deadline radar')
    await page.getByLabel('Status').selectOption('live')
    await page.getByLabel('Legal basis').fill('§ 20 WEG')
    await page.getByRole('button', { name: 'Add feature' }).click()
    const radar = page.getByRole('listitem').filter({ hasText: 'Deadline radar' })
    await expect(radar.getByText(/Live since/)).toBeVisible()
    await expect(radar.getByRole('img', { name: /^\d{12}\.0\.0 · 20\d\d-\d\d-\d\d \d\d:\d\d:\d\d UTC$/ })).toBeVisible()

    const quiet = page.getByRole('listitem').filter({ hasText: 'Quiet wish' })
    const noisy = page.getByRole('listitem').filter({ hasText: 'Noisy wish' })
    await quiet.getByRole('button', { name: 'Publish' }).click()
    await expect(quiet.getByRole('button', { name: 'Hide' })).toBeVisible()
    await noisy.getByRole('button', { name: 'Reject' }).click()
    await expect(page.getByText('Noisy wish')).toHaveCount(0)
    await quiet.getByRole('button', { name: 'Hide' }).click()
    await expect(quiet.getByText('Hidden')).toBeVisible()

    await page.getByRole('button', { name: 'Add competitor' }).click()
    await page.getByLabel('Competitor').fill('Northwind')
    await page.getByRole('button', { name: 'Add', exact: true }).click()
    await expect(page.getByText('Northwind', { exact: true })).toBeVisible()
    await page.getByRole('checkbox', { name: 'Public' }).check()
    await page.getByRole('button', { name: 'Add aspect' }).click()
    await page.getByLabel('Aspect').fill('Statutory deadlines')
    await page.getByRole('button', { name: 'Add', exact: true }).click()
    await page.getByLabel('Stance').selectOption('yes')
    await page.getByLabel('Quote').fill('Every deadline is on the public help page.')
    await page.getByLabel('Source page').fill('https://northwind.example/deadlines')
    await page.getByLabel('Read on').fill('2026-09-01')
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    await page.getByRole('button', { name: 'Approve', exact: true }).click()
    await expect(page.getByText('Approved.')).toBeVisible()
    await page.getByLabel('Releases from').selectOption({ label: 'Pharos' })
    const history = page.getByRole('checkbox', { name: 'Publish release history' })
    await expect(history).toBeEnabled()
    await expect(history).not.toBeChecked()
    await history.check()
    await expect(history).toBeChecked()
    await expect(page.getByText('releases in 30 days')).toBeVisible()
    await expect(page.getByText('4', { exact: true }).first()).toBeVisible()
    await expectFits(page)
    await capture(page, `admin-${width}-light.png`)
    await page.getByText('Approved.').scrollIntoViewIfNeeded()
    await capture(page, `admin-comparison-${width}-light.png`)
    await page.emulateMedia({ colorScheme: 'dark' })
    await capture(page, `admin-comparison-${width}-dark.png`)
    await capture(page, `admin-${width}-dark.png`)
  })
}

test('a stale release-history publish reloads the link that is there now', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await mockWork(page, fixtures())
  await mockBusiness(page, businessData({ role: 'admin' }), { role: 'admin' })
  await mockSettings(page, settingsData())
  const portal = await install(page)
  await page.goto('/settings/portal')
  await page.getByRole('checkbox', { name: /Product portal/ }).check()
  await page.getByRole('button', { name: 'Publish product' }).click()
  await page.getByLabel('Title').fill('Harbour office')
  await page.getByLabel('Summary').fill('Work that is ready before the morning opens.')
  await page.getByRole('button', { name: 'Publish', exact: true }).click()
  await expect(page.getByText('Harbour office', { exact: true })).toBeVisible()
  await page.getByLabel('Releases from').selectOption({ label: 'Pharos' })
  const history = page.getByRole('checkbox', { name: 'Publish release history' })
  await expect(history).toBeEnabled()
  await expect(history).not.toBeChecked()
  portal.retarget('p-aeon', 'Aeon')
  const sent = page.waitForRequest(request => request.method() === 'PUT' && request.url().includes('/api/portal/pace') && (request.postData() ?? '').includes('release_history'))
  await history.click()
  expect((await sent).postDataJSON()).toEqual({ release_history: true, project_id: 'p-pharos', revision: 1 })
  await expect(page.getByRole('alert').filter({ hasText: 'The linked project changed, so release history was not changed.' })).toBeVisible()
  await expect(page.getByLabel('Releases from')).toHaveValue('p-aeon')
  await expect(history).toBeEnabled()
  await expect(history).not.toBeChecked()
  expect(portal.historyWrites()).toBe(1)
})
