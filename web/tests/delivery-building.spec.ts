// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1493 (Tick's approved design): Delivery › Numbers › Building it. Risks: the section, its tiles or the Learn
// buttons below move between loading, a failed read, no data and the answer, or when the worst five open and close;
// the whole tile cannot open them from the keyboard, Esc does not return focus, an outside click leaves them open;
// phones get a floating panel instead of a bottom sheet, sideways scroll or targets under 44 px.
import { expect, test, type Locator, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { controlStability } from './control-stability'
import { deliveryMetrics, mockDelivery } from './delivery-numbers-fixtures'
import { buildingAnswer, mockBuilding } from './delivery-building-fixtures'

type Answer = 'ready' | 'empty' | 'error' | 'hold'
function gate() { let open!: () => void; const wait = new Promise<void>(resolve => { open = resolve }); return { wait, open } }

async function setup(page: Page, options: { theme?: 'light' | 'dark'; level?: 'simple' | 'expert'; first?: Answer } = {}) {
  const work = fixtures()
  work.preferences.theme = { choice: options.theme ?? 'light' }
  work.preferences['delivery:numbers'] = { level: options.level ?? 'simple', window: 7 }
  await mockWork(page, work)
  await mockDelivery(page, () => ({ status: 200, body: deliveryMetrics() }))
  const state = { next: options.first ?? 'ready' as Answer, held: gate(), reads: 0, work }
  await mockBuilding(page, async (projectId, window) => {
    state.reads++
    let now = state.next
    // A held read answers with whatever the test asks for once it opens the gate.
    if (now === 'hold') { await state.held.wait; now = state.next }
    if (now === 'error') return { status: 500, body: { error: 'building read failed' } }
    return { status: 200, body: buildingAnswer(projectId, window, { empty: now === 'empty' }) }
  })
  return state
}
const section = (page: Page) => page.getByTestId('delivery-building')
const trigger = (page: Page, name: string) => section(page).getByRole('button', { name: `Worst in this window: ${name}` })
/** The rest of the page has its answers (summary, Tickets, sources): only Building it can still change. */
async function settled(page: Page) {
  await expect(page.getByTestId('delivery-summary')).toContainText('Last 7 days vs. the 7 before', { timeout: 15_000 })
  await expect(page.getByTestId('delivery-tickets').locator('[aria-busy="true"]')).toHaveCount(0)
}
/** Expert: the sources, Tickets and the numbers below have their answers, so nothing above or below Building it still moves. */
async function settledExpert(page: Page) {
  await expect(page.getByTestId('delivery-tiles').getByRole('listitem').first()).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('main [aria-busy="true"]')).toHaveCount(0, { timeout: 15_000 })
  await expect(page.locator('.dl-status .sk')).toHaveCount(0)
}
const learnBelow = (page: Page) => page.getByTestId('delivery-simple').getByRole('button', { name: 'Learn: How long the PR checks take' })
// Tick's 1440 check: the Expert "Today" line of Lead time ended in "… · 2 …" with no way to read the rest. It is
// clipped text like on every other tile: the full text on hover and on keyboard focus, nothing moves, and a click on
// it still opens the worst five like the rest of the tile.
async function clippedTodayLine(page: Page) {
  const tile = section(page).locator('[data-building="lead"]'), readout = tile.locator('.readout'), tip = page.locator('.tooltip')
  await expect(readout).toHaveText(/^Today · p50 .+ · p90 .+ · 2 items$/)
  await expect(readout).toHaveAttribute('data-clip-tip', '')
  const full = (await readout.textContent())!.trim()
  await readout.scrollIntoViewIfNeeded()
  const guard = await controlStability(page, { lead: trigger(page, 'Lead time per item'), readout, plot: tile.locator('.plot') })
  await guard.check(async () => {
    await readout.hover()
    await expect(tip).toHaveText(full)
  })
  await page.mouse.move(4, 4)
  await expect(tip).toHaveCount(0)
  await guard.check(async () => {
    await tile.locator('.plot').focus()
    await page.keyboard.press('Shift+Tab')
    await expect(readout).toBeFocused()
    await expect(tip).toHaveText(full)
  })
  guard.done()
  await readout.click()
  await expect(page.getByTestId('delivery-worst')).toContainText('Lead time per item · last 7 days')
  await expect(trigger(page, 'Lead time per item')).toHaveAttribute('aria-expanded', 'true')
  await page.keyboard.press('Escape')
  await expect(page.getByTestId('delivery-worst')).toHaveCount(0)
}

for (const width of [1440, 1024, 390] as const) {
  test(`Building it at ${width}, light and dark: four tiles, Simple and Expert, in the design's order`, async ({ page }, info) => {
    test.setTimeout(90_000)
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const state = await setup(page)
    for (const theme of ['light', 'dark'] as const) {
      state.work.preferences.theme = { choice: theme }
      state.work.preferences['delivery:numbers'] = { level: 'simple', window: 7 }
      await page.goto('/p/AEON/delivery')
      // A cold dev server compiles the Delivery route on the first visit.
      await expect(page.getByTestId('delivery-simple')).toBeVisible({ timeout: 45_000 })
      const head = section(page).getByTestId('delivery-building-head')
      await expect(head).toHaveText('0 of 3 on target', { timeout: 15_000 })
      const tiles = section(page).getByRole('listitem')
      await expect(tiles).toHaveCount(4)
      await expect(tiles.getByRole('heading', { level: 4 })).toHaveText(['First-try rate', 'Rounds per item', 'Idle between rounds', 'Lead time per item'])
      await expect(tiles.nth(0)).toContainText('Far off')
      await expect(tiles.nth(3)).toContainText('No target yet: watched')
      // It follows Tickets and comes before "Making a change ready".
      const order = await page.getByTestId('delivery-simple').evaluate(root => [...root.querySelectorAll('h3')].map(h => h.textContent?.trim()))
      expect(order.indexOf('Building it')).toBe(order.indexOf('Making a change ready') - 1)
      const boxes = await tiles.evaluateAll(items => items.map(item => item.getBoundingClientRect()).map(r => ({ x: Math.round(r.x), y: Math.round(r.y) })))
      const columns = new Set(boxes.map(box => box.x)).size
      expect(columns).toBe(width === 1440 ? 4 : width === 1024 ? 2 : 1)
      if (width === 390) {
        // Phones keep value, verdict, sentence and footer; the small chart and the trend stay on wide screens.
        await expect(tiles.first().locator('.s-spark')).toBeHidden()
        await expect(tiles.first().locator('.s-trend')).toBeHidden()
        // The whole tile is the target that opens the worst five.
        for (const box of await section(page).getByTestId('delivery-building-open').evaluateAll(items => items.map(item => item.getBoundingClientRect().height))) expect(box).toBeGreaterThanOrEqual(44)
      }
      expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(1)
      await section(page).scrollIntoViewIfNeeded()
      await section(page).screenshot({ path: info.outputPath(`aeon-1493/building-simple-${width}-${theme}.png`) })

      await page.getByRole('radiogroup', { name: 'Level of detail' }).getByRole('radio', { name: 'Expert' }).click()
      await expect(section(page).getByTestId('delivery-building-head')).toHaveText('0 of 3 on target')
      if (width === 390) {
        await expect(section(page).locator('.s-pp').nth(1)).toHaveText('p50 3.2 · mean 3.6 · p90 6')
      } else {
        await expect(section(page).locator('.plot')).toHaveCount(4)
        await expect(section(page).getByRole('listitem').first()).toContainText('Event log: rounds, gate verdicts, merges')
      }
      await section(page).scrollIntoViewIfNeeded()
      await section(page).screenshot({ path: info.outputPath(`aeon-1493/building-expert-${width}-${theme}.png`) })
      if (width === 1440 && theme === 'light') await clippedTodayLine(page)
      if (theme === 'dark') {
        // The design's dark Expert overlay (and the phone sheet in dark).
        await trigger(page, 'Idle between rounds').click()
        await expect(page.getByTestId('delivery-worst')).toContainText('idle 2 h 10 min')
        await page.screenshot({ path: info.outputPath(`aeon-1493/building-overlay-idle-expert-${width}-dark.png`) })
        await page.keyboard.press('Escape')
        await expect(page.getByTestId('delivery-worst')).toHaveCount(0)
      }
    }
  })
}

test('Building it: Enter opens the worst five on the first row, ↓ moves, Esc closes and returns focus; nothing moves', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  await expect(section(page).getByTestId('delivery-building-head')).toHaveText('0 of 3 on target', { timeout: 15_000 })
  await settled(page)
  const rounds = trigger(page, 'Rounds per item'), idle = trigger(page, 'Idle between rounds')
  await rounds.scrollIntoViewIfNeeded()
  const guard = await controlStability(page, { rounds, idle, learnRounds: section(page).getByRole('button', { name: 'Learn: Rounds per item' }), below: learnBelow(page) })
  const panel = page.getByTestId('delivery-worst')
  await guard.check(async () => {
    await rounds.focus()
    await page.keyboard.press('Enter')
    await expect(panel).toBeVisible()
  })
  await expect(rounds).toHaveAttribute('aria-expanded', 'true')
  await expect(panel.getByRole('heading', { name: 'Worst in this window' })).toBeVisible()
  await expect(panel).toContainText('Rounds per item · last 7 days')
  await expect(panel.getByRole('link')).toHaveCount(5)
  await expect(panel.getByRole('link').first()).toBeFocused()
  await expect(panel.getByRole('link').first()).toHaveAttribute('aria-label', 'AEON-418 Chat composer keeps the draft on reload, 5 rounds. Rounds: failed, changes, merge-forward, changes, passed')
  await expect(panel.getByRole('link').first().locator('svg.rd')).toHaveCount(5)
  await expect(panel).toContainText('Filters apply: type, priority, worked by')
  // Anchored to its tile: below it, or flipped above when there is no room.
  const [tile, box] = [await rounds.boundingBox(), await panel.boundingBox()]
  expect(box!.y >= tile!.y + tile!.height - 1 || box!.y + box!.height <= tile!.y + 1).toBe(true)
  for (const row of await panel.getByRole('link').evaluateAll(links => links.map(link => link.getBoundingClientRect().height))) expect(row).toBeGreaterThanOrEqual(44)
  await page.keyboard.press('ArrowDown')
  await expect(panel.getByRole('link').nth(1)).toBeFocused()
  await page.screenshot({ path: info.outputPath('aeon-1493/building-overlay-1440-light.png') })
  await guard.check(async () => {
    await page.keyboard.press('Escape')
    await expect(panel).toHaveCount(0)
  })
  await expect(rounds).toBeFocused()
  await expect(rounds).toHaveAttribute('aria-expanded', 'false')
  // A click opens it too; a click outside closes it, and hovering or opening another tile moves nothing.
  await guard.check(async () => {
    await idle.hover()
    await idle.click()
    await expect(panel).toBeVisible()
    await expect(panel).toContainText('idle 2 h 10 min')
  })
  await guard.check(async () => {
    // Outside: the next section's heading, which has no action of its own.
    await page.getByRole('heading', { name: 'Making a change ready', level: 3 }).click({ position: { x: 4, y: 4 } })
    await expect(panel).toHaveCount(0)
  })
  guard.done()
})

test('Building it: on a phone the worst five open as a bottom sheet with 44 px targets', async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  await expect(section(page).getByTestId('delivery-building-head')).toHaveText('0 of 3 on target', { timeout: 15_000 })
  const first = trigger(page, 'First-try rate')
  await first.scrollIntoViewIfNeeded()
  await first.click()
  const panel = page.getByTestId('delivery-worst')
  await expect(panel).toBeVisible()
  await expect(panel).toHaveClass(/\bbottom\b/)
  const box = (await panel.boundingBox())!
  expect(Math.abs(box.y + box.height - 844)).toBeLessThanOrEqual(1)
  expect(box.width).toBeGreaterThanOrEqual(389)
  const close = panel.getByRole('button', { name: 'Close' })
  expect((await close.boundingBox())!.height).toBeGreaterThanOrEqual(44)
  for (const row of await panel.getByRole('link').evaluateAll(links => links.map(link => link.getBoundingClientRect().height))) expect(row).toBeGreaterThanOrEqual(44)
  await page.screenshot({ path: info.outputPath('aeon-1493/building-sheet-390-light.png') })
  await close.click()
  await expect(panel).toHaveCount(0)
  await expect(first).toBeFocused()
})

// Tick's 1440 check: flipped above its tile, the worst five covered the app header (Agents, the search). Now it stops
// 8 px below the header; with no room on either side the page first scrolls the tile to 16 px below the header, at
// once when motion is reduced. Every frame from the click until it rests is measured against the header.
type Shot = { hits: string[]; frames: number; scrolls: ScrollToOptions[] }
async function watchHeader(page: Page) {
  await page.evaluate(() => {
    const shot: Shot & { on: boolean } = { hits: [], frames: 0, scrolls: [], on: true }
    ;(window as unknown as { shot: typeof shot }).shot = shot
    const header = document.querySelector('header.app-header')!
    const frame = () => {
      if (!shot.on) return
      const panel = document.querySelector('[data-testid="delivery-worst"]')
      if (panel && getComputedStyle(panel).visibility === 'visible') {
        shot.frames++
        const a = panel.getBoundingClientRect(), b = header.getBoundingClientRect()
        if (a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top) shot.hits.push(`panel ${a.top}–${a.bottom} over header ${b.top}–${b.bottom}`)
      }
      requestAnimationFrame(frame)
    }
    requestAnimationFrame(frame)
  })
}
const stopWatch = (page: Page) => page.evaluate(() => { const shot = (window as unknown as { shot: Shot & { on: boolean } }).shot; shot.on = false; return shot as Shot })
/** Scrolls the page so the tile's top sits at `top` in the viewport, and proves it got there. */
async function placeTile(page: Page, tile: Locator, top: number) {
  await tile.evaluate((el, at) => { const main = document.querySelector('main')!; main.scrollTop += el.getBoundingClientRect().top - at }, top)
  await expect.poll(async () => Math.round((await tile.boundingBox())!.y), { message: 'the page is long enough to place the tile' }).toBe(Math.round(top))
}

test('Building it at 1440, 1024 and 768: the worst five never cover the app header', async ({ page }) => {
  test.setTimeout(90_000)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.addInitScript(() => {
    const scrollTo = Element.prototype.scrollTo
    Element.prototype.scrollTo = function (this: Element, ...args: unknown[]) {
      const shot = (window as unknown as { shot?: Shot }).shot
      if (shot && typeof args[0] === 'object') shot.scrolls.push(args[0] as ScrollToOptions)
      return (scrollTo as (...a: unknown[]) => void).apply(this, args)
    }
  })
  await setup(page, { level: 'expert' })
  await page.goto('/p/AEON/delivery')
  await expect(section(page).getByTestId('delivery-building-head')).toHaveText('0 of 3 on target', { timeout: 15_000 })
  await settledExpert(page)
  const lead = trigger(page, 'Lead time per item'), panel = page.getByTestId('delivery-worst')
  // The tile's top padding: clipped text and the chart answer the pointer themselves.
  const open = async () => { await lead.click({ position: { x: 24, y: 6 } }); await expect(panel).toBeVisible(); await expect(panel.getByRole('link')).toHaveCount(5) }
  const shut = async () => { await page.keyboard.press('Escape'); await expect(panel).toHaveCount(0) }
  for (const width of [1440, 1024, 768] as const) {
    await test.step(`at ${width}`, async () => {
      await page.setViewportSize({ width, height: 900 })
      await page.emulateMedia({ reducedMotion: 'no-preference' })
      const header = (await page.locator('header.app-header').boundingBox())!, headerBottom = header.y + header.height

      // Room below: it opens there, and its height is what the other two cases must make room for.
      await placeTile(page, lead, headerBottom + 16)
      await open()
      const tileHeight = (await lead.boundingBox())!.height, need = (await panel.boundingBox())!.height
      expect((await panel.boundingBox())!.y).toBeGreaterThanOrEqual(headerBottom + 16 + tileHeight)
      await shut()

      // No room below, room above under the header: it flips above and keeps 8 px below the header; the page stays put.
      const low = 900 - 4 - tileHeight
      expect(low - 6 - (headerBottom + 8), 'room above the tile').toBeGreaterThanOrEqual(need)
      await placeTile(page, lead, low)
      await watchHeader(page)
      await open()
      let box = (await panel.boundingBox())!
      expect(box.y + box.height).toBeLessThanOrEqual(low + 1)
      expect(box.y).toBeGreaterThanOrEqual(headerBottom + 8 - .5)
      expect(Math.round((await lead.boundingBox())!.y)).toBe(Math.round(low))
      let shot = await stopWatch(page)
      expect(shot.frames).toBeGreaterThan(0)
      expect(shot.hits).toEqual([])
      expect(shot.scrolls).toEqual([])
      await shut()

      // Room on neither side (Tick's case): the page scrolls the tile to 16 px below the header, then it opens below.
      for (const motion of ['no-preference', 'reduce'] as const) {
        await page.emulateMedia({ reducedMotion: motion })
        const middle = headerBottom + 8 + 6 + Math.round(need / 2)
        await placeTile(page, lead, middle)
        expect(900 - (middle + tileHeight) - 12, 'room below the tile').toBeLessThan(need)
        await watchHeader(page)
        await open()
        const tile = (await lead.boundingBox())!
        box = (await panel.boundingBox())!
        expect(Math.abs(tile.y - (headerBottom + 16))).toBeLessThanOrEqual(1)
        expect(box.y).toBeGreaterThanOrEqual(tile.y + tile.height - 1)
        shot = await stopWatch(page)
        expect(shot.frames).toBeGreaterThan(0)
        expect(shot.hits).toEqual([])
        expect(shot.scrolls.map(call => call.behavior)).toEqual([motion === 'reduce' ? 'instant' : 'smooth'])
        await shut()
      }
    })
  }
})

for (const width of [1440, 390] as const) {
  test(`Building it at ${width}: loading, a failed read, the retry and no data keep every control in place`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const state = await setup(page, { first: 'hold' })
    await page.goto('/p/AEON/delivery')
    const head = section(page).getByTestId('delivery-building-head')
    await expect(section(page).getByRole('listitem')).toHaveCount(4, { timeout: 15_000 })
    await settled(page)
    await expect(section(page).getByRole('listitem').first()).toHaveAttribute('aria-busy', 'true')
    const learnFirst = section(page).getByRole('button', { name: 'Learn: First-try rate' }), learnLead = section(page).getByRole('button', { name: 'Learn: Lead time per item' })
    await learnBelow(page).scrollIntoViewIfNeeded()
    // Measured top-down, so the first name that moves says where a shift starts: above the section, its head, or a tile.
    const ticketsLearn = page.getByTestId('delivery-tickets').getByRole('button', { name: /^Learn: / }).first()
    const title = section(page).getByRole('heading', { name: 'Building it', level: 3 }), sub = section(page).getByText('From the first build to a change that passes its checks', { exact: false })
    const ticketsLastLearn = page.getByTestId('delivery-tickets').getByRole('button', { name: /^Learn: / }).last()
    const guard = await controlStability(page, { ticketsLearn, ticketsLastLearn, title, sub, learnFirst, learnLead, below: learnBelow(page) })
    // Loading → a failed read: "Not loaded" with Retry in the head's own line.
    state.next = 'error'
    await guard.check(async () => {
      state.held.open()
      await expect(section(page).getByRole('alert')).toContainText('Not loaded.')
    })
    await expect(section(page).getByRole('button', { name: 'Worst in this window: First-try rate' })).toBeDisabled()
    // Retry → the answer.
    state.next = 'ready'
    await guard.check(async () => {
      await section(page).getByRole('button', { name: 'Retry' }).click()
      await expect(head).toHaveText('0 of 3 on target')
    })
    // A filter with nothing recorded: the no-data sentence in the same line, no figures.
    state.next = 'empty'
    await guard.check(async () => {
      await page.getByRole('radiogroup', { name: 'Type' }).getByRole('radio', { name: 'Bug' }).click()
      await expect(head).toHaveText('No rounds recorded in this window yet.')
    })
    await expect(section(page).getByRole('listitem').first()).toContainText('No data yet')
    guard.done()
  })
}

test('Building it: the previous worst five stay closed until the new window answers', async ({ page }) => {
  test.setTimeout(90_000)
  await page.setViewportSize({ width: 1440, height: 1000 })
  const work = fixtures()
  work.preferences.theme = { choice: 'light' }
  work.preferences['delivery:numbers'] = { level: 'simple', window: 7 }
  await mockWork(page, work)
  await mockDelivery(page, () => ({ status: 200, body: deliveryMetrics() }))
  const held = gate()
  let hold = false
  await mockBuilding(page, async (projectId, window) => {
    if (hold) await held.wait
    const body = buildingAnswer(projectId, window)
    if (window === 30) for (const metric of body.metrics) if (metric.worst[0]) metric.worst[0].key = 'AEON-930'
    return { status: 200, body }
  })
  await page.goto('/p/AEON/delivery')
  const head = section(page).getByTestId('delivery-building-head')
  await expect(head).toHaveText('0 of 3 on target', { timeout: 15_000 })
  await settled(page)
  const rounds = trigger(page, 'Rounds per item')
  await rounds.scrollIntoViewIfNeeded()
  await expect(rounds).toBeEnabled()
  hold = true
  await page.getByRole('radiogroup', { name: 'Chart window' }).getByRole('radio', { name: '30 days' }).click()
  await expect(rounds).toBeDisabled()
  await expect(page.getByTestId('delivery-worst')).toHaveCount(0)
  await expect(head).toHaveText('0 of 3 on target')
  await rounds.click({ force: true })
  await expect(page.getByTestId('delivery-worst')).toHaveCount(0)
  hold = false
  held.open()
  await expect(rounds).toBeEnabled()
  await rounds.click()
  await expect(page.getByTestId('delivery-worst').getByRole('link').first()).toHaveAttribute('aria-label', /^AEON-930 /)
})
