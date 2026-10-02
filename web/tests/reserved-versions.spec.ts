// SPDX-License-Identifier: AGPL-3.0-only
// AEON-556: history visibility is a person/workspace preference; the complete
// history still supplies aggregate statistics and direct links.
import { expect, test, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork, watchErrors } from './work-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'

const NOW = Date.parse('2026-10-02T10:00:00Z')
const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const rows = (page: Page) => sheet(page).getByRole('grid', { name: 'Releases, newest first' }).getByRole('row')
const toggle = (page: Page) => page.getByRole('switch', { name: 'Show reserved versions', exact: true })
const footer = (page: Page) => page.locator('footer.app-footer .version-pill')
const row = (page: Page, version: string) => sheet(page).locator(`[id="release-${version.replace(/\./g, '-')}"]`)

async function setup(page: Page, value?: Record<string, unknown>) {
  await page.clock.setFixedTime(new Date(NOW))
  const data = fixtures()
  const history = releaseHistory(NOW)
  data.preferences.releases = { last_seen: history.releases[4].version }
  if (value) data.preferences['developer-ui'] = value
  await mockWork(page, data)
  await mockReleases(page, history)
  return { data, history }
}

// Invoke the same persisted choice while the sheet is open so bounds can be
// compared in one document. Actual Settings switches are exercised below.
async function showReserved(page: Page, show: boolean) {
  await page.evaluate(async value => {
    const { useDeveloperSettings } = await import('/src/lib/developerSettings.ts')
    await useDeveloperSettings().setShowReservedVersions(value)
  }, show)
}
async function boxes(controls: Locator) {
  return controls.evaluateAll(elements => elements.filter(el => el.getClientRects().length).map(el => {
    const { x, y, width, height } = el.getBoundingClientRect()
    return { name: el.getAttribute('aria-label') ?? el.textContent, x, y, width, height }
  }))
}

for (const width of [1440, 390]) {
  test(`rows toggle without changing counts, stats or controls at ${width}px`, async ({ page }, info) => {
    const errors = watchErrors(page)
    await page.setViewportSize({ width, height: 1000 })
    const { data, history } = await setup(page)
    await page.goto('/releases/all?release_view=details')
    await expect(rows(page)).toHaveCount(6)
    await expect(row(page, history.releases[2].version)).toHaveCount(0)
    await expect(sheet(page).locator('.result-count')).toHaveText('6 published · 1 reserved')
    await expect(sheet(page).locator('.head .eyebrow')).toContainText('6 published · 1 reserved')
    const cadence = sheet(page).getByRole('region', { name: 'Release cadence' })
    await expect(cadence.locator('.total')).toHaveText('7 releases')
    await page.evaluate(() => document.fonts.ready)
    const controls = sheet(page).locator('.head button, .head input, .filters button, .stat-card button, .cadence .stepper button')
    const before = await boxes(controls)
    const stat = sheet(page).getByRole('region', { name: 'Release stats' })
    const stats = await stat.innerText()
    const chart = await cadence.innerText()
    await page.screenshot({ path: info.outputPath(`reserved-off-${width}.png`) })
    await showReserved(page, true)
    await expect(rows(page)).toHaveCount(7)
    await expect(row(page, history.releases[2].version)).toContainText('Reserved, never published')
    await expect.poll(() => data.preferences['developer-ui']?.show_reserved_versions).toBe(true)
    expect(await stat.innerText()).toBe(stats)
    expect(await cadence.innerText()).toBe(chart)
    expect(await boxes(controls)).toEqual(before)
    await page.screenshot({ path: info.outputPath(`reserved-on-${width}.png`) })
    await showReserved(page, false)
    await expect(rows(page)).toHaveCount(6)
    expect(await boxes(controls)).toEqual(before)
    expect(errors).toEqual([])
    if (width < 760) {
      await sheet(page).locator('.list-pane').evaluate(el => { el.scrollTop = 700 })
      await page.screenshot({ path: info.outputPath(`reserved-off-list-${width}.png`) })
      await showReserved(page, true)
      await expect(row(page, history.releases[2].version)).toBeVisible()
      await page.screenshot({ path: info.outputPath(`reserved-on-list-${width}.png`) })
    }
  })

  test(`direct reserved links open quietly and navigation skips them at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 })
    const { history } = await setup(page)
    const reserved = history.releases[2]
    await page.goto(`/releases/${reserved.version}`)
    await expect(sheet(page).locator('.detail .top')).toHaveText('Reserved version')
    await expect(sheet(page).locator('.hidden-history')).toHaveText('Hidden in the history. Show reserved versions in Developer settings.')
    await expect(row(page, reserved.version)).toHaveCount(0)
    await page.reload()
    await expect(sheet(page).locator('.hidden-history')).toBeVisible()
    await page.keyboard.press('j')
    await expect(page).toHaveURL(`/releases/${history.releases[3].version}`)
    await page.keyboard.press('k')
    await expect(page).toHaveURL(`/releases/${history.releases[1].version}`)
    await page.keyboard.press('j')
    await expect(page).toHaveURL(`/releases/${history.releases[3].version}`)
    await expect(sheet(page).locator('.hidden-history')).toHaveCount(0)
  })
}

test('Settings opt-in persists, preserves flow controls and keeps both switches under the pointer', async ({ page }) => {
  const { data } = await setup(page, { show_flow_controls: true })
  await page.goto('/settings/developer#reserved-versions')
  const flow = page.getByRole('switch', { name: 'Show the flow controls (not yet tested end to end)' })
  await expect(flow).toBeChecked()
  await expect(toggle(page)).not.toBeChecked()
  await page.evaluate(() => document.fonts.ready)
  const switches = page.getByRole('switch')
  const before = await boxes(switches)
  let complete!: () => void
  const held = new Promise<void>(resolve => { complete = resolve })
  await page.route('**/api/preferences/developer-ui', async route => {
    if (route.request().method() === 'PUT') await held
    await route.fallback()
  })
  await toggle(page).check()
  await expect(toggle(page)).toBeDisabled()
  expect(await boxes(switches)).toEqual(before)
  complete()
  await expect.poll(() => data.preferences['developer-ui']).toEqual({ show_flow_controls: true, show_reserved_versions: true })
  await expect(toggle(page)).toBeEnabled()
  expect(await boxes(switches)).toEqual(before)
  await page.reload()
  await expect(toggle(page)).toBeChecked()
  await page.goto('/releases/all')
  await expect(rows(page)).toHaveCount(7)
  await page.goto('/settings/developer')
  await toggle(page).uncheck()
  await expect.poll(() => data.preferences['developer-ui']).toEqual({ show_flow_controls: true, show_reserved_versions: false })
  await expect(toggle(page)).toBeEnabled()
  expect(await boxes(switches)).toEqual(before)
})

test('failed saves remain off with a retry, and error text moves no switch', async ({ page }) => {
  const { data } = await setup(page)
  let fail = true
  await page.route('**/api/preferences/developer-ui', route => route.request().method() === 'PUT' && fail
    ? route.fulfill({ status: 503, json: { error: 'unavailable' } }) : route.fallback())
  await page.goto('/settings/developer')
  await expect(toggle(page)).not.toBeChecked()
  await page.evaluate(() => document.fonts.ready)
  const before = await boxes(page.getByRole('switch'))
  await toggle(page).click()
  await expect(page.getByRole('alert')).toContainText('Your developer preference could not be saved')
  await expect(toggle(page)).not.toBeChecked()
  expect(data.preferences['developer-ui']).toBeUndefined()
  expect(await boxes(page.getByRole('switch'))).toEqual(before)
  fail = false
  await toggle(page).check()
  await expect.poll(() => data.preferences['developer-ui']?.show_reserved_versions).toBe(true)
})

test('malformed and unreadable choices keep reserved rows hidden', async ({ page }) => {
  await setup(page, { show_reserved_versions: 'true' })
  await page.goto('/releases/all')
  await expect(rows(page)).toHaveCount(6)
  await page.route('**/api/preferences/developer-ui', route => route.fulfill({ status: 503, json: { error: 'unavailable' } }))
  await page.reload()
  await expect(rows(page)).toHaveCount(6)
})

test('comparison choices and result counts follow their separate scopes', async ({ page }) => {
  const { history } = await setup(page)
  await page.goto(`/releases/${history.releases[1].version}`)
  await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
  await expect(row(page, history.releases[3].version)).toHaveAttribute('aria-selected', 'true')
  await expect(sheet(page).locator('.compare .included li')).toHaveCount(1)
  await expect(row(page, history.releases[2].version)).toHaveCount(0)
  await sheet(page).getByRole('button', { name: 'Done', exact: true }).click()
  await sheet(page).getByRole('searchbox', { name: 'Search releases' }).fill(history.releases[2].version)
  await expect(rows(page)).toHaveCount(0)
  await expect(sheet(page).locator('.result-count')).toHaveText('1 of 7')
  await expect(sheet(page).locator('.list-pane .hidden-history')).toHaveText('Hidden in the history. Show reserved versions in Developer settings.')
  await expect(sheet(page).getByRole('heading', { name: 'No release matches', exact: true })).toHaveCount(0)
  await expect(sheet(page).getByRole('button', { name: 'Clear search and filters', exact: true })).toHaveCount(0)
  await showReserved(page, true)
  await expect(rows(page)).toHaveCount(1)
  await expect(sheet(page).locator('.result-count')).toHaveText('1 of 7')
  await expect(sheet(page).locator('.hidden-history')).toHaveCount(0)
  await sheet(page).getByRole('searchbox', { name: 'Search releases' }).fill('no matches')
  await expect(sheet(page).locator('.result-count')).toHaveText('0 of 7')
  await expect(sheet(page).getByRole('heading', { name: 'No release matches', exact: true })).toBeVisible()
  await expect(sheet(page).getByRole('button', { name: 'Clear search and filters', exact: true })).toBeVisible()
})

test('a direct hidden reservation cannot become a comparison endpoint', async ({ page }) => {
  const { history } = await setup(page)
  await page.goto(`/releases/${history.releases[2].version}`)
  await expect(sheet(page).locator('.hidden-history')).toBeVisible()
  await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
  await expect(row(page, history.releases[1].version)).toHaveAttribute('aria-selected', 'true')
  await expect(sheet(page).locator(`.compare .pair .calendar-version[aria-label^="${history.releases[2].version}"]`)).toHaveCount(0)
})

test('turning reservations off removes any reserved comparison endpoints', async ({ page }) => {
  const { history } = await setup(page, { show_reserved_versions: true })
  await page.goto(`/releases/${history.releases[2].version}`)
  await expect(row(page, history.releases[2].version)).toHaveAttribute('aria-selected', 'true')
  await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
  const endpoint = sheet(page).locator(`.compare .pair .calendar-version[aria-label^="${history.releases[2].version}"]`)
  await expect(endpoint).toHaveCount(1)
  await showReserved(page, false)
  await expect(row(page, history.releases[2].version)).toHaveCount(0)
  await expect(endpoint).toHaveCount(0)
  await sheet(page).getByRole('searchbox', { name: 'Search releases' }).fill('no matches')
  await expect(sheet(page).getByRole('button', { name: 'Compare', exact: true })).toBeEnabled()
  await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
  await expect(sheet(page).locator('.compare')).toHaveCount(0)
})

test('the footer and hover card count only visible new versions', async ({ page }) => {
  await setup(page)
  await page.goto('/')
  await expect(footer(page).locator('.new-badge')).toHaveText('3 new')
  await showReserved(page, true)
  await expect(footer(page).locator('.new-badge')).toHaveText('4 new')
  await footer(page).hover()
  await expect(page.locator('.release-hover-card .card-new')).toHaveText('4 new')
  await showReserved(page, false)
  await expect(footer(page).locator('.new-badge')).toHaveText('3 new')
  await expect(page.locator('.release-hover-card .card-new')).toHaveText('3 new')
})

test('no footer badge is invented when only hidden reservations are newer', async ({ page }) => {
  const { history } = await setup(page)
  history.releases[0].state = 'reserved'
  history.releases[0].published_at = null
  const data = fixtures()
  data.preferences.releases = { last_seen: history.releases[1].version }
  await mockWork(page, data)
  await mockReleases(page, history)
  await page.goto('/')
  await footer(page).hover()
  await expect(page.locator('.release-hover-card')).toBeVisible()
  await expect(footer(page).locator('.new-badge')).toHaveCount(0)
  await expect(footer(page)).not.toHaveAccessibleName(/new since/)
  await showReserved(page, true)
  await expect(footer(page).locator('.new-badge')).toHaveText('1 new')
})

for (const show of [false, true]) {
  test(`withdrawn coordinates stay out of rows, details, names and statistics with opt-in ${show}`, async ({ page }) => {
    const { history } = await setup(page, { show_reserved_versions: show })
    const withdrawn = { ...history.releases[2], version: '261002083000.0.0', state: 'withdrawn', codename: 'Withdrawn Name', reserved_at: '2026-10-02T08:30:00Z' }
    history.releases.unshift(withdrawn)
    await page.goto(`/releases/${withdrawn.version}`)
    await expect(rows(page)).toHaveCount(show ? 7 : 6)
    await expect(row(page, withdrawn.version)).toHaveCount(0)
    await expect(sheet(page).locator('.result-count')).toHaveText('6 published · 1 reserved')
    await expect(sheet(page).getByText(/not in this build/)).toBeVisible()
    await expect(sheet(page).locator('.detail').getByRole('button', { name: `Copy version ${withdrawn.version}`, exact: true })).toHaveCount(0)
    await expect(sheet(page)).not.toContainText(withdrawn.codename)
    await expect(sheet(page).getByRole('region', { name: 'Release cadence' }).locator('.total')).toHaveText('7 releases')
    expect(await page.evaluate(async version => {
      const { codenameOf } = await import('/src/lib/codenames.ts')
      return codenameOf(version)
    }, withdrawn.version)).toBe('')
    await sheet(page).getByRole('button', { name: 'Compare', exact: true }).click()
    const comparison = sheet(page).locator('section.compare')
    await expect(comparison).toBeVisible()
    await expect(comparison).not.toContainText(withdrawn.codename)
    await expect(comparison.locator(`.pair .calendar-version[aria-label^="${withdrawn.version}"]`)).toHaveCount(0)
  })
}
