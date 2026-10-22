// SPDX-License-Identifier: AGPL-3.0-only
// Attachments on a ticket: the strip, drop and paste, reorder, captions, delete
// with undo, inline Markdown images and the full-screen viewer with compare.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors, type Call } from './work-fixtures'
import { expectStableControls } from './helpers/stable'

const panel = (page: Page) => page.getByRole('complementary', { name: 'Ticket details' })
const strip = (page: Page) => panel(page).getByRole('region', { name: /^Attachments/ })
const tiles = (page: Page) => strip(page).locator('.tile[data-attachment-id]')
const viewer = (page: Page) => page.locator('dialog.lightbox')
const writes = (calls: Call[], method: string, prefix: string) => calls.filter(c => c.method === method && c.path.startsWith(prefix))

async function open(page: Page, width = 1440) {
  await page.setViewportSize({ width, height: 900 })
  const data = fixtures()
  const calls = await mockWork(page, data)
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(panel(page).getByRole('heading', { name: 'Connect Hetzner Cloud for managed provisioning' })).toBeVisible()
  await expect(tiles(page)).toHaveCount(4)
  return { data, calls }
}
async function dropFiles(page: Page, files: { name: string; type: string }[]) {
  await panel(page).evaluate((el, files) => {
    const dt = new DataTransfer()
    for (const f of files) dt.items.add(new File([new Uint8Array([137, 80, 78, 71])], f.name, { type: f.type }))
    for (const type of ['dragenter', 'dragover', 'drop']) el.dispatchEvent(new DragEvent(type, { dataTransfer: dt, bubbles: true, cancelable: true }))
  }, files)
}

test('the strip shows a count, thumbnails and file cards', async ({ page }) => {
  const errors = watchErrors(page)
  await open(page)
  await expect(strip(page).locator('.count')).toHaveText('4')
  await expect(tiles(page).nth(3)).toContainText('PDF')
  await expect(tiles(page).nth(3)).toContainText('provider-notes.pdf')
  await expect(tiles(page).first().locator('img')).toHaveAttribute('src', '/api/attachments/att-1/content?variant=thumb')
  expect(errors).toEqual([])
})

test('dropping files anywhere on the ticket uploads them with a drop overlay', async ({ page }) => {
  const { calls } = await open(page)
  await panel(page).evaluate(el => {
    const dt = new DataTransfer(); dt.items.add(new File(['x'], 'a.png', { type: 'image/png' }))
    el.dispatchEvent(new DragEvent('dragenter', { dataTransfer: dt, bubbles: true, cancelable: true }))
  })
  await expect(panel(page).locator('.drop-overlay')).toContainText('Drop to attach to PHAROS-11')
  await dropFiles(page, [{ name: 'Screen A.png', type: 'image/png' }, { name: 'notes.txt', type: 'text/plain' }])
  await expect(panel(page).locator('.drop-overlay')).toHaveCount(0)
  await expect(tiles(page)).toHaveCount(6)
  await expect(strip(page).locator('.count')).toHaveText('6')
  const posts = writes(calls, 'POST', '/api/nodes/n-1/attachments')
  expect(posts).toHaveLength(2)
  expect(String(posts[0].body)).toContain('name="file"; filename="Screen A.png"')
})

test('pasting a screenshot uploads it with a readable name', async ({ page }) => {
  await open(page)
  await panel(page).focus()
  await panel(page).evaluate(el => {
    const dt = new DataTransfer(); dt.items.add(new File([new Uint8Array([1, 2, 3])], 'image.png', { type: 'image/png' }))
    el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }))
  })
  await expect(tiles(page)).toHaveCount(5)
  await tiles(page).last().click()
  await expect(viewer(page).locator('.name')).toHaveText(/^Screenshot \d{4}-\d{2}-\d{2} \d{2}\.\d{2}\.png$/)
})

test('a failed upload says why and can be retried or removed', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures(), { failUpload: true })
  await page.goto('/p/PHAROS/PHAROS-11')
  await expect(tiles(page)).toHaveCount(4)
  await dropFiles(page, [{ name: 'huge.png', type: 'image/png' }])
  const failed = strip(page).locator('.item.failed')
  await expect(failed.getByRole('status')).toHaveAttribute('aria-label', 'huge.png did not upload: The file is too large')
  await failed.getByRole('button', { name: 'Remove' }).click()
  await expect(strip(page).locator('.item.failed')).toHaveCount(0)
})

test('delete reaches the server at once; Undo in the toast undoes it on the server', async ({ page }) => {
  const { calls, data } = await open(page)
  let release!: () => void
  const pending = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/events/1/undo', async route => { await pending; await route.fallback() })
  await tiles(page).first().hover()
  await strip(page).getByRole('button', { name: 'Delete fleet-list-before.png' }).click()
  await expect(tiles(page)).toHaveCount(3)
  await expect.poll(() => writes(calls, 'DELETE', '/api/attachments/').length).toBe(1)
  expect(writes(calls, 'DELETE', '/api/attachments/')[0].path).toBe('/api/attachments/att-1')
  const toast = page.locator('.toast').filter({ hasText: 'Deleted fleet-list-before.png' })
  await toast.getByRole('button', { name: 'Undo' }).click()
  await expect(tiles(page)).toHaveCount(4)
  await expect(tiles(page).first()).toHaveAttribute('data-attachment-id', 'att-1')
  expect(data.attachments['n-1'].map(a => a.id)).not.toContain('att-1')
  release()
  await expect.poll(() => writes(calls, 'POST', '/api/events/').map(c => c.path)).toEqual(['/api/events/1/undo'])
  expect(calls.filter(c => c.path === '/api/events')).toEqual([])
  await expect.poll(() => data.attachments['n-1'].map(a => a.id)).toContain('att-1')
  // From the keyboard, and without Undo: the delete stands.
  await tiles(page).nth(2).focus()
  await page.keyboard.press('Delete')
  await expect(tiles(page)).toHaveCount(3)
  await expect.poll(() => writes(calls, 'DELETE', '/api/attachments/').length).toBe(2)
  expect(writes(calls, 'DELETE', '/api/attachments/')[1].path).toBe('/api/attachments/att-3')
  await expect(page.locator('.toast').filter({ hasText: 'Deleted phone.png' })).toBeHidden({ timeout: 9000 })
  expect(writes(calls, 'POST', '/api/events/')).toHaveLength(1)
})

test('AEON-1416: pending attachment Undo stays in place and failed Undo shows plain copy', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  for (const width of [390, 1024, 1440]) for (const theme of ['light', 'dark']) {
    const { data } = await open(page, width)
    await page.evaluate(mode => { document.documentElement.dataset.theme = mode }, theme)
    let release!: () => void
    const pending = new Promise<void>(resolve => { release = resolve })
    await page.route('**/api/events/1/undo', async route => {
      await pending
      await route.fulfill({ status: 500, json: { error: 'internal receipt diagnostic' } })
    })
    const target = strip(page).locator('[data-attachment-id="att-1"]')
    const add = strip(page).getByRole('button', { name: 'Add', exact: true })
    await expectStableControls({
      controls: { add, restoredAttachment: target }, scrollAreas: { strip: strip(page) },
      interactions: [{ name: 'delete and immediately undo', run: async () => {
        await target.focus()
        const deletion = page.waitForResponse(response => response.request().method() === 'DELETE')
        await page.keyboard.press('Delete')
        await deletion
        await expect(target).toHaveCount(0)
        const request = page.waitForRequest('**/api/events/1/undo')
        await page.getByRole('button', { name: 'Undo', exact: true }).click()
        await request
        await expect(target).toBeVisible()
        expect(data.attachments['n-1'].some(item => item.id === 'att-1')).toBe(false)
        await page.screenshot({ path: testInfo.outputPath(`attachment-${width}-${theme}-pending.png`) })
      } }],
    })
    await expectStableControls({
      controls: { add }, scrollAreas: { strip: strip(page) },
      interactions: [{ name: 'failed Undo', run: async () => {
        release()
        await expect(target).toHaveCount(0)
        const error = page.locator('.toast.error')
        await expect(error.locator('span')).toHaveText('fleet-list-before.png could not be restored.')
        await expect(error).not.toContainText(/receipt|diagnostic|unknown error/i)
        await page.screenshot({ path: testInfo.outputPath(`attachment-${width}-${theme}-failed.png`) })
      } }],
    })
    await page.unroute('**/api/events/1/undo')
  }
})

test('Alt+arrows reorder with the attachment precondition', async ({ page }) => {
  const { calls, data } = await open(page)
  await tiles(page).first().focus()
  await page.keyboard.press('Alt+ArrowRight')
  await expect(tiles(page).nth(1)).toHaveAttribute('data-attachment-id', 'att-1')
  await expect(tiles(page).nth(1)).toBeFocused()
  const patch = writes(calls, 'PATCH', '/api/attachments/att-1')[0]
  expect(Object.keys(patch.body as object)).toEqual(['position'])
  expect(Number((patch.body as { position: string }).position)).toBeGreaterThan(2048)
  expect(Number((patch.body as { position: string }).position)).toBeLessThan(3072)
  expect(patch.headers['if-unmodified-since']).toBe(data.attachments['n-1'].find(a => a.id === 'att-1')!.created_at)
})

test('the viewer: fit, 100%, zoom, arrows through all attachments, details and caption', async ({ page }) => {
  const { calls } = await open(page)
  await tiles(page).nth(1).click()
  const box = viewer(page)
  await expect(box).toBeVisible()
  await expect(box.locator('.name')).toHaveText('After: compact list')
  await expect(box.locator('.meta')).toContainText('1440 × 900')
  await expect(box.getByRole('button', { name: 'Fit' })).toHaveAttribute('aria-pressed', 'true')
  await expect(box.locator('img.photo')).toHaveAttribute('src', /variant=preview/)
  await page.keyboard.press('1')
  await expect(box.locator('.percent')).toHaveText('100%')
  // The original only at 100%.
  await expect(box.locator('img.photo')).toHaveAttribute('src', /variant=original/)
  await page.keyboard.press('+')
  await expect(box.locator('.percent')).toHaveText('125%')
  await page.keyboard.press('0')
  await expect(box.getByRole('button', { name: 'Fit' })).toHaveAttribute('aria-pressed', 'true')
  await page.keyboard.press('ArrowRight')
  await expect(box.locator('.name')).toHaveText('phone.png')
  await page.keyboard.press('ArrowRight')
  await expect(box.locator('.file-stage')).toContainText('provider-notes.pdf')
  await expect(box.getByRole('link', { name: 'Download', exact: true })).toHaveAttribute('href', '/api/attachments/att-4/content?variant=original')
  await page.keyboard.press('ArrowRight')
  await expect(box.locator('.name')).toHaveText('Before: card grid')
  await expect(box.getByRole('list', { name: 'All attachments' }).getByRole('button')).toHaveCount(4)
  await page.keyboard.press('d')
  const caption = box.getByRole('textbox', { name: 'Caption' })
  await caption.fill('Before: the card grid')
  await caption.press('Enter')
  await expect.poll(() => writes(calls, 'PATCH', '/api/attachments/att-1').length).toBe(1)
  expect(writes(calls, 'PATCH', '/api/attachments/att-1')[0].body).toEqual({ caption: 'Before: the card grid' })
  await page.keyboard.press('Escape')
  await page.keyboard.press('Escape')
  await expect(box).toBeHidden()
  // Focus returns to the thumbnail that opened the viewer.
  await expect(tiles(page).nth(1)).toBeFocused()
})

test('compare two screens: slider, side by side and onion skin; pick B in the strip', async ({ page }) => {
  await open(page)
  await tiles(page).first().click()
  const box = viewer(page)
  await page.keyboard.press('c')
  await expect(box.getByRole('radio', { name: 'Slider' })).toHaveAttribute('aria-checked', 'true')
  const handle = box.getByRole('slider', { name: 'Compare divide' })
  await expect(handle).toHaveAttribute('aria-valuenow', '50')
  await handle.focus()
  await page.keyboard.press('ArrowLeft')
  await expect(handle).toHaveAttribute('aria-valuenow', '45')
  await expect(box.locator('img.over')).toHaveAttribute('style', /clip-path: inset\(0px 0px 0px 45%\)/)
  await box.getByRole('radio', { name: 'Onion skin' }).click()
  await expect(box.getByLabel('Overlay opacity')).toBeVisible()
  await box.getByRole('radio', { name: 'Side by side' }).click()
  await expect(box.locator('.side')).toHaveCount(2)
  await box.getByRole('button', { name: 'Compare with phone.png' }).click()
  await expect(box.locator('.side figcaption').nth(1)).toContainText('phone.png')
  await page.keyboard.press('c')
  await expect(box.locator('.side')).toHaveCount(0)
})

test('![caption](attachment:<id>) in Markdown shows the image inline and opens the viewer', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const data = fixtures()
  data.nodes.find(n => n.id === 'n-1')!.body = 'See the new list:\n\n![After](attachment:att-2)\n\n![Elsewhere](https://example.com/x.png)'
  await mockWork(page, data)
  await page.goto('/p/PHAROS/PHAROS-11')
  const inline = panel(page).locator('.md-attachment')
  await expect(inline).toHaveCount(1)
  await expect(inline.locator('img')).toHaveAttribute('src', '/api/attachments/att-2/content?variant=preview')
  await expect(panel(page).locator('.markdown-body').first()).toContainText('![Elsewhere](https://example.com/x.png)')
  await inline.click()
  await expect(viewer(page).locator('.name')).toHaveText('After: compact list')
})

test('pasting an image into the editor attaches it and inserts the reference', async ({ page }) => {
  await open(page)
  await page.keyboard.press('e')
  const area = panel(page).getByLabel('Description, Markdown')
  await area.fill('Look: ')
  await area.evaluate(el => {
    const dt = new DataTransfer(); dt.items.add(new File([new Uint8Array([1])], 'design.png', { type: 'image/png' }))
    el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }))
  })
  await expect(area).toHaveValue(/^Look: !\[design\.png\]\(attachment:att-new-\d+-0\)$/)
})

test('wide panels show the attachments as a gallery with captions in the context column', async ({ page }) => {
  await open(page, 1920)
  const context = panel(page).getByRole('complementary', { name: 'Attachments, relations and activity' })
  await expect(context.locator('.attachments.gallery')).toBeVisible()
  await expect(context.locator('.caption').first()).toHaveText('Before: card grid')
  await context.locator('.caption').nth(2).click()
  const input = context.getByRole('textbox', { name: 'Caption' })
  await input.fill('Phone')
  await input.press('Enter')
  await expect(context.locator('.caption').nth(2)).toHaveText('Phone')
})

test('on a phone the viewer fits the image, keeps Close in reach and swipes between attachments', async ({ page }) => {
  await open(page, 390)
  await tiles(page).first().click()
  const box = viewer(page)
  await expect(box.locator('.name')).toHaveText('Before: card grid')
  await expect(box.getByRole('button', { name: 'Close viewer' })).toBeInViewport({ ratio: 1 })
  await expect(box.getByRole('button', { name: 'Details' })).toBeInViewport({ ratio: 1 })
  await expect(box.locator('img.photo')).toHaveClass(/ready/)
  const image = (await box.locator('img.photo').boundingBox())!
  expect(image.x).toBeGreaterThanOrEqual(0)
  expect(image.x + image.width).toBeLessThanOrEqual(390)
  const stage = (await box.locator('.stage').boundingBox())!
  const y = stage.y + stage.height / 3
  await page.mouse.move(stage.x + 300, y)
  await page.mouse.down()
  await page.mouse.move(stage.x + 110, y, { steps: 6 })
  await page.mouse.up()
  await expect(box.locator('.name')).toHaveText('After: compact list')
})
