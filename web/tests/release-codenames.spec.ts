// SPDX-License-Identifier: AGPL-3.0-only
// AEON-430: every release has a sci-fi codename from its sequence, and the
// marketing name is front and centre: the row's title, the detail's heading,
// the footer's pill. AEON-488 keeps the list name beside a separate Pretty
// version; the detail and footer retain their name-to-version reveal.
import { test, expect, type Page } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, me, mockWork } from './work-fixtures'
import { CODENAMES, mockReleases, presentedHistory } from './releases-fixtures'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const options = (page: Page) => page.getByRole('grid', { name: 'Releases, newest first' }).getByRole('row')

async function open(page: Page, version?: string, now?: number) {
  await mockWork(page, fixtures())
  const history = presentedHistory(now)
  Object.assign(history.releases.find(r => r.state === 'reserved')!, { release_sequence: 54, codename: 'Bold Booster' })
  await mockReleases(page, history)
  await page.goto(`/releases/${version ?? history.releases[0].version}`)
  await expect(sheet(page)).toBeVisible()
  return history
}

test('release list screenshot', async ({ page }) => {
  test.skip(!process.env.RELEASE_LIST_SHOTS, 'Screenshots are opt-in review artifacts.')
  await page.setViewportSize({ width: 1600, height: 1000 })
  const now = new Date('2026-10-01T14:01:10Z').getTime()
  await page.clock.install({ time: new Date(now) })
  await open(page, undefined, now)
  await mkdir(process.env.RELEASE_LIST_SHOTS!, { recursive: true })
  await page.mouse.move(1, 1)
  await sheet(page).locator('.listbox').focus()
  await expect(sheet(page).locator('.row').first()).toBeInViewport()
  await page.screenshot({ path: join(process.env.RELEASE_LIST_SHOTS!, `${process.env.RELEASE_LIST_SHOT_LABEL ?? 'after'}-desktop.png`) })
})

test('the marketing name leads every row; codename and Pretty version are both visible', async ({ page }) => {
  const history = await open(page)
  // Newest: the name leads, a presented theme is the subtitle.
  const newest = options(page).nth(0)
  await expect(newest.locator('.rn-name')).toHaveText(CODENAMES[5])
  await expect(newest.locator('.headline')).toHaveText('Releases with a name')
  // A reservation keeps its sequence's name beside its state.
  const reserved = options(page).nth(2)
  await expect(reserved.locator('.rn-name')).toHaveText('Bold Booster')
  await expect(reserved.locator('.headline')).toHaveText('Reserved, never published')
  // No brief: the name and version still both appear, including on row hover.
  const untitled = options(page).nth(3)
  await expect(untitled.locator('.rn-name')).toHaveText(CODENAMES[3])
  await expect(untitled.locator('.headline')).toHaveCount(0)
  const version = untitled.getByRole('button', { name: `Copy version ${history.releases[3]!.version}`, exact: true })
  await expect(version).toHaveAttribute('data-version-view', 'pretty')
  await expect(version.locator('.version-pretty')).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
  await expect(version.locator('[data-version-character="pretty"]').last()).toHaveCSS('opacity', '1')
  await expect(untitled.locator('.rn-stamp')).toHaveCount(0)
  await untitled.hover()
  await expect(untitled.locator('.rn-name')).toHaveCSS('opacity', '1')
  await expect(version).toHaveAttribute('data-version-view', 'pretty')
  for (const row of await options(page).all()) {
    await expect(row.locator('.rn-name')).toBeVisible()
    await expect(row.locator('.row-version .version-pretty')).toBeVisible()
  }
})

test('the list version uses the dock crossfade without hiding the codename or shifting the heading', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  const history = await open(page)
  const row = options(page).nth(1)
  const version = row.getByRole('button', { name: `Copy version ${history.releases[1]!.version}`, exact: true })
  const canonical = version.locator('[data-version-character="canonical"]')
  const pretty = version.locator('[data-version-character="pretty"]')
  await sheet(page).locator('.shell').evaluate(async el => { await Promise.all(el.getAnimations().map(animation => animation.finished)) })
  await row.evaluate(async el => { await Promise.all(el.getAnimations().map(animation => animation.finished)) })
  // Playwright may scroll the 44 px target into view before hovering it.
  await version.scrollIntoViewIfNeeded()
  const bounds = await version.boundingBox()
  await version.hover()
  await expect(version).toHaveAttribute('data-version-view', 'revealed')
  await expect(canonical.last()).toHaveCSS('opacity', '1')
  await expect(pretty.last()).toHaveCSS('opacity', '0')
  await expect(version.locator('.version-canonical')).toHaveText(history.releases[1]!.version)
  await expect(row.locator('.rn-name')).toHaveCSS('opacity', '1')
  expect(await version.boundingBox()).toEqual(bounds)
  const transitions = await canonical.evaluateAll(nodes => nodes.map(node => {
    const style = getComputedStyle(node)
    return [style.transitionDuration, style.transitionDelay, style.transitionTimingFunction]
  }))
  expect(transitions[0]).toEqual(['0.42s', '0s', 'ease-in-out'])
  expect(transitions.at(-1)).toEqual(['0.42s', '0.58s', 'ease-in-out'])
  await page.mouse.move(1, 1)
  await sheet(page).getByRole('grid').focus()
  await expect(version).toHaveAttribute('data-version-view', 'pretty')
  await expect(canonical.last()).toHaveCSS('opacity', '0')
  await expect(pretty.last()).toHaveCSS('opacity', '1')
})

test('focus reveals the list version; click, Enter and Space copy without selecting that row', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const history = await open(page)
  const row = options(page).nth(3)
  const version = row.getByRole('button', { name: `Copy version ${history.releases[3]!.version}`, exact: true })
  const address = page.url()
  await version.focus()
  await expect(version).toHaveAttribute('data-version-view', 'revealed')
  await expect(version.locator('[data-version-character="canonical"]').last()).toHaveCSS('opacity', '1')
  expect(await version.locator('[data-version-character="canonical"]').evaluateAll(nodes => nodes.every(node => (node as HTMLElement).style.transition === 'none'))).toBe(true)
  for (const action of ['Enter', 'Space', 'click']) {
    if (action === 'click') await version.click()
    else await version.press(action)
    await expect(version).toHaveAttribute('data-copy-state', 'copied')
    await expect(row.getByRole('status')).toHaveText('Version copied')
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(history.releases[3]!.version)
    await expect(row).toHaveAttribute('aria-selected', 'false')
    expect(page.url()).toBe(address)
  }
  // Clicking the persistent name still selects, and j/k still move the list.
  await row.locator('.rn-name').click()
  await expect(row).toHaveAttribute('aria-selected', 'true')
  await expect(page).toHaveURL(`/releases/${history.releases[3]!.version}`)
  await sheet(page).getByRole('grid').focus()
  await page.keyboard.press('k')
  await expect(options(page).nth(2)).toHaveAttribute('aria-selected', 'true')
})

test('release list copy controls have accessible interactive row semantics', async ({ page }) => {
  await open(page)
  const results = await new AxeBuilder({ page }).include('.releases .list-pane').withRules(['nested-interactive', 'aria-required-children', 'aria-required-parent']).analyze()
  expect(results.violations.map(violation => ({ id: violation.id, targets: violation.nodes.map(node => node.target) }))).toEqual([])
})

for (const width of [320, 390, 600, 1600]) {
  test(`the list version is right-aligned and wraps only when needed at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    await open(page)
    if (width <= 760) {
      // Phone detail hides the grid, but keeps its selected row for Back.
      await expect(sheet(page).getByRole('row', { selected: true, includeHidden: true })).toHaveCount(1)
      await sheet(page).getByRole('button', { name: 'All releases' }).click()
    }
    const row = options(page).first()
    const name = row.locator('.rn-name')
    const version = row.locator('.row-version')
    const heading = row.locator('.line1')
    await expect(row.locator('.row-identity .current-tag')).toBeVisible()
    const [nameBounds, versionBounds, headingBounds] = await Promise.all([name.boundingBox(), version.boundingBox(), heading.boundingBox()])
    expect(Math.abs(versionBounds!.x + versionBounds!.width - headingBounds!.x - headingBounds!.width)).toBeLessThanOrEqual(1)
    if (width <= 390) expect(versionBounds!.y).toBeGreaterThanOrEqual(nameBounds!.y + nameBounds!.height)
    else expect(Math.abs(versionBounds!.y + versionBounds!.height / 2 - nameBounds!.y - nameBounds!.height / 2)).toBeLessThanOrEqual(1)
    const button = version.getByRole('button')
    await sheet(page).getByRole('grid').focus()
    await page.keyboard.press('Tab')
    await expect(button).toBeFocused()
    await expect(button.locator('[data-version-character="canonical"]').last()).toHaveCSS('opacity', '1')
    expect((await button.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    expect(await sheet(page).evaluate(el => el.scrollWidth - el.clientWidth)).toBe(0)
    expect(await row.evaluate(el => el.scrollWidth - el.clientWidth)).toBe(0)
    if (process.env.RELEASE_LIST_SHOTS && width <= 390) {
      await mkdir(process.env.RELEASE_LIST_SHOTS, { recursive: true })
      await page.screenshot({ path: join(process.env.RELEASE_LIST_SHOTS, `after-${width}px.png`) })
    }
  })
}

test('the detail heading is the name; the number is a quiet line in the notes; search finds a name', async ({ page }) => {
  const history = await open(page)
  const detail = sheet(page).locator('.detail')
  await expect(detail.locator('h2 .rn-name')).toHaveText(CODENAMES[5])
  // No "Release 113"-style number in the header; Details shows it in the technical line (AEON-488).
  await expect(detail.locator('.eyebrow.top')).not.toContainText(/\d/)
  await expect(detail.locator('.tech')).toHaveCount(0)
  await sheet(page).getByRole('radio', { name: 'Details' }).click()
  await expect(detail.locator('.tech')).toContainText(`PAIMOS 7 · Release ${history.releases[0].release_sequence} · stable · ${history.releases[0].version}`)
  await sheet(page).getByRole('radio', { name: 'Highlights' }).click()
  await page.getByRole('searchbox', { name: 'Search releases' }).fill('cyan')
  await expect(options(page)).toHaveCount(1)
  await expect(options(page).first().locator('mark')).toHaveText('Cyan')
  await expect(sheet(page).locator('.detail h2 .rn-name')).toHaveText('Cyan Cell')
})

test('the stamp is reachable by keyboard and described to a screen reader', async ({ page }) => {
  await open(page)
  const heading = sheet(page).locator('.detail h2 .release-name')
  await expect(heading).toHaveAttribute('aria-describedby', /.+/)
  const tip = sheet(page).locator(`[id="${await heading.getAttribute('aria-describedby')}"]`)
  await expect(tip).toHaveAttribute('role', 'tooltip')
  await expect(tip.locator('.calendar-version')).toHaveAttribute('aria-label', /^\d{12}\.0\.0 · 20\d\d-/)
  await heading.focus()
  await expect(tip).toHaveCSS('opacity', '1')
})

test('phones show the name without horizontal scroll', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open(page)
  await page.getByRole('button', { name: 'All releases' }).click()
  await expect(options(page).nth(3).locator('.rn-name')).toHaveText(CODENAMES[3])
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})

test('the footer names the running release; hover reveals its version without moving anything', async ({ page }) => {
  await mockWork(page, fixtures())
  const history = presentedHistory()
  await mockReleases(page, history)
  await page.route('**/api/version', route => route.fulfill({ json: { version: history.current, scheme: 'inspr-calver-3', codename: CODENAMES[5] } }))
  await page.goto('/')
  const pill = page.locator('footer.app-footer .footer-name')
  const name = pill.locator('.rn-name')
  await expect(name).toHaveText(CODENAMES[5])
  await expect(pill).toHaveAccessibleName(new RegExp(`${CODENAMES[5]}, version ${history.current.replace(/\./g, '\\.')}`))
  // No release number anywhere in the pill's text.
  expect(await pill.innerText()).not.toMatch(/Release \d|\b\d+\.\d+\b/)
  const before = (await pill.boundingBox())!
  await pill.hover()
  await expect(pill.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await expect(pill.locator('.rn-stamp .calendar-version')).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
  const after = (await pill.boundingBox())!
  expect(Math.abs(after.width - before.width)).toBeLessThanOrEqual(1)
  await page.mouse.move(5, 5)
  await expect(pill.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  // The pill still opens the release history.
  await pill.click()
  await expect(sheet(page)).toBeVisible()
})

// The calendar version a stamp describes: canonical, then its UTC date-time.
const STAMP = /^\d{12}\.0\.0 · 20\d\d-\d\d-\d\d \d\d:\d\d:\d\d UTC$/

// AEON-430 fix round 1: the surfaces that still drew the calendar version at rest.
async function openNamed(page: Page) {
  await mockWork(page, fixtures())
  const history = presentedHistory()
  await mockReleases(page, history, { codename: CODENAMES[5] })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(page.locator('footer.app-footer .footer-name .rn-name')).toHaveText(CODENAMES[5])
  return history
}
const accountMenu = async (page: Page) => {
  await page.getByRole('button', { name: `Account for ${me.name}` }).click()
  const menu = page.getByRole('menu', { name: 'Account' })
  await expect(menu).toBeVisible()
  return menu
}

test('the account menu names the release; focus reveals its version, and the item still copies it', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const history = await openNamed(page)
  const menu = await accountMenu(page)
  const item = menu.getByRole('menuitem', { name: new RegExp(`^${CODENAMES[5]}, version ${history.current.replace(/\./g, '\\.')} — Copy version$`) })
  await expect(item).toBeVisible()
  await expect(item.locator('.rn-name')).toHaveText(CODENAMES[5])
  // No calendar version at rest: the name shows, the stamp waits.
  await expect(item.locator('.rn-name')).toHaveCSS('opacity', '1')
  await expect(item.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  // It is a menu item: roving focus reaches it with the arrow keys, and focus reveals the stamp.
  await menu.getByRole('menuitem', { name: 'Sign out' }).focus()
  await page.keyboard.press('ArrowDown')
  await expect(item).toBeFocused()
  await expect(item.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await expect(item.locator('.rn-stamp .calendar-version')).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
  // The control, not the text inside it, is described by the stamp.
  await expect(item).toHaveAccessibleDescription(STAMP)
  await page.keyboard.press('Enter')
  await expect(item).toHaveAttribute('data-copy-state', 'copied')
  await expect(item.locator('[role="status"]')).toHaveText('Copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(history.current)
  // Menu navigation is unchanged: the next item is the history.
  await page.keyboard.press('ArrowDown')
  await expect(menu.getByRole('menuitem', { name: 'Release history' })).toBeFocused()
  await expect(item.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  // Hover reveals too.
  await item.hover()
  await expect(item.locator('.rn-stamp')).toHaveCSS('opacity', '1')
})

test('the footer and detail describe their stamp; the list version names its independent copy action', async ({ page }) => {
  const history = await openNamed(page)
  const pill = page.locator('footer.app-footer .footer-name')
  await expect(pill).toHaveAccessibleDescription(STAMP)
  // The name inside the control is not the described element.
  await expect(pill.locator('.release-name')).not.toHaveAttribute('aria-describedby', /.+/)
  await pill.click()
  await expect(sheet(page)).toBeVisible()
  const row = options(page).nth(0)
  await expect(row.locator('.rn-stamp')).toHaveCount(0)
  await expect(row.getByRole('button', { name: `Copy version ${history.current}`, exact: true })).toBeVisible()
  // Standing alone, the name is focusable and carries its own description.
  const heading = sheet(page).locator('.detail h2 .release-name')
  await expect(heading).toHaveAttribute('tabindex', '0')
  await expect(heading).toHaveAccessibleDescription(STAMP)
  // The stamp describes; it is not part of the name (a heading is read by its content).
  await expect(sheet(page).locator('#release-detail-title')).toHaveAccessibleName(CODENAMES[5])
  expect(history.current).toBeTruthy()
})

test('a description the control already had stays beside the stamp, and goes with the name', async ({ page }) => {
  await openNamed(page)
  const pill = page.locator('footer.app-footer .footer-name')
  const stampId = await pill.evaluate(el => el.getAttribute('aria-describedby'))
  expect(stampId).toMatch(/\S+/)
  await pill.evaluate(el => el.setAttribute('aria-describedby', 'owner-hint'))
  // The pill's owner rewrote the attribute; the stamp is put back next to it.
  await expect.poll(() => pill.evaluate(el => el.getAttribute('aria-describedby'))).toBe(`owner-hint ${stampId}`)
})
