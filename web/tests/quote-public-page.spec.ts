// SPDX-License-Identifier: AGPL-3.0-only
// U18 (AEON-99): the page a customer opens from a quote link, as a real route.
// The sender's name, the frozen document scaled to the screen, the acceptance
// bound to the displayed fingerprint, ended and unknown links, the receipt once
// accepted; axe in light and dark at desktop and 390; and both content security
// policies: the production one on the app's pages and the stricter public one
// here, with no violation reported by the browser.
import AxeBuilder from '@axe-core/playwright'
import { randomBytes } from 'node:crypto'
import { expect, test, type Page } from '@playwright/test'
import { crmData, mockCRM } from './crm-fixtures'
import { Q, mockPublicQuote, mockQuotes, quoteWorld } from './quote-list-fixtures'
import { fixtures, mockWork } from './work-fixtures'
import { quoteDocument } from './quote-inspector-fixtures'

const LINK = '/offers/sel-demo/tok-example'
// The production policy (internal/httpapi) and a stricter one for public pages.
// The Vite dev server injects CSS as <style> elements, so these runs allow inline
// styles; the production build ships CSS files and needs no such allowance.
const DEV_STYLES = "style-src 'self' 'unsafe-inline'"
export const PRODUCTION_CSP = "default-src 'self'; img-src 'self' blob: data:"
export const PUBLIC_CSP = "default-src 'none'; script-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

async function withPolicy(page: Page, policy: string) {
  await page.addInitScript(() => {
    const seen: string[] = []
    ;(window as unknown as { cspViolations: string[] }).cspViolations = seen
    document.addEventListener('securitypolicyviolation', event => { seen.push(`${event.violatedDirective} ${event.blockedURI}`) })
  })
  await page.route('**/*', async route => {
    if (route.request().resourceType() !== 'document') return route.fallback()
    const response = await route.fetch()
    // The production SPA gives only its theme bootstrap a per-response nonce.
    // Vite serves the unreplaced template; reproduce that boundary here too.
    const nonce = randomBytes(16).toString('base64')
    const body = (await response.text()).replaceAll('__AEON_THEME_NONCE__', nonce)
    const directives = policy.split(';').map(value => value.trim()).filter(value => !value.startsWith('script-src '))
    await route.fulfill({ response, body, headers: { ...response.headers(), 'content-security-policy': `${directives.join('; ')}; script-src 'self' 'nonce-${nonce}'; ${DEV_STYLES}` } })
  })
}
const violations = (page: Page) => page.evaluate(() => (window as unknown as { cspViolations: string[] }).cspViolations)
async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')} — ${n.failureSummary?.split('\n').slice(1, 2).join(' ').trim()}`).join('\n')}`)
  expect(summary, summary.join('\n')).toEqual([])
}
function englishDocument() {
  const d = quoteDocument()
  d.title = 'Booking platform for the harbour office'; d.subtitle = 'Design, build and support'
  d.sender = { ...d.sender, company: 'Harbour & Pine Studio' }
  d.recipient = { ...d.recipient, name: 'Harbour & Pine Ltd' }
  d.legal = { intro: 'Thank you for your enquiry. We are happy to offer the following work.', accept_text: 'By accepting you agree to the terms of this offer.', vat_note: 'All amounts are net of VAT.' }
  d.sections = [{ id: '11111111-1111-4111-8111-000000000001', heading: 'Scope', body: '', nodes: [{ id: '22222222-2222-4222-8222-000000000001', kind: 'paragraph', text: 'We design and build the booking platform for the harbour office.' }] }] as never
  d.positions = [{ id: '33333333-3333-4333-8333-000000000001', pricing_source: 'manual', short_text: 'Build', long_text: 'Design and build of the platform', quantity: '1', unit_label: 'item', unit_price_cents: 1250000, total_cents: 1250000, currency: 'EUR' }]
  d.net_total_cents = 1250000
  return d
}
async function open(page: Page, state: Parameters<typeof mockPublicQuote>[1]) {
  await mockWork(page, fixtures())
  const posted = await mockPublicQuote(page, state)
  await page.goto(LINK)
  return posted
}

test('the customer sees the sender, the facts and the frozen document in its language, and accepts exactly what is shown', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const posted = await open(page, { acceptable: true })
  await expect(page.locator('.pq-sender')).toHaveText('Beispiel Studio GmbH')
  await expect(page.locator('#pq-title')).toHaveText('Relaunch des Kundenportals')
  // A German document: German words, 21.09.2026 dates and 5.560,00 EUR amounts, on the page and the paper.
  expect(await page.evaluate(() => document.documentElement.lang)).toBe('de')
  await expect(page.locator('.pq-eyebrow')).toHaveText('Für Bäckerei Hofer')
  const facts = await page.locator('.pq-facts dd').allTextContents()
  expect(facts[0]).toBe('5.560,00 EUR')
  expect(facts.slice(1).every(day => /^\d{2}\.\d{2}\.\d{4}$/.test(day))).toBe(true)
  await expect(page.locator('.pq-facts')).toContainText('Nettosumme')
  await expect(page.locator('.pq-facts')).toContainText('Gültig bis')
  await expect(page.locator('.quote-page .quote-acceptance')).toContainText('5.560,00 EUR')
  await expect(page.locator('.pq-status')).toHaveText(/^Bitte lesen Sie das Angebot unten\./)
  await expect(page.getByRole('link', { name: /^PDF öffnen/ })).toBeVisible()
  const english = await page.locator('.public-quote').evaluate(el => [...el.querySelectorAll('.pq-bar, .pq-wrap, .pq-foot')].map(n => n.textContent).join(' '))
  expect(english).not.toMatch(/\b(Please|Review|Valid until|Net total|For |Quote |Accept|Your name|Company|Note to|This page)\b/)
  await expect(page.getByRole('region', { name: 'Angebotsdokument' })).toBeVisible()
  await expect(page).toHaveTitle(/^Angebot A260922-12 von Beispiel Studio GmbH/)
  expect(await page.evaluate(() => document.querySelector('meta[name="robots"]')?.getAttribute('content'))).toBe('noindex, nofollow, noarchive')

  await page.getByRole('button', { name: 'Prüfen und annehmen' }).click()
  await expect(page.getByLabel('Ihr Name')).toBeFocused()
  await expect(page.locator('.pq-lead')).toHaveText('Sie nehmen Version 1 des Angebots A260922-12 über netto 5.560,00 EUR an, genau so wie oben dargestellt.')
  const submit = page.getByRole('button', { name: 'Angebot annehmen', exact: true })
  await expect(submit).toBeDisabled()
  await page.getByLabel('Ich habe dieses Angebot geprüft und nehme es an.').check()
  await submit.click()
  await expect(page.getByText('Bitte geben Sie Ihren Namen ein.')).toBeVisible()
  expect(posted).toHaveLength(0)
  await page.getByLabel('Ihr Name').fill('Jana Hofer')
  await page.getByLabel(/^Firma/).fill('Hofer Backwaren GmbH')
  await expect(page.getByLabel(/^Nachricht an Beispiel Studio GmbH/)).toBeVisible()
  await submit.click()
  await expect.poll(() => posted.length).toBe(1)
  expect(posted[0]).toMatchObject({ version: 1, expected_content_sha256: expect.stringMatching(/^1{8}/), name: 'Jana Hofer', company: 'Hofer Backwaren GmbH', confirm: true })
  await expect(page.getByRole('heading', { name: 'Vielen Dank, Jana Hofer' })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Vielen Dank, Jana Hofer' }).locator('..')).toBeFocused()
  await expect(page.locator('.pq-done')).toContainText(/Ihre Annahme von Version 1 wurde am \d{2}\.\d{2}\.\d{4}, \d{2}:\d{2} Uhr gespeichert\./)
  await expect(page.locator('.pq-status')).toHaveText(/^Dieses Angebot wurde (am .+ )?angenommen\.$/)
})

test('an English document gets the English catalog with English formats', async ({ page }) => {
  await mockWork(page, fixtures())
  await page.route('**/api/public/quotes/**', route => route.fulfill({ json: {
    document: englishDocument(), offer_no: 'A260922-14', version: 2, content_sha256: 'c'.repeat(64), state: 'issued', expires_at: new Date(Date.now() + 864e5).toISOString(), acceptable: true, receipt_ready: false,
  } }))
  await page.goto(LINK)
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  expect(await page.evaluate(() => document.documentElement.lang)).toBe('en')
  await expect(page.locator('.pq-eyebrow')).toHaveText('For Harbour & Pine Ltd')
  expect((await page.locator('.pq-facts dd').allTextContents())[0]).toBe('12,500.00 EUR')
  expect((await page.locator('.pq-facts dd').allTextContents())[1]).toMatch(/^\d{2}\/\d{2}\/\d{4}$/)
  await expect(page.getByRole('button', { name: 'Review and accept' })).toBeVisible()
  await expect(page.getByLabel('I have reviewed this quote and agree to accept it.')).toBeVisible()
  await expect(page).toHaveTitle(/^Quote A260922-14 from Harbour & Pine Studio/)
})

test('at 390 the paper shrinks to the screen and nothing scrolls sideways or is cut', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open(page, { acceptable: true })
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  // Nothing wider than the scroller, and the page's own surface reaches the right edge
  // (under the reserved scrollbar gutter too): no strip of another background.
  const edge = await page.evaluate(() => {
    const main = document.querySelector('main')!
    return { overflow: main.scrollWidth - main.clientWidth, main: getComputedStyle(main).backgroundColor, page: getComputedStyle(document.querySelector('.public-quote')!).backgroundColor }
  })
  expect(edge.overflow).toBeLessThanOrEqual(0)
  expect(edge.main).toBe(edge.page)
  const cut = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.public-quote *')].filter(el => {
    const r = el.getBoundingClientRect(), c = getComputedStyle(el)
    if (r.width <= 1 || r.height <= 1 || c.display === 'none' || el.closest('svg') || el.closest('.pq-paper')) return false
    return r.left < -0.5 || r.right > innerWidth + 0.5
  }).map(el => `${el.tagName}.${el.className}`))
  expect(cut).toEqual([])
  const paper = (await page.locator('.pq-paper').boundingBox())!
  expect(paper.x).toBeGreaterThanOrEqual(0)
  expect(paper.x + paper.width).toBeLessThanOrEqual(390)
})

test('an ended link reads, but cannot accept; an unknown link says so plainly', async ({ page }) => {
  await open(page, { acceptable: false, linkEnded: true })
  await expect(page.locator('.pq-status')).toHaveText(/^Dieses Angebot können Sie weiterhin lesen\. Die Annahme ist geschlossen\. Dieser Link ist am \d{2}\.\d{2}\.\d{4}, \d{2}:\d{2} Uhr abgelaufen\.$/)
  await expect(page.getByRole('button', { name: 'Angebot annehmen' })).toHaveCount(0)
  await page.unrouteAll({ behavior: 'ignoreErrors' })
  await open(page, { acceptable: false })
  await expect(page.locator('.pq-status')).toHaveText(/Die Annahme ist geschlossen\. Das Angebot war bis \d{2}\.\d{2}\.\d{4} gültig\.$/)
  await page.unrouteAll({ behavior: 'ignoreErrors' })
  await open(page, { acceptable: false, missing: true })
  // Without a document the page follows the browser's language (English here).
  await expect(page.getByRole('heading', { name: 'This link does not open a quote' })).toBeVisible()
})

test('a revoked or unknown link in a German browser says so in German', async ({ browser }) => {
  const context = await browser.newContext({ locale: 'de-AT' })
  const page = await context.newPage()
  await open(page, { acceptable: false, missing: true })
  await expect(page.getByRole('heading', { name: 'Dieser Link öffnet kein Angebot' })).toBeVisible()
  await expect(page.locator('.pq-message')).toContainText('widerrufen')
  await context.close()
})

test('once accepted, the page says when and offers the receipt', async ({ page }) => {
  await open(page, { acceptable: false, accepted: true, receiptReady: true })
  await expect(page.locator('.pq-status')).toHaveText(/^Dieses Angebot wurde am \d{2}\.\d{2}\.\d{4}, \d{2}:\d{2} Uhr angenommen\.$/)
  await expect(page.getByRole('link', { name: /^Annahmebestätigung \(PDF\)/ })).toHaveAttribute('href', '/api/public/quotes/sel-demo/tok-example/pdf')
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: the customer page at desktop and 390 in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme })
    await page.setViewportSize({ width: 1280, height: 900 })
    await open(page, { acceptable: true })
    await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
    await axe(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await axe(page)
  })
}

test('content security: the public page runs under the strict public policy without a violation', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await withPolicy(page, PUBLIC_CSP)
  await open(page, { acceptable: true })
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  await page.getByLabel('Ihr Name').fill('Jana Hofer')
  await page.getByLabel('Ich habe dieses Angebot geprüft und nehme es an.').check()
  await page.getByRole('button', { name: 'Angebot annehmen', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Vielen Dank, Jana Hofer' })).toBeVisible()
  expect(await violations(page)).toEqual([])
})

test('content security: the Quotes list and a docked quote run under the production policy', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await withPolicy(page, PRODUCTION_CSP)
  await mockWork(page, fixtures())
  await mockCRM(page, crmData())
  await mockQuotes(page, quoteWorld())
  await page.goto(`/business/quotes?quote=${Q.accepted}`)
  await expect(page.locator('.quote-dock .quote-document')).toHaveAttribute('data-quote-ready', 'true')
  await page.locator('.quote-dock').getByRole('button', { name: 'Details' }).click()
  await expect(page.getByRole('heading', { name: 'Acceptance receipt' })).toBeVisible()
  expect(await violations(page)).toEqual([])
})
