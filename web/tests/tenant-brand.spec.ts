// SPDX-License-Identifier: AGPL-3.0-only
// AEON-431: a workspace's own logo and short name in the header, set in
// Settings → Workspace → Brand; the product's name and the release codename
// move to the footer's left edge, which opens the running release's history.
//
// BRAND_SHOTS=<dir> also writes the header, footer and settings card at 1600 and
// 390, light and dark, with a square and a wide logo, for review by eye.
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { businessData, mockBusiness } from './business-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'
import type { SVGCleanup } from '../src/lib/tenantBrand'

// A square mark and a wide lockup in dark ink (so dark mode needs its plate),
// plus a light-ink version of the wide one for dark mode.
const SQUARE = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#c2410c"/><path d="M18 46 32 16l14 30h-7l-2.6-6H27.6L25 46Zm12-12h4l-2-4.6Z" fill="#fff"/></svg>`
const wide = (ink: string) => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 184 60"><circle cx="30" cy="30" r="22" fill="#2563eb"/><path d="M20 38V22l20 16V22" stroke="#fff" stroke-width="5" fill="none" stroke-linejoin="round"/><path d="M66 18h8l12 18V18h7v24h-7L74 24v18h-8Zm36 0h7v24h-7Zm15 0h18c5 0 8 3 8 7.4 0 3.5-2 6-5 6.8L147 42h-8l-5.6-9.4H124V42h-7Zm7 6v3.2h10.5c1 0 1.6-.6 1.6-1.6s-.6-1.6-1.6-1.6Zm28-6h8.5l7.5 15.5L169.5 18h8.5l-12 24h-6Z" fill="${ink}"/></svg>`
const WIDE = wide('#1d2b3a')
const WIDE_DARK = wide('#f1f5f9')

type Variant = 'light' | 'dark'
interface Stored { body: string; type: string; width: number; height: number; sha: string }
export interface BrandWorld {
  short_name: string; logos: Partial<Record<Variant, Stored>>; puts: { path: string; type: string; size: number }[]; codename?: string; fail?: string; cleanup?: SVGCleanup
  // Writes wait on `hold`; `inflight` and `maxInflight` count writes the server is handling at once.
  hold?: Promise<void>; arrived: number; inflight: number; maxInflight: number
}

function sizeOf(svg: string) {
  const box = /viewBox="0 0 (\d+) (\d+)"/.exec(svg)
  return box ? { width: Number(box[1]), height: Number(box[2]) } : { width: 1, height: 1 }
}
let counter = 0
function stored(body: string, type = 'image/svg+xml'): Stored {
  const sha = (++counter).toString(16).padStart(64, 'a')
  return { body, type, sha, ...sizeOf(body) }
}
function info(v: Variant, l: Stored) {
  return { url: `/api/brand/logo/${v}?v=${l.sha.slice(0, 16)}`, width: l.width, height: l.height, content_type: l.type, size: l.body.length, sha256: l.sha, uploaded_at: '2026-09-30T08:00:00Z' }
}
function settingsOf(w: BrandWorld, cleaned = false) {
  return { short_name: w.short_name, logo: w.logos.light ? info('light', w.logos.light) : null, logo_dark: w.logos.dark ? info('dark', w.logos.dark) : null, ...(cleaned ? { cleaned } : {}) }
}
function sessionBrand(w: BrandWorld) {
  const s = settingsOf(w)
  const ref = (l: typeof s.logo) => l ? { url: l.url, width: l.width, height: l.height } : undefined
  const out = { ...(w.short_name ? { short_name: w.short_name } : {}), ...(s.logo ? { logo: ref(s.logo) } : {}), ...(s.logo_dark ? { logo_dark: ref(s.logo_dark) } : {}) }
  return Object.keys(out).length ? out : undefined
}

export function brandWorld(options: { name?: string; light?: string; dark?: string; codename?: string } = {}): BrandWorld {
  return {
    short_name: options.name ?? '', puts: [], codename: options.codename, arrived: 0, inflight: 0, maxInflight: 0,
    logos: { ...(options.light ? { light: stored(options.light) } : {}), ...(options.dark ? { dark: stored(options.dark) } : {}) },
  }
}

// Registered last, so it answers before the other fixtures.
export async function mockBrand(page: Page, w: BrandWorld, options: { admin?: boolean } = {}) {
  const history = releaseHistory()
  await mockReleases(page, history)
  await page.route('**/api/version', route => route.fulfill({ json: { version: history.current, scheme: 'inspr-calendar-v2', ...(w.codename ? { codename: w.codename } : {}) } }))
  await page.route('**/api/**', async route => {
    const request = route.request(), path = new URL(request.url()).pathname, method = request.method()
    if (path === '/api/me') {
      return route.fulfill({ json: { dev_mode: false, identity: null, principal: { id: me.id, name: me.name, kind: 'person', roles: [options.admin === false ? 'member' : 'admin'] }, tenant: { id: 't1', slug: 'northwind', name: 'Northwind Traders', ...(sessionBrand(w) ? { brand: sessionBrand(w) } : {}) } } })
    }
    const logo = /^\/api\/brand\/logo\/(light|dark)$/.exec(path)
    if (logo && method === 'GET') {
      const l = w.logos[logo[1] as Variant]
      return l ? route.fulfill({ body: l.body, contentType: l.type, headers: { 'X-Content-Type-Options': 'nosniff' } }) : route.fulfill({ status: 404, json: { error: 'logo not found' } })
    }
    // A write the server takes its time over, so a test can edit the form meanwhile.
    const write = async (handle: () => Promise<void> | void) => {
      w.arrived++
      w.maxInflight = Math.max(w.maxInflight, ++w.inflight)
      try { await w.hold; await handle() } finally { w.inflight-- }
    }
    if (path === '/api/settings/brand') {
      if (options.admin === false) return route.fulfill({ status: 403, json: { error: 'forbidden' } })
      if (method === 'PUT') {
        const asked = String((request.postDataJSON() as { short_name: string }).short_name).trim()
        return write(async () => { w.short_name = asked; await route.fulfill({ json: settingsOf(w) }) })
      }
      return route.fulfill({ json: settingsOf(w) })
    }
    const upload = /^\/api\/settings\/brand\/logo\/(light|dark)$/.exec(path)
    if (upload) {
      const v = upload[1] as Variant
      if (method === 'DELETE') return write(async () => { delete w.logos[v]; await route.fulfill({ json: settingsOf(w) }) })
      const body = request.postDataBuffer()?.toString('utf8') ?? ''
      const type = (await request.headerValue('content-type')) ?? ''
      return write(async () => {
        w.puts.push({ path, type, size: body.length })
        if (w.fail) return route.fulfill({ status: 400, json: { error: w.fail } })
        if (type === 'image/svg+xml' && body.includes('<script')) return route.fulfill({ status: 400, json: { error: 'SVG element <script> is not supported; use a static logo with paths, shapes, text or gradients' } })
        const cleaned = body.includes('<!--') || !!w.cleanup
        w.logos[v] = stored(body.replace(/<!--[\s\S]*?-->/g, ''), type)
        return route.fulfill({ json: { ...settingsOf(w, cleaned), ...(w.cleanup ? { svg_cleanup: w.cleanup } : {}) } })
      })
    }
    return route.fallback()
  })
  return history
}

async function setup(page: Page, w: BrandWorld, options: { admin?: boolean; locale?: string } = {}) {
  await mockWork(page, fixtures(), { admin: options.admin !== false })
  const role = options.admin === false ? 'member' : 'admin'
  await mockBusiness(page, businessData({ role }), { role })
  const data = settingsData()
  data.profile.locale = options.locale ?? 'en-GB'
  await mockSettings(page, data)
  return mockBrand(page, w, options)
}

const lockup = (page: Page) => page.locator('.app-header .lockup')
const footerName = (page: Page) => page.locator('footer.app-footer .footer-name')

test('without a brand the header keeps the product mark; the footer names the product and opens the running release', async ({ page }) => {
  const errors = watchErrors(page)
  const history = await setup(page, brandWorld())
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(lockup(page).locator('.mark-backing')).toBeVisible()
  await expect(lockup(page)).toHaveAttribute('aria-label', 'PAIMOS AEON home')
  await expect(lockup(page).locator('.tenant-logo')).toHaveCount(0)
  await expect(footerName(page).locator('.footer-wordmark')).toHaveText('PAIMOS AEON')
  // An older server names no release: it reads as its calendar version (AEON-430), never as an invented name.
  await expect(footerName(page).locator('.rn-name')).toHaveCount(0)
  await page.locator('footer.app-footer .version-pill').click()
  await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible()
  await expect(page).toHaveURL(new RegExp(`releases=${history.current.replace(/\./g, '\\.')}`))
  expect(errors).toEqual([])
})

test('the product mark stays left and the named release pill stays right', async ({ page }) => {
  const history = await setup(page, brandWorld({ codename: 'Amber Aurora' }))
  await page.goto('/')
  await expect(footerName(page)).toHaveText('PAIMOS AEON')
  const pill = page.locator('footer.app-footer .version-pill')
  await expect(pill.locator('.footer-codename')).toHaveText('Amber Aurora')
  await expect(pill).toHaveAccessibleName(new RegExp(`^Release history, Amber Aurora, version ${history.current.replace(/\./g, '\\.')}`))
  await expect(pill.locator('.calendar-version')).toHaveAttribute('data-version-view', 'pretty')
  const box = await pill.boundingBox()
  expect(box!.x).toBeGreaterThan((await footerName(page).boundingBox())!.x)
  await pill.hover()
  await expect(pill.locator('.calendar-version')).toHaveAttribute('data-version-view', 'revealed')
  await expect(pill.locator('.footer-codename')).toBeVisible()
  await pill.click()
  await expect(page).toHaveURL(new RegExp(`releases=${history.current.replace(/\./g, '\\.')}`))
})

test('a workspace brand replaces the product mark: its logo and short name, and a light plate in dark mode', async ({ page }) => {
  await setup(page, brandWorld({ name: 'Northwind', light: WIDE }))
  await page.goto('/')
  await expect(lockup(page)).toHaveAttribute('aria-label', 'Northwind home')
  await expect(lockup(page).locator('.mark-backing')).toHaveCount(0)
  const img = lockup(page).locator('.tenant-logo img')
  await expect(img).toHaveAttribute('src', /^\/api\/brand\/logo\/light\?v=[0-9a-f]{16}$/)
  await expect(img).toHaveAttribute('width', '86')
  await expect(img).toHaveAttribute('height', '28')
  await expect.poll(() => img.evaluate(el => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)
  await expect(lockup(page).locator('.tenant-name')).toHaveText('Northwind')
  await expect(lockup(page).locator('.tenant-logo')).not.toHaveClass(/plate/)
  // The product keeps its name in the footer.
  await expect(footerName(page)).toContainText('PAIMOS AEON')

  await page.emulateMedia({ colorScheme: 'dark' })
  await page.evaluate(() => window.dispatchEvent(new Event('resize')))
  await expect(lockup(page).locator('.tenant-logo')).toHaveClass(/plate/)
})

test('a dark logo is used in dark mode; a broken logo falls back to the short name', async ({ page }) => {
  const w = brandWorld({ name: 'Northwind', light: WIDE, dark: WIDE_DARK })
  await page.emulateMedia({ colorScheme: 'dark' })
  await setup(page, w)
  await page.goto('/')
  const img = lockup(page).locator('.tenant-logo img')
  await expect(img).toHaveAttribute('src', /^\/api\/brand\/logo\/dark\?v=/)
  await expect(lockup(page).locator('.tenant-logo')).not.toHaveClass(/plate/)
  delete w.logos.dark
  await page.evaluate(() => { const i = document.querySelector<HTMLImageElement>('.app-header .tenant-logo img')!; i.src = `${i.src}&reload=1` })
  await expect(lockup(page).locator('.tenant-logo')).toHaveCount(0)
  await expect(lockup(page).locator('.tenant-name')).toHaveText('Northwind')
})

test('an admin sets the brand in Settings → Workspace; the header follows at once', async ({ page }) => {
  const errors = watchErrors(page)
  const w = brandWorld()
  await setup(page, w)
  await page.goto('/settings/workspace#brand')
  const card = page.locator('#brand')
  await expect(card.getByRole('heading', { name: 'Brand' })).toBeVisible()
  // Nothing set: both previews show the product mark, and one primary action.
  await expect(card.getByRole('img', { name: 'Header preview, light mode' }).locator('.wordmark')).toBeVisible()
  await expect(card.locator('.btn.primary')).toHaveCount(1)
  await expect(card.getByRole('button', { name: 'Upload logo' })).toHaveClass(/primary/)
  expect((await new AxeBuilder({ page }).include('#brand').analyze()).violations).toEqual([])

  // The client refuses what the server would refuse anyway.
  await card.getByLabel('Logo file', { exact: true }).setInputFiles({ name: 'logo.gif', mimeType: 'image/gif', buffer: Buffer.from('GIF89a') })
  await expect(card.getByRole('alert')).toHaveText('Use a PNG, WebP or SVG file.')
  await card.getByLabel('Logo file', { exact: true }).setInputFiles({ name: 'big.svg', mimeType: 'image/svg+xml', buffer: Buffer.alloc(300 * 1024, 32) })
  await expect(card.getByRole('alert')).toHaveText('The file is 300 KB; the limit is 256 KB.')
  expect(w.puts).toEqual([])

  // The server's refusal is shown in its words.
  w.fail = 'the logo is 20×20 px; each side needs at least 32 px'
  await card.getByLabel('Logo file', { exact: true }).setInputFiles({ name: 'tiny.svg', mimeType: 'image/svg+xml', buffer: Buffer.from(SQUARE) })
  await expect(card.getByRole('alert')).toHaveText('The logo is 20×20 px; each side needs at least 32 px.')
  delete w.fail

  // Unsupported SVG refuses the upload and preserves the current brand.
  await card.getByLabel('Logo file', { exact: true }).setInputFiles({ name: 'northwind.svg', mimeType: '', buffer: Buffer.from(WIDE.replace('</svg>', '<script>alert(1)</script></svg>')) })
  await expect(card.getByRole('alert')).toHaveText('SVG element <script> is not supported; use a static logo with paths, shapes, text or gradients.')
  expect(w.logos.light).toBeUndefined()
  await expect(lockup(page).locator('.tenant-logo img')).toHaveCount(0)

  // Harmless export comments may still be removed from a static logo.
  await card.getByLabel('Logo file', { exact: true }).setInputFiles({ name: 'northwind.svg', mimeType: '', buffer: Buffer.from(WIDE.replace('</svg>', '<!-- exported logo --></svg>')) })
  await expect(page.locator('.toast')).toContainText('Saved. SVG comments were removed.')
  expect(w.puts.at(-1)).toMatchObject({ path: '/api/settings/brand/logo/light', type: 'image/svg+xml' })
  await expect(card.getByRole('alert')).toHaveCount(0)
  await expect(card.getByRole('button', { name: 'Replace' })).toBeVisible()
  await expect(card.locator('.btn.primary')).toHaveCount(0)
  await expect(lockup(page).locator('.tenant-logo img')).toHaveAttribute('src', /^\/api\/brand\/logo\/light\?v=/)
  await expect(card.getByText('Auto: your logo on a light plate')).toBeVisible()
  await expect(card.getByRole('img', { name: 'Header preview, dark mode' }).locator('.logo')).toHaveClass(/plate/)

  const field = card.getByRole('textbox', { name: 'Short name' })
  await field.fill('  Northwind   Traders ')
  await field.press('Enter')
  await expect(lockup(page).locator('.tenant-name')).toHaveText('Northwind Traders')
  await expect(field).toHaveValue('Northwind Traders')
  expect(w.short_name).toBe('Northwind Traders')

  await card.getByLabel('Dark logo file').setInputFiles({ name: 'dark.svg', mimeType: 'image/svg+xml', buffer: Buffer.from(WIDE_DARK) })
  await expect(card.getByRole('button', { name: 'Remove dark logo' })).toBeVisible()
  expect((await new AxeBuilder({ page }).include('#brand').include('.app-header .lockup').include('footer.app-footer').analyze()).violations).toEqual([])
  await expect(card.getByText('Auto: your logo on a light plate')).toHaveCount(0)

  await card.getByRole('button', { name: 'Remove logo', exact: true }).click()
  await card.getByRole('button', { name: 'Remove dark logo' }).click()
  await expect(lockup(page).locator('.tenant-logo')).toHaveCount(0)
  await expect(lockup(page).locator('.tenant-name')).toHaveText('Northwind Traders')
  await field.fill('')
  await field.press('Enter')
  await expect(lockup(page).locator('.mark-backing')).toBeVisible()
  expect(errors).toEqual([])
})

// The server decides what is removed; the card reports names as plain text.
for (const locale of ['en-GB', 'de-AT']) {
  test(`SVG upload names non-drawing removals in ${locale}`, async ({ page }) => {
    const w = brandWorld()
    w.cleanup = { removed_attribute_count: 3, removed_attributes: ['role', 'aria-label', 'data-name'], removed_element_count: 0 }
    await setup(page, w, { locale })
    await page.goto('/settings/workspace#brand')
    const svg = WIDE.replace('<svg ', '<svg role="img" aria-label="Northwind" data-name="Logo" ')
    await page.locator('#brand').getByLabel('Logo file', { exact: true }).setInputFiles({ name: 'export.svg', mimeType: 'image/svg+xml', buffer: Buffer.from(svg) })
    await expect(page.locator('.toast')).toContainText(locale === 'de-AT'
      ? '3 nicht zeichnende Attribute entfernt (role, aria-label, data-name).'
      : 'Removed 3 non-drawing attributes (role, aria-label, data-name).')
  })
}

// The review's repro: an upload's response used to overwrite the name typed while it was in flight.
test('a short name typed while a logo uploads is kept and saved after it, never overwritten; writes run one at a time', async ({ page }) => {
  const errors = watchErrors(page)
  const w = brandWorld({ name: 'Northwind' })
  let release!: () => void
  w.hold = new Promise<void>(resolve => { release = resolve })
  await setup(page, w)
  await page.goto('/settings/workspace#brand')
  const card = page.locator('#brand')
  const field = card.getByRole('textbox', { name: 'Short name' })
  await expect(field).toHaveValue('Northwind')

  await card.getByLabel('Logo file', { exact: true }).setInputFiles({ name: 'northwind.svg', mimeType: 'image/svg+xml', buffer: Buffer.from(WIDE) })
  await expect.poll(() => w.arrived).toBe(1)
  await expect(field).toBeEditable()
  await field.fill('Northwind Traders')
  // Leaving the field while the upload is still in flight must not start a second write beside it.
  await field.blur()
  await page.waitForTimeout(150)
  expect(w.arrived).toBe(1)
  expect(w.maxInflight).toBe(1)

  release()
  await expect(card.getByRole('button', { name: 'Replace' })).toBeVisible()
  await expect(field).toHaveValue('Northwind Traders')
  await expect.poll(() => w.short_name).toBe('Northwind Traders')
  await expect(lockup(page).locator('.tenant-name')).toHaveText('Northwind Traders')
  expect(w.puts.map(put => put.path)).toEqual(['/api/settings/brand/logo/light'])
  expect(w.maxInflight).toBe(1)

  // Typed but not yet saved when a removal's response arrives: still the admin's text.
  w.hold = undefined
  await field.fill('Northwind Traders GmbH')
  await card.getByRole('button', { name: 'Remove logo', exact: true }).click()
  await expect(card.getByRole('button', { name: 'Upload logo' })).toBeVisible()
  await expect.poll(() => w.short_name).toBe('Northwind Traders GmbH')
  await expect(field).toHaveValue('Northwind Traders GmbH')
  expect(w.maxInflight).toBe(1)
  expect(errors).toEqual([])
})

test('the brand card, header and footer pass axe in dark mode too', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' })
  await setup(page, brandWorld({ name: 'Northwind', light: WIDE, dark: WIDE_DARK }))
  await page.goto('/settings/workspace#brand')
  await expect(page.locator('#brand').getByRole('heading', { name: 'Brand' })).toBeVisible()
  expect((await new AxeBuilder({ page }).include('#brand').include('.app-header .lockup').include('footer.app-footer').analyze()).violations).toEqual([])
})

test('members do not see the brand settings', async ({ page }) => {
  await setup(page, brandWorld({ name: 'Northwind', light: SQUARE }), { admin: false })
  await page.goto('/settings/workspace')
  await expect(page.locator('#brand')).toHaveCount(0)
  // They still see the workspace's brand in the header.
  await expect(lockup(page).locator('.tenant-name')).toHaveText('Northwind')
})

test('phones show a square logo beside the places at their root, and leave inner pages and wide logos to the places', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const w = brandWorld({ name: 'Northwind', light: SQUARE })
  await setup(page, w)
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(lockup(page).locator('.tenant-logo img')).toBeVisible()
  await expect(lockup(page).locator('.tenant-name')).toBeHidden()
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
  await page.goto('/p/PHAROS')
  await expect(page.locator('.crumbs')).toBeVisible()
  await expect(lockup(page)).toBeHidden()
  w.logos.light = stored(WIDE)
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(lockup(page)).toBeHidden()
})

// ------------------------------------------------------------------ shots
const SHOTS = process.env.BRAND_SHOTS ?? ''
for (const [kind, world] of [['square', () => brandWorld({ name: 'Northwind', light: SQUARE, codename: 'Amber Aurora' })], ['wide', () => brandWorld({ name: 'Northwind Traders', light: WIDE, codename: 'Galactic Gyroscope' })], ['wide-dark-logo', () => brandWorld({ light: WIDE, dark: WIDE_DARK, codename: 'Cobalt Comet' })], ['none', () => brandWorld()]] as const) {
  test(`shots: ${kind}`, async ({ browser }) => {
    test.skip(!SHOTS, 'BRAND_SHOTS=<dir> writes the review shots.')
    test.setTimeout(180_000)
    mkdirSync(SHOTS, { recursive: true })
    for (const theme of ['light', 'dark'] as const) for (const width of [1600, 390]) {
      const context = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 1000 }, colorScheme: theme, reducedMotion: 'reduce' })
      const page = await context.newPage()
      await setup(page, world())
      await page.goto('/')
      await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
      await page.evaluate(() => document.fonts.ready)
      await page.waitForTimeout(300)
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width)
      await page.screenshot({ path: join(SHOTS, `home__${kind}__${width}__${theme}.png`) })
      await page.goto('/p/PHAROS')
      await page.waitForTimeout(400)
      await page.screenshot({ path: join(SHOTS, `project__${kind}__${width}__${theme}.png`), clip: { x: 0, y: 0, width, height: 72 } })
      await page.goto('/settings/workspace#brand')
      await expect(page.locator('#brand')).toBeVisible()
      await page.waitForTimeout(300)
      await page.locator('#brand').screenshot({ path: join(SHOTS, `settings__${kind}__${width}__${theme}.png`) })
      await context.close()
    }
  })
}
