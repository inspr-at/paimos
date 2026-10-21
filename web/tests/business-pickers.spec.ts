// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1417: incomplete reads stay visible and recover without reloading.
import { writeFile } from 'node:fs/promises'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { crmData, mockCRM } from './crm-fixtures'
import { mockQuotes, quoteWorld } from './quote-list-fixtures'
import { controlStability } from './control-stability'

test.use({ timezoneId: 'Europe/Vienna' })

async function setup(page: Page) {
  const work = fixtures(), crm = crmData(), business = businessData({ enabled: ['business_costs', 'business_crm', 'business_quotes', 'business_hours'] })
  const quotes = quoteWorld()
  business.units[0]!.title = 'Entwicklung und langfristige technische Betreuung'
  await mockWork(page, work)
  await mockCRM(page, crm)
  await mockBusiness(page, business)
  await mockQuotes(page, quotes)
  return { work, crm, business, quotes }
}

test('AEON-1417: rejected rates remain in the hours picker and retry keeps controls still', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  await setup(page)
  let rejected = true
  const reads: string[] = []
  await page.route('**/api/cost-units/*/rates', async route => {
    const id = route.request().url().split('/').at(-2)!
    reads.push(id)
    if (id === 'cu-dev' && rejected) return route.fulfill({ status: 503, json: { error: 'rate read failed' } })
    return route.fallback()
  })
  const checks: object[] = []
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
    rejected = true
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/business/hours')
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    if (width === 390) await page.getByRole('button', { name: 'Log time', exact: true }).click()
    const form = page.getByRole('form', { name: 'Log time' })
    const cost = form.getByRole('combobox', { name: 'Cost unit' })
    await expect(form.getByRole('status').filter({ hasText: 'Some rates could not be loaded' })).toBeVisible()
    const failed = cost.locator('option[value="cu-dev"]')
    await expect(failed).toHaveText('Entwicklung und langfristige technische Betreuung — rates unavailable')
    await expect(failed).toHaveJSProperty('disabled', true)
    const chosenDay = form.getByRole('radio').first()
    const stability = await controlStability(page, {
      cost, days: form.getByRole('radiogroup', { name: 'Day' }), chosenDay,
      ticket: form.getByRole('button', { name: /^Ticket:/ }), log: form.getByRole('button', { name: 'Log', exact: true }),
    })
    await stability.check(() => cost.selectOption('cu-design'))
    await stability.check(() => chosenDay.click())
    await page.screenshot({ path: testInfo.outputPath(`hours-${width}-${theme}.png`), fullPage: true })
    const beforeRetry = reads.length
    rejected = false
    await stability.check(async () => {
      await form.getByRole('button', { name: 'Retry rates' }).click()
      await expect(form.getByRole('button', { name: 'Retry rates' })).toHaveCount(0)
      await expect(failed).toHaveJSProperty('disabled', false)
    })
    expect(reads.slice(beforeRetry)).toEqual(['cu-dev'])
    stability.done()
    checks.push({ view: 'hours', width, theme, partialVisible: true, retryRecovered: true, controlsStable: true })

    rejected = true
    await page.goto('/business/rates')
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await expect(page.getByRole('status').filter({ hasText: 'Some rates could not be loaded' })).toBeVisible()
    await expect(page.getByText('Rates could not be loaded. Retry below.')).toBeVisible()
    const ratesStability = await controlStability(page, {
      search: page.getByRole('searchbox', { name: 'Find a cost unit' }),
      create: page.getByRole('button', { name: 'New cost unit', exact: true }),
    })
    await page.screenshot({ path: testInfo.outputPath(`rates-${width}-${theme}.png`), fullPage: true })
    rejected = false
    await ratesStability.check(async () => {
      await page.getByRole('button', { name: 'Retry rates' }).click()
      await expect(page.getByRole('button', { name: 'Retry rates' })).toHaveCount(0)
    })
    ratesStability.done()
    checks.push({ view: 'rates', width, theme, partialVisible: true, retryRecovered: true, controlsStable: true })
  }
  await writeFile(testInfo.outputPath('checks.json'), JSON.stringify(checks, null, 2))
  await writeFile(testInfo.outputPath('checks.md'), JSON.stringify(checks, null, 2))
})

test('AEON-1417: partial customer quote contact and project lists show retry beside retained results', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const { work, crm, quotes } = await setup(page)
  let partial = true
  await page.route('**/api/crm/organisations?*', route => {
    const offset = new URL(route.request().url()).searchParams.get('offset')
    return route.fulfill({ json: { items: offset === '0' ? crm.customers.filter(c => !c.deleted) : [], next_offset: partial ? 100 : null } })
  })
  await page.route('**/api/quotes?*', route => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor')
    return route.fulfill({ json: cursor ? [] : quotes.rows, headers: partial ? { 'X-Next-Cursor': 'still-more' } : {} })
  })
  await page.route('**/api/nodes?*', async route => {
    const params = new URL(route.request().url()).searchParams
    if (!['contact', 'project'].includes(params.get('kind') ?? '')) return route.fallback()
    const items = params.get('kind') === 'contact'
      ? crm.contacts.filter(c => !c.deleted).map(c => ({ id: c.id, title: c.name, fields: { email: c.email, role: c.role } }))
      : work.projects.map(p => ({ ...p, fields: {}, body: '', updated_at: '2026-09-23T12:00:00Z' }))
    return route.fulfill({ json: { items: params.has('cursor') ? [] : items, next_cursor: partial ? 'still-more' : null } })
  })
  const checks: object[] = []
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width, height: 1000 })
    for (const [view, path, searchName, warning] of [
      ['customers', '/business/customers', 'Find a customer', 'Only part of the customer list'],
      ['quotes', '/business/quotes', 'Find a quote', 'Only part of the quote list'],
      ['projects', '/', 'Filter projects', 'Only part of the project details'],
    ]) {
      partial = true
      await page.goto(path!)
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
      await expect(page.getByRole('status').filter({ hasText: warning! })).toBeVisible()
      if (view === 'customers') await expect(page.getByRole('status').filter({ hasText: 'Contact search is incomplete' })).toBeVisible()
      const search = page.getByRole('searchbox', { name: searchName!, exact: true })
      const create = page.getByRole('button', { name: view === 'customers' ? 'New customer' : view === 'quotes' ? 'New quote' : 'New project', exact: true })
      const stable = await controlStability(page, { search, create })
      await page.screenshot({ path: testInfo.outputPath(`${view}-${width}-${theme}.png`), fullPage: true })
      if (view === 'quotes') {
        await create.click()
        const dialog = page.getByRole('dialog', { name: 'New quote', exact: true })
        await expect(dialog.getByRole('status').filter({ hasText: 'Only part of the customer list' })).toBeVisible()
        await expect(dialog.getByRole('button', { name: 'Create quote', exact: true })).toBeEnabled()
        const dialogStable = await controlStability(page, {
          ...(width === 390 ? { frame: dialog } : {}),
          customer: dialog.getByRole('combobox', { name: 'Customer', exact: true }),
          title: dialog.getByRole('textbox', { name: 'Title', exact: true }),
          create: dialog.getByRole('button', { name: 'Create quote', exact: true }),
        })
        await page.screenshot({ path: testInfo.outputPath(`quote-picker-${width}-${theme}.png`), fullPage: true })
        partial = false
        await dialogStable.check(async () => {
          await dialog.getByRole('button', { name: 'Retry customers' }).click()
          await expect(dialog.getByRole('button', { name: 'Retry customers' })).toHaveCount(0)
        })
        dialogStable.done()
        checks.push({ view: 'quote-picker', width, theme, partialVisible: true, retryRecovered: true, controlsStable: true })
        await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
      }
      partial = false
      await stable.check(async () => {
        await page.locator('.load-status').getByRole('button', { name: 'Try again', exact: true }).click()
        await expect(page.getByRole('status').filter({ hasText: warning! })).toHaveCount(0)
      })
      stable.done()
      checks.push({ view, width, theme, partialVisible: true, retryRecovered: true, controlsStable: true })
    }
  }
  await writeFile(testInfo.outputPath('checks.json'), JSON.stringify(checks, null, 2))
  await writeFile(testInfo.outputPath('checks.md'), JSON.stringify(checks, null, 2))
})

test('AEON-1417: 200 cost units keep rate request concurrency at six', async ({ page }) => {
  const { business } = await setup(page)
  await page.addInitScript(() => {
    const original = window.fetch.bind(window)
    const reads = { active: 0, peak: 0, total: 0, completed: 0 }
    Object.assign(window, { aeon1417RateReads: reads })
    window.fetch = async (input, init) => {
      const path = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
      if (!/\/api\/cost-units\/[^/]+\/rates$/.test(new URL(path, location.href).pathname)) return original(input, init)
      reads.active++; reads.total++; reads.peak = Math.max(reads.peak, reads.active)
      try { return await original(input, init) }
      finally { reads.active--; reads.completed++ }
    }
  })
  const units = Array.from({ length: 200 }, (_, i) => ({ ...business.units[0], id: `cu-${i}`, key: `CU-${i + 1}`, title: `Cost unit ${i + 1}` }))
  await page.route('**/api/nodes?*', route => new URL(route.request().url()).searchParams.get('kind') === 'cost_unit'
    ? route.fulfill({ json: { items: units, next_cursor: null } }) : route.fallback())
  let release!: () => void, entered!: () => void, requests = 0
  const barrier = new Promise<void>(resolve => { release = resolve })
  const sixEntered = new Promise<void>(resolve => { entered = resolve })
  await page.route('**/api/cost-units/*/rates', async route => {
    if (++requests === 6) entered()
    await barrier
    const id = route.request().url().split('/').at(-2)!
    await route.fulfill({ json: [{ ...business.rates[0], id: `rate-${id}`, cost_unit_node_id: id }] })
  })
  await page.goto('/business/hours')
  try {
    await sixEntered
    const blocked = await page.evaluate(() => (window as unknown as { aeon1417RateReads: { active: number; total: number } }).aeon1417RateReads)
    expect(blocked.active).toBe(6)
    expect(blocked.total).toBe(6)
  } finally { release() }
  await expect.poll(() => page.evaluate(() => (window as unknown as { aeon1417RateReads: { completed: number } }).aeon1417RateReads.completed), { timeout: 20_000 }).toBe(200)
  const completed = await page.evaluate(() => (window as unknown as { aeon1417RateReads: { active: number; peak: number; total: number } }).aeon1417RateReads)
  expect(completed).toMatchObject({ active: 0, peak: 6, total: 200 })
})

test('AEON-1417: continuation on the last allowed page reports partial for every business directory', async ({ page }) => {
  await setup(page)
  await page.goto('/business/hours')
  await expect(page.getByRole('table', { name: 'Hours per ticket and day' })).toBeVisible()
  await page.evaluate(async () => {
    const { useBusiness } = await import('/src/stores/business.ts' /* @vite-ignore */)
    const { useProjects } = await import('/src/stores/projects.ts' /* @vite-ignore */)
    await Promise.all([useBusiness().loadCostUnits(), useProjects().load()])
  })
  let continuation = true
  const pages = new Map<string, number>()
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url()), path = url.pathname
    const kind = url.searchParams.get('kind') ?? ''
    const key = path === '/api/crm/organisations' ? 'customers' : path === '/api/quotes' ? 'quotes' : path === '/api/nodes' && ['contact', 'cost_unit', 'project'].includes(kind) ? kind : ''
    if (!key) return route.fallback()
    const page = (pages.get(key) ?? 0) + 1; pages.set(key, page)
    const cap = key === 'customers' || key === 'quotes' ? 50 : 20
    const more = page < cap || continuation
    if (key === 'customers') return route.fulfill({ json: { items: [{ id: `customer-${page}` }], next_offset: more ? page * 100 : null } })
    if (key === 'quotes') return route.fulfill({ json: [{ quote_node_id: `quote-${page}` }], headers: more ? { 'X-Next-Cursor': `quote-${page}` } : {} })
    return route.fulfill({ json: { items: [{ id: `${key}-${page}`, title: `Record ${page}`, fields: {} }], next_cursor: more ? `${key}-${page}` : null } })
  })
  for (const more of [true, false]) {
    continuation = more; pages.clear()
    const results = await page.evaluate(async () => {
      const crm = await import('/src/lib/crm.ts' /* @vite-ignore */)
      const quotes = await import('/src/lib/quotes/list.ts' /* @vite-ignore */)
      const api = await import('/src/lib/api.ts' /* @vite-ignore */)
      const [customers, offers, contacts, units, projects] = await Promise.all([
        crm.listCustomers(), quotes.fetchQuotes(), crm.contactDirectory(),
        api.listNodePages({ kind: ['cost_unit'], limit: 200 }), api.listNodePages({ kind: ['project'], limit: 500 }),
      ])
      return [
        { key: 'customers', partial: customers.partial, rows: customers.items.length },
        { key: 'quotes', partial: offers.partial, rows: offers.items.length },
        { key: 'contact', partial: contacts.partial, rows: contacts.items.size },
        { key: 'cost_unit', partial: units.partial, rows: units.items.length },
        { key: 'project', partial: projects.partial, rows: projects.items.length },
      ]
    })
    for (const result of results) {
      const cap = result.key === 'customers' || result.key === 'quotes' ? 50 : 20
      expect(result.partial, result.key).toBe(more)
      expect(result.rows, result.key).toBe(cap)
      expect(pages.get(result.key), result.key).toBe(cap)
    }
  }
})
