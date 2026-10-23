// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1406 (Tick's approved design): the empty Open tab is a calm picture with one line.
// Risks: the card jumps between reading (D), all clear (A) and a silent source (B); a silent
// source still reads as all clear; both theme pictures show; a narrow phone scrolls sideways.
// AEON-1631: a desk that did not answer shows the all-clear art while the badge counts items.
import { expect, test, type Locator, type Page, type TestInfo } from '@playwright/test'
import { writeFile } from 'node:fs/promises'
import { expectStableControls } from './helpers/stable'
import { fixtures, mockWork } from './work-fixtures'

// Every Desk source answers with an empty page. Kept here rather than in
// decision-desk-fixtures.ts, whose importers are at the 15-spec impact bound.
async function mockEmptyDesk(page: Page, theme: 'light' | 'dark', withItem = false) {
  const data = fixtures()
  data.preferences.theme = { choice: theme }
  await mockWork(page, data)
  const reads: string[] = []
  await page.route('**/api/**', route => {
    const url = new URL(route.request().url()), path = url.pathname, read = route.request().method() === 'GET'
    if (read) reads.push(path + url.search)
    if (path === '/api/decision-desk/projection') return route.fulfill({ json: { items: withItem ? [{ id: 'accounts-1477', kind: 'account_matrix', revision: 1, title: 'Confirm the shared agent accounts', created_at: '2026-10-17T13:00:00Z', held: false, can_decide: true, href: '/settings/accounts', source: '/api/settings/accounts' }] : [], counts: { open: withItem ? 1 : 0, held: 0, chores: 0 }, has_more: false, as_of: new Date().toISOString() } })
    if (path === '/api/decision-desk') return route.fulfill({ json: { items: [], has_more: false } })
    if (path === '/api/approvals') return route.fulfill({ json: [] })
    if (path === '/api/rules/doctrine/inbox') return route.fulfill({ json: { items: [], pending: 0 } })
    if (read && (path === '/api/stepup-requests' || path === '/api/key-trim-proposals')) return route.fulfill({ json: { items: [], has_more: false } })
    if (path === '/api/harness-sessions') return route.fulfill({ json: { items: [], next_cursor: null } })
    if (read && path.endsWith('/messages')) return route.fulfill({ json: { items: [], next_after: 0 } })
    return route.fallback()
  })
  return reads
}

// AEON-1700 (Tick UI spec v1): one sync line replaces the refresh hint, the stale note and the phone's floating hint.
// Risks: a second status element; a check, countdown or live stream claimed that did not happen; numbers or states
// that move the line or anything around it; a screen reader hearing every tick; a Try again too small to tap.
const syncLine = (page: Page) => page.getByTestId('desk-sync')
const syncWords = (page: Page) => syncLine(page).locator('.sync-words')
async function oneSyncLine(page: Page, step: string) {
  await expect(syncLine(page), `${step}: exactly one sync line`).toHaveCount(1)
  for (const old of ['desk-refresh-hint', 'desk-stale', 'desk-refresh-slot']) await expect(page.getByTestId(old), `${step}: ${old} is gone`).toHaveCount(0)
  await expect(page.locator('.refresh-slot, .refresh-hint, .floating'), `${step}: no floating hint`).toHaveCount(0)
}
// The page's own clock text, formatted as the line formats it (24 hours).
function clockOf(page: Page, at: number, seconds = false) {
  return page.evaluate(([value, withSeconds]) => new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', ...(withSeconds ? { second: '2-digit' } : {}), hour12: false }), [at, seconds] as const)
}
// Every sentence the polite region says from now on (§5). A navigation replaces the region, so record after it.
async function recordAnnouncements(page: Page) {
  await page.evaluate(() => {
    const region = document.querySelector('[data-testid="desk-sync-announce"]')!
    const heard: string[] = []
    new MutationObserver(() => { const text = region.textContent?.trim(); if (text) heard.push(text) }).observe(region, { childList: true, characterData: true, subtree: true })
    Object.assign(window, { deskAnnouncements: heard })
  })
  return () => page.evaluate(() => [...(window as unknown as { deskAnnouncements: string[] }).deskAnnouncements])
}
// WCAG contrast of an element's text over the background colours painted beneath it, alpha composited as drawn.
function contrast(locator: Locator) {
  return locator.evaluate(element => {
    const canvas = document.createElement('canvas'); canvas.width = canvas.height = 1
    const context = canvas.getContext('2d', { willReadFrequently: true })!
    const paint = (color: string) => { context.fillStyle = color; context.fillRect(0, 0, 1, 1); return [...context.getImageData(0, 0, 1, 1).data] }
    const layers: string[] = []
    for (let node: Element | null = element; node; node = node.parentElement) layers.push(getComputedStyle(node).backgroundColor)
    paint('#fff')
    let back = [255, 255, 255]
    for (const color of layers.reverse()) back = paint(color)
    const fore = paint(getComputedStyle(element).color)
    const luminance = ([r, g, b]: number[]) => [r!, g!, b!].map(v => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4 }).reduce((sum, c, i) => sum + c * [0.2126, 0.7152, 0.0722][i]!, 0)
    const [light, dark] = [luminance(fore), luminance(back)].sort((x, y) => y - x)
    return (light! + 0.05) / (dark! + 0.05)
  })
}
// §1 with items: the row's free end on a wide row (right-aligned, on the tabs' centre line, adding no height), one
// line under the tabs on a narrow one (8 px below the row above, 14 px above the list), always 20 px tall.
async function syncPlacement(page: Page) {
  return page.evaluate(() => {
    const r = (selector: string) => { const e = document.querySelector(selector); if (!e) return null; const b = e.getBoundingClientRect(); return { x: b.x, y: b.y, width: b.width, height: b.height, right: b.right, bottom: b.bottom } }
    const bar = r('.dp-bar')!, tabs = r('[role="radiogroup"][aria-label="Show"]')!, line = r('[data-testid="desk-sync"]')!, text = r('[data-testid="desk-sync"] .sync-text')!
    const filter = document.querySelector('.dec-filter:not(.reserved)') ? r('.dec-filter') : null
    return { bar, tabs, line, text, filter, list: r('[data-testid="desk-list"]')!, inCard: !!document.querySelector('[data-testid="desk-clear"] [data-testid="desk-sync"]'),
      gap: parseFloat(getComputedStyle(document.querySelector('.dp-bar')!).marginBottom) }
  })
}
function expectRowPlacement(at: Awaited<ReturnType<typeof syncPlacement>>, wide: boolean, step: string) {
  expect(at.inCard, `${step}: the line sits in the tab row`).toBe(false)
  if (wide && at.filter) {
    // Decided shows the stamp filter beside the tabs. A line longer than the free end grows upward in its slot
    // (the AEON-1477 slot), never onto the filter or down onto the list.
    expect(at.text.height % 20, `${step}: whole 20 px lines`).toBeCloseTo(0, 0)
    expect(at.line.x, `${step}: clear of the stamp filter`).toBeGreaterThanOrEqual(at.filter.right + 13.5)
    expect(at.text.bottom, `${step}: never below the row`).toBeLessThanOrEqual(at.tabs.bottom + 0.5)
  } else expect(at.text.height, `${step}: one 20 px line`).toBeCloseTo(20, 0)
  if (wide) {
    if (!at.filter) expect(Math.abs(at.text.y + at.text.height / 2 - (at.tabs.y + at.tabs.height / 2)), `${step}: on the tabs' centre line`).toBeLessThanOrEqual(1)
    expect(at.line.right, `${step}: right-aligned at the row's free end`).toBeLessThanOrEqual(at.bar.right - 13.5)
    expect(at.line.right).toBeGreaterThanOrEqual(at.bar.right - 14.5)
    expect(at.list.y - at.tabs.bottom, `${step}: the line adds no height`).toBeLessThanOrEqual(at.gap + 0.5)
  } else {
    const above = at.filter ?? at.tabs
    expect(Math.abs(at.line.y - (above.y + above.height) - 8), `${step}: 8 px under the row above (${at.line.y - above.y - above.height})`).toBeLessThanOrEqual(0.5)
    expect(Math.abs(at.list.y - at.line.bottom - 14), `${step}: 14 px above the list (${at.list.y - at.line.bottom})`).toBeLessThanOrEqual(0.5)
    expect(Math.abs(at.line.x - at.tabs.x), `${step}: left-aligned with the tabs`).toBeLessThanOrEqual(0.5)
    expect(at.line.height, `${step}: the narrow line is one line`).toBeCloseTo(20, 0)
  }
}

// AEON-1477: a transient failure must never replace good rows or move controls.
// AEON-1616 (R18): empty phone-only rows must not bury the first item or empty state.
// AEON-1700: the sync line says each refresh state in place: one line, no second element, no movement.
for (const width of [1440, 1024, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1477 refresh failures keep the items and hint geometry ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const goodAt = new Date('2026-10-17T13:58:00Z')
    await page.clock.setFixedTime(goodAt)
    await mockEmptyDesk(page, theme, true)
    let failure = 0, reads = 0, empty = false
    await page.route(url => url.pathname === '/api/decision-desk/projection', route => {
      reads++
      if (empty) return route.fulfill({ json: { items: [], counts: { open: 0, held: 0, chores: 0 }, has_more: false, as_of: goodAt.toISOString() } })
      return failure ? route.fulfill({ status: failure, json: { error: 'Unavailable' } }) : route.fallback()
    })
    await page.goto('/decision-desk')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const list = page.getByTestId('desk-list'), row = page.getByTestId('desk-row-u:accounts-1477'), sync = syncLine(page), words = syncWords(page)
    const wide = width >= 1024, phone = width <= 520
    await expect(row).toBeVisible()
    await expect(list).toHaveAttribute('aria-busy', 'false')
    const heard = await recordAnnouncements(page)
    const shown = await clockOf(page, goodAt.getTime())
    const refresh = async (status: number) => {
      failure = status
      const before = reads
      await page.evaluate(() => window.dispatchEvent(new Event('focus')))
      await expect.poll(() => reads).toBeGreaterThan(before)
      await expect(list).toHaveAttribute('aria-busy', 'false')
      await expect(row).toBeVisible()
      await expect(page.getByTestId('desk-failed')).toHaveCount(0)
      await expect(page.getByTestId('desk-empty')).toHaveCount(0)
      await oneSyncLine(page, `after a ${status || 'good'} read`)
    }
    await documentFonts(page)
    // The wall clock is fixed, so the view's poll is always the full 30 s away; nothing streams live here.
    await expect(sync).toHaveAttribute('data-state', 'just')
    await expect(words).toHaveText(phone ? 'checked just now · next in 30 s' : 'checked just now · next check in 30 s')
    await oneSyncLine(page, 'items')
    const spacing = async () => {
      const geometry = await page.evaluate(() => {
        const tabs = document.querySelector('[role="radiogroup"][aria-label="Show"]')!.getBoundingClientRect()
        const list = document.querySelector('[data-testid="desk-list"]')!.getBoundingClientRect()
        const bar = document.querySelector('.dp-bar')!
        const controls = ['#desk-title', '[data-testid="desk-notice"]', '[data-testid="desk-tab-open"]', '[data-testid="desk-tab-decided"]'].map(selector => {
          const r = document.querySelector(selector)!.getBoundingClientRect()
          return { selector, x: r.x, y: r.y, width: r.width, height: r.height }
        })
        return { gap: list.top - tabs.bottom, token: parseFloat(getComputedStyle(bar).marginBottom), list: { x: list.x, y: list.y, width: list.width }, controls }
      })
      // Written before the assertion so a negative-control run retains the old spacing and unaffected controls too.
      await writeFile(testInfo.outputPath(`AEON-1616-measured-${width}-${theme}.md`), JSON.stringify({ width, theme, ...geometry }) + '\n')
      expect(geometry.token, 'the list has a positive desktop spacing token').toBeGreaterThan(0)
      expect(geometry.gap, 'the first card or empty frame follows the tabs').toBeGreaterThanOrEqual(0)
      // A wide row holds the line; a narrow one gives it exactly one line between the tabs and the list (§1).
      expect(geometry.gap, 'nothing but the sync line between the tabs and the first card').toBeLessThanOrEqual(geometry.token + (wide ? 0 : 8 + 20) + 0.5)
      return geometry
    }
    const filled = await spacing()
    expectRowPlacement(await syncPlacement(page), wide, `${width} ${theme} items`)
    expect(await contrast(words), 'the line text is at least 4.5:1').toBeGreaterThanOrEqual(4.5)
    await page.screenshot({ path: testInfo.outputPath(`AEON-1700-items-${width}-${theme}.png`) })
    if (width === 1440 && theme === 'light') {
      // The exact clock on hover: the tip names the last check and the poll's own next tick.
      await sync.locator('.sync-text').hover()
      await expect(page.locator('.tooltip')).toHaveText(`Last check ${await clockOf(page, goodAt.getTime(), true)} · next ${await clockOf(page, goodAt.getTime() + 30_000, true)}`)
      await page.screenshot({ path: testInfo.outputPath('AEON-1700-tooltip-1440-light.png') })
      await page.mouse.move(0, 0)
      await expect(page.locator('.tooltip')).toHaveCount(0)
    }
    const anchors = { heading: page.getByRole('heading', { level: 1, name: 'Decision Desk' }), tabs: page.getByRole('radiogroup', { name: 'Show' }), open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'), row, slot: page.getByTestId('desk-sync-slot') }
    const before = await Promise.all(Object.values(anchors).map(control => control.boundingBox()))
    await expectStableControls({ controls: anchors, scrollAreas: { page: page.locator('html'), list }, interactions: [
      { name: 'one 429 keeps the row and checks again', run: async () => {
        await refresh(429)
        await expect(sync).toHaveAttribute('data-state', 'retrying')
        await expect(words).toHaveText('Checking again…')
        await page.screenshot({ path: testInfo.outputPath(`AEON-1700-first-failure-${width}-${theme}.png`) })
      } },
      { name: 'second failure shows the quiet line', run: async () => {
        await refresh(500)
        await expect(sync).toHaveAttribute('data-state', 'quiet')
        await expect(words).toHaveText(`Couldn’t check · ${phone ? 'retry' : 'trying again'} in 30 s · showing ${shown}`)
        await expect(sync).toHaveClass(/\bwarn\b/)
        await expect(sync.getByRole('button', { name: 'Try again' })).toHaveCount(0)
        expect(await contrast(words), 'warn words are at least 4.5:1').toBeGreaterThanOrEqual(4.5)
        await page.screenshot({ path: testInfo.outputPath(`AEON-1700-quiet-${width}-${theme}.png`) })
      } },
      { name: 'the outage escalates after sixty seconds', run: async () => {
        // Fixed wall time leaves timer cadence alone: no polling failures are
        // added by this jump. The view's existing injected clock ticks in place.
        await page.clock.setFixedTime(new Date(goodAt.getTime() + 60_001))
        await expect(sync).toHaveAttribute('data-state', 'escalated')
        await expect(words).toHaveText(`Not updated since ${shown}`)
        await expect(sync.getByRole('button', { name: 'Try again' })).toBeVisible()
        await page.screenshot({ path: testInfo.outputPath(`AEON-1700-escalated-${width}-${theme}.png`) })
      } },
      { name: 'retry recovers in place', run: async () => {
        failure = 0
        await sync.getByRole('button', { name: 'Try again' }).click()
        await expect(sync).toHaveAttribute('data-state', 'just')
        await expect(sync.getByRole('button', { name: 'Try again' })).toHaveCount(0)
        await expect(list).toHaveAttribute('aria-busy', 'false')
      } },
      { name: 'offline says so in place and checks again once back', run: async () => {
        await page.context().setOffline(true)
        await expect(sync).toHaveAttribute('data-state', 'offline')
        await expect(words).toHaveText(`Offline · showing ${await clockOf(page, goodAt.getTime() + 60_001)} · ${phone ? 'resumes when online' : 'checks resume when you’re back'}`)
        await expect(sync).not.toHaveClass(/\bwarn\b/)
        await page.screenshot({ path: testInfo.outputPath(`AEON-1700-offline-${width}-${theme}.png`) })
        const beforeRead = reads
        await page.context().setOffline(false)
        await expect.poll(() => reads, 'coming back reads the Desk').toBeGreaterThan(beforeRead)
        await expect(sync).toHaveAttribute('data-state', 'just')
      } },
      { name: 'five failures escalate without waiting sixty seconds', run: async () => {
        for (let count = 1; count <= 5; count++) {
          await refresh(500)
          if (count === 1) await expect(sync).toHaveAttribute('data-state', 'retrying')
          else if (count < 5) await expect(sync).toHaveAttribute('data-state', 'quiet')
        }
        await expect(sync).toHaveAttribute('data-state', 'escalated')
      } },
    ].map(interaction => ({ ...interaction, run: async () => {
      await interaction.run()
      expect(await Promise.all(Object.values(anchors).map(control => control.boundingBox()))).toEqual(before)
      expect(await spacing()).toEqual(filled)
    } })) })
    // The brief asks for zero pixels, beyond the shared guard's ±0.5 px limit.
    expect(await Promise.all(Object.values(anchors).map(control => control.boundingBox()))).toEqual(before)
    // §5: each transition spoke once (quiet, escalated, recovered, quiet, escalated); checks, ticks and offline never.
    expect(await heard()).toEqual(['Couldn’t check the Desk. Trying again.', `The Desk hasn’t updated since ${shown}. Try again is available.`, 'The Desk is up to date again.',
      'Couldn’t check the Desk. Trying again.', `The Desk hasn’t updated since ${await clockOf(page, goodAt.getTime() + 60_001)}. Try again is available.`])

    // A genuinely empty desk uses the same first-card slot; the title and tabs remain exactly where they were.
    await refresh(0)
    const fixed = { heading: anchors.heading, tabs: anchors.tabs, open: anchors.open, decided: anchors.decided }
    const fixedAt = await Promise.all(Object.values(fixed).map(control => control.boundingBox()))
    await expectStableControls({ controls: fixed, scrollAreas: { page: page.locator('html') }, interactions: [{ name: 'last item leaves an empty desk in the same slot', run: async () => {
      empty = true
      const beforeRead = reads
      await page.evaluate(() => window.dispatchEvent(new Event('focus')))
      await expect.poll(() => reads).toBeGreaterThan(beforeRead)
      await expect(list).toHaveAttribute('aria-busy', 'false')
      await expect(page.getByTestId('desk-clear')).toHaveAttribute('data-state', 'clear')
      await expect(page.getByTestId('desk-empty')).toHaveText('Nothing waits for you')
      expect(await Promise.all(Object.values(fixed).map(control => control.boundingBox()))).toEqual(fixedAt)
    } }] })
    // §1: the empty card takes the line, centred 8 px under its message; the tab row keeps none.
    await oneSyncLine(page, 'empty')
    await expect(page.getByTestId('desk-sync-slot')).toHaveCount(0)
    await expect(page.getByTestId('desk-clear').getByTestId('desk-sync')).toBeVisible()
    const message = await box(page.getByTestId('desk-empty')), line = await box(sync), card = await box(page.getByTestId('desk-clear'))
    expect(Math.abs(line.y - (message.y + message.height) - 8), `8 px under the message (${line.y - message.y - message.height})`).toBeLessThanOrEqual(0.5)
    expect(Math.abs(line.x + line.width / 2 - (card.x + card.width / 2)), 'centred in the card').toBeLessThanOrEqual(0.5)
    expect(line.height).toBeCloseTo(20, 0)
    const cleared = await spacing()
    expect(cleared.controls).toEqual(filled.controls)
    if (wide) expect(cleared).toEqual(filled)
    else expect(cleared.gap, 'without its line the phone row goes straight to the card').toBeCloseTo(cleared.token, 1)
    expect((await box(page.getByTestId('desk-clear'))).y, 'the empty state begins inside the same card hairline').toBe(await list.evaluate(element => element.getBoundingClientRect().y + element.clientTop))
    await expect(sync, 'the empty card settles to the normal line').toHaveAttribute('data-state', 'just')
    expect(await contrast(words), 'the card line is at least 4.5:1').toBeGreaterThanOrEqual(4.5)
    await onePicture(page, theme)
    await page.screenshot({ path: testInfo.outputPath(`AEON-1700-empty-${width}-${theme}.png`) })
    await writeFile(testInfo.outputPath(`AEON-1616-layout-${width}-${theme}.md`), JSON.stringify({ width, theme, filled, cleared, movement: 0 }) + '\n')
  })
}

async function documentFonts(page: Page) { await page.evaluate(() => document.fonts.ready.then(() => undefined)) }

// AEON-1477 / AEON-1616 (Tick): wide rows keep their inline filter and the line at the row's end; narrow rows show
// the stamp line only on Decided and the sync line under it. Switching tabs never moves the title, tabs or round action.
async function mockFailingDesk(page: Page) {
  await page.clock.setFixedTime(new Date('2026-10-17T13:58:00Z'))
  await mockEmptyDesk(page, 'light', true)
  const world = { failure: 0, reads: 0 }
  await page.route(url => url.pathname === '/api/decision-desk/projection', route => {
    world.reads++
    return world.failure ? route.fulfill({ status: world.failure, json: { error: 'Unavailable' } }) : route.fallback()
  })
  await page.goto('/decision-desk')
  await expect(page.getByTestId('desk-row-u:accounts-1477')).toBeVisible()
  await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
  await documentFonts(page)
  // Two failed focus refreshes give the quiet line (one alone only checks again).
  const failTwice = async () => {
    world.failure = 500
    for (let attempt = 0; attempt < 2; attempt++) {
      const before = world.reads
      await page.evaluate(() => window.dispatchEvent(new Event('focus')))
      await expect.poll(() => world.reads).toBeGreaterThan(before)
      await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
    }
    await expect(syncLine(page)).toHaveAttribute('data-state', 'quiet')
  }
  return { world, failTwice }
}

async function box(locator: Locator) { return (await locator.boundingBox())! }

for (const width of [1440, 1024, 390]) {
  test(`AEON-1477 Open and Decided keep controls still at ${width}, with and without the hint`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const { failTwice } = await mockFailingDesk(page)
    const list = page.getByTestId('desk-list'), row = page.getByTestId('desk-row-u:accounts-1477'), sync = syncLine(page)
    const tabs = page.getByRole('radiogroup', { name: 'Show' }), stamps = page.getByRole('radiogroup', { name: 'Stamp' }), slot = page.getByTestId('desk-sync-slot')
    const wide = width >= 1024
    const controls = { heading: page.getByRole('heading', { level: 1, name: 'Decision Desk' }), tabs, open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided') }
    const controlsAt = await Promise.all(Object.values(controls).map(control => control.boundingBox()))
    const unchanged = async () => expect(await Promise.all(Object.values(controls).map(control => control.boundingBox()))).toEqual(controlsAt)
    const crop = async (name: string) => page.screenshot({ path: testInfo.outputPath(`AEON-1700-${name}-${width}.png`), clip: { x: 0, y: 0, width, height: Math.min(900, (await box(list)).y + 180) } })
    const sweep = async (label: string, hinted: boolean) => {
      const listAt = (await box(list)).y, rowAt = (await box(row)).y
      await oneSyncLine(page, `${width} ${label} Open`)
      expectRowPlacement(await syncPlacement(page), wide, `${width} ${label} Open`)
      if (hinted) await crop('open-quiet')
      await page.getByTestId('desk-tab-decided').click()
      await expect(stamps).toBeVisible()
      await expect(page.getByTestId('desk-empty')).toBeVisible()
      await unchanged()
      // Decided has no empty card: the line stays in the row, under the stamp filter on a narrow one.
      await oneSyncLine(page, `${width} ${label} Decided`)
      expectRowPlacement(await syncPlacement(page), wide, `${width} ${label} Decided`)
      const t = await box(tabs), f = await box(stamps), s = await box(slot)
      if (wide) {
        expect((await box(list)).y, `${width} ${label}: the inline filter adds no height`).toBe(listAt)
        expect(Math.abs(f.y + f.height / 2 - (t.y + t.height / 2)), `${width} ${label}: the filter sits on the tabs' line`).toBeLessThanOrEqual(0.5)
        expect(f.x, `${width} ${label}: the filter follows the tabs`).toBeGreaterThanOrEqual(t.x + t.width + 10)
        expect(s.x, `${width} ${label}: the line's slot follows the filter`).toBeGreaterThanOrEqual(f.x + f.width)
        expect(s.y, `${width} ${label}: the slot is the row's own height`).toBeGreaterThanOrEqual(t.y - 0.5)
        expect(s.y + s.height).toBeLessThanOrEqual(t.y + t.height + 0.5)
      } else {
        const gap = await page.locator('.dp-bar').evaluate(bar => parseFloat(getComputedStyle(bar).rowGap))
        expect(f.y, `${width} ${label}: the visible filter follows the tabs`).toBe(t.y + t.height + gap)
        expect((await box(list)).y, `${width} ${label}: only the visible filter adds a line`).toBe(listAt + gap + f.height)
      }
      if (hinted) {
        const h = await box(sync)
        if (wide) expect(Math.abs(h.x + h.width - (s.x + s.width)), `${width}: the line is right-aligned at the row's end`).toBeLessThanOrEqual(0.5)
        expect(h.y + h.height).toBeLessThanOrEqual(s.y + s.height + 0.5)
        await crop('decided-quiet')
      } else await crop('decided')
      await page.getByTestId('desk-tab-open').click()
      await expect(stamps).toBeHidden()
      await unchanged()
      expect((await box(list)).y, `${width} ${label}: back on Open the list is where it was`).toBe(listAt)
      expect((await box(row)).y).toBe(rowAt)
      return listAt
    }
    const quiet = await sweep('normal', false)
    await failTwice()
    expect((await box(list)).y, `${width}: the quiet line takes the same line`).toBe(quiet)
    expect(await sweep('quiet', true)).toBe(quiet)
  })
}

// Touch has no hover: Try again shows with the escalated line, on a 44 × 44 target that adds no line height.
test.describe('touch', () => {
  test.use({ hasTouch: true, isMobile: true })
  test('AEON-1477 on touch Try again shows with the hint on a 44 px target', async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 844 })
    const { world, failTwice } = await mockFailingDesk(page)
    const list = page.getByTestId('desk-list'), sync = syncLine(page)
    const before = await box(list), lineAt = await box(sync)
    await failTwice()
    await page.clock.setFixedTime(new Date(new Date('2026-10-17T13:58:00Z').getTime() + 60_001))
    await expect(sync).toHaveAttribute('data-state', 'escalated')
    expect((await box(list)).y, 'the escalated line and its target add no height').toBe(before.y)
    expect(await box(sync), 'the line keeps its box').toEqual(lineAt)
    const retry = sync.getByRole('button', { name: 'Try again' })
    await expect(retry).toBeVisible()
    const target = await box(retry)
    expect(target.width, 'Try again is at least 44 px wide').toBeGreaterThanOrEqual(44)
    expect(target.height, 'Try again is at least 44 px tall').toBeGreaterThanOrEqual(44)
    // The whole box answers a tap: each corner hits the button, not a neighbour drawn over it.
    // A miss names what was hit instead, so a cover is found from the failure alone.
    expect(await retry.evaluate(button => {
      const r = button.getBoundingClientRect()
      return [[r.left + 1, r.top + 1], [r.right - 1, r.top + 1], [r.left + 1, r.bottom - 1], [r.right - 1, r.bottom - 1]].map(([x, y]) => {
        const hit = document.elementFromPoint(x, y)
        return button.contains(hit) || `${x},${y} ${hit?.tagName}.${hit?.className} (button ${r.left},${r.top},${r.width}x${r.height})`
      })
    })).toEqual([true, true, true, true])
    await page.screenshot({ path: testInfo.outputPath('AEON-1700-touch-retry-390.png'), clip: { x: 0, y: 0, width: 390, height: Math.min(844, (await box(list)).y + 180) } })
    world.failure = 0
    const reads = world.reads
    await retry.tap()
    await expect.poll(() => world.reads, 'Try again reads the Desk').toBeGreaterThan(reads)
    await expect(sync).toHaveAttribute('data-state', 'just')
    expect((await box(list)).y).toBe(before.y)
  })
})

test('AEON-1477 a genuinely failed first load never claims an empty desk', async ({ page }, testInfo) => {
  await mockEmptyDesk(page, 'light')
  // The first read waits at a barrier, so the never-read line is on screen until it fails.
  let release!: () => void
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route(url => url.pathname === '/api/decision-desk/projection', async route => { await held; await route.fulfill({ status: 500, json: { error: 'Unavailable' } }) })
  await page.goto('/decision-desk?view=decided')
  await oneSyncLine(page, 'never read')
  await expect(syncLine(page)).toHaveAttribute('data-state', 'checking')
  await expect(syncWords(page)).toHaveText('Checking now…')
  await page.screenshot({ path: testInfo.outputPath('AEON-1700-never-read-1280.png') })
  release()
  await page.getByTestId('desk-tab-open').click()
  await expect(page.getByTestId('desk-failed')).toContainText('The desk couldn’t be loaded.')
  await expect(page.getByTestId('desk-clear')).toHaveCount(0)
  await expect(page.getByTestId('desk-empty')).toHaveCount(0)
  // The failed panel says the list did not load; the one line only says the Desk checks again, never all clear.
  await oneSyncLine(page, 'first read failed')
  await expect(syncLine(page)).toHaveAttribute('data-state', /^(retrying|checking)$/)
  await expect(syncWords(page)).toHaveText(/^(Checking again…|Checking now…)$/)
  await expect(page.getByTestId('desk-tab-open')).toContainText('?')
})
// A focus refresh, awaited by its question read; one already in flight would swallow the event.
async function refreshDesk(page: Page, reads: string[]) {
  const count = () => reads.filter(path => path.startsWith('/api/decision-desk?state=open')).length
  await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
  const before = count()
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect.poll(count).toBeGreaterThan(before)
  await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
}

async function noSideways(page: Page, step: string) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), `${step}: horizontal scroll`).toBeLessThanOrEqual(0)
}
// Exactly the picture of the current theme is shown and loaded, decorative, with its size set.
async function onePicture(page: Page, theme: 'light' | 'dark') {
  const shown = page.locator(`.all-clear .art img.${theme}`), hidden = page.locator(`.all-clear .art img.${theme === 'light' ? 'dark' : 'light'}`)
  await expect(shown).toBeVisible()
  await expect(hidden).toBeHidden()
  for (const image of [shown, hidden]) {
    await expect(image).toHaveAttribute('alt', '')
    await expect(image).toHaveAttribute('width', '840')
    await expect(image).toHaveAttribute('height', '560')
  }
  await expect.poll(() => shown.evaluate(image => (image as HTMLImageElement).complete && (image as HTMLImageElement).naturalWidth)).toBe(840)
  // Decoded and painted before any screenshot: the picture decodes asynchronously.
  await shown.evaluate(async image => { await (image as HTMLImageElement).decode(); await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))) })
}

for (const width of [1440, 390]) for (const theme of ['light', 'dark'] as const) {
  test(`AEON-1406 empty desk states keep the card still ${width} ${theme}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 })
    const reads = await mockEmptyDesk(page, theme)
    // D: the first read is held at a barrier, so the reading state is on screen until released.
    let release!: () => void
    const held = new Promise<void>(resolve => { release = resolve })
    await page.route(url => url.pathname === '/api/decision-desk/projection', async route => { await held; await route.fallback() })
    // B is for a minor source only (AEON-1631): key trims are read by the Desk alone.
    let trimsFail = false
    await page.route(url => url.pathname === '/api/key-trim-proposals', route => trimsFail ? route.fulfill({ status: 503, json: { error: 'Unavailable' } }) : route.fallback())
    await page.goto('/decision-desk')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const frame = page.getByTestId('desk-clear'), quiet = page.getByTestId('desk-unanswered')
    await expect(frame).toHaveAttribute('data-state', 'reading')
    await expect(frame.getByRole('status').filter({ hasText: 'Reading the desk…' })).toBeVisible()
    await expect(page.getByTestId('desk-tab-open')).toHaveText(/^Open\s*$/)
    await expect(page.getByTestId('desk-empty')).toHaveCount(0)
    await expect(frame.locator('img')).toHaveCount(0)
    // AEON-1700: the line already has its place in the card; like the rest of D it shows once the read lands.
    await oneSyncLine(page, 'D')
    await expect(frame.getByTestId('desk-sync')).toBeHidden()
    await noSideways(page, 'D')
    await page.screenshot({ path: testInfo.outputPath(`D-${width}-${theme}.png`) })

    // D keeps A's height: the card, the frame and the tabs stay put when the read arrives.
    const still = { card: page.getByTestId('desk-list'), frame, open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'),
      heading: page.getByRole('heading', { level: 1, name: 'Decision Desk' }) }
    await expectStableControls({
      controls: still,
      scrollAreas: { page: page.locator('html') },
      interactions: [
        { name: 'D reading → A all clear', run: async () => {
          release()
          await expect(frame).toHaveAttribute('data-state', 'clear')
          await expect(page.getByTestId('desk-empty')).toHaveText('Nothing waits for you')
          await expect(quiet).toHaveText('')
          await expect(page.getByTestId('desk-tab-open')).toHaveText(/^Open\s*0$/)
          await expect(page.locator('.source-warnings')).toHaveCount(0)
          await onePicture(page, theme)
          // AEON-1700 §1: Markus's "label below": the sync line, centred 8 px under the message.
          await oneSyncLine(page, 'A')
          await expect(frame.getByTestId('desk-sync')).toBeVisible()
          await expect(syncWords(page)).toHaveText(width <= 520 ? /^checked just now\s·\snext in \d+ s$/ : /^checked just now\s·\snext check in \d+ s$/)
          const message = await box(page.getByTestId('desk-empty')), line = await box(syncLine(page)), card = await box(frame)
          expect(Math.abs(line.y - (message.y + message.height) - 8), 'A: 8 px under the message').toBeLessThanOrEqual(0.5)
          expect(Math.abs(line.x + line.width / 2 - (card.x + card.width / 2)), 'A: centred in the card').toBeLessThanOrEqual(0.5)
          expect(await contrast(syncWords(page)), 'A: the line text is at least 4.5:1').toBeGreaterThanOrEqual(4.5)
          testInfo.annotations.push({ type: `A height ${width} ${theme}`, description: String((await frame.boundingBox())!.height) })
          await page.screenshot({ path: testInfo.outputPath(`A-${width}-${theme}.png`) })
        } },
      ],
    })
    // B's line has its slot in A: the picture, the line and the slot itself stay put too. The sync line's words
    // change with each read, so its own line is held separately: it never moves up or down.
    const syncAt = (await box(syncLine(page))).y
    await expectStableControls({
      controls: { ...still, art: frame.locator('.art'), line: page.getByTestId('desk-empty'), slot: quiet },
      scrollAreas: { page: page.locator('html') },
      interactions: [
        { name: 'A → B a minor source did not answer', run: async () => {
          trimsFail = true
          await refreshDesk(page, reads)
          await expect(frame).toHaveAttribute('data-state', 'incomplete')
          await expect(quiet).toHaveText(/^Key trims didn’t answer, so this may be incomplete\s*·\s*Try again$/)
          // The calm line replaces the source list; there is one Try again, and no alert look.
          await expect(page.getByRole('button', { name: 'Try again', exact: true })).toHaveCount(1)
          await expect(page.locator('.source-warnings')).toHaveCount(0)
          await expect(page.getByTestId('desk-list')).toHaveClass(/\bclear\b/)
          await expect(page.getByTestId('desk-empty')).toHaveText('Nothing waits for you')
          await onePicture(page, theme)
          await page.screenshot({ path: testInfo.outputPath(`B-${width}-${theme}.png`) })
        } },
        { name: 'B Try again → A', run: async () => {
          trimsFail = false
          await page.getByRole('button', { name: 'Try again', exact: true }).click()
          await expect(frame).toHaveAttribute('data-state', 'clear')
          await expect(quiet).toHaveText('')
          await expect(page.getByTestId('desk-list')).toHaveAttribute('aria-busy', 'false')
        } },
      ].map(step => ({ ...step, run: async () => { await step.run(); await oneSyncLine(page, step.name); expect((await box(syncLine(page))).y, `${step.name}: the sync line stays on its line`).toBe(syncAt) } })),
    })

    // C: a project scope says so in one plain line, without the picture or a second scope line.
    await page.goto('/decision-desk?project=p-pharos')
    await expect(page.getByTestId('desk-empty')).toHaveText(/^Nothing waits for you in Pharos\s*·\s*Show all$/)
    await expect(page.getByTestId('desk-clear')).toHaveCount(0)
    await expect(page.getByTestId('desk-list').locator('img')).toHaveCount(0)
    await expect(page.getByTestId('desk-scope')).toHaveCount(0)
    const showAll = page.getByTestId('desk-empty').getByRole('link', { name: 'Show all' })
    await expect(showAll).toHaveAttribute('href', '/decision-desk')
    await noSideways(page, 'C')
    await page.screenshot({ path: testInfo.outputPath(`C-${width}-${theme}.png`) })
    await showAll.click()
    await expect(page.getByTestId('desk-clear')).toHaveAttribute('data-state', 'clear')

    await failedDeskFillsItself(page, testInfo, width, theme)
  })
}

// AEON-1631 (Tick's failed-desk design), state E of the cases above: a desk that did not answer never looks clear
// and reads itself again. Risks: the badge counts items while the Open tab shows the all-clear art; the retry never
// comes, comes early or stacks; Try again, the tabs or the rows move when the panel, its note or the stale note change.
// It runs inside the AEON-1406 cases (same widths and themes), so the file keeps its pinned case count (AEON-1619).
const accountRow = (n: number) => ({ id: `accounts-${n}`, kind: 'account_matrix', revision: 1, title: `Confirm the shared agent accounts ${n}`, created_at: '2026-10-21T17:00:00Z', held: false, can_decide: true, href: '/settings/accounts', source: '/api/settings/accounts' })
async function failedDeskFillsItself(page: Page, testInfo: TestInfo, width: number, theme: 'light' | 'dark') {
  // Time stands still unless the test moves it: every retry below happens at a known instant.
  const at = new Date('2026-10-21T17:47:00Z')
  await page.clock.install({ time: at })
  await page.clock.pauseAt(new Date(at.getTime() + 1000))
  // The empty desk stays mocked from the case above; this route answers the projection first.
  // fail: the read errors; partial: the badge counts two items the list does not carry; good: both rows.
  let mode: 'fail' | 'partial' | 'good' = 'fail', reads = 0, gate: Promise<void> | undefined
  await page.route(url => url.pathname === '/api/decision-desk/projection', async route => {
    reads++
    if (gate) await gate
    if (mode === 'fail') return route.fulfill({ status: 503, json: { error: 'Unavailable' } })
    return route.fulfill({ json: { items: mode === 'good' ? [accountRow(1), accountRow(2)] : [], counts: { open: 2, held: 0, chores: 0 }, has_more: false, as_of: at.toISOString() } })
  })
  await page.goto('/decision-desk')
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
  const panel = page.getByTestId('desk-failed'), retry = panel.getByRole('button', { name: 'Try again', exact: true })
  const retrying = panel.getByText('Retrying automatically', { exact: true }), stillNot = panel.getByText('Still not loading. Your items are safe; try again later.', { exact: true })
  const neverClear = async (step: string) => {
    await expect(page.getByTestId('desk-clear'), `${step}: no all-clear frame`).toHaveCount(0)
    await expect(page.getByText('Nothing waits for you'), `${step}: no all-clear line`).toHaveCount(0)
    await expect(page.getByTestId('desk-list').locator('img'), `${step}: no all-clear art`).toHaveCount(0)
  }
  await expect(panel).toHaveAttribute('data-retry', 'retrying')
  // The notice offer decides within its own bound before the list shows; move past it once.
  await page.clock.runFor(1000)
  await expect(panel.locator('.failed-line')).toHaveText('The desk couldn’t be loaded.')
  await expect(retrying).toBeVisible()
  await expect(stillNot).toBeHidden()
  await expect(retry).toBeEnabled()
  await neverClear('first read failed')
  // AEON-1700: the failed panel says the list did not load; the one sync line, in the tab row, only says it checks again.
  await oneSyncLine(page, 'failed panel')
  await expect(page.getByTestId('desk-sync-slot')).toHaveCount(1)
  await expect(syncLine(page)).toHaveAttribute('data-state', 'retrying')
  await expect(syncWords(page)).toHaveText('Checking again…')
  await documentFonts(page)
  await page.screenshot({ path: testInfo.outputPath(`AEON-1631-failed-${width}-${theme}.png`) })

  // The shared stability guard waits for animation frames, which a paused clock never paints, so the boxes are
  // read directly here and must not move at all.
  const boxes = (controls: Record<string, Locator>) => Promise.all(Object.entries(controls).map(async ([name, control]) => [name, await control.boundingBox()] as const))
  const still = { open: page.getByTestId('desk-tab-open'), decided: page.getByTestId('desk-tab-decided'), retry, icon: panel.locator('svg').first() }
  const panelAt = await boxes(still)
  mode = 'partial'
  const beforeCount = reads
  await page.clock.runFor(1999); expect(reads, 'no retry before 3 s').toBe(beforeCount)
  await page.clock.runFor(1); await expect.poll(() => reads, 'one retry at 3 s').toBe(beforeCount + 1)
  await expect(panel.locator('.failed-line')).toHaveText('2 items are waiting, but the list couldn’t be loaded.')
  // The tab shows the badge's own count beside the panel that says the list did not load.
  await expect(page.getByTestId('desk-tab-open')).toHaveText(/^Open\s*2$/)
  await expect(retrying).toBeVisible()
  await expect(retry).toBeEnabled()
  await neverClear('badge two, list empty')
  expect(await boxes(still), 'the count sentence moves neither Try again nor the tabs').toEqual(panelAt)
  await noSideways(page, 'E count')
  await page.screenshot({ path: testInfo.outputPath(`AEON-1631-count-${width}-${theme}.png`) })
  // The 10 s retry fills the rows; the list starts where the panel did.
  mode = 'good'
  const listAt = (await box(page.getByTestId('desk-list'))).y, before = reads
  await page.clock.runFor(9999); expect(reads, 'no retry before 10 s').toBe(before)
  await page.clock.runFor(1); await expect.poll(() => reads, 'one retry at 10 s').toBe(before + 1)
  expect(await boxes({ open: still.open, decided: still.decided }), 'the tabs stay while rows replace the panel').toEqual(panelAt.slice(0, 2))
  const first = page.getByTestId('desk-row-u:accounts-1'), second = page.getByTestId('desk-row-u:accounts-2')
  await expect(first).toBeVisible(); await expect(second).toBeVisible()
  await expect(panel).toHaveCount(0)
  await neverClear('rows arrived')
  expect((await box(page.getByTestId('desk-list'))).y).toBe(listAt)
  await page.screenshot({ path: testInfo.outputPath(`AEON-1631-filled-${width}-${theme}.png`) })

  // Rows from this visit stay when a later read misses the desk; the sync line says so quietly in its own place.
  const sync = syncLine(page), words = syncWords(page), phone = width <= 520, list = page.getByTestId('desk-list')
  const rows = { open: still.open, list, first, second, slot: page.getByTestId('desk-sync-slot') }
  await expect(list).toHaveAttribute('aria-busy', 'false')
  await page.clock.runFor(400)
  await expect(sync).toHaveAttribute('data-state', 'just')
  const heard = await recordAnnouncements(page)
  const rowsAt = await boxes(rows)
  mode = 'partial'
  const staleCount = reads
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect.poll(() => reads).toBeGreaterThan(staleCount)
  await expect(list).toHaveAttribute('aria-busy', 'false')
  await page.clock.runFor(400)
  await oneSyncLine(page, 'stale rows')
  await expect(sync).toHaveAttribute('data-state', 'quiet')
  // The 3 s retry is the next read, from its own timer.
  await expect(words).toHaveText(`Couldn’t check · ${phone ? 'retry' : 'trying again'} in 3 s · showing ${await clockOf(page, at.getTime())}`)
  await expect(first).toBeVisible(); await expect(second).toBeVisible()
  await neverClear('stale rows')
  expect(await boxes(rows), 'the quiet line keeps its place').toEqual(rowsAt)
  await noSideways(page, 'E stale')
  await page.screenshot({ path: testInfo.outputPath(`AEON-1631-stale-${width}-${theme}.png`) })
  // The 3 s retry reads the desk again and the line settles as quietly.
  mode = 'good'
  const goodCount = reads
  await page.clock.runFor(2599); expect(reads, 'no stale retry before 3 s').toBe(goodCount)
  await page.clock.runFor(1); await expect.poll(() => reads, 'one stale retry at 3 s').toBe(goodCount + 1)
  await expect(list).toHaveAttribute('aria-busy', 'false')
  await page.clock.runFor(400)
  await expect(sync).toHaveAttribute('data-state', 'just')
  expect(await boxes(rows), 'the line settles without moving the rows').toEqual(rowsAt)
  expect(await heard(), '§5: the kept rows spoke once, and their recovery once').toEqual(['Couldn’t check the Desk. Trying again.', 'The Desk is up to date again.'])

  // AEON-1700 §7 on the paused clock: the line counts down to the poll's own timer, a check holds "Checking now…" for
  // 400 ms, and a 60 s idle run (two polls, every tick) says nothing to a screen reader.
  const quietRun = await recordAnnouncements(page)
  const due = await page.evaluate(async () => {
    // @ts-expect-error Vite resolves browser source URLs.
    const { useDecisionDesk } = await import('/src/stores/decisionDesk.ts')
    return useDecisionDesk().nextPollAt as number
  })
  const idleFrom = await page.evaluate(() => Date.now()), idleReads = reads
  await page.clock.runFor(due - idleFrom)
  await expect.poll(() => reads, 'the poll reads the Desk on its own tick').toBeGreaterThan(idleReads)
  await expect(list).toHaveAttribute('aria-busy', 'false')
  await expect(sync).toHaveAttribute('data-state', 'checking')
  await expect(words).toHaveText('Checking now…')
  await page.clock.runFor(400)
  await expect(sync).toHaveAttribute('data-state', 'just')
  await page.clock.runFor(11_600)
  await expect(words).toHaveText(phone ? 'checked 12 s ago · next in 18 s' : 'checked 12 s ago · next check in 18 s')
  await page.screenshot({ path: testInfo.outputPath(`AEON-1700-normal-${width}-${theme}.png`) })
  // The number slots reserve "59": the same width for 9, 12 and 59, so a tick never shifts the words.
  const slots = await sync.locator('.sync-n').evaluateAll(numbers => numbers.map(number => {
    const probe = number.cloneNode(true) as HTMLElement
    const widths = ['9', '12', '59'].map(text => { probe.firstElementChild!.textContent = text; number.after(probe); const w = probe.getBoundingClientRect().width; probe.remove(); return w })
    return [number.getBoundingClientRect().width, ...widths]
  }))
  expect(slots).toHaveLength(2)
  for (const widths of slots) expect(new Set(widths.map(w => w.toFixed(2))).size, `one slot width for 9, 12 and 59 (${widths})`).toBe(1)
  const wordsAt = await box(words)
  await page.clock.runFor(9_000)
  await expect(words).toHaveText(phone ? 'checked 21 s ago · next in 9 s' : 'checked 21 s ago · next check in 9 s')
  expect(await box(words), '"18 s" → "9 s" keeps the text box').toEqual(wordsAt)
  await page.clock.runFor(60_000 - (await page.evaluate(() => Date.now()) - idleFrom))
  expect(reads - idleReads, 'two polls ran in the idle minute').toBeGreaterThanOrEqual(2)
  await expect(list).toHaveAttribute('aria-busy', 'false')
  expect(await quietRun(), '§5: a 60 s idle run announces nothing').toEqual([])

  // §4: the refresh icon turns while a read runs; with reduced motion it stays still and the words carry the state.
  let release!: () => void
  gate = new Promise<void>(resolve => { release = resolve })
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(sync).toHaveAttribute('data-state', 'checking')
  const icon = sync.locator('.sync-icon')
  expect(await icon.evaluate(element => element.getAnimations().length), 'reduced motion: no turn').toBe(0)
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await expect.poll(() => icon.evaluate(element => element.getAnimations().length), 'the icon turns while checking').toBe(1)
  await page.screenshot({ path: testInfo.outputPath(`AEON-1700-checking-${width}-${theme}.png`) })
  gate = undefined; release()
  await expect(list).toHaveAttribute('aria-busy', 'false')
  await page.clock.runFor(400)
  await expect(sync).toHaveAttribute('data-state', 'just')

  // §3/§4: once the Desk's wakes stream live, the line says Live with its dot. A live event pulses the icon slot once
  // and says the change until its read is done; a reconnect's catch-up is a check, never a change.
  await page.evaluate(() => {
    const pulses: string[] = [], animate = Element.prototype.animate
    Element.prototype.animate = function (this: Element, ...args: Parameters<Element['animate']>) { if (this.classList.contains('sync-mark')) pulses.push('pulse'); return animate.apply(this, args) }
    Object.assign(window, { deskPulses: pulses })
  })
  const pulses = () => page.evaluate(() => (window as unknown as { deskPulses: string[] }).deskPulses.length)
  await page.evaluate(async () => {
    // @ts-expect-error Vite resolves browser source URLs.
    const { subscribeAgents } = await import('/src/lib/agents.ts')
    Object.assign(window, { deskLiveStop: subscribeAgents(() => {}) })
  })
  await expect(sync.getByTestId('desk-sync-dot')).toBeVisible()
  await expect(words).toHaveText(phone ? /^checked just now\s·\snext in \d+ s$/ : /^Live\s·\schecked just now\s·\snext check in \d+ s$/)
  await page.clock.runFor(3_000)
  await expect(list).toHaveAttribute('aria-busy', 'false')
  await page.clock.runFor(400)
  expect(await pulses(), 'the catch-up pulses nothing').toBe(0)
  await expect(sync).toHaveAttribute('data-state', 'just')
  const liveReads = reads
  await page.evaluate(() => (window as unknown as { fixtureQuietStreams: { emit(type: string, data: string, id: number): void } }).fixtureQuietStreams.emit('question.asked', '{}', 9001))
  await expect(sync).toHaveAttribute('data-state', 'change')
  await expect(words).toHaveText('Change arrived · updating…')
  expect(await pulses(), 'one live event, one pulse').toBe(1)
  await page.screenshot({ path: testInfo.outputPath(`AEON-1700-change-${width}-${theme}.png`) })
  await page.clock.runFor(3_000)
  await expect.poll(() => reads, 'the window ends in one read').toBe(liveReads + 1)
  await expect(list).toHaveAttribute('aria-busy', 'false')
  await expect(words).toHaveText('Change arrived · updating…')
  await page.clock.runFor(400)
  await expect(sync).toHaveAttribute('data-state', 'just')
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.evaluate(() => (window as unknown as { fixtureQuietStreams: { emit(type: string, data: string, id: number): void } }).fixtureQuietStreams.emit('question.answered', '{}', 9002))
  await expect(words).toHaveText('Change arrived · updating…')
  expect(await pulses(), 'reduced motion: no pulse').toBe(1)
  await page.emulateMedia({ reducedMotion: 'no-preference' })
  await page.clock.runFor(3_000)
  await expect(list).toHaveAttribute('aria-busy', 'false')
  await page.clock.runFor(400)
  await page.evaluate(() => (window as unknown as { deskLiveStop(): void }).deskLiveStop())
  await expect(sync.getByTestId('desk-sync-dot'), 'without the stream "Live" and its dot drop').toHaveCount(0)
  // The second recorder took over at the idle run: since then (idle minute, checking, live run) nothing spoke.
  expect(await quietRun(), 'no announcement in the idle, checking and live runs').toEqual([])
}

