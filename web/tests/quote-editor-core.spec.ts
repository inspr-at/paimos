// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockQuoteEditor, NODES, QUOTE_ID, quoteDocument } from './quote-inspector-fixtures'

test('AEON-1682: native prose edit, measurement history and CAS save survive a reload with stable IDs', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  const document = quoteDocument()
  Object.assign(document.sections[1]!.nodes[2]!, { marker_x_mm: '3', text_start_mm: '1.5' })
  const { draft } = await mockQuoteEditor(page, document)
  const writes: { revision: string; document: typeof document }[] = []
  page.on('request', request => {
    if (new URL(request.url()).pathname === `/api/quotes/${QUOTE_ID}/draft` && request.method() === 'PATCH')
      writes.push({ revision: request.headers()['if-match']!, document: request.postDataJSON().document })
  })
  await page.goto(`/business/quotes/${QUOTE_ID}`)
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  const paragraph = page.locator(`.quote-page [data-text-id="${NODES[0]}"]`)
  await paragraph.click()
  await paragraph.evaluate(element => {
    const range = document.createRange()
    range.selectNodeContents(element); range.collapse(false)
    const selection = getSelection()!
    selection.removeAllRanges(); selection.addRange(range)
  })
  await page.keyboard.type(' Revised.')
  await expect(paragraph).toContainText('Revised.')
  await expect.poll(() => draft.document.sections[0]!.nodes[0]!.text).toBe(`${document.sections[0]!.nodes[0]!.text} Revised.`)
  const item = page.locator(`.quote-page [data-text-id="${NODES[4]}"]`)
  await item.click()
  const marker = page.getByRole('complementary', { name: 'Format' }).getByRole('spinbutton', { name: 'Marker across' })
  await expect(marker).toHaveValue('3')
  await marker.fill('3.5'); await marker.press('Enter')
  await expect.poll(() => (draft.document.sections[1]!.nodes[2] as { marker_x_mm?: string }).marker_x_mm).toBe('3.5')
  await page.getByRole('button', { name: 'Undo', exact: true }).click()
  await expect(marker).toHaveValue('3')
  await page.getByRole('button', { name: 'Redo', exact: true }).click()
  await expect(marker).toHaveValue('3.5')
  await expect(page.locator('.quote-titlebar .save-state')).toHaveText('Saved')
  expect(writes.length).toBeGreaterThan(0)
  expect(writes.map(write => write.revision)).toEqual(writes.map((_write, i) => `"qd-${i + 1}"`))
  const saved = structuredClone(draft.document)
  expect(saved.sections.flatMap(section => [section.id, ...section.nodes.map(node => node.id)]))
    .toEqual(document.sections.flatMap(section => [section.id, ...section.nodes.map(node => node.id)]))
  expect(saved.positions.map(position => position.unit_price_cents)).toEqual(document.positions.map(position => position.unit_price_cents))
  await page.reload()
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  await expect(paragraph).toHaveText(saved.sections[0]!.nodes[0]!.text)
  expect(draft.document).toEqual(saved)
  await item.click()
  await expect(marker).toHaveValue('3.5')
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
