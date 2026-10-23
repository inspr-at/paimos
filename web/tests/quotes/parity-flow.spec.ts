// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type Page, type Route } from '@playwright/test'
import { fixtures, mockWork } from '../work-fixtures'
import { crmData, mockCRM } from '../crm-fixtures'
import documentFixture from './fixtures/synthetic-document.json' with { type: 'json' }

const quoteId = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
const digest = 'a'.repeat(64)
const sessionId = '44444444-4444-4444-8444-444444444444'

test('customer → quote → inspector save → issue → public acceptance race → receipt', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const crm = crmData()
  const crmCalls = await mockCRM(page, crm)
  let document = structuredClone(documentFixture)
  let draftRevision = 1
  let quoteRevision = 1
  let state = 'draft'
  let created = false
  let accepted = false
  const projection = () => ({ quote_node_id: quoteId, offer_no: 'A260924-01', state, revision: quoteRevision,
    customer_org_node_id: 'org-new-1', current_version: state === 'draft' ? 0 : 1, archived: false,
    title: document.title, customer_name: 'Sample Customer', currency: 'EUR', project_ref: '' })
  const version = () => ({ quote_node_id: quoteId, version: 1, document, content_sha256: digest,
    offer_no: 'A260924-01', created_at: '2026-09-24T09:02:00Z', currency: 'EUR', total: '1.0000',
    digest_mode: 'document-v1', pricing_mode: 'cent-half-up-v1' })
  const publicLink = { id: 'link-synthetic', quote_node_id: quoteId, public_tenant: 'synthetic', version: 1,
    target_content_sha256: digest, expires_at: new Date(Date.now() + 864e5).toISOString(), path: '/offers/synthetic/token' }
  const calls: { method: string; path: string; body: Record<string, unknown> }[] = []
  await page.route('**/api/quotes**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname, method = request.method()
    let body: Record<string, unknown> = {}
    try { body = request.postDataJSON() as Record<string, unknown> } catch { /* GET */ }
    calls.push({ method, path, body })
    if (path === '/api/quotes' && method === 'POST') {
      if (body.customer_org_node_id !== 'org-new-1') return route.fulfill({ status: 400, json: { error: 'wrong customer' } })
      created = true
      return route.fulfill({ status: 201, json: projection() })
    }
    if (path === '/api/quotes' && method === 'GET') return route.fulfill({ json: created ? [projection()] : [] })
    if (path === '/api/quotes/readiness') return route.fulfill({ json: { renderer_available: true, smtp_enabled: false, smtp_configured: false, email_delivery: 'disabled' } })
    if (path === '/api/quotes/acceptances') return route.fulfill({ json: accepted ? [{ quote_node_id: quoteId, version: 1, channel: 'public', accepted_at: '2026-09-24T12:00:00Z', confirmation_state: 'ready' }] : [] })
    if (path === `/api/quotes/${quoteId}`) return route.fulfill({ json: projection() })
    if (path === `/api/quotes/${quoteId}/draft` && method === 'GET') return route.fulfill({ json: {
      document, document_sha256: digest, draft_revision: draftRevision, quote_revision: quoteRevision,
      schema_version: 1, minimum_writer_version: 1, base_version: 0, updated_at: '2026-09-24T09:00:00Z', updated_by_principal_id: sessionId,
    } })
    if (path === `/api/quotes/${quoteId}/draft` && method === 'PATCH') {
      if (request.headers()['if-match'] !== `"qd-${draftRevision}"`) return route.fulfill({ status: 412, json: { error: 'stale' } })
      document = structuredClone(body.document as typeof document)
      draftRevision++
      return route.fulfill({ json: { mutation_id: body.mutation_id, acknowledged_revision: draftRevision,
        acknowledged_quote_revision: quoteRevision, current_revision: draftRevision, current_quote_revision: quoteRevision,
        replayed: false, document, document_sha256: digest, updated_at: '2026-09-24T09:01:00Z', updated_by_principal_id: sessionId } })
    }
    if (path === `/api/quotes/${quoteId}/finalize` && method === 'POST') {
      if (body.expected_quote_revision !== quoteRevision || body.expected_draft_revision !== draftRevision || body.expected_document_sha256 !== digest) return route.fulfill({ status: 409, json: { error: 'stale' } })
      state = 'issued'; quoteRevision++
      return route.fulfill({ json: projection() })
    }
    if (path === `/api/quotes/${quoteId}/versions`) return route.fulfill({ json: state === 'draft' ? [] : [version()] })
    if (path === `/api/quotes/${quoteId}/versions/1`) return route.fulfill({ json: version() })
    if (path === `/api/quotes/${quoteId}/versions/1/public-link`) return route.fulfill({ json: publicLink })
    if (path === `/api/quotes/${quoteId}/versions/1/confirmation` && method === 'GET') return route.fulfill({ json: { state: 'ready', receipt_sha256: 'b'.repeat(64), renderer_version: 'aeon-quote-document-1', updated_at: '2026-09-24T12:01:00Z' } })
    if (path === `/api/quotes/${quoteId}/presence` && method === 'POST') return route.fulfill({ json: { session_id: sessionId, snapshot: { sessions: [], draft_revision: draftRevision, quote_revision: quoteRevision, state } } })
    if (path.startsWith(`/api/quotes/${quoteId}/presence`)) return route.fulfill({ json: { sessions: [], draft_revision: draftRevision, quote_revision: quoteRevision, state } })
    if (path.endsWith('/collaboration/stream')) return route.fulfill({ contentType: 'text/event-stream', body: '' })
    return route.fallback()
  })

  await page.goto('/business/customers')
  await page.getByRole('button', { name: 'New customer' }).click()
  const dialog = page.getByRole('dialog', { name: 'New customer' })
  await dialog.getByLabel('Name', { exact: true }).fill('Sample Customer')
  await dialog.getByRole('button', { name: 'Add customer' }).click()
  await expect(page.getByRole('heading', { name: 'Sample Customer', level: 1 })).toBeVisible()
  expect(crmCalls.some(call => call.method === 'POST' && call.path === '/api/crm/organisations')).toBe(true)

  // This is a mocked-API browser integration. Creation and finalization must
  // dispatch from the actual person-facing controls, under the same guards.
  await page.getByRole('button', { name: 'New quote' }).first().click()
  const createQuote = page.getByRole('dialog', { name: 'New quote' })
  await expect(createQuote.locator('.fixed-customer')).toContainText('Sample Customer')
  await createQuote.getByLabel('Title', { exact: true }).fill('Synthetic service proposal')
  await createQuote.getByRole('button', { name: 'Create quote', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/business/quotes/${quoteId}$`))
  expect(calls.filter(call => call.method === 'POST' && call.path === '/api/quotes').map(call => call.body))
    .toEqual([{ title: 'Synthetic service proposal', customer_org_node_id: 'org-new-1' }])
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  await expect(page.getByRole('complementary', { name: 'Format' }).getByRole('tab')).toHaveText(['Text', 'Section', 'Document'])
  await page.getByRole('textbox', { name: 'Angebotstitel' }).fill('Synthetic service proposal revised')
  await page.getByRole('button', { name: 'Save draft' }).click()
  await expect.poll(() => draftRevision).toBe(2)
  expect(document.title).toBe('Synthetic service proposal revised')
  expect(calls.find(call => call.method === 'PATCH' && call.path.endsWith('/draft'))?.body).toMatchObject({ writer_version: 2 })

  await expect(page.locator('.quote-titlebar .save-state')).toHaveText('Saved')
  await page.getByRole('button', { name: 'Details', exact: true }).click()
  const details = page.getByRole('complementary', { name: 'Details', exact: true })
  await details.getByRole('button', { name: 'Issue quote…' }).click()
  const issue = page.getByRole('dialog', { name: 'Issue A260924-01 as version 1?' })
  await expect(issue).toContainText('Nothing is sent')
  expect(calls.filter(call => call.path.endsWith('/finalize'))).toHaveLength(0)
  await issue.getByRole('button', { name: 'Issue quote', exact: true }).click()
  await expect(details.getByRole('heading', { name: 'Issued', exact: true })).toBeVisible()
  expect(calls.filter(call => call.path.endsWith('/finalize')).map(call => call.body)).toEqual([
    { expected_quote_revision: 1, expected_draft_revision: 2, expected_document_sha256: digest },
  ])
  const customerLink = details.getByRole('textbox', { name: 'Customer link', exact: true })
  await expect(customerLink).toHaveValue(/\/offers\/synthetic\/token$/)
  const linkURL = await customerLink.inputValue()
  expect(calls.some(call => call.path.includes('/email'))).toBe(false)

  let attempts = 0
  const publicRequests: { credentials: string; body: Record<string, unknown> }[] = []
  await page.context().route('**/api/public/quotes/synthetic/token**', async (route: Route) => {
    const request = route.request(), path = new URL(request.url()).pathname
    if (path.endsWith('/accept')) {
      attempts++
      publicRequests.push({ credentials: request.headers().cookie ?? '', body: request.postDataJSON() as Record<string, unknown> })
      if (accepted) return route.fulfill({ status: 409, json: { error: 'already accepted' } })
      accepted = true
      state = 'accepted'; quoteRevision++
      return route.fulfill({ status: 201, json: { accepted_at: '2026-09-24T12:00:00Z', confirmation_state: 'pending' } })
    }
    return route.fulfill({ json: { document, offer_no: 'A260924-01', version: 1, content_sha256: digest,
      state: accepted ? 'accepted' : 'issued', expires_at: publicLink.expires_at, acceptable: !accepted, receipt_ready: accepted } })
  })
  const a = await page.context().newPage(), b = await page.context().newPage()
  try {
    await Promise.all([a.goto(linkURL), b.goto(linkURL)])
    for (const tab of [a, b]) {
      await expect(tab.getByRole('heading', { name: 'Synthetic service proposal revised' }).first()).toBeVisible()
      await tab.getByLabel('Your name').fill('Example Signer')
      await tab.getByLabel('I have reviewed this quote and agree to accept it.').check()
    }
    await a.getByRole('button', { name: 'Accept quote' }).click()
    await expect(a.getByText('This quote has been accepted.')).toBeVisible()
    await b.getByRole('button', { name: 'Accept quote' }).click()
    await expect(b.getByText('This quote can no longer be accepted.')).toBeVisible()
    expect(attempts).toBe(2)
    expect(publicRequests.map(request => request.body.expected_content_sha256)).toEqual([digest, digest])
    expect(publicRequests.every(request => request.credentials === '')).toBe(true)
  } finally { await a.close(); await b.close() }
  await page.reload()
  await expect(page.getByRole('complementary', { name: 'Details', exact: true })).toBeVisible()
  const receipt = page.getByRole('complementary', { name: 'Details', exact: true }).locator('#receipt-title').locator('..').locator('..')
  await expect(receipt).toContainText('aeon-quote-document-1')
  await expect(receipt.getByRole('link', { name: 'Download receipt (PDF)' })).toHaveAttribute('href', `/api/quotes/${quoteId}/versions/1/confirmation/receipt`)
  expect(calls.filter(call => call.method === 'PATCH' && call.path.endsWith('/draft'))).toHaveLength(1)
})
