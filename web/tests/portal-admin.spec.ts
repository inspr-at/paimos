// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect, test, type Page, type Route } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'

const shots = '/private/tmp/claude-501/-Users-markus-Code-aeon/a4527da9-f872-45f5-a2f2-48dde0ce2ce5/scratchpad/shots/aeon-125-slice2'
mkdirSync(shots, { recursive: true })

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
  return settings
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

    await page.getByRole('button', { name: 'Add a feature' }).click()
    await page.getByLabel('Title').fill('Deadline radar')
    await page.getByLabel('Status').selectOption('live')
    await page.getByLabel('Legal basis').fill('§ 20 WEG')
    await page.getByRole('button', { name: 'Add feature' }).click()
    const radar = page.getByRole('listitem').filter({ hasText: 'Deadline radar' })
    await expect(radar.getByText(/Live since/)).toBeVisible()
    await expect(radar.getByRole('img', { name: /\.0\.0$/ })).toBeVisible()

    const quiet = page.getByRole('listitem').filter({ hasText: 'Quiet wish' })
    const noisy = page.getByRole('listitem').filter({ hasText: 'Noisy wish' })
    await quiet.getByRole('button', { name: 'Publish' }).click()
    await expect(quiet.getByRole('button', { name: 'Hide' })).toBeVisible()
    await noisy.getByRole('button', { name: 'Reject' }).click()
    await expect(page.getByText('Noisy wish')).toHaveCount(0)
    await quiet.getByRole('button', { name: 'Hide' }).click()
    await expect(quiet.getByText('Hidden')).toBeVisible()
    await expectFits(page)
    await page.screenshot({ path: `${shots}/admin-${width}-light.png`, fullPage: true })
    await page.emulateMedia({ colorScheme: 'dark' })
    await page.screenshot({ path: `${shots}/admin-${width}-dark.png`, fullPage: true })
  })
}
