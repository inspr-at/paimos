// SPDX-License-Identifier: AGPL-3.0-only
// Business › Customers: the list (search, sort, filters, columns, keyboard), a new
// customer, the customer page (edit mode, contacts, notes and the rewrite
// proposal, related work, undo), the quote sender in Settings, and enabling
// Customers and Quotes from Manage parts, with axe in light and dark.
import { test, expect, type Locator, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { HOFER, LUMEN, ORG_SCHEMA, QUOTE_12, crmData, mockCRM, type CRMMockOptions } from './crm-fixtures'

async function setup(page: Page, options: CRMMockOptions = {}) {
  await mockWork(page, fixtures())
  const data = crmData(options)
  const calls = await mockCRM(page, data, options)
  return { data, calls }
}
const table = (page: Page) => page.getByRole('grid', { name: 'Customers' })
const names = (page: Page) => table(page).locator('tbody .name-link')
const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
async function ready(page: Page) {
  await expect(names(page).first()).toBeVisible()
}
async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
  const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')} — ${n.failureSummary?.split('\n').slice(1, 2).join(' ').trim()}`).join('\n')}`)
  expect(summary, summary.join('\n')).toEqual([])
}
// Every line of every dot list starts at the list's clipped edge, so no dot leads a line
// (and none can end one: a dot always sits before its item).
async function dotsNeverLead(page: Page) {
  const off = await page.locator('.dot-list').evaluateAll(lists => lists.flatMap(list => {
    const box = list.getBoundingClientRect()
    if (!box.width) return []
    const edge = box.left + parseFloat(getComputedStyle(list).getPropertyValue('--dot-gap'))
    const items = [...list.children].map(el => el.getBoundingClientRect()).filter(r => r.width > 0)
    return items.filter((r, i) => (i === 0 || r.top > items[i - 1].top + 2) && Math.abs(r.left - edge) > 1).map(() => list.textContent?.trim() ?? '')
  }))
  expect(off).toEqual([])
}
async function noClipping(page: Page, scope: string) {
  const cut = await page.evaluate(selector => [...document.querySelectorAll<HTMLElement>(`${selector} *`)].filter(el => {
    const r = el.getBoundingClientRect(), c = getComputedStyle(el)
    // A dot list reaches into a clipped leading margin by design (its line-start dots hide there).
    if (r.width <= 1 || r.height <= 1 || c.display === 'none' || el.closest('svg') || el.closest('.sr-only') || el.classList.contains('dot-list')) return false
    return r.left < -0.5 || r.right > innerWidth + 0.5 || ((c.overflowX === 'auto' || c.overflowX === 'scroll' || c.overflowX === 'hidden') && el.scrollWidth > el.clientWidth + 1 && c.textOverflow !== 'ellipsis')
  }).map(el => `${el.tagName}.${el.className}`), scope)
  expect(cut, scope).toEqual([])
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
}

test('the list: numbers as the server writes them, search, sort, filters and the keyboard', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await page.goto('/business/customers')
  await ready(page)
  await expect(page.getByRole('heading', { name: 'Customers', level: 1 })).toBeVisible()
  await expect(page.locator('.summary .dot-list > span')).toHaveText(['7 customers', '5 with a number'])
  await expect(names(page)).toHaveText(['Alder & Rowe Architects', 'Atelier Lumen', 'Bäckerei Hofer', 'Café Vogel', 'Grünwerk Energie', 'Klinik Nordstern', 'Studio Meridian'])
  await dotsNeverLead(page)
  const hofer = table(page).getByRole('row').filter({ hasText: 'Bäckerei Hofer' })
  await expect(hofer.locator('.number')).toHaveText('K-0042')
  await expect(hofer).toContainText('Jana Hofer')
  await expect(hofer).toContainText('Graz, Austria')
  await expect(hofer).toContainText('95.00 EUR')
  await expect(table(page).getByRole('row').filter({ hasText: 'Atelier Lumen' })).toContainText('Not yet')

  // Sort by number: numbered customers first, blanks last; a second click reverses.
  await table(page).getByRole('button', { name: 'Number' }).click()
  await expect(names(page).first()).toHaveText('Klinik Nordstern')
  await expect(table(page).getByRole('columnheader', { name: 'Number' })).toHaveAttribute('aria-sort', 'ascending')
  await table(page).getByRole('button', { name: 'Number' }).click()
  await expect(names(page).first()).toHaveText('Bäckerei Hofer')
  await expect(names(page).last()).toHaveText('Grünwerk Energie')

  // / searches names, numbers, places and primary contacts.
  await page.keyboard.press('/')
  await expect(page.getByRole('searchbox', { name: 'Find a customer' })).toBeFocused()
  await page.keyboard.type('graz')
  await expect(names(page)).toHaveText(['Bäckerei Hofer', 'Café Vogel'])
  await expect(page.getByRole('status').filter({ hasText: 'of 7' })).toHaveText('2 of 7')
  await page.getByRole('searchbox', { name: 'Find a customer' }).fill('paul weiss')
  await expect(names(page)).toHaveText(['Klinik Nordstern'])
  await page.getByRole('searchbox', { name: 'Find a customer' }).fill('')

  // Filters: without a number, then a country.
  await page.getByRole('group', { name: 'Customer number' }).getByRole('button', { name: 'Without' }).click()
  await expect(names(page)).toHaveText(['Atelier Lumen', 'Grünwerk Energie'])
  await page.getByRole('button', { name: 'Country' }).click()
  await page.getByRole('group', { name: 'Country' }).getByLabel('Germany').check()
  await page.keyboard.press('Escape')
  await expect(names(page)).toHaveText(['Atelier Lumen'])
  await page.getByRole('button', { name: 'Clear', exact: true }).click()
  await expect(names(page)).toHaveCount(7)

  // j/k move the cursor, Enter opens the customer (back in name order).
  await table(page).getByRole('button', { name: 'Customer' }).click()
  await expect(table(page).getByRole('columnheader', { name: 'Customer' })).toHaveAttribute('aria-sort', 'ascending')
  await page.locator('body').click({ position: { x: 5, y: 300 } })
  await page.keyboard.press('j'); await page.keyboard.press('j'); await page.keyboard.press('j')
  await expect(table(page).locator('tr.cursor')).toContainText('Bäckerei Hofer')
  await page.keyboard.press('k')
  await expect(table(page).locator('tr.cursor')).toContainText('Atelier Lumen')
  await page.keyboard.press('j')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(`/business/customers/${HOFER}`)
  await expect(page.getByRole('heading', { name: 'Bäckerei Hofer', level: 1 })).toBeVisible()
  // Back to the list: the cursor is where it was.
  await page.goBack()
  await expect(table(page).locator('tr.cursor')).toContainText('Bäckerei Hofer')
  expect(errors).toEqual([])
})

test('at 1440 names get the room: the role drops out whole, and cut values show in full on hover', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  await page.goto('/business/customers')
  await ready(page)
  const cell = table(page).getByRole('row').filter({ hasText: 'Bäckerei Hofer' }).locator('td.c-contact .cell')
  // Cut: ellipsised, or dropped to the cell's hidden second line.
  const cut = (selector: string) => cell.locator(selector).evaluate(el => el.scrollWidth > el.clientWidth + 1 || el.getBoundingClientRect().top >= el.parentElement!.getBoundingClientRect().bottom - 1)
  // Wide enough for both: nothing is cut, and Customer is no wider than it needs.
  expect(await cut('.person')).toBe(false)
  expect(await cut('.role')).toBe(false)
  const widths = await table(page).locator('thead th').evaluateAll(ths => ths.map(th => Math.round(th.getBoundingClientRect().width)))
  expect(widths[0]).toBeLessThan(widths[2] + 120)
  // Narrowed from the keyboard: the role leaves whole, the name stays whole.
  const edge = page.getByRole('separator', { name: 'Resize Primary contact column' })
  await edge.focus()
  for (let i = 0; i < 7; i++) await page.keyboard.press('ArrowLeft')
  await expect.poll(() => cut('.role')).toBe(true)
  expect(await cut('.person')).toBe(false)
  expect(await cell.locator('.role').evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true)
  await cell.hover()
  await expect(page.locator('.tooltip')).toHaveText('Jana Hofer · Managing director')
  // A value that fits carries no tooltip.
  await table(page).getByRole('row').filter({ hasText: 'Café Vogel' }).locator('td.c-place .cell').hover()
  await page.waitForTimeout(500)
  await expect(page.locator('.tooltip')).toHaveCount(0)
})

const LONG_NAME = 'Hausverwaltung und Immobilienmakler Purkarthofer GmbH'
// Visible slice of an ellipsized name: on the first line, wider than a sliver, shorter than its text.
async function ellipsized(locator: Locator) {
  return locator.evaluate(el => {
    const box = el.getBoundingClientRect()
    const parent = el.parentElement!.getBoundingClientRect()
    const style = getComputedStyle(el)
    const top = Math.max(box.top, parent.top)
    const bottom = Math.min(box.bottom, parent.bottom)
    return {
      visibleHeight: bottom - top,
      clientWidth: el.clientWidth,
      scrollWidth: el.scrollWidth,
      textOverflow: style.textOverflow,
      whiteSpace: style.whiteSpace,
      title: el.getAttribute('title'),
    }
  })
}

test('a long customer name stays visible and ellipsizes at 1600 and 390', async ({ page }) => {
  const { data } = await setup(page)
  data.customers.find(c => c.id === HOFER)!.name = LONG_NAME
  for (const theme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme: theme })
    for (const width of [1600, 390] as const) {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await page.goto('/business/customers')
      const name = width === 1600
        ? table(page).getByRole('row').filter({ hasText: LONG_NAME }).locator('.name-link')
        : page.getByRole('list', { name: 'Customers' }).locator('.card-name', { hasText: LONG_NAME })
      await name.scrollIntoViewIfNeeded()
      await expect(name).toBeVisible()
      const box = await ellipsized(name)
      expect(box.visibleHeight, `${theme} ${width}`).toBeGreaterThan(8)
      expect(box.clientWidth, `${theme} ${width}`).toBeGreaterThan(40)
      expect(box.scrollWidth, `${theme} ${width}`).toBeGreaterThan(box.clientWidth + 1)
      expect(box.textOverflow, `${theme} ${width}`).toBe('ellipsis')
      expect(box.whiteSpace, `${theme} ${width}`).toBe('nowrap')
      expect(box.title, `${theme} ${width}`).toBe(LONG_NAME)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${theme} ${width}`).toBe(true)
      if (width === 1600) {
        const row = table(page).getByRole('row').filter({ hasText: LONG_NAME })
        const aligned = await row.locator('td.c-name .cell').evaluate(cell => {
          const icon = cell.querySelector('.org-mark')!.getBoundingClientRect()
          const link = cell.querySelector('.name-link')!.getBoundingClientRect()
          const legal = cell.querySelector('.legal')!.getBoundingClientRect()
          const bounds = cell.getBoundingClientRect()
          return Math.abs(icon.top - link.top) < 8 && link.top < bounds.bottom - 8 && legal.top >= bounds.bottom - 1
        })
        expect(aligned, `${theme} icon and name share the line`).toBe(true)
        await name.hover()
        await expect(page.locator('.tooltip')).toContainText(LONG_NAME)
      }
    }
  }
})

test('columns resize from the keyboard and the widths are saved', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  const saved = page.waitForRequest(r => r.url().endsWith('/api/preferences/customers') && r.method() === 'PUT')
  await page.goto('/business/customers')
  await ready(page)
  const edge = page.getByRole('separator', { name: 'Resize Industry column' })
  const before = Number(await edge.getAttribute('aria-valuenow'))
  await edge.focus()
  await page.keyboard.press('ArrowRight'); await page.keyboard.press('ArrowRight')
  await expect(edge).toHaveAttribute('aria-valuenow', String(before + 32))
  const body = (await saved).postDataJSON()
  expect(body.value.widths.industry).toBeGreaterThan(before)
})

test('an admin adds a customer with n, lands on its page, and can undo it', async ({ page }) => {
  const { calls } = await setup(page)
  await page.goto('/business/customers')
  await ready(page)
  await page.keyboard.press('n')
  const dialog = page.getByRole('dialog', { name: 'New customer' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByLabel('Name', { exact: true })).toBeFocused()
  await dialog.getByRole('button', { name: 'Add customer' }).click()
  await expect(dialog.getByText('A name is needed.')).toBeVisible()
  await dialog.getByLabel('Name', { exact: true }).fill('Weingut Sattler')
  await dialog.getByLabel(/^Website/).fill('sattler-wein.at')
  await dialog.getByLabel(/^City/).fill('Gamlitz')
  await dialog.getByLabel(/^Country/).fill('Austria')
  await dialog.getByLabel(/^Website/).press('Enter')
  await expect(page.getByRole('heading', { name: 'Weingut Sattler', level: 1 })).toBeVisible()
  const post = calls.find(c => c.method === 'POST' && c.path === '/api/crm/organisations')!
  expect(post.body).toMatchObject({ name: 'Weingut Sattler', website: 'https://sattler-wein.at', billing_address: { city: 'Gamlitz', country: 'Austria', street: '' }, hourly_rate_minor: null })
  expect(post.body).not.toHaveProperty('expected_revision')
  await expect(page.locator('.hero')).toContainText('Assigned with the first quote')
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page).toHaveURL('/business/customers')
  await expect(page.getByText('Weingut Sattler is removed again.')).toBeVisible()
  await expect(names(page)).toHaveCount(7)
})

test('members read customers but cannot change them; an empty list says who adds them', async ({ page }) => {
  await setup(page, { role: 'member' })
  await page.goto('/business/customers')
  await ready(page)
  await expect(page.getByRole('button', { name: 'New customer' })).toHaveCount(0)
  await page.keyboard.press('n')
  await expect(page.getByRole('dialog', { name: 'New customer' })).toBeHidden()
  await names(page).filter({ hasText: 'Bäckerei Hofer' }).click()
  await expect(page.getByRole('heading', { name: 'Bäckerei Hofer', level: 1 })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Edit', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Propose a rewrite' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Add contact' })).toHaveCount(0)
  await expect(page.locator('.integration')).toContainText('A workspace admin can have another CRM connected.')
  await page.unrouteAll({ behavior: 'ignoreErrors' })
  await setup(page, { role: 'member', empty: true })
  await page.goto('/business/customers')
  await expect(page.getByRole('heading', { name: 'No customers yet' })).toBeVisible()
  await expect(page.getByText('A workspace admin adds customers.')).toBeVisible()
})

test('the customer page: number, primary contact, contacts, related work and an honest CRM state', async ({ page }) => {
  const errors = watchErrors(page)
  await setup(page)
  await page.goto(`/business/customers/${HOFER}`)
  await expect(page.getByRole('heading', { name: 'Bäckerei Hofer', level: 1 })).toBeVisible()
  await expect(page).toHaveTitle(/^Bäckerei Hofer · /)
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('Customers')
  const hero = page.getByRole('region', { name: 'At a glance' })
  await expect(hero).toContainText('K-0042')
  await expect(hero).toContainText('Jana Hofer')
  await expect(hero.getByRole('link', { name: 'jana@hofer-backwaren.at' })).toHaveAttribute('href', 'mailto:jana@hofer-backwaren.at')
  await expect(hero).toContainText('95.00 EUR')
  const contacts = page.getByRole('region', { name: /Contacts/ })
  await expect(contacts.locator('.contact .name')).toHaveText(['Jana Hofer', 'Max Brandl'])
  await expect(contacts.locator('.contact').first()).toContainText('Primary')
  const related = page.getByRole('region', { name: 'Projects, quotes and hours' })
  await expect(related.getByRole('link', { name: /Pharos/ }).first()).toHaveAttribute('href', '/p/PHAROS')
  await expect(related.getByRole('link', { name: /Q-2026-0012/ })).toHaveAttribute('href', `/business/quotes/${QUOTE_12}`)
  await expect(related).toContainText('Draft, no number yet')
  await expect(related).toContainText('7h 30m')
  await expect(related).toContainText('712.50 EUR')
  await expect(related.getByRole('link', { name: /Framework agreement/ })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Addresses' })).toContainText('Same as billing')
  await expect(page.locator('.integration')).toContainText('Not connected')
  await expect(page.locator('.integration')).toContainText('No CRM provider is connected')
  // Customers with projects or quotes cannot be deleted.
  await page.getByRole('button', { name: 'More actions' }).click()
  await expect(page.getByRole('menuitem', { name: 'Delete customer…' })).toBeDisabled()
  await page.keyboard.press('Escape')
  expect(errors).toEqual([])
})

test('customer and project files can be uploaded, edited and removed; project cooperation is saved', async ({ page }) => {
  const { data } = await setup(page)
  const related = data.related[HOFER]
  const documents = related.documents as Record<string, unknown>[]
  const projects = related.projects as Record<string, unknown>[]
  let metadataBody: Record<string, unknown> | null = null
  let cooperationBody: Record<string, unknown> | null = null
  await page.route('**/api/crm/documents/att-1/metadata', async route => {
    metadataBody = route.request().postDataJSON()
    Object.assign(documents[0], metadataBody, { revision: 2 })
    await route.fulfill({ json: documents[0] })
  })
  await page.route('**/api/crm/projects/p-pharos/cooperation', async route => {
    cooperationBody = route.request().postDataJSON()
    projects[0].cooperation = cooperationBody
    projects[0].cooperation_revision = 2
    await route.fulfill({ json: { ...cooperationBody, revision: 2 } })
  })
  await page.route(`**/api/nodes/${HOFER}/attachments`, async route => {
    documents.push({ attachment_id: 'att-new', node_id: HOFER, name: 'new.pdf', title: '', category: '', status: 'draft', valid_from: null, valid_until: null, revision: 0 })
    await route.fulfill({ status: 201, json: [{ id: 'att-new', node_id: HOFER, name: 'new.pdf' }] })
  })
  await page.route('**/api/attachments/att-new', async route => { documents.splice(documents.findIndex(d => d.attachment_id === 'att-new'), 1); await route.fulfill({ status: 204, body: '' }) })
  await page.goto(`/business/customers/${HOFER}`)
  const section = page.getByRole('region', { name: 'Projects, quotes and hours' })
  await section.getByRole('button', { name: 'Edit details' }).first().click()
  await section.getByLabel('Label').fill('Updated agreement')
  await section.getByLabel('Category').fill('contract')
  await section.getByLabel('Valid until').fill('2028-12-31')
  await section.getByRole('button', { name: 'Save details' }).click()
  await expect(section.getByRole('link', { name: /Updated agreement/ })).toBeVisible()
  expect(metadataBody).toMatchObject({ title: 'Updated agreement', status: 'active', valid_until: '2028-12-31', expected_revision: 1 })
  await section.getByRole('button', { name: 'Edit cooperation' }).click()
  await section.getByLabel('Engagement').fill('Retainer')
  await section.getByLabel('Environment responsibility').fill('Customer')
  await section.getByLabel('SLA').fill('Next business day')
  await section.getByRole('button', { name: 'Save cooperation' }).click()
  await expect(section).toContainText('SLA: Next business day')
  expect(cooperationBody).toMatchObject({ engagement: 'Retainer', environment_responsibility: 'Customer', sla: 'Next business day', expected_revision: 1 })
  await section.getByLabel('Upload file').setInputFiles({ name: 'new.pdf', mimeType: 'application/pdf', buffer: Buffer.from('PDF fixture') })
  await expect(section.getByRole('link', { name: /new.pdf/ })).toBeVisible()
  await section.getByRole('button', { name: 'Edit details' }).last().click()
  await section.getByRole('button', { name: 'Delete file' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Delete file' }).click()
  await expect(section.getByRole('link', { name: /new.pdf/ })).toHaveCount(0)
})

test('edit mode: e opens one form, ⌘↵ saves the whole record, Undo restores it', async ({ page }) => {
  const { calls, data } = await setup(page)
  await page.goto(`/business/customers/${HOFER}`)
  await expect(page.getByRole('heading', { name: 'Bäckerei Hofer', level: 1 })).toBeVisible()
  await page.keyboard.press('e')
  const form = page.getByRole('form', { name: 'Edit Bäckerei Hofer' })
  await expect(form).toBeVisible()
  await expect(form.getByLabel('Name', { exact: true })).toBeFocused()
  await form.getByLabel('Hourly rate').fill('abc')
  await page.keyboard.press(`${mod}+Enter`)
  await expect(form.getByText('An amount like 95 or 95.50.')).toBeVisible()
  await expect(form.getByLabel('Hourly rate')).toBeFocused()
  await form.getByLabel('Hourly rate').fill('102.50')
  await form.getByLabel('City').first().fill('Graz-Andritz')
  await expect(page.getByText('Unsaved').first()).toBeVisible()
  await page.keyboard.press(`${mod}+Enter`)
  await expect(form).toBeHidden()
  const patch = calls.find(c => c.method === 'PATCH' && c.path === `/api/crm/organisations/${HOFER}`)!
  expect(patch.body).toMatchObject({ expected_revision: 3, name: 'Bäckerei Hofer', hourly_rate_minor: 10250, lp_rate_minor: 12000, currency: 'EUR', vat_id: 'ATU58372910', billing_address: { city: 'Graz-Andritz', street: 'Herrengasse 14' } })
  await expect(page.locator('.hero')).toContainText('102.50 EUR')
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByText('Bäckerei Hofer is back as it was.')).toBeVisible()
  await expect(page.locator('.hero')).toContainText('95.00 EUR')
  expect(data.events.some(e => e.type === 'crm.customer_updated' && e.undo_of !== null)).toBe(true)
  // Esc with changes asks before discarding them.
  await page.keyboard.press('e')
  await form.getByLabel('Industry').fill('Bakery')
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'Discard your changes?' })).toBeVisible()
  await page.getByRole('button', { name: 'Discard' }).click()
  await expect(form).toBeHidden()
  await expect(page.locator('.summary')).toContainText('Food')
})

test('a change made elsewhere keeps the draft and saves on the newer version', async ({ page }) => {
  const { calls } = await setup(page, { meanwhile: HOFER })
  await page.goto(`/business/customers/${HOFER}`)
  await expect(page.getByRole('heading', { name: 'Bäckerei Hofer', level: 1 })).toBeVisible()
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  const form = page.getByRole('form', { name: 'Edit Bäckerei Hofer' })
  await form.getByLabel('Industry').fill('Bakery')
  await form.getByRole('button', { name: 'Save' }).click()
  await expect(form.getByText('Changed elsewhere while you were editing.')).toBeVisible()
  await expect(form.getByLabel('Industry')).toHaveValue('Bakery')
  await form.getByRole('button', { name: 'Save anyway' }).click()
  await expect(form).toBeHidden()
  const patches = calls.filter(c => c.method === 'PATCH')
  expect(patches.map(p => (p.body as { expected_revision: number }).expected_revision)).toEqual([3, 4])
  await expect(page.locator('.summary')).toContainText('Bakery')
})

test('contacts: the first becomes primary, the primary can move, removal asks and undoes', async ({ page }) => {
  await setup(page)
  await page.goto(`/business/customers/${LUMEN}`)
  await expect(page.getByRole('heading', { name: 'Atelier Lumen', level: 1 })).toBeVisible()
  const contacts = page.getByRole('region', { name: /Contacts/ })
  await expect(contacts).toContainText('No contacts yet. The first one you add becomes the primary contact.')
  await contacts.getByRole('button', { name: 'Add contact' }).click()
  const form = contacts.getByRole('form', { name: 'New contact' })
  await expect(form.getByLabel('Name', { exact: true })).toBeFocused()
  await form.getByLabel('Name', { exact: true }).fill('Ines Kraus')
  await form.getByLabel(/^Email/).fill('ines@lumen')
  await form.getByRole('button', { name: 'Add contact' }).click()
  await expect(form.getByText('Use an address like jana@hofer.at.')).toBeVisible()
  await form.getByLabel(/^Email/).fill('ines@atelier-lumen.de')
  await form.getByLabel(/^Role/).fill('Partner')
  await form.getByLabel(/^Email/).press('Enter')
  await expect(page.getByText('Added Ines Kraus as the primary contact.')).toBeVisible()
  await expect(page.locator('.hero')).toContainText('Ines Kraus')
  await expect(contacts.locator('.contact').first()).toContainText('Primary')

  await page.goto(`/business/customers/${HOFER}`)
  await contacts.getByRole('button', { name: 'Make Max Brandl the primary contact' }).click()
  await expect(page.getByText('Max Brandl is the primary contact now.')).toBeVisible()
  await expect(page.locator('.hero')).toContainText('Max Brandl')
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.locator('.hero')).toContainText('Jana Hofer')

  await contacts.getByRole('button', { name: 'Remove Jana Hofer' }).click()
  await expect(page.getByRole('dialog', { name: 'Remove Jana Hofer?' })).toContainText('Max Brandl becomes the primary contact.')
  await page.getByRole('button', { name: 'Remove contact' }).click()
  await expect(contacts.locator('.contact .name')).toHaveText(['Max Brandl'])
  await expect(page.locator('.hero')).toContainText('Max Brandl')
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByText('Jana Hofer is back.')).toBeVisible()
  await expect(contacts.locator('.contact .name')).toHaveText(['Jana Hofer', 'Max Brandl'])
  await expect(page.locator('.hero')).toContainText('Jana Hofer')

  await contacts.getByRole('button', { name: 'Edit Max Brandl' }).click()
  await contacts.getByLabel(/^Phone/).fill('+43 316 820 441')
  await contacts.getByRole('button', { name: 'Save contact' }).click()
  await expect(contacts.getByRole('link', { name: '+43 316 820 441' })).toHaveAttribute('href', 'tel:+43316820441')
})

test('a note rewrite is a draft first; applying it shows the difference and can be undone', async ({ page }) => {
  const { calls } = await setup(page)
  await page.goto(`/business/customers/${HOFER}`)
  const notes = page.getByRole('region', { name: 'Notes' })
  await expect(notes).toContainText('Prefers calls before 10:00.')
  await notes.getByRole('button', { name: 'Propose a rewrite' }).click()
  const editor = notes.getByRole('textbox', { name: 'Proposed notes, Markdown' })
  await expect(editor).toBeFocused()
  await editor.fill('Prefers calls before 10:00.\nInvoices go to Max Brandl in accounting.\nSummer break in August.')
  await notes.getByRole('button', { name: 'Save proposal' }).click()
  await expect(page.getByText('Proposal saved. The notes stay as they are until it is applied.')).toBeVisible()
  expect(calls.find(c => c.path.endsWith('/note-rewrite'))!.body).toEqual({ draft_text: 'Prefers calls before 10:00.\nInvoices go to Max Brandl in accounting.\nSummer break in August.', expected_revision: 3 })
  // The notes themselves are unchanged; the proposal shows what would change.
  await expect(notes.locator('.notes-body')).toContainText('Invoices go to accounting, not to Jana.')
  const proposal = notes.getByRole('region', { name: 'Proposed rewrite' })
  await expect(proposal).toContainText('Draft, not applied')
  await expect(proposal.locator('.line.remove .text')).toHaveText(['Invoices go to accounting, not to Jana.'])
  await expect(proposal.locator('.line.add .text')).toHaveText(['Invoices go to Max Brandl in accounting.'])
  await proposal.getByRole('button', { name: 'Review and apply…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Apply this rewrite?' })
  await expect(dialog.locator('.counts > span')).toHaveText(['1 added', '1 removed'])
  await axe(page)
  await dialog.getByRole('button', { name: 'Apply rewrite' }).click()
  await expect(dialog).toBeHidden()
  await expect(notes.locator('.notes-body')).toContainText('Invoices go to Max Brandl in accounting.')
  await expect(proposal).toBeHidden()
  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByText('The earlier notes are back.')).toBeVisible()
  await expect(notes.locator('.notes-body')).toContainText('Invoices go to accounting, not to Jana.')
})

test('AI notes are honestly disabled when no model is configured', async ({ page }) => {
  const { calls } = await setup(page)
  await page.goto(`/business/customers/${HOFER}`)
  const notes = page.getByRole('region', { name: 'Notes' })
  await expect(notes.getByRole('button', { name: 'Suggest with AI' })).toBeDisabled()
  await expect(notes).toContainText('No model is configured for AI note rewriting.')
  await expect(notes.getByRole('button', { name: 'Propose a rewrite' })).toBeEnabled()
  expect(calls.some(c => c.path.endsWith('/note-ai/generate'))).toBe(false)
})

test('AI generates only a review proposal and admin applies it separately', async ({ page }) => {
  const { calls } = await setup(page, { aiEnabled: true })
  await page.goto(`/business/customers/${HOFER}`)
  const notes = page.getByRole('region', { name: 'Notes' })
  await notes.getByRole('button', { name: 'Suggest with AI' }).click()
  await expect(notes.getByRole('region', { name: 'Proposed rewrite' })).toContainText('Draft, not applied')
  await expect(notes.locator('.notes-body')).toContainText('Invoices go to accounting, not to Jana.')
  expect(calls.find(c => c.path.endsWith('/note-ai/generate'))?.body).toEqual({ expected_revision: 3 })
  await notes.getByRole('button', { name: 'Review and apply…' }).click()
  await expect(page.getByRole('dialog', { name: 'Apply this rewrite?' })).toBeVisible()
  await page.getByRole('button', { name: 'Apply rewrite' }).click()
  await expect(notes.locator('.notes-body')).toContainText('Invoices go to Max Brandl in accounting.')
})

test('a proposal made before another change can no longer be applied as it is', async ({ page }) => {
  await setup(page)
  await page.goto(`/business/customers/${HOFER}`)
  const notes = page.getByRole('region', { name: 'Notes' })
  await notes.getByRole('button', { name: 'Propose a rewrite' }).click()
  await notes.getByRole('textbox', { name: 'Proposed notes, Markdown' }).fill('Short notes.')
  await notes.getByRole('button', { name: 'Save proposal' }).click()
  // Another change to the customer (the primary contact) moves its revision.
  await page.getByRole('button', { name: 'Make Max Brandl the primary contact' }).click()
  await expect(page.locator('.hero')).toContainText('Max Brandl')
  await expect(notes).toContainText('The customer changed since this proposal was made')
  await expect(notes.getByRole('button', { name: 'Review and apply…' })).toHaveCount(0)
  await notes.getByRole('button', { name: 'Propose again' }).click()
  await expect(notes.getByRole('textbox', { name: 'Proposed notes, Markdown' })).toHaveValue('Short notes.')
})

test('Settings › Business: the quote sender is a form with a preview; saving keeps texts, layout and logo', async ({ page }) => {
  const { calls } = await setup(page)
  await page.goto('/settings/business#quotes')
  const card = page.locator('#quotes')
  await expect(card).toHaveClass(/arrived/)
  await expect(card.getByLabel('Company', { exact: true })).toHaveValue('INSPR Studio')
  await dotsNeverLead(page)
  await expect(card.locator('.paper')).toContainText('Annenstraße 1')
  await card.getByLabel('Company', { exact: true }).fill('INSPR Studio GmbH')
  await card.getByLabel('BIC').fill('rzstat2g')
  await expect(card.locator('.paper')).toContainText('INSPR Studio GmbH')
  await expect(card.getByText('Unsaved')).toBeVisible()
  await card.getByRole('button', { name: 'Save quote settings' }).click()
  await expect(page.getByText('Quote settings saved. New quotes use them.')).toBeVisible()
  const patch = calls.find(c => c.method === 'PATCH' && c.path === '/api/quotes/settings')!
  expect(patch.body).toEqual({
    expected_revision: 2, numbering_time_zone: 'Europe/Vienna', default_currency: 'EUR', smtp_confirmation_enabled: false,
    defaults: { intro: 'Thank you for your enquiry.' }, layout: { page_style: 'classic' },
    sender: { logo_file_id: 'file-logo', company: 'INSPR Studio GmbH', street: 'Annenstraße 1', postal_code: '8020', city: 'Graz', country: 'Austria', email: 'hello@inspr.example', uid: 'ATU77777777', iban: 'AT00 0000 0000 0000 0000', bic: 'rzstat2g' },
  })
  // The Customers card says how customers are kept.
  await expect(page.locator('#customers')).toContainText('Not connected')
  await axe(page)
})

test('the Quotes page links admins to the quote settings card', async ({ page }) => {
  await setup(page)
  await page.goto('/business/quotes')
  await page.getByRole('link', { name: 'Quote settings' }).click()
  await expect(page).toHaveURL('/settings/business#quotes')
  await expect(page.locator('#quotes')).toHaveClass(/arrived/)
  await expect(page.locator('#quotes').getByLabel('Company', { exact: true })).toBeVisible()
})

test('a compiled CRM provider can be configured, searched and imported without credentials in the page', async ({ page }) => {
  const { data } = await setup(page, { providers: [{ id: 'http', enabled: false, configured: false, revision: 0 }] })
  let config: Record<string, unknown> | null = null
  await page.route('**/api/crm/providers/http/config', route => {
    config = route.request().postDataJSON() as Record<string, unknown>
    return route.fulfill({ json: { id: 'http', enabled: true, configured: true, revision: 1 } })
  })
  await page.route('**/api/crm/providers/search?*', route => route.fulfill({ json: [{ provider: 'http', external_id: 'remote-1', name: 'Remote Sample', fields: {} }] }))
  await page.route('**/api/crm/providers/http/import', route => {
    expect(route.request().postDataJSON()).toEqual({ external_id: 'remote-1' })
    const imported = { ...data.customers[0]!, id: 'org-new-provider', key: 'ORG-80', name: 'Remote Sample', external_provider: 'http', external_id: 'remote-1' }
    data.customers.push(imported)
    return route.fulfill({ status: 201, json: imported })
  })
  await page.goto('/business/customers')
  await ready(page)
  await page.getByText('Provider settings').click()
  await page.getByLabel('Secret reference for http').fill('secret://test/crm')
  await page.getByRole('button', { name: 'Enable', exact: true }).click()
  expect(config).toEqual({ enabled: true, secret_ref: 'secret://test/crm', expected_revision: 0 })
  await page.getByLabel('Search connected CRM').fill('Remote')
  await page.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(page.getByRole('list', { name: 'External customers' })).toContainText('Remote Sample')
  await page.getByRole('button', { name: 'Import', exact: true }).click()
  await expect(page).toHaveURL('/business/customers/org-new-provider')
  await expect(page.getByRole('heading', { name: 'Remote Sample', level: 1 })).toBeVisible()
})

test('a linked customer shows a provider link and durable sync status', async ({ page }) => {
  const { data } = await setup(page, { providers: [{ id: 'http', enabled: true, configured: true, revision: 1 }] })
  const customer = data.customers.find(c => c.id === HOFER)!
  customer.external_provider = 'http'; customer.external_id = 'remote-1'; customer.external_url = 'https://example.invalid/customer/remote-1'
  let state: 'never' | 'ok' | 'error' = 'never'
  let fail = false
  await page.route(`**/api/crm/organisations/${HOFER}/sync-status`, route => route.fulfill({ json: { provider_id: 'http', state, attempted_at: state === 'never' ? null : '2026-09-24T12:00:00Z', synced_at: state === 'never' ? null : '2026-09-24T12:00:00Z', error: state === 'error' ? 'provider_unavailable' : '' } }))
  await page.route(`**/api/crm/organisations/${HOFER}/sync`, route => {
    if (fail) { state = 'error'; return route.fulfill({ status: 409, json: { message: 'Provider unavailable' } }) }
    state = 'ok'; return route.fulfill({ json: customer })
  })
  await page.goto(`/business/customers/${HOFER}`)
  await expect(page.getByRole('link', { name: /example.invalid/ })).toBeVisible()
  await expect(page.getByText('Not synced yet')).toBeVisible()
  await page.getByRole('button', { name: 'Sync now' }).click()
  await expect(page.getByText(/^Synced /)).toBeVisible()
  fail = true
  await page.getByRole('button', { name: 'Sync now' }).click()
  await expect(page.getByText(/Last sync failed/)).toBeVisible()
  await expect(page.getByRole('alert').filter({ hasText: 'changed elsewhere' })).toBeVisible()
})

test('Manage parts: a workspace that never had Customers enables Quotes, which brings Customers and its kinds', async ({ page }) => {
  const { calls } = await setup(page, { enabled: ['business_costs', 'business_hours'], kinds: ['epic', 'ticket', 'task', 'project', 'cost_unit'] })
  await page.goto('/settings/business')
  const parts = page.locator('#parts')
  await expect(parts.getByRole('checkbox', { name: 'Customers enabled' })).not.toBeChecked()
  await expect(parts.getByRole('checkbox', { name: 'Quotes enabled' })).not.toBeChecked()
  await expect(parts).toContainText('Also enables Customers.')
  await parts.getByRole('checkbox', { name: 'Quotes enabled' }).click()
  await expect(page.getByText('Quotes is enabled, with Customers.')).toBeVisible()
  const puts = calls.filter(c => c.method === 'PUT' && c.path.startsWith('/api/plugins/'))
  expect(puts.map(c => c.path)).toEqual(['/api/plugins/business_costs/installation', '/api/plugins/business_crm/installation', '/api/plugins/business_quotes/installation'])
  expect(puts[1].body).toEqual({ manifest_digest_sha256: 'cd'.repeat(32), enabled: true, permissions: ['integrations.call', 'nodes.contribute', 'steps.apply', 'tools.invoke', 'views.provide'] })
  const kinds = calls.filter(c => c.method === 'POST' && c.path === '/api/kinds')
  expect(kinds.map(c => (c.body as { slug: string }).slug)).toEqual(['organisation', 'contact', 'quote'])
  // The organisation kind takes the plugin's own field schema.
  expect((kinds[0].body as { field_schema: unknown }).field_schema).toEqual(ORG_SCHEMA)
  await expect(parts.getByRole('checkbox', { name: 'Customers enabled' })).toBeChecked()
  await page.goto('/business/customers')
  await ready(page)
})

test('gate states: a digest mismatch or missing permissions close Customers and say how to fix it', async ({ page }) => {
  await setup(page, { enabled: ['business_costs', 'business_hours', 'business_quotes'], mismatch: ['business_crm'] })
  await page.goto('/settings/business')
  const parts = page.locator('#parts')
  await expect(parts.locator('.area').filter({ has: page.locator('.area-name', { hasText: /^Customers/ }) })).toContainText('Digest mismatch')
  await expect(parts).toContainText('Pinned to a different build. Enabling pins it to this one.')
  await page.goto('/business/customers')
  await expect(page.getByRole('heading', { name: 'Customers is not enabled for this workspace' })).toBeVisible()
  await page.unrouteAll({ behavior: 'ignoreErrors' })
  await setup(page, { enabled: ['business_costs', 'business_hours'], underGranted: ['business_crm'] })
  await page.goto('/settings/business')
  await expect(page.locator('#parts .area').filter({ has: page.locator('.area-name', { hasText: /^Customers/ }) })).toContainText('Permissions incomplete')
  await expect(page.locator('#parts')).toContainText('Not allowed connecting other services yet. Enabling grants what it needs.')
})

test('at 390 the meta line wraps without a separator at the start or end of a line; the internal key stays hidden', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const { data } = await setup(page)
  // Long enough to wrap whatever the platform's scrollbars take: overlay scrollbars
  // (macOS without a mouse) leave 15px more than classic ones, and "Food" alone
  // fitted on one line there.
  data.customers.find(c => c.id === HOFER)!.industry = 'Bakery and confectionery'
  await page.goto(`/business/customers/${HOFER}`)
  const line = page.locator('.summary .dot-list')
  await expect(line.locator('> *')).toHaveCount(3)
  // The website wraps below the first item. A wider system font can put each
  // item on its own line; either way a wrapped line starts at the clipped edge.
  const rows = await line.evaluate(list => {
    const tops = [...list.children].map(el => Math.round(el.getBoundingClientRect().top))
    const site = Math.round(list.querySelector('a')!.getBoundingClientRect().top)
    return { lines: new Set(tops).size, siteWraps: site > tops[0] }
  })
  expect(rows.lines).toBeGreaterThanOrEqual(2)
  expect(rows.lines).toBeLessThanOrEqual(3)
  expect(rows.siteWraps).toBe(true)
  await expect(page.getByRole('region', { name: 'Projects, quotes and hours' }).getByText('Framework agreement')).toBeVisible()
  await dotsNeverLead(page)
  await expect(page.getByRole('region', { name: 'About' })).not.toContainText('ORG-1')
  await expect(page.getByRole('region', { name: 'About' })).not.toContainText('Key')
})

test('at 390 the list is cards and the customer page fits', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  await page.goto('/business/customers')
  await expect(page.getByRole('list', { name: 'Customers' }).getByRole('link').first()).toBeVisible()
  await noClipping(page, 'main')
  await page.getByRole('list', { name: 'Customers' }).getByRole('link', { name: /Bäckerei Hofer/ }).click()
  await expect(page.getByRole('heading', { name: 'Bäckerei Hofer', level: 1 })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Projects, quotes and hours' }).getByText('Q-2026-0012')).toBeVisible()
  await noClipping(page, 'main')
  await page.getByRole('button', { name: 'Edit', exact: true }).click()
  await expect(page.getByRole('form', { name: 'Edit Bäckerei Hofer' })).toBeVisible()
  await noClipping(page, 'main')
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: customers list, customer page and edit mode in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme })
    await setup(page)
    await page.goto('/business/customers')
    await ready(page)
    await axe(page)
    await page.goto(`/business/customers/${HOFER}`)
    await expect(page.getByRole('region', { name: 'Projects, quotes and hours' }).getByText('Q-2026-0012')).toBeVisible()
    await axe(page)
    await page.keyboard.press('e')
    await expect(page.getByRole('form', { name: 'Edit Bäckerei Hofer' })).toBeVisible()
    await axe(page)
  })
}
