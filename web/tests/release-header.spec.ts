// SPDX-License-Identifier: AGPL-3.0-only
// AEON-488: the release history's header. The live release's codename is the
// title with one status line under it; one stat card cycles every 7 s (hover or
// focus pauses it, an arrow stops it for good, a button pauses and resumes it,
// reduced motion never starts it); the cadence chart steps through ranges it
// remembers, and its bars are one keyboard stop.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockReleases, releaseHistory } from './releases-fixtures'
import { expectStableControls } from './helpers/stable'

const sheet = (page: Page) => page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
const card = (page: Page) => sheet(page).getByRole('region', { name: 'Release stats' })
const slide = (page: Page, n: number, label: string) => card(page).getByRole('group', { name: `${n} of 7: ${label}` })
const chart = (page: Page) => sheet(page).getByRole('region', { name: 'Release cadence' })

async function open(page: Page, options: { motion?: boolean; running?: (h: ReturnType<typeof releaseHistory>) => string; viewport?: { width: number; height: number }; history?: ReturnType<typeof releaseHistory>; now?: number } = {}) {
  await page.setViewportSize(options.viewport ?? { width: 1440, height: 1000 })
  await page.emulateMedia({ reducedMotion: options.motion ? 'no-preference' : 'reduce' })
  const now = options.now ?? Date.now()
  await page.clock.install({ time: new Date(now) })
  const history = options.history ?? releaseHistory(now)
  await mockWork(page, fixtures())
  await mockReleases(page, history, { running: options.running?.(history) })
  await page.goto('/releases')
  await expect(sheet(page).getByRole('grid', { name: 'Releases, newest first' })).toBeVisible()
  await expect(slide(page, 1, 'Releases per week')).toBeVisible()
  return history
}
// Off the card: the pointer elsewhere and focus back on the list.
async function leave(page: Page) {
  await page.mouse.move(700, 900)
  await sheet(page).getByRole('grid', { name: 'Releases, newest first' }).focus()
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

test('Resume respects reduced motion, including live preference changes', async ({ page }) => {
  await open(page)
  const resume = card(page).getByRole('button', { name: 'Resume automatic rotation' })
  await resume.click()
  await leave(page)
  await page.clock.runFor(15_000)
  await expect(slide(page, 1, 'Releases per week')).toBeVisible()
  await expect(slide(page, 1, 'Releases per week')).toHaveAttribute('aria-live', 'polite')
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await resume.click()
  await leave(page)
  await page.clock.runFor(7100)
  await expect(slide(page, 2, 'Features per week')).toBeVisible()
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await expect(resume).toBeVisible()
  await resume.click()
  await leave(page)
  await page.clock.runFor(15_000)
  await expect(slide(page, 2, 'Features per week')).toBeVisible()
  await expect(slide(page, 2, 'Features per week')).toHaveAttribute('aria-live', 'polite')
})

for (const width of [761, 800, 900]) {
  test(`at ${width}×360 the stacked cards scroll and release rows stay reachable`, async ({ page }) => {
    await open(page, { viewport: { width, height: 360 } })
    await expect(sheet(page).locator('.head .stats')).toHaveCount(0)
    await expect(sheet(page).locator('.list-pane .stats')).toHaveCount(1)
    const pane = sheet(page).locator('.list-pane')
    const bounds = await pane.boundingBox()
    expect(bounds!.height).toBeGreaterThan(80)
    await pane.evaluate(el => { el.scrollTop = el.scrollHeight })
    const row = sheet(page).getByRole('row').last()
    await expect(row).toBeInViewport()
    const statBounds = await card(page).boundingBox()
    expect(statBounds!.y + statBounds!.height).toBeLessThanOrEqual(bounds!.y)
  })
}

test('a thirty-day release gap keeps the timeline inside the phone card', async ({ page }) => {
  const now = Date.now()
  await open(page, { now, history: releaseHistory(now - 30 * 86_400_000), viewport: { width: 390, height: 844 } })
  await card(page).getByRole('button', { name: 'Next stat' }).click()
  await card(page).getByRole('button', { name: 'Next stat' }).click()
  const viz = slide(page, 3, 'Since the last release').getByRole('img')
  await expect(viz).toHaveAttribute('aria-label', /before this window/)
  await expect(viz.locator('text').last()).toContainText('earlier')
  const geometry = await viz.evaluate(el => {
    const svg = el as SVGSVGElement
    const drawn = svg.getBBox()
    return { left: drawn.x, right: drawn.x + drawn.width, width: svg.viewBox.baseVal.width }
  })
  expect(geometry.left).toBeGreaterThanOrEqual(0)
  expect(geometry.right).toBeLessThanOrEqual(geometry.width)
})

test('the light codename leads one glass status dock; Details keeps it steady and shows generation and counts in the notes', async ({ page }) => {
  const history = await open(page)
  const live = history.releases.find(r => r.version === history.current)!
  const head = sheet(page).locator('.head')
  await expect(head.getByRole('heading', { level: 1, name: live.codename })).toBeVisible()
  await expect(head.locator('.eyebrow')).toHaveText('PAIMOS AEON · RELEASE')
  await expect(head.getByRole('heading', { level: 1 })).toHaveCSS('font-weight', '300')
  await expect(head.locator('.codename-label')).toHaveCSS('text-transform', 'none')
  const headingSpacing = await head.getByRole('heading', { level: 1 }).evaluate(el => getComputedStyle(el).letterSpacing)
  expect(Number.parseFloat(headingSpacing)).toBeLessThan(0)
  await expect(head.locator('.codename-label')).toHaveCSS('letter-spacing', headingSpacing)
  await expect(head.locator('.hero .sparkle, .hero .rn-stamp')).toHaveCount(0)
  await expect(head.locator('.status-dock')).toHaveCount(1)
  await expect(head.locator('.status-line')).toHaveText(/^Live here since (\w{3} )?\d\d:\d\d · 50 min$/)
  await expect(head.getByRole('button', { name: 'Reload' })).toHaveCount(0)
  // The old tiles are gone; nothing says it twice.
  await expect(head.getByText('Running here')).toHaveCount(0)
  const version = head.getByRole('button', { name: `Copy version ${history.current}`, exact: true })
  await expect(version).toHaveCSS('background-color', 'rgba(0, 0, 0, 0)')
  await expect(version.locator('.copy-icon')).toHaveCSS('opacity', '0')
  await expect(version.locator('.version-pretty')).toHaveAttribute('data-canonical', history.current)
  await expect(version.locator('.version-canonical')).toHaveText(history.current)
  await sheet(page).getByRole('radio', { name: 'Details' }).click()
  await expect(head.locator('.eyebrow')).toHaveText('PAIMOS AEON · RELEASE')
  await expect(sheet(page).locator('.detail-info')).toHaveText('PAIMOS 7 · AEON releases · 6 published')
  await expect(sheet(page).locator('.detail .tech')).toContainText('PAIMOS 7 · Release')
})

test('Pretty dock separators use the muted ink in both themes and after a live theme change', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' })
  await open(page)
  const pretty = sheet(page).locator('.status-dock .version-pretty')
  const separators = pretty.locator('.separator:not([data-collapsed="true"]) .separator-glyph')
  await expect(separators).toHaveCount(4)
  const colors: string[] = []
  for (const colorScheme of ['light', 'dark', 'light'] as const) {
    await page.emulateMedia({ colorScheme })
    const mutedInk = await pretty.evaluate(el => getComputedStyle(el).color)
    colors.push(mutedInk)
    for (const glyph of await separators.all()) await expect(glyph).toHaveCSS('color', mutedInk)
  }
  expect(colors[0]).not.toBe(colors[1])
  expect(colors[2]).toBe(colors[0])
})

test('resting Pretty hours and minutes clear AA contrast in both themes and after a live theme change', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' })
  await open(page)
  await leave(page)
  const button = sheet(page).locator('.status-dock .version-copy')
  const time = button.locator('.version-pretty > .hh, .version-pretty > .mi')
  await expect(time).toHaveCount(2)
  // Axe cannot resolve the dock's gradient/backdrop-filter. Check its rendered
  // ink and retained segment weight against representative dock backdrops.
  const luminance = (rgb: number[]) => {
    const linear = rgb.map(value => {
      const channel = value / 255
      return channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4
    })
    return .2126 * linear[0]! + .7152 * linear[1]! + .0722 * linear[2]!
  }
  const colors: string[] = []
  for (const colorScheme of ['light', 'dark', 'light'] as const) {
    await page.emulateMedia({ colorScheme })
    await expect(button).toHaveAttribute('data-version-view', 'pretty')
    const ink = colorScheme === 'light' ? 'rgb(32, 60, 61)' : 'rgb(237, 244, 240)'
    // A live theme change replaces renderer segments. Read their computed ink
    // and opacity in one browser task from the stable button, after it settles.
    let rendered = { ink: '', time: [] as { color: string; opacity: string }[] }
    await expect.poll(async () => {
      rendered = await button.evaluate(el => ({
        ink: getComputedStyle(el).color,
        time: [...el.querySelectorAll('.version-pretty > .hh, .version-pretty > .mi')].map(segment => {
          const style = getComputedStyle(segment)
          return { color: style.color, opacity: style.opacity }
        }),
      }))
      return rendered
    }).toEqual({ ink, time: [{ color: ink, opacity: '0.8' }, { color: ink, opacity: '0.8' }] })
    colors.push(rendered.ink)
    const background = colorScheme === 'light' ? [251, 250, 246] : [24, 48, 52]
    for (const segment of rendered.time) {
      const foreground = segment.color.match(/[\d.]+/g)!.map(Number)
      const opacity = Number(segment.opacity)
      const composited = background.map((channel, index) => Math.round(foreground[index]! * opacity + channel * (1 - opacity)))
      expect(composited).toEqual(colorScheme === 'light' ? [76, 98, 98] : [194, 205, 202])
      const values = [luminance(composited), luminance(background)].sort((a, b) => b - a)
      expect((values[0]! + .05) / (values[1]! + .05)).toBeGreaterThanOrEqual(4.5)
    }
  }
  expect(colors[0]).not.toBe(colors[1])
  expect(colors[2]).toBe(colors[0])
})

test('an outdated page says so in the status line, under the codename the server runs', async ({ page }) => {
  const olderOf = (h: ReturnType<typeof releaseHistory>) => h.releases.find(r => r.state === 'published' && r.version < h.current)!
  const history = await open(page, { running: h => olderOf(h).version })
  const older = olderOf(history)
  const live = history.releases.find(r => r.version === history.current)!
  const head = sheet(page).locator('.head')
  await expect(head.getByRole('heading', { level: 1, name: live.codename })).toBeVisible()
  const status = head.locator('.status-line[role="status"]')
  await expect(status).toHaveText(new RegExp(`^Live on the server · this page still runs ${older.codename}`))
  await expect(status.getByRole('button', { name: 'Reload' })).toBeVisible()
  await expect(sheet(page).locator('.notice')).toHaveCount(0)
})

test('the dock crossfades per character to canonical text and back over the shared second', async ({ page }) => {
  const history = await open(page, { motion: true })
  const button = sheet(page).locator('.status-dock').getByRole('button', { name: `Copy version ${history.current}`, exact: true })
  const full = button.locator('[data-version-character="canonical"]')
  const pretty = button.locator('[data-version-character="pretty"]')
  // Measure after the sheet's entrance animation has settled.
  await sheet(page).locator('.shell').evaluate(async el => { await Promise.all(el.getAnimations().map(animation => animation.finished)) })
  const bounds = await button.boundingBox()
  await expect(button).toHaveAttribute('data-version-view', 'pretty')
  await expect(full).toHaveCount(history.current.length)
  await expect(full.first()).toHaveCSS('opacity', '0')
  await button.hover()
  await expect(button).toHaveAttribute('data-version-view', 'revealed')
  await expect(full.last()).toHaveCSS('opacity', '1')
  await expect(pretty.last()).toHaveCSS('opacity', '0')
  const transitions = await full.evaluateAll(nodes => nodes.map(el => {
    const style = getComputedStyle(el)
    return [Number.parseFloat(style.transitionDuration), Number.parseFloat(style.transitionDelay), style.transitionTimingFunction]
  }))
  expect(transitions[0]).toEqual([0.42, 0, 'ease-in-out'])
  expect(transitions.at(-1)).toEqual([0.42, 0.58, 'ease-in-out'])
  await expect(button.locator('.version-canonical > .ss')).toHaveCSS('opacity', '0.7')
  await expect(button.locator('.version-canonical > .hh')).toHaveCSS('opacity', '0.94')
  await expect(button.locator('.copy-icon')).toHaveCSS('opacity', '1')
  expect(await button.boundingBox()).toEqual(bounds)
  await leave(page)
  await expect(button).toHaveAttribute('data-version-view', 'pretty')
  await expect(full.last()).toHaveCSS('opacity', '0')
  await expect(pretty.last()).toHaveCSS('opacity', '1')
  await expect(button.locator('.copy-icon')).toHaveCSS('opacity', '0')
})

for (const width of [320, 390]) {
  for (const colorScheme of ['light', 'dark'] as const) {
    test(`the glass dock and canonical version fit a ${width}px phone in ${colorScheme}`, async ({ page }) => {
      await page.emulateMedia({ colorScheme })
      const history = releaseHistory()
      history.releases.find(r => r.version === history.current)!.codename = 'Intact Ion'
      await open(page, { history, viewport: { width, height: 844 } })
      const head = sheet(page).locator('.head')
      const button = head.getByRole('button', { name: `Copy version ${history.current}`, exact: true })
      await button.focus()
      await expect(button.locator('[data-version-character="canonical"]').last()).toHaveCSS('opacity', '1')
      for (const element of [head.locator('.status-dock'), button, head.getByRole('button', { name: 'Close release history' })]) {
        const bounds = await element.boundingBox()
        expect(bounds!.x).toBeGreaterThanOrEqual(0)
        expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width)
      }
      expect((await button.boundingBox())!.height).toBeGreaterThanOrEqual(44)
      const views = head.getByRole('radiogroup', { name: 'View', exact: true })
      const details = views.getByRole('radio', { name: 'Details', exact: true })
      const highlights = views.getByRole('radio', { name: 'Highlights', exact: true })
      await expectStableControls({
        controls: {
          sheet: sheet(page), dock: head.locator('.status-dock'), copy: button,
          close: head.getByRole('button', { name: 'Close release history' }),
          languages: head.getByRole('radiogroup', { name: 'Language', exact: true }),
          views, details, highlights,
        },
        scrollAreas: { sheet: sheet(page), head },
        interactions: [
          { name: 'copy hover', run: () => button.hover() },
          { name: 'Details', run: async () => {
            await details.click()
            await expect(details).toHaveAttribute('aria-checked', 'true')
          } },
          { name: 'Highlights', run: async () => {
            await highlights.click()
            await expect(highlights).toHaveAttribute('aria-checked', 'true')
          } },
        ],
      })
    })
  }
}

test('keyboard focus reveals the dock version; Enter and click copy its exact canonical value and announce it', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const history = await open(page)
  const dock = sheet(page).locator('.status-dock')
  const button = dock.getByRole('button', { name: `Copy version ${history.current}`, exact: true })
  await button.focus()
  await expect(button).toHaveAttribute('data-version-view', 'revealed')
  await page.keyboard.press('Enter')
  await expect(button).toHaveAttribute('data-copy-state', 'copied')
  await expect(dock.getByRole('status')).toHaveText('Version copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(history.current)
  await page.clock.runFor(2100)
  await button.click()
  await expect(dock.getByRole('status')).toHaveText('Version copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(history.current)
  // Leaving the pointer alone does not dismiss keyboard-visible focus.
  await page.mouse.move(1, 1)
  await button.focus()
  await expect(button).toHaveAttribute('data-version-view', 'revealed')
  await leave(page)
  await expect(button).toHaveAttribute('data-version-view', 'pretty')
})

test('reduced motion switches the dock instantly, including changes during a reveal', async ({ page }) => {
  const history = await open(page)
  const button = sheet(page).locator('.status-dock').getByRole('button', { name: `Copy version ${history.current}`, exact: true })
  const full = button.locator('[data-version-character="canonical"]')
  await button.hover()
  await expect(full.last()).toHaveCSS('opacity', '1')
  expect(await full.evaluateAll(nodes => nodes.every(node => (node as HTMLElement).style.transition === 'none'))).toBe(true)
  await leave(page)
  await expect(full.last()).toHaveCSS('opacity', '0')
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await button.hover()
  expect(await full.first().evaluate(node => (node as HTMLElement).style.transition)).toContain('420ms ease-in-out')
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await expect(full.last()).toHaveCSS('opacity', '1')
  expect(await full.evaluateAll(nodes => nodes.every(node => (node as HTMLElement).style.transition === 'none'))).toBe(true)
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
  const selected = await sheet(page).getByRole('row', { selected: true }).getAttribute('id')
  await page.keyboard.press('Home')
  expect(await sheet(page).getByRole('row', { selected: true }).getAttribute('id')).toBe(selected)
  await expect(chart(page).locator('.tip')).toBeVisible()
})

test('a clock tick preserves the chart focus, tooltip and next keyboard step', async ({ page }) => {
  await open(page, { now: new Date('2026-10-01T12:00:00Z').getTime() })
  const bars = chart(page).getByRole('listitem')
  await bars.last().focus()
  await page.keyboard.press('ArrowLeft')
  const label = await bars.nth(5).getAttribute('aria-label')
  const tooltip = await chart(page).locator('.tip').textContent()
  await page.clock.runFor(30_100)
  await expect(bars.nth(5)).toBeFocused()
  await expect(bars.nth(5)).toHaveAttribute('tabindex', '0')
  await expect(bars.nth(5)).toHaveAttribute('aria-label', label!)
  await expect(chart(page).locator('.tip')).toHaveText(tooltip!)
  await page.keyboard.press('ArrowLeft')
  await expect(bars.nth(4)).toBeFocused()
})
