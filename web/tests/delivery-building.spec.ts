// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1493 (Tick's approved design): Delivery › Numbers › Building it. Risks: the section, its tiles or the Learn
// buttons below move between loading, a failed read, no data and the answer, or when the worst five open and close;
// the whole tile cannot open them from the keyboard, Esc does not return focus, an outside click leaves them open;
// phones get a floating panel instead of a bottom sheet, sideways scroll or targets under 44 px.
import { expect, test, type Page } from '@playwright/test'
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
  const state = { next: options.first ?? 'ready' as Answer, held: gate(), reads: 0 }
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
const learnBelow = (page: Page) => page.getByTestId('delivery-simple').getByRole('button', { name: 'Learn: How long the PR checks take' })

for (const width of [1440, 1024, 390] as const) {
  for (const theme of ['light', 'dark'] as const) {
    test(`Building it at ${width} ${theme}: four tiles, Simple and Expert, in the design's order`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
      await setup(page, { theme })
      await page.goto('/p/AEON/delivery')
      const head = section(page).getByTestId('delivery-building-head')
      await expect(head).toHaveText('0 of 3 on target')
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
    })
  }
}

test('Building it: Enter opens the worst five on the first row, ↓ moves, Esc closes and returns focus; nothing moves', async ({ page }, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  await expect(section(page).getByTestId('delivery-building-head')).toHaveText('0 of 3 on target')
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
    await page.mouse.click(8, 8)
    await expect(panel).toHaveCount(0)
  })
  guard.done()
})

test('Building it: on a phone the worst five open as a bottom sheet with 44 px targets', async ({ page }, info) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await setup(page)
  await page.goto('/p/AEON/delivery')
  await expect(section(page).getByTestId('delivery-building-head')).toHaveText('0 of 3 on target')
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

for (const width of [1440, 390] as const) {
  test(`Building it at ${width}: loading, a failed read, the retry and no data keep every control in place`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 })
    const state = await setup(page, { first: 'hold' })
    await page.goto('/p/AEON/delivery')
    const head = section(page).getByTestId('delivery-building-head')
    await expect(section(page).getByRole('listitem')).toHaveCount(4)
    await expect(section(page).getByRole('listitem').first()).toHaveAttribute('aria-busy', 'true')
    const learnFirst = section(page).getByRole('button', { name: 'Learn: First-try rate' }), learnLead = section(page).getByRole('button', { name: 'Learn: Lead time per item' })
    await learnBelow(page).scrollIntoViewIfNeeded()
    const guard = await controlStability(page, { learnFirst, learnLead, below: learnBelow(page) })
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
