// SPDX-License-Identifier: AGPL-3.0-only
// AEON-542: approved A Line, beside the opt-in project flow (AEON-515).
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases, presentedHistory, RELEASE_HISTORY_NAME } from './releases-fixtures'

const screenshots = process.env.AEON_SCREENSHOTS_DIR ?? 'test-results/footer-release-pill'
const pill = (page: Page) => page.locator('footer.app-footer .version-pill')
const version = (page: Page) => pill(page).locator('.calendar-version')

async function setup(page: Page, options: { flow?: boolean; fresh?: boolean; name?: string } = {}) {
  const history = presentedHistory()
  const data = fixtures()
  if (options.flow) data.preferences['developer-ui'] = { show_flow_controls: true }
  if (options.fresh) data.preferences.releases = { last_seen: history.releases[4].version }
  await mockWork(page, data)
  await mockReleases(page, history, { codename: options.name ?? 'Lucky Lune' })
  return history
}

async function bounds(locator: Locator) {
  const rect = await locator.boundingBox()
  expect(rect).toBeTruthy()
  return rect!
}

async function insideFooter(page: Page) {
  const footer = await bounds(page.locator('footer.app-footer'))
  const right = await bounds(pill(page))
  expect(right.x + right.width).toBeLessThanOrEqual(footer.x + footer.width)
  expect(right.x + right.width).toBeGreaterThan(footer.x + footer.width - 40)
  const mark = await bounds(page.locator('footer.app-footer .footer-name'))
  expect(mark.x + mark.width).toBeLessThanOrEqual(right.x)
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0)
}

for (const colorScheme of ['light', 'dark'] as const) {
  for (const width of [1440, 390]) {
    test(`release pill and product mark at ${width} in ${colorScheme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: width < 600 ? 844 : 900 })
      await page.emulateMedia({ colorScheme })
      const history = await setup(page, { fresh: true })
      await page.goto('/')
      await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
      await expect(page.locator('footer.app-footer .footer-name')).toHaveText('PAIMOS AEON')
      await expect(pill(page).locator('.footer-codename')).toHaveText('Lucky Lune')
      await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
      await expect(version(page)).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
      await expect(pill(page).locator('.new-badge')).toHaveText('3 new')
      await expect(pill(page)).toHaveAccessibleName(`Release history, Lucky Lune, version ${history.current}, 3 new since your last visit`)
      expect(await pill(page).locator('.pill-face').evaluate(el => [...el.querySelectorAll('svg, .footer-codename, .calendar-version, .new-badge')].map(child => child.tagName === 'svg' ? 'history' : child.className.split(' ')[0]))).toEqual(['history', 'footer-codename', 'calendar-version', 'new-badge'])
      await insideFooter(page)
      await page.evaluate(() => document.fonts.ready)
      const codename = pill(page).locator('.footer-codename')
      expect(await codename.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0)
      mkdirSync(screenshots, { recursive: true })
      await page.screenshot({ path: join(screenshots, `${width < 600 ? 'phone' : 'desktop'}-${colorScheme}.png`) })
      await pill(page).hover()
      await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
      await expect(page.locator('.release-hover-card .calendar-version')).toHaveAttribute('data-version-view', 'revealed')
      await expect(pill(page).locator('.footer-codename')).toBeVisible()
      expect(await codename.evaluate(el => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0)
      await insideFooter(page)
      await page.mouse.move(1, 1)
      await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
      await pill(page).focus()
      await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
      await page.keyboard.press('Enter')
      await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible()
    })
  }
}

test('flow is absent by default and a long codename cannot push the version off a phone', async ({ page }) => {
  await setup(page, { name: 'An exceptionally long release codename for a narrow screen' })
  await page.setViewportSize({ width: 320, height: 844 })
  await page.goto('/p/PHAROS')
  await expect(pill(page)).toBeVisible()
  await expect(page.locator('footer.app-footer .flow-slot')).toHaveCount(0)
  await expect(pill(page).locator('.new-badge')).toHaveCount(0)
  await insideFooter(page)
  await pill(page).hover()
  await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
  await insideFooter(page)
})

test('unknown count reserves no badge; failed versions still open history', async ({ page }) => {
  await setup(page, { fresh: true })
  await page.route('**/api/releases**', route => route.fulfill({ status: 503, json: { error: 'Unavailable' } }))
  await page.goto('/')
  await expect(pill(page).locator('.new-badge')).toHaveCount(0)
  await expect(pill(page)).toHaveAccessibleName(/new releases since your last visit$/)
  await page.route('**/api/version', route => route.fulfill({ status: 503 }))
  await page.reload()
  await expect(pill(page).locator('.fallback')).toHaveText('Version unavailable')
  await expect(pill(page)).toHaveAccessibleName('Release history, version unavailable')
  await pill(page).click()
  await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible()
})

test('the footer stays at rest even when motion is enabled', async ({ page }) => {
  await setup(page)
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.goto('/')
  await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
  await pill(page).hover()
  await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
  await expect(version(page).locator('.ss')).toHaveCSS('max-width', '0px')
  await expect(page.locator('.release-hover-card .calendar-version .ss')).toHaveCSS('max-width', 'none')
})

// Hold /version until both footer sides and the project flow have settled.
// Loading remains usable without reserving the eventual name or badge width.
for (const width of [1440, 390, 320]) {
  for (const failed of [false, true]) {
    test(`signed-in loading keeps the release usable and the flow clear at ${width}, ${failed ? 'failure' : 'success'}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 })
      const history = await setup(page, { flow: true, fresh: true })
      let release!: () => void
      const held = new Promise<void>(resolve => { release = resolve })
      await page.route('**/api/version', async route => {
        await held
        await route.fulfill(failed ? { status: 503 } : { json: { version: history.current, scheme: 'inspr-calendar-v2', codename: 'An exceptionally long release codename' } })
      })
      await page.goto('/p/PHAROS')
      await expect(pill(page).locator('.new-badge')).toHaveText('3 new')
      await expect(pill(page).locator('.name-skeleton')).toBeVisible()
      await expect(version(page)).toHaveAttribute('aria-hidden', 'true')
      await expect(version(page)).toBeHidden()
      await page.evaluate(() => document.fonts.ready)
      release()
      if (failed) {
        await expect(pill(page).locator('.fallback')).toHaveText('Version unavailable')
        await expect(pill(page)).toHaveAccessibleName('Release history, version unavailable, 3 new since your last visit')
      } else {
        // On a phone the screen's summary takes the middle and the release gives up its time, keeping name and count (AEON-785).
        if (width <= 600) await expect(version(page)).toBeHidden()
        else await expect(version(page)).toBeVisible()
        await expect(pill(page).locator('.footer-codename')).toHaveText('An exceptionally long release codename')
        await expect(version(page)).not.toHaveAttribute('aria-hidden')
      }
      await insideFooter(page)
      await pill(page).click()
      await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible()
    })

    test(`signed-out loading reserves the plain version at ${width}, ${failed ? 'failure' : 'success'}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 844 })
      let release!: () => void
      const held = new Promise<void>(resolve => { release = resolve })
      await page.route('**/api/**', async route => {
        const path = new URL(route.request().url()).pathname
        if (path === '/api/version') {
          await held
          return route.fulfill(failed ? { status: 503 } : { json: { version: '261001130110.0.0', scheme: 'inspr-calendar-v2' } })
        }
        return route.fulfill({ status: path === '/api/me' ? 401 : 404, json: { error: 'Unauthorized', dev_mode: false } })
      })
      // Sign-in is bare. Agent registration is public and keeps the app footer.
      await page.goto('/agents/register-agent')
      const plain = page.locator('footer.app-footer .version-plain')
      await expect(plain.locator('.pill-skeleton')).toBeVisible()
      await expect(plain.locator('.calendar-version')).toHaveAttribute('aria-hidden', 'true')
      await page.evaluate(() => document.fonts.ready)
      const before = await bounds(plain)
      release()
      if (failed) await expect(plain.locator('.fallback')).toHaveText('Version unavailable')
      else await expect(plain.locator('.calendar-version')).toBeVisible()
      const after = await bounds(plain)
      expect(Math.abs(after.width - before.width)).toBeLessThan(1)
      expect(Math.abs(after.x - before.x)).toBeLessThan(1)
      await expect(pill(page)).toHaveCount(0)
      expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBe(0)
    })
  }
}

// Natural widths follow actual data; empty/unknown counts reserve no room.
for (const width of [1440, 390, 320]) {
  test(`new count takes room only while present at ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    const history = await setup(page, { flow: true, fresh: true })
    let releaseHistory!: () => void
    const historyHeld = new Promise<void>(resolve => { releaseHistory = resolve })
    await page.route('**/api/releases**', async route => { await historyHeld; await route.fulfill({ json: history }) })
    await page.goto('/p/PHAROS')
    await expect(pill(page).locator('.footer-codename')).toHaveText('Lucky Lune')
    const badge = pill(page).locator('.new-badge')
    await expect(badge).toHaveCount(0)
    const empty = await bounds(pill(page))
    releaseHistory()
    await expect(badge).toHaveText('3 new')
    expect((await bounds(pill(page))).width).toBeGreaterThan(empty.width)
    await insideFooter(page)
    await pill(page).click()
    const dialog = page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
    await expect(dialog).toBeVisible()
    await expect(badge).toHaveCount(0)
    await dialog.getByRole('button', { name: 'Close release history', exact: true }).click()
    await expect(dialog).toBeHidden()
    await expect(pill(page)).toHaveAccessibleName(`Release history, Lucky Lune, version ${history.current}`)
    await insideFooter(page)
  })
}

for (const named of [false, true]) {
  test(`the UI audit history selector opens a ${named ? 'named' : 'nameless'} release after history loads`, async ({ page }) => {
    const history = presentedHistory()
    if (!named) for (const release of history.releases) delete release.codename
    await mockWork(page, fixtures())
    await mockReleases(page, history)
    await page.goto('/')
    await expect(pill(page)).toHaveAccessibleName(`${named ? 'Release history, Full Fin' : 'Release history'}, version ${history.current}`)
    await page.getByRole('button', { name: RELEASE_HISTORY_NAME }).click()
    await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible()
  })
}

// Range rectangles include font descenders, unlike element boxes. Ellipsis
// intentionally clips horizontally; collapsed renderer segments paint no ink.
async function textFits(page: Page) {
  const result = await pill(page).evaluate(control => {
    const errors: string[] = []
    const walker = document.createTreeWalker(control, NodeFilter.SHOW_TEXT)
    let measured = 0, text: Node | null
    while ((text = walker.nextNode())) {
      if (!text.textContent?.trim()) continue
      let clip = text.parentElement
      while (clip && clip !== control && !/(hidden|clip|auto|scroll)/.test(`${getComputedStyle(clip).overflowX} ${getComputedStyle(clip).overflowY}`)) clip = clip.parentElement
      if (!clip) continue
      const box = clip.getBoundingClientRect()
      if (box.width < 1 || getComputedStyle(clip).visibility === 'hidden') continue
      const range = document.createRange()
      range.selectNodeContents(text)
      const rect = range.getBoundingClientRect()
      measured++
      const ellipsis = getComputedStyle(clip).textOverflow === 'ellipsis'
      if (rect.top < box.top - .5 || rect.bottom > box.bottom + .5 || (!ellipsis && (rect.left < box.left - .5 || rect.right > box.right + .5))) errors.push(`${text.textContent}: ${JSON.stringify({ rect: rect.toJSON(), clip: box.toJSON() })}`)
    }
    return { errors, measured }
  })
  expect(result.measured).toBeGreaterThan(5)
  expect(result.errors).toEqual([])
}

async function controlBoxes(page: Page) {
  return pill(page).evaluate(control => [control, ...control.querySelectorAll('.pill-face, .pill-face > *, .calendar-version')].map(el => {
    const r = el.getBoundingClientRect()
    return { x: r.x, y: r.y, width: r.width, height: r.height }
  }))
}

for (const colorScheme of ['light', 'dark'] as const) {
  for (const width of [1440, 390]) {
    for (const zoom of [1, 1.25, 1.5]) {
      test(`release text fits and controls never move: ${width}px, ${zoom * 100}%, ${colorScheme}`, async ({ page }) => {
        await page.setViewportSize({ width, height: 900 })
        await page.emulateMedia({ colorScheme })
        await setup(page, { name: 'Rugged Ratio', fresh: true })
        await page.goto('/')
        await expect(pill(page).locator('.new-badge')).toHaveText('3 new')
        await page.evaluate(async zoom => {
          document.documentElement.style.zoom = String(zoom)
          // CSS zoom does not resize viewport units like actual browser zoom.
          // Keep the shell within the viewport so the real pointer reaches it.
          ;(document.querySelector('#app') as HTMLElement).style.height = `${innerHeight / zoom}px`
          await document.fonts.ready
        }, zoom)
        await page.mouse.move(1, 1)
        await textFits(page)
        const before = await controlBoxes(page)
        expect((await bounds(pill(page))).height).toBeCloseTo((await bounds(page.locator('footer.app-footer'))).height, 1)
        await pill(page).hover()
        const card = page.locator('.release-hover-card')
        await expect(card).toBeVisible()
        await expect(card.locator('.eyebrow')).toHaveText('Running release')
        await expect(card.locator('.card-name')).toHaveText('Rugged Ratio')
        await expect(card.locator('.card-meta')).toHaveText(/^Released .+ UTC · .+/)
        await expect(card.locator('.card-new')).toHaveText('3 new')
        expect((await bounds(card)).y + (await bounds(card)).height).toBeLessThan((await bounds(pill(page))).y)
        await expect(card.locator('.calendar-version .ss')).toHaveCSS('max-width', 'none')
        await expect(version(page)).toHaveAttribute('data-version-view', 'pretty')
        await textFits(page)
        expect(await controlBoxes(page)).toEqual(before)
        await page.mouse.move(1, 1)
        await expect(card).toHaveCount(0)
        await page.keyboard.press('Tab')
        await pill(page).focus()
        await expect(card).toBeVisible()
        await textFits(page)
        expect(await controlBoxes(page)).toEqual(before)
        await page.keyboard.press('Escape')
        await expect(card).toHaveCount(0)
        await page.keyboard.press('Enter')
        await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })).toBeVisible()
      })
    }
  }
}

for (const width of [1440, 390]) {
  test(`long names ellipsize at their cap with uncut descenders at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    const name = 'Quietly Quarrelsome Quokka with gypqj descenders'
    await setup(page, { name })
    await page.goto('/')
    const label = pill(page).locator('.footer-codename')
    await expect(label).toHaveText(name)
    await expect(label).toHaveCSS('max-width', width === 390 ? '104px' : '168px')
    expect(await label.evaluate(el => el.scrollWidth)).toBeGreaterThan(await label.evaluate(el => el.clientWidth))
    await textFits(page)
    await pill(page).hover()
    await expect(page.locator('.release-hover-card .card-name')).toHaveText(name)
  })
}

test('short pointer visits and touch do not open the hover card; keyboard focus is immediate', async ({ page }) => {
  await setup(page)
  await page.goto('/')
  await expect(version(page)).toBeVisible()
  const card = page.locator('.release-hover-card')
  await pill(page).dispatchEvent('pointerenter', { pointerType: 'mouse' })
  await page.waitForTimeout(200)
  await expect(card).toHaveCount(0)
  await pill(page).dispatchEvent('pointerleave', { pointerType: 'mouse' })
  await page.waitForTimeout(220)
  await expect(card).toHaveCount(0)
  await pill(page).dispatchEvent('pointerenter', { pointerType: 'touch' })
  await page.waitForTimeout(420)
  await expect(card).toHaveCount(0)
  await page.keyboard.press('Tab')
  await pill(page).focus()
  await expect(card).toBeVisible({ timeout: 250 })
  await pill(page).dispatchEvent('pointerleave', { pointerType: 'mouse' })
  await expect(card).toBeVisible()
  await page.keyboard.press('Tab')
  await expect(card).toHaveCount(0)
})
