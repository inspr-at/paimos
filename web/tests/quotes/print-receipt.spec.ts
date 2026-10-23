// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import fixture from './fixtures/synthetic-document.json' with { type: 'json' }

test('AEON-1682: emitted multipage receipt preserves Unicode marks, fixed evidence and unclipped A4 text', async ({ page }, testInfo) => {
  const document = structuredClone(fixture) as any
  const paragraph = 'A bold opening and an italic continuation with enough words to wrap across the paper width. '
  const digest = 'a'.repeat(64)
  document.sections = Array.from({ length: 12 }, (_, index) => ({
    id: `11111111-1111-4111-8111-${String(index + 1).padStart(12, '0')}`,
    heading: `Part ${index + 1}`, body: '',
    nodes: [{ id: `22222222-2222-4222-8222-${String(index + 1).padStart(12, '0')}`, kind: 'paragraph',
      text: index === 0 ? 'Bold 😀 and italic' : paragraph.repeat(3),
      marks: index === 0 ? [{ start: 0, end: 4, bold: true }, { start: 5, end: 7, bold: true, italic: true }, { start: 8, end: 11, italic: true }]
        : [{ start: 2, end: 6, bold: true }, { start: 19, end: 25, italic: true }],
    }],
  }))
  await page.route('**/payload', route => route.fulfill({ json: { document, offer_no: 'A260924-01',
    accepted: { name: 'Example Signer', company: 'Example Customer', at: '2026-09-24T12:00:00Z', digest } } }))
  await page.goto('/quote-print.html')
  await expect(page.locator('html')).toHaveAttribute('data-pdf-ready', 'true')
  await page.evaluate(() => document.fonts.ready)
  const prose = page.locator('.quote-page .quote-prose-text').first()
  await expect(prose.locator('strong')).toHaveCount(2)
  await expect(prose.locator('strong em')).toHaveText('😀')
  await expect(prose.locator(':scope > em')).toHaveText('and')
  await expect(page.locator('.quote-page .quote-stamp')).toContainText('Example Signer')
  await expect(page.locator('.quote-page .quote-stamp')).toContainText(digest)
  await expect(page.locator('.quote-final-qr')).toHaveCount(0)
  const pages = await page.locator('.quote-page').count()
  expect(pages).toBeGreaterThan(1)
  const clipped = await page.locator('.quote-page .quote-prose-text').evaluateAll(nodes => nodes.some(node => {
    const text = node.getBoundingClientRect(), sheet = node.closest('.quote-page')!.getBoundingClientRect()
    return text.left < sheet.left - 1 || text.right > sheet.right + 1 || text.bottom > sheet.bottom + 1
  }))
  expect(clipped).toBe(false)
  const pdf = await page.pdf({ format: 'A4', printBackground: true, path: testInfo.outputPath('receipt.pdf') })
  expect(pdf.subarray(0, 5).toString()).toBe('%PDF-')
  expect((pdf.toString('latin1').match(/\/Type\s*\/Page\b/g) ?? []).length).toBe(pages)
})

test('print entry keeps inline marks and accepted receipt evidence', async ({ page }) => {
  const document = structuredClone(fixture)
  document.sections[0]!.nodes[0]!.text = 'Bold 😀 and italic'
  document.sections[0]!.body = 'Bold 😀 and italic'
  Object.assign(document.sections[0]!.nodes[0]!, {
    marks: [
      { start: 0, end: 4, bold: true },
      { start: 5, end: 7, bold: true, italic: true },
      { start: 8, end: 11, italic: true },
    ],
  })
  const digest = 'a'.repeat(64)
  await page.route('**/payload', route => route.fulfill({ json: {
    document, offer_no: 'A260924-01', accepted: { name: 'Example Signer', company: 'Example Customer', at: '2026-09-24T12:00:00Z', digest },
  } }))
  await page.goto('/quote-print.html')
  await expect(page.locator('html')).toHaveAttribute('data-pdf-ready', 'true')
  const prose = page.locator('.quote-page .quote-prose-text').first()
  await expect(prose.locator('strong')).toHaveCount(2)
  await expect(prose.locator('strong em')).toHaveText('😀')
  await expect(prose.locator(':scope > em')).toHaveText('and')
  await expect(page.locator('.quote-page .quote-stamp')).toContainText('Example Signer')
  await expect(page.locator('.quote-page .quote-stamp')).toContainText(digest)
  await expect(page.locator('.quote-page .quote-final-qr')).toHaveCount(0)
})

test('the final printed page QR decodes to the exact customer URL', async ({ page }) => {
  const publicURL = 'https://example.invalid/offers/synthetic/a-recoverable-capability-token'
  await page.route('**/payload', route => route.fulfill({ json: { document: fixture, offer_no: 'A260924-01', public_url: publicURL } }))
  await page.goto('/quote-print.html')
  await expect(page.locator('html')).toHaveAttribute('data-pdf-ready', 'true')
  const qr = page.locator('.quote-page').last().locator('.quote-final-qr')
  await expect(qr).toBeVisible()
  await expect(page.locator('.quote-final-qr')).toHaveCount(1)
  const decoded = await page.evaluate(async () => {
    const { decodeQuoteQr } = await import('/tests/quotes/qr-decode-harness.ts')
    return decodeQuoteQr('.quote-page .quote-final-qr')
  })
  expect(decoded).toBe(publicURL)
})

test('the emitted A4 PDF wraps long marked prose across pages without clipping', async ({ page }) => {
  const document = structuredClone(fixture) as any
  const paragraph = 'A bold opening and an italic continuation with enough words to wrap across the paper width. '
  document.sections = Array.from({ length: 12 }, (_, index) => ({
    id: `11111111-1111-4111-8111-${String(index + 1).padStart(12, '0')}`,
    heading: `Part ${index + 1}`, body: '',
    nodes: [{
      id: `22222222-2222-4222-8222-${String(index + 1).padStart(12, '0')}`,
      kind: 'paragraph', text: paragraph.repeat(3),
      marks: [{ start: 2, end: 6, bold: true }, { start: 19, end: 25, italic: true }],
    }],
  }))
  await page.route('**/payload', route => route.fulfill({ json: { document, offer_no: 'A260924-01' } }))
  await page.goto('/quote-print.html')
  await expect(page.locator('html')).toHaveAttribute('data-pdf-ready', 'true')
  expect(await page.locator('.quote-page').count()).toBeGreaterThan(1)
  const clipped = await page.locator('.quote-page .quote-prose-text').evaluateAll(nodes => nodes.some(node => {
    const text = node.getBoundingClientRect(), sheet = node.closest('.quote-page')!.getBoundingClientRect()
    return text.right > sheet.right + 1 || text.bottom > sheet.bottom + 1
  }))
  expect(clipped).toBe(false)
  await expect(page.locator('.quote-page strong').first()).toBeVisible()
  await expect(page.locator('.quote-page em').first()).toBeVisible()
  const pdf = await page.pdf({ format: 'A4', printBackground: true })
  expect(pdf.subarray(0, 5).toString()).toBe('%PDF-')
  expect((pdf.toString('latin1').match(/\/Type\s*\/Page\b/g) ?? []).length).toBeGreaterThan(1)
})

// Temporary same-host worker timing; removed before handoff.
import { writeFileSync as writeCutATiming } from 'node:fs'
import { hostname as cutAHost } from 'node:os'
let cutAStarted = 0
test.beforeEach(() => { cutAStarted = performance.now() })
test.afterEach(async ({}, testInfo) => {
  writeCutATiming(testInfo.outputPath('p10-cutA-timing.md'), JSON.stringify({
    host: cutAHost(), title: testInfo.title, seconds: (performance.now() - cutAStarted) / 1000,
    status: testInfo.status, retry: testInfo.retry,
  }))
})
