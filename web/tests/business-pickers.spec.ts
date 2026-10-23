// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1417: incomplete reads stay visible and recover without reloading.
import { writeFile } from 'node:fs/promises'
import { expect, test, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { crmData, mockCRM } from './crm-fixtures'
import { mockQuotes, quoteWorld } from './quote-list-fixtures'
import { controlStability } from './control-stability'

test.use({ timezoneId: 'Europe/Vienna' })

// One desktop and one phone profile retain both themes and native layout modes.
const renderingProfiles = [{ width: 1440, theme: 'light' }, { width: 390, theme: 'dark' }] as const

function rateBarrier() {
  let release!: () => void, enter!: () => void
  const waiting = new Promise<void>(resolve => { release = resolve })
  const entered = new Promise<void>(resolve => { enter = resolve })
  return { waiting, entered, release, enter }
}

async function controlBoxes(controls: Record<string, Locator>) {
  return Promise.all(Object.values(controls).map(control => control.evaluate(el => {
    const rect = el.getBoundingClientRect()
    let x = rect.x + scrollX, y = rect.y + scrollY
    for (let p = el.parentElement; p; p = p.parentElement) if (p !== document.scrollingElement) { x += p.scrollLeft; y += p.scrollTop }
    return { x, y, width: rect.width, height: rect.height }
  })))
}
function movementPx(before: Awaited<ReturnType<typeof controlBoxes>>, after: Awaited<ReturnType<typeof controlBoxes>>) {
  return Math.max(...before.flatMap((box, i) => (['x', 'y', 'width', 'height'] as const).map(key => Math.abs(box[key] - after[i]![key]))))
}

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
  const { business } = await setup(page)
  let rejected = true
  let held: ReturnType<typeof rateBarrier> | undefined
  let listFailure: 'partial' | 'full' | undefined
  const reads: string[] = []
  await page.route('**/api/nodes?*', route => {
    const params = new URL(route.request().url()).searchParams
    if (!listFailure || params.get('kind') !== 'cost_unit') return route.fallback()
    if (listFailure === 'full') return route.fulfill({ status: 503, json: { error: 'cost unit list unavailable' } })
    return route.fulfill({ json: { items: params.has('cursor') ? [] : business.units, next_cursor: 'still-more' } })
  })
  await page.route('**/api/cost-units/*/rates', async route => {
    const id = route.request().url().split('/').at(-2)!
    reads.push(id)
    if (id === 'cu-dev' && held) { const barrier = held; barrier.enter(); await barrier.waiting }
    if (id === 'cu-dev' && rejected) return route.fulfill({ status: 503, json: { error: 'rate read failed' } })
    return route.fallback()
  })
  const checks: object[] = []
  for (const { width, theme } of renderingProfiles) {
    rejected = true
    held = rateBarrier()
    await page.setViewportSize({ width, height: 1000 })
    await page.goto('/business/hours')
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    if (width === 390) await page.getByRole('button', { name: 'Log time', exact: true }).click()
    const form = page.getByRole('form', { name: 'Log time' })
    const cost = form.getByRole('combobox', { name: 'Cost unit' })
    await held.entered
    const failed = cost.locator('option[value="cu-dev"]')
    await expect(failed).toHaveText('Entwicklung und langfristige technische Betreuung')
    await expect(failed).toHaveJSProperty('disabled', false)
    await expect(page.locator('.load-status')).toHaveCount(0)
    await expect(page.getByText(/could not be loaded|rates unavailable/i)).toHaveCount(0)
    const pendingControls = { cost, days: form.getByRole('radiogroup', { name: 'Day' }), ticket: form.getByRole('button', { name: /^Ticket:/ }), log: form.getByRole('button', { name: 'Log', exact: true }) }
    const pendingStable = await controlStability(page, pendingControls)
    const pendingBefore = await controlBoxes(pendingControls)
    await pendingStable.check(() => cost.selectOption('cu-dev'))
    expect(movementPx(pendingBefore, await controlBoxes(pendingControls))).toBe(0)
    pendingStable.done()
    await page.screenshot({ path: testInfo.outputPath(`hours-pending-${width}-${theme}.png`), fullPage: true })
    held.release(); held = undefined
    await expect(form.getByRole('status').filter({ hasText: 'Some rates could not be loaded' })).toBeVisible()
    await expect(failed).toHaveText('Entwicklung und langfristige technische Betreuung — rates unavailable')
    await expect(failed).toHaveJSProperty('disabled', true)
    await form.getByRole('button', { name: /^Ticket:/ }).click()
    await page.getByRole('combobox', { name: 'Ticket' }).fill('PHAROS-11')
    await page.getByRole('option', { name: /PHAROS-11/ }).click()
    await form.getByRole('button', { name: 'Log', exact: true }).click()
    await expect(form.locator('.log-status')).toHaveText('Rates for this cost unit could not be loaded.')
    await expect(form.getByText('Loading rates…', { exact: true })).toHaveCount(0)
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
    checks.push({ view: 'hours', width, theme, partialVisible: true, retryRecovered: true, controlsStable: true, pendingMovementPx: 0 })

    rejected = true
    held = rateBarrier()
    await page.getByRole('navigation', { name: 'Business', exact: true }).getByRole('link', { name: 'Rates', exact: true }).click()
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await held.entered
    const confirmed = page.getByRole('table', { name: 'Rates of Entwicklung und langfristige technische Betreuung', exact: true })
    await expect(confirmed).toBeVisible()
    await expect(page.locator('.load-status')).toHaveCount(0)
    await expect(page.getByText(/could not be loaded|rates unavailable/i)).toHaveCount(0)
    const refreshControls = { search: page.getByRole('searchbox', { name: 'Find a cost unit' }), create: page.getByRole('button', { name: 'New cost unit', exact: true }), confirmed }
    const refreshStable = await controlStability(page, refreshControls)
    const refreshBefore = await controlBoxes(refreshControls)
    await refreshStable.check(() => refreshControls.search.fill('e'))
    await refreshStable.check(() => refreshControls.search.fill(''))
    expect(movementPx(refreshBefore, await controlBoxes(refreshControls))).toBe(0)
    refreshStable.done()
    await page.screenshot({ path: testInfo.outputPath(`rates-refresh-${width}-${theme}.png`), fullPage: true })
    held.release(); held = undefined
    await expect(page.getByRole('status').filter({ hasText: 'Some rates could not be loaded' })).toBeVisible()
    await expect(page.getByText('Rates could not be loaded.', { exact: true })).toBeVisible()
    await expect(page.getByText('Loading rates…', { exact: true })).toHaveCount(0)
    await expect(page.getByRole('table', { name: 'Rates of Entwicklung und langfristige technische Betreuung', exact: true })).toHaveCount(0)
    await expect(page.getByRole('table', { name: 'Rates of Design', exact: true })).toBeVisible()
    const ratesStability = await controlStability(page, {
      search: page.getByRole('searchbox', { name: 'Find a cost unit' }),
      create: page.getByRole('button', { name: 'New cost unit', exact: true }),
    })
    const filterStability = await controlStability(page, {
      retry: page.getByRole('button', { name: 'Retry rates', exact: true }),
      search: page.getByRole('searchbox', { name: 'Find a cost unit' }),
    })
    await filterStability.check(async () => {
      await page.getByRole('searchbox', { name: 'Find a cost unit' }).fill('Entwicklung')
      await expect(page.getByRole('table', { name: 'Rates of Design', exact: true })).toHaveCount(0)
    })
    await filterStability.check(async () => {
      await page.getByRole('searchbox', { name: 'Find a cost unit' }).fill('')
      await expect(page.getByRole('table', { name: 'Rates of Design', exact: true })).toBeVisible()
    })
    filterStability.done()
    await page.screenshot({ path: testInfo.outputPath(`rates-${width}-${theme}.png`), fullPage: true })
    rejected = false
    await ratesStability.check(async () => {
      await page.getByRole('button', { name: 'Retry rates' }).click()
      await expect(page.getByRole('button', { name: 'Retry rates' })).toHaveCount(0)
    })
    ratesStability.done()
    checks.push({ view: 'rates', width, theme, partialVisible: true, retryRecovered: true, controlsStable: true, refreshMovementPx: 0 })

    // A first Rates load must keep its skeleton until the held rate settles.
    held = rateBarrier()
    await page.reload()
    await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
    await held.entered
    await expect(page.locator('.sk .skeleton')).toHaveCount(4)
    await expect(page.locator('.unit, .load-status')).toHaveCount(0)
    await expect(page.getByText(/could not be loaded|rates unavailable/i)).toHaveCount(0)
    const firstControls = { search: page.getByRole('searchbox', { name: 'Find a cost unit' }), create: page.getByRole('button', { name: 'New cost unit', exact: true }) }
    const firstStable = await controlStability(page, firstControls)
    const firstBefore = await controlBoxes(firstControls)
    await firstStable.check(() => firstControls.search.fill('Entwicklung'))
    expect(movementPx(firstBefore, await controlBoxes(firstControls))).toBe(0)
    firstStable.done()
    await page.screenshot({ path: testInfo.outputPath(`rates-pending-${width}-${theme}.png`), fullPage: true })
    held.release(); held = undefined
    await expect(confirmed).toBeVisible()
    checks.push({ view: 'rates-pending', width, theme, skeletonRetained: true, movementPx: 0 })

    // Without a selection, list failures must describe the list, not a unit or missing rates.
    for (const failure of ['partial', 'full'] as const) {
      listFailure = failure
      await page.goto('/business/hours')
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
      if (width === 390) await page.getByRole('button', { name: 'Log time', exact: true }).click()
      await expect(form.getByRole('button', { name: 'Retry rates' })).toBeEnabled()
      await expect(cost).toHaveValue('')
      await expect(cost.locator('option')).toHaveCount(failure === 'partial' ? 3 : 1)
      await form.getByRole('button', { name: /^Ticket:/ }).click()
      await page.getByRole('combobox', { name: 'Ticket' }).fill('PHAROS-11')
      await page.getByRole('option', { name: /PHAROS-11/ }).click()
      const stable = await controlStability(page, {
        cost, days: form.getByRole('radiogroup', { name: 'Day' }),
        ticket: form.getByRole('button', { name: /^Ticket:/ }), log: form.getByRole('button', { name: 'Log', exact: true }),
      })
      const hint = failure === 'partial' ? 'Some cost units could not be loaded.' : 'Cost units could not be loaded.'
      await stable.check(async () => {
        await form.getByRole('button', { name: 'Log', exact: true }).click()
        await expect(form.locator('.log-status')).toHaveText(hint)
      })
      stable.done()
      await expect(cost).toHaveValue('')
      await page.screenshot({ path: testInfo.outputPath(`hours-no-selection-${failure}-${width}-${theme}.png`), fullPage: true })
      checks.push({ view: `hours-no-selection-${failure}`, width, theme, hint, controlsStable: true })
    }
    listFailure = undefined
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
  for (const { width, theme } of renderingProfiles) {
    await page.setViewportSize({ width, height: 1000 })
    for (const [view, path, searchName, warning] of [
      ['customers', '/business/customers', 'Find a customer', 'Some customers or contacts could not be loaded.'],
      ['quotes', '/business/quotes', 'Find a quote', 'Some quotes could not be loaded.'],
      ['projects', '/', 'Filter projects', 'Some project details could not be loaded.'],
    ]) {
      partial = true
      await page.goto(path!)
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme }, theme)
      const warningText = page.locator('.load-status').getByRole('status')
      await expect(warningText).toHaveText(warning!)
      const retry = page.locator('.load-status').getByRole('button', { name: 'Try again', exact: true })
      const coarse = await page.evaluate(() => matchMedia('(pointer: coarse)').matches)
      expect((await retry.boundingBox())!.height).toBe(coarse ? 44 : 28)
      const textBox = (await warningText.boundingBox())!, retryBox = (await retry.boundingBox())!
      expect(Math.abs(textBox.y + textBox.height / 2 - retryBox.y - retryBox.height / 2)).toBeLessThanOrEqual(.5)
      if (view === 'projects') {
        const rowBox = (await page.locator('.load-status').boundingBox())!
        expect(textBox.x - rowBox.x).toBe(20)
        expect(rowBox.x + rowBox.width - retryBox.x - retryBox.width).toBe(20)
      }
      const search = page.getByRole('searchbox', { name: searchName!, exact: true })
      const create = page.getByRole('button', { name: view === 'customers' ? 'New customer' : view === 'quotes' ? 'New quote' : 'New project', exact: true })
      const stable = await controlStability(page, { search, create })
      await page.screenshot({ path: testInfo.outputPath(`${view}-${width}-${theme}.png`), fullPage: true })
      if (view === 'quotes') {
        await create.click()
        const dialog = page.getByRole('dialog', { name: 'New quote', exact: true })
        const warning = dialog.getByRole('status')
        await expect(warning).toHaveText('Some customers could not be loaded.')
        await expect(dialog.getByRole('button', { name: 'Create quote', exact: true })).toBeEnabled()
        const customerField = dialog.getByRole('combobox', { name: 'Customer', exact: true })
        const titleField = dialog.getByRole('textbox', { name: 'Title', exact: true })
        const customerBox = (await customerField.boundingBox())!, warningBox = (await warning.boundingBox())!, titleBox = (await titleField.boundingBox())!
        expect(warningBox.y).toBeGreaterThanOrEqual(customerBox.y + customerBox.height)
        expect(warningBox.y + warningBox.height).toBeLessThan(titleBox.y)
        expect(await dialog.locator('.create-body .customer-load-status').count()).toBe(1)
        if (width === 390) {
          expect(await dialog.evaluate(el => Math.abs(el.getBoundingClientRect().width - innerWidth))).toBeLessThanOrEqual(.5)
          const surface = await dialog.locator('.create-card').evaluate(el => {
            const style = getComputedStyle(el), probe = document.createElement('div')
            probe.style.backgroundColor = 'var(--surface)'; el.append(probe)
            const solid = getComputedStyle(probe).backgroundColor; probe.remove()
            return { color: style.backgroundColor, image: style.backgroundImage, solid }
          })
          expect(surface.color).toBe(surface.solid)
          expect(surface.color).toMatch(/^rgb\(/)
          expect(surface.image).toBe('none')
          const footer = (await dialog.locator('.create-foot').boundingBox())!
          expect(footer.y + footer.height).toBeGreaterThanOrEqual(1000 - 20)
        }
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
