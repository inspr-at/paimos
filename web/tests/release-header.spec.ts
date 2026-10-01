// SPDX-License-Identifier: AGPL-3.0-only
// AEON-488: the release history's header. The live release's codename is the
// title with one status line under it; one stat card cycles every 7 s (hover or
// focus pauses it, an arrow stops it for good, a button pauses and resumes it,
// reduced motion never starts it); the cadence chart steps through ranges it
// remembers, and its bars are one keyboard stop.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const card = (page: Page) => sheet(page).getByRole('region', { name: 'Release stats' })
const slide = (page: Page, n: number, label: string) => card(page).getByRole('group', { name: `${n} of 7: ${label}` })
const chart = (page: Page) => sheet(page).getByRole('region', { name: 'Release cadence' })

async function open(page: Page, options: { motion?: boolean; running?: (h: ReturnType<typeof releaseHistory>) => string } = {}) {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: options.motion ? 'no-preference' : 'reduce' })
  await page.clock.install()
  const history = releaseHistory()
  await mockWork(page, fixtures())
  await mockReleases(page, history, { running: options.running?.(history) })
  await page.goto('/releases')
  await expect(sheet(page).getByRole('listbox', { name: 'Releases, newest first' })).toBeVisible()
  await expect(slide(page, 1, 'Releases per week')).toBeVisible()
  return history
}
// Off the card: the pointer elsewhere and focus back on the list.
async function leave(page: Page) {
  await page.mouse.move(700, 900)
  await sheet(page).getByRole('listbox', { name: 'Releases, newest first' }).focus()
}

test('the stat card moves on every 7 s, the current dot filling, and is silent while it moves', async ({ page }) => {
  await open(page, { motion: true })
  await leave(page)
  await expect(slide(page, 1, 'Releases per week')).toHaveAttribute('aria-live', 'off')
  await expect(card(page).getByRole('button', { name: 'Pause automatic rotation' })).toBeVisible()
  await page.clock.runFor(3500)
  const fill = card(page).locator('.dot.on .fill')
  await expect.poll(async () => parseFloat(await fill.evaluate(el => (el as HTMLElement).style.width))).toBeGreaterThan(30)
  await page.clock.runFor(3700)
  await expect(slide(page, 2, 'Features per week')).toBeVisible()
  await page.clock.runFor(7100)
  await expect(slide(page, 3, 'Since the last release')).toBeVisible()
  await expect(card(page).locator('.pos')).toHaveText('3 / 7')
})

test('hover and focus pause the card; leaving lets it move on', async ({ page }) => {
  await open(page, { motion: true })
  await leave(page)
  await card(page).hover()
  await page.clock.runFor(15_000)
  await expect(slide(page, 1, 'Releases per week')).toBeVisible()
  await leave(page)
  await page.clock.runFor(7100)
  await expect(slide(page, 2, 'Features per week')).toBeVisible()
  // Keyboard focus inside pauses it too.
  await card(page).getByRole('button', { name: 'Pause automatic rotation' }).focus()
  await page.clock.runFor(15_000)
  await expect(slide(page, 2, 'Features per week')).toBeVisible()
})

test('an arrow takes over for good; the button resumes it', async ({ page }) => {
  await open(page, { motion: true })
  await card(page).hover()
  // The arrows show on hover.
  const next = card(page).getByRole('button', { name: 'Next stat' })
  await expect(next).toHaveCSS('opacity', '1')
  await next.click()
  await expect(slide(page, 2, 'Features per week')).toBeVisible()
  await card(page).getByRole('button', { name: 'Previous stat' }).click()
  await card(page).getByRole('button', { name: 'Previous stat' }).click()
  await expect(slide(page, 7, 'Busiest day')).toBeVisible()
  await leave(page)
  await page.clock.runFor(30_000)
  await expect(slide(page, 7, 'Busiest day')).toBeVisible()
  // Not rotating by itself, a change is announced.
  await expect(slide(page, 7, 'Busiest day')).toHaveAttribute('aria-live', 'polite')
  const resume = card(page).getByRole('button', { name: 'Resume automatic rotation' })
  await resume.click()
  await leave(page)
  await page.clock.runFor(7100)
  await expect(slide(page, 1, 'Releases per week')).toBeVisible()
  await card(page).getByRole('button', { name: 'Pause automatic rotation' }).click()
  await leave(page)
  await page.clock.runFor(15_000)
  await expect(slide(page, 1, 'Releases per week')).toBeVisible()
})

test('with reduced motion the card never moves by itself', async ({ page }) => {
  await open(page)
  await leave(page)
  await expect(card(page).getByRole('button', { name: 'Resume automatic rotation' })).toBeVisible()
  await expect(slide(page, 1, 'Releases per week')).toHaveAttribute('aria-live', 'polite')
  await page.clock.runFor(30_000)
  await expect(slide(page, 1, 'Releases per week')).toBeVisible()
  // Every stat still answers by hand, each with chips under its line.
  for (const [n, label] of [[2, 'Features per week'], [3, 'Since the last release'], [4, 'Median gap'], [5, 'This week'], [6, 'Streak'], [7, 'Busiest day']] as const) {
    await card(page).getByRole('button', { name: 'Next stat' }).click()
    await expect(slide(page, n, label)).toBeVisible()
    await expect(slide(page, n, label).locator('.stat-chip').first()).toBeVisible()
    await expect(slide(page, n, label).getByRole('img')).toHaveAttribute('aria-label', /\w/)
  }
})

test('the title is the live codename with one status line; Details adds the generation and counts', async ({ page }) => {
  const history = await open(page)
  const live = history.releases.find(r => r.version === history.current)!
  const head = sheet(page).locator('.head')
  await expect(head.getByRole('heading', { level: 1, name: live.codename })).toBeVisible()
  await expect(head.locator('.eyebrow')).toHaveText('PAIMOS AEON · Releases')
  await expect(head.locator('.status-line')).toHaveText(/^Live here since (\w{3} )?\d\d:\d\d · 50 min$/)
  await expect(head.getByRole('button', { name: 'Reload' })).toHaveCount(0)
  // The old tiles are gone; nothing says it twice.
  await expect(head.getByText('Running here')).toHaveCount(0)
  await sheet(page).getByRole('radio', { name: 'Details' }).click()
  await expect(head.locator('.eyebrow')).toHaveText('PAIMOS 7 · AEON releases · 6 published · 1 reserved')
})

test('an outdated page says so in the status line, under the codename the server runs', async ({ page }) => {
  const olderOf = (h: ReturnType<typeof releaseHistory>) => h.releases.find(r => r.state === 'published' && r.version < h.current)!
  const history = await open(page, { running: h => olderOf(h).version })
  const older = olderOf(history)
  const live = history.releases.find(r => r.version === history.current)!
  const head = sheet(page).locator('.head')
  await expect(head.getByRole('heading', { level: 1, name: live.codename })).toBeVisible()
  const status = head.getByRole('status')
  await expect(status).toHaveText(new RegExp(`^Live on the server · this page still runs ${older.codename}`))
  await expect(status.getByRole('button', { name: 'Reload' })).toBeVisible()
  await expect(sheet(page).locator('.notice')).toHaveCount(0)
})

test('the range stepper walks 7 days to a year, and the choice is remembered', async ({ page }) => {
  await open(page)
  const shorter = chart(page).getByRole('button', { name: 'Shorter range' })
  const longer = chart(page).getByRole('button', { name: 'Longer range' })
  await expect(chart(page).locator('.title')).toHaveText('Last 7 days')
  await expect(shorter).toBeDisabled()
  await expect(chart(page).getByRole('listitem')).toHaveCount(7)
  for (const [title, bars] of [['Last 14 days', 14], ['Last 30 days', 30], ['Last 13 weeks', 13], ['Last 12 months', 12]] as const) {
    await longer.click()
    await expect(chart(page).locator('.title')).toHaveText(title)
    await expect(chart(page).getByRole('listitem')).toHaveCount(bars)
  }
  await expect(longer).toBeDisabled()
  await shorter.click()
  await expect(chart(page).locator('.title')).toHaveText('Last 13 weeks')
  // Days before the first release are named, not zero.
  await expect(chart(page).getByRole('listitem').first()).toHaveAttribute('aria-label', /: before the first AEON release$/)
  await page.reload()
  await expect(chart(page).locator('.title')).toHaveText('Last 13 weeks')
})

test('the bars are one keyboard stop; arrows walk them and the tooltip names the slot', async ({ page }) => {
  await open(page)
  const bars = chart(page).getByRole('listitem')
  await expect(bars.last()).toHaveAttribute('tabindex', '0')
  await expect(bars.nth(5)).toHaveAttribute('tabindex', '-1')
  await bars.last().focus()
  await expect(bars.last()).toHaveAttribute('aria-label', /^Today: \d+ releases? so far/)
  await expect(chart(page).locator('.tip')).toContainText('so far')
  await page.keyboard.press('ArrowLeft')
  await expect(bars.nth(5)).toBeFocused()
  await expect(bars.nth(5)).toHaveAttribute('tabindex', '0')
  await expect(bars.last()).toHaveAttribute('tabindex', '-1')
  await page.keyboard.press('End')
  await expect(bars.last()).toBeFocused()
  // Home and End stay in the chart: the list keeps its row.
  const selected = await sheet(page).getByRole('option', { selected: true }).getAttribute('id')
  await page.keyboard.press('Home')
  expect(await sheet(page).getByRole('option', { selected: true }).getAttribute('id')).toBe(selected)
  await expect(chart(page).locator('.tip')).toBeVisible()
})

for (const colorScheme of ['light', 'dark'] as const) {
  test(`axe: the header ${colorScheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme })
    await open(page)
    await page.evaluate(value => { document.documentElement.dataset.theme = value }, colorScheme)
    await card(page).getByRole('button', { name: 'Next stat' }).click()
    const results = await new AxeBuilder({ page }).include('.releases .head').withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).exclude('.calendar-version').analyze()
    const summary = results.violations.map(v => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.slice(0, 4).map(n => `    ${n.target.join(' ')}`).join('\n')}`)
    expect(summary, summary.join('\n')).toEqual([])
  })
}
