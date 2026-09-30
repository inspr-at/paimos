// SPDX-License-Identifier: AGPL-3.0-only
// AEON-430: every release has a sci-fi codename from its sequence, and the
// marketing name is front and centre: the row's title, the detail's heading,
// the footer's pill. The calendar version appears only on hover or focus, in
// the footer's chip style; the release number stays a quiet line in the notes.
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork } from './work-fixtures'
import { CODENAMES, mockReleases, presentedHistory } from './releases-fixtures'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const options = (page: Page) => page.getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option')

async function open(page: Page, version?: string) {
  await mockWork(page, fixtures())
  const history = presentedHistory()
  Object.assign(history.releases.find(r => r.state === 'reserved')!, { release_sequence: 54, codename: 'Bold Booster' })
  await mockReleases(page, history)
  await page.goto(`/releases/${version ?? history.releases[0].version}`)
  await expect(sheet(page)).toBeVisible()
  return history
}

test('the marketing name leads every row; the brief is its subtitle, and the version waits for hover', async ({ page }) => {
  await open(page)
  // Newest: the name leads, a presented theme is the subtitle.
  const newest = options(page).nth(0)
  await expect(newest.locator('.rn-name')).toHaveText(CODENAMES[5])
  await expect(newest.locator('.headline')).toHaveText('Releases with a name')
  // A reservation keeps its sequence's name beside its state.
  const reserved = options(page).nth(2)
  await expect(reserved.locator('.rn-name')).toHaveText('Bold Booster')
  await expect(reserved.locator('.headline')).toHaveText('Reserved, never published')
  // No brief: the name alone, no version text at rest.
  const untitled = options(page).nth(3)
  await expect(untitled.locator('.rn-name')).toHaveText(CODENAMES[3])
  await expect(untitled.locator('.headline')).toHaveCount(0)
  await expect(untitled.locator('.rn-stamp')).toHaveCSS('opacity', '0')
  await untitled.hover()
  await expect(untitled.locator('.rn-stamp')).toHaveCSS('opacity', '1')
  await expect(untitled.locator('.rn-name')).toHaveCSS('opacity', '0')
  await expect(untitled.locator('.rn-stamp .calendar-version')).toContainText(/^\d\d·\d\d·\d\d \d\d:\d\d/)
})

test('the detail heading is the name; the number is a quiet line in the notes; search finds a name', async ({ page }) => {
  const history = await open(page)
  const detail = sheet(page).locator('.detail')
  await expect(detail.locator('h2 .rn-name')).toHaveText(CODENAMES[5])
  // No "Release 113"-style number in the header.
  await expect(detail.locator('.eyebrow.top')).not.toContainText(/\d/)
  await expect(detail.locator('.tech')).toHaveText(`Release ${history.releases[0].release_sequence} · ${history.releases[0].version}`)
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

test('the footer pill, a history row and the detail heading describe themselves by their stamp', async ({ page }) => {
  const history = await openNamed(page)
  const pill = page.locator('footer.app-footer .footer-name')
  await expect(pill).toHaveAccessibleDescription(STAMP)
  // The name inside the control is not the described element.
  await expect(pill.locator('.release-name')).not.toHaveAttribute('aria-describedby', /.+/)
  await pill.click()
  await expect(sheet(page)).toBeVisible()
  const row = options(page).nth(0)
  await expect(row).toHaveAccessibleDescription(STAMP)
  await expect(row.locator('.release-name')).not.toHaveAttribute('aria-describedby', /.+/)
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
