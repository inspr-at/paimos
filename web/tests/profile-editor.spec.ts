// SPDX-License-Identifier: AGPL-3.0-only
// U19 (AEON-110): Settings › Business › Document profiles. The profiles on the
// Business card, the editor with its live A4 preview (the real quote renderer on
// an invented quote for Beispiel Stahl GmbH), fonts read and shown in their own
// face, colours checked against the paper, geometry in millimetres, the table's
// fit, labels by locale, the default for new quotes, duplicate and archive with
// Undo, leaving with unsaved edits, phones, axe in light and dark, the production
// CSP, and the quote title bar's profile picker. Synthetic content only.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { businessData, mockBusiness } from './business-fixtures'
import { crmData, mockCRM } from './crm-fixtures'
import { FONT, PROFILE, mockProfiles, profileWorld, type ProfileWorld } from './profile-fixtures'
import { mockQuotes, quoteWorld, Q } from './quote-list-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { syntheticTtf } from './synthetic-font'
import { fixtures, mockWork, watchErrors } from './work-fixtures'

const ALL = ['business_costs', 'business_crm', 'business_quotes', 'business_hours']
async function setup(page: Page, options: Parameters<typeof profileWorld>[0] & { role?: 'admin' | 'member' } = {}) {
  await mockWork(page, fixtures())
  await mockBusiness(page, businessData({ enabled: ALL, role: options.role }))
  await mockSettings(page, settingsData())
  const world = profileWorld(options)
  const calls = await mockProfiles(page, world)
  return { world, calls }
}
async function editor(page: Page, id: string) {
  await page.goto(`/settings/business/profiles/${id}`)
  await expect(page.locator('.profile-preview .quote-document')).toHaveAttribute('data-quote-ready', 'true')
}
const preview = (page: Page) => page.locator('.profile-preview .quote-document')
const eyebrow = (page: Page) => page.locator('.head-titles .eyebrow')
const toast = (page: Page) => page.locator('.toast').last()
const jump = (page: Page, name: string) => page.getByRole('navigation', { name: 'Profile sections' }).getByRole('button', { name, exact: true }).click()
const mm = (value: number) => value * 96 / 25.4
async function axe(page: Page) {
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').exclude('.quote-document').analyze()
  const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')} — ${n.failureSummary?.split('\n').slice(1, 2).join(' ').trim()}`).join('\n')}`)
  expect(summary, summary.join('\n')).toEqual([])
}
const latest = (world: ProfileWorld, id: string) => world.profiles.find(p => p.id === id)!.revisions.at(-1)!

test('the Business card lists the profiles in use and opens the editor at the default', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  await page.goto('/settings/business#quote-profiles')
  const card = page.locator('#quote-profiles')
  await expect(card.locator('.profile-row')).toHaveCount(2)
  await expect(card.locator('.profile-row').first()).toContainText('Beispiel Stahl GmbH')
  await expect(card.locator('.profile-row').first()).toContainText('Default')
  await expect(card).toContainText('1 archived profile in the editor.')
  await card.getByRole('link', { name: 'Edit profiles' }).click()
  await expect(page).toHaveURL(`/settings/business/profiles/${PROFILE.steel}`)
  await expect(page.locator('#profiles-title')).toHaveText('Beispiel Stahl GmbH')
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('Document profiles')
})

test('an edit shows in the live preview at once; saving makes a revision that Undo restores', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const errors = watchErrors(page)
  const { world, calls } = await setup(page)
  await editor(page, PROFILE.steel)
  await expect(preview(page).locator('.quote-page-header').first()).toContainText('ANGEBOT A260925-07')
  await expect(preview(page).locator('.quote-page-header').first()).toContainText('21.09.2026')
  await jump(page, 'Colours')
  const accent = page.getByLabel('Accent', { exact: true })
  await accent.fill('#8a3b12'); await accent.press('Enter')
  await expect(preview(page).locator('.quote-overline').first()).toHaveCSS('color', 'rgb(138, 59, 18)')
  await expect(eyebrow(page)).toContainText('unsaved changes')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(toast(page)).toContainText('Saved Beispiel Stahl GmbH as revision 3.')
  const patch = calls.find(c => c.method === 'PATCH')!.body as { expected_revision: number; definition: { colors: Record<string, string> } }
  expect(patch.expected_revision).toBe(2)
  expect(patch.definition.colors.accent).toBe('#8a3b12')
  await expect(eyebrow(page)).toContainText('revision 3')
  await toast(page).getByRole('button', { name: 'Undo' }).click()
  await expect(toast(page)).toContainText('is back as it was, as revision 4')
  expect(latest(world, PROFILE.steel).definition.colors.accent).toBe('#2a6f86')
  await expect(preview(page).locator('.quote-overline').first()).toHaveCSS('color', 'rgb(42, 111, 134)')
  expect(errors).toEqual([])
})

test('a new profile starts from the defaults and is created with the save key', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await setup(page)
  await editor(page, PROFILE.steel)
  await page.getByRole('navigation', { name: 'Document profiles' }).getByRole('button', { name: 'New' }).click()
  await expect(page).toHaveURL('/settings/business/profiles/new')
  await expect(page.locator('#profiles-title')).toHaveText('New profile')
  await expect(page.getByRole('button', { name: 'Create profile' })).toBeVisible()
  await page.getByLabel('Name', { exact: true }).fill('Beispiel Messe')
  await page.keyboard.press('ControlOrMeta+s')
  await expect(toast(page)).toContainText('Created Beispiel Messe.')
  const post = calls.find(c => c.method === 'POST' && c.path === '/api/quote-profiles')!.body as { name: string; definition: { schema: string } }
  expect(post).toMatchObject({ name: 'Beispiel Messe', definition: { schema: 'inspr.document-profile.v1' } })
  await expect(page).toHaveURL(/\/settings\/business\/profiles\/7e5a0000-0000-4000-8000-0000000000\d\d$/)
  await expect(page.getByRole('navigation', { name: 'Document profiles' })).toContainText('Beispiel Messe')
})

test('fonts: files are read for family, weight and style, uploaded, and shown in their own face', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await setup(page)
  await editor(page, PROFILE.steel)
  await jump(page, 'Type')
  await page.getByLabel('Font files').setInputFiles([
    { name: 'grotesk.ttf', mimeType: 'font/ttf', buffer: Buffer.from(syntheticTtf('Beispiel Grotesk', 'SemiBold Italic', 600, true)) },
    { name: 'Beispiel_Mono-Bold.woff2', mimeType: 'font/woff2', buffer: FONT },
    { name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('not a font') },
  ])
  const fonts = page.locator('.fonts')
  await expect(fonts.getByRole('textbox', { name: 'Family of font 1' })).toHaveValue('Beispiel Grotesk')
  await expect(fonts.getByRole('combobox', { name: 'Weight of Beispiel Grotesk' })).toHaveValue('600')
  await expect(fonts.getByRole('combobox', { name: 'Style of Beispiel Grotesk 600' })).toHaveValue('italic')
  await expect(fonts.getByRole('textbox', { name: 'Family of font 2' })).toHaveValue('Beispiel Mono')
  await expect(fonts.getByRole('combobox', { name: 'Weight of Beispiel Mono' })).toHaveValue('700')
  await expect(fonts).toContainText('Beispiel_Mono-Bold.woff2: weight and style read from the file name.')
  await expect(fonts).toContainText('notes.txt is not a TTF, OTF or WOFF2 font.')
  await expect(fonts.locator('.group-head').first()).toContainText('Body text Beispiel Grotesk')
  await expect(fonts.locator('.group-head').nth(1)).toContainText('Headings Beispiel Mono')
  // Each face shows in the file itself, loaded from this origin.
  await expect(fonts.locator('.specimen.ready')).toHaveCount(2)
  await expect(page.getByRole('img', { name: /^Type specimen/ })).toBeVisible()
  expect(calls.filter(c => c.method === 'POST' && c.path === '/api/quote-profiles/assets')).toHaveLength(2)
  // Dropping a file on the zone does the same as choosing it.
  const drop = page.locator('.fonts .drop')
  const transfer = await page.evaluateHandle(bytes => {
    const data = new DataTransfer()
    data.items.add(new File([new Uint8Array(bytes)], 'Beispiel_Grotesk-Regular.ttf', { type: 'font/ttf' }))
    return data
  }, [...syntheticTtf('Beispiel Grotesk', 'Regular', 400, false)])
  await drop.dispatchEvent('dragenter', { dataTransfer: transfer })
  await expect(drop).toHaveClass(/over/)
  await drop.dispatchEvent('drop', { dataTransfer: transfer })
  await expect(fonts.getByRole('combobox', { name: 'Weight of Beispiel Grotesk' })).toHaveCount(2)
  await fonts.getByRole('button', { name: 'Remove Beispiel Grotesk Regular' }).click()
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(toast(page)).toContainText('revision 3')
  const saved = calls.find(c => c.method === 'PATCH')!.body as { definition: { fonts: { family: string; weight: number; style: string; role: string }[] } }
  expect(saved.definition.fonts.map(f => [f.family, f.weight, f.style, f.role])).toEqual([['Beispiel Grotesk', 600, 'italic', 'body'], ['Beispiel Mono', 700, 'normal', 'display']])
})

test('colours are checked against the paper: a warning, never a block', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await setup(page)
  await editor(page, PROFILE.steel)
  await jump(page, 'Colours')
  const quiet = page.getByLabel('Quiet text', { exact: true })
  await expect(page.locator('.color').filter({ hasText: 'Quiet text' }).locator('.ratio')).toHaveClass(/ok/)
  await quiet.fill('#cccccc'); await quiet.press('Enter')
  const row = page.locator('.color').filter({ hasText: 'Quiet text' })
  await expect(row.locator('.ratio')).toHaveClass(/low/)
  await expect(row.locator('.ratio')).toContainText('1.6:1')
  await expect(row).toContainText('Below AA on the paper; small print will be hard to read.')
  await expect(quiet).toHaveAccessibleDescription(/Page header, footer and page numbers.*Below AA/)
  await quiet.fill('nope'); await expect(row).toContainText('Use a colour like #2a7f78.')
  await quiet.fill('#ccc'); await quiet.press('Enter')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(toast(page)).toContainText('revision 3')
  expect((calls.find(c => c.method === 'PATCH')!.body as { definition: { colors: Record<string, string> } }).definition.colors.soft).toBe('#cccccc')
})

test('geometry in millimetres moves the classic page; the standard layout keeps its own', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  await editor(page, PROFILE.steel)
  await jump(page, 'Page')
  const top = page.getByRole('spinbutton', { name: 'Top' })
  await expect(top).toHaveAttribute('aria-valuetext', '18 millimetres')
  await top.fill('30'); await top.press('Enter')
  await expect.poll(async () => parseFloat(await preview(page).locator('.quote-page').first().evaluate(el => getComputedStyle(el).paddingTop))).toBeCloseTo(mm(30), 0)
  await top.press('ArrowUp')
  await expect(top).toHaveValue('30.5')
  await page.getByRole('button', { name: 'Margins' }).click()
  await expect(page.locator('.preview-inner')).toHaveClass(/guides/)
  // Standard: its own margins; the fields say so and wait.
  await jump(page, 'Basics')
  await page.getByRole('radio', { name: /^Standard/ }).click()
  await jump(page, 'Page')
  await expect(top).toBeDisabled()
  await expect(page.locator('#' + (await page.locator('.form-section').nth(3).getAttribute('id')))).toContainText('The standard layout keeps its own A4 margins')
  await expect(page.getByRole('button', { name: 'Margins' })).toHaveCount(0)
})

test('the positions table must fit between the margins; Fit the description makes it fit', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { calls } = await setup(page)
  await editor(page, PROFILE.steel)
  await jump(page, 'Table')
  const description = page.getByRole('spinbutton', { name: 'Description' })
  await description.fill('120'); await description.press('Enter')
  await expect(page.locator('[data-problem="columns"]')).toContainText('The columns are 217 mm wide; the page has 168 mm between the margins.')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(toast(page)).toContainText('The columns are 217 mm wide')
  expect(calls.some(c => c.method === 'PATCH')).toBe(false)
  await page.getByRole('button', { name: 'Fit the description' }).click()
  await expect(page.locator('[data-problem="columns"]')).toContainText('168 of 168 mm between the margins.')
  await expect(description).toHaveValue('71')
})

test('the default for new quotes is one switch, with Undo', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { world } = await setup(page)
  await editor(page, PROFILE.plain)
  const box = page.getByRole('checkbox', { name: /Default for new quotes/ })
  await expect(box).not.toBeChecked()
  await box.check()
  await expect(toast(page)).toContainText('New quotes start with Studio English.')
  expect(world.settings.default_profile_id).toBe(PROFILE.plain)
  await expect(page.getByRole('link', { name: /Studio English/ })).toContainText('Default')
  await toast(page).getByRole('button', { name: 'Undo' }).click()
  await expect.poll(() => world.settings.default_profile_id).toBe(PROFILE.steel)
  // Unsaved edits first: the default takes the saved revision.
  await page.getByLabel('Name', { exact: true }).fill('Studio English 2')
  await expect(box).toBeDisabled()
})

test('duplicate and archive each come with Undo; archived profiles restore from the rail', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  const { world, calls } = await setup(page)
  await editor(page, PROFILE.steel)
  await page.getByRole('button', { name: 'Duplicate' }).click()
  await expect(toast(page)).toContainText('Made Beispiel Stahl GmbH (copy).')
  await expect(page.locator('#profiles-title')).toHaveText('Beispiel Stahl GmbH (copy)')
  const copy = world.profiles.at(-1)!
  expect(copy.revisions[0]!.definition.colors.accent).toBe('#2a6f86')
  await toast(page).getByRole('button', { name: 'Undo' }).click()
  await expect(page).toHaveURL(`/settings/business/profiles/${PROFILE.steel}`)
  expect(copy.archived).toBe(true)

  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await expect(toast(page)).toContainText('Archived Beispiel Stahl GmbH. New quotes start with the standard look.')
  await expect(eyebrow(page)).toContainText('archived')
  await expect(page.getByLabel('Name', { exact: true })).toBeDisabled()
  expect(world.settings.default_profile_id).toBeUndefined()
  await toast(page).getByRole('button', { name: 'Undo' }).click()
  await expect(toast(page)).toContainText('Beispiel Stahl GmbH is back.')
  await expect.poll(() => world.settings.default_profile_id).toBe(PROFILE.steel)
  await expect(page.getByLabel('Name', { exact: true })).toBeEnabled()

  const rail = page.getByRole('navigation', { name: 'Document profiles' })
  await rail.getByRole('button', { name: /^Archived/ }).click()
  await rail.getByRole('button', { name: 'Restore Messe 2025' }).click()
  await expect(toast(page)).toContainText('Messe 2025 is back.')
  expect(calls.filter(c => c.path.endsWith('/undo')).length).toBe(3)
})

test('the locale moves default labels and formats; labels written by hand stay', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  await editor(page, PROFILE.steel)
  await jump(page, 'Labels')
  await page.getByLabel('Recipient', { exact: true }).fill('Kunde')
  await jump(page, 'Basics')
  await page.getByRole('radio', { name: 'English' }).click()
  await expect(page.locator('.form-section').first()).toContainText('Dates and amounts print as 21/09/2026 · 5,560.00 EUR.')
  await expect(page.locator('.form-section').first()).toContainText('Labels that were still the German defaults are now English')
  await jump(page, 'Labels')
  await expect(page.getByLabel('Document title', { exact: true })).toHaveValue('QUOTE')
  await expect(page.getByLabel('Recipient', { exact: true })).toHaveValue('Kunde')
  await expect(preview(page).locator('.quote-overline').first()).toContainText('QUOTE')
  await expect(preview(page).locator('.quote-page-footer').first()).toContainText('PAGE 1 OF')
})

test('unsaved edits ask before another profile opens', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  await editor(page, PROFILE.steel)
  await page.getByLabel('Name', { exact: true }).fill('Beispiel Stahl Neu')
  await page.getByRole('link', { name: /Studio English/ }).click()
  const confirm = page.getByRole('dialog', { name: 'Discard your changes?' })
  await expect(confirm).toBeVisible()
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  await expect(page).toHaveURL(`/settings/business/profiles/${PROFILE.steel}`)
  await expect(page.getByLabel('Name', { exact: true })).toHaveValue('Beispiel Stahl Neu')
  await page.getByRole('link', { name: /Studio English/ }).click()
  await page.getByRole('dialog', { name: 'Discard your changes?' }).getByRole('button', { name: 'Discard' }).click()
  await expect(page.locator('#profiles-title')).toHaveText('Studio English')
})

test('phones: the form or the preview, nothing cut', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  await page.goto(`/settings/business/profiles/${PROFILE.steel}`)
  await expect(page.locator('.form-section').first()).toBeVisible()
  // The section jumps are one row that scrolls sideways (QA3): what is past its edge
  // is reached by scrolling, and choosing a section brings its pill into view.
  const jumps = page.getByRole('navigation', { name: 'Profile sections' })
  expect(await jumps.evaluate(el => getComputedStyle(el).overflowX)).toBe('auto')
  await jumps.getByRole('button', { name: 'Labels' }).evaluate(el => el.scrollIntoView({ inline: 'nearest' }))
  await jumps.getByRole('button', { name: 'Labels' }).click()
  await expect(jumps.getByRole('button', { name: 'Labels' })).toBeInViewport({ ratio: 1 })
  const cut = () => page.evaluate(() => [...document.querySelectorAll<HTMLElement>('.profiles-page *')].filter(el => {
    const r = el.getBoundingClientRect(), c = getComputedStyle(el)
    if (r.width <= 1 || r.height <= 1 || c.display === 'none' || el.closest('svg, .quote-document, .sr-only, .jumps')) return false
    return r.left < -0.5 || r.right > innerWidth + 0.5
  }).map(el => `${el.tagName}.${el.className}`))
  expect(await cut()).toEqual([])
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.getByRole('group', { name: 'Show' }).getByRole('button', { name: 'Preview' }).click()
  await expect(preview(page)).toHaveAttribute('data-quote-ready', 'true')
  const box = (await preview(page).locator('.quote-page').first().boundingBox())!
  expect(box.x).toBeGreaterThanOrEqual(0)
  expect(box.x + box.width).toBeLessThanOrEqual(390)
  expect(await cut()).toEqual([])
})

test('under the production CSP the fonts and the mark load from this origin', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.addInitScript(() => {
    const seen: string[] = []
    ;(window as unknown as { cspViolations: string[] }).cspViolations = seen
    document.addEventListener('securitypolicyviolation', event => { seen.push(`${event.violatedDirective} ${event.blockedURI}`) })
  })
  await page.route('**/*', async route => {
    if (route.request().resourceType() !== 'document') return route.fallback()
    const response = await route.fetch()
    // Match writeHTML's per-document nonce policy. Vite's CSS loader reads the
    // same nonce from this test-only meta tag; the built application uses bundles.
    const nonce = 'YWVvbi1uaWdodGx5LWZpeHR1cmUtb25seQ'
    const body = (await response.text()).replaceAll('__AEON_THEME_NONCE__', nonce)
      .replace('<head>', `<head><meta property="csp-nonce" nonce="${nonce}">`)
    const policy = `default-src 'self'; img-src 'self' blob: data:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; script-src 'self' 'nonce-${nonce}'; style-src 'self' 'nonce-${nonce}'`
    await route.fulfill({ response, body, headers: { ...response.headers(), 'content-security-policy': policy } })
  })
  await setup(page, { font: true })
  await editor(page, PROFILE.steel)
  await jump(page, 'Type')
  await expect(page.locator('.specimen.ready')).toHaveCount(1)
  await expect.poll(() => page.evaluate(() => [...document.fonts].some(face => face.family.includes('QuoteProfile') && face.status === 'loaded'))).toBe(true)
  await expect(preview(page).locator('.quote-mark img').first()).toHaveJSProperty('complete', true)
  expect(await page.evaluate(() => (window as unknown as { cspViolations: string[] }).cspViolations)).toEqual([])
})

test('a draft picks its profile in the title bar, with Undo; an issued version shows the frozen one', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await mockWork(page, fixtures())
  await mockCRM(page, crmData())
  const profiles = profileWorld()
  const snapshot = (id: string, revision?: number) => {
    const p = profiles.profiles.find(item => item.id === id && !item.archived)
    if (!p) return null
    const at = revision ?? p.revisions.length
    return { id, revision: at, definition: structuredClone(p.revisions[at - 1]!.definition) }
  }
  const world = quoteWorld()
  const version = world.versions.get(Q.issued)![0]!
  ;(version.document as { profile?: unknown }).profile = snapshot(PROFILE.steel, 1)
  const calls = await mockQuotes(page, world, { profile: id => snapshot(id) })
  await mockProfiles(page, profiles)
  await page.goto(`/business/quotes/${Q.draft}`)
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  const picker = page.getByRole('button', { name: /^Document profile: Standard document/ })
  await picker.click()
  const menu = page.getByRole('menu', { name: 'Document profile' })
  await expect(menu.getByRole('menuitemradio', { name: /Standard document/ })).toHaveAttribute('aria-checked', 'true')
  await expect(menu.getByRole('menuitemradio')).toHaveCount(3)
  await menu.getByRole('menuitemradio', { name: /Beispiel Stahl GmbH/ }).click()
  await expect(toast(page)).toContainText('This draft now uses Beispiel Stahl GmbH.')
  expect(calls.find(c => c.method === 'PUT' && c.path.endsWith('/profile'))?.body).toEqual({ expected_draft_revision: 3, profile_id: PROFILE.steel })
  await expect(page.locator('.quote-document .quote-page-header').first()).toContainText('ANGEBOT')
  await expect(page.getByRole('button', { name: /^Document profile: Beispiel Stahl GmbH/ })).toBeVisible()
  await toast(page).getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByRole('button', { name: /^Document profile: Standard document/ })).toBeVisible()
  expect(calls.filter(c => c.method === 'PUT' && c.path.endsWith('/profile')).at(-1)?.body).toEqual({ expected_draft_revision: 4, profile_id: '' })

  await page.goto(`/business/quotes/${Q.issued}`)
  await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
  const frozen = page.locator('.frozen-profile')
  await expect(frozen).toContainText('Beispiel Stahl GmbH · rev. 1')
  await expect(frozen).toContainText('Issued with Beispiel Stahl GmbH, revision 1. It never changes.')
  await expect(page.getByRole('button', { name: /^Document profile/ })).toHaveCount(0)
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: the editor, its warnings, a new and an archived profile, the picker, in ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme })
    await page.setViewportSize({ width: 1440, height: 900 })
    await setup(page, { font: true })
    await page.goto('/settings/business#quote-profiles')
    await expect(page.locator('#quote-profiles .profile-row').first()).toBeVisible()
    await axe(page)
    await editor(page, PROFILE.steel)
    await axe(page)
    await jump(page, 'Colours')
    const quiet = page.getByLabel('Quiet text', { exact: true })
    await quiet.fill('#bbbbbb'); await quiet.press('Enter')
    await axe(page)
    await editor(page, 'new')
    await axe(page)
    await editor(page, PROFILE.old)
    await axe(page)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`/settings/business/profiles/${PROFILE.steel}`)
    await expect(page.locator('.form-section').first()).toBeVisible()
    await axe(page)
  })
}
