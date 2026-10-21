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
