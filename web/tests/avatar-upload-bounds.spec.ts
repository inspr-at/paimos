// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { makePng, mockSettings, settingsData } from './settings-fixtures'

async function openProfile(page: Page) {
  await mockWork(page, fixtures())
  const data = settingsData()
  await mockSettings(page, data)
  await page.goto('/settings/personal')
  await expect(page.getByLabel('First name')).toHaveValue('Markus')
  return data
}

async function cameraPhoto(page: Page, orientation: number) {
  const base64 = await page.evaluate(() => {
    const canvas = document.createElement('canvas')
    canvas.width = 6000; canvas.height = 4000
    const ctx = canvas.getContext('2d')!
    ctx.fillStyle = '#ff0000'; ctx.fillRect(0, 0, 3000, 4000)
    ctx.fillStyle = '#0000ff'; ctx.fillRect(3000, 0, 3000, 4000)
    const data = canvas.toDataURL('image/jpeg', 0.9).split(',')[1]
    canvas.width = canvas.height = 0
    return data
  })
  const jpeg = Buffer.from(base64, 'base64')
  // Minimal EXIF TIFF with one orientation tag; no fixture files or metadata.
  const exif = Buffer.from('ffe1002245786966000049492a0008000000010012010300010000000100000000000000', 'hex')
  exif.writeUInt16LE(orientation, 28)
  return { name: 'camera.jpg', mimeType: 'image/jpeg', buffer: Buffer.concat([jpeg.subarray(0, 2), exif, jpeg.subarray(2)]) }
}

async function pick(page: Page, file: { name: string; mimeType: string; buffer: Buffer }) {
  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: 'Add a photo', exact: true }).click()
  await (await chooser).setFiles(file)
  await expect(page.getByRole('button', { name: /Save photo/ })).toBeEnabled()
}

type EncodingHold = Window & { finishAvatarEncoding: () => void; avatarEncodingReady: boolean }
async function holdEncoding(page: Page) {
  await page.evaluate(() => {
    const original = HTMLCanvasElement.prototype.toBlob
    HTMLCanvasElement.prototype.toBlob = function (callback, type, quality) {
      original.call(this, blob => {
        const state = window as EncodingHold
        state.finishAvatarEncoding = () => callback(blob)
        state.avatarEncodingReady = true
      }, type, quality)
    }
  })
}
async function finishEncoding(page: Page) {
  await expect.poll(() => page.evaluate(() => (window as EncodingHold).avatarEncodingReady)).toBe(true)
  await page.evaluate(() => (window as EncodingHold).finishAvatarEncoding())
}

async function controlBoxes(page: Page) {
  return page.locator('.crop-dialog').evaluate(dialog =>
    [...dialog.querySelectorAll('.head button, .foot button, .stage, .zoom button, .slider')].map(el => {
      const { x, y, width, height } = el.getBoundingClientRect()
      return { x, y, width, height }
    }))
}

for (const { orientation, viewport, dimensions, corners } of [
  { orientation: 1, viewport: { width: 1280, height: 800 }, dimensions: [2508, 1672], corners: ['red', 'blue'] },
  { orientation: 6, viewport: { width: 390, height: 844 }, dimensions: [1672, 2508], corners: ['red', 'blue'] },
  { orientation: 2, viewport: { width: 1280, height: 800 }, dimensions: [2508, 1672], corners: ['blue', 'red'] },
]) {
  test(`camera photo saves within caps with EXIF ${orientation}; controls stay still at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const data = await openProfile(page)
    const file = await cameraPhoto(page, orientation)
    let finishUpload!: () => void
    const uploadHeld = new Promise<void>(resolve => { finishUpload = resolve })
    let received = false
    // The UI suite uses a fixture API. Inspect the actual multipart image and
    // enforce the server's limits before allowing that API to save the avatar.
    await page.route('**/api/me/avatar', async route => {
      const request = route.request()
      const form = await new Response(new Uint8Array(request.postDataBuffer()!), { headers: { 'content-type': request.headers()['content-type'] } }).formData()
      const upload = form.get('file') as File
      const crop = JSON.parse(form.get('crop') as string)
      const info = await page.evaluate(async bytes => {
        const bitmap = await createImageBitmap(new Blob([new Uint8Array(bytes)]))
        const canvas = document.createElement('canvas')
        canvas.width = bitmap.width; canvas.height = bitmap.height
        const ctx = canvas.getContext('2d')!
        ctx.drawImage(bitmap, 0, 0)
        const color = (x: number, y: number) => {
          const [r, , b] = ctx.getImageData(x, y, 1, 1).data
          return r > b ? 'red' : 'blue'
        }
        const info = { width: bitmap.width, height: bitmap.height, corners: [color(10, 10), color(bitmap.width - 11, bitmap.height - 11)] }
        bitmap.close(); canvas.width = canvas.height = 0
        return info
      }, [...new Uint8Array(await upload.arrayBuffer())])
      expect([info.width, info.height]).toEqual(dimensions)
      expect(info.width * info.height).toBeLessThanOrEqual(4_194_304)
      expect(Math.max(info.width, info.height)).toBeLessThanOrEqual(4096)
      expect(upload.size).toBeLessThanOrEqual(8 << 20)
      expect(crop.size).toBeGreaterThan(0); expect(crop.size).toBeLessThanOrEqual(2048)
      expect(crop.x).toBeGreaterThanOrEqual(0); expect(crop.y).toBeGreaterThanOrEqual(0)
      expect(crop.x + crop.size).toBeLessThanOrEqual(info.width)
      expect(crop.y + crop.size).toBeLessThanOrEqual(info.height)
      expect(info.corners).toEqual(corners)
      received = true
      await uploadHeld
      await route.fallback()
    })
    await pick(page, file)
    const dialog = page.getByRole('dialog', { name: 'Crop your photo' })
    await holdEncoding(page)
    const before = await controlBoxes(page)
    await dialog.getByRole('button', { name: /Save photo/ }).click()
    await expect(dialog.getByRole('status')).toHaveText('Preparing your photo…')
    expect(await controlBoxes(page)).toEqual(before)
    await expect(dialog.getByRole('slider')).toBeDisabled()
    await finishEncoding(page)
    await expect.poll(() => received).toBe(true)
    await expect(dialog.getByRole('progressbar')).toBeVisible()
    expect(await controlBoxes(page)).toEqual(before)
    finishUpload()
    await expect(dialog).toHaveCount(0)
    expect(data.uploads).toHaveLength(1)
    await expect(page.locator('.toast').filter({ hasText: 'Your photo is updated.' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Change your photo' }).locator('img')).toBeVisible()
  })
}

test('stopping preparation never uploads the late encoding result', async ({ page }) => {
  const data = await openProfile(page)
  await pick(page, await cameraPhoto(page, 6))
  await holdEncoding(page)
  const dialog = page.getByRole('dialog', { name: 'Crop your photo' })
  await dialog.getByRole('button', { name: /Save photo/ }).click()
  await expect(dialog.getByRole('status')).toBeVisible()
  await dialog.getByRole('button', { name: /^Stop/ }).click()
  await finishEncoding(page)
  await expect(dialog.getByRole('button', { name: /Save photo/ })).toBeEnabled()
  expect(data.uploads).toHaveLength(0)
  await dialog.getByRole('button', { name: /^Cancel Esc/ }).click()
  await expect(dialog).toHaveCount(0)
})

test('a rejected upload leaves controls in place and names the actual limits', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await openProfile(page)
  await page.route('**/api/me/avatar', route => route.fulfill({ status: 400, json: { error: 'image dimensions exceed limit' } }))
  await pick(page, { name: 'small.png', mimeType: 'image/png', buffer: makePng(1000, 500) })
  const dialog = page.getByRole('dialog', { name: 'Crop your photo' })
  const before = await controlBoxes(page)
  // Modified browser keys remain native; Cmd/Ctrl+Enter submits from the slider.
  const slider = dialog.getByRole('slider')
  await slider.focus()
  await slider.press('ControlOrMeta+Enter')
  await expect(dialog.getByRole('alert')).toHaveText('Use a photo with at most 4,194,304 pixels and 4096 pixels per side, and a square crop at most 2048 pixels per side.')
  expect(await controlBoxes(page)).toEqual(before)
  await slider.focus()
  await slider.press('Escape')
  await expect(dialog.getByRole('application')).toBeFocused()
  await expect(dialog).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
})
