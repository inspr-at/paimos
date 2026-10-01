// SPDX-License-Identifier: AGPL-3.0-only
// The footer bar and the release history: the version pill, the sheet and its
// deep links, keys, compare, search and filters, what is new since the last
// visit, evidence, and the notice when the server runs a newer version.
import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'

// AEON-309: the CalVer3 renderer names a version by its canonical value and UTC
// time; an interactive one adds its copy action.
function versionLabel(version: string, copy = true): string {
  const m = /^(\d\d)(\d\d)(\d\d)(\d\d)(\d\d)(\d\d)\.0\.0$/.exec(version)
  if (!m) throw new Error(`not a calendar version: ${version}`)
  return `${version} · 20${m[1]}-${m[2]}-${m[3]} ${m[4]}:${m[5]}:${m[6]} UTC${copy ? ' — Copy version' : ''}`
}

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const options = (page: Page) => page.getByRole('listbox', { name: 'Releases, newest first' }).getByRole('option')
const pill = (page: Page) => page.getByRole('button', { name: /^Release history, version / })
const escaped = (v: string) => v.replace(/\./g, '\\.')

async function setup(page: Page, options: { lastSeen?: string; running?: string; bigProject?: number } = {}) {
  const history = releaseHistory()
  const data = fixtures({ bigProject: options.bigProject })
  if (options.lastSeen) data.preferences.releases = { last_seen: options.lastSeen }
  const calls = await mockWork(page, data)
  const state = await mockReleases(page, history, { running: options.running })
  return { history, data, calls, state }
}

test('the footer bar holds its own row: pages and docked panels end above it', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  await page.goto('/p/PHAROS/PHAROS-11')
  const panel = page.locator('.ticket-ws.panel')
  await expect(panel).toBeVisible()
  const foot = (await page.locator('footer.app-footer').boundingBox())!
  expect(foot.height).toBe(40)
  expect(foot.y + foot.height).toBe(900)
  const box = (await panel.boundingBox())!
  expect(box.y + box.height).toBeLessThanOrEqual(foot.y - 8)
  await expect(page.locator('footer.app-footer')).toContainText('PAIMOS AEON')
})

test('on phones the footer folds away reading down and returns on the way up', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page, { bigProject: 30 })
  await page.goto('/p/AEON')
  const footer = page.locator('footer.app-footer')
  await expect(footer).toBeVisible()
  expect((await footer.boundingBox())!.height).toBe(44)
  await expect.poll(() => page.locator('main').evaluate(el => el.scrollHeight - el.clientHeight)).toBeGreaterThan(600)
  await page.mouse.move(195, 420)
  await page.mouse.wheel(0, 400)
  await expect(footer).toHaveClass(/hidden/)
  await page.waitForTimeout(350)
  await page.mouse.wheel(0, -200)
  await expect(footer).not.toHaveClass(/hidden/)
})

test('the mark in the release history goes home and leaves the history', async ({ page }) => {
  await setup(page)
  await page.goto('/p/PHAROS')
  await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
  await pill(page).click()
  await expect(sheet(page)).toBeVisible()
  await sheet(page).getByRole('link', { name: 'PAIMOS AEON home' }).click()
  await expect(page).toHaveURL(/\/$/)
  await expect(sheet(page)).toHaveCount(0)
})

test('the version pill opens the history over the page, and Esc brings the page back', async ({ page }) => {
  const errors = watchErrors(page)
  const { history } = await setup(page)
  await page.goto('/p/PHAROS')
  await expect(page.locator('tr.ticket-row:not(.ghost)').first()).toBeVisible()
  await pill(page).click()
  await expect(sheet(page)).toBeVisible()
  await expect(page).toHaveURL(new RegExp(`/p/PHAROS(/tickets)?\\?releases=${escaped(history.current)}$`))
  await expect(page).toHaveTitle('Releases · PAIMOS AEON')
  await expect(options(page)).toHaveCount(history.releases.length)
  await expect(options(page).first()).toHaveAttribute('aria-selected', 'true')
  await expect(options(page).first()).toContainText('Current')
  // Row counts follow the blocks in the detail. Without a told ticket, a feature or fix commit counts as other.
  await expect(options(page).first().getByRole('img', { name: '3 other changes', exact: true })).toHaveAttribute('data-tip', '3 other changes')
  await expect(options(page).first().getByRole('img', { name: '1 feature', exact: true })).toHaveCount(0)
  await expect(options(page).nth(1).getByRole('img', { name: '4 other changes', exact: true })).toBeVisible()
  await expect(sheet(page).getByRole('heading', { level: 2, name: history.releases.find(r => r.version === history.current)!.codename })).toBeVisible()
  // AEON-488: the title is the live release's codename under a product eyebrow, with one status line.
  const live = history.releases.find(r => r.version === history.current)!
  await expect(sheet(page).locator('.head .eyebrow')).toHaveText('PAIMOS AEON · Releases')
  await expect(sheet(page).getByRole('heading', { level: 1, name: live.codename })).toBeVisible()
  await expect(sheet(page).locator('.head .status-line')).toHaveText(/^Live here since (\w{3} )?\d\d:\d\d · 50 min$/)
  await expect(sheet(page).locator('.detail .live-line')).toHaveText(/^Live here since (\w{3} )?\d\d:\d\d · (published|tagged) /)
  await page.keyboard.press('Escape')
  await expect(sheet(page)).toHaveCount(0)
  await expect(page).toHaveURL(/\/p\/PHAROS(\/tickets)?$/)
  await expect(pill(page)).toBeFocused()
  expect(errors).toEqual([])
})

test('deep links open one release, and the address follows the selection', async ({ page }) => {
  const { history } = await setup(page)
  const target = history.releases[3]
  await page.goto(`/releases/${target.version}`)
  await expect(sheet(page)).toBeVisible()
  await expect(options(page).nth(3)).toHaveAttribute('aria-selected', 'true')
  // AEON-305: the tag message is evidence, not a title; the key stays as a chip.
  await expect(sheet(page).locator('.detail .headline')).toHaveCount(0)
  await expect(sheet(page).getByText('Historical tag headline')).toHaveCount(0)
  await expect(sheet(page).locator('.detail .tickets')).toContainText('PAI-1057')
  // AEON-430: its row title is the codename, never the tag message.
  await expect(options(page).nth(3).locator('.rn-name')).toHaveText(target.codename!)
  await expect(options(page).nth(3).locator('.headline')).toHaveCount(0)
  await page.keyboard.press('k')
  await expect(page).toHaveURL(`/releases/${history.releases[2].version}`)
  // Opened from a link, closing leads to Projects.
  await page.keyboard.press('Escape')
  await expect(sheet(page)).toHaveCount(0)
  await expect(page).toHaveURL('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
})

test('keys: j and k move, Enter opens, e shows evidence, ? lists keys, / searches, c compares, Esc steps back', async ({ page }) => {
  const { history } = await setup(page)
  // Selecting a release writes the address through a navigation that refreshes
  // the session, so the address lands after the cursor has moved. Hold those
  // refreshes and release them only once Compare is showing: the late address
  // used to be taken as a new selection and the comparison closed under the keys.
  let armed = false
  let navigations = 0
  let releaseNavigations: () => void = () => {}
  let navigationGate = Promise.resolve()
  await page.route(/\/api\/me(?:\?|$)/, async route => {
    if (!armed) { await route.fallback(); return }
    navigations++
    try {
      await navigationGate
      await route.fallback()
    } finally { navigations-- }
  })
  const historyReady = page.waitForResponse(res => res.ok() && new URL(res.url()).pathname === '/api/releases')
  await page.goto('/releases')
  await historyReady
  await expect(options(page).first()).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('listbox', { name: 'Releases, newest first' })).toBeFocused()
  // The first selection's address has landed. Later ones wait until Compare is open.
  await expect(page).toHaveURL(new RegExp(`/releases/${escaped(history.current)}(?:$|\\?)`))
  navigationGate = new Promise(resolve => { releaseNavigations = resolve })
  armed = true
  await page.keyboard.press('j')
  await expect(options(page).nth(1)).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
  await expect(sheet(page).getByRole('heading', { level: 2, name: history.releases[1].codename })).toBeFocused()
  await page.keyboard.press('e')
  await expect(sheet(page).getByRole('button', { name: /^Evidence/ })).toHaveAttribute('aria-expanded', 'true')
  await page.keyboard.press('?')
  await expect(page.getByRole('dialog', { name: 'Release history keys' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'Release history keys' })).toHaveCount(0)
  await expect(sheet(page)).toBeVisible()

  await page.keyboard.press('/')
  const search = sheet(page).getByRole('searchbox', { name: 'Search releases' })
  await expect(search).toBeFocused()
  await page.keyboard.type('hetzner')
  await expect(options(page)).toHaveCount(1)
  await expect(sheet(page).locator('.result-count')).toHaveText(`1 of ${history.releases.length}`)
  await expect(sheet(page).locator('.detail mark')).toHaveText(['Hetzner'])
  await page.keyboard.press('Escape')
  await expect(options(page)).toHaveCount(history.releases.length)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('listbox', { name: 'Releases, newest first' })).toBeFocused()

  // Compare from the selected release; j and k move the other end.
  await page.keyboard.press('c')
  const compare = sheet(page).locator('section.compare')
  await expect(compare).toBeVisible()
  await expect(compare.getByRole('heading', { level: 2 })).toContainText('From')
  await expect(compare.locator('.facts')).toContainText('1 release')
  await page.keyboard.press('j')
  await page.keyboard.press('j')
  // The address catches up here, after both ends are chosen, and the comparison stays.
  await expect.poll(() => navigations).toBeGreaterThan(0)
  releaseNavigations()
  await expect(page).toHaveURL(new RegExp(`/releases/${escaped(history.releases[1].version)}(?:$|\\?)`))
  await expect.poll(() => navigations).toBe(0)
  await expect(compare).toBeVisible()
  await expect(compare.locator('.facts')).toContainText('2 releases')
  await expect(compare.locator('.facts')).toContainText('3 tickets')
  await expect(compare.locator('.changes')).toContainText('Other changes')
  // AEON-305: no tag message outside Evidence; without a theme or benefit a
  // release shows its version and, since AEON-430, its codename.
  const included = compare.getByRole('region', { name: 'Releases in this range' }).getByRole('listitem')
  await expect(included).toHaveCount(2)
  await expect(included).not.toContainText(['Wide lists and columns', 'Retry a busy BEGIN in release acceptance'])
  await expect(included.locator('.rn-name')).toHaveText([history.releases[1].codename!, history.releases[3].codename!])
  await compare.getByRole('button', { name: 'Swap' }).click()
  await expect(compare.locator('.facts')).toContainText('2 releases')
  await page.keyboard.press('Escape')
  await expect(compare).toHaveCount(0)
  await page.keyboard.press('Escape')
  await expect(sheet(page)).toHaveCount(0)
})

test('compare follows Back, Forward and an in-app release link', async ({ page }) => {
  const { history } = await setup(page)
  const path = (index: number) => `/releases/${history.releases[index].version}`
  const selected = (index: number) => expect(options(page).nth(index)).toHaveAttribute('aria-selected', 'true')
  const compare = sheet(page).locator('section.compare')
  // Client-side, like a release link. A full load would remount the sheet and hide the bug.
  const openRelease = async (index: number) => {
    const mark = await page.evaluate(() => {
      const state = window as unknown as { __releaseNav?: number }
      state.__releaseNav = (state.__releaseNav ?? 0) + 1
      return state.__releaseNav
    })
    await page.evaluate(async url => {
      const { router } = await import('/src/router.ts')
      await router.push(url)
    }, path(index))
    expect(await page.evaluate(() => (window as unknown as { __releaseNav?: number }).__releaseNav)).toBe(mark)
  }
  await page.goto(path(0))
  await selected(0)
  await openRelease(3)
  await expect(page).toHaveURL(path(3))
  await selected(3)
  await page.getByRole('listbox', { name: 'Releases, newest first' }).focus()
  await page.keyboard.press('c')
  await expect(compare).toBeVisible()
  await page.goBack()
  await expect(page).toHaveURL(path(0))
  await expect(compare).toHaveCount(0)
  await selected(0)
  await page.getByRole('listbox', { name: 'Releases, newest first' }).focus()
  await page.keyboard.press('c')
  await expect(compare).toBeVisible()
  await page.goForward()
  await expect(page).toHaveURL(path(3))
  await expect(compare).toHaveCount(0)
  await selected(3)
  await page.getByRole('listbox', { name: 'Releases, newest first' }).focus()
  await page.keyboard.press('c')
  await expect(compare).toBeVisible()
  await openRelease(1)
  await expect(page).toHaveURL(path(1))
  await expect(compare).toHaveCount(0)
  await selected(1)
})

test('a cancelled selection does not swallow the next visit to that release', async ({ page }) => {
  const { history } = await setup(page)
  const path = (index: number) => `/releases/${history.releases[index].version}`
  const selected = (index: number) => expect(options(page).nth(index)).toHaveAttribute('aria-selected', 'true')
  const compare = sheet(page).locator('section.compare')
  // Hold the session refresh so j's address never lands. k returns to the
  // release already in the address, which cancels j. That cancelled version
  // must not stay an echo, or the next visit would leave Compare open.
  let armed = false
  let requests = 0
  let releaseGate: () => void = () => {}
  const gate = new Promise<void>(resolve => { releaseGate = resolve })
  await page.route(/\/api\/me(?:\?|$)/, async route => {
    if (!armed) { await route.fallback(); return }
    requests++
    try {
      await gate
      await route.fallback()
    } finally { requests-- }
  })
  await page.goto(path(0))
  await selected(0)
  await expect(page.getByRole('listbox', { name: 'Releases, newest first' })).toBeFocused()
  armed = true
  await page.keyboard.press('j')
  await selected(1)
  await expect.poll(() => requests).toBe(1)
  await page.keyboard.press('k')
  await selected(0)
  await page.keyboard.press('c')
  await expect(compare).toBeVisible()
  await page.keyboard.press('j')
  await selected(2)
  armed = false
  releaseGate()
  await expect.poll(() => requests).toBe(0)
  await expect(page).toHaveURL(path(0))
  await expect(compare).toBeVisible()
  await page.evaluate(async url => {
    const { router } = await import('/src/router.ts')
    await router.push(url)
  }, path(1))
  await expect(page).toHaveURL(path(1))
  await expect(compare).toHaveCount(0)
  await selected(1)
})

test('filters follow the feature and fix blocks, and still keep releases with tickets', async ({ page }) => {
  await setup(page)
  await page.goto('/releases')
  await expect(options(page)).toHaveCount(7)
  const toggles = sheet(page).getByRole('group', { name: 'Show only releases with' })
  // These commits name tickets but none tells a benefit, so the rows show other, not features or fixes.
  await toggles.getByRole('button', { name: 'Features' }).click()
  await expect(sheet(page).getByText('0 of 7')).toBeVisible()
  await expect(sheet(page).getByRole('heading', { name: 'No release matches' })).toBeVisible()
  await toggles.getByRole('button', { name: 'Features' }).click()
  await toggles.getByRole('button', { name: 'Fixes' }).click()
  await expect(toggles.getByRole('button', { name: 'Fixes' })).toHaveAttribute('aria-pressed', 'true')
  await expect(sheet(page).getByText('0 of 7')).toBeVisible()
  await toggles.getByRole('button', { name: 'Fixes' }).click()
  await toggles.getByRole('button', { name: 'Tickets' }).click()
  await expect(options(page)).toHaveCount(5)
  await sheet(page).getByRole('searchbox', { name: 'Search releases' }).fill('nothing like this')
  await expect(sheet(page).getByRole('heading', { name: 'No release matches' })).toBeVisible()
  await sheet(page).getByRole('button', { name: 'Clear search and filters' }).click()
  await expect(options(page)).toHaveCount(7)
})

test('new since the last visit: a badge on the pill, highlighted releases, and the visit is remembered', async ({ page }) => {
  const history = releaseHistory()
  const { data } = await setup(page, { lastSeen: history.releases[4].version })
  await page.goto('/')
  await expect(page.locator('.new-badge')).toHaveText('3 new')
  await expect(pill(page)).toHaveAccessibleName(/, 3 new since your last visit$/)
  await pill(page).click()
  await expect(sheet(page).locator('.row.fresh')).toHaveCount(3)
  await expect(sheet(page).getByText('New since your last visit')).toBeVisible()
  await expect.poll(() => data.preferences.releases).toEqual({ last_seen: history.current })
  await page.keyboard.press('Escape')
  await expect(page.locator('.new-badge')).toHaveCount(0)
})

test('a first visit remembers the running version quietly', async ({ page }) => {
  const { data, history } = await setup(page)
  await page.goto('/')
  await expect(pill(page)).toBeVisible()
  await expect.poll(() => data.preferences.releases).toEqual({ last_seen: history.current })
  await expect(page.locator('.new-badge')).toHaveCount(0)
})

test('evidence: runs, commit and digest with copy, the rollback target, and what is not known', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const { history } = await setup(page)
  const previous = history.releases[1]
  await page.goto(`/releases/${previous.version}`)
  await expect(sheet(page).locator('.detail .badges')).toContainText('Rollback target')
  await sheet(page).getByRole('button', { name: /^Evidence/ }).click()
  await expect(sheet(page).getByRole('link', { name: /CI/ })).toHaveAttribute('href', previous.evidence.ci.url)
  await expect(sheet(page).getByRole('link', { name: previous.tag })).toHaveAttribute('href', previous.evidence.release_url)
  await sheet(page).getByRole('button', { name: 'Copy the image digest' }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(previous.evidence.image!.digest)
  await expect(sheet(page).getByRole('status').filter({ hasText: 'Image digest copied' })).toHaveCount(1)
  await expect(sheet(page).getByText(/Rolling back deploys the image digest above\./)).toBeVisible()
  // The evidence stays open while moving; gaps are named, not hidden.
  await page.keyboard.press('End')
  await expect(sheet(page).getByText('No GitHub release exists for this tag, so it has no publication time or image record.')).toBeVisible()
  await expect(sheet(page).locator('.run.failure')).toHaveText('failed')
})

test('ticket keys link to the tickets this workspace has; others stay plain', async ({ page }) => {
  const { history } = await setup(page)
  await page.goto(`/releases/${history.releases[3].version}`)
  await expect(sheet(page).locator('.detail .tickets')).toContainText('PAI-1057')
  await expect(sheet(page).locator('.detail .tickets').getByRole('link')).toHaveCount(0)
  await page.keyboard.press('k'); await page.keyboard.press('k')
  const tickets = sheet(page).locator('.detail .tickets')
  // AEON is a project here, but AEON-74 is not one of its tickets: no dead link.
  await expect(tickets.getByRole('link', { name: /^PHAROS-11: / })).toHaveAttribute('href', '/p/PHAROS/PHAROS-11')
  await expect(tickets.getByRole('link')).toHaveCount(1)
  await expect(tickets.getByText('AEON-74')).toHaveAttribute('data-tip', 'AEON-74 is not a ticket in this AEON workspace')
})

test('a reserved version reads as reserved and never published', async ({ page }) => {
  const { history } = await setup(page)
  const reserved = history.releases[2]
  await page.goto(`/releases/${reserved.version}`)
  await expect(options(page).nth(2)).toHaveClass(/reserved/)
  await expect(options(page).nth(2)).toContainText('Reserved, never published')
  await expect(sheet(page).locator('.detail')).toContainText('The version was taken, but no release was published under it.')
  await expect(sheet(page).getByRole('button', { name: /^Evidence/ })).toHaveCount(0)
})

test('a build without history says so', async ({ page }) => {
  await mockWork(page, fixtures())
  await mockReleases(page, { schema: 'inspr.release-history.v1', product: 'PAIMOS AEON', repository: '', version_scheme: 'inspr-calendar-v2', generated_at: '0001-01-01T00:00:00Z', source: 'none', current: 'dev', live_since: new Date().toISOString(), releases: [] }, { running: 'dev' })
  await page.goto('/releases')
  await expect(sheet(page).getByRole('heading', { name: 'No release history in this build' })).toBeVisible()
  await expect(sheet(page)).toContainText('as development builds are')
})

test('a newer version on the server: a toast offers what is new and a reload', async ({ page }) => {
  const history = releaseHistory()
  const { state } = await setup(page, { running: history.releases[1].version })
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await expect(pill(page)).toHaveAccessibleName(new RegExp(`version ${escaped(history.releases[1].version)}`))
  state.server = history.current
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  // AEON-305: without a theme or benefit the toast names only the release, never the tag message.
  // AEON-430: by its marketing name, not its version.
  const newest = history.releases.find(r => r.version === history.current)!
  const toast = page.locator('.toast').filter({ hasText: `PAIMOS AEON was updated to ${newest.codename}` })
  await expect(toast).not.toContainText('Time entry editing')
  await expect(toast).toBeVisible()
  await expect(toast.getByRole('button', { name: 'Reload' })).toBeVisible()
  await toast.getByRole('button', { name: 'What’s new' }).click()
  await expect(sheet(page)).toBeVisible()
  await expect(page).toHaveURL(`/?releases=${history.current}`)
  await expect(options(page).first()).toHaveAttribute('aria-selected', 'true')
  const update = sheet(page).getByRole('status').filter({ hasText: 'this page still runs' })
  await expect(update).toBeVisible()
  // AEON-488: the title already names what the server runs; the status line says this page is older.
  await expect(sheet(page).getByRole('heading', { level: 1, name: newest.codename })).toBeVisible()
  await expect(update.locator('.calendar-version').first()).toHaveAttribute('aria-label', versionLabel(history.releases[1].version, false))
  await expect(sheet(page).getByText(/not in this build.s release history/)).toHaveCount(0)
})

test('the palette and the account menu open the history too', async ({ page }) => {
  await setup(page)
  await page.goto('/')
  await expect(page.getByRole('list', { name: 'Projects' })).toBeVisible()
  await page.keyboard.press('Control+k')
  await page.keyboard.type('release history')
  await page.getByRole('option', { name: /Release history/ }).click()
  await expect(sheet(page)).toBeVisible()
  // Keys wait until the sheet has taken focus.
  await expect(page.getByRole('listbox', { name: 'Releases, newest first' })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(sheet(page)).toHaveCount(0)
  await page.getByRole('button', { name: /^Account for / }).click()
  await page.getByRole('menu', { name: 'Account' }).getByRole('menuitem', { name: 'Release history', exact: true }).click()
  await expect(sheet(page)).toBeVisible()
})

test('rows keep one look per state: current raised, new warm, selected ringed, the rest plain', async ({ page }) => {
  const history = releaseHistory()
  await setup(page, { lastSeen: history.releases[4].version })
  await page.goto(`/releases/${history.releases[5].version}`)
  const look = (i: number) => options(page).nth(i).evaluate(el => { const c = getComputedStyle(el); return { bg: c.backgroundColor, outline: c.outlineStyle } })
  await expect(options(page).nth(5)).toHaveAttribute('aria-selected', 'true')
  await expect(options(page).nth(1)).toHaveClass(/fresh/)
  const current = await look(0), fresh = await look(1), selected = await look(5), plain = await look(6)
  expect(selected).toEqual({ bg: 'rgba(0, 0, 0, 0)', outline: 'solid' })
  expect(plain).toEqual({ bg: 'rgba(0, 0, 0, 0)', outline: 'none' })
  expect(fresh.bg).not.toBe('rgba(0, 0, 0, 0)')
  expect(current.bg).not.toBe(fresh.bg)
  expect(current.outline).toBe('none')
})

test('nothing is clipped at 390: stat card and chart stacked, the count on its own line, full headlines', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  const { history } = await setup(page)
  await page.goto('/releases')
  await expect(options(page).first()).toBeVisible()
  // AEON-488: one stat card that cycles, its arrows always there at 44 px, and the chart under it.
  const card = sheet(page).getByRole('region', { name: 'Release stats' })
  await expect(card.getByRole('group', { name: '1 of 7: Releases per week' })).toBeVisible()
  const next = card.getByRole('button', { name: 'Next stat' })
  await expect(next).toBeVisible()
  expect((await next.boundingBox())!.height).toBe(44)
  await next.click()
  await expect(card.getByRole('group', { name: '2 of 7: Features per week' })).toBeVisible()
  await expect(sheet(page).getByRole('region', { name: 'Release cadence' })).toBeVisible()
  await expect(sheet(page).locator('.result-count')).toHaveText('6 published · 1 reserved')
  const clipped = () => page.evaluate(() => {
    const out: string[] = []
    for (const el of document.querySelectorAll<HTMLElement>('dialog[open] *')) {
      const r = el.getBoundingClientRect()
      if (!r.width || !r.height || getComputedStyle(el).visibility === 'hidden') continue
      if (el.closest('.list-pane, .detail-pane') && (r.bottom < 0 || r.top > innerHeight)) continue
      if (r.left < -0.5 || r.right > innerWidth + 0.5) out.push(`${el.className || el.tagName} outside ${Math.round(r.left)}..${Math.round(r.right)}`)
      const c = getComputedStyle(el)
      if ((c.overflowX !== 'visible' || c.textOverflow === 'ellipsis') && el.scrollWidth > el.clientWidth + 1 && !el.matches('.list-pane, .detail-pane, .listbox')) out.push(`${el.className || el.tagName} cut ${el.scrollWidth}>${el.clientWidth}`)
    }
    return out
  })
  expect(await clipped()).toEqual([])
  await options(page).nth(1).click()
  await expect(sheet(page).locator('.detail')).toBeVisible()
  await sheet(page).getByRole('button', { name: /^Evidence/ }).click()
  expect(await clipped()).toEqual([])
  await sheet(page).getByRole('button', { name: 'All releases' }).click()
  await sheet(page).getByRole('button', { name: 'Compare' }).click()
  await options(page).nth(3).click()
  await expect(sheet(page).locator('.detail-pane > .compare')).toBeVisible()
  expect(await clipped()).toEqual([])
  void history
})

for (const colorScheme of ['light', 'dark'] as const) {
  for (const viewport of [{ width: 1440, height: 900 }, { width: 390, height: 844 }]) {
    test(`axe: release history ${viewport.width} ${colorScheme}`, async ({ page }) => {
      await page.setViewportSize(viewport)
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' })
      const { history } = await setup(page, { lastSeen: releaseHistory().releases[4].version })
      await page.goto(`/releases/${history.releases[1].version}`)
      await expect(sheet(page).locator('.detail')).toBeVisible()
      await sheet(page).getByRole('button', { name: /^Evidence/ }).click()
      await page.waitForTimeout(250)
      // The calendar version is the vendored INSPR display (pinned presentation), as in a11y.spec.
      const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
      const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')} — ${n.failureSummary?.split('\n').slice(1, 2).join(' ').trim()}`).join('\n')}`)
      expect(summary, summary.join('\n')).toEqual([])
    })
  }
}

test('the cadence shows each day’s count above its bar, every bar named for screen readers (AEON-488)', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await setup(page)
  await page.goto('/releases')
  await expect(options(page).first()).toBeVisible()
  const days = sheet(page).getByRole('list', { name: /^\d+ releases? in the last 7 days$/ }).getByRole('listitem')
  await expect(days).toHaveCount(7)
  const all = await days.evaluateAll(items => items.map(li => ({ label: li.getAttribute('aria-label') ?? '', count: li.querySelector('.val')?.textContent ?? null, today: li.classList.contains('current'), before: li.classList.contains('before') })))
  // A day with releases carries its number; an empty one has none; before the first release it says so.
  for (const day of all) {
    if (day.before) { expect(day.label).toMatch(/^\w{3} \d{1,2} \w{3}: before the first AEON release$/); expect(day.count).toBeNull(); continue }
    const n = /^(?:\w{3} \d{1,2} \w{3}|Today): (\d+) releases?/.exec(day.label)?.[1]
    expect(n, day.label).toBeTruthy()
    expect(day.count).toBe(n === '0' ? null : n)
  }
  expect(all.at(-1)!.today).toBe(true)
  expect(all.at(-1)!.label).toMatch(/^Today: \d+ releases? so far/)
  expect(all.some(day => day.count !== null)).toBe(true)
})

// INSPR-CalVer3 (AEON-309): history versions reserved under inspr-calendar-v2 and new
// inspr-calver-3 ones render alike: six segments, no v, no .0.0, seconds hidden at rest,
// and the accessible name keeps the exact canonical version with its UTC date-time.
test('release history renders CalVer2 history and CalVer3 versions as six-segment Pretty', async ({ page }) => {
  const { history } = await setup(page)
  await page.goto('/')
  await pill(page).click()
  await expect(sheet(page)).toBeVisible()
  // At rest: the pointer that clicked the footer pill would otherwise hover a row.
  await page.mouse.move(1, 1)
  const rows = sheet(page).locator('.row .calendar-version')
  await expect(rows.first()).toBeVisible()
  const count = await rows.count()
  expect(count).toBe(history.releases.length)
  for (let i = 0; i < count; i++) {
    const row = rows.nth(i)
    const canonical = history.releases[i].version
    await expect(row).toHaveAttribute('aria-label', new RegExp(`^${escaped(canonical)} · 20\\d\\d-\\d\\d-\\d\\d \\d\\d:\\d\\d:\\d\\d UTC$`))
    const drawn = await row.evaluate(el => el.textContent ?? '')
    expect(drawn).not.toContain('.0.0')
    expect((await row.locator('[data-collapsed="true"]').first().boundingBox())?.width ?? 0).toBe(0)
  }
  // AEON-430: the footer leads with the marketing name; its version is the hover chip.
  await expect(page.locator('footer.app-footer .footer-name .rn-name')).toBeVisible()
})
