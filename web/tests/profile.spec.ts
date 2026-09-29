// SPDX-License-Identifier: AGPL-3.0-only
// The personal profile: fields that save one at a time with Undo, the server's
// objections under their field, the photo (pick, paste, crop by keyboard, upload,
// remove), avatars across the app, and the greeting on Projects.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { makePng, mockSettings, settingsData, type SettingsMockOptions } from './settings-fixtures'

const MIRA = '22222222-2222-4222-8222-222222222222'
async function setup(page: Page, options: SettingsMockOptions = {}) {
  await mockWork(page, fixtures())
  const data = settingsData(options)
  await mockSettings(page, data, options)
  return data
}
async function openPersonal(page: Page) {
  await page.goto('/settings/personal')
  await expect(page.getByLabel('First name')).toHaveValue('Markus')
}
async function pickPhoto(page: Page, file: { name: string; mimeType: string; buffer: Buffer }) {
  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: /^(Add a photo|Change your photo)$/ }).click()
  await (await chooser).setFiles(file)
}
const crop = (page: Page) => page.getByRole('dialog', { name: 'Crop your photo' })

test('fields save one at a time, with Undo in the toast', async ({ page }) => {
  const errors = watchErrors(page)
  const data = await setup(page)
  await openPersonal(page)
  await expect(page.getByText('markus@barta.com')).toBeVisible()
  await expect(page.getByText('Managed by sign-in')).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Email' })).toHaveCount(0)

  const preferred = page.getByLabel('What should we call you?')
  // Empty, it shows what the greeting will use.
  await expect(preferred).toHaveAttribute('placeholder', 'Markus')
  await expect(page.locator('#profile-preferred_name-note')).toHaveText('Empty, so the greeting says Markus.')
  await preferred.fill('  Max ')
  await preferred.press('Enter')
  await expect.poll(() => data.patches).toEqual([{ preferred_name: 'Max' }])
  await expect(preferred).toHaveValue('Max')
  const toast = page.locator('.toast').filter({ hasText: 'What we call you saved.' })
  await expect(toast).toBeVisible()
  await toast.getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => data.patches.at(-1)).toEqual({ preferred_name: '' })
  await expect(preferred).toHaveValue('')

  // Initials: derived until overridden.
  const initials = page.getByLabel('Initials')
  await expect(initials).toHaveAttribute('placeholder', 'MB')
  await expect(page.getByText('Derived from your name: MB')).toBeVisible()
  await initials.fill('MXB')
  await initials.blur()
  await expect.poll(() => data.patches.at(-1)).toEqual({ initials: 'MXB' })
  await expect(page.getByRole('button', { name: /^Account for / }).locator('.letters')).toHaveText('MXB')
  expect(errors).toEqual([])
})

test('the handle is checked as you type, and a taken one says so under the field', async ({ page }) => {
  const data = await setup(page, { taken: ['mira'] })
  await openPersonal(page)
  const handle = page.getByLabel('Handle')
  await handle.fill('M')
  await expect(handle).toHaveValue('m')
  await expect(page.getByText('At least 2 characters.')).toBeVisible()
  await handle.fill('mi ra!')
  await expect(page.getByText('Only letters, digits, dots, dashes and underscores.')).toBeVisible()
  await handle.press('Enter')
  expect(data.patches).toEqual([])
  await handle.fill('mira')
  await handle.press('Enter')
  await expect(page.getByText('Someone in this workspace already uses this handle.')).toBeVisible()
  await expect(handle).toHaveAttribute('aria-invalid', 'true')
  await handle.fill('markus.b')
  await handle.press('Enter')
  await expect.poll(() => data.patches.at(-1)).toEqual({ short_name: 'markus.b' })
  await expect(page.getByText('Someone in this workspace already uses this handle.')).toHaveCount(0)
})

test('the server’s objections show under the field they are about', async ({ page }) => {
  await setup(page, { reject: { last_name: 'must be a trimmed string of at most 100 characters' } })
  await openPersonal(page)
  await page.getByLabel('Last name').fill('Barta-Holm')
  await page.getByLabel('Last name').press('Enter')
  await expect(page.locator('#profile-last_name-note')).toHaveText(/Use up to 100 characters, without spaces at either end\./)
  await expect(page.getByLabel('Last name')).toHaveAttribute('aria-invalid', 'true')
})

test.describe('time zone and language', () => {
  test.use({ timezoneId: 'America/New_York' })
  test('a searchable time zone, Detect from this device, and a language with its date preview', async ({ page }) => {
    const data = await setup(page)
    await openPersonal(page)
    await page.getByRole('button', { name: /^Time zone/ }).click()
    await expect(page.getByRole('searchbox', { name: 'Search time zones' })).toBeFocused()
    await page.keyboard.type('tokyo')
    await page.getByRole('option', { name: /Tokyo/ }).click()
    await expect.poll(() => data.patches.at(-1)).toEqual({ timezone: 'Asia/Tokyo' })
    await expect(page.getByRole('button', { name: /^Time zone Tokyo/ })).toBeVisible()
    await page.getByRole('button', { name: 'Detect' }).click()
    await expect.poll(() => data.patches.at(-1)).toEqual({ timezone: 'America/New_York' })
    await expect(page.getByRole('button', { name: 'Detect' })).toHaveCount(0)

    await expect(page.locator('.note.preview')).toContainText('Dates read')
    await page.getByRole('button', { name: /^Language and region/ }).click()
    await expect(page.getByRole('searchbox', { name: 'Search languages' })).toBeFocused()
    await page.keyboard.type('american')
    await page.keyboard.press('Enter')
    await expect.poll(() => data.patches.at(-1)).toEqual({ locale: 'en-US' })
    await expect(page.locator('.note.preview')).toContainText('weeks start on Sunday')
  })
})

test('a photo: pick it, crop it with the keyboard, see it everywhere', async ({ page }) => {
  const data = await setup(page)
  await openPersonal(page)
  await pickPhoto(page, { name: 'portrait.png', mimeType: 'image/png', buffer: makePng(1000, 500) })
  await expect(crop(page)).toBeVisible()
  const stage = crop(page).getByRole('application', { name: 'Photo position' })
  await expect(stage).toBeFocused()
  await expect(crop(page).getByRole('group', { name: 'Previews' }).locator('img')).toHaveCount(3)
  // Centred, the crop is the middle square; + zooms, arrows move.
  await page.keyboard.press('+'); await page.keyboard.press('+')
  await page.keyboard.press('ArrowLeft'); await page.keyboard.press('Shift+ArrowUp')
  await expect(crop(page).getByRole('slider', { name: 'Zoom' })).toHaveValue(/^1\.2/)
  await page.keyboard.press('Enter')
  await expect(crop(page)).toHaveCount(0)
  expect(data.uploads).toHaveLength(1)
  const c = data.uploads[0].crop
  expect(c.size).toBeLessThan(500)
  expect(c.x).toBeLessThan(250 + (500 - c.size) / 2) // moved left of centre
  expect(c.x + c.size).toBeLessThanOrEqual(1000); expect(c.y + c.size).toBeLessThanOrEqual(500)
  await expect(page.locator('.toast').filter({ hasText: 'Your photo is updated.' })).toBeVisible()
  // The picture, versioned by its hash, in the header and on the profile.
  // The header avatar sits inside a round button (AEON-312): the small picture, the 64 for sharp screens.
  await expect(page.getByRole('button', { name: /^Account for / }).locator('img')).toHaveAttribute('srcset', /\/api\/people\/11111111-1111-4111-8111-111111111111\/avatar\/64\?v=/)
  await expect(page.getByRole('button', { name: 'Change your photo' }).locator('img')).toBeVisible()
})

test('paste a copied image to crop it', async ({ page }) => {
  await setup(page)
  await openPersonal(page)
  await page.locator('#profile').click({ position: { x: 5, y: 5 } })
  await page.evaluate(async bytes => {
    const file = new File([new Uint8Array(bytes)], 'pasted.png', { type: 'image/png' })
    const data = new DataTransfer(); data.items.add(file)
    window.dispatchEvent(new ClipboardEvent('paste', { clipboardData: data, bubbles: true }))
  }, [...makePng(300, 300)])
  await expect(crop(page)).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(crop(page)).toHaveCount(0)
})

test('the wrong kind of file, or one too large, says so in plain words', async ({ page }) => {
  await setup(page)
  await openPersonal(page)
  await pickPhoto(page, { name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('hello') })
  await expect(page.getByText('Use a PNG, JPEG or WebP image. This file is not one of those.')).toBeVisible()
  await pickPhoto(page, { name: 'huge.png', mimeType: 'image/png', buffer: Buffer.alloc(9 * 1024 * 1024) })
  await expect(page.getByText('This photo is 9.0 MB; a photo can be up to 8 MB.')).toBeVisible()
  await expect(crop(page)).toHaveCount(0)
})

test('removing the photo falls back to initials on the person’s colour', async ({ page }) => {
  const data = await setup(page, { photo: true })
  await openPersonal(page)
  const photo = page.getByRole('button', { name: 'Change your photo' })
  await expect(photo.locator('img')).toBeVisible()
  await page.getByRole('button', { name: 'Remove' }).click()
  await page.getByRole('button', { name: 'Remove photo' }).click()
  await expect(page.getByRole('button', { name: 'Add a photo' }).locator('img')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Add a photo' }).locator('.letters')).toHaveText('MB')
  await expect(page.getByRole('button', { name: 'Add a photo' }).locator('.avatar')).toHaveClass(/c-teal|c-/)
  expect(data.removals).toBe(1)
})

test('people show their picture where there is one, else their initials', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockSettings(page, settingsData({ photo: true }), { photo: true, people: [] })
  await page.goto('/p/PHAROS')
  await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
  // PHAROS-11 is mine (a picture); PHAROS-13 is Mira's (none: initials, asked once).
  const mine = page.locator('#row-n-1 .avatar')
  const mira = page.locator('#row-n-3 .avatar')
  await expect(mine.locator('img')).toBeVisible()
  await expect(mira.locator('.letters')).toHaveText('MH')
  await expect(mira.locator('img')).toHaveCount(0)
  void me; void MIRA
})

test('the greeting sits above Projects, drawn once with the time zone, and opens the profile', async ({ page }) => {
  const data = await setup(page)
  await page.goto('/')
  const welcome = page.getByRole('region', { name: 'Welcome' })
  await expect(welcome).toContainText('Good afternoon, Markus')
  await expect(welcome).toContainText('Small steps, shipped, beat big plans on paper.')
  expect(data.greetings).toHaveLength(1)
  expect(data.greetings[0].timezone).toBeTruthy()
  // The project list stays above the fold at 1280x800.
  await page.setViewportSize({ width: 1280, height: 800 })
  const firstRow = (await page.getByRole('list', { name: 'Projects' }).getByRole('listitem').first().boundingBox())!
  expect(firstRow.y + firstRow.height).toBeLessThan(800 - 40)
  // Back and forth inside the app: still one draw per page load.
  await page.getByRole('button', { name: /^Account for / }).click()
  await page.getByRole('menuitem', { name: 'Personal settings' }).click()
  await expect(page).toHaveURL('/settings/personal')
  await page.getByRole('navigation', { name: 'Places' }).getByRole('link', { name: 'Projects' }).click()
  await expect(welcome).toContainText('Good afternoon, Markus')
  expect(data.greetings).toHaveLength(1)
  await welcome.getByRole('link', { name: /^Your profile/ }).click()
  await expect(page).toHaveURL('/settings/personal#profile')
})

test('with the greeting off there is no block and no draw', async ({ page }) => {
  const data = await setup(page, { greeting: false })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Welcome' })).toHaveCount(0)
  await page.waitForTimeout(300)
  expect(data.greetings).toHaveLength(0)
})

test('at 390 nothing in the profile is cut', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page, { photo: true })
  await openPersonal(page)
  const cut = await page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.settings-page *')].filter(el => {
    const r = el.getBoundingClientRect(), c = getComputedStyle(el)
    if (r.width <= 1 || r.height <= 1 || c.display === 'none' || el.closest('svg')) return false
    return r.left < -0.5 || r.right > innerWidth + 0.5 || ((c.overflowX !== 'visible' || c.textOverflow === 'ellipsis') && el.scrollWidth > el.clientWidth + 1)
  }).map(el => `${el.tagName}.${el.className}`))
  expect(cut).toEqual([])
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: profile, crop dialog and greeting in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
    await setup(page, { photo: true })
    const check = async () => {
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
      const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')} — ${n.failureSummary?.split('\n').slice(1, 2).join(' ').trim()}`).join('\n')}`)
      expect(summary, summary.join('\n')).toEqual([])
    }
    await page.goto('/')
    await expect(page.getByRole('region', { name: 'Welcome' })).toContainText('Good afternoon')
    await page.waitForTimeout(300)
    await check()
    await openPersonal(page)
    await page.waitForTimeout(300)
    await check()
    await pickPhoto(page, { name: 'portrait.png', mimeType: 'image/png', buffer: makePng(600, 600) })
    await expect(crop(page).getByRole('application', { name: 'Photo position' })).toBeFocused()
    await check()
  })
}
